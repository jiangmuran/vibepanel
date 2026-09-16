package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Menus: an agent asking with options rather than for permission.
//
// Claude Code draws two. AskUserQuestion is one or more questions, each with
// options, single or multiple choice, with a "Type something" row for an
// answer of the person's own (or, when options carry previews, notes on an
// option instead), then a review page. ExitPlanMode is a plan with "yes,
// auto", "yes, approve edits" and a row for what to change instead.
//
// What reaches the pane is keys, and which keys was measured against Claude
// Code 2.1 in tmux rather than guessed (build log, 2026-09-16): a digit picks
// a single-choice option and moves on, and toggles a multiple-choice one; the
// own-answer row is reached with Down and typed into; a multiple-choice
// question is left from its Submit row; the review page's "Submit answers" is
// 1; "Chat about this" is the digit after "Type something"; with previews, n
// opens an option's notes, Escape leaves them, Enter chooses; plan feedback
// is typed into its third row.
//
// A reply is read one question at a time: digits choose, anything else is
// the person's own answer, 「跳过」 declines. The card carries the question
// and its options, and the same shown-first rule as a permission prompt
// applies: a bare "2" answers only a menu this person was shown.

// menuProgress is how far a person got through a menu with several
// questions: the message it was, and the question next to answer.
type menuProgress struct {
	message int64
	next    int
}

// currentMenu is the menu a session is waiting on, if it is, and which of its
// questions is next.
func (b *Bridge) currentMenu(ctx context.Context, sessionID string) (store.Session, store.SessionMessage, *hooks.Menu, int, bool) {
	row, err := b.d.DB.GetSession(ctx, sessionID)
	if err != nil || !Addressable(row) {
		return store.Session{}, store.SessionMessage{}, nil, 0, false
	}
	cur, kind := b.current(ctx, row)
	if kind != store.MessageQuestion || cur.Menu == "" {
		return row, cur, nil, 0, false
	}
	mm, next := b.menuOf(cur)
	if mm == nil {
		return row, cur, nil, 0, false
	}
	return row, cur, mm, next, true
}

// menuOf is a message's menu and the question next to answer, or nil.
func (b *Bridge) menuOf(msg store.SessionMessage) (*hooks.Menu, int) {
	if msg.Menu == "" {
		return nil, 0
	}
	var m hooks.Menu
	if json.Unmarshal([]byte(msg.Menu), &m) != nil || (m.Tool == hooks.ToolAskUserQuestion && len(m.Questions) == 0) {
		return nil, 0
	}
	b.mu.Lock()
	pr := b.menus[msg.SessionID]
	b.mu.Unlock()
	if pr.message == msg.ID && pr.next < len(m.Questions) {
		return &m, pr.next
	}
	return &m, 0
}

// renderMenu is one question of a menu, or the plan, for a phone.
func renderMenu(m *hooks.Menu, next int, lang string) string {
	var b strings.Builder
	if m.Tool == hooks.ToolExitPlanMode {
		plan, more := cut(m.Plan, 1500)
		if more {
			plan += " …"
		}
		b.WriteString(msg(lang, "menuPlan") + "\n" + plan + "\n\n")
		b.WriteString(msg(lang, "menuPlanHint"))
		return b.String()
	}
	q := m.Questions[next]
	if len(m.Questions) > 1 {
		b.WriteString(msg(lang, "menuStep", next+1, len(m.Questions)) + " ")
	}
	if q.Header != "" {
		b.WriteString(q.Header + " · ")
	}
	b.WriteString(q.Question)
	for i, o := range q.Options {
		fmt.Fprintf(&b, "\n%d. %s", i+1, o.Label)
		if d, more := cut(o.Description, 90); d != "" {
			if more {
				d += " …"
			}
			b.WriteString("\n   " + d)
		}
	}
	b.WriteString("\n\n")
	switch {
	case q.Preview && q.Multi:
		b.WriteString(msg(lang, "menuAtLaptop"))
	case q.Preview:
		b.WriteString(msg(lang, "menuHintPreview"))
	case q.Multi:
		b.WriteString(msg(lang, "menuHintMulti"))
	default:
		b.WriteString(msg(lang, "menuHintSingle"))
	}
	return b.String()
}

