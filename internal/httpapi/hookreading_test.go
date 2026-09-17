package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The reports a Claude Code session sends in one afternoon, through the real
// handler and the real poller: the reading in internal/hooks and the rules in
// the detector are only worth anything if the row a person sees ends up right.
func TestClaudeCodesReportsEndUpAsTheRightState(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"hooked"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":["sleep","600"]}`)
	token, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	report := func(state, doc string) {
		t.Helper()
		doc = strings.Replace(doc, "{", `{"transcript_path":"`+transcript+`",`, 1)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state?sessionId="+sess.ID+"&state="+state, strings.NewReader(doc))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("report %s: status %d", doc, res.StatusCode)
		}
	}
	want := func(st session.State, why string) {
		t.Helper()
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatal(err)
		}
		row, err := srv.DB.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.State != st {
			t.Fatalf("state %q from %q, want %q: %s", row.State, row.StateSource, st, why)
		}
	}

	report("working", `{"hook_event_name":"UserPromptSubmit","prompt":"hello"}`)
	report("done", `{"hook_event_name":"Stop","last_assistant_message":"hello","background_tasks":[]}`)
	want(session.StateDone, "a finished turn")

	report("waiting", `{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`)
	want(session.StateDone, "the idle notification a minute later is not a question")
	msgs, _ := srv.DB.ListSessionMessages(ctx, sess.ID, 0)
	for _, m := range msgs {
		if strings.Contains(m.Text, "waiting for your input") {
			t.Fatalf("the idle notification was stored as a %s message, which the chat bridge pushes", m.Kind)
		}
	}

	report("waiting", `{"hook_event_name":"Notification","notification_type":"auth_success","message":"Claude Code login successful"}`)
	want(session.StateDone, "signing in is not a person being needed")

	report("working", `{"hook_event_name":"UserPromptSubmit","prompt":"audit it with seven agents"}`)
	report("done", `{"hook_event_name":"Stop","last_assistant_message":"7 agents started","background_tasks":[{"id":"a1","type":"subagent","status":"running","description":"audit"}]}`)
	want(session.StateWorking, "the turn ended with agents still running")
	report("working", `{"hook_event_name":"PreToolUse","agent_id":"a1","tool_name":"Read","tool_input":{"file_path":"/x"}}`)
	report("waiting", `{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`)
	// Before the poller looks, too. A refused report written to the row anyway
	// is corrected two seconds later, and by then the transition to done and
	// back has gone out to every webhook and every phone.
	if row, _ := srv.DB.GetSession(ctx, sess.ID); row.State != session.StateWorking {
		t.Fatalf("a refused report wrote %q to the row", row.State)
	}
	want(session.StateWorking, "the idle notification arrived while the agents were running")

	report("working", `{"hook_event_name":"UserPromptSubmit","prompt":"<task-notification>"}`)
	report("working", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"sleep 40"}}`)
	want(session.StateWorking, "a tool is running")

	// A question dialog: one key does not answer a dialog of several questions,
	// and a background agent's progress does not either -- not even for the
	// moment before the poller looks, when a refused report written to the row
	// anyway would already have gone out to every webhook and phone.
	report("waiting", `{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"a?"},{"question":"b?"}]}}`)
	time.Sleep(1100 * time.Millisecond) // past the window in which any working report is taken as late
	report("working", `{"hook_event_name":"PostToolUse","agent_id":"a1","tool_name":"Read","tool_input":{"file_path":"/x"}}`)
	if row, _ := srv.DB.GetSession(ctx, sess.ID); row.State != session.StateWaiting {
		t.Fatalf("a background agent's PostToolUse wrote %q over the main thread's question", row.State)
	}
	time.Sleep(10 * time.Millisecond) // the key has to be later than the report, on a clock with that resolution
	srv.HandleKey(sess.ID)
	want(session.StateWaiting, "one keystroke into a two-question dialog")

	interrupt := func(at time.Time) {
		t.Helper()
		line := `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `"}` + "\n"
		f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(line)
		f.Close()
	}
	// A stamp far in the future is a stepped clock or a forged line, and
	// believing it would end every report after it.
	interrupt(time.Now().Add(time.Hour))
	want(session.StateWaiting, "an interrupt stamped an hour from now")

	// Escape. No hook fires; the transcript records it, stamped now.
	interrupt(time.Now())
	want(session.StateDone, "the person pressed Escape, and the transcript says so")
}

// A permission prompt approved from a phone. The bridge sends keys to tmux
// directly, past the browser's input path, and Claude Code reports nothing
// until the approved tool has finished -- so without the keys counting as
// input the session stayed a triangle for as long as the tool ran.
func TestKeysFromTheChatBridgeAnswerAPrompt(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"hooked"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":["sleep","600"]}`)
	token, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Attached, as the poller attaches every live session.
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state?sessionId="+sess.ID+"&state=waiting",
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make verify"}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if row, _ := srv.DB.GetSession(ctx, sess.ID); row.State != session.StateWaiting {
		t.Fatalf("setup: state %q, want waiting", row.State)
	}

	// Past lateReport: a working report inside it would be refused as late,
	// but a key is not a report, and this sleep is only so the key is
	// strictly later than the prompt on the clock.
	time.Sleep(10 * time.Millisecond)
	if err := srv.chatTerminal().Keys(ctx, sess.TmuxName, "1"); err != nil {
		t.Fatal(err)
	}
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if row, _ := srv.DB.GetSession(ctx, sess.ID); row.State != session.StateWorking {
		t.Fatalf("state %q after the prompt was answered from a phone, want working", row.State)
	}
}
