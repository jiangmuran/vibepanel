package tmux

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
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
