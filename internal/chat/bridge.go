package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	// Monitor reads the machine for "系统" and the alerts; nil when the panel
	// has no monitor, and then neither exists.
	Monitor Monitor
	// AlertEvery is how often the alerts look; zero takes DefaultAlertEvery.
	AlertEvery time.Duration
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
	// NeedsHello is how many requests are waiting, right now, for a person
	// this IM cannot speak to until they say something (微信's context
	// token), and WaitingOn who those people are. Current, not a running
	// total: a count that never went down read as five messages still lost
	// after the person had come back and seen them all.
	NeedsHello int64    `json:"needsHello"`
	WaitingOn  []string `json:"waitingOn"`
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
	run      func(ctx context.Context) said
	expires  time.Time
}

// said is a reply and what it showed the person. A reply that carries a
// session's request -- the prompt it is asking, the question -- is recorded
// against that message, so a later bare "y" is an answer to something the
// person has seen and not to whatever the session asked since.
type said struct {
	text  string
	shows []shown
	// ask is the permission request this reply shows, when it shows one
	// that can be answered with buttons.
	ask *shown
}

type shown struct {
	session string
	message int64
}

func plain(text string) said { return said{text: text} }

func showing(text, sessionID string, messageID int64) said {
	return said{text: text, shows: []shown{{session: sessionID, message: messageID}}}
}

// asking is showing a permission request, with its buttons where the IM has
// them.
func asking(text, sessionID string, messageID int64) said {
	s := showing(text, sessionID, messageID)
	s.ask = &s.shows[0]
	return s
}

// requestButtons are allow, deny and screen for one request. The message id
// is on each: the press answers this request, and a press on the card of a
// request since answered is refused rather than allowing the next one.
func requestButtons(sessionID string, messageID int64, lang string) []Button {
	suffix := sessionID + ":" + strconv.FormatInt(messageID, 10)
	return []Button{
		{Label: pick(lang, "允许", "Allow"), Value: "approve:" + suffix},
		{Label: pick(lang, "拒绝", "Deny"), Value: "deny:" + suffix, Danger: true},
		{Label: pick(lang, "看屏幕", "Screen"), Value: "screen:" + suffix},
	}
}

