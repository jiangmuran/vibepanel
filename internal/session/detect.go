package session

import (
	"sync"
	"time"
)

// Thresholds for the output heuristic.
//
// These are the numbers a human reads a sidebar with, not a control loop.
// Too short and a session flickers between working and done between spinner
// frames; too long and "it finished" arrives after you have already tabbed
// over to look.
const (
	// activityWindow — output this recently means the session is working.
	// Agent TUIs redraw a spinner while they think, so a thinking agent is
	// continuously producing output and reads as working without any help.
	activityWindow = 2 * time.Second

	// hookGrace — how long a hook report outranks subsequent output.
	//
	// A "waiting for you" notification is emitted at the same instant the
	// prompt is drawn, so the output that immediately follows it is the
	// prompt itself, not the agent resuming work. Without this the state
	// flips back to working a few milliseconds after the hook fired.
	hookGrace = 3 * time.Second
)

// Observation is what the detector has been told about one session.
type Observation struct {
	// Dead means the pane's process exited. tmux keeps it on screen.
	Dead bool
	// ShellOnly means the foreground process is a plain shell rather than an
	// agent, so there is nothing that could be "working".
	ShellOnly bool
}

// Detector decides each session's state from what the pump saw, what any hook
// reported, and what the user said.
//
// Kept separate from the manager and from tmux so that the rules can be tested
// as rules, at whatever times the test likes, rather than by running an agent
// and waiting.
type Detector struct {
	mu    sync.Mutex
	track map[string]*tracker
}

type tracker struct {
	lastOutput time.Time
	lastBell   time.Time

	hookState State
	hookAt    time.Time

	manualState State
	manualAt    time.Time
}

// NewDetector returns an empty detector.
func NewDetector() *Detector { return &Detector{track: map[string]*tracker{}} }

func (d *Detector) get(id string) *tracker {
	t, ok := d.track[id]
	if !ok {
		t = &tracker{}
		d.track[id] = t
	}
	return t
}

// Observe records what the PTY pump saw. Called on the pump goroutine.
func (d *Detector) Observe(id string, sig Signals, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.get(id)
	// Visible, not Bytes: a chunk of pure mode changes is the terminal being
	// reconfigured, not the session doing something.
	if sig.Visible {
		t.lastOutput = now
	}
	if sig.Bell {
		t.lastBell = now
	}
}

// Report records a state a hook declared. Hooks are the only precise source:
// the agent itself saying what it is doing.
func (d *Detector) Report(id string, st State, now time.Time) {
	if !st.Valid() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.get(id)
	t.hookState, t.hookAt = st, now
	// A hook report is fresh evidence, so an older manual override no longer
	// describes the situation.
	t.manualState, t.manualAt = "", time.Time{}
}

// SetManual records a state the user chose by clicking the indicator.
func (d *Detector) SetManual(id string, st State, now time.Time) {
	if !st.Valid() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.get(id)
	t.manualState, t.manualAt = st, now
}

// Forget drops a session's history.
func (d *Detector) Forget(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.track, id)
}

// Evaluate returns the session's state now, and what decided it.
//
// The ordering is by recency of evidence rather than a fixed ranking of
// sources. A stale declaration should not outrank what the terminal is
// visibly doing, and a fresh declaration should not be overridden by output it
// predicted. So: whichever of {user said, agent said, terminal did} happened
// last wins, with hooks given a short lead over the output they cause.
func (d *Detector) Evaluate(id string, obs Observation, now time.Time) (State, Source) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.get(id)

	// A dead pane is finished by definition; nothing else can be true of it.
	if obs.Dead {
		return StateDone, SourceHeuristic
	}

	// The user's own answer stands until the session does something new.
	if t.manualState != "" && !t.lastOutput.After(t.manualAt) {
		return t.manualState, SourceManual
	}

	// A hook report stands until output arrives well after it. "Well after"
	// because the notification and the prompt it announces are simultaneous.
	if t.hookState != "" && t.lastOutput.Sub(t.hookAt) < hookGrace {
		return t.hookState, SourceHook
	}

	// A bell is the only unambiguous "a human is needed" signal available
	// without hooks, so it outranks activity: an agent that rang and is now
	// redrawing its prompt is still waiting.
	if !t.lastBell.IsZero() && !t.lastOutput.After(t.lastBell.Add(activityWindow)) {
		return StateWaiting, SourceHeuristic
	}

	if now.Sub(t.lastOutput) < activityWindow && !t.lastOutput.IsZero() {
		// A plain shell echoing keystrokes is not "working" in any sense the
		// sidebar should shout about.
		if obs.ShellOnly {
			return StateDone, SourceHeuristic
		}
		return StateWorking, SourceHeuristic
	}

	return StateDone, SourceHeuristic
}

// IsShellCommand reports whether a pane's foreground process is a plain shell.
//
// Lives here rather than in the tmux package because it is a judgement about
// what the state machine should conclude, not a fact about tmux.
func IsShellCommand(cmd string) bool {
	switch cmd {
	case "bash", "sh", "zsh", "fish", "dash", "ksh", "tcsh", "csh", "tmux", "":
		return true
	}
	return false
}
