package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jiangmuran/vibepanel/internal/secret"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The bridge: sessions on one side, adapters on the other.
//
// Two things flow through it. Changes to sessions come in from the server
// (a state transition, a message a hook carried), are held for a moment so a
// flicker between tool calls is not three pushes, are routed by the rules,
// and go out as one card per paired peer. Messages from people come in from
// the adapters, are checked against the paired peers, parsed, addressed, and
// go into a pane as a paste or a keystroke, or come back as a reply.
//
// Nothing here runs on the poller's goroutine or an HTTP request's. The
// server hands changes over with a non-blocking send and forgets them; a full
// queue drops a change and counts it, which is the same trade the event log
// makes and for the same reason: the panel's idea of what its sessions are
// doing must never wait on a chat app.

// Terminal is what the bridge needs from tmux.
type Terminal interface {
	Paste(ctx context.Context, tmuxName, text string) error
	Keys(ctx context.Context, tmuxName string, keys ...string) error
	// Screen is the visible pane, with SGR sequences when ansi is set.
	Screen(ctx context.Context, tmuxName string, ansi bool) (string, error)
	// Fullscreen reports whether the pane is on its alternate screen, which
	// is what a TUI looks like from outside and what "auto" screenshots key on.
	Fullscreen(ctx context.Context, tmuxName string) bool
}

// Shooter renders a captured screen to a PNG.
type Shooter func(ansi string) ([]byte, error)

// Deps is everything the bridge is given.
type Deps struct {
	DB   *store.DB
	Term Terminal
	Box  *secret.Box
	Log  *slog.Logger
	// PublicURL is the panel's address, for the link on every card.
	PublicURL func() string
	// Zone is the panel's time zone, for quiet hours and the spend day.
	Zone func() *time.Location
	Now  func() time.Time
	HTTP *http.Client
	// Shot is nil when the panel has no renderer; "shot" then says so.
	Shot Shooter
	// Assistant is nil when advanced mode is unavailable.
	Assistant Assistant
	// Audit writes one line to the panel's audit log.
	Audit func(ctx context.Context, event, detail string)
	// PastedDir is where an inbound picture is saved before its path is
	// typed into a session.
	PastedDir string
	// Usage answers the "usage" command with whatever the panel knows about
	// today's tokens; nil means only the bridge's own counters are shown.
	Usage func(ctx context.Context, lang string) string
}

// Health is how one channel is doing, for the settings page.
type Health struct {
	Kind        string `json:"kind"`
	Running     bool   `json:"running"`
	LastOK      int64  `json:"lastOk"`
	LastError   string `json:"lastError"`
	LastErrorAt int64  `json:"lastErrorAt"`
	LastInbound int64  `json:"lastInbound"`
	Received    int64  `json:"received"`
	Sent        int64  `json:"sent"`
	Failed      int64  `json:"failed"`
	// NeedsHello counts pushes dropped because the IM cannot be spoken to
	// until the person says something (微信's context token).
	NeedsHello int64 `json:"needsHello"`
}

type channel struct {
	kind   string
	ad     Adapter
	caps   Capabilities
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	health Health
}

// event is one change the server told the bridge about.
type event struct {
	sessionID string
}

// pending is an action waiting for "ok".
type pending struct {
	describe string
	run      func(ctx context.Context) string
	expires  time.Time
}

// Bridge is the thing. One per panel.
type Bridge struct {
	d Deps

	events  chan event
	dropped atomic.Int64

	mu       sync.Mutex
	chans    map[string]*channel
	timers   map[string]*time.Timer
	holds    map[string]bool
	pend     map[string]pending
	more     map[string]string
	handles  map[string]int
	hello    map[string]time.Time
	lang     string
	stopping bool
	ctx      context.Context
}

