package session

import (
	"os"
	"testing"
	"time"
)

// Input reaches the detector only when it is a line: a keystroke that submits,
// not every character typed into a prompt.
func TestALiveCallsOnInputForALine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close() //nolint:errcheck
	defer w.Close() //nolint:errcheck
	var got []string
	l := &Live{ID: "s", ptmx: w, onInput: func(id string) { got = append(got, id) }}
	for _, p := range []string{"h", "i", "\r"} {
		if _, err := l.Write("viewer", []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0] != "s" {
		t.Errorf("onInput calls = %v, want one for the carriage return", got)
	}
}

// Codex's legacy `notify` fires when a turn ends and never when the next one
// starts. Before this rule a Codex session that had reported "waiting" once
// reported it forever: 「现在几乎不可用」.
func TestALegacyNotifyIsReleasedWhenTheNextTurnStarts(t *testing.T) {
	t.Run("the screen moves on", func(t *testing.T) {
		d := NewDetector()
		d.ReportNotify("s", StateWaiting, at(0))
		// The end of the answer, landing as the notify fires.
		d.Observe("s", Signals{Visible: true, Advanced: true}, at(500*time.Millisecond))
		if st, _ := d.Evaluate("s", Observation{}, at(time.Second)); st != StateWaiting {
			t.Errorf("released by the last lines of the turn that reported it: %q", st)
		}
		// Well after: the agent is producing output again.
		d.Observe("s", Signals{Visible: true, Advanced: true}, at(10*time.Second))
		if st, src := d.Evaluate("s", Observation{}, at(11*time.Second)); st != StateWorking || src != SourceHeuristic {
			t.Errorf("state = %q from %q once the screen moved on, want working from the heuristic", st, src)
		}
	})
	t.Run("somebody sends a line", func(t *testing.T) {
		d := NewDetector()
		d.ReportNotify("s", StateWaiting, at(0))
		d.Input("s", at(time.Minute))
		if st, _ := d.Evaluate("s", Observation{}, at(time.Minute+time.Second)); st != StateWorking {
			t.Errorf("state = %q after a prompt was sent, want working", st)
		}
	})
	t.Run("nothing happens", func(t *testing.T) {
		d := NewDetector()
		d.ReportNotify("s", StateWaiting, at(0))
		d.Observe("s", Signals{Bytes: 30, Visible: true}, at(20*time.Second)) // a redraw, no line feed
		if st, src := d.Evaluate("s", Observation{}, at(time.Hour)); st != StateWaiting || src != SourceHook {
			t.Errorf("state = %q from %q with nothing done since, want waiting from the hook", st, src)
		}
	})
}

// The same inputs against a real hook report must change nothing: that report
// is superseded by the next report, which is TestAnAnimationDoesNotDiscardAHookReport's
// whole argument.
func TestAHookReportIsNotReleasedLikeANotify(t *testing.T) {
	d := NewDetector()
	d.Report("s", StateWaiting, at(0))
	d.Observe("s", Signals{Visible: true, Advanced: true}, at(10*time.Second))
	d.Input("s", at(20*time.Second))
	if st, src := d.Evaluate("s", Observation{}, at(time.Minute)); st != StateWaiting || src != SourceHook {
		t.Errorf("state = %q from %q, want waiting from the hook: the release is for notify alone", st, src)
	}
}

func TestMarkNotifyAppliesToARestoredReport(t *testing.T) {
	d := NewDetector()
	d.Restore("s", StateWaiting, SourceHook, at(0))
	d.MarkNotify("s")
	d.Input("s", at(time.Minute))
	if st, _ := d.Evaluate("s", Observation{}, at(time.Minute+time.Second)); st != StateWorking {
		t.Errorf("a restored notify report stuck after input: %q", st)
	}
	// And marking a session with no hook state creates nothing.
	d.MarkNotify("other")
	if d.Tracked() != 1 {
		t.Errorf("MarkNotify created a tracker")
	}
}

func TestTheAgentsLogDecidesWhenNoHookHas(t *testing.T) {
	d := NewDetector()
	d.ReportLog("s", StateWorking, at(0))
	if st, src := d.Evaluate("s", Observation{}, at(time.Second)); st != StateWorking || src != SourceHook {
		t.Errorf("state = %q from %q", st, src)
	}
	d.ReportLog("s", StateWaiting, at(5*time.Second))
	if st, _ := d.Evaluate("s", Observation{}, at(6*time.Second)); st != StateWaiting {
		t.Errorf("an approval request in the log read as %q", st)
	}
	// Not from a shell: the agent that wrote the log is not there any more.
	if st, _ := d.Evaluate("s", Observation{ShellOnly: true}, at(7*time.Second)); st != StateDone {
		t.Errorf("a pane back at a shell kept the log's state: %q", st)
	}
	// A hook report newer than the log wins; an older one does not.
	d.Report("s", StateDone, at(10*time.Second))
	if st, _ := d.Evaluate("s", Observation{}, at(11*time.Second)); st != StateDone {
		t.Errorf("a newer hook report lost to the log: %q", st)
	}
	d.ReportLog("s", StateWorking, at(20*time.Second))
	if st, _ := d.Evaluate("s", Observation{}, at(21*time.Second)); st != StateWorking {
		t.Errorf("a newer log entry lost to an older hook report: %q", st)
	}
}

// The poller hands the same newest log entry over on every tick. A click must
// survive that, and be undone only by an entry written after it.
func TestTheLogDoesNotUndoAClickWithTheEntryItAnswered(t *testing.T) {
	d := NewDetector()
	d.ReportLog("s", StateWaiting, at(0))
	d.SetManual("s", StateDone, at(time.Second))
	d.ReportLog("s", StateWaiting, at(0))
	if st, src := d.Evaluate("s", Observation{}, at(3*time.Second)); st != StateDone || src != SourceManual {
		t.Errorf("state = %q from %q, want the click to stand", st, src)
	}
	d.ReportLog("s", StateWorking, at(10*time.Second))
	if st, _ := d.Evaluate("s", Observation{}, at(11*time.Second)); st != StateWorking {
		t.Errorf("a newer log entry did not replace the click: %q", st)
	}
}
