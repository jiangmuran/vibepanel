package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// A reboot, honestly simulated.
//
// The machine going down takes two things at once: the tmux server, and the
// panel process. Killing the server on the test's own throwaway socket is the
// first; DetachAll is the second, because every PTY the manager holds is a
// child of the panel and dies with it. Doing only the first leaves the manager
// holding attachments to a server that is not there, which is a state a real
// reboot never produces and which would make the tests below pass or fail for
// reasons that have nothing to do with restoring.
//
// It touches nothing but the socket newTestServer created. Red line 1.
func simulateReboot(t *testing.T, srv *Server) {
	t.Helper()
	ctx := context.Background()
	srv.Manager.DetachAll()
	if err := srv.Tmux.KillServer(ctx); err != nil {
		t.Fatalf("kill the test's own tmux server: %v", err)
	}
	// kill-server returns before the server has finished going, and a command
	// that arrives in that window gets "server exited unexpectedly" -- which is
	// neither ErrNoServer nor ErrNoSession, so Reconcile propagates it and the
	// test fails somewhere unrelated to what it is checking. A real reboot has
	// no such window: nothing is running to ask.
	//
	// The socket file goes too, the way /tmp does across a reboot.
	_ = os.Remove(srv.Tmux.SocketPath())
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := srv.Tmux.List(ctx); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the tmux server on the test socket never finished shutting down")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForPane polls a pane's capture until it contains want.
//
// A restored pane runs a shell that cats a file and then execs; none of that is
// synchronous with the tmux command that created the session.
func waitForPane(t *testing.T, srv *Server, tmuxName, want string) string {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := srv.Tmux.Capture(ctx, tmuxName)
		if err == nil {
			last = out
			if strings.Contains(out, want) {
				return out
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("pane %s never showed %q; last capture was:\n%s", tmuxName, want, last)
	return ""
}

func getSession(t *testing.T, srv *Server, id string) store.Session {
	t.Helper()
	rec, err := srv.DB.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("get session %s: %v", id, err)
	}
	return rec
}

// A session that survives a reboot is one whose command was written down.
//
// This is the whole of the first half. `command` on the row is
// #{pane_current_command}, which the poller rewrites every two seconds with the
// name of whatever is in the pane -- "sleep" here, "node" for an agent -- and
// handleRestartSession said so and ran a login shell instead. So the panel came
// back from a reboot able to give you a shell wearing an agent's name, which is
// the wrong answer delivered convincingly.
//
// Mutation run: dropping `LaunchCommand: req.Command` from handleCreateSession
// fails this -- the restored pane never prints the marker, because it is a
// login shell.
func TestRestoringRunsTheCommandTheSessionWasCreatedWith(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"restore"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		newSessionBody(t, project.ID, "sh", "-c", "printf 'VP_LAUNCH_MARKER\n'; exec cat"))

	if !sess.LaunchRecorded || len(sess.LaunchCommand) != 3 {
		t.Fatalf("the argv was not recorded: %+v", sess)
	}
	waitForPane(t, srv, sess.TmuxName, "VP_LAUNCH_MARKER")

	// Let the poller's view of the row be written, so that `command` holds the
	// label this test exists to distrust.
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := getSession(t, srv, sess.ID).Command; got == "" || got == "sh" {
		t.Logf("pane_current_command is %q", got)
	}

	simulateReboot(t, srv)

	if err := srv.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	rec := getSession(t, srv, sess.ID)
	if !Restorable(rec) {
		t.Fatalf("after the reboot the session is not marked restorable: exited=%v status=%d",
			rec.Exited, rec.ExitStatus)
	}

	res := restorePost(t, ts, []string{sess.ID})
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("restore said %+v", res)
	}

	out := waitForPane(t, srv, sess.TmuxName, "VP_LAUNCH_MARKER")
	if strings.Count(out, "VP_LAUNCH_MARKER") == 0 {
		t.Fatalf("the restored pane is not running the recorded command:\n%s", out)
	}
	if got := getSession(t, srv, sess.ID); got.RestoredAt == 0 {
		t.Error("restoredAt was not set, so nothing in the UI can say this process is new")
	}
}

