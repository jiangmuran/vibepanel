// Package weixin is the chat adapter for personal WeChat, over the iLink
// bot API (微信 ClawBot).
//
// The IM has no application to register and no token to type: a person
// scans a QR code and the server hands back a bot_token, which lives until
// the server says it does not. Everything after that is long polling and
// one rule that shapes the whole adapter: the bot may only speak to a
// person who has spoken to it, by echoing the context_token that came with
// their last message. The bridge holds that token per peer and hands it
// back on every call; this package never stores one.
package weixin

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// Kind is the registry key and the channel's name in the database.
const Kind = "weixin"

func init() {
	chat.Register(chat.Factory{
		Kind:  Kind,
		Label: "微信",
		Login: true,
		// Filled by the sign-in, never typed; declared so the settings
		// route knows which to withhold (the token) and which to show
		// (who is signed in) without knowing this adapter by name.
		Fields: []chat.Field{
			{Name: "bot_token", Label: "Bot token", Secret: true},
			{Name: "user_id", Label: "User"},
			{Name: "bot_id", Label: "Bot"},
		},
		New: New,
	})
}

// config is the sealed channel configuration: empty before a sign-in, the
// credentials the sign-in produced afterwards. apiBase and cdnBase are
// hidden overrides so tests can point the adapter at a fake.
type config struct {
	BotToken string `json:"bot_token"`
	BaseURL  string `json:"base_url"`
	BotID    string `json:"bot_id"`
	UserID   string `json:"user_id"`
	APIBase  string `json:"apiBase"`
	CDNBase  string `json:"cdnBase"`
}

// persisted is what survives a restart, through Sink.State.
type persisted struct {
	// Cursor is the last non-empty get_updates_buf.
	Cursor string `json:"cursor"`
	// TypingTicket is per user, so the user it was fetched for is kept
	// next to it; a second paired person gets their own fetch rather than
	// the first person's ticket.
	TypingTicket     string `json:"typing_ticket"`
	TypingTicketAt   int64  `json:"typing_ticket_at"`
	TypingTicketUser string `json:"typing_ticket_user,omitempty"`
	// Seen is message_id -> when, for dedupe. Delivery is at-least-once:
	// the same id comes again a second later and again after a stalled
	// turn, and each redelivery would be a second paste into a pane.
	Seen map[string]int64 `json:"seen"`
}

const (
	seenTTL   = 24 * time.Hour
	ticketTTL = 24 * time.Hour
	// ticketRetry is how long an empty ticket (typing unavailable) is
	// believed before asking again; a day without typing over one bad
	// answer is too long.
	ticketRetry = time.Hour
)

// The official client's pacing: how long to wait after a failed poll,
// how much longer after three in a row, how often to repeat "typing" and
// for how long at most. They are per adapter so a test can shorten its own
// without racing another test's goroutines.
const (
	defaultBackoffShort = 2 * time.Second
	defaultBackoffLong  = 30 * time.Second
	defaultKeepalive    = 5 * time.Second
	defaultTypingMax    = 2 * time.Minute
)

// Adapter is one signed-in bot, or one waiting to be signed in.
type Adapter struct {
	c      *client
	qrBase string
	botID  string
	userID string
	logf   func(string, ...any)
	now    func() time.Time

	backoffShort, backoffLong time.Duration
	keepalive, typingMax      time.Duration

	mu    sync.Mutex
	state persisted
	sink  chat.Sink

	loginMu sync.Mutex
	logins  map[string]*login

	typingMu sync.Mutex
	typers   map[string]chan struct{}
}

// New builds the adapter from its configuration. With no bot_token it can
// only sign in; Run refuses to start.
func New(raw json.RawMessage, env chat.Env) (chat.Adapter, error) {
	var cfg config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("weixin: config: %w", err)
		}
	}
	logf := env.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	httpc := env.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 60 * time.Second}
	}
	qrBase := defaultBase
	if cfg.APIBase != "" {
		qrBase = cfg.APIBase
	}
	apiBase := cfg.BaseURL
	if apiBase == "" {
		apiBase = qrBase
	}
	cdn := defaultCDN
	if cfg.CDNBase != "" {
		cdn = cfg.CDNBase
	}
	a := &Adapter{
		c:            &client{http: httpc, base: apiBase, cdn: cdn, token: cfg.BotToken, logf: logf},
		qrBase:       qrBase,
		botID:        cfg.BotID,
		userID:       cfg.UserID,
		logf:         logf,
		now:          time.Now,
		logins:       map[string]*login{},
		backoffShort: defaultBackoffShort, backoffLong: defaultBackoffLong,
		keepalive: defaultKeepalive, typingMax: defaultTypingMax,
		typers: map[string]chan struct{}{},
	}
	if len(env.State) > 0 {
		// A state that does not parse is a state from before the schema
		// changed; starting over costs one redelivered batch, which the
		// bridge's own rules tolerate, and is better than never starting.
		if err := json.Unmarshal(env.State, &a.state); err != nil {
			logf("state unreadable, starting over: %v", err)
			a.state = persisted{}
		}
	}
	if a.state.Seen == nil {
		a.state.Seen = map[string]int64{}
	}
	return a, nil
}

