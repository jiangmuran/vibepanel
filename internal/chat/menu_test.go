package chat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

func menuMessage(t *testing.T, r *rig, sessionID string, m hooks.Menu) int64 {
	t.Helper()
	raw, _ := json.Marshal(m)
	msg, err := r.db.AddSessionMessage(r.ctx, store.SessionMessage{SessionID: sessionID, Kind: store.MessageQuestion, Text: m.Summary(), Menu: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	return msg.ID
}

var twoQuestions = hooks.Menu{Tool: hooks.ToolAskUserQuestion, Questions: []hooks.MenuQuestion{
	{Header: "Color", Question: "Pick a color", Options: []hooks.MenuOption{{Label: "Red"}, {Label: "Green"}, {Label: "Blue"}}},
	{Header: "Pets", Question: "Pick pets", Multi: true, Options: []hooks.MenuOption{{Label: "Cat"}, {Label: "Dog"}, {Label: "Fish"}}},
}}

// The key sequences, each as measured against Claude Code 2.1 in tmux.
func TestMenuStepsAreWhatClaudeCodeReads(t *testing.T) {
	single := hooks.Menu{Tool: hooks.ToolAskUserQuestion, Questions: []hooks.MenuQuestion{{Question: "Pick a name", Options: []hooks.MenuOption{{Label: "Alpha"}, {Label: "Beta"}}}}}
	preview := hooks.Menu{Tool: hooks.ToolAskUserQuestion, Questions: []hooks.MenuQuestion{{Question: "Pick a layout", Preview: true, Options: []hooks.MenuOption{{Label: "A"}, {Label: "B"}}}}}
	plan := hooks.Menu{Tool: hooks.ToolExitPlanMode, Plan: "1. write hello.txt"}
	k := func(keys ...string) menuStep { return menuStep{keys: keys} }
	p := func(text string) menuStep { return menuStep{paste: text} }
	for _, c := range []struct {
		name string
		m    hooks.Menu
		next int
		a    menuAnswer
		want []menuStep
	}{
		{"a digit picks a single choice", single, 0, menuAnswer{choices: []int{2}}, []menuStep{k("2")}},
		{"own answer on the Type something row", single, 0, menuAnswer{text: "小满"}, []menuStep{k("Down", "Down"), p("小满"), k("Enter")}},
		{"an option with words is an own answer naming it", single, 0, menuAnswer{choices: []int{1}, text: "但要短"}, []menuStep{k("Down", "Down"), p("Alpha: 但要短"), k("Enter")}},
		{"skip is Chat about this", single, 0, menuAnswer{skip: true}, []menuStep{k("4")}},
		{"first of two questions: no submit yet", twoQuestions, 0, menuAnswer{choices: []int{2}}, []menuStep{k("2")}},
		{"multiple choice toggles then leaves by its Submit row, then the review page", twoQuestions, 1, menuAnswer{choices: []int{1, 3}},
			[]menuStep{k("1"), k("3"), k("Down", "Down", "Down", "Down", "Enter"), k("1")}},
		{"multiple choice with an own answer", twoQuestions, 1, menuAnswer{choices: []int{1}, text: "Hamster"},
			[]menuStep{k("1"), k("Down", "Down", "Down"), p("Hamster"), k("Down", "Enter"), k("1")}},
		{"previews: an option", preview, 0, menuAnswer{choices: []int{2}}, []menuStep{k("Down"), k("Enter")}},
		{"previews: an option with a note closes the note first", preview, 0, menuAnswer{choices: []int{2}, text: "标题放左边"},
			[]menuStep{k("Down"), k("n"), p("标题放左边"), k("Escape"), k("Enter")}},
		{"previews: words alone are notes only", preview, 0, menuAnswer{text: "都不要"}, []menuStep{k("n"), p("都不要"), k("Enter")}},
		{"plan: go ahead", plan, 0, menuAnswer{choices: []int{1}}, []menuStep{k("Enter")}},
		{"plan: approve edits", plan, 0, menuAnswer{choices: []int{2}}, []menuStep{k("Down", "Enter")}},
		{"plan: what to change", plan, 0, menuAnswer{text: "先别写文件"}, []menuStep{k("Down", "Down"), p("先别写文件"), k("Enter")}},
	} {
		got, ok := menuSteps(&c.m, c.next, c.a)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
	both := hooks.Menu{Tool: hooks.ToolAskUserQuestion, Questions: []hooks.MenuQuestion{{Question: "q", Preview: true, Multi: true, Options: []hooks.MenuOption{{Label: "a"}}}}}
	if _, ok := menuSteps(&both, 0, menuAnswer{choices: []int{1}}); ok {
		t.Error("previews with several choices were answered blind")
	}
}

func TestMenuRepliesAreRead(t *testing.T) {
	plan := hooks.Menu{Tool: hooks.ToolExitPlanMode}
	for _, c := range []struct {
		in   string
		m    hooks.Menu
		next int
		want menuAnswer
		why  string
	}{
		{"2", twoQuestions, 0, menuAnswer{choices: []int{2}}, ""},
		{"２", twoQuestions, 0, menuAnswer{choices: []int{2}}, ""},
		{"1 3", twoQuestions, 0, menuAnswer{}, "menuOnlyOne"},
		{"4", twoQuestions, 0, menuAnswer{}, "menuOutOfRange"},
		{"2，但要深一点", twoQuestions, 0, menuAnswer{choices: []int{2}, text: "但要深一点"}, ""},
		{"我想要紫色", twoQuestions, 0, menuAnswer{text: "我想要紫色"}, ""},
		{"跳过", twoQuestions, 0, menuAnswer{skip: true}, ""},
		{"1 3", twoQuestions, 1, menuAnswer{choices: []int{1, 3}}, ""},
		{"1、3，仓鼠", twoQuestions, 1, menuAnswer{choices: []int{1, 3}, text: "仓鼠"}, ""},
		{"2", plan, 0, menuAnswer{choices: []int{2}}, ""},
		{"3", plan, 0, menuAnswer{}, "menuPlanPick"},
		{"先别写文件", plan, 0, menuAnswer{text: "先别写文件"}, ""},
	} {
		got, why := parseMenuAnswer(c.in, &c.m, c.next)
		if why != c.why || (why == "" && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%q: got %+v %q, want %+v %q", c.in, got, why, c.want, c.why)
		}
	}
}

// The whole conversation: the card is the question with its options and
// buttons, a yes is not an answer, a bare number to a menu nobody saw is
// shown first, and two questions are answered one after the other with the
// review page submitted at the end.
func TestAMenuIsAnsweredQuestionByQuestion(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Buttons: true, Edit: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "usage panel", "claude", session.StateWaiting)
	h, _ := r.db.ChatHandle(r.ctx, "s1")
	id := menuMessage(t, r, "s1", twoQuestions)

	r.pushed(row, session.StateWaiting, 1)
	r.ad.mu.Lock()
	card := r.ad.sent[0]
	r.ad.mu.Unlock()
	if card.Card == nil || !strings.Contains(card.Card.Body, "（第 1/2 题） Color · Pick a color") || !strings.Contains(card.Card.Body, "3. Blue") {
		t.Fatalf("card: %+v", card.Card)
	}
	if len(card.Buttons) != 4 || card.Buttons[1].Value != fmt.Sprintf("menu:s1:%d:0:2", id) || card.Buttons[3].Label != "跳过" {
		t.Fatalf("buttons: %+v", card.Buttons)
	}
	for _, b := range card.Buttons {
		if strings.Contains(b.Label, "允许") {
			t.Fatalf("a menu offered allow: %+v", card.Buttons)
		}
	}
	// A yes is not an answer: no key, the menu again.
	r.say("me", "好")
	if len(r.term.pressed("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "选择题") || !strings.Contains(r.ad.last(), "1. Red") {
		t.Fatalf("a yes to a menu: %v %q", r.term.pressed("vp_s1"), r.ad.last())
	}

	// Question one by button; the reply shows question two.
	r.press(fmt.Sprintf("menu:s1:%d:0:2", id), "m1")
	if keys := r.term.pressed("vp_s1"); len(keys) != 1 || keys[0][0] != "2" {
		t.Fatalf("question one: %v", keys)
	}
	if !strings.Contains(r.ad.last(), "已选：Green") || !strings.Contains(r.ad.last(), "Pick pets") {
		t.Fatalf("receipt: %q", r.ad.last())
	}
	// The old card's button, pressed again, is for a question already
	// answered.
	r.press(fmt.Sprintf("menu:s1:%d:0:1", id), "m1")
	if len(r.term.pressed("vp_s1")) != 1 || !strings.Contains(r.ad.last(), "之前的题") {
		t.Fatalf("a stale press: %v %q", r.term.pressed("vp_s1"), r.ad.last())
	}

	// Question two in words: two choices and an own answer.
	r.say("me", fmt.Sprintf("%d: 1 3，仓鼠", h))
	var flat []string
	for _, k := range r.term.pressed("vp_s1")[1:] {
		flat = append(flat, k...)
	}
	if got := strings.Join(flat, " "); got != "1 3 Down Down Down Down Enter 1" {
		t.Fatalf("question two keys: %s", got)
	}
	if got := r.term.pasted("vp_s1"); len(got) != 1 || got[0] != "仓鼠" {
		t.Fatalf("question two words: %q", got)
	}
	if !strings.Contains(r.ad.last(), "已回答：Cat、Fish、「仓鼠」") {
		t.Fatalf("final receipt: %q", r.ad.last())
	}
	r.ad.mu.Lock()
	e, ok := r.ad.edits["m1"]
	r.ad.mu.Unlock()
	if !ok || len(e.Buttons) != 0 || !strings.Contains(e.Card.StateText, "已回答") {
		t.Fatalf("the card was not closed: %+v", e)
	}
}

func TestABareNumberToAMenuNobodySawIsShownFirst(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "x", "claude", session.StateWaiting)
	menuMessage(t, r, "s1", hooks.Menu{Tool: hooks.ToolExitPlanMode, Plan: "1. rm -rf build"})
	r.say("me", "1")
	if len(r.term.pressed("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "rm -rf build") || !strings.Contains(r.ad.last(), "还没看过") {
		t.Fatalf("unseen plan: %v %q", r.term.pressed("vp_s1"), r.ad.last())
	}
	r.say("me", "先别删，列出来给我看")
	if got := r.term.pasted("vp_s1"); len(got) != 1 || got[0] != "先别删，列出来给我看" {
		t.Fatalf("plan feedback: %q %q", got, r.ad.last())
	}
}

// "3 标题放左边" to a menu is an answer, even when a session numbered 3
// exists; before menus it was handle 3 and some words.
func TestAMenuReplyIsNotReadAsAnotherSessionsHandle(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "asks", "claude", session.StateWaiting)
	r.session("s2", "other", "claude", session.StateDone)
	r.session("s3", "third", "claude", session.StateDone)
	h1, _ := r.db.ChatHandle(r.ctx, "s1")
	h2, _ := r.db.ChatHandle(r.ctx, "s2")
	menuMessage(t, r, "s1", hooks.Menu{Tool: hooks.ToolAskUserQuestion, Questions: []hooks.MenuQuestion{{Question: "q", Options: []hooks.MenuOption{{Label: "a"}, {Label: "b"}}}}})
	r.say("me", fmt.Sprintf("屏幕 %d", h1)) // not a show of the menu
	r.say("me", "列表")                     // this one is
	r.say("me", fmt.Sprintf("%d 就这样", h2))
	if len(r.term.pasted("vp_s2")) != 0 {
		t.Fatalf("a menu answer went to [%d]: %q", h2, r.term.pasted("vp_s2"))
	}
	if got := r.term.pasted("vp_s1"); len(got) != 1 || got[0] != fmt.Sprintf("%d 就这样", h2) {
		t.Fatalf("pasted into the menu: %q, reply %q", got, r.ad.last())
	}
}

// A button from an earlier menu does not answer the menu the session asks
// next, even when the question and option numbers line up.
func TestAnOldMenusButtonDoesNotAnswerTheNextMenu(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true, Buttons: true, Edit: true, QuoteRefs: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	row := r.session("s1", "x", "claude", session.StateWaiting)
	single := hooks.Menu{Tool: hooks.ToolAskUserQuestion, Questions: []hooks.MenuQuestion{{Question: "first", Options: []hooks.MenuOption{{Label: "a"}, {Label: "b"}}}}}
	old := menuMessage(t, r, "s1", single)
	r.pushed(row, session.StateWaiting, 1)
	single.Questions[0].Question = "second"
	menuMessage(t, r, "s1", single)
	r.press(fmt.Sprintf("menu:s1:%d:0:1", old), "m1")
	if len(r.term.pressed("vp_s1")) != 0 || !strings.Contains(r.ad.last(), "second") {
		t.Fatalf("an old menu's button: %v %q", r.term.pressed("vp_s1"), r.ad.last())
	}
}
