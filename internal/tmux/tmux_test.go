package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// newTestClient gives each test its own tmux server on a throwaway socket.
//
// Isolation is not cosmetic here: a test that reached the default socket could
// enumerate and kill the developer's real sessions.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := "vibepanel-test-" + strconv.Itoa(os.Getpid()) + "-" + t.Name()
	socket = strings.NewReplacer("/", "_", " ", "_").Replace(socket)
	c := New(socket, t.TempDir())
	// So the suite can be pointed at another tmux without editing anything.
	//
	// Added when CI on tmux 3.4 failed fourteen tests here and this machine had
	// only 3.6: the bug was in what an older tmux does to its own output, and
	// there was no way to reproduce it except by pushing and reading the log.
	//
	//	TEST_TMUX_BIN=/path/to/tmux go test ./internal/tmux/
	//
	// Not VIBEPANEL_-prefixed: the panel reports every unrecognised variable
	// under that prefix at startup, on purpose, and internal/config has a test
	// that says so. Setting one for the whole `go test ./...` run failed that
	// test -- correctly.
	if bin := os.Getenv("TEST_TMUX_BIN"); bin != "" {
		c.Bin = bin
	}
	t.Cleanup(func() {
		_ = c.KillServer(context.Background())
		// kill-server leaves the socket file behind.
		_ = os.Remove(c.SocketPath())
	})
	return c
}

