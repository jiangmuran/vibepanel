package hooks

import (
	"strings"
	"testing"
)

// Each document below is one Claude Code 2.1.273 sent, trimmed of the fields
// every document carries (session_id, cwd, transcript_path, permission_mode).
// They were captured in a throwaway tmux with every hook event logging its
// stdin; the build log entry has the sequences they came from.
func TestReadCorrectsWhatTheHookWasInstalledToReport(t *testing.T) {
	cases := []struct {
		name     string
		reported string
		doc      string
		want     Reading
	}{
		{"the idle notification a minute after every turn is not a person being needed",
			"waiting",
			`{"hook_event_name":"Notification","message":"Claude is waiting for your input","notification_type":"idle_prompt"}`,
			Reading{}},
		{"a permission notification is a menu a keystroke answers",
			"waiting",
			`{"hook_event_name":"Notification","message":"Claude needs your permission","notification_type":"permission_prompt"}`,
			Reading{State: "waiting", Answerable: true}},
		{"so is a background worker's",
			"waiting",
			`{"hook_event_name":"Notification","message":"worker needs permission for Bash","notification_type":"worker_permission_prompt"}`,
			Reading{State: "waiting", Answerable: true}},
		{"a permission request for a command is a menu",
			"waiting",
			`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"python3 -c \"import time; time.sleep(40)\""}}`,
			Reading{State: "waiting", Answerable: true}},
		{"a question dialog is not: it takes a keystroke per question",
			"waiting",
			`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Red or blue?"}]}}`,
			Reading{State: "waiting"}},
		{"a background agent that needs a person is waiting",
			"waiting",
			`{"hook_event_name":"Notification","message":"worker needs your input","notification_type":"agent_needs_input"}`,
			Reading{State: "waiting"}},
		{"a sign-in is not a person being needed",
			"waiting",
			`{"hook_event_name":"Notification","message":"Claude Code login successful","notification_type":"auth_success"}`,
			Reading{}},
		{"a push the agent chose to send is not a prompt",
			"waiting",
			`{"hook_event_name":"Notification","message":"build is green","notification_type":"push_notification"}`,
			Reading{}},
		{"a notification type nobody has listed keeps meaning waiting",
			"waiting",
			`{"hook_event_name":"Notification","message":"?","notification_type":"something_new"}`,
			Reading{State: "waiting"}},
		{"a notification that does not say why keeps meaning waiting",
			"waiting",
			`{"hook_event_name":"Notification","message":"Claude needs your permission"}`,
			Reading{State: "waiting"}},
		{"a turn that launched a background agent is still working",
			"done",
			`{"hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"launched","background_tasks":[{"id":"ac38f443843af140c","type":"subagent","status":"running","description":"Background task: sleep and list files","agent_type":"general-purpose"}],"session_crons":[]}`,
			Reading{State: "working"}},
		{"a background shell does not hold a finished turn open",
			"done",
			`{"hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"started","background_tasks":[{"id":"bq2i4lu60","type":"shell","status":"running","description":"Sleep for 45 seconds then print fin","command":"sleep 45; echo fin"}],"session_crons":[]}`,
			Reading{State: "done"}},
		{"nor does a monitor",
			"done",
			`{"hook_event_name":"Stop","background_tasks":[{"id":"m1","type":"monitor","status":"running","description":"CI"}]}`,
			Reading{State: "done"}},
		{"an agent that already finished does not hold it either",
			"done",
			`{"hook_event_name":"Stop","background_tasks":[{"id":"a1","type":"subagent","status":"completed","description":"x"}]}`,
			Reading{State: "done"}},
		{"a pending workflow holds it",
			"done",
			`{"hook_event_name":"Stop","background_tasks":[{"id":"w1","type":"workflow","status":"pending","description":"x"}]}`,
			Reading{State: "working"}},
		{"a scheduled wakeup is not work in flight",
			"done",
			`{"hook_event_name":"Stop","background_tasks":[],"session_crons":[{"id":"c1","schedule":"*/5 * * * *","recurring":true,"prompt":"check"}]}`,
			Reading{State: "done"}},
		{"a turn that died on an API error waits, and Enter does not answer it",
			"waiting",
			`{"hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":""}`,
			Reading{State: "waiting"}},
		{"a background task of a type nobody listed does not hold a turn open",
			"done",
			`{"hook_event_name":"Stop","background_tasks":[{"id":"t1","type":"teammate","status":"running","description":"x"}]}`,
			Reading{State: "done"}},
		{"Escape during a running tool is the end of the turn",
			"working",
			`{"hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_input":{"command":"sleep 30"},"tool_use_id":"t","error":"interrupted","is_interrupt":true}`,
			Reading{State: "done"}},
		{"a tool that failed on its own is still a turn going on",
			"working",
			`{"hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_input":{"command":"false"},"tool_use_id":"t","error":"exit 1","is_interrupt":false}`,
			Reading{State: "working"}},
		{"a session opening is done",
			"done",
			`{"hook_event_name":"SessionStart","source":"startup"}`,
			Reading{State: "done"}},
		{"a session cleared is done",
			"done",
			`{"hook_event_name":"SessionStart","source":"clear"}`,
			Reading{State: "done"}},
		{"a compaction in the middle of a turn changes nothing",
			"done",
			`{"hook_event_name":"SessionStart","source":"compact"}`,
			Reading{}},
		{"a subagent's tool call says which subagent",
			"working",
			`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"sleep 20"},"agent_id":"ac38f443843af140c","agent_type":"general-purpose"}`,
			Reading{State: "working", Agent: "ac38f443843af140c"}},
		{"an empty document is the old script, and changes nothing",
			"waiting", ``, Reading{State: "waiting"}},
		{"a document that is not JSON costs the reading, never the state",
			"done", `{"hook_event_name":"Stop","background_tasks":[{"type":"subagent","status":"running"`, Reading{State: "done"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Read(c.reported, []byte(c.doc)); got != c.want {
				t.Fatalf("Read(%q) =\n  %+v\nwant\n  %+v", c.reported, got, c.want)
			}
		})
	}
}

// The subagent id goes into the detector's memory and nowhere else, but it is
// still a string a hook document chose.
func TestTheAgentIDIsBounded(t *testing.T) {
	got := Read("working", []byte(`{"hook_event_name":"PreToolUse","agent_id":"`+strings.Repeat("a", 10000)+"\\u001b[31m"+`"}`))
	if n := len([]rune(got.Agent)); n > 128 {
		t.Fatalf("agent id kept at %d runes", n)
	}
	if strings.ContainsRune(got.Agent, 0x1b) {
		t.Fatal("agent id kept an escape")
	}
}

// A rule for an event nothing installs is a rule that never runs. These are the
// events Read has something to say about for Claude Code, and the installer
// has to write each of them -- with the state the rule was written against.
func TestEveryEventReadHasARuleForIsInstalled(t *testing.T) {
	want := map[string]string{
		"Notification":       "waiting",
		"PermissionRequest":  "waiting",
		"PreToolUse":         "working",
		"PostToolUseFailure": "working",
		"SessionStart":       "done",
		"Stop":               "done",
		"StopFailure":        "waiting",
		// Not read, but the answer to a prompt: without it an approved tool
		// stands as waiting until the next one starts.
		"PostToolUse": "working",
	}
	for event, state := range want {
		if got, ok := events[event]; !ok || got != state {
			t.Errorf("events[%q] = %q (installed %v), want %q", event, got, ok, state)
		}
	}
}
