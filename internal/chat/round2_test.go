package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// What the second round of people found, each as the smallest conversation
// that shows it.

// Allowed at the laptop, the card on the phone kept its buttons and still
// said it needed permission.
func TestARequestAnsweredAtTheLaptopLosesItsButtons(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Buttons: true, Edit: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: deploy to prod")
	r.pushed(row, session.StateWaiting, 1)
	if open, _ := r.db.OpenChatRequests(r.ctx, "s1"); len(open) != 1 {
		t.Fatalf("the card is an open request: %+v", open)
	}
	// Allowed at the laptop, and the session asks the next thing: a new
	// card, and the old one must stop offering the first request.
	r.prompt("s1", "Bash: rm -rf dist")
	r.pushed(row, session.StateWaiting, 2)
	r.waitFor(func() bool {
		open, _ := r.db.OpenChatRequests(r.ctx, "s1")
		return len(open) == 1 && open[0].Ref == "m2"
	})
	r.ad.mu.Lock()
	defer r.ad.mu.Unlock()
	e, ok := r.ad.edits["m1"]
	if !ok || e.Card == nil || len(e.Buttons) != 0 || !strings.Contains(e.Card.StateText, "电脑上处理") || !strings.Contains(e.Card.Body, "deploy to prod") {
		t.Fatalf("the old card was not closed: %+v", e)
	}
	if _, edited := r.ad.edits["m2"]; edited {
		t.Fatal("the current request's card was closed")
	}
}

// Sentences follow the focus while something else waits, and the receipt
// says who is still waiting; "不切了" clears the focus.
func TestTheFocusHoldsWhileSomethingWaitsAndCanBeCleared(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "asks", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: rm -rf node_modules")
	r.session("s2", "docs", "claude", session.StateDone)
	h1, _ := r.db.ChatHandle(r.ctx, "s1")
	h2, _ := r.db.ChatHandle(r.ctx, "s2")
	r.say("me", fmt.Sprintf("切到 %d", h2))
	r.say("me", "fix typo in CONTRIBUTING")
	if got := r.term.pasted("vp_s2"); len(got) != 1 {
		t.Fatalf("a sentence with a focus went elsewhere: %q, reply %q", got, r.ad.last())
	}
	if !strings.Contains(r.ad.last(), fmt.Sprintf("[%d] 还在等你", h1)) {
		t.Fatalf("the receipt should say who is still waiting: %q", r.ad.last())
	}
	// A bare yes is still for the one that asked, not the focus.
	r.say("me", "不切了")
	if p, _ := r.db.GetChatPeer(r.ctx, r.ad.kind, "me"); p.FocusSession != "" {
		t.Fatalf("focus not cleared: %q", p.FocusSession)
	}
	if len(r.term.pasted("vp_s2")) != 1 {
		t.Fatal("不切了 was delivered as words")
	}
}

// Quoting a long card and saying "more" continues that card.
func TestQuotingALongCardContinuesThatOne(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	long := func(word string) string { return strings.Repeat(word+" ", 400) }
	a := r.session("a", "a", "claude", session.StateDone)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "a", Kind: "assistant", Text: long("alpha")})
	r.pushed(a, session.StateDone, 1)
	b := r.session("b", "b", "claude", session.StateDone)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "b", Kind: "assistant", Text: long("beta")})
	r.pushed(b, session.StateDone, 2)
	before := r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "更多", QuotedRef: "m1"})
	r.settleFrom(before)
	if !strings.Contains(r.ad.last(), "alpha") {
		t.Fatalf("quoted more: %q", r.ad.last())
	}
}

// A press that did what it said leaves the card to say so.
func TestAPressOnAnEditableCardSendsNoExtraMessage(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Buttons: true, Edit: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "codex", session.StateWaiting)
	id := r.prompt("s1", "shell: make")
	r.pushed(row, session.StateWaiting, 1)
	r.press(fmt.Sprintf("approve:s1:%d", id), "m1")
	if len(r.term.pressed("vp_s1")) != 1 || r.ad.count() != 1 {
		t.Fatalf("keys %v, sent %q", r.term.pressed("vp_s1"), r.ad.texts())
	}
}

// "嗯嗯" is a breath, not a task.
func TestASoundIsNotATask(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateDone)
	h, _ := r.db.ChatHandle(r.ctx, "s1")
	r.say("me", fmt.Sprintf("切到 %d", h))
	sent := r.ad.count()
	r.say("me", "嗯嗯。")
	if len(r.term.pasted("vp_s1")) != 0 || r.ad.count() != sent {
		t.Fatalf("a sound was delivered or answered: %q %q", r.term.pasted("vp_s1"), r.ad.texts())
	}
	// And a command that did not parse is not a task either.
	r.say("me", "静音一下那个测试的")
	if len(r.term.pasted("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "没听懂") {
		t.Fatalf("an unclear command: %q %q", r.term.pasted("vp_s1"), r.ad.last())
	}
	// Words keep their own punctuation.
	r.say("me", fmt.Sprintf("%d号，嗯，选A吧，小改就行。", h))
	if got := r.term.pasted("vp_s1"); len(got) != 1 || got[0] != "选A吧，小改就行。" {
		t.Fatalf("delivered %q", got)
	}
}