func (a *Adapter) Kind() string { return Kind }

func (a *Adapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{
		Edit: false, Buttons: false, QuoteRefs: false, Proactive: false,
		MaxText: 4000, Images: true, Typing: true,
	}
}

// save persists the state through the sink, pruning what has aged out. It
// is called with a.mu held.
func (a *Adapter) saveLocked(ctx context.Context) {
	cutoff := a.now().Add(-seenTTL).Unix()
	for id, at := range a.state.Seen {
		if at < cutoff {
			delete(a.state.Seen, id)
		}
	}
	if a.sink == nil {
		return
	}
	raw, err := json.Marshal(a.state)
	if err != nil {
		return
	}
	a.sink.State(ctx, raw)
}

// Run is the long poll. It ends only when ctx does or when the server says
// the sign-in is over.
func (a *Adapter) Run(ctx context.Context, sink chat.Sink) error {
	if a.c.token == "" {
		return errors.New("weixin: not signed in; scan the QR code on the settings page")
	}
	a.mu.Lock()
	a.sink = sink
	a.mu.Unlock()
	a.c.notify(ctx, "notifystart")
	defer func() {
		a.stopAllTyping()
		// ctx is over by now; the goodbye gets a moment of its own.
		bye, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		a.c.notify(bye, "notifystop")
	}()

	timeout := longPollTimeout
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.mu.Lock()
		cursor := a.state.Cursor
		a.mu.Unlock()
		res, err := a.c.getUpdates(ctx, cursor, timeout)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if timedOut(err) {
				// The server had nothing within the window; the official
				// client treats this as an empty batch and asks again.
				continue
			}
			var ae *apiError
			if errors.As(err, &ae) && ae.code() == staleToken {
				err = errors.New("weixin: the sign-in has expired (session timeout); scan the QR code again")
				sink.Health(false, err)
				return err
			}
			failures++
			sink.Health(false, err)
			wait := a.backoffShort
			if failures >= 3 {
				wait = a.backoffLong
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		failures = 0
		if res.LongpollingTimeoutMs > 0 {
			timeout = time.Duration(res.LongpollingTimeoutMs) * time.Millisecond
		}
		fresh := a.admit(ctx, res)
		sink.Health(true, nil)
		for _, m := range fresh {
			a.deliver(ctx, sink, m)
		}
	}
}

// admit records the cursor and the ids of this batch and persists both
// before anything is delivered. Persisting afterwards would mean a crash
// mid-batch replays the batch on restart, and the seen map cannot catch
// what was never written.
func (a *Adapter) admit(ctx context.Context, res *updatesResp) []message {
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := false
	if res.GetUpdatesBuf != "" {
		a.state.Cursor = res.GetUpdatesBuf
		changed = true
	}
	var fresh []message
	now := a.now().Unix()
	for _, m := range res.Msgs {
		if m.MessageType != messageFromUser {
			continue
		}
		id := messageRef(m)
		if _, dup := a.state.Seen[id]; dup {
			continue
		}
		a.state.Seen[id] = now
		changed = true
		fresh = append(fresh, m)
	}
	if changed {
		a.saveLocked(ctx)
	}
	return fresh
}

// messageRef is the id a message is deduped and quoted by: message_id,
// else the sender's client_id, else the sequence number.
func messageRef(m message) string {
	switch {
	case m.MessageID != 0:
		return strconv.FormatInt(m.MessageID, 10)
	case m.ClientID != "":
		return m.ClientID
	}
	return "seq-" + strconv.FormatInt(m.Seq, 10)
}