// Bridge is the thing. One per panel.
type Bridge struct {
	d Deps

	events  chan event
	dropped atomic.Int64
	// handled counts inbound messages fully dealt with, so a test can wait
	// for the goroutine handle() runs on rather than sleeping.
	handled atomic.Int64

	mu     sync.Mutex
	chans  map[string]*channel
	timers map[string]*time.Timer
	pend   map[string]pending
	// more holds the rest of a long message per person per session, and
	// moreLast which session a bare "more" continues: two long cards in a
	// row must not make the first one's tail unreachable.
	more     map[string]string
	moreLast map[string]string
	// missed is, per person, the sessions that started waiting while a push
	// could not reach them (微信 before they have spoken, or after the
	// context token ran out). Their next message is answered with those
	// first. In memory: a restart loses it, and the person still has the
	// list command.
	missed map[string]map[string]bool
	// clarify is when the assistant's last question to a person expires:
	// while it stands, a bare "yes" answers the assistant rather than a
	// session.
	clarify map[string]time.Time
	// stopped is when a person last asked to stop something, whether or
	// not it parked a confirmation. An "ok" soon after is them confirming
	// the stop they expect to be asked about, never a yes to a prompt: a
	// stop at a permission prompt answers "it is not working, it is asking",
	// and reading the ok that follows as "allow" allows the thing they were
	// trying to stop.
	stopped map[string]time.Time
	// alarms is the alert state per watched number: cpu, mem, disk.
	alarms map[string]*alarm
	// missedOther counts, per person, pushes that could not reach them
	// about sessions not waiting (a finished turn, an answer given by
	// someone else), so the catch-up can say that more was missed.
	missedOther map[string]int
	handles     map[string]int
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
	if d.AlertEvery <= 0 {
		d.AlertEvery = DefaultAlertEvery
	}
	return &Bridge{
		d: d,
		// 256: a change is a few bytes and the drain is a timer arm, so the
		// queue empties faster than any poller fills it; the bound exists so
		// a stuck bridge cannot grow without limit, not to be reached.
		events:      make(chan event, 256),
		chans:       map[string]*channel{},
		timers:      map[string]*time.Timer{},
		pend:        map[string]pending{},
		more:        map[string]string{},
		moreLast:    map[string]string{},
		missed:      map[string]map[string]bool{},
		clarify:     map[string]time.Time{},
		stopped:     map[string]time.Time{},
		alarms:      map[string]*alarm{},
		missedOther: map[string]int{},
		handles:     map[string]int{},
		hello:       map[string]time.Time{},
		peers:       map[string]*sync.Mutex{},
		logins:      map[string]LoginAdapter{},
		failed:      map[string]string{},
		lang:        "zh",
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
	go b.watch(ctx)
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
	// Before anything else, whatever the rules say about telling anybody:
	// a request that ended at the laptop must not keep its buttons.
	b.sweep(ctx, row)
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
	// A session that has reported messages and stops without a new one is
	// the flicker between two tool calls, not a finished turn: the Stop hook
	// always carries the last message. Only the status message is kept true.
	// A session that never reported anything (no hooks) is told about, since
	// its state is all there is; so is one whose process ended.
	quietDone := false
	if c.State == string(session.StateDone) && c.Kind == "" && !row.Exited {
		if _, any, _ := b.d.DB.LatestSessionMessage(ctx, sessionID); any {
			quietDone = true
		} else {
			// No hooks: the state is all there is, and "done" is worth a
			// card only after work. A session created a moment ago settles
			// into done having done nothing, and six new sessions were six
			// cards saying so.
			// And not in its first minute: a new session's shell starting up
			// reads as a moment of work followed by done.
			quietDone = !b.finishedWork(ctx, row) || b.d.Now().Unix()-row.CreatedAt < 60
		}
	}
	card, rest := b.cardFor(row, c, last, handle, project, d.Body, lang)

	now := b.d.Now().Unix()
	for _, p := range peers {
		ch, ok := b.channel(p.Channel)
		if !ok {
			continue
		}
		if !Destined(d.To, p.Channel, p.PeerID) {
			// A person the rule does not send this to is not shown it by an
			// edit either: editing their old "done" card into "needs your
			// permission" with the command in it told exactly the people a
			// rule excluded.
			continue
		}
		if !d.Send || quietDone {
			// Not a push, but a status message that exists is kept true.
			b.editStatus(ctx, ch, p, sessionID, card)
			continue
		}
		if until, _ := b.d.DB.ChatMutedUntil(ctx, p.Channel, p.PeerID, sessionID, now); until > 0 {
			continue
		}
		if c.State == string(session.StateWorking) {
			b.editStatus(ctx, ch, p, sessionID, card)
			continue
		}
		if !ch.caps.Proactive && p.ContextToken == "" {
			b.undelivered(ctx, ch, p, sessionID, c.State, ErrNeedsHello)
			continue
		}
		if c.State == string(session.StateWaiting) && (c.Kind == store.MessagePrompt || c.Kind == store.MessageQuestion) &&
			last.ID > 0 && b.seen(ctx, p, last.ID) {
			// Already in front of them: a reply showed it while the push
			// was being held, or this is the same request pushed again.
			continue
		}
		if err := b.sendCard(ctx, ch, p, row, c, last, card, rest, lang); err != nil {
			continue
		}
		if ch.caps.Images && b.shooter() != nil && wantShot(d.Screenshot, b.d.Term.Fullscreen(ctx, row.TmuxName)) {
			b.sendShot(ctx, ch, p, row, handle)
		}
	}
}

// cardFor composes a session's card and the part of its message that did not
// fit. body is whether the rules let the message be shown at all.
//
// There is deliberately no focus change anywhere near here. The bridge once
// made the session it had just told a person about their focus, and a person
// who had said "focus 2" found their next bare sentence typed into whichever
// session happened to push last.
func (b *Bridge) cardFor(row store.Session, c Change, last store.SessionMessage, handle int, project string, body bool, lang string) (*Card, string) {
	card := &Card{
		Handle: handle, Title: row.Title, Project: project, State: c.State,
		Glyph: glyph(c.State), StateText: stateText(lang, c.State, c.Kind),
		URL: b.sessionURL(row.ID), LinkLabel: msg(lang, "linkLabel"),
	}
	if row.StateChangedAt > 0 {
		card.Footer = ago(b.d.Now().Sub(time.Unix(row.StateChangedAt, 0)), lang)
	}
	if c.Tool != "shell" {
		card.Footer = strings.TrimPrefix(card.Footer+" · "+c.Tool, " · ")
	}
	rest := ""
	if body && last.Text != "" {
		// A finished turn is a summary to glance at; a request is something
		// to read before answering, so it gets the room.
		limit := BodyLimit
		if c.State == string(session.StateDone) {
			limit = doneBodyLimit
		}
		head, more := cut(last.Text, limit)
		if more {
			rest = strings.TrimSpace(strings.TrimPrefix(last.Text, head))
			head += "\n" + msg(lang, "more", len([]rune(rest)), handle)
		}
		card.Body = head
	}
	return card, rest
}

// finishedWork says whether a session came to done from working, by the
// transition log.
func (b *Bridge) finishedWork(ctx context.Context, row store.Session) bool {
	evs, err := b.d.DB.RecentSessionEvents(ctx, row.StateChangedAt-messageSlack, 1, store.EventScope{SessionID: row.ID})
	return err == nil && len(evs) > 0 && evs[0].From == session.StateWorking
}

// doneBodyLimit is how much of a finished turn a card carries.
const doneBodyLimit = 400

// sendCard sends one session's card to one person and records what it showed.
func (b *Bridge) sendCard(ctx context.Context, ch *channel, p store.ChatPeer, row store.Session, c Change, last store.SessionMessage, card *Card, rest, lang string) error {
	sessionID := row.ID
	// The message the card shows, when it shows one. A card without the
	// body shows the person that something is asked, not what, and "allow"
	// to it must not count as having read the request.
	var shownID int64
	if card.Body != "" {
		shownID = last.ID
	}
	// A copy per person: the hint depends on what this person's IM can do.
	shownCard := *card
	out := Outbound{Card: &shownCard}
	kind := store.OutboundStatus
	waiting := c.State == string(session.StateWaiting)
	switch {
	case waiting && c.Kind == store.MessagePrompt && ch.caps.Buttons:
		out.Buttons = requestButtons(sessionID, last.ID, lang)
		kind = store.OutboundRequest
		shownID = last.ID
	case waiting && c.Kind == store.MessagePrompt:
		// No buttons, so the card says what to type. A first-time 微信 user
		// otherwise learns it only by answering wrong once.
		shownCard.Hint = msg(lang, "hintPrompt", card.Handle, card.Handle)
	case waiting && c.Kind == store.MessageQuestion:
		shownCard.Hint = msg(lang, "hintQuestion", card.Handle)
	}
	// The remainder is parked before the card goes out, so "more" typed
	// the instant the card lands finds it.
	b.park(chatKey(p.Channel, p.PeerID), sessionID, rest)
	ref, err := b.send(ctx, ch, p, out)
	if err != nil {
		b.undelivered(ctx, ch, p, sessionID, c.State, err)
		return err
	}
	// A record that fails to write breaks quote routing for this one
	// message and turns the next edit into a fresh send; both are worth
	// a line in the log, neither is worth failing the push.
	if err := b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{
		Channel: p.Channel, PeerID: p.PeerID, Ref: ref, SessionID: sessionID, Kind: kind, MessageID: shownID,
	}); err != nil {
		b.d.Log.Warn("chat outbound record", "err", err)
	}
	if ch.caps.Edit {
		if err := b.d.DB.SetChatStatusRef(ctx, p.Channel, p.PeerID, sessionID, ref); err != nil {
			b.d.Log.Warn("chat status ref", "err", err)
		}
	}
	return nil
}

