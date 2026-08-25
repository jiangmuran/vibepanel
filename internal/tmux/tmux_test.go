package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
		{[]string{"show-options", "-wg", "-v", "allow-set-title"}, "on"},
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