// deliver turns one message into an Inbound. The context token always
// rides along, even on a message with nothing else in it, because it is
// the only thing that lets the bridge answer this person at all.
func (a *Adapter) deliver(ctx context.Context, sink chat.Sink, m message) {
	in := chat.Inbound{
		PeerID:       m.FromUserID,
		Ref:          messageRef(m),
		ContextToken: m.ContextToken,
		At:           time.UnixMilli(m.CreateTimeMs),
	}
	if m.CreateTimeMs == 0 {
		in.At = a.now()
	}
	for _, it := range m.ItemList {
		if it.RefMsg != nil && in.QuotedText == "" {
			in.QuotedText = quoted(it.RefMsg)
		}
		switch it.Type {
		case itemText:
			if in.Text == "" && it.TextItem != nil {
				in.Text = it.TextItem.Text
			}
		case itemVoice:
			if it.VoiceItem != nil && in.Text == "" {
				in.Text = it.VoiceItem.Text
			}
		case itemImage:
			if it.ImageItem != nil && in.FetchImage == nil {
				// Fetched only when the bridge asks: a paired person, a
				// session to go to. A stranger's picture is never
				// downloaded or decrypted.
				item := it.ImageItem
				in.FetchImage = func(ctx context.Context) ([]byte, error) { return a.fetchImage(ctx, item) }
			}
		}
	}
	sink.Inbound(ctx, in)
}

func quoted(r *refMsg) string {
	s := r.Title
	if r.MessageItem != nil && r.MessageItem.TextItem != nil && r.MessageItem.TextItem.Text != "" {
		s += "\n" + r.MessageItem.TextItem.Text
	}
	return strings.TrimSpace(s)
}

func (a *Adapter) fetchImage(ctx context.Context, it *imageItem) ([]byte, error) {
	if it.Media == nil {
		return nil, errors.New("weixin: image item without media")
	}
	key, err := imageKey(it)
	if err != nil {
		return nil, err
	}
	raw, err := a.c.download(ctx, it.Media)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return raw, nil
	}
	return decryptECB(key, raw)
}

// Send sends text. A card renders plain (the client draws **bold** and
// fenced code in place, and nothing else is asked of it); code goes under
// the text in a fence; buttons are dropped, because the bridge already
// says in words what each would have done.
func (a *Adapter) Send(ctx context.Context, to chat.Peer, m chat.Outbound) (string, error) {
	if to.ContextToken == "" {
		return "", fmt.Errorf("weixin: no context token for this person yet: %w", chat.ErrNeedsHello)
	}
	text := m.Text
	if m.Card != nil {
		text = chat.RenderPlain(m.Card)
	}
	if m.Code != "" {
		// No fence: the 微信 client draws one as three literal backticks
		// above and below, which on a phone is two lines of noise around a
		// screen that is already short of room.
		if text != "" {
			text += "\n"
		}
		text += strings.TrimRight(m.Code, "\n")
	}
	text = filterMarkdown(text)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("weixin: nothing to send")
	}
	var ref string
	for _, piece := range chat.Split(text, a.Capabilities().MaxText) {
		r, err := a.c.sendItem(ctx, to.ID, to.ContextToken, item{Type: itemText, TextItem: &textItem{Text: piece}})
		if err != nil {
			return "", needsHello(err)
		}
		ref = r
	}
	return ref, nil
}

// Edit is declared impossible in Capabilities, so the bridge never calls
// it; a message on WeChat is what it was when it was sent.
func (a *Adapter) Edit(ctx context.Context, to chat.Peer, ref string, m chat.Outbound) error {
	return errors.New("weixin: messages cannot be edited")
}