// park keeps the rest of a long message for "more".
func (b *Bridge) park(key, sessionID, rest string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rest == "" {
		delete(b.more, key+"|"+sessionID)
		return
	}
	b.more[key+"|"+sessionID] = rest
	b.moreLast[key] = sessionID
}

// undelivered records a push that did not reach a person. A waiting session
// is remembered for their next message; the audit log says it happened,
// because a request nobody saw is the failure this feature exists to
// prevent.
func (b *Bridge) undelivered(ctx context.Context, ch *channel, p store.ChatPeer, sessionID, state string, err error) {
	if !errors.Is(err, ErrNeedsHello) {
		b.d.Log.Warn("chat push", "channel", p.Channel, "err", err)
	}
	key := chatKey(p.Channel, p.PeerID)
	if state != string(session.StateWaiting) {
		b.mu.Lock()
		b.missedOther[key]++
		b.mu.Unlock()
		return
	}
	b.mu.Lock()
	if b.missed[key] == nil {
		b.missed[key] = map[string]bool{}
	}
	first := !b.missed[key][sessionID]
	b.missed[key][sessionID] = true
	b.mu.Unlock()
	if first {
		h, _ := b.handleIfAny(sessionID)
		why := err.Error()
		if errors.Is(err, ErrNeedsHello) {
			why = "until they write"
		}
		b.d.Audit(ctx, "chat.undelivered", fmt.Sprintf("[%d] %s: %s", h, who(p), why))
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
	defer ch.mu.Unlock()
	if err == nil {
		ch.health.Sent++
		return ref, nil
	}
	// The page shows the last error next to the counters: "12 failed" with
	// nothing to say why is a number nobody can act on.
	if errors.Is(err, ErrNeedsHello) {
		// Not a failure of the channel: the IM is waiting on the person,
		// which the page says by name. Painted as an error it read as a
		// broken channel on a day nothing was wrong.
		return ref, err
	}
	ch.health.Failed++
	ch.health.LastError = err.Error()
	ch.health.LastErrorAt = b.d.Now().Unix()
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
	if base == "" || !reachable(base) {
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
			h := ch.health
			ch.mu.Unlock()
			h.NeedsHello, h.WaitingOn = b.owed(ctx, row.Kind)
			out = append(out, h)
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

// owed counts the missed requests still waiting on the people of one
// channel, and names those people.
func (b *Bridge) owed(ctx context.Context, kind string) (int64, []string) {
	b.mu.Lock()
	missed := map[string][]string{}
	for key, ids := range b.missed {
		if !strings.HasPrefix(key, kind+":") {
			continue
		}
		for id := range ids {
			missed[key] = append(missed[key], id)
		}
	}
	b.mu.Unlock()
	// Requests, not deliveries: one request held for two people is one.
	requests := map[string]bool{}
	names := []string{}
	for key, ids := range missed {
		waiting := false
		for _, id := range ids {
			if row, err := b.d.DB.GetSession(ctx, id); err == nil && Addressable(row) && row.State == session.StateWaiting {
				requests[id], waiting = true, true
			}
		}
		if !waiting {
			continue
		}
		id := strings.TrimPrefix(key, kind+":")
		p, err := b.d.DB.GetChatPeer(ctx, kind, id)
		if err != nil {
			p = store.ChatPeer{PeerID: id}
		}
		names = append(names, who(p))
	}
	sort.Strings(names)
	return int64(len(requests)), names
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
	d, who := b.previewChange(ctx, c)
	return d, c, who, true
}

// PreviewRequest says what the rules would do if the session asked for
// permission now. A preview of a session as it stands answered about its
// last state, and a rule for permission requests read as though it did not
// apply to the very session it was written for.
func (b *Bridge) PreviewRequest(ctx context.Context, sessionID string) (Decision, []string, bool) {
	_, c, _, ok := b.change(ctx, sessionID)
	if !ok {
		return Decision{}, nil, false
	}
	c.State, c.Kind = string(session.StateWaiting), store.MessagePrompt
	d, who := b.previewChange(ctx, c)
	return d, who, true
}

func (b *Bridge) previewChange(ctx context.Context, c Change) (Decision, []string) {
	sessionID := c.SessionID
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
	return d, who
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

// SetPeerMode changes how a paired person's messages are read. Only a paired
// person has a mode: one set on a stranger was carried into the pairing.
func (b *Bridge) SetPeerMode(ctx context.Context, channel, peerID, mode string) error {
	p, err := b.d.DB.GetChatPeer(ctx, channel, peerID)
	if err != nil {
		return err
	}
	if p.Status != store.PeerPaired {
		return ErrPairByCode
	}
	p.Mode = mode
	return b.d.DB.PutChatPeer(ctx, p)
}

// SetPeerDisplay names a person. 微信 gives no name, and two paired people
// who are both "o9cq…@im.wechat" cannot be told apart on the page, in the
// log, or in "allowed by".
func (b *Bridge) SetPeerDisplay(ctx context.Context, channel, peerID, name string) error {
	p, err := b.d.DB.GetChatPeer(ctx, channel, peerID)
	if err != nil {
		return err
	}
	p.Display = name
	return b.d.DB.PutChatPeer(ctx, p)
}

// ErrPairByCode refuses pairing a pending person without their code.
var ErrPairByCode = errors.New("chat: a pending person is paired with their code")

// SetPeerStatus blocks a person from the settings page.
//
// It never pairs anybody. The owner being signed in proves who is at the
// page, not who is on the other end of the chat: anyone who finds the bot
// gets a pending row, and pairing the newest one pairs whoever wrote last.
// The code the person reads out is what ties the row to them, so that is the
// only way to paired. That includes from blocked: "unblock" paired a
// stranger who had been blocked while pending, in two clicks. Unblocking is
// removing the row; the person's next message starts over with a code.
func (b *Bridge) SetPeerStatus(ctx context.Context, channel, peerID, status string) error {
	p, err := b.d.DB.GetChatPeer(ctx, channel, peerID)
	if err != nil {
		return err
	}
	was := p.Status
	if status == store.PeerPaired && was != store.PeerPaired {
		return ErrPairByCode
	}
	p.Status = status
	if err := b.d.DB.PutChatPeer(ctx, p); err != nil {
		return err
	}
	b.d.Audit(ctx, "chat.peer", fmt.Sprintf("%s (%s): %s -> %s", who(p), channel, was, status))
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

// ErrChannelConfig is a configuration the adapter itself refuses.
var ErrChannelConfig = errors.New("chat: the channel's settings are not usable")

// configError carries the adapter's own words about what is wrong, which is
// what the page shows; Is makes it an ErrChannelConfig.
type configError struct{ err error }

func (e *configError) Error() string        { return e.err.Error() }
func (e *configError) Unwrap() error        { return e.err }
func (e *configError) Is(target error) bool { return target == ErrChannelConfig }

func (b *Bridge) WriteChannel(ctx context.Context, kind string, enabled bool, cfg ChannelConfig) error {
	f, ok := FactoryFor(kind)
	if !ok {
		return fmt.Errorf("chat: no adapter %q", kind)
	}
	if enabled {
		// Asked before anything is stored: an empty 飞书 form was saved,
		// answered "saved", switched on, and then sat on the page as an
		// error the person had just been told was not one. Only what the
		// adapter can tell from the values; a token that is well formed and
		// wrong is still found when the channel runs.
		values, _ := json.Marshal(cfg.Values)
		if _, err := f.New(values, Env{HTTP: b.d.HTTP, State: cfg.State, PublicURL: b.d.PublicURL()}); err != nil {
			return &configError{err: err}
		}
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
	if raw, err := b.d.DB.GetSetting(base, lastInboundKey(row.Kind), ""); err == nil {
		ch.health.LastInbound, _ = strconv.ParseInt(raw, 10, 64)
	}
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
	at := s.b.d.Now().Unix()
	s.ch.mu.Lock()
	s.ch.health.Received++
	s.ch.health.LastInbound = at
	s.ch.mu.Unlock()
	in.Channel = s.ch.kind
	// Kept across restarts: "last message 3 minutes ago" is how the owner
	// tells a quiet channel from a dead one, and a restart must not make
	// every channel look like it never heard anything.
	_ = s.b.d.DB.SetSetting(ctx, lastInboundKey(s.ch.kind), strconv.FormatInt(at, 10))
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

// reachable says whether an address can be opened from a phone at all. A
// panel reached as localhost puts a link on every card that opens nothing,
// which reads as the panel being broken.
func reachable(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
}

func lastInboundKey(kind string) string { return "chat.lastInbound." + kind }
