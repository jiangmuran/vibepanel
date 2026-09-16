package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/secret"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The bridge against a fake IM and a fake tmux, with a real database.
//
// What is tested is the product's rules -- who may talk, where words go,
// which keys "allow" is -- rather than any IM's wire format, which each
// adapter's own tests cover against a fake server.

// fakeAdapter records what the bridge sends and lets a test feed it messages.
type fakeAdapter struct {
	kind string
	caps Capabilities
	mu   sync.Mutex
	sent []Outbound
	refs int
	// edits are Edit calls, by ref.
	edits  map[string]Outbound
	images int
	acks   []string
	typing []bool
	sink   Sink
	ready  chan struct{}
}

func newFake(kind string, caps Capabilities) *fakeAdapter {
	return &fakeAdapter{kind: kind, caps: caps, edits: map[string]Outbound{}, ready: make(chan struct{})}
}

func (f *fakeAdapter) Kind() string               { return f.kind }
func (f *fakeAdapter) Capabilities() Capabilities { return f.caps }
func (f *fakeAdapter) Run(ctx context.Context, sink Sink) error {
	f.mu.Lock()
	f.sink = sink
	f.mu.Unlock()
	close(f.ready)
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeAdapter) Send(ctx context.Context, to Peer, m Outbound) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs++
	f.sent = append(f.sent, m)
	return fmt.Sprintf("m%d", f.refs), nil
}
func (f *fakeAdapter) Edit(ctx context.Context, to Peer, ref string, m Outbound) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits[ref] = m
	return nil
}
func (f *fakeAdapter) SendImage(ctx context.Context, to Peer, png []byte, caption string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images++
	f.refs++
	return fmt.Sprintf("m%d", f.refs), nil
}
func (f *fakeAdapter) Typing(ctx context.Context, to Peer, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing = append(f.typing, on)
	return nil
}
func (f *fakeAdapter) Ack(ctx context.Context, a Action, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, text)
	return nil
}

// texts is everything sent, plain text and cards rendered plain, in order.
func (f *fakeAdapter) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		if m.Card != nil {
			out = append(out, RenderPlain(m.Card))
		} else {
			out = append(out, m.Text)
		}
	}
	return out
}

func (f *fakeAdapter) last() string {
	t := f.texts()
	if len(t) == 0 {
		return ""
	}
	return t[len(t)-1]
}

func (f *fakeAdapter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// inbound feeds a message through the sink and waits for the bridge to
// finish with it, which the tests observe through what was sent.
func (f *fakeAdapter) inbound(t *testing.T, in Inbound) {
	t.Helper()
	<-f.ready
	f.mu.Lock()
	s := f.sink
	f.mu.Unlock()
	s.Inbound(context.Background(), in)
}

func (f *fakeAdapter) Health(ok bool, err error)                        {}
func (f *fakeAdapter) State(ctx context.Context, state json.RawMessage) {}

// fakeTerm records pastes and keys per tmux name.
type fakeTerm struct {
	mu         sync.Mutex
	pastes     map[string][]string
	keys       map[string][][]string
	screen     string
	fullscreen bool
}

func newTerm() *fakeTerm {
	return &fakeTerm{pastes: map[string][]string{}, keys: map[string][][]string{}, screen: "$ ls\nmain.go\n\n\n"}
}
func (f *fakeTerm) Paste(ctx context.Context, name, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pastes[name] = append(f.pastes[name], text)
	return nil
}
func (f *fakeTerm) Keys(ctx context.Context, name string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[name] = append(f.keys[name], keys)
	return nil
}
func (f *fakeTerm) Screen(ctx context.Context, name string, ansi bool) (string, error) {
	return f.screen, nil
}
func (f *fakeTerm) Fullscreen(ctx context.Context, name string) bool { return f.fullscreen }

func (f *fakeTerm) pasted(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pastes[name]...)
}
func (f *fakeTerm) pressed(name string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.keys[name]...)
}