// menuButtons are a question's options as buttons, where a tap is a whole
// answer: single choice, and the plan. A multiple choice is answered in text.
func menuButtons(sessionID string, messageID int64, m *hooks.Menu, next int, lang string) []Button {
	value := func(opt int) string {
		return fmt.Sprintf("menu:%s:%d:%d:%d", sessionID, messageID, next, opt)
	}
	if m.Tool == hooks.ToolExitPlanMode {
		return []Button{
			{Label: pick(lang, "按计划执行", "Go ahead"), Value: value(1)},
			{Label: pick(lang, "执行，逐个确认修改", "Go, approve edits"), Value: value(2)},
		}
	}
	q := m.Questions[next]
	if q.Multi {
		return nil
	}
	var out []Button
	for i, o := range q.Options {
		label, more := cut(o.Label, 18)
		if more {
			label += "…"
		}
		out = append(out, Button{Label: fmt.Sprintf("%d. %s", i+1, label), Value: value(i + 1)})
	}
	if !q.Preview {
		out = append(out, Button{Label: pick(lang, "跳过", "Skip"), Value: value(0)})
	}
	return out
}

// menuAnswer is one reply to one question.
type menuAnswer struct {
	choices []int
	text    string
	skip    bool
}

var (
	menuDigits     = regexp.MustCompile(`^[0-9\s,，、]+$`)
	menuChoiceText = regexp.MustCompile(`^([0-9])\s*[,，、。.]\s*(\S.*)$`)
	// The words start at the first thing that is not a number or a
	// separator: "1、3，仓鼠" is 1 and 3, and 仓鼠.
	menuMultiText = regexp.MustCompile(`^([0-9][0-9\s,，、]*?)\s*[,，、]\s*([^0-9\s,，、].*)$`)
)

var skipWords = map[string]bool{"跳过": true, "skip": true, "先不答": true, "不答": true, "聊聊": true, "chat": true}

// parseMenuAnswer reads a reply to one question, or says why it cannot.
func parseMenuAnswer(text string, m *hooks.Menu, next int) (menuAnswer, string) {
	s := strings.TrimSpace(narrow(text))
	if s == "" {
		return menuAnswer{}, "menuEmpty"
	}
	digits := func(part string, max int) ([]int, bool) {
		var out []int
		seen := map[int]bool{}
		for _, f := range strings.FieldsFunc(part, func(r rune) bool { return r == ' ' || r == ',' || r == '，' || r == '、' }) {
			n, err := strconv.Atoi(f)
			if err != nil || n < 1 || n > max {
				return nil, false
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		return out, len(out) > 0
	}
	if m.Tool == hooks.ToolExitPlanMode {
		if menuDigits.MatchString(s) {
			c, ok := digits(s, 2)
			if !ok || len(c) != 1 {
				return menuAnswer{}, "menuPlanPick"
			}
			return menuAnswer{choices: c}, ""
		}
		return menuAnswer{text: strings.TrimSpace(text)}, ""
	}
	q := m.Questions[next]
	if skipWords[Normalize(s)] {
		return menuAnswer{skip: true}, ""
	}
	n := len(q.Options)
	switch {
	case menuDigits.MatchString(s):
		c, ok := digits(s, n)
		if !ok {
			return menuAnswer{}, "menuOutOfRange"
		}
		if !q.Multi && len(c) != 1 {
			return menuAnswer{}, "menuOnlyOne"
		}
		return menuAnswer{choices: c}, ""
	case !q.Multi && menuChoiceText.MatchString(s):
		mm := menuChoiceText.FindStringSubmatch(s)
		c, ok := digits(mm[1], n)
		if !ok {
			return menuAnswer{}, "menuOutOfRange"
		}
		return menuAnswer{choices: c, text: mm[2]}, ""
	case q.Multi && menuMultiText.MatchString(s):
		mm := menuMultiText.FindStringSubmatch(s)
		c, ok := digits(mm[1], n)
		if !ok {
			return menuAnswer{}, "menuOutOfRange"
		}
		return menuAnswer{choices: c, text: mm[2]}, ""
	}
	return menuAnswer{text: strings.TrimSpace(text)}, ""
}

// menuStep is one thing done to the pane: keys pressed, or text pasted.
type menuStep struct {
	keys  []string
	paste string
}

func downs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "Down"
	}
	return out
}

