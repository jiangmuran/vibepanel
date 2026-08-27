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
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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
// U+241F SYMBOL FOR UNIT SEPARATOR -- the printable picture of the character,
// not the character. A pane title or a working directory can legally contain
// spaces, tabs, colons and pipes, so every "obvious" separator is a parsing bug
// waiting for the first path with a space in it. Any value that could contain
// this one is scrubbed of it by scrubbed() below, so it cannot appear except
// where this puts it.
//
// It was the real ASCII Unit Separator, 0x1F, and that broke every tmux before
// 3.5. Measured against tmux 3.4, which is what Ubuntu 24.04 LTS ships:
//
//	3.6:  "vp_p\x1f/some/path\x1fEND"
//	3.4:  "vp_p\\037/some/path\\037END"
//
// Old tmux escapes non-printable bytes in its own output as octal, so the
// separator arrived as the four characters \, 0, 3, 7 and Split found one
// field where thirteen were expected. Every symptom named something else:
// sessions missing from the listing, panes that never started their command,
// "expected 13 fields, got 1".
//
// Unescaping it back is not an option, because the escaping is not reversible:
// 3.4 does not escape backslash, so a directory genuinely named `lit\037here`
// and an escaped separator are the same eight characters. `mkdir` is all it
// takes to make a running session vanish, which is the exact failure the
// separator was chosen to avoid in the first place.
//
// So the separator has to be something tmux never escapes. Multi-byte UTF-8
// passes through both versions untouched, and this codepoint says what it is.
const fieldSep = "\u241f"

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

// Paste delivers text to a pane the way a paste arrives, not the way typing
// does.
//
// The difference is bracketed paste. A multi-line block written byte by byte
// into the PTY is indistinguishable from someone typing three lines and
// pressing Enter three times, so a shell runs each line and an agent acts on
// the first sentence before it has read the last. Wrapped in ESC[200~ / ESC[201~
// the same block is one submission.
//
// The wrapping cannot be done blind: an application that has not asked for
// bracketed paste receives the markers as literal garbage. tmux knows which
// panes asked, because it tracks the mode for each of them, and `paste-buffer
// -p` brackets only when the target pane wants it. Delegating that is the
// whole reason this goes through tmux rather than through the PTY.
//
// `-d` deletes the buffer afterwards: the text is somebody's prompt, and the
// paste buffer is shared with anything else on this socket.
func (c *Client) Paste(ctx context.Context, name, text string) error {
	const buf = "vibepanel-paste"
	if _, err := c.runStdin(ctx, text, "load-buffer", "-b", buf, "-"); err != nil {
		return err
	}
	_, err := c.run(ctx, "paste-buffer", "-d", "-p", "-b", buf, "-t", target(name))
	return err
}

