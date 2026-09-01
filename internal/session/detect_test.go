package session

import (
	"sync"
	"testing"
	"time"
)

// base is a fixed clock so the tests describe rules rather than race a wall
// clock.
var base = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return base.Add(d) }

// Silence means done only when nothing is running.
//
// This test used to assert that ten seconds of silence made a session done
// whatever was in it, which is what the code did and what made the panel
// announce that a thinking agent had finished. "Done" is a claim about
// completion; the only evidence for it is that the thing which was working has
// exited and the pane is back at a shell.
func TestQuietShellIsDone(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true}, at(0))
	st, _ := d.Evaluate("s", Observation{ShellOnly: true}, at(10*time.Second))
	if st != StateDone {
		t.Errorf("state after ten seconds of silence at a shell = %q, want %q", st, StateDone)
	}
}

// An agent that has gone quiet has not finished.
//
// Thinking, waiting on a slow tool call, or writing somewhere other than the
// screen all look identical from here — and all three used to be reported as
// done, with a green check, against a session in the middle of a task.
func TestQuietAgentIsStillWorking(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true}, at(0))
	st, src := d.Evaluate("s", Observation{ShellOnly: false}, at(30*time.Second))
	if st != StateWorking {
		t.Errorf("an agent silent for thirty seconds reads as %q, want %q; the process is still "+
			"there and nothing has finished", st, StateWorking)
	}
	if src != SourceHeuristic {
		t.Errorf("source = %q, want %q", src, SourceHeuristic)
	}
}

// And it must not flicker, which is what shipped: activityWindow and the
// poller's interval were both two seconds, so a session printing every couple
// of seconds landed on either side of the boundary at random.
func TestAnAgentDoesNotFlickerBetweenPolls(t *testing.T) {
	d := NewDetector()
	obs := Observation{ShellOnly: false}
	seen := map[State]bool{}
	// Output every two seconds, sampled every two seconds, offset by a little
	// each time — the pattern that produced the alternating dot.
	for i := 0; i < 10; i++ {
		tick := time.Duration(i) * 2 * time.Second
		d.Observe("s", Signals{Bytes: 40, Visible: true}, at(tick))
		st, _ := d.Evaluate("s", obs, at(tick+1900*time.Millisecond))
		seen[st] = true
		st2, _ := d.Evaluate("s", obs, at(tick+2100*time.Millisecond))
		seen[st2] = true
	}
	if len(seen) != 1 {
		t.Errorf("the state alternated across polls: %v", seen)
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
	//
	// Advanced, not merely Visible. An agent that resumed work prints, and
	// printing moves the screen forward; that is what distinguishes it from
	// one redrawing a spinner in place. This test used to pass with Visible
	// alone, and so did a session that had rung and was animating.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	d.Observe("s", Signals{Bytes: 500, Visible: true, Advanced: true}, at(5*time.Second))
	if st, _ := d.Evaluate("s", Observation{}, at(5*time.Second+100*time.Millisecond)); st != StateWorking {
		t.Errorf("state = %q, want %q once work resumed", st, StateWorking)
	}
}

func TestAnAnimationDoesNotClearTheBell(t *testing.T) {
	// The failure this was written for, measured against the real binary: a
	// pane that rings and then animates showed waiting at three seconds and
	// working from eight onwards, for as long as it kept animating. An agent
	// asking a question with a live "esc to interrupt" line under it read as
	// busy — on a phone, sorted below the sessions that were merely working,
	// with a circle instead of a triangle.
	//
	// bellGrace cannot fix it. The test was "has anything printed since the
	// ring", and something always has: 480 bytes over three seconds of steady
	// state, none of them a line feed.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	for i := 1; i <= 100; i++ {
		// '\r|' over the same line, once every 200ms, for twenty seconds.
		d.Observe("s", Signals{Bytes: 30, Visible: true}, at(time.Duration(i)*200*time.Millisecond))
	}
	if st, _ := d.Evaluate("s", Observation{}, at(21*time.Second)); st != StateWaiting {
		t.Errorf("state = %q after twenty seconds of animation, want %q; "+
			"the one signal this panel exists for was erased by a spinner", st, StateWaiting)
	}
}

