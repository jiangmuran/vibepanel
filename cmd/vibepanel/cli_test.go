package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/httpapi"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/ws"
)

// The CLI against a real panel: an httpapi.Server on a throwaway tmux
// socket and database, with an account made through the setup route, and
// the commands pointed at it through the two environment variables a
// container would set. What is checked is that each command reaches the
// panel's own handler -- the rename shows in the state, the text typed
// shows on the screen -- rather than that it printed something.

type cliPanel struct {
	ts  *httptest.Server
	db  *store.DB
	dir string
	srv *httpapi.Server
}

func startCLIPanel(t *testing.T) *cliPanel { return startCLIPanelWith(t, true) }

// startCLIPanelWith starts the panel and, with an account, finishes setup.
func startCLIPanelWith(t *testing.T, withAccount bool) *cliPanel {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	socket := "vibepanel-test-cli-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano()%100000, 10)
	tm := tmux.New(socket, dir)
	if bin := os.Getenv("TEST_TMUX_BIN"); bin != "" {
		tm.Bin = bin
	}
	if err := tm.EnsureServer(ctx); err != nil {
		t.Fatalf("tmux: %v", err)
	}
	t.Cleanup(func() {
		_ = tm.KillServer(context.Background())
		_ = os.Remove(tm.SocketPath())
	})
	cfg := config.Config{DataDir: dir, Addr: ":0", TmuxSocket: socket, StaticDir: dir}
	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := session.NewManager(tm, 256<<10)
	t.Cleanup(mgr.DetachAll)
	srv := &httpapi.Server{
		Cfg: cfg, DB: db, Tmux: tm, Manager: mgr, Hub: ws.NewHub(), Detector: session.NewDetector(),
		Sampler: &sysmon.Sampler{DiskPath: dir},
		Auth:    &httpapi.Auth{Throttle: auth.NewThrottle(), SetupToken: "cli-setup-token"},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	if withAccount {
		res, err := http.Post(ts.URL+"/api/auth/setup", "application/json",
			strings.NewReader(`{"token":"cli-setup-token","username":"tester","password":"a sufficiently long password"}`))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("setup: %d", res.StatusCode)
		}
	}
	t.Setenv("VIBEPANEL_DATA_DIR", dir)
	t.Setenv("VIBEPANEL_TMUX_SOCKET", socket)
	t.Setenv("VIBEPANEL_CLI_URL", ts.URL)
	t.Setenv("VIBEPANEL_CLI_TOKEN", "")
	return &cliPanel{ts: ts, db: db, dir: dir, srv: srv}
}

