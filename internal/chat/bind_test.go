package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// What the people who tried this on a phone ran into, each as the smallest
// conversation that shows it.

// prompt records a permission request and returns its message id.
func (r *rig) prompt(sessionID, text string) int64 {
	r.t.Helper()
	m, err := r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: sessionID, Kind: store.MessagePrompt, Text: text})
	if err != nil {
		r.t.Fatal(err)
	}
	return m.ID
}

// pushed changes a session and waits for the card.
func (r *rig) pushed(row store.Session, to session.State, sent int) {
	r.t.Helper()
	r.b.SessionChanged(row, to)
	r.waitFor(func() bool { return r.ad.count() >= sent })
}

func (r *rig) press(value, messageRef string) {
	r.t.Helper()
	before := r.b.Handled()
	r.ad.inbound(r.t, Inbound{PeerID: "me", Action: &Action{Value: value, ID: "cb", MessageRef: messageRef}})
	r.settleFrom(before)
}

// The card for the first request still has its buttons when the session asks
// again. Pressing "allow" on it must not allow the second request.
func TestAnOldCardsButtonDoesNotAnswerTheNextRequest(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Buttons: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	first := r.prompt("s1", "Bash: make test")
	r.pushed(row, session.StateWaiting, 1)
	r.press(fmt.Sprintf("approve:s1:%d", first), "m1")
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 {
		t.Fatalf("the card's own request was not answered: %v", keys)
	}
	if !strings.Contains(r.ad.last(), "make test") {
		t.Fatalf("the receipt should say what was allowed: %q", r.ad.last())
	}

	// The session asks again; the old card is pressed again.
	r.prompt("s1", "Bash: rm -rf ~")
	r.press(fmt.Sprintf("approve:s1:%d", first), "m1")
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 {
		t.Fatalf("an old card's button allowed the next request: %v", keys)
	}
	if !strings.Contains(r.ad.last(), "rm -rf ~") || !strings.Contains(r.ad.last(), "已经处理过") {
		t.Fatalf("the refusal should show the current request: %q", r.ad.last())
	}
	// A value that claims the new request on the old message is still the
	// old message: the record decides, not the value.
	msgs, _ := r.db.ListSessionMessages(r.ctx, "s1", 1)
	r.press(fmt.Sprintf("approve:s1:%d", msgs[0].ID), "m1")
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 {
		t.Fatalf("a forged value on an old card allowed: %v", keys)
	}
	// A button from before requests were named is old by definition.
	r.press("approve:s1", "")
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 {
		t.Fatalf("an unnamed button allowed: %v", keys)
	}
}

// Only one session is waiting, so a bare "y" has an obvious address, but the
// person was never shown what it is asking.
func TestABareYesNeverAnswersARequestThePersonHasNotSeen(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: git push --force")
	r.say("me", "y")
	if len(r.term.pressed("vp_s1")) != 0 {
		t.Fatalf("an unseen request was allowed: %v", r.term.pressed("vp_s1"))
	}
	if !strings.Contains(r.ad.last(), "git push --force") {
		t.Fatalf("the request should be shown: %q", r.ad.last())
	}
	r.say("me", "好")
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 || keys[0][0] != "Enter" {
		t.Fatalf("the shown request was not allowed: %v", keys)
	}
	a := strings.Join(r.audit, "\n")
	if !strings.Contains(a, "chat.approved") || !strings.Contains(a, "git push --force") || !strings.Contains(a, "fix") {
		t.Fatalf("the audit should say what was allowed and where: %q", a)
	}
}

