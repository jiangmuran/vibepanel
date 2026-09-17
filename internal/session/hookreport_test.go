package session

import (
	"testing"
	"time"
)

// Each sequence here is one a real Claude Code 2.1.273 produced, or one a review
// of the first version of these rules found, with the timings involved. The
// reports are what hooks.Read makes of the documents; this file is about what
// the detector does with them.

func stateOf(t *testing.T, d *Detector, when time.Duration) (State, Source) {
	t.Helper()
	return d.Evaluate("s", Observation{}, at(when))
}

// PreToolUse at .913 and PermissionRequest at .932 for the same AskUserQuestion.
// Delivered the other way round, the dialog on screen read working.
func TestAWorkingReportJustAfterAPromptWasSentBeforeIt(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting}, at(0))
	if d.Hook("s", HookReport{State: StateWorking}, at(20*time.Millisecond)) {
		t.Fatal("the late PreToolUse was taken")
	}
	if st, _ := stateOf(t, d, time.Second); st != StateWaiting {
		t.Fatalf("state %q with the dialog on screen, want waiting", st)
	}
	// A second on, it is an answer.
	if !d.Hook("s", HookReport{State: StateWorking}, at(lateReport)) {
		t.Fatal("a working report a whole window later was refused")
	}
}

// Seven background agents calling tools while the main thread shows a question:
// each call reported working over the one state that needed a person.
func TestASubagentsProgressDoesNotAnswerAnotherAgentsPrompt(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting}, at(0))
	if d.Hook("s", HookReport{State: StateWorking, Agent: "a1"}, at(5*time.Second)) {
		t.Fatal("a subagent's working was taken over the main thread's question")
	}
	if st, _ := stateOf(t, d, 6*time.Second); st != StateWaiting {
		t.Fatalf("state %q, want waiting", st)
	}

	d.Hook("s", HookReport{State: StateWaiting, Agent: "a2"}, at(20*time.Second))
	if d.Hook("s", HookReport{State: StateWorking, Agent: "a3"}, at(25*time.Second)) {
		t.Fatal("a3 answered a2's prompt")
	}
	if !d.Hook("s", HookReport{State: StateWorking, Agent: "a2"}, at(30*time.Second)) {
		t.Fatal("a2's own PostToolUse was refused")
	}
}

// A subagent's prompt denied with feedback never reports again. Refusing the
// main thread over it held a whole turn at waiting -- UserPromptSubmit included.
func TestTheMainThreadIsNeverRefusedOverASubagentsPrompt(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting, Agent: "x"}, at(0))
	if !d.Hook("s", HookReport{State: StateWorking}, at(10*time.Second)) {
		t.Fatal("the main thread's report was refused over a subagent's prompt")
	}
	if st, _ := stateOf(t, d, 11*time.Second); st != StateWorking {
		t.Fatalf("state %q, want working", st)
	}
}

// Approving sends nothing: PostToolUse fires when the tool ends. The keystroke
// on the menu is the approval, and Claude's menu takes `1` without Enter.
func TestAKeystrokeAnswersAPermissionMenu(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting, Answerable: true}, at(0))
	d.Key("s", at(4*time.Second))
	if st, _ := stateOf(t, d, 5*time.Second); st != StateWorking {
		t.Fatalf("state %q after the menu was answered, want working", st)
	}
	// And the next prompt stands as always.
	d.Hook("s", HookReport{State: StateWaiting, Answerable: true}, at(10*time.Second))
	if st, src := stateOf(t, d, 11*time.Second); st != StateWaiting || src != SourceHook {
		t.Fatalf("state %q from %q, want the new prompt", st, src)
	}
}

// A line is a key.
func TestALineAnswersAPermissionMenu(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting, Answerable: true}, at(0))
	d.Input("s", at(4*time.Second))
	if st, _ := stateOf(t, d, 5*time.Second); st != StateWorking {
		t.Fatalf("state %q, want working", st)
	}
}