// A quote by ref of a message about no session, and a yes: not answered for
// whoever happens to be waiting.
func TestAQuotedRefAboutNothingIsNotAYesForSomebody(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: ls")
	r.say("me", "列表") // shown
	r.say("me", "帮助") // m2, about nothing
	before := r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "y", QuotedRef: "m2"})
	r.settleFrom(before)
	if len(r.term.pressed("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "说不清") {
		t.Fatalf("keys %v, reply %q", r.term.pressed("vp_s1"), r.ad.last())
	}
}

// "Not done yet, here it is" carries the buttons the card would have.
func TestAShownRequestCarriesItsButtons(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Buttons: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "codex", session.StateWaiting)
	id := r.prompt("s1", "shell: make")
	r.say("me", "y")
	r.ad.mu.Lock()
	last := r.ad.sent[len(r.ad.sent)-1]
	r.ad.mu.Unlock()
	if len(last.Buttons) != 3 || last.Buttons[0].Value != fmt.Sprintf("approve:s1:%d", id) {
		t.Fatalf("buttons on the shown request: %+v", last)
	}
	o, _, _ := r.db.ChatOutboundByRef(r.ctx, r.ad.kind, "me", "m1")
	if o.Kind != store.OutboundRequest || o.MessageID != id {
		t.Fatalf("recorded as %+v", o)
	}
}

// Nobody is paired from blocked; unblocking is removing the row.
func TestNobodyIsPairedFromBlocked(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("x", store.PeerBlocked, store.ModeNormal)
	if err := r.b.SetPeerStatus(r.ctx, r.ad.kind, "x", store.PeerPaired); err != ErrPairByCode {
		t.Fatalf("blocked to paired: %v", err)
	}
	r.peer("y", store.PeerPending, store.ModeNormal)
	if err := r.b.SetPeerMode(r.ctx, r.ad.kind, "y", store.ModeAdvanced); err != ErrPairByCode {
		t.Fatalf("a mode for a stranger: %v", err)
	}
}

// What is owed is what still waits: a request answered at the laptop while
// the person was away is not counted as held for them.
func TestOwedCountsOnlyWhatStillWaits(t *testing.T) {
	r := newRig(t, Capabilities{})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: ls")
	r.b.SessionChanged(row, session.StateWaiting)
	r.waitFor(func() bool { return r.b.Healths(r.ctx)[0].NeedsHello == 1 })
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateDone, session.SourceHook)
	if h := r.b.Healths(r.ctx)[0]; h.NeedsHello != 0 || len(h.WaitingOn) != 0 {
		t.Fatalf("owed after it stopped waiting: %+v", h)
	}
}

// Narrow, then broad: the old card's words contain the new request's.
func TestQuotingANarrowCardDoesNotAllowTheBroadRequest(t *testing.T) {
	r := newRig(t, Capabilities{})
	p := r.peer("me", store.PeerPaired, store.ModeNormal)
	p.ContextToken = "tok"
	_ = r.db.PutChatPeer(r.ctx, p)
	row := r.session("s1", "app", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: rm -rf /home/zhou/projects/miniapp/tmp")
	r.pushed(row, session.StateWaiting, 1)
	oldCard := r.ad.last()
	r.prompt("s1", "Bash: rm -rf /home/zhou")
	before := r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "可以", QuotedText: oldCard, ContextToken: "tok"})
	r.settleFrom(before)
	if len(r.term.pressed("vp_s1")) != 0 {
		t.Fatalf("the narrow card allowed the broad request: %q", r.ad.last())
	}
	if !strings.Contains(r.ad.last(), "rm -rf /home/zhou\n") {
		t.Fatalf("the current request should be shown: %q", r.ad.last())
	}
}

// Round three: what a yes-like word does when it is not taken as one, a no
// the person addressed, a stale quote while something else asks, and a
// question left waiting.
func TestRoundThreeAnswers(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "asks", "codex", session.StateWaiting)
	r.prompt("s1", "shell: rm -rf node_modules")
	r.session("s4", "question", "claude", session.StateWaiting)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "s4", Kind: store.MessageQuestion, Text: "A or B?"})
	h1, _ := r.db.ChatHandle(r.ctx, "s1")

	// 嗯嗯 is not a yes, and says so while one request asks.
	r.say("me", "嗯嗯")
	if len(r.term.pressed("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "没有当成允许") {
		t.Fatalf("嗯嗯: %v %q", r.term.pressed("vp_s1"), r.ad.last())
	}
	// A bare yes is for the one asking permission, not ambiguous with the
	// question; it was shown just now, so it goes through.
	r.say("me", "可以")
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 || keys[0][0] != "y" {
		t.Fatalf("a yes with a question also waiting: %v %q", keys, r.ad.last())
	}
	// An addressed no needs no showing first.
	r.prompt("s1", "shell: deploy web to prod")
	r.say("me", fmt.Sprintf("%d号不行", h1))
	if keys := r.term.pressed("vp_s1"); len(keys) != 2 || keys[1][0] != "n" {
		t.Fatalf("an addressed no: %v %q", keys, r.ad.last())
	}
}