// New builds a bridge. Nothing runs until Start.
func New(d Deps) *Bridge {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Zone == nil {
		d.Zone = func() *time.Location { return time.Local }
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.HTTP == nil {
		d.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if d.PublicURL == nil {
		d.PublicURL = func() string { return "" }
	}
	if d.Audit == nil {
		d.Audit = func(context.Context, string, string) {}
	}
	return &Bridge{
		d:       d,
		events:  make(chan event, 256),
		chans:   map[string]*channel{},
		timers:  map[string]*time.Timer{},
		holds:   map[string]bool{},
		pend:    map[string]pending{},
		more:    map[string]string{},
		handles: map[string]int{},
		hello:   map[string]time.Time{},
		lang:    "zh",
	}
}

// Start runs the bridge until ctx ends: the event loop, and every enabled
// channel.
func (b *Bridge) Start(ctx context.Context) {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()
	if raw, err := b.d.DB.GetSetting(ctx, LangKey, ""); err == nil && (raw == "en" || raw == "zh") {
		b.mu.Lock()
		b.lang = raw
		b.mu.Unlock()
	}
	if m, err := b.d.DB.ChatHandles(ctx); err == nil {
		b.mu.Lock()
		b.handles = m
		b.mu.Unlock()
	}
	go b.loop(ctx)
	b.Reload(ctx)
}

// Dropped is how many changes the queue refused.
func (b *Bridge) Dropped() int64 { return b.dropped.Load() }

// SetLang changes the chat language for every peer.
func (b *Bridge) SetLang(lang string) {
	if lang != "en" && lang != "zh" {
		return
	}
	b.mu.Lock()
	b.lang = lang
	b.mu.Unlock()
}

func (b *Bridge) language() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lang
}

// SessionChanged is the server saying a session's state changed. Never
// blocks.
func (b *Bridge) SessionChanged(row store.Session, to session.State) {
	b.enqueue(row.ID)
}

// SessionSaid is the server saying a hook carried a message. Never blocks.
func (b *Bridge) SessionSaid(row store.Session, m store.SessionMessage) {
	b.enqueue(row.ID)
}

func (b *Bridge) enqueue(sessionID string) {
	select {
	case b.events <- event{sessionID: sessionID}:
	default:
		b.dropped.Add(1)
	}
}

func (b *Bridge) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-b.events:
			b.schedule(ctx, ev.sessionID)
		}
	}
}

// schedule (re)arms the session's coalesce timer. A change while the timer
// runs resets it, so what goes out is the state the session settled on.
func (b *Bridge) schedule(ctx context.Context, sessionID string) {
	if len(b.channelsRunning()) == 0 {
		return
	}
	wait := DefaultCoalesce
	if d, ok := b.decide(ctx, sessionID); ok {
		wait = d.Coalesce
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.timers[sessionID]; ok {
		t.Stop()
	}
	b.timers[sessionID] = time.AfterFunc(wait, func() {
		b.mu.Lock()
		delete(b.timers, sessionID)
		b.mu.Unlock()
		b.push(ctx, sessionID)
	})
}

func (b *Bridge) channelsRunning() []*channel {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*channel, 0, len(b.chans))
	for _, c := range b.chans {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].kind < out[j].kind })
	return out
}

func (b *Bridge) channel(kind string) (*channel, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.chans[kind]
	return c, ok
}

// change reads what routing needs to know about a session now.
func (b *Bridge) change(ctx context.Context, sessionID string) (store.Session, Change, store.SessionMessage, bool) {
	row, err := b.d.DB.GetSession(ctx, sessionID)
	if err != nil {
		return store.Session{}, Change{}, store.SessionMessage{}, false
	}
	last, _, _ := b.d.DB.LatestSessionMessage(ctx, sessionID)
	c := Change{
		SessionID: row.ID, ProjectID: row.ProjectID,
		Tool: AgentFor(row.LaunchCommand, row.Command), State: string(row.State),
	}
	// A message older than the state change is not what the session is
	// waiting on; it is what it said last time. Only a fresh one sharpens
	// the state line.
	if last.At >= row.StateChangedAt-1 {
		c.Kind = last.Kind
	} else {
		last = store.SessionMessage{}
	}
	return row, c, last, true
}

