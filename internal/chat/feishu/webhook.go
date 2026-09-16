package feishu

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// The webhook: what 飞书 sends, in the order it is checked.
//
//  1. The body, capped. 飞书's largest event is a rich-text post well under
//     100 KB; anything over a mebibyte is not from 飞书.
//  2. If the body is {"encrypt": ...}: decrypt it. If a key is configured
//     and the body is not encrypted, refuse: every request from 飞书 is
//     encrypted once a key is set, so a plaintext one is somebody trying to
//     walk past the signature.
//  3. The handshake ({"type": "url_verification"}), checked against the
//     Verification Token and answered. It is answered before the signature
//     is looked at because the docs exclude it from signing.
//  4. The signature, when encrypted: hex(sha256(timestamp + nonce + key +
//     raw body)), over the bytes as received, never re-serialised.
//  5. The Verification Token in header.token (v2) or token (v1).
//  6. header.app_id, when present, must be this app's.
//  7. Dispatch on header.event_type, dedupe, respond 200 {} at once, and do
//     anything slow on a goroutine: 飞书 gives the whole callback 3 seconds
//     and retries an event four times on a miss.

// maxBody caps a webhook body.
const maxBody = 1 << 20

// eventMessage and eventCardAction are the two event types handled.
const (
	eventMessage    = "im.message.receive_v1"
	eventCardAction = "card.action.trigger"
)

// WebhookHandler implements chat.WebhookAdapter.
func (a *Adapter) WebhookHandler() http.Handler { return http.HandlerFunc(a.serve) }

// envelopeIn is the union of the three body shapes: the handshake, a v2
// event, and enough of a v1 event to refuse it politely.
type envelopeIn struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Token     string `json:"token"`
	Schema    string `json:"schema"`
	Header    struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Token     string `json:"token"`
		AppID     string `json:"app_id"`
	} `json:"header"`
	Event json.RawMessage `json:"event"`
}

func (a *Adapter) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		a.refuse(w, http.StatusBadRequest, fmt.Errorf("webhook: read: %w", err))
		return
	}
	if len(raw) > maxBody {
		a.refuse(w, http.StatusRequestEntityTooLarge, errors.New("webhook: body over 1 MiB"))
		return
	}
	var probe struct {
		Encrypt string `json:"encrypt"`
	}
	_ = json.Unmarshal(raw, &probe)
	plain := raw
	encrypted := probe.Encrypt != ""
	switch {
	case encrypted && a.encryptKey == "":
		a.refuse(w, http.StatusBadRequest, errors.New("webhook: an encrypted event arrived but no encrypt key is configured"))
		return
	case encrypted:
		plain, err = decrypt(a.encryptKey, probe.Encrypt)
		if err != nil {
			a.refuse(w, http.StatusBadRequest, fmt.Errorf("webhook: %w", err))
			return
		}
	case a.encryptKey != "":
		a.refuse(w, http.StatusUnauthorized, errors.New("webhook: plaintext event refused; an encrypt key is configured"))
		return
	}
	var ev envelopeIn
	if err := json.Unmarshal(plain, &ev); err != nil {
		a.refuse(w, http.StatusBadRequest, fmt.Errorf("webhook: body: %w", err))
		return
	}
	if ev.Type == "url_verification" {
		if !a.tokenOK(ev.Token) {
			a.refuse(w, http.StatusUnauthorized, errors.New("webhook: handshake with a wrong verification token"))
			return
		}
		writeJSON(w, map[string]string{"challenge": ev.Challenge})
		return
	}
	if encrypted && !a.signatureOK(r.Header, raw) {
		a.refuse(w, http.StatusUnauthorized, errors.New("webhook: bad signature"))
		return
	}
	token := ev.Header.Token
	if token == "" {
		token = ev.Token
	}
	if !a.tokenOK(token) {
		a.refuse(w, http.StatusUnauthorized, errors.New("webhook: wrong verification token"))
		return
	}
	if ev.Header.AppID != "" && ev.Header.AppID != a.appID {
		// Somebody else's app, pointed at this URL by mistake. A 200 keeps
		// 飞书 from retrying it four times; the health line says why
		// nothing arrives.
		a.refuse(w, http.StatusOK, fmt.Errorf("webhook: event for app %s, this is %s", ev.Header.AppID, a.appID))
		return
	}
	sink, ctx := a.running()
	if sink == nil {
		// Not running: let 飞书 retry, so the message is not lost across
		// a restart that happened to land between the two.
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	switch ev.Header.EventType {
	case eventMessage:
		a.message(ctx, sink, ev.Header.EventID, ev.Event)
	case eventCardAction:
		a.cardAction(ctx, sink, ev.Header.EventID, ev.Event)
	default:
		a.logf("ignoring event %q", ev.Header.EventType)
	}
	sink.Health(true, nil)
	writeJSON(w, map[string]any{})
}