// rig is a bridge with one fake channel, one paired peer, and a few sessions.
type rig struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	b     *Bridge
	ad    *fakeAdapter
	term  *fakeTerm
	now   time.Time
	audit []string
	amu   sync.Mutex
}

var rigKinds sync.Mutex

func newRig(t *testing.T, caps Capabilities) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secret.Open(filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	kind := "fake-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
	ad := newFake(kind, caps)
	rigKinds.Lock()
	Register(Factory{Kind: kind, Label: "Fake", New: func(json.RawMessage, Env) (Adapter, error) { return ad, nil }})
	rigKinds.Unlock()
	// The store stamps rows with the real clock, and the bridge's rule that a
	// message must be no older than the state change compares the two, so
	// the rig's clock is the real one, frozen at the start.
	r := &rig{t: t, ctx: ctx, db: db, ad: ad, term: newTerm(), now: time.Now().Truncate(time.Second)}
	prev := DefaultCoalesce
	DefaultCoalesce = 300 * time.Millisecond
	t.Cleanup(func() { DefaultCoalesce = prev })
	r.b = New(Deps{
		DB: db, Term: r.term, Box: box, Now: func() time.Time { return r.now },
		PublicURL: func() string { return "https://panel.test" },
		Shot:      func(string) ([]byte, error) { return []byte("png"), nil },
		Audit: func(_ context.Context, event, detail string) {
			r.amu.Lock()
			r.audit = append(r.audit, event+" "+detail)
			r.amu.Unlock()
		},
		PastedDir: t.TempDir(),
	})
	r.b.Start(ctx)
	if err := r.b.WriteChannel(ctx, kind, true, ChannelConfig{Values: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	<-ad.ready
	return r
}

func (r *rig) peer(id, status, mode string) store.ChatPeer {
	p := store.ChatPeer{Channel: r.ad.kind, PeerID: id, Status: status, Mode: mode, CreatedAt: r.now.Unix(), LastSeenAt: r.now.Unix()}
	if err := r.db.PutChatPeer(r.ctx, p); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) session(id, title, launch string, st session.State) store.Session {
	if _, err := r.db.GetProject(r.ctx, "p1"); err != nil {
		if _, err := r.db.CreateProject(r.ctx, "p1", "vibepanel", r.t.TempDir()); err != nil {
			r.t.Fatal(err)
		}
	}
	s, err := r.db.CreateSession(r.ctx, store.Session{
		ID: id, ProjectID: "p1", TmuxName: "vp_" + id, Title: title, State: st,
		LaunchCommand: []string{launch}, LaunchRecorded: true, Command: launch,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *rig) say(peer, text string) {
	r.ad.inbound(r.t, Inbound{PeerID: peer, Text: text, At: r.now})
	r.settle()
}

// settle waits for the bridge's goroutines to finish what they were handed.
func (r *rig) settle() {
	deadline := time.Now().Add(2 * time.Second)
	last := -1
	for time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
		n := r.ad.count()
		if n == last {
			return
		}
		last = n
	}
}

func (r *rig) waitFor(pred func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("condition not met; sent so far: %q", r.ad.texts())
}

func TestAStrangerGetsAPairingCodeAndNothingElse(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.session("s1", "fix tmux", "claude", session.StateWaiting)
	r.say("stranger", "y")
	if len(r.term.pressed("vp_s1")) != 0 {
		t.Fatal("a stranger's y reached the pane")
	}
	texts := r.ad.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "配对码") {
		t.Fatalf("stranger got %q", texts)
	}
	peers, _ := r.db.ListChatPeers(r.ctx)
	if len(peers) != 1 || peers[0].Status != store.PeerPending || len(peers[0].PairingCode) != 6 {
		t.Fatalf("peer row: %+v", peers)
	}
	// Saying it again within a minute does not repeat the code.
	r.say("stranger", "hello?")
	if r.ad.count() != 1 {
		t.Fatalf("code repeated: %q", r.ad.texts())
	}
	// A wrong code pairs nobody; the right one does.
	if _, err := r.b.Pair(r.ctx, "000000"); err == nil && peers[0].PairingCode != "000000" {
		t.Fatal("a wrong code paired")
	}
	p, err := r.b.Pair(r.ctx, peers[0].PairingCode)
	if err != nil || p.Status != store.PeerPaired {
		t.Fatalf("Pair: %+v %v", p, err)
	}
	r.settle()
	if !strings.Contains(r.ad.last(), "已配对") {
		t.Fatalf("no welcome: %q", r.ad.texts())
	}
	// A stale code (older than the TTL) does not pair.
	r.say("late", "hi")
	peers, _ = r.db.ListChatPeers(r.ctx)
	var late store.ChatPeer
	for _, p := range peers {
		if p.PeerID == "late" {
			late = p
		}
	}
	r.now = r.now.Add(pairingTTL + time.Minute)
	if _, err := r.b.Pair(r.ctx, late.PairingCode); err == nil {
		t.Fatal("an expired code paired")
	}
}

func TestABlockedPeerIsIgnoredSilently(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("bad", store.PeerBlocked, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateWaiting)
	r.say("bad", "y")
	if r.ad.count() != 0 || len(r.term.pressed("vp_s1")) != 0 {
		t.Fatalf("blocked peer got through: %q %v", r.ad.texts(), r.term.pressed("vp_s1"))
	}
}

func TestWordsGoToTheOnlyWaitingSessionWithAReceipt(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix tmux", "claude", session.StateWaiting)
	r.session("s2", "docs", "codex", session.StateDone)
	r.say("me", "please also update the readme")
	if got := r.term.pasted("vp_s1"); len(got) != 1 || got[0] != "please also update the readme" {
		t.Fatalf("pasted %q", got)
	}
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 || keys[0][0] != "Enter" {
		t.Fatalf("submit keys %v", keys)
	}
	if len(r.term.pasted("vp_s2")) != 0 {
		t.Fatal("the done session got the words")
	}
	if !strings.HasPrefix(r.ad.last(), "→ [") || !strings.Contains(r.ad.last(), "已送入") {
		t.Fatalf("receipt: %q", r.ad.last())
	}
	// The receipt names the handle the session got.
	h, _ := r.db.ChatHandle(r.ctx, "s1")
	if !strings.Contains(r.ad.last(), fmt.Sprintf("[%d]", h)) {
		t.Fatalf("receipt %q does not name handle %d", r.ad.last(), h)
	}
}

func TestTwoWaitingSessionsRefuseABareReply(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	p := r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix tmux", "claude", session.StateWaiting)
	r.session("s2", "docs", "codex", session.StateWaiting)
	// Even with a focus.
	p.FocusSession = "s1"
	_ = r.db.PutChatPeer(r.ctx, p)
	r.say("me", "y")
	for _, name := range []string{"vp_s1", "vp_s2"} {
		if len(r.term.pressed(name)) != 0 || len(r.term.pasted(name)) != 0 {
			t.Fatalf("%s received something from an ambiguous y", name)
		}
	}
	if !strings.Contains(r.ad.last(), "都在等你") || !strings.Contains(r.ad.last(), "fix tmux") || !strings.Contains(r.ad.last(), "docs") {
		t.Fatalf("refusal should list the waiting sessions: %q", r.ad.last())
	}
	// With a handle it goes through, and the handle is stripped.
	h2, _ := r.db.ChatHandle(r.ctx, "s2")
	r.say("me", fmt.Sprintf("%d: use sentence case", h2))
	if got := r.term.pasted("vp_s2"); len(got) != 1 || got[0] != "use sentence case" {
		t.Fatalf("pasted %q", got)
	}
}

func TestApproveAndDenyPressTheToolsKeys(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("c", "claude one", "claude", session.StateWaiting)
	hc, _ := r.db.ChatHandle(r.ctx, "c")
	r.say("me", fmt.Sprintf("%d: y", hc))
	if keys := r.term.pressed("vp_c"); len(keys) != 1 || keys[0][0] != "Enter" {
		t.Fatalf("claude approve keys %v", keys)
	}
	if len(r.term.pasted("vp_c")) != 0 {
		t.Fatal("y was pasted as text")
	}
	r.say("me", fmt.Sprintf("[%d] 拒绝", hc))
	if keys := r.term.pressed("vp_c"); len(keys) != 2 || keys[1][0] != "Escape" {
		t.Fatalf("claude deny keys %v", keys)
	}

	r.session("x", "codex one", "codex", session.StateWaiting)
	hx, _ := r.db.ChatHandle(r.ctx, "x")
	r.say("me", fmt.Sprintf("#%d 允许", hx))
	if keys := r.term.pressed("vp_x"); len(keys) != 1 || keys[0][0] != "y" {
		t.Fatalf("codex approve keys %v", keys)
	}

	// A shell has no prompt to answer.
	r.session("sh", "shell", "bash", session.StateWaiting)
	hs, _ := r.db.ChatHandle(r.ctx, "sh")
	r.say("me", fmt.Sprintf("%d: y", hs))
	if len(r.term.pressed("vp_sh")) != 0 || !strings.Contains(r.ad.last(), "shell") {
		t.Fatalf("shell answered: %v %q", r.term.pressed("vp_sh"), r.ad.last())
	}

	// A done session has nothing to approve.
	_ = r.db.SetSessionState(r.ctx, "c", session.StateDone, session.SourceHook)
	r.say("me", fmt.Sprintf("%d: y", hc))
	if keys := r.term.pressed("vp_c"); len(keys) != 2 {
		t.Fatalf("keys pressed on a session that was not waiting: %v", keys)
	}
	if !strings.Contains(r.ad.last(), "没有在等你") {
		t.Fatalf("reply %q", r.ad.last())
	}
}

func TestAWorkingSessionRefusesTextUntilStopped(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("w", "busy", "claude", session.StateWorking)
	hw, _ := r.db.ChatHandle(r.ctx, "w")
	r.say("me", fmt.Sprintf("%d: also fix the docs", hw))
	if len(r.term.pasted("vp_w")) != 0 {
		t.Fatal("text went into a working pane")
	}
	if !strings.Contains(r.ad.last(), "正在工作") {
		t.Fatalf("reply %q", r.ad.last())
	}
	// Stop needs a confirmation, and "ok" presses the interrupt keys.
	r.say("me", fmt.Sprintf("stop %d", hw))
	if len(r.term.pressed("vp_w")) != 0 || !strings.Contains(r.ad.last(), "回复 ok") {
		t.Fatalf("stop without confirm: %v %q", r.term.pressed("vp_w"), r.ad.last())
	}
	r.say("me", "ok")
	if keys := r.term.pressed("vp_w"); len(keys) != 1 || keys[0][0] != "Escape" {
		t.Fatalf("interrupt keys %v", keys)
	}
	// Nothing pending now.
	r.say("me", "ok")
	if !strings.Contains(r.ad.last(), "没有等待确认") {
		t.Fatalf("reply %q", r.ad.last())
	}
	// An expired confirmation is refused.
	r.say("me", fmt.Sprintf("stop %d", hw))
	r.now = r.now.Add(pendingTTL + time.Second)
	r.say("me", "ok")
	if keys := r.term.pressed("vp_w"); len(keys) != 1 {
		t.Fatalf("an expired ok pressed keys: %v", keys)
	}
	// Cancel drops it.
	r.say("me", fmt.Sprintf("stop %d", hw))
	r.say("me", "cancel")
	r.say("me", "ok")
	if keys := r.term.pressed("vp_w"); len(keys) != 1 {
		t.Fatalf("a cancelled stop pressed keys: %v", keys)
	}
}

func TestAStateChangeIsPushedOnceItSettles(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Edit: true, Buttons: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix tmux", "claude", session.StateWorking)
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateWaiting, session.SourceHook)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "s1", Kind: "prompt", Text: "Bash: rm -rf build"})
	// Flicker: several changes inside the coalesce window are one push.
	r.b.SessionChanged(row, session.StateWaiting)
	r.b.SessionSaid(row, store.SessionMessage{})
	r.b.SessionChanged(row, session.StateWaiting)
	time.Sleep(DefaultCoalesce / 2)
	if r.ad.count() != 0 {
		t.Fatalf("pushed before the window closed: %q", r.ad.texts())
	}
	r.waitFor(func() bool { return r.ad.count() == 1 })
	card := r.ad.sent[0]
	if card.Card == nil || card.Card.State != "waiting" || !strings.Contains(card.Card.Body, "rm -rf build") {
		t.Fatalf("card: %+v", card.Card)
	}
	if !strings.Contains(card.Card.StateText, "允许") {
		t.Fatalf("a prompt should read as needing permission: %q", card.Card.StateText)
	}
	if len(card.Buttons) != 3 || card.Buttons[0].Value != "approve:s1" || !card.Buttons[1].Danger {
		t.Fatalf("buttons: %+v", card.Buttons)
	}
	if card.Card.URL != "https://panel.test/?session=s1" {
		t.Fatalf("url %q", card.Card.URL)
	}
	// The message is remembered, so a quote of it routes.
	o, ok, _ := r.db.ChatOutboundByRef(r.ctx, r.ad.kind, "me", "m1")
	if !ok || o.SessionID != "s1" || o.Kind != store.OutboundStatus {
		t.Fatalf("outbound record: %+v %v", o, ok)
	}
	// Working afterwards edits the same message rather than sending another.
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateWorking, session.SourceHook)
	r.b.SessionChanged(row, session.StateWorking)
	r.waitFor(func() bool {
		r.ad.mu.Lock()
		defer r.ad.mu.Unlock()
		e, ok := r.ad.edits["m1"]
		return ok && e.Card != nil && e.Card.State == "working"
	})
	if r.ad.count() != 1 {
		t.Fatalf("working sent a new message: %q", r.ad.texts())
	}
	// Done is a new message.
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateDone, session.SourceHook)
	r.b.SessionChanged(row, session.StateDone)
	r.waitFor(func() bool { return r.ad.count() == 2 })
	if r.ad.sent[1].Card == nil || r.ad.sent[1].Card.State != "done" || len(r.ad.sent[1].Buttons) != 0 {
		t.Fatalf("done card: %+v", r.ad.sent[1])
	}
}

