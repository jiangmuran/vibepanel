package ws

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// replayOf collects the replay frames that follow a subscribe, until the
// socket goes quiet.
func (v *viewer) replayOf() []byte {
	v.t.Helper()
	var out []byte
	for {
		select {
		case r := <-v.in:
			if r.typ == websocket.MessageBinary && len(r.data) > 0 && r.data[0] == FrameReplay {
				out = append(out, r.data[binaryHeaderLen:]...)
			}
		case <-time.After(500 * time.Millisecond):
			return out
		}
	}
}

func TestAResubscribeCarriesOnFromWhereTheViewerStopped(t *testing.T) {
	s := serveSession(t, "vp_ws_resume", `i=0; while [ $i -lt 300 ]; do echo "history line $i"; i=$((i+1)); done`)

	a := dialViewer(t, s, "resume-a")
	a.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24})
	first := a.await(MsgSubscribed)
	full := a.replayOf()
	if first.ReplayStream == "" || len(full) == 0 {
		t.Fatalf("the first subscribe named no stream (%q) or sent no replay (%d bytes); "+
			"nothing below means anything", first.ReplayStream, len(full))
	}
	if first.Resumed {
		t.Error("a first subscribe, with nothing to resume from, was marked as a continuation")
	}
	seen := first.ReplayOffset + int64(len(full))

	// Output the first viewer never receives: what happens while its
	// connection is down. The pane echoes what is typed at it.
	if _, err := s.live.Write("resume-a", []byte("while-you-were-away\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	b := dialViewer(t, s, "resume-a")
	b.send(map[string]any{
		"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24,
		"stream": first.ReplayStream, "since": seen,
	})
	again := b.await(MsgSubscribed)
	gap := b.replayOf()
	if !again.Resumed {
		t.Fatalf("a resubscribe from offset %d of stream %s was not resumed: the viewer "+
			"would clear its terminal and take the whole %d bytes again", seen, first.ReplayStream, len(full))
	}
	if again.ReplayOffset != seen {
		t.Errorf("resumed replay starts at %d, want %d, where the viewer stopped", again.ReplayOffset, seen)
	}
	if !bytes.Contains(gap, []byte("while-you-were-away")) {
		t.Errorf("the resumed replay did not carry the output the viewer missed: %q", gap)
	}
	if bytes.Contains(gap, []byte("history line 10\r")) {
		t.Error("the resumed replay repeated history the viewer already had")
	}
	if len(gap) >= len(full) {
		t.Errorf("the resumed replay was %d bytes against %d for the whole snapshot", len(gap), len(full))
	}
}

func TestAResumeFromAnotherStreamGetsTheWholeSnapshot(t *testing.T) {
	s := serveSession(t, "vp_ws_resume_other", `echo some history`)
	v := dialViewer(t, s, "resume-other")
	// An offset from an attachment that no longer exists: what a browser
	// holds across a panel restart. Offset 0 of it is still not offset 0 of
	// this one.
	v.send(map[string]any{
		"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24,
		"stream": "0000000000000000", "since": 0,
	})
	m := v.await(MsgSubscribed)
	got := v.replayOf()
	if m.Resumed {
		t.Error("a resume from a different stream was accepted; the viewer would splice two " +
			"unrelated streams together")
	}
	if !strings.Contains(string(got), "some history") {
		t.Errorf("the fallback was not the whole snapshot: %q", got)
	}
}
