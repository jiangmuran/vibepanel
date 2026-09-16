package hooks

import (
	"strings"
	"testing"
)

// What Claude Code sent for the question menu in the owner's session on
// 2026-09-16, trimmed: a PreToolUse for AskUserQuestion, whose input is the
// whole menu.
const askDoc = `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[
 {"question":"用量面板最该突出的数字是什么？","header":"主数字","multiSelect":false,"options":[
   {"label":"拆开看，输出为主 (推荐)","description":"主位显示输出 token","preview":"今天 …"},
   {"label":"总量为主，不变","description":"","preview":"…"}]},
 {"question":"Pick pets","header":"Pets","multiSelect":true,"options":[{"label":"Cat"},{"label":"Dog"},{"label":"Fish"}]}]}}`

func TestAMenuIsReadFromTheToolCallThatDrawsIt(t *testing.T) {
	r, ok := Extract([]byte(askDoc))
	if !ok || r.Kind != KindQuestion || r.Menu == nil {
		t.Fatalf("report: %+v %v", r, ok)
	}
	m := r.Menu
	if m.Tool != ToolAskUserQuestion || len(m.Questions) != 2 {
		t.Fatalf("menu: %+v", m)
	}
	q0, q1 := m.Questions[0], m.Questions[1]
	if q0.Header != "主数字" || !q0.Preview || q0.Multi || len(q0.Options) != 2 || q0.Options[0].Description != "主位显示输出 token" {
		t.Fatalf("first question: %+v", q0)
	}
	if !q1.Multi || q1.Preview || q1.Options[2].Label != "Fish" {
		t.Fatalf("second question: %+v", q1)
	}
	if !strings.Contains(r.Text, "主数字: 用量面板最该突出的数字是什么？\n1. 拆开看") {
		t.Fatalf("summary: %q", r.Text)
	}

	plan, ok := Extract([]byte(`{"hook_event_name":"PreToolUse","tool_name":"ExitPlanMode","tool_input":{"plan":"1. write hello.txt"}}`))
	if !ok || plan.Kind != KindQuestion || plan.Menu == nil || plan.Menu.Tool != ToolExitPlanMode || plan.Menu.Plan != "1. write hello.txt" {
		t.Fatalf("plan: %+v", plan)
	}
	// Any other tool before it runs is a state, not a message.
	if _, ok := Extract([]byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`)); ok {
		t.Fatal("a PreToolUse for Bash became a message")
	}
	// A permission request for another tool is still a prompt.
	if r, _ := Extract([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)); r.Kind != KindPrompt || r.Menu != nil {
		t.Fatalf("permission request: %+v", r)
	}
	// Nor are too many questions.
	var many strings.Builder
	many.WriteString(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[`)
	for i := 0; i < 9; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"question":"q","options":[{"label":"x"}]}`)
	}
	many.WriteString(`]}}`)
	if r, _ := Extract([]byte(many.String())); r.Menu != nil {
		t.Fatalf("nine questions: %+v", r.Menu)
	}
	// A menu too big to be Claude Code's is not stored as one.
	var big strings.Builder
	big.WriteString(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"q","options":[`)
	for i := 0; i < 12; i++ {
		if i > 0 {
			big.WriteString(",")
		}
		big.WriteString(`{"label":"x"}`)
	}
	big.WriteString(`]}]}}`)
	if r, _ := Extract([]byte(big.String())); r.Menu != nil {
		t.Fatalf("an oversized menu: %+v", r.Menu)
	}
}
