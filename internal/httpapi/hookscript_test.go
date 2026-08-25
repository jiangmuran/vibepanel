package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// TestTheReporterScriptActuallyReportsState walks the whole hook path with the
// real script: the file that gets installed into somebody's agent
// configuration, the environment the panel injects into a session, curl, the
// endpoint, the token check, the detector, and the state the sidebar reads.
//
// Each of those pieces had a test. The path did not. The two script tests are
// both negative — it no-ops with no environment, and it refuses an unknown
// state — so nothing established that the positive case works at all.
//
// That matters more than it looks. Without hooks the panel infers state from
// the byte stream, and the only signal that reaches it is the terminal bell:
// tmux swallows OSC 9 and OSC 777 before the panel can see them, and the agent
// most people run here does not ring. So this script is not an optional
// enhancement to state detection, it is most of state detection, and it was
// the one part of it running outside Go and unexercised end to end.
func TestTheReporterScriptActuallyReportsState(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed; the reporter shells out to it")
	}
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"hooked"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	token, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatalf("HookToken: %v", err)
	}
	script, err := hooks.InstallScript(t.TempDir())
	if err != nil {
		t.Fatalf("InstallScript: %v", err)
	}

	run := func(state, tok string) {
		t.Helper()
		cmd := exec.Command(script, state)
		cmd.Env = append(os.Environ(),
			"VIBEPANEL_SESSION_ID="+sess.ID,
			"VIBEPANEL_TOKEN="+tok,
			"VIBEPANEL_URL="+ts.URL,
		)
		out, rerr := cmd.CombinedOutput()
		// The script promises never to fail and never to print: an agent hook
		// that errors costs more than a missed state update.
		if rerr != nil {
			t.Fatalf("the reporter exited %v, which an agent would surface: %s", rerr, out)
		}
		if len(out) > 0 {
			t.Errorf("the reporter printed %q; it is run by an agent and must be silent", out)
		}
	}

	stateOf := func() string {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatalf("GET /api/state: %v", err)
		}
		defer res.Body.Close()
		var got struct {
			Sessions []store.Session `json:"sessions"`
		}
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatalf("decode state: %v", err)
		}
		for _, s := range got.Sessions {
			if s.ID == sess.ID {
				return string(s.State)
			}
		}
		t.Fatalf("session %s is not in /api/state", sess.ID)
		return ""
	}

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if got := stateOf(); got == want {
				return
			} else if time.Now().After(deadline) {
				t.Fatalf("state is %q, want %q", got, want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	run("waiting", token)
	waitFor("waiting")

	// And back again, so the test is about the path rather than about one
	// value happening to be the default.
	run("done", token)
	waitFor("done")

	// A wrong token must change nothing. The reporter is installed globally
	// and runs wherever an agent runs; the token is what keeps a session on
	// somebody else's panel from being driven by it.
	run("working", "not-the-token")
	time.Sleep(1500 * time.Millisecond)
	if got := stateOf(); got != "done" {
		t.Errorf("state is %q after a report with a bad token; it should still be done", got)
	}
}