func (b *Bridge) decide(ctx context.Context, sessionID string) (Decision, bool) {
	_, c, _, ok := b.change(ctx, sessionID)
	if !ok {
		return Decision{}, false
	}
	raw, _ := b.d.DB.GetSetting(ctx, RoutesKey, "")
	return ParseRoutes(raw).Decide(c, b.d.Now().In(b.d.Zone())), true
}

// push tells every paired peer about a session, as the rules allow.
func (b *Bridge) push(ctx context.Context, sessionID string) {
	row, c, last, ok := b.change(ctx, sessionID)
	if !ok || row.ArchivedAt != nil || row.Scratch {
		return
	}
	raw, _ := b.d.DB.GetSetting(ctx, RoutesKey, "")
	d := ParseRoutes(raw).Decide(c, b.d.Now().In(b.d.Zone()))
	if d.Hold {
		// Inside quiet hours: look again in a while. The timer is the same
		// one a new change would reset, so a session that keeps changing
		// still goes out once when the window ends.
		b.mu.Lock()
		if t, ok := b.timers[sessionID]; ok {
			t.Stop()
		}
		b.timers[sessionID] = time.AfterFunc(5*time.Minute, func() {
			b.mu.Lock()
			delete(b.timers, sessionID)
			b.mu.Unlock()
			b.push(ctx, sessionID)
		})
		b.mu.Unlock()
		return
	}
	peers, err := b.d.DB.PairedChatPeers(ctx)
	if err != nil || len(peers) == 0 {
		return
	}
	handle, err := b.handleOf(ctx, sessionID)
	if err != nil {
		return
	}
	project := ""
	if p, perr := b.d.DB.GetProject(ctx, row.ProjectID); perr == nil {
		project = p.Name
	}
	lang := b.language()
	card := &Card{
		Handle: handle, Title: row.Title, Project: project, State: c.State,
		Glyph: glyph(c.State), StateText: stateText(lang, c.State, c.Kind),
		URL: b.sessionURL(sessionID),
	}
	if row.StateChangedAt > 0 {
		card.Footer = ago(b.d.Now().Sub(time.Unix(row.StateChangedAt, 0)), lang)
	}
	if c.Tool != "shell" {
		card.Footer = strings.TrimPrefix(card.Footer+" · "+c.Tool, " · ")
	}
	body, rest := "", ""
	if d.Body && last.Text != "" {
		var more bool
		body, more = cut(last.Text, BodyLimit)
		if more {
			rest = strings.TrimSpace(strings.TrimPrefix(last.Text, body))
			body += "\n" + msg(lang, "more", len([]rune(rest)))
		}
	}
	card.Body = body

	now := b.d.Now().Unix()
	for _, p := range peers {
		ch, ok := b.channel(p.Channel)
		if !ok {
			continue
		}
		if !d.Send || !destined(d.To, p) {
			// Not a push, but a status message that exists is kept true.
			b.editStatus(ctx, ch, p, sessionID, card)
			continue
		}
		if until, _ := b.d.DB.ChatMutedUntil(ctx, p.Channel, p.PeerID, sessionID, now); until > 0 {
			continue
		}
		if !ch.caps.Proactive && p.ContextToken == "" {
			ch.mu.Lock()
			ch.health.NeedsHello++
			ch.mu.Unlock()
			continue
		}
		if c.State == "working" {
			b.editStatus(ctx, ch, p, sessionID, card)
			continue
		}
		out := Outbound{Card: card}
		if c.Kind == "prompt" && c.State == "waiting" && ch.caps.Buttons {
			out.Buttons = []Button{
				{Label: pick(lang, "允许", "Allow"), Value: "approve:" + sessionID},
				{Label: pick(lang, "拒绝", "Deny"), Value: "deny:" + sessionID, Danger: true},
				{Label: pick(lang, "看屏幕", "Screen"), Value: "screen:" + sessionID},
			}
		}
		ref, err := b.send(ctx, ch, p, out)
		if err != nil {
			b.d.Log.Warn("chat push", "channel", p.Channel, "err", err)
			continue
		}
		if rest != "" {
			b.mu.Lock()
			b.more[chatKey(p.Channel, p.PeerID)] = rest
			b.mu.Unlock()
		}
		_ = b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{
			Channel: p.Channel, PeerID: p.PeerID, Ref: ref, SessionID: sessionID, Kind: store.OutboundStatus,
		})
		if ch.caps.Edit {
			_ = b.d.DB.SetChatStatusRef(ctx, p.Channel, p.PeerID, sessionID, ref)
		}
		// The session a person was just told about is the one a bare
		// reply means, subject to the single-waiting rule at reply time.
		if c.State == "waiting" {
			p.FocusSession = sessionID
			_ = b.d.DB.PutChatPeer(ctx, p)
		}
		if ch.caps.Images && b.d.Shot != nil && wantShot(d.Screenshot, b.d.Term.Fullscreen(ctx, row.TmuxName)) {
			b.sendShot(ctx, ch, p, row, handle)
		}
	}
}

