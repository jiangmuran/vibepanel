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
	// lastAdvance — when the screen last moved forward, as opposed to being
	// redrawn where it stood. See the bell rule in Evaluate.
	lastAdvance time.Time
	lastBell    time.Time

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
	if sig.Advanced {
		t.lastAdvance = now
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

// Restore seeds a session from what was written down before the panel stopped.
//
// The detector keeps its evidence in memory: when a bell last rang, what a
// hook last said, what the user last chose. A restart threw all of it away,
// and the poller then re-derived every session from live facts alone — which
// for anything that is not a shell means "working".
//
// Measured against the real binary: an agent sitting on "Do you want to
// proceed? (y/n)" showed waiting, `systemctl restart vibepanel` was issued,
// and it showed working from then on. The session was untouched, the question
// was still on its screen, and the panel had stopped saying so. Restarting the
// backend is the operation this whole architecture exists to make safe, and it
// silently destroyed the one state the panel is for — for every waiting
// session at once.
//
// Only what cannot be re-derived is restored. "Working" and "done" come from
// the pane's foreground process, which is still true after a restart and is
// better read fresh. A bell, a hook report and a manual choice are events that
// happened, and nothing on the wire will say they did a second time.
//
// Stale evidence corrects itself: a session that really did go back to work
// while the panel was down advances its screen within moments of being
// re-attached, and that clears the bell exactly as it would have.
func (d *Detector) Restore(id string, st State, src Source, at time.Time) {
	if !st.Valid() || at.IsZero() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.get(id)
	switch src {
	case SourceHook:
		t.hookState, t.hookAt = st, at
	case SourceManual:
		t.manualState, t.manualAt = st, at
	case SourceHeuristic:
		// The only heuristic state that rests on an event rather than on what
		// is running right now.
		if st == StateWaiting {
			t.lastBell = at
		}
	}
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
	//
	// KNOWN GAP, one of two: "something new" is lastOutput, and a spinner is
	// output. `Advanced` was introduced to separate a screen that moved
	// forward from one redrawn where it stood — the measurement is three
	// paragraphs down, a redrawing TUI at 480 bytes and zero line feeds in
	// three seconds — and only the bell rule was changed to use it. This rule
	// and the hook rule directly below both still read lastOutput.
	// lastAdvance's own field comment names the bell rule as its only reader,
	// which is the shape of the omission.
	//
	// It fails in the case the feature exists for. Somebody overrides the state
	// precisely when the automatic one is wrong, and the automatic one is most
	// often wrong while a TUI is animating; so the next chunk arrives in
	// milliseconds, lastOutput passes manualAt, and the dot snaps back. The
	// click reads as having done nothing.
	//
	// The fix is `t.lastAdvance` in place of `t.lastOutput` here. It is a
	// judgement rather than an obvious correction — it makes a manual state
	// stickier, and a stale manual state is its own hazard — but the argument
	// the bell rule makes applies unchanged: a screen being redrawn where it
	// stands is not the session doing something.
	//
	// Found by reading, in a stretch where nothing could be run. Whoever
	// changes it should drive a pane that rings and then animates, the way the
	// measurement below was taken, rather than trusting this paragraph.
	if t.manualState != "" && !t.lastOutput.After(t.manualAt) {
		return t.manualState, SourceManual
	}

	// A hook report stands until output arrives well after it. "Well after"
	// because the notification and the prompt it announces are simultaneous.
	//
	// KNOWN GAP, the second of two, and the worse one. See the manual rule
	// above: `Advanced` was introduced to separate a screen that moved forward
	// from one being redrawn where it stood, and only the bell rule below was
	// changed to use it.
	//
	// The paragraph in the bell rule applies here word for word — "an agent
	// whose TUI keeps moving defeats any finite grace, because the test was
	// 'has anything printed since', and something always has". hookGrace is
	// three seconds and the same measurement says a spinner emits 480 bytes in
	// three. So a hook that reported "waiting for you" is discarded, and the
	// fall-through reads the foreground process and says working.
	//
	// That is the panel's precise source being overridden by its guess, in the
	// case the precise source exists for. The README's claim is that the bell
	// and a hook report "are separate and outrank it"; this rule makes that
	// true for three seconds.
	//
	// Same fix as above — `t.lastAdvance` in place of `t.lastOutput` — and it
	// is less of a judgement call here than there: hookGrace's own comment
	// says the grace exists to cover "the prompt itself, not the agent
	// resuming work", and a line feed is exactly the difference between those
	// two, measured on the wire rather than timed.
	//
	// The "state is guessed" notice does not cover for this, which is worth
	// knowing before deciding the severity. stateIsGuessed returns false as
	// soon as *any* session's state came from a hook — deliberately, because
	// the script is installed globally or not at all. So on a panel with more
	// than one session, one of them holding a fresh hook report suppresses the
	// notice panel-wide while this one is being misreported. Running many
	// sessions at once is the entire premise, so the mitigation is absent
	// exactly where it would be needed.
	//
	// Not fixed, and not to be trusted from this paragraph: it needs a real
	// agent that reports through a hook and then animates while it waits,
	// which is the thing to reproduce before changing anything.
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
	// Cleared by output that moved the screen forward, not by any output at
	// all. An agent that rang and is animating is still waiting.
	//
	// bellGrace was meant to cover the redraw that follows the ring, and it
	// does — for two seconds. An agent whose TUI keeps moving defeats any
	// finite grace, because the test was "has anything printed since", and
	// something always has. Measured against the real binary, with a pane that
	// rings and then animates: waiting at three seconds, working from eight
	// onwards, and working for as long as it kept going. The one signal this
	// panel exists to surface, erased by a spinner.
	//
	// A line feed is the difference, and it is clean on the wire. Three seconds
	// of steady state, read off the PTY the panel actually sees:
	//
	//   spinner  480 bytes  LF=0     '\r|' over the same line
	//   lines    430 bytes  LF=22    the agent producing output
	//   box      105 bytes  LF=0     a full-screen repaint, cursor-addressed
	//
	// So "the screen advanced" separates an agent that went back to work from
	// one that is redrawing while it waits, and neither case needs a timer to
	// guess with.
	if !obs.ShellOnly && !t.lastBell.IsZero() && !t.lastAdvance.After(t.lastBell.Add(bellGrace)) {
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