// menuSteps is what answering one question puts into the pane, or ok false
// when this kind of question cannot be answered from a phone. The cursor is
// on a question's first row when it is drawn.
func menuSteps(m *hooks.Menu, next int, a menuAnswer) ([]menuStep, bool) {
	if m.Tool == hooks.ToolExitPlanMode {
		if a.text != "" {
			return []menuStep{{keys: downs(2)}, {paste: a.text}, {keys: []string{"Enter"}}}, true
		}
		return []menuStep{{keys: append(downs(a.choices[0]-1), "Enter")}}, true
	}
	q := m.Questions[next]
	n := len(q.Options)
	var steps []menuStep
	switch {
	case q.Preview && q.Multi:
		return nil, false
	case q.Preview:
		// No own-answer row: words are notes, on the option chosen or on
		// nothing. Enter while the notes are open submits notes only, so a
		// chosen option's notes are closed with Escape first.
		if a.skip {
			return []menuStep{{keys: []string{"Escape"}}}, true
		}
		if len(a.choices) == 0 {
			steps = []menuStep{{keys: []string{"n"}}, {paste: a.text}, {keys: []string{"Enter"}}}
			break
		}
		steps = []menuStep{{keys: downs(a.choices[0] - 1)}}
		if a.text != "" {
			steps = append(steps, menuStep{keys: []string{"n"}}, menuStep{paste: a.text}, menuStep{keys: []string{"Escape"}})
		}
		steps = append(steps, menuStep{keys: []string{"Enter"}})
	case q.Multi:
		if a.skip {
			return []menuStep{{keys: []string{"Escape"}}}, true
		}
		for _, c := range a.choices {
			steps = append(steps, menuStep{keys: []string{strconv.Itoa(c)}})
		}
		if a.text != "" {
			// Typing into the own-answer row ticks it.
			steps = append(steps, menuStep{keys: downs(n)}, menuStep{paste: a.text}, menuStep{keys: []string{"Down", "Enter"}})
		} else {
			steps = append(steps, menuStep{keys: append(downs(n+1), "Enter")})
		}
	default:
		switch {
		case a.skip:
			steps = []menuStep{{keys: []string{strconv.Itoa(n + 2)}}}
		case len(a.choices) == 1 && a.text == "":
			steps = []menuStep{{keys: []string{strconv.Itoa(a.choices[0])}}}
		default:
			// An option with words, or words alone: without notes on this
			// screen, both are the person's own answer.
			text := a.text
			if len(a.choices) == 1 {
				text = q.Options[a.choices[0]-1].Label + ": " + a.text
			}
			steps = []menuStep{{keys: downs(n)}, {paste: text}, {keys: []string{"Enter"}}}
		}
	}
	// The last of several questions leads to the review page, whose first
	// row submits. A skip does not: it leaves the menu.
	if len(m.Questions) > 1 && next == len(m.Questions)-1 && !a.skip {
		steps = append(steps, menuStep{keys: []string{"1"}})
	}
	return steps, true
}

// describeMenuAnswer is what a receipt and the audit say was answered.
func describeMenuAnswer(m *hooks.Menu, next int, a menuAnswer, lang string) string {
	if a.skip {
		return msg(lang, "menuSkipped")
	}
	var labels []string
	if m.Tool == hooks.ToolExitPlanMode {
		if len(a.choices) == 1 {
			return pick(lang, []string{"", "按计划执行（自动）", "执行，逐个确认修改"}[a.choices[0]], []string{"", "go ahead (auto)", "go ahead, approving edits"}[a.choices[0]])
		}
		return msg(lang, "menuFeedback", preview(a.text))
	}
	q := m.Questions[next]
	sort.Ints(a.choices)
	for _, c := range a.choices {
		labels = append(labels, q.Options[c-1].Label)
	}
	if a.text != "" {
		labels = append(labels, "「"+preview(a.text)+"」")
	}
	return strings.Join(labels, pick(lang, "、", ", "))
}