// runStdin is run with something on the command's standard input.
func (c *Client) runStdin(ctx context.Context, stdin string, rest ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, c.args(rest...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", strings.Join(rest, " "), msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

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
	// Stamp what the server was started with.
	//
	// `-f` is read once, at start-server, and the panel never kills its server
	// -- that is the premise of the project. So the file above is rewritten on
	// every upgrade and the running server goes on using whatever it read at
	// boot: a config change takes effect at the next reboot or not at all. That
	// covers allow-passthrough, which is the reason tmux 3.3 is the floor, and
	// the smcup@/rmcup@ and indn@ overrides.
	//
	// Nothing could see the difference, and an upgrade leaves a new binary that
	// is not running and a new tmux config that is not loaded while both look
	// installed. A hash in a server option is enough to tell, and it costs one
	// command at a moment when the server has just started anyway.
	//
	// Best effort: a tmux too old to know the option is not a reason to refuse
	// to start, and the version check has its own line in doctor.
	if _, err := c.run(ctx, "set-option", "-s", configStampOption, ConfigStamp()); err != nil {
		// Deliberately not returned. The stamp is a diagnostic, and failing a
		// start over one would trade a working panel for a readable warning.
		_ = err
	}
	return nil
}

// configStampOption is where the running server records the config it read.
const configStampOption = "@vibepanel-conf"

// ConfigStamp is a short hash of the embedded tmux config.
func ConfigStamp() string {
	sum := sha256.Sum256(Config)
	return hex.EncodeToString(sum[:8])
}

// RunningConfigStamp reports the config the running server was started with.
//
// Empty means either no server, or one started before this option existed --
// which is itself the interesting answer, because a server that predates the
// stamp predates the config change that added it.
func (c *Client) RunningConfigStamp(ctx context.Context) string {
	out, err := c.run(ctx, "show-options", "-s", "-v", configStampOption)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ServerRunning reports whether our tmux server is up.
func (c *Client) ServerRunning(ctx context.Context) bool {
	_, err := c.run(ctx, "list-sessions", "-F", "#{session_name}")
	return err == nil
}

// MinMajor and MinMinor are the oldest tmux the embedded config is valid for.
//
// 3.3 because of allow-passthrough. An older tmux does not refuse the config:
// it reports an unknown option, carries on with defaults, and from then on
// swallows the sequences agent TUIs use for progress and notifications. Every
// symptom of that is something not appearing, which is why it is worth saying
// at startup rather than leaving in a requirements list.
const (
	MinMajor = 3
	MinMinor = 3
)

// ParseVersion pulls the major and minor numbers out of a tmux version string.
//
// tmux reports "3.4", "3.3a" for a patch release, and "next-3.6" for a
// development build, so a plain split on "." is not enough: the letters and
// any prefix have to be ignored rather than parsed.
func ParseVersion(s string) (major, minor int, ok bool) {
	digits := func(r rune) bool { return r >= '0' && r <= '9' }
	fields := strings.FieldsFunc(s, func(r rune) bool { return !digits(r) && r != '.' })
	for _, f := range fields {
		maj, min, found := strings.Cut(f, ".")
		if !found {
			continue
		}
		a, err1 := strconv.Atoi(maj)
		// A minor like "3a" has already been split off by FieldsFunc, but a
		// trailing dot or an empty half can still arrive here.
		b, err2 := strconv.Atoi(min)
		if err1 != nil || err2 != nil {
			continue
		}
		return a, b, true
	}
	return 0, 0, false
}

// AtLeastMinimum reports whether a version string is new enough for the
// embedded config. An unparseable version is treated as new enough: refusing
// to run because a version string looked unfamiliar would be worse than the
// problem.
func AtLeastMinimum(v string) bool {
	major, minor, ok := ParseVersion(v)
	if !ok {
		return true
	}
	return major > MinMajor || (major == MinMajor && minor >= MinMinor)
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

// Capture returns the pane's scrollback including SGR escape sequences.
//
// This is the cold path used when the backend restarts and has lost its
// in-memory replay buffer: it reconstructs what the user was looking at
// instead of showing a blank terminal attached to a live session.
func (c *Client) Capture(ctx context.Context, name string) (string, error) {
	return c.run(ctx, "capture-pane", "-p", "-e", "-J", "-S", "-", "-t", target(name))
}

// CaptureLines is Capture with a bound: at most lines of history above the
// visible screen, plus the screen itself.
//
// The bound is applied by tmux rather than by trimming afterwards, and that is
// the whole reason this exists. Measured on tmux 3.6 against a pane holding a
// full 20,000-line history of coloured 130-column output, three runs each:
//
//	-S -      2,971,621 bytes    69 ms
//	-S -8000  1,195,852 bytes    31 ms
//	-S -4000    601,591 bytes    19 ms
//	-S -2000    304,423 bytes    13 ms
//	-S -1000    155,836 bytes     8 ms
//
// Cost is linear in the lines asked for, so the choice of bound is the choice
// of cost. At the two dozen sessions this panel is built for, the unbounded
// capture is 71 MB and 1.7 seconds of tmux per pass — which is not something to
// do on a timer next to a poller that runs every two seconds.
//
// A non-positive count means the whole history, i.e. Capture.
func (c *Client) CaptureLines(ctx context.Context, name string, lines int) (string, error) {
	if lines <= 0 {
		return c.Capture(ctx, name)
	}
	return c.run(ctx, "capture-pane", "-p", "-e", "-J",
		"-S", "-"+strconv.Itoa(lines), "-t", target(name))
}

// Respawn restarts the process in a session's pane, reusing the command it was
// created with.
//
// This is the only way back from a dead pane. Without it a crashed agent is a
// corpse you can read but not revive, and the answer to "claude died overnight"
// is to leave the panel and go find a terminal — which is the thing the panel
// exists to avoid.
//
// -k kills anything still running first. A pane whose process merely stopped
// responding is the other case people reach for this in, and respawn-pane
// refuses on a live pane without it.
func (c *Client) Respawn(ctx context.Context, name string) error {
	_, err := c.run(ctx, "respawn-pane", "-k", "-t", target(name))
	if err != nil {
		return fmt.Errorf("tmux: respawn %s: %w", name, err)
	}
	return nil
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
	// DeadStatus is #{pane_dead_status}, the wait status of the exited
	// process. Only meaningful while Dead; tmux leaves it empty otherwise, so
	// it reads as 0 and a live pane must never be described by it.
	//
	// It is also empty for a pane that did not exit but was killed, which is a
	// different thing that read identically. See DeadSignal.
	DeadStatus int
	// DeadSignal is #{pane_dead_signal}, set instead of DeadStatus when the
	// process was killed rather than having exited.
	//
	// Measured: SIGKILL leaves dead_status empty and dead_signal 9. The panel
	// read only the first, so a process killed by the OOM killer reported
	// "exited with status 0" — the same as one that finished its work. On a
	// machine running a couple of dozen agents that is not a rare distinction.
	DeadSignal int
	// Bell is #{window_bell_flag}, and it is load-bearing exactly once: at
	// startup, before anything attaches.
	//
	// This said "always false under the panel's configuration … do not build
	// on it". That is wrong, and the panel does build on it — Reconcile reads
	// it to recover a bell that rang while the panel was down, which is the
	// one moment nobody was watching. Measured under the embedded config, with
	// no client attached: the flag reads 1, and attaching clears it to 0.
	//
	// While a client is attached it is indeed always false, because tmux
	// forwards the bell to that client instead of latching it, and the \007 on
	// the PTY is what the scanner picks up. Both halves are true; the comment
	// only stated the second one and then told the next reader to delete the
	// first.
	Bell        bool
	Width       int
	Height      int
	Activity    int64 // #{session_activity} — unix seconds
	AlternateOn bool  // #{alternate_on} — a full-screen TUI is drawing
}

// scrubbed wraps a format variable so tmux replaces control characters before
// we ever see them.
//
// list-sessions puts one session per line, so a value containing a newline --
// or the field separator -- takes the whole record apart. Measured, on a real
// tmux: a session whose working directory was named with a newline in it, and
// another with a 0x1f, both *disappeared from the listing* — parseInfo counted
// the wrong number of fields and List drops the line rather than blind the
// sidebar to everything else.
//
// The separator is in the class too, not only the control characters. It is a
// printable codepoint now (see fieldSep), which means a directory can be named
// with it, and a separator that a value may contain is not a separator.
//
// Disappearing is bad enough. What made it a real defect is what happens next:
// the poller treats a session it cannot see as gone, and now writes that to the
// database — so a running session is announced as dead because of the name of
// the directory it is sitting in. `mkdir $'a\nb'` is all it takes, and the
// panel is for people who run agents in directories they did not choose.
//
// tmux does this substitution itself, so the value is clean before it is
// joined. A literal character range rather than [[:cntrl:]]: the modifier is
// terminated by a colon, and a POSIX class contains two of them, so the parser
// splits in the wrong place and the whole field comes back empty. That failure
// is silent, which is why this comment names the shape that works.
func scrubbed(variable string) string {
	return "#{s/[\x01-\x1f]|" + fieldSep + "/?/:" + variable + "}"
}

// infoFields must stay in the same order as parseInfo reads them.
var infoFields = []string{
	"#{session_name}",
	scrubbed("pane_title"),
	scrubbed("pane_current_command"),
	scrubbed("pane_current_path"),
	"#{pane_pid}",
	"#{pane_dead}",
	"#{window_bell_flag}",
	"#{window_width}",
	"#{window_height}",
	"#{session_activity}",
	"#{alternate_on}",
	// Appended rather than inserted: parseInfo indexes this slice positionally.
	"#{pane_dead_status}",
	"#{pane_dead_signal}",
}

var infoFormat = strings.Join(infoFields, fieldSep)

// ExitStatus is how the pane ended, in the one number the rest of the panel
// carries.
//
// 128+signal for a kill, which is the convention every shell uses and makes
// the common ones recognisable on sight: 137 is SIGKILL, 139 a segfault, 143 a
// SIGTERM. Reading dead_status alone reported all of them as 0, which is the
// number a task that finished cleanly has.
func (i Info) ExitStatus() int {
	if i.DeadSignal > 0 {
		return 128 + i.DeadSignal
	}
	return i.DeadStatus
}

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
		// The line itself, quoted, because the count alone names nothing. A
		// format variable this tmux does not know expands to empty and the
		// count still comes out right; a *modifier* it does not know makes
		// tmux refuse the whole format and put its complaint here instead --
		// and "got 1" is then a sentence in English that the caller throws
		// away. One CI run on an older tmux was spent guessing at which
		// variable it was, from an error that was holding the answer.
		return Info{}, fmt.Errorf("tmux: expected %d fields, got %d in %q",
			len(infoFields), len(f), line)
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	return Info{
		Name:        f[0],
		Title:       f[1],
		Command:     f[2],
		Path:        f[3],
		PID:         atoi(f[4]),
		Dead:        f[5] == "1",
		DeadStatus:  atoi(f[11]),
		DeadSignal:  atoi(f[12]),
		Bell:        f[6] == "1",
		Width:       atoi(f[7]),
		Height:      atoi(f[8]),
		Activity:    int64(atoi(f[9])),
		AlternateOn: f[10] == "1",
	}, nil
}

// KillServer tears down the whole socket. Only used by tests and by an explicit
// "shut everything down" action — never on normal shutdown, since the entire
// point is that sessions outlive us.
// SessionEnvValue reads one variable out of a session's environment.
//
// Sessions are given VIBEPANEL_URL and VIBEPANEL_TOKEN with -e at creation, and
// `set-environment` on a live session reaches only panes started after it. So
// what a session was handed when it was made is what its hooks will keep
// posting to for as long as it lives, whatever the panel is configured with
// now. Asking tmux is the only way to find out what that was.
//
// Returns "" with no error when the variable is not set, which is an ordinary
// state -- a session created before hooks were installed, or by hand.
func (c *Client) SessionEnvValue(ctx context.Context, name, key string) (string, error) {
	out, err := c.run(ctx, "show-environment", "-t", target(name), key)
	if err != nil {
		// Measured against a real tmux rather than assumed: a variable that was
		// never set is an *error*, "unknown variable: X", not the "-KEY" line
		// the manual's output format suggests. The first version of this looked
		// for the dash and reported a missing variable as a broken session.
		if strings.Contains(err.Error(), "unknown variable") {
			return "", nil
		}
		return "", err
	}
	// "KEY=value", or "-KEY" for one explicitly removed from the environment.
	if strings.HasPrefix(out, "-") {
		return "", nil
	}
	_, v, ok := strings.Cut(out, "=")
	if !ok {
		return "", nil
	}
	return v, nil
}

func (c *Client) KillServer(ctx context.Context) error {
	_, err := c.run(ctx, "kill-server")
	if errors.Is(err, ErrNoServer) {
		return nil
	}
	return err
}