func TestAFullScreenRepaintDoesNotClearTheBell(t *testing.T) {
	// The other shape of the same thing: a TUI rewriting cells where they
	// stand. 105 bytes over three seconds, no line feeds, no carriage returns.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	for i := 1; i <= 50; i++ {
		d.Observe("s", Signals{Bytes: 12, Visible: true}, at(time.Duration(i)*200*time.Millisecond))
	}
	if st, _ := d.Evaluate("s", Observation{}, at(11*time.Second)); st != StateWaiting {
		t.Errorf("state = %q while a waiting agent repainted its screen, want %q", st, StateWaiting)
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
	//
	// Advanced, and that is the whole content of the rule rather than a detail
	// of the fixture: the grace is measured against a screen that moved forward,
	// so that a spinner cannot end it. Without the flag this fixture is a
	// redrawing TUI, which is TestAnAnimationDoesNotDiscardAHookReport and wants
	// the opposite answer. The two together are the rule.
	d := NewDetector()
	d.Report("s", StateDone, at(0))
	d.Observe("s", Signals{Bytes: 400, Visible: true, Advanced: true}, at(hookGrace+time.Second))
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
	// describes it — output that moved the screen forward, which is what
	// separates this from TestAnAnimationDoesNotClearAManualOverride below.
	d.Observe("s", Signals{Bytes: 300, Visible: true, Advanced: true}, at(3*time.Second))
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
	// Observation{} would mean "something that is not a shell is running",
	// which is not what "nothing has been observed" means. In the panel the
	// poller derives this from #{pane_current_command}, and an empty command
	// is a shell.
	d := NewDetector()
	st, _ := d.Evaluate("never-seen", Observation{ShellOnly: true}, at(0))
	if st != StateDone {
		t.Errorf("state = %q, want %q", st, StateDone)
	}
}

func TestForget(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	d.Forget("s")
	if st, _ := d.Evaluate("s", Observation{ShellOnly: true}, at(100*time.Millisecond)); st != StateDone {
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

// Every entry point on the detector, at once.
//
// The poller evaluates every session on a timer, the pump reports signals from
// its own goroutine, hooks arrive over HTTP, the user clicks a dot, and
// sessions are forgotten and retained as they come and go — all against the
// same maps. The mutex looks right; this is the cheap way to find out.
func TestConcurrentUseOfTheDetector(t *testing.T) {
	d := NewDetector()
	ids := []string{"a", "b", "c", "d"}

	stop := make(chan struct{})
	time.AfterFunc(700*time.Millisecond, func() { close(stop) })
	var wg sync.WaitGroup

	work := func(f func(id string, n int)) {
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					f(ids[n%len(ids)], n)
				}
			}(i)
		}
	}

	now := time.Now()
	work(func(id string, n int) {
		d.Observe(id, Signals{Bytes: 10, Visible: true, Bell: n%3 == 0}, now)
	})
	work(func(id string, _ int) { d.Evaluate(id, Observation{ShellOnly: false}, time.Now()) })
	work(func(id string, n int) {
		if n%2 == 0 {
			d.Report(id, StateWaiting, time.Now())
		} else {
			d.SetManual(id, StateDone, time.Now())
		}
	})
	work(func(id string, n int) {
		if n%2 == 0 {
			d.Forget(id)
		} else {
			d.Retain(ids[:1+n%len(ids)])
		}
		_ = d.Tracked()
	})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("detector callers did not finish; something is deadlocked")
	}
}

func TestABellInAPlainShellIsNotAnAgentAskingForYou(t *testing.T) {
	// Pressing TAB on an ambiguous completion is how readline says "I have
	// nothing for you": it rings the bell and prints nothing at all. The bell
	// character is not visible output, so it does not move lastOutput, and the
	// only thing that clears a bell is visible output arriving more than
	// bellGrace after it. Nothing was ever going to print.
	//
	// So a scratch shell where somebody hit TAB sat on an orange triangle
	// indefinitely, claiming an agent needed a human — and waiting sorts to the
	// top, so it sat there *above* the sessions that really did. This file
	// already says why that is the expensive kind of wrong: "a panel that cries
	// for attention it does not need is one people stop looking at."
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 30, Visible: true}, at(0)) // the prompt
	d.Observe("s", Signals{Bell: true}, at(time.Second))     // TAB, and a beep
	st, _ := d.Evaluate("s", Observation{ShellOnly: true}, at(time.Minute))
	if st == StateWaiting {
		t.Errorf("a bare shell that beeped reports %q a minute later; "+
			"nothing is waiting for anybody", st)
	}
}

func TestABellStillMeansWaitingWhenSomethingIsRunning(t *testing.T) {
	// The converse, so the fix above cannot be widened by accident. A pane
	// whose foreground process is not a shell has an agent in it, and a bell
	// there is the one unambiguous "a human is needed" signal the panel has
	// without hooks.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 30, Visible: true}, at(0))
	d.Observe("s", Signals{Bell: true}, at(time.Second))
	if st, _ := d.Evaluate("s", Observation{}, at(time.Minute)); st != StateWaiting {
		t.Errorf("state = %q, want %q", st, StateWaiting)
	}
}