// run executes one command and returns what it printed.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	cmdErr := commands[args[0]](args[1:])
	os.Stdout = old
	w.Close()
	<-done
	return buf.String(), cmdErr
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runCLI(t, args...)
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestTheCLIOperatesTheRunningPanel(t *testing.T) {
	p := startCLIPanel(t)
	ctx := context.Background()
	work := t.TempDir()

	// Projects: add, rename, pin, order, archive, restore -- each visible in
	// the panel's own state afterwards.
	out := mustRun(t, "project", "add", work, "--name", "Alpha")
	if !strings.Contains(out, "created project") {
		t.Fatalf("add: %s", out)
	}
	mustRun(t, "project", "add", t.TempDir(), "--name", "Beta")
	mustRun(t, "project", "rename", "Alp", "Alpha Prime")
	mustRun(t, "project", "pin", "Alpha Prime")
	projects, _ := p.db.ListProjects(ctx)
	var alpha store.Project
	for _, pr := range projects {
		if pr.Name == "Alpha Prime" {
			alpha = pr
		}
	}
	if alpha.ID == "" || !alpha.Pinned {
		t.Fatalf("after rename and pin: %+v", projects)
	}
	if _, err := runCLI(t, "project", "rename", "nothing-here", "x"); err == nil || !strings.Contains(err.Error(), "no project") {
		t.Errorf("an unknown project: %v", err)
	}
	mustRun(t, "project", "order", "Beta", "Alpha Prime")
	if out := mustRun(t, "project", "ls"); !strings.Contains(out, "Alpha Prime *") || !strings.Contains(out, "Beta") {
		t.Errorf("ls: %s", out)
	}
	mustRun(t, "project", "archive", "Beta")
	if out := mustRun(t, "project", "ls"); !strings.Contains(out, "archived") {
		t.Errorf("ls after archive: %s", out)
	}
	mustRun(t, "project", "restore", "Beta")

	// The CLI's token is a row on the settings page and a 0600 file.
	tokens, _ := p.db.ListAPITokens(ctx)
	if len(tokens) != 1 || tokens[0].Name != "cli" {
		t.Fatalf("tokens: %+v", tokens)
	}
	if info, err := os.Stat(filepath.Join(p.dir, cliTokenFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", info, err)
	}

	// Sessions: new, rename, send, screen, state, kill.
	out = mustRun(t, "session", "new", "--project", "Alpha Prime", "--title", "shell", "--", "sh")
	if !strings.Contains(out, "created session") {
		t.Fatalf("new: %s", out)
	}
	sessions, _ := p.db.ListSessions(ctx)
	if len(sessions) != 1 || sessions[0].Title != "shell" {
		t.Fatalf("sessions: %+v", sessions)
	}
	mustRun(t, "session", "rename", "shell", "the shell")
	mustRun(t, "session", "send", "the shell", "echo cli-was-here", "--enter")
	deadline := time.Now().Add(5 * time.Second)
	var screen string
	for time.Now().Before(deadline) {
		screen = mustRun(t, "session", "screen", "the shell")
		if strings.Contains(screen, "cli-was-here") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(screen, "cli-was-here") {
		t.Errorf("the typed text is not on the screen:\n%s", screen)
	}
	mustRun(t, "session", "state", "the shell", "waiting")
	mustRun(t, "session", "pin", "the shell")
	if out := mustRun(t, "status"); !strings.Contains(out, "▲ waiting") || !strings.Contains(out, "the shell") || !strings.Contains(out, "pinned") {
		t.Errorf("status: %s", out)
	}
	mustRun(t, "session", "kill", "the shell")
	if sessions, _ := p.db.ListSessions(ctx); len(sessions) != 0 {
		t.Errorf("after kill: %+v", sessions)
	}

	// Notes and todos.
	mustRun(t, "note", "set", "Alpha Prime", "remember the milk")
	if out := mustRun(t, "note", "get", "Alpha Prime"); !strings.Contains(out, "remember the milk") {
		t.Errorf("note: %s", out)
	}
	mustRun(t, "todo", "add", "Alpha Prime", "buy milk")
	mustRun(t, "todo", "add", "Alpha Prime", "drink it")
	mustRun(t, "todo", "done", "Alpha Prime", "buy")
	if out := mustRun(t, "todo", "ls", "Alpha Prime"); !strings.Contains(out, "[x]  buy milk") || !strings.Contains(out, "[ ]  drink it") {
		t.Errorf("todos: %s", out)
	}
	mustRun(t, "todo", "rm", "Alpha Prime", "drink")

	// Tokens and the log. Revoking the CLI's own token locks nothing for
	// long: the next command mints another.
	minted := mustRun(t, "token", "create", "a script")
	if len(strings.TrimSpace(minted)) < 20 {
		t.Errorf("create: %q", minted)
	}
	if out := mustRun(t, "token", "ls"); !strings.Contains(out, "a script") || !strings.Contains(out, "cli") {
		t.Errorf("tokens: %s", out)
	}
	mustRun(t, "token", "revoke", "cli")
	if _, err := os.Stat(filepath.Join(p.dir, cliTokenFile)); err == nil {
		t.Error("the file outlived the revocation")
	}
	mustRun(t, "settings", "archive-idle", "14")
	// Setting a share origin from here says how to undo it: the panel cannot
	// always tell that the name is its own when asked over loopback.
	if out := mustRun(t, "settings", "share-origin", "https://share.example.com"); !strings.Contains(out, "https://share.example.com") ||
		!strings.Contains(out, "vibepanel settings share-origin clear") {
		t.Errorf("settings share-origin set: %s", out)
	}
	// An address is not something the check connects to.
	if out, err := runCLI(t, "settings", "share-origin", "check", "http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "domain name") {
		t.Errorf("settings share-origin check of an address: err %v, out %s; want it refused", err, out)
	}
	// .invalid never resolves (RFC 6761), so this stops at DNS, on any network.
	if out := mustRun(t, "settings", "share-origin", "check", "share.invalid"); !strings.Contains(out, "status:    needs_dns") || !strings.Contains(out, "https://share.invalid") {
		t.Errorf("settings share-origin check: %s", out)
	}
	if out := mustRun(t, "settings", "share-origin", "clear"); !strings.Contains(out, "panel address") {
		t.Errorf("settings share-origin clear: %s", out)
	}
	tokens, _ = p.db.ListAPITokens(ctx)
	names := []string{}
	for _, tk := range tokens {
		names = append(names, tk.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "cli") {
		t.Errorf("no new cli token after the revocation: %v", names)
	}
	out = mustRun(t, "audit", "-n", "200")
	if !strings.Contains(out, "session.input") || !strings.Contains(out, "project.archived") {
		t.Errorf("audit: %s", out)
	}
	if strings.Index(out, "setup.completed") > strings.Index(out, "session.input") {
		t.Errorf("the log is not oldest first:\n%s", out)
	}
}

// Without a panel, the subcommands the first CLI had still work on the
// database, and say so; the ones that need the panel say it is not running.
func TestTheCLIFallsBackWhenThePanelIsDown(t *testing.T) {
	p := startCLIPanel(t)
	mustRun(t, "project", "add", t.TempDir(), "--name", "Gamma")
	p.ts.Close()
	// A port nothing listens on.
	t.Setenv("VIBEPANEL_CLI_URL", "http://127.0.0.1:1")
	if out := mustRun(t, "project", "ls"); !strings.Contains(out, "Gamma") {
		t.Errorf("ls offline: %s", out)
	}
	if _, err := runCLI(t, "project", "rename", "Gamma", "Delta"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("rename offline: %v", err)
	}
	if _, err := runCLI(t, "status"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("status offline: %v", err)
	}
}

// A data directory with no panel.url and no VIBEPANEL_CLI_URL is one whose
// panel is not running, whatever listens on the default port: the CLI
// works on the database and never knocks on another panel's door.
func TestTheCLIReadsThePanelsAddressFromItsDataDirectory(t *testing.T) {
	p := startCLIPanel(t)
	mustRun(t, "project", "add", t.TempDir(), "--name", "Omega")
	os.Unsetenv("VIBEPANEL_CLI_URL")
	if out := mustRun(t, "project", "ls"); !strings.Contains(out, "Omega") {
		t.Errorf("ls with no address: %s", out)
	}
	if _, err := runCLI(t, "status"); err == nil || !strings.Contains(err.Error(), panelURLFile) {
		t.Errorf("status with no address: %v", err)
	}
	// The file the panel writes is what points the CLI at it -- while the
	// panel holds the lock. The file alone, as a crash leaves it, is nothing:
	// no token goes to whatever bound that port since.
	if err := os.WriteFile(filepath.Join(p.dir, panelURLFile), []byte(p.ts.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "status"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("panel.url with no lock held: %v", err)
	}
	release, err := config.LockDataDir(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if out := mustRun(t, "status"); !strings.Contains(out, "Omega") {
		t.Errorf("status through panel.url: %s", out)
	}
}

// The findings of the audit's hands-on run, each pinned: a directory is
// one project; a restart's receipt names what it restarted; the panel's
// two flags go anywhere; every command answers --help; a grid is bounded.
func TestTheCLIAfterTheHandsOnRun(t *testing.T) {
	p := startCLIPanel(t)
	dir := t.TempDir()
	mustRun(t, "project", "add", dir, "--name", "Once")
	if _, err := runCLI(t, "project", "add", dir, "--name", "Twice"); err == nil || !strings.Contains(err.Error(), "already the project Once") {
		t.Errorf("the same directory twice: %v", err)
	}
	mustRun(t, "session", "new", "--project", "Once", "--title", "brief", "--", "true")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rows, _ := p.db.ListSessions(context.Background()); len(rows) == 1 && rows[0].Exited {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if out := mustRun(t, "session", "restart", "brief"); !strings.Contains(out, "restarted brief (") || strings.Contains(out, "(tmux )") {
		t.Errorf("restart receipt: %q", out)
	}
	if _, err := runCLI(t, "session", "new", "--project", "Once", "--cols", "0", "--rows", "0", "--", "sleep", "60"); err == nil || !strings.Contains(err.Error(), "grid") {
		t.Errorf("a zero grid: %v", err)
	}
	for _, cmd := range [][]string{{"project", "--help"}, {"session", "-h"}, {"note", "help"}, {"token", "--help"}, {"me", "--help"}} {
		if out, err := runCLI(t, cmd...); err != nil || !strings.Contains(out, "usage: vibepanel "+cmd[0]) {
			t.Errorf("%v: %v %q", cmd, err, out)
		}
	}
	// The panel's flags anywhere on the line, lifted into the environment.
	got := liftPanelFlags([]string{"session", "ls", "--data-dir", "/x", "--tmux-socket=s1", "--", "--data-dir", "kept"})
	if strings.Join(got, " ") != "session ls -- --data-dir kept" || os.Getenv("VIBEPANEL_DATA_DIR") != "/x" || os.Getenv("VIBEPANEL_TMUX_SOCKET") != "s1" {
		t.Errorf("lift: %v %q %q", got, os.Getenv("VIBEPANEL_DATA_DIR"), os.Getenv("VIBEPANEL_TMUX_SOCKET"))
	}
	t.Setenv("VIBEPANEL_DATA_DIR", p.dir)
	t.Setenv("VIBEPANEL_TMUX_SOCKET", "")
	// A cleartext bearer off loopback is refused; loopback is not a wire.
	t.Setenv("VIBEPANEL_CLI_URL", "http://10.1.2.3:18443")
	if _, err := runCLI(t, "status"); err == nil || !strings.Contains(err.Error(), "in the clear") {
		t.Errorf("plain http off loopback: %v", err)
	}
}

// Before setup there is no account to mint a token for; a running panel is
// then no better than none, and the commands that can work on the database
// do, which is what `project add` and `session new` always did before the
// first account.
func TestBeforeSetupTheCLIWorksOnTheDatabase(t *testing.T) {
	p := startCLIPanelWith(t, false)
	out := mustRun(t, "project", "add", t.TempDir(), "--name", "Early")
	if !strings.Contains(out, "created project") {
		t.Fatalf("add: %s", out)
	}
	projects, _ := p.db.ListProjects(context.Background())
	if len(projects) != 1 || projects[0].Name != "Early" {
		t.Errorf("projects: %+v", projects)
	}
	if _, err := runCLI(t, "project", "rename", "Early", "Late"); err == nil || !strings.Contains(err.Error(), "no account") {
		t.Errorf("rename before setup: %v", err)
	}
	if tokens, _ := p.db.ListAPITokens(context.Background()); len(tokens) != 0 {
		t.Errorf("a token was minted with no account: %+v", tokens)
	}
}

// `vibepanel me` from inside a session: the environment the panel gave the
// session is the whole configuration, and what it says shows on the owner's
// side -- in the state and under the row.
func TestMeSpeaksForTheSessionItRunsIn(t *testing.T) {
	p := startCLIPanel(t)
	ctx := context.Background()
	mustRun(t, "project", "add", t.TempDir(), "--name", "Voice")
	mustRun(t, "session", "new", "--project", "Voice", "--title", "agent", "--", "sleep", "60")
	sessions, _ := p.db.ListSessions(ctx)
	if len(sessions) != 1 {
		t.Fatalf("sessions: %+v", sessions)
	}
	root, err := p.srv.HookToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIBEPANEL_SESSION_ID", sessions[0].ID)
	t.Setenv("VIBEPANEL_TOKEN", hooks.ReportToken(root, sessions[0].ID))
	t.Setenv("VIBEPANEL_URL", p.ts.URL)
	if out := mustRun(t, "me"); !strings.Contains(out, "agent") || !strings.Contains(out, "Voice") {
		t.Errorf("me: %s", out)
	}
	mustRun(t, "me", "state", "waiting", "--why", "which table to drop")
	mustRun(t, "me", "info", "set", "summary", "3 of 5 done, blocked on the migration")
	mustRun(t, "me", "info", "set", "progress", "60%")
	mustRun(t, "me", "info", "set", "labels", "db,migration")
	mustRun(t, "me", "title", "schema work")
	row, _ := p.db.GetSession(ctx, sessions[0].ID)
	if row.State != "waiting" || row.Summary != "3 of 5 done, blocked on the migration" || row.Progress != 60 || strings.Join(row.Labels, ",") != "db,migration" || row.Title != "schema work" {
		t.Errorf("row: %+v", row)
	}
	if last, ok, _ := p.db.LatestSessionMessage(ctx, row.ID); !ok || last.Text != "which table to drop" {
		t.Errorf("why: %+v", last)
	}
	if out := mustRun(t, "status"); !strings.Contains(out, "▲ waiting") || !strings.Contains(out, "schema work") {
		t.Errorf("status: %s", out)
	}
	// The owner's side of the same fields, and clearing.
	t.Setenv("VIBEPANEL_SESSION_ID", "")
	mustRun(t, "session", "info", "schema", "clear")
	row, _ = p.db.GetSession(ctx, sessions[0].ID)
	if row.Summary != "" || row.Progress != -1 || len(row.Labels) != 0 {
		t.Errorf("after clear: %+v", row)
	}
	if out := mustRun(t, "me", "guide"); !strings.Contains(out, "me state waiting") {
		t.Errorf("guide: %s", out)
	}
	// say, notify and send from inside; the file lands under the session.
	t.Setenv("VIBEPANEL_SESSION_ID", sessions[0].ID)
	mustRun(t, "me", "say", "the migration ran")
	if out := mustRun(t, "me", "notify", "drop the old table?", "--choices", "drop,keep"); !strings.Contains(out, "0 contact") {
		t.Errorf("notify: %s", out)
	}
	png := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n0000"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := mustRun(t, "me", "send", png, "--caption", "coverage"); !strings.Contains(out, "sent chart.png") {
		t.Errorf("send: %s", out)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "sessions", sessions[0].ID, "attachments", "chart.png")); err != nil {
		t.Errorf("the file is not under the session: %v", err)
	}
	t.Setenv("VIBEPANEL_SESSION_ID", "")
	if out := mustRun(t, "session", "attachments", "schema"); !strings.Contains(out, "chart.png") {
		t.Errorf("attachments: %s", out)
	}
	if out := mustRun(t, "session", "attach", "schema", png, "--caption", "again"); !strings.Contains(out, "chart-2.png") {
		t.Errorf("attach: %s", out)
	}
	// Outside a session there is nothing to speak for.
	t.Setenv("VIBEPANEL_TOKEN", "")
	if _, err := runCLI(t, "me"); err == nil || !strings.Contains(err.Error(), "inside a session") {
		t.Errorf("me outside a session: %v", err)
	}
}

// The guide goes into a project's AGENTS.md once, with a CLAUDE.md that
// points at it when there is none; a second write changes nothing.
func TestTheGuideIsWrittenOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Mine\n\nrules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "me", "guide", "--write", dir)
	first, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.HasPrefix(string(first), "# Mine\n") || !strings.Contains(string(first), guideMarker) || !strings.Contains(string(first), "me notify") {
		t.Fatalf("AGENTS.md: %s", first)
	}
	if claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); err != nil || string(claude) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md: %q %v", claude, err)
	}
	if out := mustRun(t, "me", "guide", "--write", dir); !strings.Contains(out, "already has") {
		t.Errorf("second write: %s", out)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(second) != string(first) {
		t.Error("the second write changed the file")
	}
}
