// Package tmux is a thin, typed wrapper over the tmux(1) CLI.
//
// Why tmux at all: it is the only piece of this system that must outlive the
// vibepanel process. The Go server is a client that attaches; when it restarts
// (upgrade, crash, `systemctl restart`) every agent keeps running because the
// processes are children of the tmux server, not of us. Owning the PTYs
// directly would mean a restart kills every session — which is exactly the
// failure this project exists to avoid.
package tmux

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the tmux configuration the panel runs its server with. See the
// file itself for why each option is load-bearing.
//
//go:embed vibepanel.conf
var Config []byte

// fieldSep separates fields inside a single -F format line.
//
// ASCII Unit Separator: a pane title or a working directory can legally
// contain spaces, tabs, colons and pipes, so every "obvious" separator is a
// parsing bug waiting for the first path with a space in it.
const fieldSep = "\x1f"

// ErrNoServer means no tmux server is listening on our socket. It is a normal
// startup condition (nothing created yet), not a failure — callers should treat
// it as "zero sessions" rather than propagating it to the user.
var ErrNoServer = errors.New("tmux: no server running on socket")

// ErrNoSession means the named session does not exist.
var ErrNoSession = errors.New("tmux: session not found")

// Client talks to one tmux server, identified by its socket name.
type Client struct {
	// Socket is the -L socket name. vibepanel uses a dedicated one so that it
	// can never enumerate, resize or kill sessions belonging to the user's own
	// interactive tmux (or to any other tool on the box).
	Socket string

	// ConfigPath is the -f config file, written by EnsureServer.
	ConfigPath string

	// Bin is the tmux executable; overridable for tests and odd installs.
	Bin string
}

// New returns a Client for the given socket, keeping its config file in dir.
func New(socket, dir string) *Client {
	return &Client{
		Socket:     socket,
		ConfigPath: filepath.Join(dir, "tmux.conf"),
		Bin:        "tmux",
	}
}

// args prefixes the socket and config flags onto a command.
//
// -f is passed on every call even though tmux only reads it when the server
// starts. That way whichever command happens to be first also brings the
// server up correctly — there is no ordering rule for callers to get wrong.
func (c *Client) args(rest ...string) []string {
	a := make([]string, 0, len(rest)+4)
	a = append(a, "-L", c.Socket)
	if c.ConfigPath != "" {
		a = append(a, "-f", c.ConfigPath)
	}
	return append(a, rest...)
}

// target renders a session name as an exact-match tmux target.
//
// The leading '=' disables tmux's default prefix matching. Without it a target
// of "vp_ab" also matches a session named "vp_abcd", so a kill aimed at one
// session can land on a different one — silently, and only once two generated
// names happen to share a prefix. The trailing ':' selects the session's
// current window, which is what every pane-scoped command here wants.
func target(name string) string { return "=" + name + ":" }

// sessionTarget is the exact-match form for session-scoped commands.
//
// It deliberately omits the ':' window suffix that target() appends. Asking for
// a window of a session that does not exist makes tmux fail with "no current
// target" instead of "can't find session", which turns an ordinary "does this
// exist?" check into an unrecognised error.
func sessionTarget(name string) string { return "=" + name }

// run executes a tmux command and returns trimmed stdout.
func (c *Client) run(ctx context.Context, rest ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, c.args(rest...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		switch {
		case strings.Contains(msg, "no server running"),
			strings.Contains(msg, "error connecting"):
			return "", ErrNoServer
		case strings.Contains(msg, "session not found"),
			strings.Contains(msg, "can't find session"),
			strings.Contains(msg, "no current target"):
			return "", ErrNoSession
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", strings.Join(rest, " "), msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// EnsureServer writes the config file and makes sure a server is running with
// it. Safe to call repeatedly.
//
// The config is rewritten on every start rather than only when missing: after
// an upgrade the binary's embedded config is the truth, and a stale file on
// disk would silently keep an old option set alive on a long-running server.
func (c *Client) EnsureServer(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(c.ConfigPath), 0o700); err != nil {
		return fmt.Errorf("tmux: config dir: %w", err)
	}
	if err := os.WriteFile(c.ConfigPath, Config, 0o600); err != nil {
		return fmt.Errorf("tmux: write config: %w", err)
	}
	if c.ServerRunning(ctx) {
		return nil
	}
	if _, err := c.run(ctx, "start-server"); err != nil {
		return fmt.Errorf("tmux: start server: %w", err)
	}
	return nil
}

// ServerRunning reports whether our tmux server is up.
func (c *Client) ServerRunning(ctx context.Context) bool {
	_, err := c.run(ctx, "list-sessions", "-F", "#{session_name}")
	return err == nil
}

// Version returns the tmux version string, e.g. "3.6".
func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, c.Bin, "-V").Output()
	if err != nil {
		return "", fmt.Errorf("tmux -V: %w", err)
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "tmux "), nil
}