func TestAButtonPressAnswersThePromptAndIsAcknowledged(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Buttons: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "codex", session.StateWaiting)
	r.ad.inbound(t, Inbound{PeerID: "me", Action: &Action{Value: "deny:s1", ID: "cb1"}})
	r.waitFor(func() bool { return len(r.term.pressed("vp_s1")) == 1 })
	if keys := r.term.pressed("vp_s1"); keys[0][0] != "n" {
		t.Fatalf("codex deny keys %v", keys)
	}
	r.ad.mu.Lock()
	acks := r.ad.acks
	r.ad.mu.Unlock()
	if len(acks) != 1 || !strings.Contains(acks[0], "已拒绝") {
		t.Fatalf("acks %q", acks)
	}
	// A press for a session that is no longer waiting presses nothing.
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateDone, session.SourceHook)
	r.ad.inbound(t, Inbound{PeerID: "me", Action: &Action{Value: "approve:s1", ID: "cb2"}})
	r.settle()
	if len(r.term.pressed("vp_s1")) != 1 {
		t.Fatal("a stale approve pressed keys")
	}
}

func TestQuotedRepliesRouteByRefAndByHandleInText(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "one", "claude", session.StateWaiting)
	r.session("s2", "two", "claude", session.StateWaiting)
	_ = r.db.RecordChatOutbound(r.ctx, store.ChatOutbound{Channel: r.ad.kind, PeerID: "me", Ref: "old", SessionID: "s2", Kind: store.OutboundStatus})
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "go ahead", QuotedRef: "old"})
	r.settle()
	if got := r.term.pasted("vp_s2"); len(got) != 1 || got[0] != "go ahead" {
		t.Fatalf("quote by ref: %q", got)
	}
	// 微信 style: only the quoted text, with the card's handle in it.
	h1, _ := r.db.ChatHandle(r.ctx, "s1")
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "y", QuotedText: fmt.Sprintf("▲ [%d] one · vibepanel", h1)})
	r.settle()
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 || keys[0][0] != "Enter" {
		t.Fatalf("quote by text: %v", keys)
	}
}