// 微信 quotes by text. A quoted card is an address and also says which
// request it showed.
func TestQuotingAnOldCardOnWeixinIsNotAnAnswerToTheNewRequest(t *testing.T) {
	r := newRig(t, Capabilities{})
	p := r.peer("me", store.PeerPaired, store.ModeNormal)
	p.ContextToken = "tok"
	_ = r.db.PutChatPeer(r.ctx, p)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: npm publish")
	r.pushed(row, session.StateWaiting, 1)
	oldCard := r.ad.last()
	if !strings.Contains(oldCard, "1号可以") {
		t.Fatalf("a card on an IM without buttons should say what to type: %q", oldCard)
	}
	r.prompt("s1", "Bash: npm unpublish everything")
	before := r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "y", QuotedText: oldCard, ContextToken: "tok"})
	r.settleFrom(before)
	if len(r.term.pressed("vp_s1")) != 0 {
		t.Fatalf("quoting the old card allowed the new request: %v", r.term.pressed("vp_s1"))
	}
	if !strings.Contains(r.ad.last(), "unpublish everything") {
		t.Fatalf("reply %q", r.ad.last())
	}
	// The reply showed the current request, so it is not pushed again, and
	// quoting that reply answers it.
	shown := r.ad.last()
	r.b.SessionChanged(row, session.StateWaiting)
	time.Sleep(testCoalesce + 200*time.Millisecond)
	if r.ad.last() != shown {
		t.Fatalf("a request already shown was pushed again: %q", r.ad.last())
	}
	before = r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "拒绝", QuotedText: shown, ContextToken: "tok"})
	r.settleFrom(before)
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 || keys[0][0] != "Escape" {
		t.Fatalf("quoting the current card did not answer it: %v", keys)
	}
}

// Being told about a session used to make it the focus, so a person who had
// chosen a focus found their words typed into whichever session pushed last.
func TestAPushDoesNotMoveTheFocus(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s2", "docs", "claude", session.StateDone)
	h2, _ := r.db.ChatHandle(r.ctx, "s2")
	r.say("me", fmt.Sprintf("focus %d", h2))
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.pushed(row, session.StateWaiting, 2)
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateDone, session.SourceHook)
	p, _ := r.db.GetChatPeer(r.ctx, r.ad.kind, "me")
	if p.FocusSession != "s2" {
		t.Fatalf("a push moved the focus to %q", p.FocusSession)
	}
	r.say("me", "update the changelog too")
	if len(r.term.pasted("vp_s1")) != 0 || len(r.term.pasted("vp_s2")) != 1 {
		t.Fatalf("words went to %q / %q", r.term.pasted("vp_s1"), r.term.pasted("vp_s2"))
	}
	r.say("me", "列表")
	if !strings.Contains(r.ad.last(), "默认会话") {
		t.Fatalf("the list should mark the focus: %q", r.ad.last())
	}
	if !strings.Contains(r.ad.last(), "默认会话") {
		t.Fatalf("a receipt for a focus delivery should say so: %q", r.ad.last())
	}
}

// Once answered, a request comes off every card that showed it.
func TestAnAnsweredRequestIsRetiredEverywhere(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Buttons: true, Edit: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.peer("partner", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "codex", session.StateWaiting)
	id := r.prompt("s1", "shell: make release")
	r.pushed(row, session.StateWaiting, 2)
	refs := map[string]string{}
	outs, _ := r.db.ChatOutboundsForMessage(r.ctx, "s1", id)
	for _, o := range outs {
		refs[o.PeerID] = o.Ref
	}
	if len(refs) != 2 {
		t.Fatalf("both cards should be recorded against the request: %+v", outs)
	}
	r.press(fmt.Sprintf("deny:s1:%d", id), refs["me"])
	r.ad.mu.Lock()
	defer r.ad.mu.Unlock()
	for who, ref := range refs {
		e, ok := r.ad.edits[ref]
		if !ok || e.Card == nil || len(e.Buttons) != 0 || !strings.Contains(e.Card.StateText, "已拒绝") {
			t.Fatalf("%s's card was not retired: %+v", who, e)
		}
	}
}

