package headless

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jiangmuran/vibepanel/internal/id"
)

// Run states, as GET /api/headless/runs reports them.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateError   = "error"
	StateStopped = "stopped"
)

const (
	// bufferCap is how many events a run keeps for replay. A long answer is
	// thousands of text deltas; past the cap the oldest deltas are merged,
	// which costs a reconnecting client nothing but granularity.
	bufferCap = 5000
	// keepFinished is how long a finished run stays listable and replayable,
	// so glasses that slept through the end of an answer can still read it.
	keepFinished = 30 * time.Minute
	// stopGrace is SIGTERM to SIGKILL.
	stopGrace = 3 * time.Second
	// stderrKeep is how much stderr an error event quotes.
	stderrKeep = 2000
)

// Errors Start answers with; the HTTP layer maps them to 409 and 429.
var (
	ErrSessionBusy = errors.New("that session already has a run in progress")
	ErrTooMany     = errors.New("too many runs in progress")
)

// Spec is one run to start.
type Spec struct {
	Binary    string
	Args      []string
	Dir       string
	Env       []string
	Timeout   time.Duration
	SessionID string
	ProjectID string
	Prompt    string
	// MaxConcurrent is the cap at the moment of starting; settings can
	// change between runs, so it travels with the run rather than living on
	// the registry.
	MaxConcurrent int
}

// Run is one `claude -p` and everything it has said.
type Run struct {
	ID        string
	SessionID string
	ProjectID string
	Prompt    string
	StartedAt time.Time

	mu       sync.Mutex
	events   []Event
	seq      int64
	changed  chan struct{} // closed and replaced on every append
	state    string
	endedAt  time.Time
	terminal bool
	cmd      *exec.Cmd
	exited   bool
	// why a kill was asked for, which decides the terminal event.
	stopAsked bool
	timedOut  bool
	timeout   time.Duration
}

