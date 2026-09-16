// Package telegram is the chat adapter for the Telegram Bot API.
//
// It is the polling kind: a goroutine long-polls getUpdates and hands each
// private-chat message to the bridge, and the other methods are one API call
// each. Nothing here knows what a session is; that is the seam adapter.go
// describes, and it is what keeps this package at one file of protocol.
//
// Private chats only. A bot token can be added to a group by anyone who has
// it, and a group's messages would then arrive here too; they are dropped
// before their text is read, because the alternative is the panel typing a
// stranger's message into someone's terminal.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// Kind is the registry key and the channel name the bridge stores peers under.
const Kind = "telegram"

const (
	defaultAPIBase = "https://api.telegram.org"
	// pollTimeout is what getUpdates is asked to hold the request open for.
	// Telegram's own ceiling is higher, but a poll that outlives a proxy's
	// idle timeout is one that fails every time.
	pollTimeout = 50
	// httpTimeout is the fallback client's timeout, and the floor the
	// polling client is raised to: it has to outlive pollTimeout with room
	// for the round trip, or every long poll ends as a client-side error
	// and the backoff is what the channel page shows.
	httpTimeout = 70 * time.Second
	// maxImage caps a downloaded photo. Telegram's Bot API serves nothing
	// larger, so this is a bound on what a broken server can make the panel
	// hold in memory, not a limit a person will meet.
	maxImage = 20 << 20
	// maxCaption is Telegram's caption limit; maxAck its callback toast's.
	maxCaption = 1024
	maxAck     = 200
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
)

func init() {
	chat.Register(chat.Factory{
		Kind:  Kind,
		Label: "Telegram",
		Fields: []chat.Field{
			{Name: "token", Label: "Bot token", Secret: true, Hint: "From @BotFather"},
		},
		New: New,
	})
}

// config is the sealed channel configuration. apiBase is not in Fields: it
// is how tests point the adapter at a fake, and how someone running a local
// Bot API server would reach it, but it is not something the form asks.
type config struct {
	Token   string `json:"token"`
	APIBase string `json:"apiBase"`
}

// state is what survives a restart: the last update id handed to the bridge,
// so the first poll after a restart asks for what came after it rather than
// for everything Telegram still holds.
type state struct {
	Offset int64 `json:"offset"`
}

// Adapter is one bot.
type Adapter struct {
	token   string
	apiBase string
	http    *http.Client
	// poll is http with a timeout long enough for a long poll; see
	// httpTimeout.
	poll   *http.Client
	logf   func(string, ...any)
	offset int64
	// minWait and maxWait are the poll backoff ladder; fields rather than
	// the constants so a test can run it in milliseconds.
	minWait, maxWait time.Duration
}

// New builds an adapter from {"token": "..."}; see config.
func New(raw json.RawMessage, env chat.Env) (chat.Adapter, error) {
	var cfg config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("telegram: config: %w", err)
		}
	}
	// A token pasted with the "bot" prefix that appears in the API URL is a
	// common slip, and it fails with a 404 that says nothing about why.
	cfg.Token = strings.TrimPrefix(strings.TrimSpace(cfg.Token), "bot")
	if cfg.Token == "" {
		return nil, errors.New("telegram: a bot token is required")
	}
	cfg.APIBase = strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if cfg.APIBase == "" {
		cfg.APIBase = defaultAPIBase
	}
	a := &Adapter{token: cfg.Token, apiBase: cfg.APIBase, http: env.HTTP, logf: env.Logf, minWait: minBackoff, maxWait: maxBackoff}
	if a.http == nil {
		a.http = &http.Client{Timeout: httpTimeout}
	}
	a.poll = a.http
	if a.http.Timeout > 0 && a.http.Timeout < httpTimeout {
		// A shallow copy shares the transport (and so the test's fake and
		// the panel's proxy settings) but not the ceiling.
		c := *a.http
		c.Timeout = httpTimeout
		a.poll = &c
	}
	if len(env.State) > 0 {
		var st state
		// A state that does not parse is a state from another version;
		// starting from nothing costs a replay of what Telegram still
		// holds, which the bridge's pairing gate absorbs.
		if err := json.Unmarshal(env.State, &st); err == nil {
			a.offset = st.Offset
		}
	}
	if a.logf == nil {
		a.logf = func(string, ...any) {}
	}
	return a, nil
}

func (a *Adapter) Kind() string { return Kind }