func TestCommandsListScreenMuteFocusContext(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Images: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix tmux", "claude", session.StateWaiting)
	r.session("s2", "docs", "codex", session.StateDone)
	r.say("me", "list")
	if l := r.ad.last(); !strings.Contains(l, "fix tmux") || !strings.Contains(l, "docs") || !strings.Contains(l, "▲") || !strings.Contains(l, "✓") {
		t.Fatalf("list: %q", l)
	}
	h1, _ := r.db.ChatHandle(r.ctx, "s1")
	h2, _ := r.db.ChatHandle(r.ctx, "s2")

	r.say("me", fmt.Sprintf("screen %d", h2))
	r.ad.mu.Lock()
	last := r.ad.sent[len(r.ad.sent)-1]
	r.ad.mu.Unlock()
	if !strings.Contains(last.Code, "main.go") || strings.HasSuffix(last.Code, "\n") {
		t.Fatalf("screen: %+v", last)
	}

	r.say("me", fmt.Sprintf("shot %d", h2))
	r.ad.mu.Lock()
	images := r.ad.images
	r.ad.mu.Unlock()
	if images != 1 {
		t.Fatalf("shot sent %d images", images)
	}

	r.say("me", fmt.Sprintf("open %d", h2))
	if !strings.Contains(r.ad.last(), "https://panel.test/?session=s2") {
		t.Fatalf("open: %q", r.ad.last())
	}

	r.say("me", fmt.Sprintf("mute %d 30m", h1))
	until, _ := r.db.ChatMutedUntil(r.ctx, r.ad.kind, "me", "s1", r.now.Unix())
	if until != r.now.Add(30*time.Minute).Unix() {
		t.Fatalf("muted until %d", until)
	}
	// A muted session is not pushed.
	row, _ := r.db.GetSession(r.ctx, "s1")
	before := r.ad.count()
	r.b.SessionChanged(row, session.StateWaiting)
	time.Sleep(DefaultCoalesce + 200*time.Millisecond)
	if r.ad.count() != before {
		t.Fatalf("a muted session was pushed: %q", r.ad.texts())
	}
	r.say("me", fmt.Sprintf("unmute %d", h1))
	if until, _ := r.db.ChatMutedUntil(r.ctx, r.ad.kind, "me", "s1", r.now.Unix()); until != 0 {
		t.Fatal("still muted")
	}

	r.say("me", fmt.Sprintf("focus %d", h2))
	p, _ := r.db.GetChatPeer(r.ctx, r.ad.kind, "me")
	if p.FocusSession != "s2" {
		t.Fatalf("focus %q", p.FocusSession)
	}

	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "s2", Kind: "user", Text: "write docs"})
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "s2", Kind: "assistant", Text: "Done, see README."})
	r.say("me", fmt.Sprintf("context %d", h2))
	if l := r.ad.last(); !strings.Contains(l, "▷ write docs") || !strings.Contains(l, "◇ Done, see README.") {
		t.Fatalf("context: %q", l)
	}
	r.say("me", "context 99")
	if !strings.Contains(r.ad.last(), "没有 [99]") {
		t.Fatalf("unknown handle: %q", r.ad.last())
	}
	r.say("me", "help")
	if !strings.Contains(r.ad.last(), "screen 3") {
		t.Fatalf("help: %q", r.ad.last())
	}
	r.say("me", "usage")
	if !strings.Contains(r.ad.last(), "今天") {
		t.Fatalf("usage: %q", r.ad.last())
	}
}