// CreateOptions describes a session to create.
type CreateOptions struct {
	Name    string   // tmux session name, e.g. "vp_a3f1c2d4"
	Dir     string   // working directory
	Command []string // argv to run; empty means the user's login shell
	Env     []string // extra "K=V" pairs, injected with -e
	Width   int      // initial grid width
	Height  int      // initial grid height
}

// Create starts a detached session. It is an error if the name is taken.
//
// Everything about how the session behaves comes from the config file loaded
// at server start, not from post-hoc set-option calls: history-limit in
// particular only applies to panes created after it is set, so configuring a
// session after new-session would leave the first pane with tmux's 2000-line
// default and a scrollback the panel cannot fully replay.
func (c *Client) Create(ctx context.Context, o CreateOptions) error {
	if o.Width <= 0 {
		o.Width = 120
	}
	if o.Height <= 0 {
		o.Height = 32
	}
	argv := []string{
		"new-session", "-d",
		"-s", o.Name,
		"-x", strconv.Itoa(o.Width),
		"-y", strconv.Itoa(o.Height),
	}
	if o.Dir != "" {
		argv = append(argv, "-c", o.Dir)
	}
	for _, kv := range o.Env {
		argv = append(argv, "-e", kv)
	}
	if len(o.Command) > 0 {
		argv = append(argv, o.Command...)
	}
	_, err := c.run(ctx, argv...)
	return err
}

// SocketPath is where tmux puts the socket for this client's name.
//
// Mirrors tmux's own rule: $TMUX_TMPDIR, else /tmp, then a tmux-<uid>
// directory. Used by tests to remove the file after killing the server —
// kill-server leaves it behind, and a few hundred dead sockets accumulating in
// /tmp is a poor look for a tool whose whole job is managing tmux.
func (c *Client) SocketPath() string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), c.Socket)
}

// AttachArgs returns the argv (after the binary) for attaching to a session.
//
// The caller runs this itself on a PTY it owns, rather than going through
// Client.run, because attaching is the one tmux command whose whole purpose is
// to keep a terminal open and stream through it.
//
// Deliberately a plain attach, not `attach -d`: detaching other clients is
// pointless when the panel is the only one, and hostile if someone is
// debugging the same socket from a shell.
func (c *Client) AttachArgs(name string) []string {
	return c.args("attach-session", "-t", target(name))
}

// Has reports whether the named session exists.
func (c *Client) Has(ctx context.Context, name string) (bool, error) {
	_, err := c.run(ctx, "has-session", "-t", sessionTarget(name))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNoServer), errors.Is(err, ErrNoSession):
		return false, nil
	case strings.Contains(err.Error(), "can't find"):
		return false, nil
	}
	return false, err
}

// Kill removes a session and everything running in it.
func (c *Client) Kill(ctx context.Context, name string) error {
	_, err := c.run(ctx, "kill-session", "-t", sessionTarget(name))
	if errors.Is(err, ErrNoServer) || errors.Is(err, ErrNoSession) {
		return nil // already gone; killing twice is not an error
	}
	if err != nil && strings.Contains(err.Error(), "can't find") {
		return nil
	}
	return err
}

// Resize sets the authoritative grid size for a session.
func (c *Client) Resize(ctx context.Context, name string, width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("tmux: invalid size %dx%d", width, height)
	}
	_, err := c.run(ctx, "resize-window", "-t", target(name),
		"-x", strconv.Itoa(width), "-y", strconv.Itoa(height))
	return err
}

// SendKeys writes literal bytes to a session's active pane.
//
// -l means literal: without it tmux parses the payload as key names, so a user
// typing the word "Enter" would submit a newline instead of five characters.
func (c *Client) SendKeys(ctx context.Context, name, literal string) error {
	_, err := c.run(ctx, "send-keys", "-t", target(name), "-l", literal)
	return err
}

// Capture returns the pane's scrollback including SGR escape sequences.
//
// This is the cold path used when the backend restarts and has lost its
// in-memory replay buffer: it reconstructs what the user was looking at
// instead of showing a blank terminal attached to a live session.
func (c *Client) Capture(ctx context.Context, name string) (string, error) {
	return c.run(ctx, "capture-pane", "-p", "-e", "-J", "-S", "-", "-t", target(name))
}

