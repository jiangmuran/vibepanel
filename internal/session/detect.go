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
	// activityWindow — output this recently counts as activity.
	//
	// Comfortably longer than the poller's interval, deliberately. When the two
	// were both two seconds the question "was there output in the last window"
	// was being sampled at exactly the rate the window was wide, so a session
	// printing every couple of seconds landed on either side of the boundary at
	// random and its dot alternated. A window that is not a multiple of the
	// sampling rate stops the aliasing.
	//
	// This used to be the whole of the working/done decision, on the reasoning
	// that agent TUIs redraw a spinner while they think and so are never quiet.
	// That holds for an agent with an animation and fails for one waiting on a
	// slow tool call, or one whose output is piped somewhere. See Evaluate.
	activityWindow = 5 * time.Second

	// bellGrace — how long a bell keeps meaning "a human is needed" once
	// output starts again.
	//
	// Its own constant, not activityWindow. The two were shared, which read as
	// tidy and was an accident: one answers "has this printed lately", the
	// other "is that ring still the most recent thing that mattered". Widening
	// the first for sampling reasons silently widened the second, which is how
	// a session that had resumed work went on showing a triangle.
	bellGrace = 2 * time.Second

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

// Retain drops history for every session not in the given set.
//
// Cleanup driven by the authoritative list rather than by each deletion path
// remembering to call Forget. Forget is still called where a session is
// removed, because immediate is better than eventual — but it cannot be the
// only mechanism: the poller rebuilds a tracker for every row it sees, so a
// delete that races a poll leaves one behind however careful the handler is.
// This is also the only thing that cleans up after a session that vanished
// without going through a handler at all.
func (d *Detector) Retain(ids []string) {
	keep := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		keep[id] = struct{}{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.track {
		if _, ok := keep[id]; !ok {
			delete(d.track, id)
		}
	}
}

// Tracked reports how many sessions the detector is holding history for.
//
// Exists so that forgetting can be asserted. Every path that removes a session
// is supposed to call Forget, and one that does not leaks a tracker for the
// life of the process — a small leak, but the kind nothing notices, because
// until now there was no way to observe it from outside this package. Cleanup
// that cannot be seen is cleanup that quietly stops happening.
func (d *Detector) Tracked() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.track)
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
	//
	// Except from a plain shell, where there is no agent to be waiting for
	// anything and the bell is the line editor complaining. Pressing TAB on an
	// ambiguous completion rings it and prints nothing — and the bell
	// character is not visible output, so it does not move lastOutput, and the
	// only thing that ever clears a bell is visible output arriving more than
	// bellGrace after it. Nothing was going to print. A scratch terminal where
	// somebody hit TAB therefore sat on an orange triangle indefinitely, and
	// waiting sorts to the top, so it sat there above the sessions that really
	// were asking for a human. See below for why that is the expensive
	// direction to be wrong in.
	if !obs.ShellOnly && !t.lastBell.IsZero() && !t.lastOutput.After(t.lastBell.Add(bellGrace)) {
		return StateWaiting, SourceHeuristic
	}

	// What is actually running decides, not whether it happened to print.
	//
	// Silence used to mean done. So an agent thinking for four seconds, or
	// waiting on a slow tool call, or writing to a file instead of the screen,
	// was announced as finished — a green check against a session that was
	// mid-task. "Done" is a claim about completion, and the panel had no
	// evidence for it; all it knew was that nothing had printed lately.
	//
	// The evidence it does have is the foreground process. A pane running
	// `claude` has not finished anything: the agent is still there. A pane back
	// at a shell has, because the thing that was working exited. That is the
	// difference between the two, and it is the one tmux can answer.
	//
	// Not "waiting", though the temptation is real. An agent that is quiet is
	// either thinking or asking, and nothing here can tell which — guessing
	// "asking" would put a triangle on every session that paused, and a panel
	// that cries for attention it does not need is one people stop looking at.
	// Saying "working" is true of both, and the two signals that genuinely mean
	// a human is needed — the bell, and a hook report — are checked above.
	if !obs.ShellOnly {
		return StateWorking, SourceHeuristic
	}

	// A plain shell: whether it echoed a keystroke a moment ago changes
	// nothing worth showing.
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