func TestANonProactiveChannelNeedsAHelloFirst(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: false})
	p := r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.b.SessionChanged(row, session.StateWaiting)
	time.Sleep(DefaultCoalesce + 200*time.Millisecond)
	if r.ad.count() != 0 {
		t.Fatalf("pushed with no context token: %q", r.ad.texts())
	}
	h := r.b.Healths(r.ctx)
	if len(h) != 1 || h[0].NeedsHello != 1 {
		t.Fatalf("health: %+v", h)
	}
	// The person says something: the token is kept and the next push goes.
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "list", ContextToken: "tok-1"})
	r.settle()
	p, _ = r.db.GetChatPeer(r.ctx, r.ad.kind, "me")
	if p.ContextToken != "tok-1" {
		t.Fatalf("token not kept: %+v", p)
	}
	before := r.ad.count()
	r.b.SessionChanged(row, session.StateWaiting)
	r.waitFor(func() bool { return r.ad.count() == before+1 })
}

func TestLongBodiesAreCutAndMoreContinues(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateDone)
	long := strings.Repeat("一二三四五六七八九十\n", 300)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "s1", Kind: "assistant", Text: long})
	r.b.SessionChanged(row, session.StateDone)
	r.waitFor(func() bool { return r.ad.count() == 1 })
	body := r.ad.sent[0].Card.Body
	if len([]rune(body)) > BodyLimit+60 || !strings.Contains(body, "回复 more") {
		t.Fatalf("body of %d runes: %q", len([]rune(body)), body[len(body)-80:])
	}
	r.say("me", "more")
	if !strings.HasPrefix(r.ad.last(), "一二三") {
		t.Fatalf("more: %q", r.ad.last()[:40])
	}
	for i := 0; i < 5; i++ {
		r.say("me", "more")
	}
	if !strings.Contains(r.ad.last(), "没有更多") {
		t.Fatalf("after the end: %q", r.ad.last())
	}
}