func (a *Adapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{
		Edit:      true,
		Buttons:   true,
		QuoteRefs: true,
		Proactive: true,
		Flavor:    chat.FlavorHTML,
		MaxText:   4096,
		Images:    true,
		Typing:    true,
		VoiceText: false,
	}
}

// Error is what Telegram said no with. Code is the HTTP-style error_code in
// the body; Description its text, which is the only thing that says why.
type Error struct {
	Code        int
	Description string
	// RetryAfter is set on a 429, in seconds.
	RetryAfter int
}

func (e *Error) Error() string {
	return fmt.Sprintf("telegram: %s (%d)", e.Description, e.Code)
}

// is reports whether err is a Telegram error whose description contains
// needle, case-insensitively. Telegram's descriptions are prose, prefixed
// with "Bad Request: ", and the prose is the only handle there is.
func is(err error, code int, needle string) bool {
	var te *Error
	if !errors.As(err, &te) || te.Code != code {
		return false
	}
	return strings.Contains(strings.ToLower(te.Description), needle)
}

// response is the Bot API's envelope for every method.
type response struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (a *Adapter) url(method string) string {
	return a.apiBase + "/bot" + a.token + "/" + method
}

// call posts params as JSON and decodes result into out, when out is not nil.
func (a *Adapter) call(ctx context.Context, client *http.Client, method string, params any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return a.do(client, method, req, out)
}

func (a *Adapter) do(client *http.Client, method string, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	defer resp.Body.Close()
	// The envelope is small; the bound is against a proxy answering with a
	// login page, not against Telegram.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	var env response
	if err := json.Unmarshal(raw, &env); err != nil {
		// Not the Bot API's envelope: a proxy, a wrong apiBase, a captive
		// portal. The status is the most useful thing to say.
		return &Error{Code: resp.StatusCode, Description: fmt.Sprintf("%s: not a Bot API response (%s)", method, http.StatusText(resp.StatusCode))}
	}
	if !env.OK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		desc := env.Description
		if desc == "" {
			desc = http.StatusText(resp.StatusCode)
		}
		return &Error{Code: code, Description: method + ": " + desc, RetryAfter: env.Parameters.RetryAfter}
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram: %s: result: %w", method, err)
		}
	}
	return nil
}

// sentMessage is the part of a Message a send needs back.
type sentMessage struct {
	MessageID int64 `json:"message_id"`
}

// chatID turns a peer id back into what Telegram wants. Peer ids are the
// decimal chat id and nothing else, so a peer that does not parse is a row
// from some other adapter and is refused rather than sent to chat 0.
func chatID(p chat.Peer) (int64, error) {
	id, err := strconv.ParseInt(p.ID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("telegram: peer %q is not a chat id", p.ID)
	}
	return id, nil
}

// renderHTML draws an Outbound in Telegram's HTML subset. A card goes
// through chat.RenderHTML; loose text is escaped so an agent's "<tool>" does
// not become a rejected entity; code sits in a pre block under it.
func renderHTML(m chat.Outbound) string {
	if m.Card != nil {
		return chat.RenderHTML(m.Card)
	}
	text := html.EscapeString(m.Text)
	if m.Code != "" {
		if text != "" {
			text += "\n"
		}
		text += "<pre>" + html.EscapeString(m.Code) + "</pre>"
	}
	return text
}

// renderPlain is the same message with no markup at all: what is sent when
// Telegram will not parse the HTML, because a lost message is worse than an
// unformatted one.
func renderPlain(m chat.Outbound) string {
	if m.Card != nil {
		return chat.RenderPlain(m.Card)
	}
	text := m.Text
	if m.Code != "" {
		if text != "" {
			text += "\n"
		}
		text += m.Code
	}
	return text
}

// keyboard is an inline keyboard: one row, one button per choice. Telegram
// draws nothing for Danger, so the label carries the meaning on its own,
// which it already has to on the IMs with no buttons at all.
func keyboard(buttons []chat.Button) map[string]any {
	row := make([]map[string]string, 0, len(buttons))
	for _, b := range buttons {
		row = append(row, map[string]string{"text": b.Label, "callback_data": b.Value})
	}
	rows := [][]map[string]string{}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return map[string]any{"inline_keyboard": rows}
}