// SendImage encrypts, uploads and sends a picture; the caption goes first
// as its own text message, the way the official client sends a caption.
func (a *Adapter) SendImage(ctx context.Context, to chat.Peer, png []byte, caption string) (string, error) {
	if to.ContextToken == "" {
		return "", fmt.Errorf("weixin: no context token for this person yet: %w", chat.ErrNeedsHello)
	}
	if strings.TrimSpace(caption) != "" {
		if _, err := a.Send(ctx, to, chat.Outbound{Text: caption}); err != nil {
			return "", err
		}
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	var fk [16]byte
	if _, err := rand.Read(fk[:]); err != nil {
		return "", err
	}
	fileKey := hex.EncodeToString(fk[:])
	cipherText, err := encryptECB(key[:], png)
	if err != nil {
		return "", err
	}
	sum := md5.Sum(png)
	var u uploadURLResp
	req := uploadURLReq{
		FileKey: fileKey, MediaType: mediaImage, ToUserID: to.ID,
		RawSize: len(png), RawFileMD5: hex.EncodeToString(sum[:]), FileSize: len(cipherText),
		NoNeedThumb: true, AESKey: hex.EncodeToString(key[:]), BaseInfo: base(),
	}
	if _, err := a.c.post(ctx, "getuploadurl", req, &u, sendTimeout); err != nil {
		return "", err
	}
	param, err := a.c.upload(ctx, u, fileKey, cipherText)
	if err != nil {
		return "", err
	}
	// The outbound key is base64 of the hex string, not of the bytes: the
	// official client encodes it that way and the phone decodes it that
	// way, and a raw-bytes key is an image that shows as broken.
	it := item{Type: itemImage, ImageItem: &imageItem{
		Media:   &cdnMedia{EncryptQueryParam: param, AESKey: base64Hex(key[:]), EncryptType: 1},
		MidSize: len(cipherText),
	}}
	ref, err := a.c.sendItem(ctx, to.ID, to.ContextToken, it)
	return ref, needsHello(err)
}

// Typing shows "对方正在输入" on the phone. On: fetch the ticket if the
// cached one is stale, send status 1 and keep sending it every five
// seconds until off or two minutes, whichever is first. Off: send status 2
// and stop the keepalive.
func (a *Adapter) Typing(ctx context.Context, to chat.Peer, on bool) error {
	if !on {
		ticket, _ := a.cachedTicket(to.ID)
		a.stopTyping(to.ID)
		if ticket == "" {
			return nil
		}
		return a.c.sendTyping(ctx, to.ID, ticket, typingOff)
	}
	ticket, err := a.ticket(ctx, to)
	if err != nil {
		return err
	}
	if ticket == "" {
		return nil
	}
	if err := a.c.sendTyping(ctx, to.ID, ticket, typingOn); err != nil {
		return err
	}
	stop := make(chan struct{})
	a.typingMu.Lock()
	if old, ok := a.typers[to.ID]; ok {
		close(old)
	}
	a.typers[to.ID] = stop
	a.typingMu.Unlock()
	go a.keepTyping(to.ID, ticket, stop)
	return nil
}

func (a *Adapter) keepTyping(user, ticket string, stop chan struct{}) {
	deadline := time.After(a.typingMax)
	tick := time.NewTicker(a.keepalive)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-deadline:
			a.stopTyping(user)
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(context.Background(), quickTimeout)
			_ = a.c.sendTyping(ctx, user, ticket, typingOn)
			cancel()
		}
	}
}

func (a *Adapter) stopTyping(user string) {
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	if stop, ok := a.typers[user]; ok {
		close(stop)
		delete(a.typers, user)
	}
}

func (a *Adapter) stopAllTyping() {
	a.typingMu.Lock()
	defer a.typingMu.Unlock()
	for user, stop := range a.typers {
		close(stop)
		delete(a.typers, user)
	}
}

// cachedTicket returns the stored ticket for a user while it is fresh.
func (a *Adapter) cachedTicket(user string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.TypingTicketUser != user || a.state.TypingTicketAt == 0 {
		return "", false
	}
	age := a.now().Sub(time.Unix(a.state.TypingTicketAt, 0))
	ttl := ticketTTL
	if a.state.TypingTicket == "" {
		ttl = ticketRetry
	}
	if age >= ttl {
		return "", false
	}
	return a.state.TypingTicket, true
}

// ticket is getconfig, once a day per user. The ticket's real lifetime is
// unknown; a day is what the official client assumes.
func (a *Adapter) ticket(ctx context.Context, to chat.Peer) (string, error) {
	if t, ok := a.cachedTicket(to.ID); ok {
		return t, nil
	}
	t, err := a.c.getConfig(ctx, to.ID, to.ContextToken)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.state.TypingTicket = t
	a.state.TypingTicketAt = a.now().Unix()
	a.state.TypingTicketUser = to.ID
	a.saveLocked(ctx)
	a.mu.Unlock()
	return t, nil
}

// Ack has nothing to acknowledge: there are no buttons.
func (a *Adapter) Ack(ctx context.Context, act chat.Action, text string) error { return nil }

// needsHello marks a refused send as one only the person can unblock. ret -2
// "prepare failed" is what the server says for an expired context token and
// for a token whose replies are used up; both clear when the person writes
// again, and the text was already split below the length that also causes
// it, so the bridge's "they have to say something" is the honest reading.
func needsHello(err error) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.code() == -2 {
		return fmt.Errorf("%w: %w", chat.ErrNeedsHello, err)
	}
	return err
}