func TestRoutesDecideWhoIsToldAndTheAuditSaysWhat(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("a", store.PeerPaired, store.ModeNormal)
	r.peer("b", store.PeerPaired, store.ModeNormal)
	raw, _ := json.Marshal(Routes{
		Rules:   []Rule{{ID: "1", Enabled: true, Match: Match{Tools: []string{"codex"}}, To: []string{r.ad.kind + ":b"}, Body: true}},
		Default: Rule{To: []string{"*"}, Match: Match{States: []string{"waiting"}}, Body: false},
	})
	_ = r.db.SetSetting(r.ctx, RoutesKey, string(raw))
	row := r.session("x", "codex one", "codex", session.StateWaiting)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "x", Kind: "assistant", Text: "secret body"})
	r.b.SessionChanged(row, session.StateWaiting)
	r.waitFor(func() bool { return r.ad.count() == 1 })
	if !strings.Contains(r.ad.sent[0].Card.Body, "secret body") {
		t.Fatalf("rule with body: %+v", r.ad.sent[0].Card)
	}
	row2 := r.session("c", "claude one", "claude", session.StateWaiting)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "c", Kind: "assistant", Text: "private"})
	r.b.SessionChanged(row2, session.StateWaiting)
	r.waitFor(func() bool { return r.ad.count() == 3 })
	for _, m := range r.ad.sent[1:] {
		if m.Card.Body != "" {
			t.Fatalf("default without body leaked text: %+v", m.Card)
		}
	}
	h, _ := r.db.ChatHandle(r.ctx, "c")
	r.say("a", fmt.Sprintf("%d: y", h))
	r.amu.Lock()
	defer r.amu.Unlock()
	joined := strings.Join(r.audit, "\n")
	if !strings.Contains(joined, "chat.in") || !strings.Contains(joined, "chat.approved") {
		t.Fatalf("audit: %q", joined)
	}
}

