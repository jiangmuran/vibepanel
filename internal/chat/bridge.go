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
	// Coalesce is how long a change is held before it is sent, when no rule
	// says otherwise; zero takes DefaultCoalesce. A field rather than a
	// package variable so a test can run in milliseconds without writing
	// something a running bridge reads.
	Coalesce time.Duration
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
	// placeholder marks an adapter built only to sign in (LoginAdapter):
	// it is not running, so it counts for nothing and answers no webhook.
	placeholder bool

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
	// handled counts inbound messages fully dealt with, so a test can wait
	// for the goroutine handle() runs on rather than sleeping.
	handled atomic.Int64

	mu      sync.Mutex
	chans   map[string]*channel
	timers  map[string]*time.Timer
	pend    map[string]pending
	more    map[string]string
	handles map[string]int
	// hello is when each stranger was last answered with a code, so a
	// stranger who keeps talking is not answered every time. Pruned when it
	// grows past a few hundred entries, which only a flood produces.
	hello map[string]time.Time
	// peers serialises inbound messages from one person: "stop 3" then "ok"
	// must run in that order, and the confirmation state assumes it. One
	// mutex per person, people independent of each other.
	peers map[string]*sync.Mutex
	// logins are QR sign-ins in progress, by kind, outside chans so a
	// Reload while somebody is scanning does not lose the sign-in.
	logins map[string]LoginAdapter
	// failed is why a configured channel could not be started, by kind, so
	// the page can say more than "not running".
	failed map[string]string
	lang   string
	ctx    context.Context
	// reloadMu serialises Reload, held for the whole of it, starts
	// included: two at once each started the channels the other then
	// forgot, and an adapter with no owner polls the same bot token
	// forever.
	reloadMu sync.Mutex
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
	if d.Coalesce <= 0 {
		d.Coalesce = DefaultCoalesce
	}
	return &Bridge{
		d: d,
		// 256: a change is a few bytes and the drain is a timer arm, so the
		// queue empties faster than any poller fills it; the bound exists so
		// a stuck bridge cannot grow without limit, not to be reached.
		events:  make(chan event, 256),
		chans:   map[string]*channel{},
		timers:  map[string]*time.Timer{},
		pend:    map[string]pending{},
		more:    map[string]string{},
		handles: map[string]int{},
		hello:   map[string]time.Time{},
		peers:   map[string]*sync.Mutex{},
		logins:  map[string]LoginAdapter{},
		failed:  map[string]string{},
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

// SetAssistant swaps the advanced mode's brain, or removes it with nil. The
// settings page rebuilds it when its configuration changes; a call in flight
// finishes on the old one.
func (b *Bridge) SetAssistant(a Assistant) {
	b.mu.Lock()
	b.d.Assistant = a
	b.mu.Unlock()
}

// SetShooter installs or removes the screenshot renderer.
func (b *Bridge) SetShooter(s Shooter) {
	b.mu.Lock()
	b.d.Shot = s
	b.mu.Unlock()
}

func (b *Bridge) assistant() Assistant {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.d.Assistant
}

func (b *Bridge) shooter() Shooter {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.d.Shot
}

// Handled is how many inbound messages have been fully dealt with.
func (b *Bridge) Handled() int64 { return b.handled.Load() }

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

func (b *Bridge) schedule(ctx context.Context, sessionID string) {
	// Armed whether or not a channel is running right now: a Reload empties
	// the channel map for a few seconds, and a session that went waiting
	// during a settings save is still a session somebody wants told about.
	// push finds no channel and does nothing, which costs a timer.
	wait := b.d.Coalesce
	if d, ok := b.decide(ctx, sessionID); ok && d.Coalesce > 0 {
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
		if !c.placeholder {
			out = append(out, c)
		}
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
	if c.Kind = CurrentKind(row, last); c.Kind == "" {
		last = store.SessionMessage{}
	}
	return row, c, last, true
}

// CurrentKind is the kind of the message a session is waiting on, or "" when
// its last message predates the state change and so is what it said last
// time, not what it is asking now.
//
// The slack: the hook handler stores the message before it writes the
// state (recordHookMessage, so the two never arrive in the wrong order),
// and each write takes its own timestamp. Under lock contention the second
// can wait out busy_timeout, five seconds, so the message may be that much
// older than the change it belongs to.
func CurrentKind(row store.Session, last store.SessionMessage) string {
	if last.SessionID == "" || last.At < row.StateChangedAt-messageSlack {
		return ""
	}
	return last.Kind
}

// messageSlack is how much older than the state change a message may be and
// still count as what the session is waiting on; see CurrentKind.
const messageSlack = 6

// Addressable says whether a session is one a chat may list, address or
// answer: it exists, is not a scratch terminal, and its process has not
// gone. Every path that turns a handle or a focus into a pane goes through
// this, so the list, the reply and the tools agree on what a session is.
func Addressable(row store.Session) bool {
	return row.ArchivedAt == nil && !row.Scratch && !row.Exited
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
	// An exited session is still told about: its last "done" is the card
	// that says the process is gone, which is the one worth having.
	if !ok || row.ArchivedAt != nil || row.Scratch {
		return
	}
	raw, _ := b.d.DB.GetSetting(ctx, RoutesKey, "")
	d := ParseRoutes(raw).Decide(c, b.d.Now().In(b.d.Zone()))
	if d.Hold {
		// Inside quiet hours: look again in a while. Five minutes is coarse
		// enough to cost nothing and fine enough that a window ending at
		// 08:00 is honoured by 08:05. The timer is the same one a new change
		// would reset, so a session that keeps changing still goes out once
		// when the window ends.
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
	handle, err := b.Handle(ctx, sessionID)
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
		if !d.Send || !Destined(d.To, p.Channel, p.PeerID) {
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
		if c.State == string(session.StateWorking) {
			b.editStatus(ctx, ch, p, sessionID, card)
			continue
		}
		out := Outbound{Card: card}
		if c.Kind == store.MessagePrompt && c.State == string(session.StateWaiting) && ch.caps.Buttons {
			out.Buttons = []Button{
				{Label: pick(lang, "允许", "Allow"), Value: "approve:" + sessionID},
				{Label: pick(lang, "拒绝", "Deny"), Value: "deny:" + sessionID, Danger: true},
				{Label: pick(lang, "看屏幕", "Screen"), Value: "screen:" + sessionID},
			}
		}
		// The remainder is parked before the card goes out, so "more" typed
		// the instant the card lands finds it.
		b.mu.Lock()
		if rest != "" {
			b.more[chatKey(p.Channel, p.PeerID)] = rest
		} else {
			delete(b.more, chatKey(p.Channel, p.PeerID))
		}
		b.mu.Unlock()
		ref, err := b.send(ctx, ch, p, out)
		if err != nil {
			b.d.Log.Warn("chat push", "channel", p.Channel, "err", err)
			continue
		}
		// A record that fails to write breaks quote routing for this one
		// message and turns the next edit into a fresh send; both are worth
		// a line in the log, neither is worth failing the push.
		if err := b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{
			Channel: p.Channel, PeerID: p.PeerID, Ref: ref, SessionID: sessionID, Kind: store.OutboundStatus,
		}); err != nil {
			b.d.Log.Warn("chat outbound record", "err", err)
		}
		if ch.caps.Edit {
			if err := b.d.DB.SetChatStatusRef(ctx, p.Channel, p.PeerID, sessionID, ref); err != nil {
				b.d.Log.Warn("chat status ref", "err", err)
			}
		}
		// The session a person was just told about is the one a bare
		// reply means, subject to the single-waiting rule at reply time.
		if c.State == string(session.StateWaiting) {
			p.FocusSession = sessionID
			_ = b.d.DB.PutChatPeer(ctx, p)
		}
		if ch.caps.Images && b.shooter() != nil && wantShot(d.Screenshot, b.d.Term.Fullscreen(ctx, row.TmuxName)) {
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
	shot := b.shooter()
	if shot == nil {
		return
	}
	png, err := shot(ansi)
	if err != nil {
		b.d.Log.Warn("chat screenshot", "err", err)
		return
	}
	ref, err := ch.ad.SendImage(ctx, Peer{ID: p.PeerID, ContextToken: p.ContextToken}, png, fmt.Sprintf("[%d] %s", handle, row.Title))
	if err != nil {
		b.d.Log.Warn("chat screenshot send", "channel", p.Channel, "err", err)
		return
	}
	if err := b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{
		Channel: p.Channel, PeerID: p.PeerID, Ref: ref, SessionID: row.ID, Kind: store.OutboundScreen,
	}); err != nil {
		b.d.Log.Warn("chat outbound record", "err", err)
	}
}

func (b *Bridge) sessionURL(sessionID string) string {
	base := b.d.PublicURL()
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/?session=" + sessionID
}

func (b *Bridge) Healths(ctx context.Context) []Health {
	rows, _ := b.d.DB.ListChatChannels(ctx)
	out := []Health{}
	for _, row := range rows {
		if ch, ok := b.channel(row.Kind); ok && !ch.placeholder {
			ch.mu.Lock()
			out = append(out, ch.health)
			ch.mu.Unlock()
			continue
		}
		h := Health{Kind: row.Kind}
		b.mu.Lock()
		if why, ok := b.failed[row.Kind]; ok {
			h.LastError = why
			h.LastErrorAt = b.d.Now().Unix()
		}
		b.mu.Unlock()
		out = append(out, h)
	}
	return out
}

func (b *Bridge) Handle(ctx context.Context, sessionID string) (int, error) {
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

// Preview says what the rules would do about a session right now, for the
// settings page's "why did this go here": the decision, and who would be
// told after the destinations are intersected with the paired peers and
// their mutes -- the same two checks push makes. Nothing is sent.
func (b *Bridge) Preview(ctx context.Context, sessionID string) (Decision, Change, []string, bool) {
	_, c, _, ok := b.change(ctx, sessionID)
	if !ok {
		return Decision{}, Change{}, nil, false
	}
	raw, _ := b.d.DB.GetSetting(ctx, RoutesKey, "")
	d := ParseRoutes(raw).Decide(c, b.d.Now().In(b.d.Zone()))
	who := []string{}
	if d.Send {
		peers, _ := b.d.DB.PairedChatPeers(ctx)
		now := b.d.Now().Unix()
		for _, p := range peers {
			if !Destined(d.To, p.Channel, p.PeerID) {
				continue
			}
			if until, _ := b.d.DB.ChatMutedUntil(ctx, p.Channel, p.PeerID, sessionID, now); until > 0 {
				continue
			}
			who = append(who, chatKey(p.Channel, p.PeerID))
		}
	}
	return d, c, who, true
}

// TestSend sends a line to every paired peer of a channel, or to one, so the
// settings page can prove the channel reaches a phone. Returns how many were
// reached and the first error.
func (b *Bridge) TestSend(ctx context.Context, kind, peerID, text string) (int, error) {
	ch, ok := b.channel(kind)
	if !ok {
		return 0, fmt.Errorf("chat: %s is not running", kind)
	}
	peers, err := b.d.DB.PairedChatPeers(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	var first error
	for _, p := range peers {
		if p.Channel != kind || (peerID != "" && p.PeerID != peerID) {
			continue
		}
		if !ch.caps.Proactive && p.ContextToken == "" {
			if first == nil {
				first = fmt.Errorf("chat: %s cannot be reached until they message the bot", p.PeerID)
			}
			continue
		}
		if _, err := b.send(ctx, ch, p, Outbound{Text: text}); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		sent++
	}
	if sent == 0 && first == nil {
		first = fmt.Errorf("chat: nobody is paired on %s", kind)
	}
	return sent, first
}

// SetPeerMode changes how a paired person's messages are read.
func (b *Bridge) SetPeerMode(ctx context.Context, channel, peerID, mode string) error {
	p, err := b.d.DB.GetChatPeer(ctx, channel, peerID)
	if err != nil {
		return err
	}
	p.Mode = mode
	return b.d.DB.PutChatPeer(ctx, p)
}

// SetPeerStatus pairs or blocks a person from the settings page. The owner
// is signed in, so this needs no code; the code is for the case where the
// owner is not looking at the page when the stranger says hello.
func (b *Bridge) SetPeerStatus(ctx context.Context, channel, peerID, status string) error {
	p, err := b.d.DB.GetChatPeer(ctx, channel, peerID)
	if err != nil {
		return err
	}
	was := p.Status
	p.Status = status
	if status == store.PeerPaired {
		p.PairingCode = ""
	}
	if err := b.d.DB.PutChatPeer(ctx, p); err != nil {
		return err
	}
	b.d.Audit(ctx, "chat.peer", fmt.Sprintf("%s: %s -> %s", chatKey(channel, peerID), was, status))
	if status == store.PeerPaired && was != store.PeerPaired {
		if ch, ok := b.channel(channel); ok {
			b.reply(ctx, ch, p, msg(b.language(), "paired"))
		}
	}
	return nil
}

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
	// The running adapter is stopped before the row is written, not after:
	// its sink persists state by reading and rewriting the row, and a
	// batch landing between the write and the cancel would put the old
	// token back.
	b.stopChannel(kind)
	if err := b.d.DB.PutChatChannel(ctx, store.ChatChannel{
		Kind: kind, Enabled: enabled, ConfigEnc: b.d.Box.Seal(plain, channelContext(kind)),
	}); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.logins, kind)
	b.mu.Unlock()
	b.Reload(ctx)
	return nil
}

// stopChannel cancels one running channel and waits for it.
func (b *Bridge) stopChannel(kind string) {
	b.mu.Lock()
	c, ok := b.chans[kind]
	if ok {
		delete(b.chans, kind)
	}
	b.mu.Unlock()
	if !ok {
		return
	}
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
	}
}

func (b *Bridge) RemoveChannel(ctx context.Context, kind string) error {
	b.stopChannel(kind)
	if err := b.d.DB.DeleteChatChannel(ctx, kind); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.logins, kind)
	delete(b.failed, kind)
	b.mu.Unlock()
	b.Reload(ctx)
	return nil
}

func (b *Bridge) Reload(ctx context.Context) {
	b.reloadMu.Lock()
	defer b.reloadMu.Unlock()
	b.mu.Lock()
	base := b.ctx
	old := b.chans
	b.chans = map[string]*channel{}
	b.failed = map[string]string{}
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
	fail := func(err error) {
		b.d.Log.Warn("chat channel", "kind", row.Kind, "err", err)
		b.mu.Lock()
		b.failed[row.Kind] = err.Error()
		b.mu.Unlock()
	}
	cfg, err := b.open(row)
	if err != nil {
		fail(err)
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
		fail(err)
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
		err := ad.Run(ctx, &sink{b: b, ch: ch, ctx: ctx})
		ch.mu.Lock()
		ch.health.Running = false
		if err != nil && !errors.Is(err, context.Canceled) {
			ch.health.LastError = err.Error()
			ch.health.LastErrorAt = b.d.Now().Unix()
		}
		ch.mu.Unlock()
	}()
}

func (b *Bridge) Webhook(kind string) http.Handler {
	ch, ok := b.channel(kind)
	if !ok || ch.placeholder {
		return nil
	}
	if w, ok := ch.ad.(WebhookAdapter); ok {
		return w.WebhookHandler()
	}
	return nil
}

func (b *Bridge) LoginAdapter(kind string) (LoginAdapter, error) {
	if ch, ok := b.channel(kind); ok {
		if l, ok := ch.ad.(LoginAdapter); ok {
			return l, nil
		}
		return nil, fmt.Errorf("chat: %s does not sign in by QR", kind)
	}
	b.mu.Lock()
	l, ok := b.logins[kind]
	b.mu.Unlock()
	if ok {
		return l, nil
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
	l, ok = ad.(LoginAdapter)
	if !ok {
		return nil, fmt.Errorf("chat: %s does not sign in by QR", kind)
	}
	// Kept apart from the running channels so a Reload while somebody is
	// scanning does not lose the sign-in; WriteChannel drops it once the
	// credentials are stored.
	b.mu.Lock()
	b.logins[kind] = l
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
	// ctx is the channel's own; State stops writing once it is done, so a
	// batch persisted by an adapter being replaced cannot put a config the
	// page just replaced back.
	ctx context.Context
}

func (s *sink) Inbound(ctx context.Context, in Inbound) {
	s.ch.mu.Lock()
	s.ch.health.Received++
	s.ch.health.LastInbound = s.b.d.Now().Unix()
	s.ch.mu.Unlock()
	in.Channel = s.ch.kind
	// On the bridge's context, not the adapter's: a settings save restarts
	// every adapter, and a reply half-way through a paste must finish, not
	// die with the poll loop that delivered it. The channel pointer stays
	// valid for the reply; its adapter answers Send until its own ctx ends.
	bctx := s.b.baseContext()
	go func() {
		// One person at a time, in arrival order; see Bridge.peers.
		mu := s.b.peerLock(chatKey(in.Channel, in.PeerID))
		mu.Lock()
		defer mu.Unlock()
		s.b.handle(bctx, s.ch, in)
		s.b.handled.Add(1)
	}()
}

func (b *Bridge) baseContext() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx == nil {
		return context.Background()
	}
	return b.ctx
}

func (b *Bridge) peerLock(key string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	mu, ok := b.peers[key]
	if !ok {
		mu = &sync.Mutex{}
		b.peers[key] = mu
	}
	return mu
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
	if s.ctx != nil && s.ctx.Err() != nil {
		return
	}
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
