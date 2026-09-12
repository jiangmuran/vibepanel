package session

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/tmux"
)

func TestSchemeReportsMode(t *testing.T) {
	for _, tc := range []struct {
		in       string
		on, seen bool
	}{
		{"ordinary output", false, false},
		{"\x1b[?2031h", true, true},
		{"\x1b[?2031l", false, true},
		{"\x1b[?2031h then \x1b[?2031l", false, true},
		{"\x1b[?2031l then \x1b[?2031h", true, true},
	} {
		on, seen := schemeReportsMode([]byte(tc.in))
		if on != tc.on || seen != tc.seen {
			t.Errorf("schemeReportsMode(%q) = %v, %v; want %v, %v", tc.in, on, seen, tc.on, tc.seen)
		}
	}
}

// TestASchemeChangeIsSentOnceAndOnlyToAClientThatAsked.
//
// Two ways to get this wrong, and both are worse than not doing it.
//
// Sent to a client that never turned mode 2031 on, `\x1b[?997;1n` is not a
// report -- it is bytes on stdin, and tmux types them into whichever pane has
// focus. And sent on every call rather than on a change, it goes to tmux on
// every subscribe from every tab, each one making tmux ask for the colours
// again.
//
// A pipe stands in for the PTY, which is all SetScheme touches.
func TestASchemeChangeIsSentOnceAndOnlyToAClientThatAsked(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close() //nolint:errcheck // test
	defer w.Close() //nolint:errcheck // test

	read := func() string {
		_ = r.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		buf := make([]byte, 64)
		n, _ := r.Read(buf)
		return string(buf[:n])
	}

	l := &Live{ptmx: w}

	l.SetScheme(true)
	if got := read(); got != "" {
		t.Errorf("sent %q to a client that never asked for scheme reports", got)
	}

	l.mu.Lock()
	l.schemeReports = true
	l.mu.Unlock()

	l.SetScheme(false)
	if got := read(); got != "\x1b[?997;2n" {
		t.Errorf("switching to light sent %q, want the light report", got)
	}
	l.SetScheme(false)
	if got := read(); got != "" {
		t.Errorf("an unchanged scheme was sent again: %q", got)
	}
	l.SetScheme(true)
	if got := read(); got != "\x1b[?997;1n" {
		t.Errorf("switching to dark sent %q, want the dark report", got)
	}
}

// TestTmuxIsToldWhichThemeTheBrowserIsOn, against a real tmux.
//
// tmux's own `#{client_theme}` is the witness, and it is the same thing that
// was read off the running panel when this was found: six clients, every one
// of them "light", on a panel being used in dark mode.
//
// Both halves. RememberScheme before Attach is the restart case -- Reconcile
// attaches every session before any browser has spoken. SetScheme after is a
// browser switching theme on a session that is already attached.
func TestTmuxIsToldWhichThemeTheBrowserIsOn(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)

	// `#{client_theme}` arrived in tmux 3.6. On anything older it expands to
	// the empty string rather than failing, so this test read "" and reported
	// it as the panel having told tmux nothing -- which is what it did on
	// every CI run from the commit that added it: the runner installs tmux
	// from Ubuntu's archive, which is 3.4, and main was red for three pushes
	// before anybody looked at the workflow rather than at `make verify`.
	//
	// Skipped rather than weakened: what it proves needs tmux to have somewhere
	// to report the answer. The rule itself -- a change is sent, once, and only
	// to a client that asked -- is checked without any tmux at all by
	// TestASchemeChangeIsSentOnceAndOnlyToAClientThatAsked above.
	//
	// An unparseable version runs the test, following AtLeastMinimum: a
	// version string nobody recognises is likelier to be new than old, and
	// failing loudly is better than skipping silently.
	if v, err := tm.Version(ctx); err == nil {
		if major, minor, ok := tmux.ParseVersion(v); ok && (major < 3 || (major == 3 && minor < 6)) {
			t.Skipf("tmux %s has no #{client_theme}; it arrived in 3.6", v)
		}
	}

	const name = "vp_scheme"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 100, Height: 30,
		Command: []string{"sh", "-c", "sleep 60"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	t.Cleanup(m.DetachAll)

	m.RememberScheme(true)
	live, err := m.Attach(ctx, "s1", name, 100, 30)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	bin := tm.Bin
	if bin == "" {
		bin = "tmux"
	}
	theme := func() string {
		out, _ := exec.Command(bin, "-S", tm.SocketPath(), "list-clients", "-F", "#{client_theme}").Output()
		return strings.TrimSpace(string(out))
	}
	waitFor := func(want, why string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if theme() == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: tmux says the client is %q, want %q", why, theme(), want)
	}

	waitFor("dark", "a session attached after a browser reported dark")
	live.SetScheme(false)
	waitFor("light", "the browser switched to light")
	live.SetScheme(true)
	waitFor("dark", "the browser switched back")
}