func TestEnsureServerLoadsConfig(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	if !c.ServerRunning(ctx) {
		t.Fatal("server not running after EnsureServer")
	}

	// A config file with a bad option name makes tmux print an error and carry
	// on with defaults, so "the server started" proves nothing on its own.
	// Read back one option from each scope to confirm the file was applied.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"show-options", "-s", "-v", "set-clipboard"}, "on"},
		{[]string{"show-options", "-g", "-v", "status"}, "off"},
		{[]string{"show-options", "-g", "-v", "history-limit"}, "20000"},
		{[]string{"show-options", "-wg", "-v", "remain-on-exit"}, "on"},
		{[]string{"show-options", "-wg", "-v", "window-size"}, "latest"},

		// The settings whose failure is silent rather than loud. One per scope
		// proves the file was read; these prove the lines that matter survived
		// it, and each of them fails by doing nothing observable:
		//
		// bell-action is the one with an incident behind it — "none" reads like
		// "do not react" and actually stops tmux forwarding the bell at all,
		// which removes the only signal the panel has that an agent wants a
		// human. Nothing else would look different.
		{[]string{"show-options", "-g", "-v", "bell-action"}, "any"},
		{[]string{"show-options", "-g", "-v", "visual-bell"}, "off"},
		// Without passthrough, sequences tmux does not model are swallowed and
		// agent TUIs lose progress bars and notifications. Also the option that
		// sets the real minimum tmux version: it arrived in 3.3.
		{[]string{"show-options", "-wg", "-v", "allow-passthrough"}, "on"},
		{[]string{"show-options", "-wg", "-v", "monitor-bell"}, "on"},
		// 500ms of default disambiguation makes every Esc feel like the app
		// has hung, which reads as the agent being broken rather than tmux.
		{[]string{"show-options", "-s", "-v", "escape-time"}, "0"},
		{[]string{"show-options", "-s", "-v", "default-terminal"}, "tmux-256color"},
	} {
		got, err := c.run(ctx, tc.args...)
		if err != nil {
			t.Errorf("%v: %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%v = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestSessionLifecycle(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	dir := t.TempDir()
	const name = "vp_lifecycle"

	if ok, err := c.Has(ctx, name); err != nil || ok {
		t.Fatalf("Has before create = %v, %v; want false, nil", ok, err)
	}

	err := c.Create(ctx, CreateOptions{
		Name:    name,
		Dir:     dir,
		Command: []string{"sleep", "300"},
		Env:     []string{"VIBEPANEL_SESSION_ID=test-session-id"},
		Width:   100,
		Height:  40,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ok, err := c.Has(ctx, name)
	if err != nil || !ok {
		t.Fatalf("Has after create = %v, %v; want true, nil", ok, err)
	}

	info, err := c.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Name != name {
		t.Errorf("Name = %q, want %q", info.Name, name)
	}
	if info.Width != 100 || info.Height != 40 {
		t.Errorf("size = %dx%d, want 100x40", info.Width, info.Height)
	}
	// pane_current_command is eventually consistent: immediately after
	// new-session it still reads "tmux" until the pane's fork exec's.
	if got := waitForCommand(t, c, name, "sleep"); got != "sleep" {
		t.Errorf("Command = %q, want %q", got, "sleep")
	}
	if info.Dead {
		t.Error("Dead = true for a running sleep")
	}

	// List must see exactly the one session we made.
	list, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != name {
		t.Fatalf("List = %+v, want one session named %q", list, name)
	}

	if err := c.Resize(ctx, name, 80, 24); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if info, err = c.Get(ctx, name); err != nil {
		t.Fatalf("Get after resize: %v", err)
	}
	if info.Width != 80 || info.Height != 24 {
		t.Errorf("size after resize = %dx%d, want 80x24", info.Width, info.Height)
	}

	if err := c.Kill(ctx, name); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if ok, err = c.Has(ctx, name); err != nil || ok {
		t.Fatalf("Has after kill = %v, %v; want false, nil", ok, err)
	}
	// Killing an absent session is a no-op, not an error: the UI can race with
	// a session exiting on its own and must not surface that as a failure.
	if err := c.Kill(ctx, name); err != nil {
		t.Errorf("Kill twice: %v", err)
	}
}

// tmux reports its version in more shapes than a split on "." survives, and
// getting this wrong in either direction is bad: a false "too old" nags about
// a working install, and a false "new enough" leaves the passthrough problem
// undiagnosed on the one machine where it matters.
func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		in           string
		major, minor int
		ok           bool
		atLeast      bool
	}{
		{"3.6", 3, 6, true, true},
		{"3.3", 3, 3, true, true},
		{"3.2", 3, 2, true, false},
		{"2.8", 2, 8, true, false},
		// A patch release carries a letter.
		{"3.3a", 3, 3, true, true},
		{"3.2a", 3, 2, true, false},
		// A development build carries a prefix.
		{"next-3.6", 3, 6, true, true},
		{"4.0", 4, 0, true, true},
		// Nothing usable. Treated as new enough: refusing to run because a
		// version string looked unfamiliar is worse than the problem.
		{"", 0, 0, false, true},
		{"master", 0, 0, false, true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			major, minor, ok := ParseVersion(tc.in)
			if ok != tc.ok || (ok && (major != tc.major || minor != tc.minor)) {
				t.Errorf("ParseVersion(%q) = %d, %d, %v; want %d, %d, %v",
					tc.in, major, minor, ok, tc.major, tc.minor, tc.ok)
			}
			if got := AtLeastMinimum(tc.in); got != tc.atLeast {
				t.Errorf("AtLeastMinimum(%q) = %v, want %v", tc.in, got, tc.atLeast)
			}
		})
	}
}

// Red line 7, which had no test until now: targets must be exact.
//
// tmux resolves a target by trying an exact match, then a prefix match. So the
// danger is not two sessions existing at once — the exact one wins — it is
// aiming at a session that has already gone while a longer name is still
// there. That is an ordinary event: the UI races with a session exiting on its
// own, and a kill arriving a moment late would land on a different session
// entirely. Silently, and only once two generated names happened to share a
// prefix.
//
// Everything here goes through target()/sessionTarget(), so dropping the "="
// from either would leave every other test in this package passing.
func TestTargetsAreExactNotPrefixes(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	// Only the longer name exists. The shorter one is a prefix of it.
	const long, short = "vp_abcd", "vp_ab"
	if err := c.Create(ctx, CreateOptions{
		Name: long, Dir: t.TempDir(), Command: []string{"sleep", "300"},
		Width: 80, Height: 24,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if ok, err := c.Has(ctx, short); err != nil || ok {
		t.Fatalf("Has(%q) = %v, %v while only %q exists; the target resolved by prefix",
			short, ok, err, long)
	}

	// A resize aimed at the absent session must not move the one that is there.
	_ = c.Resize(ctx, short, 120, 40)
	info, err := c.Get(ctx, long)
	if err != nil {
		t.Fatalf("Get(%q): %v", long, err)
	}
	if info.Width != 80 || info.Height != 24 {
		t.Errorf("%s was resized to %dx%d by a command aimed at %q",
			long, info.Width, info.Height, short)
	}

	// And the one that matters: killing something already gone must not take a
	// live session with it.
	if err := c.Kill(ctx, short); err != nil {
		t.Fatalf("Kill(%q) on an absent session should be a no-op: %v", short, err)
	}
	if ok, err := c.Has(ctx, long); err != nil || !ok {
		t.Fatalf("%s is gone after killing %q; the kill landed on the wrong session",
			long, short)
	}

	if err := c.Kill(ctx, long); err != nil {
		t.Fatalf("Kill(%q): %v", long, err)
	}
}

// waitForCommand polls until the pane reports want, or gives up and returns
// whatever it last saw so the caller can report a useful diff.
func waitForCommand(t *testing.T, c *Client, name, want string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for {
		info, err := c.Get(context.Background(), name)
		if err != nil {
			t.Fatalf("Get while waiting for command: %v", err)
		}
		last = info.Command
		if last == want || time.Now().After(deadline) {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestEnvIsInjected(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	// The state-reporting hooks identify their session by reading
	// VIBEPANEL_SESSION_ID out of the environment, so -e injection reaching the
	// pane's shell is a hard requirement, not a convenience.
	const name = "vp_env"
	const want = "abc123"
	err := c.Create(ctx, CreateOptions{
		Name:    name,
		Dir:     t.TempDir(),
		Command: []string{"sh", "-c", "printf %s \"$VIBEPANEL_SESSION_ID\"; sleep 60"},
		Env:     []string{"VIBEPANEL_SESSION_ID=" + want},
		Width:   80,
		Height:  24,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		out, cerr := c.Capture(ctx, name)
		if cerr != nil {
			t.Fatalf("Capture: %v", cerr)
		}
		if strings.Contains(out, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("env var never reached pane; capture was %q", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCaptureKeepsEscapeSequences(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	const name = "vp_capture"
	err := c.Create(ctx, CreateOptions{
		Name:    name,
		Dir:     t.TempDir(),
		Command: []string{"sh", "-c", "printf '\\033[31mRED\\033[0m\\n'; sleep 60"},
		Width:   80,
		Height:  24,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Cold-start replay is only useful if colour survives it. A capture without
	// -e would hand the browser plain text and every restored session would
	// come back monochrome.
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, cerr := c.Capture(ctx, name)
		if cerr != nil {
			t.Fatalf("Capture: %v", cerr)
		}
		if strings.Contains(out, "RED") {
			if !strings.Contains(out, "\033[") {
				t.Fatalf("capture lost SGR escapes: %q", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("output never appeared; capture was %q", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDeadPaneIsPreserved(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	// remain-on-exit is what keeps a crashed agent's traceback on screen.
	// If this regresses, sessions vanish the moment their command exits and
	// the user loses the only copy of the error.
	const name = "vp_dead"
	err := c.Create(ctx, CreateOptions{
		Name:    name,
		Dir:     t.TempDir(),
		Command: []string{"sh", "-c", "echo bye; exit 3"},
		Width:   80,
		Height:  24,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		info, gerr := c.Get(ctx, name)
		if gerr != nil {
			t.Fatalf("session disappeared instead of staying dead: %v", gerr)
		}
		if info.Dead {
			out, _ := c.Capture(ctx, name)
			if !strings.Contains(out, "bye") {
				t.Errorf("dead pane lost its output: %q", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("pane never reported Dead")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Red lines 1 and 7, as tests rather than as paragraphs somebody remembers.
//
// The rules are that every tmux command names the panel's own socket, and that
// every target is the exact-match form. Both were true when this was written
// and nothing was checking either one. The failure they guard is not subtle:
// tmux resolves targets by prefix, so `-t vp_ab` also matches `vp_abcd`, and a
// command without `-L` lands on whatever tmux the user is running next to this
// one — which for the person this was built for is a session that has been
// alive for weeks.

// There is deliberately no test here asserting that target() returns
// "=name:". TestTargetsAreExactNotPrefixes above already proves the property
// that matters, against a real tmux with a real prefix collision, and it
// catches every way of getting this wrong rather than the one way a string
// comparison knows about. Two tests for one rule is the thing this codebase
// keeps paying for elsewhere.

func TestEveryCommandNamesOurSocket(t *testing.T) {
	c := New("a-socket", t.TempDir())
	for _, argv := range [][]string{
		c.args("list-sessions"),
		c.args("kill-session", "-t", sessionTarget("vp_x")),
		c.AttachArgs("vp_x"),
	} {
		joined := strings.Join(argv, " ")
		idx := -1
		for i, a := range argv {
			if a == "-L" {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Errorf("no -L in %q; this command would use the user's own tmux", joined)
			continue
		}
		if idx+1 >= len(argv) || argv[idx+1] != "a-socket" {
			t.Errorf("-L is not followed by the configured socket in %q", joined)
		}
	}
}

// Nothing outside this package builds a tmux target by hand.
//
// The helpers exist so the '=' … ':' form cannot be forgotten, and a helper
// nobody is obliged to use is a convention rather than a rule. This walks the
// Go sources and objects to a `-t` argument anywhere else — which is what
// hand-building a target looks like.
func TestNoHandBuiltTargetsElsewhere(t *testing.T) {
	root := ".."
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// The tmux package is where the helpers live; node_modules is not ours.
			if info.Name() == "tmux" || info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, `"-t"`) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the source: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("tmux targets are built outside internal/tmux, where the exact-match form "+
			"cannot be enforced:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// TestPasteIsBracketedOnlyForPanesThatAskedForIt pins why input for a
// multi-line block goes through tmux instead of straight into the PTY.
//
// Written into the PTY, "line one\nline two" is indistinguishable from someone
// typing two lines and pressing Enter twice: a shell runs each, and an agent
// acts on the first sentence of a three-line instruction before it has read
// the third. ESC[200~ … ESC[201~ makes it one submission — but only for an
// application that asked for bracketed paste. Send the markers to one that did
// not and it receives them as literal garbage in the middle of the text.
//
// tmux tracks that mode per pane, which is the entire reason this is
// delegated. The test is really about `paste-buffer -p` behaving as
// advertised, because the whole design rests on it.
func TestPasteIsBracketedOnlyForPanesThatAskedForIt(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	// `cat -v` renders ESC as ^[ so the markers are visible in a capture.
	//
	// `-echo` keeps the tty from printing its own copy of everything, which is
	// what ECHOCTL does and what made the first reading of this look like a
	// double paste. `-icanon min 1` is the one that matters: in canonical mode
	// the driver holds a line until it sees a newline, and the closing ESC[201~
	// arrives after the last line and before any newline — so it sat in the
	// tty buffer and the test read half a paste.
	const declares = "vp_paste_yes"
	const silent = "vp_paste_no"
	for name, prologue := range map[string]string{
		declares: `printf '\033[?2004h'; `,
		silent:   ``,
	} {
		if err := c.Create(ctx, CreateOptions{
			Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
			Command: []string{"sh", "-c", "stty -echo -icanon min 1 time 0; " + prologue + "exec cat -v"},
		}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}
	time.Sleep(700 * time.Millisecond)

	const block = "please refactor the auth flow\nand do not touch the tmux config"
	for _, name := range []string{declares, silent} {
		if err := c.Paste(ctx, name, block); err != nil {
			t.Fatalf("Paste to %s: %v", name, err)
		}
	}
	time.Sleep(700 * time.Millisecond)

	got := map[string]string{}
	for _, name := range []string{declares, silent} {
		out, err := c.Capture(ctx, name)
		if err != nil {
			t.Fatalf("Capture %s: %v", name, err)
		}
		got[name] = out
	}

	if !strings.Contains(got[declares], "^[[200~") || !strings.Contains(got[declares], "^[[201~") {
		t.Errorf("a pane that asked for bracketed paste was not given the markers, so a "+
			"multi-line instruction arrives as line after line of typing:\n%s", got[declares])
	}
	if strings.Contains(got[silent], "^[[200~") || strings.Contains(got[silent], "^[[201~") {
		t.Errorf("a pane that never asked for bracketed paste was sent the markers, which "+
			"it will read as text in the middle of the message:\n%s", got[silent])
	}
	// Both must still have the words, whatever the wrapping.
	for _, name := range []string{declares, silent} {
		if !strings.Contains(got[name], "please refactor the auth flow") {
			t.Errorf("%s never received the text at all:\n%s", name, got[name])
		}
	}
}

// TestTheBellFlagLatchesWhenNobodyIsAttached pins a property of tmux, not of
// this code, and the panel's recovery at startup rests on it.
//
// While a client is attached, `bell-action any` makes tmux forward the bell to
// that client and `window_bell_flag` stays 0 — which is why the raw \007 on
// the PTY is the signal in normal running. With no client there is nobody to
// forward to, so tmux latches the flag instead, and `Reconcile` reads it once
// at startup to recover a bell that rang while the panel was down. That is the
// one moment nobody was watching, and it is the moment the whole feature is
// for.
//
// Two comments said this could not work: the field's own ("always false under
// the panel's configuration … do not build on it") and the config's ("nothing
// polls window_bell_flag"). Both were wrong, and either would have licensed
// deleting the recovery. A measurement in a test is harder to be wrong about
// than a sentence.
//
// Attaching clears the flag, so the recovery consumes it exactly once. That
// half is asserted here too: without it a session that rang before startup
// would be re-observed as waiting on every poll, and the state would never
// clear.
func TestTheBellFlagLatchesWhenNobodyIsAttached(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	// EnsureServer is what writes the embedded config, and this test is about
	// what that config makes tmux do. Without it the server comes up on tmux's
	// defaults and the assertions below say nothing about the panel — which is
	// how the first version passed with `monitor-bell off` and with
	// `bell-action none` pasted into vibepanel.conf.
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	const name = "vp_bellflag"
	if err := c.Create(ctx, CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 0.3; printf 'needs you\\a'; exec sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	byName := func() Info {
		t.Helper()
		infos, err := c.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, i := range infos {
			if i.Name == name {
				return i
			}
		}
		t.Fatalf("session %s is not in the listing", name)
		return Info{}
	}

	if !byName().Bell {
		t.Fatal("a bell that rang with no client attached did not latch; a bell raised " +
			"while the panel was down is lost, which is the one time nobody was watching")
	}

	// Attach the way the panel does — a real client on a PTY — and the flag
	// must be spent.
	cmd := exec.CommandContext(ctx, c.Bin, c.args(c.AttachArgs(name)...)...)
	// The panel sets TERM on its own client (manager.go), and "the way the
	// panel does" has to include that. Without it -- which is the state of any
	// non-interactive CI step -- tmux attaches to a terminal it cannot drive
	// and the bell flag is never spent, so this failed on a runner while
	// passing on every developer's machine, where TERM is inherited.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	_ = f.Close()
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	time.Sleep(500 * time.Millisecond)

	if byName().Bell {
		t.Error("the flag survived a client attaching, so the same bell would be " +
			"re-observed on every poll and the session would never stop waiting")
	}
}

// TestTmuxSwallowsDesktopNotificationSequences records which of the documented
// "a human is needed" signals can actually arrive.
//
// The session package parses OSC 9 and OSC 777 as notifications, and the state
// table has always listed them beside the terminal bell. Under this config
// they never get the chance: tmux forwards neither to its client, and does not
// turn either into a window bell or activity flag. They are consumed and
// dropped. Measured — a pane emitting each, with a real client attached:
//
// \tOSC 9;plain notification   bell_flag=0 activity=0, nothing on the client
// \tOSC 777;notify;t;b         bell_flag=0 activity=0, nothing on the client
// \tBEL                        bell_flag=1
//
// So the terminal bell is not merely the most reliable signal without hooks,
// it is the only one that exists. That is a stronger statement than the build
// log's "claude rings zero bells", which is about one agent; this is about any
// agent, including one that does the polite thing and sends OSC 9.
//
// The parser stays correct for the day this changes, and this test is how
// anyone finds out that it has: a tmux that starts forwarding OSC 9 fails it,
// and the failure is good news.
func TestTmuxSwallowsDesktopNotificationSequences(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	emit := func(name, seq string) string {
		t.Helper()
		script := "import sys,time; sys.stdout.write(" + seq + "); sys.stdout.flush(); time.sleep(20)"
		if err := c.Create(ctx, CreateOptions{
			Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
			Command: []string{"python3", "-u", "-c", script},
		}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		time.Sleep(900 * time.Millisecond)

		cmd := exec.CommandContext(ctx, c.Bin, c.args(c.AttachArgs(name)...)...)
		f, err := pty.Start(cmd)
		if err != nil {
			t.Fatalf("attach %s: %v", name, err)
		}
		done := make(chan string, 1)
		go func() {
			buf := make([]byte, 64<<10)
			var got []byte
			deadline := time.Now().Add(2500 * time.Millisecond)
			for time.Now().Before(deadline) {
				_ = f.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
				n, rerr := f.Read(buf)
				got = append(got, buf[:n]...)
				if rerr != nil && n == 0 {
					continue
				}
			}
			done <- string(got)
		}()
		out := <-done
		_ = f.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return out
	}

	// chr(27)+chr(93) is ESC ], chr(7) is BEL. Built this way so the test file
	// contains no raw escape characters of its own.
	seen := emit("vp_osc9", `chr(27)+chr(93)+"9;something needs you"+chr(7)`)
	if strings.Contains(seen, "\x1b]9;") {
		t.Error("tmux now forwards OSC 9 to its client. That is good news: the parser " +
			"already handles it, and the state machine can start believing it.")
	}
	seen777 := emit("vp_osc777", `chr(27)+chr(93)+"777;notify;title;body"+chr(7)`)
	if strings.Contains(seen777, "\x1b]777;") {
		t.Error("tmux now forwards OSC 777 to its client; see the OSC 9 case above")
	}

	// The contrast that makes the point: a real BEL does arrive.
	if err := c.Create(ctx, CreateOptions{
		Name: "vp_realbell", Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 0.3; printf '\\a'; exec sleep 20"},
	}); err != nil {
		t.Fatalf("Create bell: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	infos, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var belled bool
	for _, i := range infos {
		if i.Name == "vp_realbell" {
			belled = i.Bell
		}
	}
	if !belled {
		t.Error("a real BEL did not register either, so this test proves nothing about " +
			"the sequences above")
	}
}

func TestAKilledPaneIsNotACleanExit(t *testing.T) {
	// tmux reports the two differently: pane_dead_status is the wait status of
	// a process that returned, and it is empty for one that was killed, where
	// pane_dead_signal holds the signal. The panel read only the first, so a
	// pane killed by the OOM killer described itself as "exited with status 0"
	// — indistinguishable from one that finished its work. On a machine
	// running a couple of dozen agents that is not a rare distinction.
	ctx := context.Background()
	c := newTestClient(t)
	// EnsureServer, because remain-on-exit lives in the embedded config: a
	// server started without it destroys the session the moment the process
	// ends, and there is no dead pane left to ask about.
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	const name = "vp_killed"
	if err := c.Create(ctx, CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "echo starting; exec sleep 600"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var info Info
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		got, err := c.Get(ctx, name)
		if err == nil && got.PID > 0 && got.Command == "sleep" {
			info = got
			break
		}
	}
	if info.PID == 0 {
		t.Fatal("the pane never started its command")
	}
	if err := syscall.Kill(info.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}

	var dead Info
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		got, err := c.Get(ctx, name)
		if err == nil && got.Dead {
			dead = got
			break
		}
	}
	if !dead.Dead {
		t.Fatal("the pane never went dead")
	}
	// tmux can lose the wait status, and when it does there is nothing here to
	// assert about. Measured on tmux 3.4, roughly one run in ten: the pane is
	// reported dead the moment its pty closes, before the server has reaped the
	// child, so both fields read 0 and stay that way -- the killed pid is still
	// a zombie in /proc at that point. That is a fact about tmux, not about
	// ExitStatus(), which is what this test is for.
	if dead.DeadStatus == 0 && dead.DeadSignal == 0 {
		v, _ := c.Version(ctx)
		t.Skipf("tmux %s marked the pane dead without collecting a wait status", v)
	}
	if dead.DeadSignal != int(syscall.SIGKILL) {
		t.Errorf("DeadSignal = %d, want %d", dead.DeadSignal, syscall.SIGKILL)
	}
	if got := dead.ExitStatus(); got != 128+int(syscall.SIGKILL) {
		t.Errorf("ExitStatus() = %d, want %d; a killed process must not report "+
			"the number a successful one has", got, 128+int(syscall.SIGKILL))
	}
}

func TestAnOrdinaryExitKeepsItsStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	const name = "vp_exited"
	if err := c.Create(ctx, CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "exit 3"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		got, err := c.Get(ctx, name)
		if err == nil && got.Dead {
			if got.ExitStatus() != 3 {
				t.Errorf("ExitStatus() = %d, want 3", got.ExitStatus())
			}
			if got.DeadSignal != 0 {
				t.Errorf("DeadSignal = %d for a process that exited normally", got.DeadSignal)
			}
			return
		}
	}
	t.Fatal("the pane never went dead")
}

// What tmux reports for a script is its interpreter's name, not the script's.
//
// This pins the fact the panel's agent detection rests on. `IsAgentCommand`
// matches `#{pane_current_command}` against "claude" and "codex", and there is
// nothing else to match: the frontend creates every session with an empty
// command, so the panel never launches an agent itself and has no launch
// command to remember. The string is therefore a fact about how somebody else
// packaged their program.
//
// A native binary reports its own name. A program shipped as a script with a
// `#!` line -- which is how Claude Code arrives when installed through npm --
// reports the interpreter, because that is what the kernel actually executed.
// So the panel sees "node", `IsAgentCommand` says no, `stateIsGuessed` returns
// false, and the notice saying the states are inferred never appears on
// exactly the sessions it is about. Nothing else would say so, because a guess
// usually looks plausible. `doctor` prints this list for that reason.
//
// Written with /bin/sh rather than node so it runs anywhere, and with a
// builtin rather than a child process: a script that runs `sleep` puts sleep
// in the foreground process group and tmux reports *that*, which is the same
// lesson by a different route and a worse fixture for it.
func TestAScriptIsReportedByItsInterpreterNotItsOwnName(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "agentish")
	// `read` is a shell builtin, so nothing is forked and the interpreter
	// itself stays in the foreground.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nread line\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	const name = "vp_shebang"
	if err := c.Create(ctx, CreateOptions{Name: name, Dir: dir, Command: []string{script}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := waitForCommand(t, c, name, "sh")
	if got == "agentish" {
		t.Fatalf("tmux reported the script's own name %q; if that is now true, the agent "+
			"match is sound for npm installs and doctor's advice about \"node\" is wrong", got)
	}
	if got != "sh" {
		t.Fatalf("tmux reports %q for a #!/bin/sh script, want \"sh\"; the premise that a "+
			"packaged agent is reported by its interpreter needs re-measuring", got)
	}
}

// What a session was given, read back from tmux.
//
// The hooks post to VIBEPANEL_URL, injected with -e when the session is made.
// `set-environment` on a live session reaches only panes started after it, so a
// panel restarted with a different --addr leaves every existing session posting
// to the old address -- silently, because report.sh suppresses its own failures
// and the settings page reads the agent's config rather than what arrived.
// Nothing could ask what a session actually holds until this existed.
func TestSessionEnvValueReadsWhatTheSessionWasGiven(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	const name = "vp_envread"
	const url = "http://127.0.0.2:8443"
	if err := c.Create(ctx, CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sleep", "60"},
		Env:     []string{"VIBEPANEL_URL=" + url},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := c.SessionEnvValue(ctx, name, "VIBEPANEL_URL")
	if err != nil {
		t.Fatalf("SessionEnvValue: %v", err)
	}
	if got != url {
		t.Errorf("VIBEPANEL_URL reads back as %q, want %q", got, url)
	}

	// A variable that was never set is not an error: sessions made before the
	// hooks were installed, or by hand, simply do not have one.
	if got, err := c.SessionEnvValue(ctx, name, "VIBEPANEL_NOT_SET"); err != nil || got != "" {
		t.Errorf("an unset variable gave (%q, %v), want (\"\", nil)", got, err)
	}
}

// The running server records the config it was started with.
//
// `-f` is read once, at start-server, and the panel never kills its server --
// that is the premise of the project. EnsureServer rewrites the config file on
// every call, so the file is always current while the running server goes on
// using whatever it read at boot. A config change therefore takes effect at the
// next reboot or not at all, and nothing could see the difference: an upgrade
// leaves a new binary that is not running and a new tmux config that is not
// loaded, and both look installed.
func TestTheServerRecordsTheConfigItStartedWith(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	if got := c.RunningConfigStamp(ctx); got != "" {
		t.Fatalf("a stamp came back before any server was started: %q", got)
	}
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	want := ConfigStamp()
	if want == "" {
		t.Fatal("the embedded config hashes to nothing")
	}
	if got := c.RunningConfigStamp(ctx); got != want {
		t.Errorf("the server records %q, the embedded config hashes to %q", got, want)
	}

	// A second EnsureServer must not restamp a server it did not start. The
	// whole value is that the stamp describes what the *running* server read,
	// so a call that refreshes it would report agreement it cannot know about.
	//
	// The first version of this changed the config *file* and asserted the
	// stamp was unchanged -- which it would have been either way, because
	// ConfigStamp hashes the embedded bytes and not the file. It passed for a
	// reason that had nothing to do with what it was testing. Setting a value
	// that cannot be produced by hashing anything is decisive instead.
	const bogus = "0000000000000000"
	if _, err := c.run(ctx, "set-option", "-s", configStampOption, bogus); err != nil {
		t.Fatalf("set the marker: %v", err)
	}
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatalf("second EnsureServer: %v", err)
	}
	if got := c.RunningConfigStamp(ctx); got != bogus {
		t.Errorf("EnsureServer restamped a running server (%q -> %q); the stamp would then "+
			"agree with the binary whatever the server actually read", bogus, got)
	}

	// And ServerRunning has to be true for a server with no sessions, or the
	// line above is only true by luck. Measured: `list-sessions` fails on a
	// session-less server under tmux's defaults, and succeeds under this
	// project's config, which sets exit-empty off.
	if !c.ServerRunning(ctx) {
		t.Error("ServerRunning says no for a server with no sessions; EnsureServer would then " +
			"restamp every server it is asked about")
	}
}

// The embedded config has to be valid on the tmux that is actually installed,
// and nothing was checking that.
//
// `allow-set-title` did not exist below tmux 3.6. On 3.4 -- which is what
// Ubuntu 24.04 LTS ships, so a very ordinary place to run this -- tmux
// answered `invalid option: allow-set-title`, and from there fourteen tests
// across internal/tmux and internal/session failed with symptoms that named
// something else entirely: sessions missing from the listing, panes that never
// started their command, `expected 13 fields, got 1`. Nothing pointed at the
// config, because tmux does not report config errors on stderr at start-server
// and does not put them in `show-messages` either. It draws them in the pane
// and carries on.
//
// `source-file` is the one place tmux does report them: non-zero exit and the
// message on stderr. So the config is sourced into a throwaway server here,
// which makes every option in the file a checked claim about the local tmux
// rather than about the author's.
//
// This is the general form of the bug. A specific assertion per option would
// not have caught it -- the assertion for allow-set-title was passing on the
// machine it was written on.
func TestTheEmbeddedConfigLoadsWithoutComplaintOnThisTmux(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// A server started with no config of its own, so the only thing being
	// judged is the file sourced into it a moment later. Starting it with the
	// embedded config would hide the error rather than raise it: tmux draws a
	// config error in the pane and defers the message to whichever command
	// comes next, which is exactly how this went unnoticed.
	//
	// `new-session` rather than `start-server`, because a server with no
	// sessions exits immediately under tmux's default exit-empty -- and
	// exit-empty is one of the things this very file turns off.
	plain := &Client{Socket: c.Socket, Bin: c.Bin}
	if out, err := plain.run(ctx, "new-session", "-d", "sleep", "60"); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}

	path := filepath.Join(t.TempDir(), "vibepanel.conf")
	if err := os.WriteFile(path, Config, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, err := plain.run(ctx, "source-file", path); err != nil {
		t.Fatalf("the embedded config is not valid on %s:\n%v\n%s", versionOf(t, plain), err, out)
	}
}

func versionOf(t *testing.T, c *Client) string {
	t.Helper()
	v, err := c.Version(context.Background())
	if err != nil {
		return "an unknown tmux"
	}
	return "tmux " + v
}

// The field separator has to be a character tmux never escapes.
//
// tmux before 3.5 escapes non-printable bytes in its own output as octal, so
// the old separator -- a real 0x1F -- arrived as the four characters \, 0, 3,
// 7 and every record came back as one field. Measured on tmux 3.4, which is
// what Ubuntu 24.04 LTS ships:
//
//	3.6:  "vp_p\x1f/some/path\x1fEND"
//	3.4:  "vp_p\\037/some/path\\037END"
//
// Unescaping is not available as a repair: 3.4 leaves backslash alone, so a
// directory named `lit\037here` and an escaped separator are the same eight
// characters, and telling them apart is guessing.
//
// This test cannot run an old tmux, so it pins the property that makes the
// difference instead. Anything in ASCII's control range fails here, which is
// the whole class the escaping applies to.
func TestTheFieldSeparatorIsNotSomethingOldTmuxWouldEscape(t *testing.T) {
	if fieldSep == "" {
		t.Fatal("empty field separator")
	}
	for _, r := range fieldSep {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("the field separator contains %U, which tmux 3.4 emits as an octal escape; "+
				"every record then parses as one field and every session disappears from the listing",
				r)
		}
	}
	// And it has to be scrubbed out of the values, because a printable
	// separator is one a directory can legally be named with.
	if !strings.Contains(scrubbed("pane_current_path"), fieldSep) {
		t.Errorf("scrubbed() does not remove the separator from values: %q",
			scrubbed("pane_current_path"))
	}
}