// 微信 cannot be spoken to until the person writes. What waited meanwhile is
// shown first, and the message they wrote without having seen it is not run.
func TestWhatAPersonMissedIsShownBeforeAnythingElse(t *testing.T) {
	r := newRig(t, Capabilities{})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: terraform apply")
	r.b.SessionChanged(row, session.StateWaiting)
	r.waitFor(func() bool {
		r.amu.Lock()
		defer r.amu.Unlock()
		return strings.Contains(strings.Join(r.audit, "\n"), "chat.undelivered")
	})
	before := r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", Text: "y", ContextToken: "tok"})
	r.settleFrom(before)
	if len(r.term.pressed("vp_s1")) != 0 {
		t.Fatalf("a message written before seeing the request answered it: %v", r.term.pressed("vp_s1"))
	}
	texts := strings.Join(r.ad.texts(), "\n")
	if !strings.Contains(texts, "你不在的时候") || !strings.Contains(texts, "terraform apply") {
		t.Fatalf("missed: %q", texts)
	}
	// Having seen it, the next "y" answers it; the list is not shown twice.
	r.say("me", "y")
	if len(r.term.pressed("vp_s1")) != 1 {
		t.Fatalf("after catching up: %q", r.ad.texts())
	}
}

// An IM that refuses a send because the person must speak first is counted
// as that, with the reason on the page.
func TestARefusedSendIsCountedWithItsReason(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.ad.mu.Lock()
	r.ad.sendErr = fmt.Errorf("weixin: sendmessage: ret -2: %w", ErrNeedsHello)
	r.ad.mu.Unlock()
	r.b.SessionChanged(row, session.StateWaiting)
	r.waitFor(func() bool {
		h := r.b.Healths(r.ctx)
		return len(h) == 1 && h[0].NeedsHello == 1
	})
	// Not an error of the channel: the page names who it waits on.
	h := r.b.Healths(r.ctx)[0]
	if h.Failed != 0 || h.LastError != "" || len(h.WaitingOn) != 1 || h.WaitingOn[0] != "me" {
		t.Fatalf("health: %+v", h)
	}
	// Once they write, nothing is owed.
	r.ad.mu.Lock()
	r.ad.sendErr = nil
	r.ad.mu.Unlock()
	r.say("me", "hi")
	if h := r.b.Healths(r.ctx)[0]; h.NeedsHello != 0 {
		t.Fatalf("still owed after they wrote: %+v", h)
	}
}

func TestPendingConfirmationsReadYesAsOkAndSayWhatTheyReplace(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("w", "busy", "claude", session.StateWorking)
	r.session("x", "other", "claude", session.StateWorking)
	r.session("q", "asks", "claude", session.StateWaiting)
	r.prompt("q", "Bash: ls")
	hw, _ := r.db.ChatHandle(r.ctx, "w")
	hx, _ := r.db.ChatHandle(r.ctx, "x")
	r.say("me", fmt.Sprintf("停 %d", hw))
	r.say("me", fmt.Sprintf("stop %d", hx))
	if !strings.Contains(r.ad.last(), "作废") {
		t.Fatalf("a replaced confirmation should be named: %q", r.ad.last())
	}
	// "好的" confirms the stop; it does not approve the waiting prompt.
	r.say("me", "好的")
	if len(r.term.pressed("vp_q")) != 0 || len(r.term.pressed("vp_w")) != 0 || len(r.term.pressed("vp_x")) != 1 {
		t.Fatalf("keys: q %v w %v x %v", r.term.pressed("vp_q"), r.term.pressed("vp_w"), r.term.pressed("vp_x"))
	}
	// Stopping what is not working says so without a confirmation.
	hq, _ := r.db.ChatHandle(r.ctx, "q")
	_ = r.db.SetSessionState(r.ctx, "q", session.StateDone, session.SourceHook)
	r.say("me", fmt.Sprintf("stop %d", hq))
	if !strings.Contains(r.ad.last(), "没在工作") {
		t.Fatalf("stop on a stopped session: %q", r.ad.last())
	}
}

