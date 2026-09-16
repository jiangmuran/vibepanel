package chat

import (
	"fmt"
	"strings"
	"testing"

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
	if !ok || e.Card == nil || len(e.Buttons) != 0 || !strings.Contains(e.Card.StateText, "已处理") {
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