// refuse answers with status and tells the health line, so a wrong token
// pasted into the console shows up on the settings page rather than as
// silence.
func (a *Adapter) refuse(w http.ResponseWriter, status int, err error) {
	// The door is unauthenticated, so what it refuses is written down at
	// most once every few seconds: the health line and the log should say
	// "somebody is knocking with the wrong token", not fill up with it.
	a.mu.Lock()
	quiet := a.now().Sub(a.lastRefused) < refuseEvery
	if !quiet {
		a.lastRefused = a.now()
	}
	a.mu.Unlock()
	if !quiet {
		if sink, _ := a.running(); sink != nil {
			sink.Health(false, err)
		}
		a.logf("%v", err)
	}
	if status == http.StatusOK {
		writeJSON(w, map[string]any{})
		return
	}
	http.Error(w, err.Error(), status)
}

// refuseEvery is how often a refused request is reported; see refuse.
const refuseEvery = 10 * time.Second

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Adapter) tokenOK(got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(a.verifyToken)) == 1
}

// signatureOK checks X-Lark-Signature over the raw body.
func (a *Adapter) signatureOK(h http.Header, raw []byte) bool {
	ts, nonce, sig := h.Get("X-Lark-Request-Timestamp"), h.Get("X-Lark-Request-Nonce"), h.Get("X-Lark-Signature")
	if ts == "" || nonce == "" || sig == "" {
		return false
	}
	// A signed request is good for five minutes, not forever: the
	// tombstones that stop a redelivery being pasted twice last a day, and
	// a captured request replayed after that would be new again.
	if secs, err := strconv.ParseInt(ts, 10, 64); err != nil || absDuration(a.now().Sub(time.Unix(secs, 0))) > signatureWindow {
		return false
	}
	want := signature(ts, nonce, a.encryptKey, raw)
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(sig)), []byte(want)) == 1
}

