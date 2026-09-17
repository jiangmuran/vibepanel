package hooks

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestExtractReadsEachAgentEvent(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Report
	}{
		{"stop carries the last message",
			`{"hook_event_name":"Stop","session_id":"x","transcript_path":"/t.jsonl","stop_hook_active":false,"last_assistant_message":"Done: three files changed."}`,
			Report{Event: "Stop", Kind: KindAssistant, Text: "Done: three files changed.", TranscriptPath: "/t.jsonl"}},
		{"a permission notification is a prompt",
			`{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash","title":"Permission"}`,
			Report{Event: "Notification", Kind: KindPrompt, Text: "Claude needs your permission to use Bash"}},
		{"an idle notification is not a message, only where the transcript is",
			`{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input","transcript_path":"/t.jsonl"}`,
			Report{Event: "Notification", TranscriptPath: "/t.jsonl"}},
		{"an unknown notification type is a notice",
			`{"hook_event_name":"Notification","notification_type":"auth_success","message":"Signed in"}`,
			Report{Event: "Notification", Kind: KindNotice, Text: "Signed in"}},
		{"a permission request shows the command",
			`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build","description":"clean"},"tool_use_id":"t1"}`,
			Report{Event: "PermissionRequest", Kind: KindPrompt, Text: "Bash: rm -rf build"}},
		{"a permission request for an edit shows the path",
			`{"hook_event_name":"PermissionRequest","tool_name":"Edit","tool_input":{"file_path":"/srv/app/main.go","old_string":"a","new_string":"b"}}`,
			Report{Event: "PermissionRequest", Kind: KindPrompt, Text: "Edit: /srv/app/main.go"}},
		{"a permission request with nothing recognisable compacts the input",
			`{"hook_event_name":"PermissionRequest","tool_name":"mcp__x__y","tool_input":{"n":3}}`,
			Report{Event: "PermissionRequest", Kind: KindPrompt, Text: `mcp__x__y {"n":3}`}},
		{"an elicitation is a question",
			`{"hook_event_name":"Elicitation","server":"s","tool":"ask","prompt":"Which region?"}`,
			Report{Event: "Elicitation", Kind: KindQuestion, Text: "Which region?"}},
		{"the person's own prompt is kept as user",
			`{"hook_event_name":"UserPromptSubmit","prompt":"fix the flaky test"}`,
			Report{Event: "UserPromptSubmit", Kind: KindUser, Text: "fix the flaky test"}},
		{"an interrupt carries no text and says so",
			`{"hook_event_name":"Interrupt","session_id":"x"}`,
			Report{Event: "Interrupt", Kind: KindNotice, Interrupted: true}},
		{"a tool call carries only the transcript path",
			`{"hook_event_name":"PreToolUse","transcript_path":"/t.jsonl","tool_name":"Read","tool_input":{}}`,
			Report{Event: "PreToolUse", TranscriptPath: "/t.jsonl"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Extract([]byte(c.raw))
			if !ok {
				t.Fatalf("not ok")
			}
			if got != c.want {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestExtractRefusesWhatItCannotUse(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"not json",
		`[1,2]`,
		`{"hook_event_name":"PreToolUse","tool_name":"Read"}`, // no path, no text
		`{"hook_event_name":"Stop","last_assistant_message":""}`,
		`{"hook_event_name":"Stop","last_assistant_message":"\u0000\u0001"}`,
		`{"hook_event_name":"Stop","last_assistant_message":"x`, // truncated
	} {
		if r, ok := Extract([]byte(raw)); ok {
			t.Errorf("%q was read as %+v", raw, r)
		}
	}
}

func TestExtractBoundsAndCleansText(t *testing.T) {
	long := strings.Repeat("字", MaxText+100)
	r, ok := Extract([]byte(`{"hook_event_name":"Stop","last_assistant_message":"` + long + `"}`))
	if !ok {
		t.Fatal("not ok")
	}
	if n := len([]rune(r.Text)); n != MaxText {
		t.Fatalf("kept %d runes, want %d", n, MaxText)
	}
	// Runes, not bytes: no half glyph at the end.
	if !strings.HasSuffix(r.Text, "字") {
		t.Fatalf("cut inside a glyph: %q", r.Text[len(r.Text)-6:])
	}

	r, _ = Extract([]byte(`{"hook_event_name":"Stop","last_assistant_message":"a\r\nb\u001b[31mc\td  "}`))
	if r.Text != "a\nbc\td" {
		t.Fatalf("cleaned to %q", r.Text)
	}

	// Past MaxPayload the document is cut and so unparseable; the state
	// report must survive that, which is the caller's job, and this must not
	// panic or read the whole thing.
	huge := `{"hook_event_name":"Stop","last_assistant_message":"` + strings.Repeat("a", MaxPayload) + `"}`
	if _, ok := Extract([]byte(huge)); ok {
		t.Fatal("an over-size document was read")
	}
}

func TestCleanReplacesInvalidUTF8(t *testing.T) {
	got := Clean("ok\xffbad", 100)
	if got != "ok�bad" {
		t.Fatalf("got %q", got)
	}
}

// The kinds this package emits are the store's kinds, spelt as literals on
// both sides because this package does not import the store. Same shape as
// the state check in states_test.go, for the same reason.
func TestPayloadKindsAreTheStoreKinds(t *testing.T) {
	src, err := os.ReadFile("../store/chat.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`Message(?:Assistant|Prompt|Question|Notice|User)\s*=\s*"([a-z]+)"`)
	store := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		store[m[1]] = true
	}
	for _, k := range []string{KindAssistant, KindPrompt, KindQuestion, KindNotice, KindUser} {
		if !store[k] {
			t.Errorf("kind %q is not one the store accepts: %v", k, store)
		}
	}
	if len(store) != 5 {
		t.Errorf("the store has %d kinds and this package knows 5", len(store))
	}
}