func TestPicturesTakeTheirCaptionAndWaitForPrompts(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: rm build")
	h, _ := r.db.ChatHandle(r.ctx, "s1")
	fetched := 0
	fetch := func(context.Context) ([]byte, error) { fetched++; return []byte{0xff, 0xd8, 1}, nil }
	before := r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", Text: fmt.Sprintf("%d: this is the bug", h), FetchImage: fetch})
	r.settleFrom(before)
	if fetched != 0 || len(r.term.pasted("vp_s1")) != 0 {
		t.Fatalf("a picture went into a permission prompt: fetched %d pasted %q", fetched, r.term.pasted("vp_s1"))
	}
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateDone, session.SourceHook)
	before = r.b.Handled()
	r.ad.inbound(t, Inbound{PeerID: "me", Text: fmt.Sprintf("%d: this is the bug", h), FetchImage: fetch})
	r.settleFrom(before)
	got := r.term.pasted("vp_s1")
	if len(got) != 1 || !strings.HasSuffix(got[0], ".jpg this is the bug") || len(r.term.pressed("vp_s1")) != 1 {
		t.Fatalf("caption: pasted %q keys %v", got, r.term.pressed("vp_s1"))
	}
	if !strings.Contains(r.ad.last(), "图片和文字") {
		t.Fatalf("receipt %q", r.ad.last())
	}
}

func TestSmallThingsPeopleTripOver(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.b.d.PublicURL = func() string { return "http://127.0.0.1:8080" }
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateDone)
	h, _ := r.db.ChatHandle(r.ctx, "s1")

	r.say("me", fmt.Sprintf("打开 %d", h))
	if !strings.Contains(r.ad.last(), "本机地址") {
		t.Fatalf("a localhost link: %q", r.ad.last())
	}
	r.say("me", "/deploy")
	if !strings.Contains(r.ad.last(), "没有 /deploy") || len(r.term.pasted("vp_s1")) != 0 {
		t.Fatalf("unknown slash: %q", r.ad.last())
	}
	r.say("me", "全部允许")
	if !strings.Contains(r.ad.last(), "不能一次全部允许") {
		t.Fatalf("allow all: %q", r.ad.last())
	}
	r.say("me", fmt.Sprintf("静音 %d 午饭后", h))
	if !strings.Contains(r.ad.last(), "没看懂") {
		t.Fatalf("an unreadable duration: %q", r.ad.last())
	}
	// A session that ended is gone, not unknown.
	_ = r.db.SetSessionExit(r.ctx, "s1", true, 0)
	r.say("me", fmt.Sprintf("%d: still there?", h))
	if !strings.Contains(r.ad.last(), "已经结束") {
		t.Fatalf("an ended session: %q", r.ad.last())
	}
	// "3 继续" reaches [3] when [3] exists.
	r.session("s2", "docs", "claude", session.StateDone)
	h2, _ := r.db.ChatHandle(r.ctx, "s2")
	r.say("me", fmt.Sprintf("%d 继续", h2))
	if got := r.term.pasted("vp_s2"); len(got) != 1 || got[0] != "继续" {
		t.Fatalf("a spaced handle: %q", got)
	}
}

// A pending person who sends the code to the bot is told where it goes.
func TestAPendingPersonSendingTheirCodeIsToldWhereItGoes(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.say("new", "hi")
	peers, _ := r.db.ListChatPeers(r.ctx)
	r.say("new", peers[0].PairingCode)
	if !strings.Contains(r.ad.last(), "「聊天」页") || !strings.Contains(r.ad.last(), "https://panel.test") {
		t.Fatalf("reply %q", r.ad.last())
	}
	spaced := peers[0].PairingCode[:3] + " " + peers[0].PairingCode[3:]
	if _, err := r.b.Pair(r.ctx, spaced); err != nil {
		t.Fatalf("a spaced code did not pair: %v", err)
	}
}