// fakeAssistant answers with what the test set.
type fakeAssistant struct {
	intent Intent
	answer string
	budget float64
	calls  int
}

func (f *fakeAssistant) Translate(ctx context.Context, req AssistantRequest) (Intent, float64, error) {
	f.calls++
	return f.intent, 0.01, nil
}
func (f *fakeAssistant) Ask(ctx context.Context, req AssistantRequest) (AssistantAnswer, error) {
	f.calls++
	return AssistantAnswer{Text: f.answer, CostUSD: 0.02}, nil
}
func (f *fakeAssistant) Budget() float64 { return f.budget }

func TestAdvancedModeSendsThroughAConfirmationAndStaysWithinBudget(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Typing: true})
	fa := &fakeAssistant{budget: 0.025}
	r.b.SetAssistant(fa)
	r.peer("me", store.PeerPaired, store.ModeAdvanced)
	r.session("s1", "fix tmux", "claude", session.StateDone)
	r.session("s2", "docs", "codex", session.StateDone)
	h2, _ := r.db.ChatHandle(r.ctx, "s2")
	fa.intent = Intent{Verb: "send", Handle: h2, Text: "add a changelog entry"}

	// Nothing is waiting and there is no focus, so a sentence reaches the
	// assistant, which decides "send to s2"; that needs an ok.
	r.say("me", "tell the docs one to add a changelog entry")
	if len(r.term.pasted("vp_s2")) != 0 {
		t.Fatal("an assistant send went in without confirmation")
	}
	if !strings.Contains(r.ad.last(), "add a changelog entry") || !strings.Contains(r.ad.last(), "回复 ok") {
		t.Fatalf("confirmation: %q", r.ad.last())
	}
	r.say("me", "ok")
	if got := r.term.pasted("vp_s2"); len(got) != 1 || got[0] != "add a changelog entry" {
		t.Fatalf("after ok: %q", got)
	}
	// The typing indicator was turned on and off around the call.
	r.ad.mu.Lock()
	typing := r.ad.typing
	r.ad.mu.Unlock()
	if len(typing) != 2 || !typing[0] || typing[1] {
		t.Fatalf("typing %v", typing)
	}
	// An explicit handle bypasses the assistant entirely.
	before := fa.calls
	r.say("me", fmt.Sprintf("%d: and a date", h2))
	if fa.calls != before || len(r.term.pasted("vp_s2")) != 2 {
		t.Fatalf("explicit handle went through the assistant: calls %d pasted %q", fa.calls, r.term.pasted("vp_s2"))
	}
	// Ask.
	fa.answer = "s1 finished; s2 is idle."
	r.say("me", "问：现在都在干嘛")
	if r.ad.last() != "s1 finished; s2 is idle." {
		t.Fatalf("ask: %q", r.ad.last())
	}
	// Spend so far: 0.01 + 0.02 = 0.03 > 0.025, so the next call is refused.
	r.say("me", "问：再问一次")
	if !strings.Contains(r.ad.last(), "预算") {
		t.Fatalf("budget: %q", r.ad.last())
	}
	// Normal mode never calls it.
	r.peer("plain", store.PeerPaired, store.ModeNormal)
	before = fa.calls
	r.say("plain", "问：什么情况")
	if fa.calls != before || !strings.Contains(r.ad.last(), "高级模式") {
		t.Fatalf("normal mode reached the assistant: %d %q", fa.calls, r.ad.last())
	}
}

func TestAnInboundPictureLandsNextToTheSessionAsAPath(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	p := r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateDone)
	p.FocusSession = "s1"
	_ = r.db.PutChatPeer(r.ctx, p)
	r.ad.inbound(t, Inbound{PeerID: "me", Image: []byte{0x89, 'P', 'N', 'G', 1, 2, 3}})
	r.settle()
	got := r.term.pasted("vp_s1")
	if len(got) != 1 || !strings.HasSuffix(strings.TrimSpace(got[0]), ".png") || !strings.HasPrefix(got[0], r.b.d.PastedDir) {
		t.Fatalf("pasted %q", got)
	}
	if len(r.term.pressed("vp_s1")) != 0 {
		t.Fatal("a picture's path was submitted; it should be typed and left")
	}
}