// CaptureScrollback returns only the history above the visible screen.
//
// Used to prime a replay buffer before attaching. Attaching makes tmux repaint
// the visible screen, so capturing that too would draw it twice; ending the
// range one line into the history lets the two compose exactly.
func (c *Client) CaptureScrollback(ctx context.Context, name string) (string, error) {
	return c.run(ctx, "capture-pane", "-p", "-e", "-J", "-S", "-", "-E", "-1", "-t", target(name))
}

// Info is a point-in-time snapshot of one session, used for naming, state
// heuristics and the sidebar.
type Info struct {
	Name  string
	Title string // #{pane_title} — set by the app via OSC 0/2
	// Command is #{pane_current_command}, e.g. "claude" or "bash".
	//
	// Eventually consistent: for roughly 200ms after Create it still reads
	// "tmux", because the pane's fork has not exec'd the real command yet.
	// Never cache the first reading as a session's identity — poll for it.
	Command string
	Path    string // #{pane_current_path} — the pane's cwd right now
	PID     int    // #{pane_pid} — first process in the pane
	Dead    bool   // #{pane_dead} — process exited, output preserved
	// Bell is #{window_bell_flag}.
	//
	// Always false under the panel's configuration: with bell-action "any"
	// tmux forwards the bell to the client instead of latching the flag for a
	// status line that is turned off. The real signal is the \007 in the PTY
	// stream, which the OSC scanner picks up. Kept because the field is free
	// and a future configuration might want it — but do not build on it.
	Bell        bool
	Width       int
	Height      int
	Activity    int64 // #{session_activity} — unix seconds
	AlternateOn bool  // #{alternate_on} — a full-screen TUI is drawing
}

// infoFields must stay in the same order as parseInfo reads them.
var infoFields = []string{
	"#{session_name}",
	"#{pane_title}",
	"#{pane_current_command}",
	"#{pane_current_path}",
	"#{pane_pid}",
	"#{pane_dead}",
	"#{window_bell_flag}",
	"#{window_width}",
	"#{window_height}",
	"#{session_activity}",
	"#{alternate_on}",
}

var infoFormat = strings.Join(infoFields, fieldSep)

// List returns a snapshot of every session on our socket.
//
// A missing server yields an empty slice and no error: "nothing created yet"
// is the normal state on a fresh install, and making every caller special-case
// it would guarantee someone forgets.
func (c *Client) List(ctx context.Context) ([]Info, error) {
	out, err := c.run(ctx, "list-sessions", "-F", infoFormat)
	if errors.Is(err, ErrNoServer) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	lines := strings.Split(out, "\n")
	infos := make([]Info, 0, len(lines))
	for _, line := range lines {
		info, perr := parseInfo(line)
		if perr != nil {
			// One malformed line (a pane title containing a newline, say) must
			// not blind the sidebar to every other session.
			continue
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Get returns a snapshot of one session.
func (c *Client) Get(ctx context.Context, name string) (Info, error) {
	out, err := c.run(ctx, "display-message", "-p", "-t", target(name), infoFormat)
	if err != nil {
		return Info{}, err
	}
	return parseInfo(out)
}

func parseInfo(line string) (Info, error) {
	f := strings.Split(line, fieldSep)
	if len(f) != len(infoFields) {
		return Info{}, fmt.Errorf("tmux: expected %d fields, got %d", len(infoFields), len(f))
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	return Info{
		Name:        f[0],
		Title:       f[1],
		Command:     f[2],
		Path:        f[3],
		PID:         atoi(f[4]),
		Dead:        f[5] == "1",
		Bell:        f[6] == "1",
		Width:       atoi(f[7]),
		Height:      atoi(f[8]),
		Activity:    int64(atoi(f[9])),
		AlternateOn: f[10] == "1",
	}, nil
}

// ClearBell acknowledges a window's bell flag so the next one is a fresh edge.
//
// Without this the flag latches on: a session that rang once would look like it
// is asking for attention forever, and the "waiting" badge would never clear.
func (c *Client) ClearBell(ctx context.Context, name string) error {
	// Selecting the window is how tmux itself clears the flag; there is no
	// dedicated command for it.
	_, err := c.run(ctx, "select-window", "-t", target(name))
	if errors.Is(err, ErrNoSession) {
		return nil
	}
	return err
}

// KillServer tears down the whole socket. Only used by tests and by an explicit
// "shut everything down" action — never on normal shutdown, since the entire
// point is that sessions outlive us.
func (c *Client) KillServer(ctx context.Context) error {
	_, err := c.run(ctx, "kill-server")
	if errors.Is(err, ErrNoServer) {
		return nil
	}
	return err
}
