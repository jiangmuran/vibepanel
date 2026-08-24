package session

import (
	"testing"
	"time"
)

// base is a fixed clock so the tests describe rules rather than race a wall
// clock.
var base = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return base.Add(d) }

func TestQuietSessionIsDone(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true}, at(0))
	if st, _ := d.Evaluate("s", Observation{}, at(10*time.Second)); st != StateDone {
		t.Errorf("state after ten seconds of silence = %q, want %q", st, StateDone)
	}
}

func TestRecentOutputIsWorking(t *testing.T) {
	// Agent TUIs redraw a spinner while thinking, so a thinking agent is
	// continuously producing output and needs no other signal.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 120, Visible: true}, at(0))
	st, src := d.Evaluate("s", Observation{}, at(500*time.Millisecond))
	if st != StateWorking {
		t.Errorf("state = %q, want %q", st, StateWorking)
	}
	if src != SourceHeuristic {
		t.Errorf("source = %q, want %q", src, SourceHeuristic)
	}
}

func TestShellEchoIsNotWorking(t *testing.T) {
	// A shell echoing what you type is not a task in progress, and marking it
	// so would put a "working" dot on every terminal you touch.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 3, Visible: true}, at(0))
	if st, _ := d.Evaluate("s", Observation{ShellOnly: true}, at(200*time.Millisecond)); st != StateDone {
		t.Errorf("state = %q, want %q for a shell", st, StateDone)
	}
}

func TestBellMeansWaiting(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 40, Visible: true, Bell: true}, at(0))
	st, src := d.Evaluate("s", Observation{}, at(time.Second))
	if st != StateWaiting {
		t.Errorf("state = %q, want %q", st, StateWaiting)
	}
	if src != SourceHeuristic {
		t.Errorf("source = %q", src)
	}
}

func TestBellSurvivesThePromptItAnnounces(t *testing.T) {
	// The bell and the prompt redraw arrive together. If output immediately
	// after a bell cleared it, every "needs your approval" would show as
	// "working" a few milliseconds later — the single most important state in
	// this panel, wrong every time.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	d.Observe("s", Signals{Bytes: 200, Visible: true}, at(50*time.Millisecond))
	if st, _ := d.Evaluate("s", Observation{}, at(300*time.Millisecond)); st != StateWaiting {
		t.Errorf("state = %q, want the bell to still hold at %q", st, StateWaiting)
	}
}

func TestWorkResumingClearsTheBell(t *testing.T) {
	// But a bell must not latch forever: once the agent is clearly working
	// again, the session is no longer asking for anything.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	d.Observe("s", Signals{Bytes: 500, Visible: true}, at(5*time.Second))
	if st, _ := d.Evaluate("s", Observation{}, at(5*time.Second+100*time.Millisecond)); st != StateWorking {
		t.Errorf("state = %q, want %q once work resumed", st, StateWorking)
	}
}

func TestHookOutranksTheHeuristic(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 100, Visible: true}, at(0)) // would read as working
	d.Report("s", StateWaiting, at(10*time.Millisecond))
	st, src := d.Evaluate("s", Observation{}, at(200*time.Millisecond))
	if st != StateWaiting || src != SourceHook {
		t.Errorf("state = %q from %q, want %q from %q", st, src, StateWaiting, SourceHook)
	}
}

func TestHookHoldsThroughTheOutputItPredicts(t *testing.T) {
	// A Notification hook fires as the prompt is drawn, so output follows it
	// immediately. That output is the prompt, not the agent resuming.
	d := NewDetector()
	d.Report("s", StateWaiting, at(0))
	d.Observe("s", Signals{Bytes: 400, Visible: true}, at(100*time.Millisecond))
	if st, _ := d.Evaluate("s", Observation{}, at(500*time.Millisecond)); st != StateWaiting {
		t.Errorf("state = %q, want the hook to hold at %q", st, StateWaiting)
	}
}

func TestOutputWellAfterAHookWins(t *testing.T) {
	// The hook's lead is a grace period, not a lock. An agent that was told to
	// carry on is working again, whatever it last announced.
	d := NewDetector()
	d.Report("s", StateDone, at(0))
	d.Observe("s", Signals{Bytes: 400, Visible: true}, at(hookGrace+time.Second))
	st, src := d.Evaluate("s", Observation{}, at(hookGrace+time.Second+100*time.Millisecond))
	if st != StateWorking || src != SourceHeuristic {
		t.Errorf("state = %q from %q, want %q from %q", st, src, StateWorking, SourceHeuristic)
	}
}

func TestManualOverrideSticksUntilActivity(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	d.SetManual("s", StateDone, at(time.Second))

	// Marking something done must keep it down the list; bouncing back on the
	// next poll is what makes such a control useless.
	st, src := d.Evaluate("s", Observation{}, at(2*time.Second))
	if st != StateDone || src != SourceManual {
		t.Fatalf("state = %q from %q, want %q from %q", st, src, StateDone, SourceManual)
	}

	// New output means the situation changed, so the override no longer
	// describes it.
	d.Observe("s", Signals{Bytes: 300, Visible: true}, at(3*time.Second))
	if st, src = d.Evaluate("s", Observation{}, at(3*time.Second+100*time.Millisecond)); src == SourceManual {
		t.Errorf("manual override survived new output: %q from %q", st, src)
	}
}

func TestHookClearsAManualOverride(t *testing.T) {
	// The agent reporting what it is doing is fresher evidence than a click
	// from before it said anything.
	d := NewDetector()
	d.SetManual("s", StateDone, at(0))
	d.Report("s", StateWaiting, at(time.Second))
	st, src := d.Evaluate("s", Observation{}, at(2*time.Second))
	if st != StateWaiting || src != SourceHook {
		t.Errorf("state = %q from %q, want %q from %q", st, src, StateWaiting, SourceHook)
	}
}

func TestDeadPaneIsDoneWhateverElseHappened(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	d.Report("s", StateWorking, at(time.Second))
	if st, _ := d.Evaluate("s", Observation{Dead: true}, at(2*time.Second)); st != StateDone {
		t.Errorf("state = %q for a dead pane, want %q", st, StateDone)
	}
}

func TestInvalidReportsAreIgnored(t *testing.T) {
	// Hook payloads are HTTP bodies shaped by whatever the user put in their
	// agent config.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 100, Visible: true}, at(0))
	d.Report("s", State("banana"), at(time.Second))
	if st, src := d.Evaluate("s", Observation{}, at(time.Second+10*time.Millisecond)); src == SourceHook {
		t.Errorf("an invalid hook report was accepted: %q from %q", st, src)
	}
}

func TestUnknownSessionIsDone(t *testing.T) {
	// A session nothing has been observed for should read as quiet, not as a
	// crash or a blank.
	d := NewDetector()
	st, _ := d.Evaluate("never-seen", Observation{}, at(0))
	if st != StateDone {
		t.Errorf("state = %q, want %q", st, StateDone)
	}
}

func TestForget(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	d.Forget("s")
	if st, _ := d.Evaluate("s", Observation{}, at(100*time.Millisecond)); st != StateDone {
		t.Errorf("state after Forget = %q, want %q", st, StateDone)
	}
}

func TestIsShellCommand(t *testing.T) {
	for _, c := range []string{"bash", "zsh", "fish", "sh", ""} {
		if !IsShellCommand(c) {
			t.Errorf("%q should be a shell", c)
		}
	}
	for _, c := range []string{"claude", "codex", "htop", "vim", "node"} {
		if IsShellCommand(c) {
			t.Errorf("%q should not be a shell", c)
		}
	}
}