// A restore finds a command that only the login shell can find.
//
// This is the case the panel exists for: `claude` lives in ~/.local/bin or
// behind an nvm/mise shim, and the pane inherits the *panel's* PATH, which
// under a systemd unit is /usr/bin:/bin. tmux.LaunchArgv is what notices and
// runs it as `$SHELL -l -c 'exec …'`, so creating the session works. Restoring
// it handed Create `/bin/sh -c <script> … <argv>` instead, and LaunchArgv,
// asked about /bin/sh, found /bin/sh and wrapped nothing: the agent ran under a
// *non-login* shell with the panel's PATH and the pane died with status 127.
// Reboot, and the session that had been running for a week could not be brought
// back — nor restarted, because respawn re-runs the same wrapper.
//
// The unfindable command is arranged inside the test's own tree, with a
// stand-in for the person's login shell that puts a directory on PATH the way a
// profile does. Nothing here writes to the developer's shell configuration.
//
// Mutation run: restoring `tmux.LaunchArgv(rec.LaunchCommand)` to a bare
// `rec.LaunchCommand...` fails this at the second waitForPane, with
// `vibepanel-restore: 3: exec: vpagent: not found` and `Pane is dead (status
// 127)` in the capture.
func TestARestoredSessionFindsWhatOnlyTheLoginShellCanFind(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	// After newTestServer, on purpose: the tmux server is already up, started
	// with the real shell, so the only thing this stand-in changes is the
	// environment of the panes created below. A server whose own PATH carried
	// the directory would hide the bug rather than test it.
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(bin, "vpagent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\nprintf 'VP_AGENT_MARKER\\n'\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(home, "fakelogin")
	script := "#!/bin/sh\nPATH=\"" + bin + ":$PATH\"\nexport PATH\n" +
		"[ \"$1\" = -l ] && shift\nexec /bin/sh \"$@\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)

	if _, err := exec.LookPath("vpagent"); err == nil {
		t.Fatal("vpagent is on this process's PATH, so the pane can find it too and " +
			"there is nothing here for a login shell to fix")
	}

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"restore-path"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", newSessionBody(t, project.ID, "vpagent"))
	waitForPane(t, srv, sess.TmuxName, "VP_AGENT_MARKER")

	simulateReboot(t, srv)
	if err := srv.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	res := restorePost(t, ts, []string{sess.ID})
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("restore said %+v", res)
	}

	waitForPane(t, srv, sess.TmuxName, "VP_AGENT_MARKER")
}

// The scrollback comes back, and it says out loud that it is old.
//
// Both halves in one test on purpose: putting a dead agent's output back on
// screen without marking it is worse than not putting it back at all. Somebody
// reads the last thing the agent said and believes it is still there.
//
// Two mutations run. Removing the archiveOnce call fails it at the old marker:
// there is nothing to put back. Removing the banner from the payload in
// restoreSession fails it at "The process below is new", with the old output
// sitting above a fresh prompt and nothing between them.
func TestRestoredScrollbackComesBackMarkedAsOld(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"scrollback"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		newSessionBody(t, project.ID, "sh", "-c", "printf 'VP_LAUNCH_MARKER\n'; exec cat"))
	waitForPane(t, srv, sess.TmuxName, "VP_LAUNCH_MARKER")

	// Something on the screen that the command cannot produce a second time, so
	// that finding it after the restore can only mean the archive was replayed.
	// `cat` is the pane's process precisely so this echoes.
	if err := srv.Tmux.Paste(ctx, sess.TmuxName, "VP_OLD_SCREEN_MARKER\n"); err != nil {
		t.Fatalf("paste: %v", err)
	}
	waitForPane(t, srv, sess.TmuxName, "VP_OLD_SCREEN_MARKER")

	srv.archiveOnce(ctx)
	sb, err := srv.DB.GetScrollback(ctx, sess.ID)
	if err != nil {
		t.Fatalf("nothing was archived: %v", err)
	}
	if !strings.Contains(string(sb.Content), "VP_OLD_SCREEN_MARKER") {
		t.Fatalf("the archive does not hold what was on screen:\n%s", sb.Content)
	}

	simulateReboot(t, srv)
	if err := srv.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res := restorePost(t, ts, []string{sess.ID}); len(res) != 1 || !res[0].OK {
		t.Fatalf("restore said %+v", res)
	}

	out := waitForPane(t, srv, sess.TmuxName, "VP_OLD_SCREEN_MARKER")
	oldAt := strings.Index(out, "VP_OLD_SCREEN_MARKER")
	bannerAt := strings.Index(out, "The process below is new")
	if bannerAt < 0 {
		t.Fatalf("the restored pane carries no banner; a reader cannot tell this screen "+
			"belongs to a process that no longer exists:\n%s", out)
	}
	if oldAt > bannerAt {
		t.Errorf("the banner is above the old scrollback rather than below it, so it marks "+
			"the wrong half:\n%s", out)
	}

	// The archive is handed back exactly once. Leaving it would replay somebody
	// else's screen into the next restore, under a banner that would be a lie
	// about which process it belonged to.
	if _, err := srv.DB.GetScrollback(ctx, sess.ID); err == nil {
		t.Error("the archive survived being restored")
	}
}