// Info is a run as the list shows it.
type Info struct {
	RunID     string `json:"runId"`
	SessionID string `json:"sessionId"`
	ProjectID string `json:"projectId"`
	State     string `json:"state"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   int64  `json:"endedAt"`
	Prompt    string `json:"prompt"`
}

// Registry holds the runs in progress and the recently finished.
type Registry struct {
	mu   sync.Mutex
	runs map[string]*Run
	// now is a clock a test can move.
	now func() time.Time
}

// NewRegistry is an empty registry.
func NewRegistry() *Registry {
	return &Registry{runs: map[string]*Run{}, now: time.Now}
}

// Start launches the run, or refuses it when its session is already running
// or the cap is reached. The check and the registration are one critical
// section, so two requests at once cannot both slip under the cap.
func (g *Registry) Start(spec Spec) (*Run, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
	running := 0
	for _, r := range g.runs {
		if r.State() != StateRunning {
			continue
		}
		running++
		if r.SessionID == spec.SessionID {
			return nil, ErrSessionBusy
		}
	}
	if spec.MaxConcurrent > 0 && running >= spec.MaxConcurrent {
		return nil, ErrTooMany
	}

	run := &Run{
		ID:        "r_" + id.New(),
		SessionID: spec.SessionID,
		ProjectID: spec.ProjectID,
		Prompt:    spec.Prompt,
		StartedAt: g.now(),
		changed:   make(chan struct{}),
		state:     StateRunning,
		timeout:   spec.Timeout,
	}
	if err := run.start(spec); err != nil {
		return nil, err
	}
	g.runs[run.ID] = run
	return run, nil
}

// Get is one run by id.
func (g *Registry) Get(runID string) (*Run, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
	r, ok := g.runs[runID]
	return r, ok
}

// List is every run kept, for one project or all ("" ), newest first.
func (g *Registry) List(projectID string) []Info {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
	out := []Info{}
	for _, r := range g.runs {
		if projectID != "" && r.ProjectID != projectID {
			continue
		}
		out = append(out, r.Info())
	}
	sortInfos(out)
	return out
}

// RunningSession reports whether a run is in progress on that session.
func (g *Registry) RunningSession(sessionID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.runs {
		if r.SessionID == sessionID && r.State() == StateRunning {
			return true
		}
	}
	return false
}

func (g *Registry) sweepLocked() {
	cut := g.now().Add(-keepFinished)
	for k, r := range g.runs {
		r.mu.Lock()
		old := r.state != StateRunning && r.endedAt.Before(cut)
		r.mu.Unlock()
		if old {
			delete(g.runs, k)
		}
	}
}

func sortInfos(xs []Info) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j].StartedAt > xs[j-1].StartedAt; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// State is the run's state now.
func (r *Run) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Info is the run's list entry.
func (r *Run) Info() Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ended int64
	if !r.endedAt.IsZero() {
		ended = r.endedAt.Unix()
	}
	return Info{
		RunID: r.ID, SessionID: r.SessionID, ProjectID: r.ProjectID,
		State: r.state, StartedAt: r.StartedAt.Unix(), EndedAt: ended,
		Prompt: clip(oneLine(r.Prompt), 120),
	}
}

// Since is every buffered event after seq, a channel closed when there is
// more, and whether the run has ended (its terminal event is buffered).
func (r *Run) Since(after int64) ([]Event, <-chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, r.changed, r.terminal
}

// Stop asks the run to end: SIGTERM to its process group now, SIGKILL after
// three seconds if anything is left. The terminal event is `stopped`. A run
// that has already ended is left alone.
func (r *Run) Stop() {
	r.mu.Lock()
	if r.terminal || r.exited {
		r.mu.Unlock()
		return
	}
	r.stopAsked = true
	r.mu.Unlock()
	r.kill()
}

// kill is SIGTERM to the group, then SIGKILL to the group if the leader has
// not exited by then. The group rather than the pid: claude runs shells,
// MCP servers and subagents, and killing only the parent leaves them working.
func (r *Run) kill() {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.AfterFunc(stopGrace, func() {
		r.mu.Lock()
		exited := r.exited
		r.mu.Unlock()
		if !exited {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})
}

// add buffers events and wakes every waiter. Nothing is added after the
// terminal event: exactly one of result, error or stopped ends a run.
func (r *Run) add(evs ...Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	woke := false
	for _, e := range evs {
		if r.terminal {
			break
		}
		r.seq++
		e.Seq = r.seq
		r.events = append(r.events, e)
		woke = true
		if e.Terminal() {
			r.terminal = true
			r.endedAt = time.Now()
			switch {
			case e.Type == "stopped":
				r.state = StateStopped
			case e.Type == "error", e.Type == "result" && e.Fields["isError"] == true:
				r.state = StateError
			default:
				r.state = StateDone
			}
		}
	}
	if len(r.events) > bufferCap {
		r.events = compact(r.events, bufferCap)
	}
	if woke {
		close(r.changed)
		r.changed = make(chan struct{})
	}
}

// compact brings a buffer back under limit by merging adjacent text deltas in
// its older half into one event carrying the run's last seq. The newer half
// is left alone, because that is where a live client is reading. If merging
// is not enough (a run of thousands of tool calls), the oldest events go.
func compact(evs []Event, limit int) []Event {
	half := len(evs) / 2
	var out []Event
	for i := 0; i < half; i++ {
		e := evs[i]
		if e.Type == "text" && len(out) > 0 && out[len(out)-1].Type == "text" {
			last := &out[len(out)-1]
			d, _ := last.Fields["d"].(string)
			nd, _ := e.Fields["d"].(string)
			last.Fields = map[string]any{"d": d + nd}
			last.Seq = e.Seq
			continue
		}
		out = append(out, e)
	}
	out = append(out, evs[half:]...)
	if len(out) > limit {
		out = append([]Event(nil), out[len(out)-limit:]...)
	}
	return out
}

func (r *Run) start(spec Spec) error {
	cmd := exec.Command(spec.Binary, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	// Closed: claude -p with a terminal or a pipe on stdin reads it as more
	// prompt, and the panel's stdin is nothing to read.
	cmd.Stdin = nil
	lines := &lineWriter{fn: func(line []byte) { r.add(Normalize(line)...) }}
	var stderr tailBuffer
	cmd.Stdout = lines
	cmd.Stderr = &stderr
	// Its own process group, so Stop and the timeout reach everything the
	// run started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A shell the assistant backgrounded can keep stdout open after claude
	// itself has exited; without this Wait would block on the pipe forever.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting claude: %w", err)
	}
	r.mu.Lock()
	r.cmd = cmd
	r.mu.Unlock()

	var timer *time.Timer
	if spec.Timeout > 0 {
		timer = time.AfterFunc(spec.Timeout, func() {
			r.mu.Lock()
			if r.terminal || r.exited {
				r.mu.Unlock()
				return
			}
			r.timedOut = true
			r.mu.Unlock()
			r.kill()
		})
	}
	go func() {
		err := cmd.Wait()
		if timer != nil {
			timer.Stop()
		}
		lines.flush()
		r.mu.Lock()
		r.exited = true
		stopAsked, timedOut, timeout := r.stopAsked, r.timedOut, r.timeout
		r.mu.Unlock()
		switch {
		case stopAsked:
			r.add(Event{Type: "stopped", Fields: map[string]any{}})
		case timedOut:
			r.add(Event{Type: "error", Fields: map[string]any{"message": fmt.Sprintf("timed out after %s", timeout)}})
		default:
			// Normally a no-op: the result line has already ended the run.
			msg := "claude exited without a result"
			if err != nil {
				msg = "claude: " + err.Error()
			}
			if tail := strings.TrimSpace(stderr.String()); tail != "" {
				msg += ": " + tail
			}
			r.add(Event{Type: "error", Fields: map[string]any{"message": msg}})
		}
	}()
	return nil
}

// lineWriter splits what it is written into lines and hands each over.
type lineWriter struct {
	mu  sync.Mutex
	buf []byte
	fn  func([]byte)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		if len(bytes.TrimSpace(line)) > 0 {
			w.fn(append([]byte(nil), line...))
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(bytes.TrimSpace(w.buf)) > 0 {
		w.fn(append([]byte(nil), w.buf...))
	}
	w.buf = nil
}

// tailBuffer keeps the first stderrKeep bytes of stderr: the first lines are
// where a harness says "not logged in" or "unknown option".
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if room := stderrKeep - len(t.buf); room > 0 {
		if len(p) > room {
			t.buf = append(t.buf, p[:room]...)
		} else {
			t.buf = append(t.buf, p...)
		}
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