// A dialog of several questions takes a keystroke each; the first did not
// answer the rest. PostToolUse arrives the moment the last one is. The same
// holds for a failed turn, where Enter on an empty prompt answers nothing.
func TestAKeystrokeDoesNotAnswerAQuestionDialog(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting}, at(0))
	d.Key("s", at(4*time.Second))
	d.Input("s", at(5*time.Second))
	if st, _ := stateOf(t, d, 6*time.Second); st != StateWaiting {
		t.Fatalf("state %q with the second question on screen, want waiting", st)
	}
}

// PermissionRequest, then six seconds later a Notification for the same prompt
// that cannot say which tool it is about. It keeps what the request said.
func TestTheNotificationForAPromptKeepsWhatTheRequestSaid(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting, Answerable: true}, at(0))
	d.Hook("s", HookReport{State: StateWaiting}, at(6*time.Second))
	d.Key("s", at(8*time.Second))
	if st, _ := stateOf(t, d, 9*time.Second); st != StateWorking {
		t.Fatalf("state %q: the notification lost that the request was a menu", st)
	}

	// Not across agents: the main thread's question does not inherit a1's menu.
	d = NewDetector()
	d.Hook("s", HookReport{State: StateWaiting, Answerable: true, Agent: "a1"}, at(0))
	d.Hook("s", HookReport{State: StateWaiting}, at(6*time.Second))
	d.Key("s", at(8*time.Second))
	if st, _ := stateOf(t, d, 9*time.Second); st != StateWaiting {
		t.Fatalf("state %q: the main thread's question inherited a1's menu", st)
	}
}

// Input from before the prompt is the line that started the turn.
func TestAKeyBeforeAPromptDoesNotAnswerIt(t *testing.T) {
	d := NewDetector()
	d.Key("s", at(0))
	d.Hook("s", HookReport{State: StateWaiting, Answerable: true}, at(3*time.Second))
	if st, _ := stateOf(t, d, 5*time.Second); st != StateWaiting {
		t.Fatalf("state %q, want waiting", st)
	}
}

// A done between two prompts ends the first; the second does not inherit it.
func TestAnswerabilityEndsWithThePrompt(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting, Answerable: true}, at(0))
	d.Hook("s", HookReport{State: StateDone}, at(10*time.Second))
	d.Hook("s", HookReport{State: StateWaiting}, at(20*time.Second))
	d.Key("s", at(25*time.Second))
	if st, _ := stateOf(t, d, 26*time.Second); st != StateWaiting {
		t.Fatalf("state %q: a question after a done inherited an old menu", st)
	}
}

// Escape at 15:53:51.336 in the transcript, and no hook of any kind, then or
// two minutes later.
func TestAnInterruptEndsAWorkingReport(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWorking}, at(0))
	d.Interrupted("s", at(5*time.Second))
	if st, src := stateOf(t, d, 6*time.Second); st != StateDone || src != SourceHook {
		t.Fatalf("state %q from %q after Escape, want done", st, src)
	}
}

// Escape on the question dialog: the same, from waiting.
func TestAnInterruptEndsAWaitingReport(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting}, at(0))
	d.Interrupted("s", at(30*time.Second))
	if st, _ := stateOf(t, d, 31*time.Second); st != StateDone {
		t.Fatalf("state %q, want done", st)
	}
}

// The report's time is when the panel received it; the interrupt's is when
// Claude wrote it. A hook fired before the Escape can arrive after.
func TestAnInterruptStampedJustBeforeTheReportStillCounts(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWorking}, at(time.Second))
	d.Interrupted("s", at(time.Second-200*time.Millisecond))
	if st, _ := stateOf(t, d, 2*time.Second); st != StateDone {
		t.Fatalf("state %q, want done", st)
	}
	// Read again on the next tick, it does not end the next turn.
	d.Hook("s", HookReport{State: StateWorking}, at(3*time.Second))
	d.Interrupted("s", at(time.Second-200*time.Millisecond))
	if st, _ := stateOf(t, d, 4*time.Second); st != StateWorking {
		t.Fatalf("state %q: the previous turn's interrupt ended this one", st)
	}
	// Nor one from well before the report.
	d.Interrupted("s", at(3*time.Second-2*interruptSkew))
	if st, _ := stateOf(t, d, 5*time.Second); st != StateWorking {
		t.Fatalf("state %q: an interrupt from before the report ended it", st)
	}
}