func TestAYesWithNothingAskingIsNotForTheFocus(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s5", "infra", "claude", session.StateDone)
	h5, _ := r.db.ChatHandle(r.ctx, "s5")
	r.say("me", fmt.Sprintf("切到 %d", h5))
	r.say("me", "好")
	if !strings.Contains(r.ad.last(), "没有会话在等你允许") {
		t.Fatalf("reply %q", r.ad.last())
	}
}

// A name the owner gave is not replaced by the IM's profile name, in the
// row or in what the bridge says about the person.
func TestTheOwnersNameForAPersonSticks(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	p := r.peer("me", store.PeerPaired, store.ModeNormal)
	p.Display = "老婆"
	_ = r.db.PutChatPeer(r.ctx, p)
	r.session("s1", "fix", "codex", session.StateWaiting)
	r.prompt("s1", "shell: ls")
	h, _ := r.db.ChatHandle(r.ctx, "s1")
	before := r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", PeerName: "Lin Xiao", Text: fmt.Sprintf("%d号不行", h)})
	r.settleFrom(before)
	r.amu.Lock()
	defer r.amu.Unlock()
	joined := strings.Join(r.audit, "\n")
	if strings.Contains(joined, "Lin Xiao") || !strings.Contains(joined, "老婆") {
		t.Fatalf("audit: %s", joined)
	}
}

// A rule that sends a session's requests to one person does not show them to
// the others by editing a card those others already have.
func TestAnExcludedPersonsCardIsNotEditedIntoTheRequest(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Edit: true, Buttons: true, QuoteRefs: true})
	r.peer("a", store.PeerPaired, store.ModeNormal)
	r.peer("b", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateDone)
	_ = r.db.RecordSessionEvent(r.ctx, store.SessionEvent{At: time.Now().Unix(), SessionID: "s1", ProjectID: "p1", From: session.StateWorking, To: session.StateDone})
	r.advance(2 * time.Minute)
	r.pushed(row, session.StateDone, 2) // both get the done card
	raw, _ := json.Marshal(Routes{
		Rules:   []Rule{{ID: "1", Enabled: true, Match: Match{Kinds: []string{"prompt"}}, To: []string{r.ad.kind + ":a"}, Body: true}},
		Default: DefaultRoutes().Default,
	})
	_ = r.db.SetSetting(r.ctx, RoutesKey, string(raw))
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateWaiting, session.SourceHook)
	r.prompt("s1", "Bash: deploy to prod")
	r.pushed(row, session.StateWaiting, 3)
	time.Sleep(testCoalesce)
	r.ad.mu.Lock()
	defer r.ad.mu.Unlock()
	for ref, e := range r.ad.edits {
		if e.Card != nil && strings.Contains(e.Card.Body, "deploy to prod") {
			t.Fatalf("card %s was edited to show the request: %+v", ref, e.Card)
		}
	}
}

// A stranger writing every forty seconds is answered once a minute, not once.
func TestAStrangerWhoKeepsWritingIsAnsweredAgain(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.say("new", "hi")
	r.advance(40 * time.Second)
	r.say("new", "hello?")
	r.advance(40 * time.Second)
	r.say("new", "anyone?")
	n := 0
	for _, s := range r.ad.texts() {
		if strings.Contains(s, "配对码") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("codes sent: %d, %q", n, r.ad.texts())
	}
}

// The preview says what a permission request from a session would do, not
// only what its current state does.
func TestThePreviewAnswersForARequest(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("a", store.PeerPaired, store.ModeNormal)
	r.peer("b", store.PeerPaired, store.ModeNormal)
	raw, _ := json.Marshal(Routes{
		Rules:   []Rule{{ID: "1", Name: "requests to a", Enabled: true, Match: Match{Kinds: []string{"prompt"}}, To: []string{r.ad.kind + ":a"}}},
		Default: DefaultRoutes().Default,
	})
	_ = r.db.SetSetting(r.ctx, RoutesKey, string(raw))
	r.session("s1", "fix", "claude", session.StateDone)
	d, who, ok := r.b.PreviewRequest(r.ctx, "s1")
	if !ok || d.Rule != "requests to a" || len(who) != 1 || who[0] != r.ad.kind+":a" {
		t.Fatalf("request preview: %+v %v", d, who)
	}
}

// One request held for two people is one request.
func TestOwedCountsRequestsNotPeople(t *testing.T) {
	r := newRig(t, Capabilities{})
	r.peer("a", store.PeerPaired, store.ModeNormal)
	r.peer("b", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: ls")
	r.b.SessionChanged(row, session.StateWaiting)
	r.waitFor(func() bool { return len(r.b.Healths(r.ctx)[0].WaitingOn) == 2 })
	if h := r.b.Healths(r.ctx)[0]; h.NeedsHello != 1 {
		t.Fatalf("health: %+v", h)
	}
}