func wantShot(policy string, fullscreen bool) bool {
	switch policy {
	case ShotAlways:
		return true
	case ShotAuto:
		return fullscreen
	}
	return false
}

// destined says whether a rule's To names this peer.
func destined(to []string, p store.ChatPeer) bool {
	for _, t := range to {
		if t == "*" || t == p.Channel+":"+p.PeerID {
			return true
		}
	}
	return false
}

// editStatus updates the session's existing status message, if the adapter
// can and one exists; otherwise nothing, deliberately.
func (b *Bridge) editStatus(ctx context.Context, ch *channel, p store.ChatPeer, sessionID string, card *Card) {
	if !ch.caps.Edit {
		return
	}
	ref, ok, _ := b.d.DB.ChatStatusRef(ctx, p.Channel, p.PeerID, sessionID)
	if !ok {
		return
	}
	if err := ch.ad.Edit(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, ref, Outbound{Card: card}); err != nil {
		// The message may have been deleted or be too old to edit; the
		// next change sends a fresh one.
		_ = b.d.DB.ClearChatStatus(ctx, p.Channel, p.PeerID, sessionID)
	}
}

func (b *Bridge) send(ctx context.Context, ch *channel, p store.ChatPeer, out Outbound) (string, error) {
	ref, err := ch.ad.Send(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, out)
	ch.mu.Lock()
	if err != nil {
		ch.health.Failed++
	} else {
		ch.health.Sent++
	}
	ch.mu.Unlock()
	return ref, err
}

func (b *Bridge) sendShot(ctx context.Context, ch *channel, p store.ChatPeer, row store.Session, handle int) {
	ansi, err := b.d.Term.Screen(ctx, row.TmuxName, true)
	if err != nil {
		return
	}
	png, err := b.d.Shot(ansi)
	if err != nil {
		b.d.Log.Warn("chat screenshot", "err", err)
		return
	}
	ref, err := ch.ad.SendImage(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, png, fmt.Sprintf("[%d] %s", handle, row.Title))
	if err != nil {
		b.d.Log.Warn("chat screenshot send", "channel", p.Channel, "err", err)
		return
	}
	_ = b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{
		Channel: p.Channel, PeerID: p.PeerID, Ref: ref, SessionID: row.ID, Kind: store.OutboundScreen,
	})
}

func (b *Bridge) sessionURL(sessionID string) string {
	base := b.d.PublicURL()
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/?session=" + sessionID
}