// A stop between two tool calls is not a finished turn.
func TestADoneWithoutANewMessageIsNotPushed(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateDone)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "s1", Kind: "assistant", Text: "earlier", At: time.Now().Add(-time.Hour).Unix()})
	_ = r.db.SetSessionState(r.ctx, "s1", session.StateDone, session.SourceHook)
	r.b.SessionChanged(row, session.StateDone)
	time.Sleep(testCoalesce + 200*time.Millisecond)
	if r.ad.count() != 0 {
		t.Fatalf("a flicker was pushed: %q", r.ad.texts())
	}
	// A session with no hooks is told about once it finished work, and not
	// for settling into done the moment it was created.
	row2 := r.session("s2", "plain", "claude", session.StateDone)
	r.b.SessionChanged(row2, session.StateDone)
	time.Sleep(testCoalesce + 200*time.Millisecond)
	if r.ad.count() != 0 {
		t.Fatalf("a new idle session was pushed: %q", r.ad.texts())
	}
	_ = r.db.RecordSessionEvent(r.ctx, store.SessionEvent{At: time.Now().Unix(), SessionID: "s2", ProjectID: "p1", From: session.StateWorking, To: session.StateDone})
	r.b.SessionChanged(row2, session.StateDone)
	r.waitFor(func() bool { return r.ad.count() == 1 })
}

// Two long cards: "more" continues the last, "more N" the one named.
func TestMoreIsPerSession(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	long := func(word string) string { return strings.Repeat(word+" ", 400) }
	a := r.session("a", "a", "claude", session.StateDone)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "a", Kind: "assistant", Text: long("alpha")})
	r.pushed(a, session.StateDone, 1)
	b := r.session("b", "b", "claude", session.StateDone)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "b", Kind: "assistant", Text: long("beta")})
	r.pushed(b, session.StateDone, 2)
	ha, _ := r.db.ChatHandle(r.ctx, "a")
	r.say("me", "more")
	if !strings.Contains(r.ad.last(), "beta") {
		t.Fatalf("bare more: %q", r.ad.last())
	}
	r.say("me", fmt.Sprintf("more %d", ha))
	if !strings.Contains(r.ad.last(), "alpha") {
		t.Fatalf("more for the first card: %q", r.ad.last())
	}
}

// A list shows what each waiting session asks, and a person who read it there
// can answer it.
func TestTheListShowsRequestsAndCountsAsSeeingThem(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "one", "codex", session.StateWaiting)
	r.session("s2", "two", "codex", session.StateWaiting)
	r.prompt("s1", "shell: docker compose down")
	r.prompt("s2", "shell: docker compose up")
	r.say("me", "列表")
	if l := r.ad.last(); !strings.Contains(l, "compose down") || !strings.Contains(l, "compose up") {
		t.Fatalf("list: %q", l)
	}
	h2, _ := r.db.ChatHandle(r.ctx, "s2")
	r.say("me", fmt.Sprintf("%d号可以", h2))
	if keys := r.term.pressed("vp_s2"); len(keys) != 1 || keys[0][0] != "y" {
		t.Fatalf("an answer to a listed request: %v %q", keys, r.ad.last())
	}
}

// While the assistant's question stands, a "yes" is the answer to it.
func TestAYesAfterTheAssistantAsksGoesToTheAssistant(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	fa := &fakeAssistant{intent: Intent{Verb: "clarify", Say: "[1] 还是 [2]？"}}
	r.b.SetAssistant(fa)
	r.peer("me", store.PeerPaired, store.ModeAdvanced)
	r.session("s1", "one", "codex", session.StateWaiting)
	r.prompt("s1", "shell: ls")
	r.say("me", "那个测试的")
	calls := fa.calls
	fa.intent = Intent{Verb: "none", Say: "ok"}
	r.say("me", "好")
	if fa.calls != calls+1 || len(r.term.pressed("vp_s1")) != 0 {
		t.Fatalf("a yes to the assistant's question went to a session: calls %d keys %v", fa.calls-calls, r.term.pressed("vp_s1"))
	}
}