// signatureWindow is how far a request's timestamp may be from now.
const signatureWindow = 5 * time.Minute

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func signature(ts, nonce, key string, raw []byte) string {
	h := sha256.New()
	h.Write([]byte(ts))
	h.Write([]byte(nonce))
	h.Write([]byte(key))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// decrypt opens an "encrypt" field: AES-256-CBC with sha256(key), the IV
// in the first 16 bytes, PKCS#7 padding.
func decrypt(key, b64 string) ([]byte, error) {
	buf, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decrypt: base64: %w", err)
	}
	if len(buf) < 2*aes.BlockSize || len(buf)%aes.BlockSize != 0 {
		return nil, errors.New("decrypt: bad length")
	}
	k := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	iv, ct := buf[:aes.BlockSize], buf[aes.BlockSize:]
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pad := int(pt[len(pt)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(pt) {
		return nil, errors.New("decrypt: bad padding")
	}
	for _, b := range pt[len(pt)-pad:] {
		if int(b) != pad {
			return nil, errors.New("decrypt: bad padding")
		}
	}
	return pt[:len(pt)-pad], nil
}

// fresh records ids and reports whether none had been seen. Both the
// message_id and the event_id are recorded: the receive-message page says
// to trust message_id, the overview says event_id, and a redelivery
// carries the same pair, so either one being known is enough to drop it.
func (a *Adapter) fresh(ids ...string) bool {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Sub(a.pruned) > time.Minute {
		for k, t := range a.seen {
			if now.Sub(t) > tombstoneTTL {
				delete(a.seen, k)
			}
		}
		a.pruned = now
	}
	dup := false
	for _, id := range ids {
		if id == "" {
			continue
		}
		if t, ok := a.seen[id]; ok && now.Sub(t) <= tombstoneTTL {
			dup = true
		}
	}
	if dup {
		return false
	}
	for _, id := range ids {
		if id != "" {
			a.seen[id] = now
		}
	}
	return true
}

// receivedMessage is the part of im.message.receive_v1 that is read.
type receivedMessage struct {
	Sender struct {
		SenderID struct {
			OpenID string `json:"open_id"`
		} `json:"sender_id"`
		SenderType string `json:"sender_type"`
	} `json:"sender"`
	Message struct {
		MessageID   string `json:"message_id"`
		ParentID    string `json:"parent_id"`
		CreateTime  string `json:"create_time"`
		ChatType    string `json:"chat_type"`
		MessageType string `json:"message_type"`
		Content     string `json:"content"`
		Mentions    []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"mentions"`
	} `json:"message"`
}

func (a *Adapter) message(ctx context.Context, sink chat.Sink, eventID string, raw json.RawMessage) {
	var ev receivedMessage
	if err := json.Unmarshal(raw, &ev); err != nil {
		a.logf("message event: %v", err)
		return
	}
	m := ev.Message
	if ev.Sender.SenderType != "user" {
		// A bot's own messages come back as events with sender_type bot;
		// answering them is a loop.
		return
	}
	if m.ChatType != "p2p" {
		return
	}
	if m.MessageID == "" || ev.Sender.SenderID.OpenID == "" {
		return
	}
	if !a.fresh("m:"+m.MessageID, "e:"+eventID) {
		return
	}
	in := chat.Inbound{
		PeerID:    ev.Sender.SenderID.OpenID,
		Ref:       m.MessageID,
		QuotedRef: m.ParentID,
		At:        msTime(m.CreateTime, a.now()),
	}
	switch m.MessageType {
	case "text":
		var c struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(m.Content), &c)
		in.Text = c.Text
		for _, mn := range m.Mentions {
			if mn.Key != "" {
				in.Text = strings.ReplaceAll(in.Text, mn.Key, "@"+mn.Name)
			}
		}
	case "post":
		in.Text = flattenPost(m.Content)
	case "image":
		var c struct {
			ImageKey string `json:"image_key"`
		}
		_ = json.Unmarshal([]byte(m.Content), &c)
		if c.ImageKey == "" {
			return
		}
		// Fetched only when the bridge asks (a paired person, a session to
		// go to), on the bridge's goroutine: the 3-second budget here is
		// for the response, and a stranger's picture is never downloaded.
		key := c.ImageKey
		ref := in.Ref
		in.FetchImage = func(ctx context.Context) ([]byte, error) { return a.download(ctx, ref, key, "image") }
	case "audio":
		// No transcription exists; the bridge sees an empty message and
		// says nothing, which is better than pretending to have heard.
	default:
		a.logf("ignoring %s message", m.MessageType)
		return
	}
	sink.Inbound(ctx, in)
}

// flattenPost turns a rich-text post into lines. The received form is
// {"title","content":[[run...]]}; a language-keyed form ({"zh_cn": {...}})
// is accepted too, because the docs show it for sending and say nothing
// about whether a forwarded post keeps it.
func flattenPost(content string) string {
	var direct struct {
		Title   string              `json:"title"`
		Content [][]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &direct); err != nil {
		return ""
	}
	if direct.Content == nil {
		var byLang map[string]json.RawMessage
		if json.Unmarshal([]byte(content), &byLang) != nil {
			return ""
		}
		for _, v := range byLang {
			if json.Unmarshal(v, &direct) == nil && direct.Content != nil {
				break
			}
		}
	}
	var lines []string
	if direct.Title != "" {
		lines = append(lines, direct.Title)
	}
	for _, para := range direct.Content {
		var b strings.Builder
		for _, r := range para {
			var run struct {
				Tag      string `json:"tag"`
				Text     string `json:"text"`
				Href     string `json:"href"`
				UserName string `json:"user_name"`
				UserID   string `json:"user_id"`
			}
			if json.Unmarshal(r, &run) != nil {
				continue
			}
			switch run.Tag {
			case "text", "md":
				b.WriteString(run.Text)
			case "a":
				b.WriteString(run.Text)
				if run.Href != "" && run.Href != run.Text {
					b.WriteString(" (" + run.Href + ")")
				}
			case "at":
				name := run.UserName
				if name == "" {
					name = run.UserID
				}
				b.WriteString("@" + name)
			case "img":
				b.WriteString("[图片]")
			}
		}
		lines = append(lines, b.String())
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// cardActionEvent is the part of card.action.trigger that is read.
type cardActionEvent struct {
	Operator struct {
		OpenID string `json:"open_id"`
	} `json:"operator"`
	Action struct {
		Value json.RawMessage `json:"value"`
	} `json:"action"`
	Context struct {
		OpenMessageID string `json:"open_message_id"`
	} `json:"context"`
}

func (a *Adapter) cardAction(ctx context.Context, sink chat.Sink, eventID string, raw json.RawMessage) {
	var ev cardActionEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		a.logf("card action: %v", err)
		return
	}
	value := actionValue(ev.Action.Value)
	if value == "" || ev.Operator.OpenID == "" {
		return
	}
	// A callback with no event id is still one press on one message, and a
	// second copy of it must not press twice.
	dedupe := "e:" + eventID
	if eventID == "" {
		dedupe = "c:" + ev.Context.OpenMessageID + ":" + ev.Operator.OpenID + ":" + value
	}
	if !a.fresh(dedupe) {
		return
	}
	sink.Inbound(ctx, chat.Inbound{
		PeerID: ev.Operator.OpenID,
		At:     a.now(),
		Action: &chat.Action{Value: value, ID: eventID, MessageRef: ev.Context.OpenMessageID},
	})
}

// actionValue reads the bridge's string back out of a button value: the
// "v" key of the object this adapter put there, or a bare string for a card
// built by hand.
func actionValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m struct {
		V string `json:"v"`
	}
	if json.Unmarshal(raw, &m) == nil {
		return m.V
	}
	return ""
}

// msTime reads 飞书's string millisecond timestamp, falling back to now.
func msTime(s string, now time.Time) time.Time {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms <= 0 {
		return now
	}
	return time.UnixMilli(ms)
}