// Send posts one message and returns its id.
func (a *Adapter) Send(ctx context.Context, to chat.Peer, m chat.Outbound) (string, error) {
	id, err := chatID(to)
	if err != nil {
		return "", err
	}
	params := map[string]any{
		"chat_id": id,
		// A card ends with the panel's URL and Telegram would unfurl it
		// under every status message.
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if len(m.Buttons) > 0 {
		params["reply_markup"] = keyboard(m.Buttons)
	}
	if m.ReplyTo != "" {
		if rid, err := strconv.ParseInt(m.ReplyTo, 10, 64); err == nil {
			// reply_parameters is Bot API 7.0; a local Bot API server can
			// be older and ignores fields it does not know, so the field
			// it does know is sent as well. Telegram reads
			// reply_parameters first when both are present. Either way,
			// a quoted message that has since been deleted must not
			// cost the reply.
			params["reply_parameters"] = map[string]any{"message_id": rid, "allow_sending_without_reply": true}
			params["reply_to_message_id"] = rid
			params["allow_sending_without_reply"] = true
		}
	}
	var sent sentMessage
	err = a.withFallback(ctx, m, params, func(p map[string]any) error {
		return a.call(ctx, a.http, "sendMessage", p, &sent)
	})
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(sent.MessageID, 10), nil
}

// withFallback sends the message as HTML and, if Telegram will not parse
// it, once more as plain text. The HTML here is either RenderHTML's or
// html-escaped, so a rejection means a Telegram rule the renderer does not
// know (a tag it dropped, an entity limit); the message still has to arrive.
func (a *Adapter) withFallback(ctx context.Context, m chat.Outbound, params map[string]any, send func(map[string]any) error) error {
	params["text"] = renderHTML(m)
	params["parse_mode"] = "HTML"
	err := send(params)
	if err == nil || !is(err, 400, "can't parse entities") {
		return err
	}
	a.logf("html rejected, sending plain: %v", err)
	delete(params, "parse_mode")
	params["text"] = renderPlain(m)
	return send(params)
}

// Edit replaces a sent message's text and buttons. The keyboard is always
// sent: an edit with no reply_markup leaves the old buttons on the message,
// so a prompt that has been answered would keep offering Allow and Deny.
func (a *Adapter) Edit(ctx context.Context, to chat.Peer, ref string, m chat.Outbound) error {
	id, err := chatID(to)
	if err != nil {
		return err
	}
	mid, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: ref %q is not a message id", ref)
	}
	params := map[string]any{
		"chat_id":              id,
		"message_id":           mid,
		"reply_markup":         keyboard(m.Buttons),
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	err = a.withFallback(ctx, m, params, func(p map[string]any) error {
		return a.call(ctx, a.http, "editMessageText", p, nil)
	})
	// The bridge edits on every change it saw, and two changes can render
	// the same; Telegram calls that an error and it is not one.
	if is(err, 400, "message is not modified") {
		return nil
	}
	return err
}

// SendImage posts a PNG as a photo, with the caption as plain text.
func (a *Adapter) SendImage(ctx context.Context, to chat.Peer, png []byte, caption string) (string, error) {
	id, err := chatID(to)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", strconv.FormatInt(id, 10))
	if caption != "" {
		_ = w.WriteField("caption", truncate(caption, maxCaption))
	}
	part, err := w.CreateFormFile("photo", "screen.png")
	if err != nil {
		return "", fmt.Errorf("telegram: sendPhoto: %w", err)
	}
	if _, err := part.Write(png); err != nil {
		return "", fmt.Errorf("telegram: sendPhoto: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("telegram: sendPhoto: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url("sendPhoto"), &body)
	if err != nil {
		return "", fmt.Errorf("telegram: sendPhoto: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	var sent sentMessage
	if err := a.do(a.http, "sendPhoto", req, &sent); err != nil {
		return "", err
	}
	return strconv.FormatInt(sent.MessageID, 10), nil
}

// Typing shows "typing…" for a few seconds. Telegram has no way to take it
// back, and it clears on its own or when a message arrives, so off is a
// no-op rather than a request.
func (a *Adapter) Typing(ctx context.Context, to chat.Peer, on bool) error {
	if !on {
		return nil
	}
	id, err := chatID(to)
	if err != nil {
		return err
	}
	return a.call(ctx, a.http, "sendChatAction", map[string]any{"chat_id": id, "action": "typing"}, nil)
}

// Ack answers a callback query. Telegram shows the button as busy until
// this is called, so it is called even with nothing to say.
func (a *Adapter) Ack(ctx context.Context, act chat.Action, text string) error {
	params := map[string]any{"callback_query_id": act.ID}
	if text != "" {
		params["text"] = truncate(text, maxAck)
	}
	return a.call(ctx, a.http, "answerCallbackQuery", params, nil)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