// Nothing to correct on a session no hook has spoken for, and an interrupt is
// not a reason to invent a report.
func TestAnInterruptWithoutAReportChangesNothing(t *testing.T) {
	d := NewDetector()
	// Tracked, with output and input, so the refusal is about there being no
	// report and not about the session being unknown.
	d.Observe("s", Signals{Visible: true}, at(0))
	d.Input("s", at(0))
	d.Interrupted("s", at(time.Second))
	if st, src := stateOf(t, d, 2*time.Second); st != StateWorking || src != SourceHeuristic {
		t.Fatalf("state %q from %q, want the heuristic's working", st, src)
	}
	if st := d.HookState("s"); st != "" {
		t.Fatalf("hook state %q invented by an interrupt", st)
	}
}

// An interrupt newer than a click is the person acting again; one older is what
// they clicked about.
func TestAClickStandsAgainstAnOlderInterruptAndNotANewerOne(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWorking}, at(0))
	d.SetManual("s", StateWaiting, at(10*time.Second))
	d.Interrupted("s", at(5*time.Second))
	if st, src := stateOf(t, d, 11*time.Second); st != StateWaiting || src != SourceManual {
		t.Fatalf("state %q from %q, want the later click", st, src)
	}

	d = NewDetector()
	d.Hook("s", HookReport{State: StateWorking}, at(0))
	d.SetManual("s", StateWaiting, at(10*time.Second))
	d.Interrupted("s", at(20*time.Second))
	if st, src := stateOf(t, d, 21*time.Second); st != StateDone || src != SourceHook {
		t.Fatalf("state %q from %q, want the later interrupt", st, src)
	}
}

// A refused report is not evidence of anything, so it does not take away what
// somebody clicked either.
func TestARefusedReportLeavesAClick(t *testing.T) {
	d := NewDetector()
	d.Hook("s", HookReport{State: StateWaiting}, at(0))
	d.SetManual("s", StateDone, at(100*time.Millisecond))
	d.Hook("s", HookReport{State: StateWorking}, at(200*time.Millisecond))
	if st, src := stateOf(t, d, time.Second); st != StateDone || src != SourceManual {
		t.Fatalf("state %q from %q, want the click to stand", st, src)
	}
}

// Codex writes no approval into its rollout, so the log says working while
// Codex rings for a y.
func TestABellAfterTheLogOutranksItsWorking(t *testing.T) {
	d := NewDetector()
	d.ReportLog("s", StateWorking, at(0))
	d.Observe("s", Signals{Bell: true, Visible: true}, at(10*time.Second))
	if st, _ := stateOf(t, d, 11*time.Second); st != StateWaiting {
		t.Fatalf("state %q with a bell after the log's working, want waiting", st)
	}
	// Answered: the screen moves on, and the log is right again.
	d.Observe("s", Signals{Visible: true, Advanced: true}, at(20*time.Second))
	if st, src := stateOf(t, d, 21*time.Second); st != StateWorking || src != SourceHook {
		t.Fatalf("state %q from %q after the screen moved, want the log's working", st, src)
	}
}

// A bell from before the log's entry is the log's to overrule, and a log that
// says done is not second-guessed by one.
func TestABellBeforeTheLogDoesNotOutrankIt(t *testing.T) {
	d := NewDetector()
	d.Observe("s", Signals{Bell: true, Visible: true}, at(0))
	d.ReportLog("s", StateWorking, at(time.Second))
	if st, _ := stateOf(t, d, 2*time.Second); st != StateWorking {
		t.Fatalf("state %q, want the newer log's working", st)
	}
	d.ReportLog("s", StateDone, at(3*time.Second))
	d.Observe("s", Signals{Bell: true, Visible: true}, at(4*time.Second))
	if st, _ := stateOf(t, d, 5*time.Second); st != StateDone {
		t.Fatalf("state %q, want the log's done: Codex rings when a turn completes", st)
	}
}