// Restoring is opt-in per session, and the opt-out is the default.
//
// A panel that rebuilt two dozen agents on every boot -- each of them starting
// to work, each of them costing money -- would be a worse failure than the list
// of dead rows it replaced.
//
// Mutation run: making RestoreFlagged ignore row.RestoreOnBoot and restore
// everything fails this at the second assertion, with the unflagged session
// running again.
func TestOnlyTheSessionsThatAskedAreRestoredOnBoot(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"boot"}`)
	body := newSessionBody(t, project.ID, "sh", "-c", "exec cat")
	flagged := postJSON[store.Session](t, ts, "/api/sessions", body)
	plain := postJSON[store.Session](t, ts, "/api/sessions", body)

	patchJSON(t, ts, "/api/sessions/"+flagged.ID, `{"restoreOnBoot":true}`)
	if !getSession(t, srv, flagged.ID).RestoreOnBoot {
		t.Fatal("the flag did not stick")
	}

	simulateReboot(t, srv)
	if err := srv.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if has, _ := srv.Tmux.Has(ctx, flagged.TmuxName); !has {
		t.Error("the session marked restore-on-boot did not come back")
	}
	if has, _ := srv.Tmux.Has(ctx, plain.TmuxName); has {
		t.Error("a session nobody asked for was started on boot; two dozen of these is what " +
			"the opt-in exists to prevent")
	}
	if !Restorable(getSession(t, srv, plain.ID)) {
		t.Error("the unflagged session is not offered for restoring either, so it is simply lost")
	}
}

// A row from before the panel recorded commands must say so.
//
// The dangerous version of this is the quiet one: start a login shell, keep the
// agent's name on the tab, and let somebody find out by typing at it. `”` in
// launch_command is every row written before migration v9, and it has to read
// as "unknown" rather than as "no command", which is a login shell and is a
// different and exactly reproducible thing.
//
// Mutation run: making scanSession treat ” as a recorded empty argv fails
// this -- launchRecorded comes back true for a row the panel knows nothing
// about.
func TestARowFromBeforeV9AdmitsItsCommandIsUnknown(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"old"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		newSessionBody(t, project.ID, "sh", "-c", "exec cat"))

	// What migration v9 leaves on every row that already existed.
	if _, err := srv.DB.SQL().ExecContext(ctx,
		`UPDATE sessions SET launch_command = '' WHERE id = ?`, sess.ID); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	rec := getSession(t, srv, sess.ID)
	if rec.LaunchRecorded {
		t.Error("a row with no recorded command claims to have one")
	}
	if len(rec.LaunchCommand) != 0 {
		t.Errorf("launchCommand = %v, want empty", rec.LaunchCommand)
	}

	// And it still restores -- into a login shell, which is the honest fallback
	// as long as the UI has said that is what will happen.
	simulateReboot(t, srv)
	if err := srv.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res := restorePost(t, ts, []string{sess.ID}); len(res) != 1 || !res[0].OK {
		t.Fatalf("restore said %+v", res)
	}
	if has, _ := srv.Tmux.Has(ctx, sess.TmuxName); !has {
		t.Error("a row with an unknown command could not be restored at all")
	}
}

// One bad session in a batch must not take the other twenty-three with it.
//
// The realistic shape after a reboot: a worktree was pruned while the machine
// was off, so one project directory is gone and the rest are fine.
//
// Mutation run: making handleRestoreSessions return on the first error fails
// this at the second assertion.
func TestRestoreNamesTheOnesItCouldNotBringBack(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"partial"}`)
	body := newSessionBody(t, project.ID, "sh", "-c", "exec cat")
	good := postJSON[store.Session](t, ts, "/api/sessions", body)

	simulateReboot(t, srv)
	if err := srv.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	res := restorePost(t, ts, []string{"no-such-session", good.ID})
	if len(res) != 2 {
		t.Fatalf("got %d results for two ids: %+v", len(res), res)
	}
	if res[0].OK || res[0].Error == "" {
		t.Errorf("the unknown id came back as %+v, with nothing said about it", res[0])
	}
	if !res[1].OK {
		t.Errorf("a good session was not restored because a different one failed: %+v", res[1])
	}
}