// answerMenu answers the question a session's menu is on.
func (b *Bridge) answerMenu(ctx context.Context, p store.ChatPeer, target Target, bound int64, text string, lang string) said {
	row, cur, m, next, ok := b.currentMenu(ctx, target.SessionID)
	h, _ := b.Handle(ctx, target.SessionID)
	if !ok {
		return plain(msg(lang, "notWaiting", h))
	}
	show := func(key string) said {
		return showing(msg(lang, key, h)+"\n"+renderMenu(m, next, lang), row.ID, cur.ID)
	}
	if bound < 0 || (bound > 0 && bound != cur.ID) {
		return show("menuStale")
	}
	if bound == 0 && !b.seen(ctx, p, cur.ID) {
		return show("menuUnseen")
	}
	a, why := parseMenuAnswer(text, m, next)
	if why != "" {
		return show(why)
	}
	steps, ok := menuSteps(m, next, a)
	if !ok {
		return plain(msg(lang, "menuAtLaptop") + msg(lang, "menuScreen", h))
	}
	for _, st := range steps {
		var err error
		if st.paste != "" {
			err = b.d.Term.Paste(ctx, row.TmuxName, st.paste)
		} else {
			err = b.d.Term.Keys(ctx, row.TmuxName, st.keys...)
		}
		if err != nil {
			return plain(msg(lang, "gone", h))
		}
	}
	what := describeMenuAnswer(m, next, a, lang)
	b.d.Audit(ctx, "chat.answered", fmt.Sprintf("[%d] %s · %s via %s: %s", h, row.Title, who(p), target.How, preview(what)))

	last := a.skip || m.Tool == hooks.ToolExitPlanMode || next >= len(m.Questions)-1
	b.mu.Lock()
	if last {
		delete(b.menus, row.ID)
	} else {
		b.menus[row.ID] = menuProgress{message: cur.ID, next: next + 1}
	}
	b.mu.Unlock()
	if !last {
		// The next question, shown, so a bare "1" to it counts as seen.
		return showing(msg(lang, "menuReceiptNext", h, what)+"\n"+renderMenu(m, next+1, lang), row.ID, cur.ID)
	}
	b.close(ctx, row, h, cur.ID, cur.Text, msg(lang, "menuAnsweredBy", who(p)), &p, "")
	return plain(msg(lang, "menuReceipt", h, what))
}

// menuCardBody is what a card shows for a menu instead of its summary.
func (b *Bridge) menuCardBody(ctx context.Context, sessionID string, lang string) (string, *hooks.Menu, int, int64) {
	_, cur, m, next, ok := b.currentMenu(ctx, sessionID)
	if !ok {
		return "", nil, 0, 0
	}
	return renderMenu(m, next, lang), m, next, cur.ID
}

// pressMenu answers a menu from a button: "<question>:<option>", option 0 to
// skip. The press is for the question it was drawn with; one for a question
// already answered shows the one the menu is on now.
func (b *Bridge) pressMenu(ctx context.Context, p store.ChatPeer, sessionID string, bound int64, extra string, lang string) said {
	qs, os, _ := strings.Cut(extra, ":")
	qi, err1 := strconv.Atoi(qs)
	opt, err2 := strconv.Atoi(os)
	if err1 != nil || err2 != nil {
		return said{}
	}
	if bound == 0 {
		bound = -1
	}
	if _, cur, m, next, ok := b.currentMenu(ctx, sessionID); ok && cur.ID == bound && qi != next {
		h, _ := b.Handle(ctx, sessionID)
		return showing(msg(lang, "menuStale", h)+"\n"+renderMenu(m, next, lang), sessionID, cur.ID)
	}
	text := strconv.Itoa(opt)
	if opt == 0 {
		text = "skip"
	}
	return b.answerMenu(ctx, p, Target{SessionID: sessionID, How: HowButton}, bound, text, lang)
}