// handleOf returns a session's number, assigning one on first use.
func (b *Bridge) handleOf(ctx context.Context, sessionID string) (int, error) {
	b.mu.Lock()
	h, ok := b.handles[sessionID]
	b.mu.Unlock()
	if ok {
		return h, nil
	}
	h, err := b.d.DB.ChatHandle(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	b.handles[sessionID] = h
	b.mu.Unlock()
	return h, nil
}

func chatKey(channel, peer string) string { return channel + ":" + peer }

// ─── channels ─────────────────────────────────────────────────────────────

// channelContext binds a channel's sealed configuration to its kind, so a
// blob copied between rows opens as nothing.
func channelContext(kind string) string { return "chat:" + kind }

// ChannelConfig is a channel's configuration in the clear: the form values
// plus the adapter's own persisted state.
type ChannelConfig struct {
	Values map[string]string `json:"values"`
	State  json.RawMessage   `json:"state,omitempty"`
}

// ReadChannel opens a channel's stored configuration.
func (b *Bridge) ReadChannel(ctx context.Context, kind string) (store.ChatChannel, ChannelConfig, error) {
	row, err := b.d.DB.GetChatChannel(ctx, kind)
	if err != nil {
		return store.ChatChannel{}, ChannelConfig{}, err
	}
	cfg, err := b.open(row)
	return row, cfg, err
}

func (b *Bridge) open(row store.ChatChannel) (ChannelConfig, error) {
	var cfg ChannelConfig
	if b.d.Box == nil {
		return cfg, errors.New("chat: no secret box")
	}
	plain, err := b.d.Box.Unseal(row.ConfigEnc, channelContext(row.Kind))
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Values == nil {
		cfg.Values = map[string]string{}
	}
	return cfg, nil
}

// WriteChannel seals and stores a channel's configuration, then restarts it.
func (b *Bridge) WriteChannel(ctx context.Context, kind string, enabled bool, cfg ChannelConfig) error {
	if _, ok := FactoryFor(kind); !ok {
		return fmt.Errorf("chat: no adapter %q", kind)
	}
	if b.d.Box == nil {
		return errors.New("chat: no secret box")
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := b.d.DB.PutChatChannel(ctx, store.ChatChannel{
		Kind: kind, Enabled: enabled, ConfigEnc: b.d.Box.Seal(plain, channelContext(kind)),
	}); err != nil {
		return err
	}
	b.Reload(ctx)
	return nil
}

// RemoveChannel stops and forgets a channel and its peers.
func (b *Bridge) RemoveChannel(ctx context.Context, kind string) error {
	if err := b.d.DB.DeleteChatChannel(ctx, kind); err != nil {
		return err
	}
	b.Reload(ctx)
	return nil
}

// Reload starts every enabled channel and stops every other one.
//
// Everything is restarted, not only what changed: a channel's configuration
// is one sealed blob, and comparing blobs to decide whether a restart is
// needed is a way to keep an adapter running on a token that was replaced.
func (b *Bridge) Reload(ctx context.Context) {
	b.mu.Lock()
	base := b.ctx
	old := b.chans
	b.chans = map[string]*channel{}
	b.mu.Unlock()
	for _, c := range old {
		c.cancel()
	}
	for _, c := range old {
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
		}
	}
	if base == nil {
		return
	}
	rows, err := b.d.DB.ListChatChannels(ctx)
	if err != nil {
		b.d.Log.Warn("chat channels", "err", err)
		return
	}
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		b.startChannel(base, row)
	}
}