func TestRestoreBringsBackWhatCannotBeRederived(t *testing.T) {
	// The detector keeps its evidence in memory. A restart threw it away and
	// the poller re-derived every session from live facts, which for anything
	// that is not a shell means "working". Measured against the real binary:
	// an agent sitting on "Do you want to proceed? (y/n)" showed waiting,
	// the backend was restarted, and it showed working from then on — with the
	// question still on its screen.
	for _, tc := range []struct {
		name string
		st   State
		src  Source
		want State
		src2 Source
	}{
		{"a bell that rang before the restart", StateWaiting, SourceHeuristic, StateWaiting, SourceHeuristic},
		{"what a hook last said", StateWaiting, SourceHook, StateWaiting, SourceHook},
		{"what the user chose", StateDone, SourceManual, StateDone, SourceManual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDetector()
			d.Restore("s", tc.st, tc.src, at(0))
			st, src := d.Evaluate("s", Observation{}, at(time.Second))
			if st != tc.want || src != tc.src2 {
				t.Errorf("state = %q from %q, want %q from %q", st, src, tc.want, tc.src2)
			}
		})
	}
}

func TestRestoreDoesNotResurrectWorking(t *testing.T) {
	// Working and done come from the pane's foreground process, which is still
	// true after a restart and is better read fresh. Restoring them would pin
	// a stale answer over a live one.
	d := NewDetector()
	d.Restore("s", StateWorking, SourceHeuristic, at(0))
	if st, _ := d.Evaluate("s", Observation{ShellOnly: true}, at(time.Second)); st != StateDone {
		t.Errorf("state = %q, want the live reading %q", st, StateDone)
	}
}

func TestRestoredWaitingStillClearsWhenWorkResumes(t *testing.T) {
	// A session answered at the tmux socket while the panel was down must
	// correct itself, not sit on a triangle forever.
	d := NewDetector()
	d.Restore("s", StateWaiting, SourceHeuristic, at(0))
	d.Observe("s", Signals{Bytes: 200, Visible: true, Advanced: true}, at(10*time.Second))
	if st, _ := d.Evaluate("s", Observation{}, at(11*time.Second)); st != StateWorking {
		t.Errorf("state = %q, want %q once the screen advanced again", st, StateWorking)
	}
}

func TestRestoreIgnoresNonsense(t *testing.T) {
	// A row read back from the database is not necessarily one this build
	// wrote. An older panel, a hand-edited database, a state that has since
	// been removed from the enum — any of them reaches Restore, and a manual
	// source pins whatever it is given until the session does something new.
	//
	// An unknown state renders as nothing at all: no dot, no shape, no colour.
	// A session that is asking for a human, showing as blank, is the worst
	// outcome available here.
	d := NewDetector()
	d.Restore("s", State("banana"), SourceManual, at(0))
	st, _ := d.Evaluate("s", Observation{}, at(time.Second))
	if !st.Valid() {
		t.Errorf("state = %q, which is not one this panel can draw", st)
	}
	// And a row with no timestamp says nothing about when anything happened.
	d2 := NewDetector()
	d2.Restore("s", StateWaiting, SourceManual, time.Time{})
	if st, src := d2.Evaluate("s", Observation{}, at(time.Second)); src == SourceManual {
		t.Errorf("a row with no timestamp was treated as a choice the user made (%q)", st)
	}
}

func TestAnAnimationDoesNotDiscardAHookReport(t *testing.T) {
	// The same failure TestAnAnimationDoesNotClearTheBell was written for, one
	// rule up. hookGrace is three seconds and the measurement in that test is
	// 480 bytes of spinner in three seconds with no line feed in any of them,
	// so "has anything printed since the report" is always yes and the report
	// is discarded — leaving the fall-through to read the foreground process
	// and answer working.
	//
	// That is the panel's precise source being overridden by its guess, in
	// exactly the case the precise source exists for: an agent that asked a
	// question and is now animating an "esc to interrupt" line under it.
	d := NewDetector()
	d.Report("s", StateWaiting, at(0))
	for i := 1; i <= 100; i++ {
		// '\r|' over the same line, once every 200ms, for twenty seconds.
		d.Observe("s", Signals{Bytes: 30, Visible: true}, at(time.Duration(i)*200*time.Millisecond))
	}
	st, src := d.Evaluate("s", Observation{}, at(21*time.Second))
	if st != StateWaiting || src != SourceHook {
		t.Errorf("state = %q from %q after twenty seconds of animation, want %q from %q; "+
			"the agent said it was waiting and a spinner overruled it", st, src, StateWaiting, SourceHook)
	}
}