// The archive does not rewrite itself for a session that has printed nothing.
//
// Without the gate this captures and rewrites every session's blob on every
// tick: at the byte cap and two dozen sessions that is 6 MB of database writes
// every thirty seconds on a panel where nothing at all is happening, forever,
// on a machine that is expected to stay up for months.
//
// Mutation run: deleting the `seen && prev == row.LastOutputAt` check in
// archiveOnce fails this -- the sentinel is overwritten by a real capture.
func TestArchivingSkipsASessionThatHasNotPrinted(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"idle"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		newSessionBody(t, project.ID, "sh", "-c", "printf VP_IDLE; exec cat"))
	waitForPane(t, srv, sess.TmuxName, "VP_IDLE")

	srv.archiveOnce(ctx)
	if _, err := srv.DB.GetScrollback(ctx, sess.ID); err != nil {
		t.Fatalf("the first pass archived nothing: %v", err)
	}

	// A sentinel that a real capture could never produce. If the second pass
	// captures, this is gone.
	if err := srv.DB.PutScrollback(ctx, store.Scrollback{
		SessionID: sess.ID, CapturedAt: 1, Content: []byte("VP_SENTINEL"),
	}); err != nil {
		t.Fatalf("put sentinel: %v", err)
	}
	srv.archiveOnce(ctx)

	sb, err := srv.DB.GetScrollback(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(sb.Content) != "VP_SENTINEL" {
		t.Errorf("an idle session was captured and written again; the gate on last_output_at "+
			"is not holding. content is now %q", truncate(string(sb.Content)))
	}
}

// The byte cap keeps the end, on a line boundary.
//
// Both halves matter. Keeping the front would archive the beginning of a
// session and throw away what it was doing when the machine went down, which is
// the only part anybody wants. Cutting at an arbitrary byte would put the tail
// of an SGR sequence at the head of the archive, and that gets printed into the
// restored pane as literal text -- the one place where being a few bytes off
// produces visible garbage rather than a missing line.
//
// Mutation run: changing boundScrollback to `b[:max]` fails the first
// assertion; removing the newline seek fails the third.
func TestTheScrollbackCapKeepsTheEndAndCutsOnALine(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		b.WriteString("\x1b[38;5;")
		b.WriteString(strings.Repeat("9", 2))
		b.WriteString("mline ")
		b.WriteString(strings.Repeat("x", 40))
		if i == 199 {
			b.WriteString(" LAST")
		}
		b.WriteString("\n")
	}
	full := []byte(b.String())

	out, truncated := boundScrollback(full, 1000)
	if !truncated {
		t.Fatal("a 10 KB capture was not reported as truncated at a 1 KB cap")
	}
	if !strings.HasSuffix(string(out), "LAST\n") {
		t.Error("the cap kept the front; what somebody wants back is the end")
	}
	if len(out) > 1000 {
		t.Errorf("the cap did not bite: %d bytes", len(out))
	}
	// Exactly: what came back must be preceded by a newline in what went in.
	//
	// Stated this way rather than as "it looks like it starts with an escape",
	// which is what the first version did and which a mid-line cut passes
	// vacuously — the bytes at an arbitrary offset are usually not an escape,
	// and the check then asserts nothing at all.
	if !strings.Contains(string(full), "\n"+string(out)) {
		t.Errorf("the archive does not begin on a line boundary, so a restored pane can be "+
			"handed the tail of an escape sequence as literal text. it starts %q",
			out[:min(len(out), 24)])
	}

	// Under the cap, nothing is touched at all.
	short := []byte("one\ntwo\n")
	got, cut := boundScrollback(short, 1000)
	if cut || string(got) != string(short) {
		t.Errorf("a capture under the cap was modified: %q truncated=%v", got, cut)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────

// newSessionBody builds a create request without hand-escaping JSON inside a Go
// string inside a shell command, which is three levels of quoting and was wrong
// on the first attempt.
func newSessionBody(t *testing.T, projectID string, argv ...string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"projectId": projectID, "command": argv})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func restorePost(t *testing.T, ts *httptest.Server, ids []string) []restoreResult {
	t.Helper()
	body, _ := json.Marshal(restoreSessionsRequest{IDs: ids})
	res, err := ts.Client().Post(ts.URL+"/api/sessions/restore", "application/json",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST restore: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("POST restore: %s: %s", res.Status, b)
	}
	var out struct {
		Results []restoreResult `json:"results"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Results
}

func patchJSON(t *testing.T, ts *httptest.Server, path, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("PATCH %s: %s: %s", path, res.Status, b)
	}
	_, _ = io.Copy(io.Discard, res.Body)
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