// A sentence meant for something else must not become the answer to a
// question the person never saw, just because that session is the one
// waiting; named, it goes in.
func TestAnUnseenQuestionIsShownBeforeWordsAnswerIt(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateWaiting)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "s1", Kind: store.MessageQuestion, Text: "Which database should I drop?"})
	r.say("me", "the staging one")
	if len(r.term.pasted("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "Which database") {
		t.Fatalf("words answered an unseen question: %q %q", r.term.pasted("vp_s1"), r.ad.last())
	}
	// "y" to a question is words, not the approve key, once seen.
	r.say("me", "y")
	if got := r.term.pasted("vp_s1"); len(got) != 1 || got[0] != "y" {
		t.Fatalf("a seen question's y: pasted %q keys %v", got, r.term.pressed("vp_s1"))
	}
	h, _ := r.db.ChatHandle(r.ctx, "s1")
	r.say("me", fmt.Sprintf("%d: the staging one", h))
	if got := r.term.pasted("vp_s1"); len(got) != 2 {
		t.Fatalf("a named answer: %q", got)
	}
	// A spoken handle is the address, not part of the answer.
	r.say("me", fmt.Sprintf("%d号可以", h))
	if got := r.term.pasted("vp_s1"); len(got) != 3 || got[2] != "可以" {
		t.Fatalf("a spoken handle's answer: %q", got)
	}
}

func TestABareYesToAQuestionNobodyShowedIsNotTyped(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateWaiting)
	_, _ = r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: "s1", Kind: store.MessageQuestion, Text: "Delete the old branch?"})
	r.say("me", "y")
	if len(r.term.pasted("vp_s1")) != 0 || len(r.term.pressed("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "old branch") {
		t.Fatalf("an unseen question: %q %v %q", r.term.pasted("vp_s1"), r.term.pressed("vp_s1"), r.ad.last())
	}
}

// Without hooks the panel knows a session waits, not on what. A bare "y" has
// to follow at least a card about this wait.
func TestABareYesToAHooklessWaitNeedsACardFirst(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "fix", "claude", session.StateWaiting)
	r.say("me", "y")
	if len(r.term.pressed("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "屏幕") {
		t.Fatalf("a hookless wait allowed blind: %v %q", r.term.pressed("vp_s1"), r.ad.last())
	}
	r.pushed(row, session.StateWaiting, 2)
	r.say("me", "y")
	if len(r.term.pressed("vp_s1")) != 1 {
		t.Fatalf("after the card: %v %q", r.term.pressed("vp_s1"), r.ad.texts())
	}
}

// "Last message 3 minutes ago" is how an owner tells a quiet channel from a
// dead one; a restart must not reset it to never.
func TestTheLastInboundOutlivesARestart(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.say("me", "列表")
	r.b.Reload(r.ctx)
	r.waitFor(func() bool {
		h := r.b.Healths(r.ctx)
		return len(h) == 1 && h[0].Running
	})
	if h := r.b.Healths(r.ctx); h[0].LastInbound != r.now.Unix() {
		t.Fatalf("after a restart: %+v", h[0])
	}
}

// "OK" is a yes when nothing is waiting for a confirmation, and not after a
// stop, where it is the person confirming the stop they expected to be asked
// about.
func TestOkIsAYesExceptAfterAStop(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "fix", "claude", session.StateWaiting)
	h, _ := r.db.ChatHandle(r.ctx, "s1")
	r.prompt("s1", "Bash: rm -rf build")
	r.say("me", fmt.Sprintf("停 %d", h))
	if !strings.Contains(r.ad.last(), "没在工作") || !strings.Contains(r.ad.last(), "rm -rf build") {
		t.Fatalf("stop at a prompt: %q", r.ad.last())
	}
	r.say("me", "OK")
	if len(r.term.pressed("vp_s1")) != 0 {
		t.Fatalf("an ok after a stop allowed the prompt: %v", r.term.pressed("vp_s1"))
	}
	r.say("me", "OK")
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 || keys[0][0] != "Enter" {
		t.Fatalf("a later OK to a seen request: %v %q", keys, r.ad.last())
	}
}