func (b *Bridge) startChannel(base context.Context, row store.ChatChannel) {
	f, ok := FactoryFor(row.Kind)
	if !ok {
		return
	}
	cfg, err := b.open(row)
	if err != nil {
		b.d.Log.Warn("chat channel config", "kind", row.Kind, "err", err)
		return
	}
	values, _ := json.Marshal(cfg.Values)
	ad, err := f.New(values, Env{
		HTTP: b.d.HTTP, State: cfg.State, PublicURL: b.d.PublicURL(),
		Logf: func(format string, args ...any) {
			b.d.Log.Info("chat " + row.Kind + ": " + fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		b.d.Log.Warn("chat channel", "kind", row.Kind, "err", err)
		return
	}
	ctx, cancel := context.WithCancel(base)
	ch := &channel{kind: row.Kind, ad: ad, caps: ad.Capabilities(), cancel: cancel, done: make(chan struct{})}
	ch.health = Health{Kind: row.Kind, Running: true}
	b.mu.Lock()
	b.chans[row.Kind] = ch
	b.mu.Unlock()
	go func() {
		defer close(ch.done)
		err := ad.Run(ctx, &sink{b: b, ch: ch})
		ch.mu.Lock()
		ch.health.Running = false
		if err != nil && !errors.Is(err, context.Canceled) {
			ch.health.LastError = err.Error()
			ch.health.LastErrorAt = b.d.Now().Unix()
		}
		ch.mu.Unlock()
	}()
}

// Healths reports every channel, configured or running.
func (b *Bridge) Healths(ctx context.Context) []Health {
	rows, _ := b.d.DB.ListChatChannels(ctx)
	out := []Health{}
	for _, row := range rows {
		if ch, ok := b.channel(row.Kind); ok {
			ch.mu.Lock()
			out = append(out, ch.health)
			ch.mu.Unlock()
			continue
		}
		out = append(out, Health{Kind: row.Kind})
	}
	return out
}

// Webhook returns the handler for a webhook adapter, or nil.
func (b *Bridge) Webhook(kind string) http.Handler {
	ch, ok := b.channel(kind)
	if !ok {
		return nil
	}
	if w, ok := ch.ad.(WebhookAdapter); ok {
		return w.WebhookHandler()
	}
	return nil
}

// LoginAdapter returns the QR-login adapter for a kind, building one from
// an empty configuration when none is running yet, so sign-in can start
// before anything is stored.
func (b *Bridge) LoginAdapter(kind string) (LoginAdapter, error) {
	if ch, ok := b.channel(kind); ok {
		if l, ok := ch.ad.(LoginAdapter); ok {
			return l, nil
		}
		return nil, fmt.Errorf("chat: %s does not sign in by QR", kind)
	}
	f, ok := FactoryFor(kind)
	if !ok || !f.Login {
		return nil, fmt.Errorf("chat: %s does not sign in by QR", kind)
	}
	ad, err := f.New(json.RawMessage(`{}`), Env{HTTP: b.d.HTTP, PublicURL: b.d.PublicURL(),
		Logf: func(format string, args ...any) { b.d.Log.Info("chat " + kind + ": " + fmt.Sprintf(format, args...)) }})
	if err != nil {
		return nil, err
	}
	l, ok := ad.(LoginAdapter)
	if !ok {
		return nil, fmt.Errorf("chat: %s does not sign in by QR", kind)
	}
	b.mu.Lock()
	// Kept so the status poll finds the same login; replaced by Reload
	// once credentials are stored.
	b.chans[kind] = &channel{kind: kind, ad: ad, caps: ad.Capabilities(), cancel: func() {}, done: closedChan()}
	b.mu.Unlock()
	return l, nil
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// sink is the adapter's way back into the bridge.
type sink struct {
	b  *Bridge
	ch *channel
}

func (s *sink) Inbound(ctx context.Context, in Inbound) {
	s.ch.mu.Lock()
	s.ch.health.Received++
	s.ch.health.LastInbound = s.b.d.Now().Unix()
	s.ch.mu.Unlock()
	in.Channel = s.ch.kind
	go s.b.handle(ctx, s.ch, in)
}

func (s *sink) Health(ok bool, err error) {
	s.ch.mu.Lock()
	defer s.ch.mu.Unlock()
	if ok {
		s.ch.health.LastOK = s.b.d.Now().Unix()
		return
	}
	if err != nil {
		s.ch.health.LastError = err.Error()
	}
	s.ch.health.LastErrorAt = s.b.d.Now().Unix()
}

func (s *sink) State(ctx context.Context, state json.RawMessage) {
	row, cfg, err := s.b.ReadChannel(ctx, s.ch.kind)
	if err != nil {
		return
	}
	cfg.State = state
	plain, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	row.ConfigEnc = s.b.d.Box.Seal(plain, channelContext(row.Kind))
	_ = s.b.d.DB.PutChatChannel(ctx, row)
}