func TestAnAnimationDoesNotClearAManualOverride(t *testing.T) {
	// Somebody overrides the state precisely when the automatic one is wrong,
	// and the automatic one is most often wrong while a TUI is animating. So
	// the next chunk arrives in milliseconds, the override is dropped, and the
	// click reads as having done nothing.
	d := NewDetector()
	d.SetManual("s", StateWaiting, at(0))
	for i := 1; i <= 100; i++ {
		d.Observe("s", Signals{Bytes: 30, Visible: true}, at(time.Duration(i)*200*time.Millisecond))
	}
	st, src := d.Evaluate("s", Observation{}, at(21*time.Second))
	if st != StateWaiting || src != SourceManual {
		t.Errorf("state = %q from %q, want %q from %q; a redrawing screen is not "+
			"the session doing something new", st, src, StateWaiting, SourceManual)
	}
}

// What a session was started as counts, not only what is in the pane now.
//
// The panel starts every command through a login shell so that a version
// manager's shims are on PATH. For the moment between that shell starting and
// its `exec`, `pane_current_command` is the shell -- and a poll landing there
// used to conclude the session was a scratch terminal, which turns off the
// "states are being guessed" notice and classifies a working agent as idle.
func TestASessionKnowsItIsRunningAnAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		launch  []string
		current string
		want    bool
	}{
		{"claude, seen in the pane", []string{"claude"}, "claude", true},
		{"claude, mid-exec", []string{"claude"}, "bash", true},
		{"an absolute path", []string{"/home/x/.local/bin/claude"}, "bash", true},
		{"with arguments", []string{"codex", "--model", "gpt"}, "sh", true},
		{"opencode", []string{"opencode"}, "bash", true},

		// A plain shell that has an agent running inside it. The argv cannot
		// see this and the pane can.
		{"an agent started by hand", nil, "claude", true},

		{"a scratch terminal", nil, "bash", false},
		{"a shell profile", []string{"bash"}, "bash", false},
		{"htop", []string{"htop"}, "htop", false},

		// The program, not its arguments: a flag that mentions an agent is not
		// the agent being run.
		{"a flag that says claude", []string{"python", "--model", "claude-opus-5"}, "python", false},
	} {
		if got := SessionRunsAnAgent(tc.launch, tc.current); got != tc.want {
			t.Errorf("%s: SessionRunsAnAgent(%v, %q) = %v, want %v",
				tc.name, tc.launch, tc.current, got, tc.want)
		}
	}
}

func TestABellAfterAManualOverrideIsHeard(t *testing.T) {
	// The click is sticky, not permanent. Advanced needs a line feed that was
	// not a repaint, so an agent sitting in its TUI never advances the screen,
	// and nothing but a hook report could expire the override -- while an agent
	// with hooks installed is exactly the one that does not need the bell. A
	// session somebody marked done therefore rang for a human all afternoon
	// while the panel went on answering done, which sorts to the bottom.
	d := NewDetector()
	d.Observe("s", Signals{Bytes: 10, Visible: true, Bell: true}, at(0))
	d.SetManual("s", StateDone, at(time.Second))
	for i := 1; i <= 300; i++ {
		d.Observe("s", Signals{Bytes: 30, Visible: true}, at(time.Duration(i)*time.Second))
	}
	if st, src := d.Evaluate("s", Observation{}, at(305*time.Second)); st != StateDone || src != SourceManual {
		t.Fatalf("state = %q from %q before the new bell, want %q from %q",
			st, src, StateDone, SourceManual)
	}

	d.Observe("s", Signals{Bell: true}, at(600*time.Second))
	st, src := d.Evaluate("s", Observation{}, at(601*time.Second))
	if st != StateWaiting || src != SourceHeuristic {
		t.Errorf("state = %q from %q after a bell rung ten minutes past the click, "+
			"want %q from %q; the agent asked for a human and the panel withheld it",
			st, src, StateWaiting, SourceHeuristic)
	}
}

func TestATabCompletionBellDoesNotUndoAManualOverride(t *testing.T) {
	// The other side of that rule, and why it is not simply "any bell". In a
	// plain shell the bell is the line editor complaining about an ambiguous
	// TAB, which is why it never raises waiting either. Forgetting what a
	// person deliberately said because they pressed TAB would be worse than the
	// stale state the rule above is fixing.
	d := NewDetector()
	d.SetManual("s", StateWaiting, at(0))
	d.Observe("s", Signals{Bell: true}, at(time.Second))
	st, src := d.Evaluate("s", Observation{ShellOnly: true}, at(2*time.Second))
	if st != StateWaiting || src != SourceManual {
		t.Errorf("state = %q from %q, want %q from %q", st, src, StateWaiting, SourceManual)
	}
}