// A quoted reply that is not a card, about one session, is that session; a
// quote that names nobody does not fall back to whoever is waiting.
func TestAQuoteThatNamesNobodyIsNotAnsweredForSomebodyElse(t *testing.T) {
	r := newRig(t, Capabilities{})
	p := r.peer("me", store.PeerPaired, store.ModeNormal)
	p.ContextToken = "tok"
	_ = r.db.PutChatPeer(r.ctx, p)
	r.session("s1", "one", "claude", session.StateWaiting)
	r.prompt("s1", "Bash: rm -rf assets")
	r.session("s3", "three", "claude", session.StateDone)
	h3, _ := r.db.ChatHandle(r.ctx, "s3")
	r.say("me", "列表") // s1's request is shown
	quote := func(text, quoted string) {
		before := r.b.Handled()
		r.ad.inbound(t, Inbound{PeerID: "me", Text: text, QuotedText: quoted, ContextToken: "tok"})
		r.settleFrom(before)
	}
	quote("可以", fmt.Sprintf("→ [%d] 已拒绝：Bash: delete the .env file", h3))
	if len(r.term.pressed("vp_s1")) != 0 {
		t.Fatalf("a quote of [%d] allowed [1]: %q", h3, r.ad.last())
	}
	quote("可以", "没有等待确认的操作。")
	if len(r.term.pressed("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "说不清") {
		t.Fatalf("a quote naming nobody: %v %q", r.term.pressed("vp_s1"), r.ad.last())
	}
}

// Two commands that start the same are different requests.
func TestAQuotedCardMustShowTheWholeRequest(t *testing.T) {
	if quoteShows("▲ [3] app\n要你允许\n\nBash: cd /home/zhou/projects/miniapp && rm -rf build/cache", "Bash: cd /home/zhou/projects/miniapp && rm -rf src") {
		t.Fatal("a card for one command read as the card for another with the same start")
	}
	if !quoteShows("▲ [3] app\n要你允许\n\nBash: cd /home/zhou/projects/miniapp &&\n rm -rf src\nhttps://x", "Bash: cd /home/zhou/projects/miniapp && rm -rf src") {
		t.Fatal("the card for the request did not read as it")
	}
}

// A channel whose own settings are refused is not stored and not started.
func TestAChannelTheAdapterRefusesIsNotSaved(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	rigKinds.Lock()
	if _, ok := FactoryFor("picky"); !ok {
		Register(Factory{Kind: "picky", Label: "Picky", New: func(raw json.RawMessage, _ Env) (Adapter, error) {
			var v map[string]string
			_ = json.Unmarshal(raw, &v)
			if v["token"] == "" {
				return nil, errors.New("picky: a token is required")
			}
			return newFake("picky", Capabilities{}), nil
		}})
	}
	rigKinds.Unlock()
	err := r.b.WriteChannel(r.ctx, "picky", true, ChannelConfig{Values: map[string]string{}})
	if !errors.Is(err, ErrChannelConfig) || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("an empty form: %v", err)
	}
	if _, err := r.db.GetChatChannel(r.ctx, "picky"); err != store.ErrNotFound {
		t.Fatalf("stored anyway: %v", err)
	}
	// Off, it may be saved half-filled.
	if err := r.b.WriteChannel(r.ctx, "picky", false, ChannelConfig{Values: map[string]string{}}); err != nil {
		t.Fatalf("saving it off: %v", err)
	}
}
