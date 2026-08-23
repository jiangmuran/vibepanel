package session

import (
	"encoding/base64"
	"reflect"
	"testing"
)

func osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
}

func TestOSCTitle(t *testing.T) {
	s := newOSCScanner()
	s.feed([]byte("before\x1b]0;my session\x07after"))
	_, _, titles := s.drain()
	if !reflect.DeepEqual(titles, []string{"my session"}) {
		t.Errorf("titles = %q, want [my session]", titles)
	}
}

func TestOSCTitleWithSTTerminator(t *testing.T) {
	// Several agent TUIs terminate with ST (ESC backslash) rather than BEL.
	// Handling only BEL means those sessions never get a name.
	s := newOSCScanner()
	s.feed([]byte("\x1b]2;st terminated\x1b\\rest"))
	_, _, titles := s.drain()
	if !reflect.DeepEqual(titles, []string{"st terminated"}) {
		t.Errorf("titles = %q, want [st terminated]", titles)
	}
}

func TestOSC52Clipboard(t *testing.T) {
	s := newOSCScanner()
	s.feed([]byte("x" + osc52("copied text") + "y"))
	_, clip, _ := s.drain()
	if !reflect.DeepEqual(clip, []string{"copied text"}) {
		t.Errorf("clipboard = %q, want [copied text]", clip)
	}
}

func TestOSC52QueryIsNotACopy(t *testing.T) {
	// "?" asks the terminal for the clipboard's contents. Treating it as a copy
	// would push a literal "?" onto the user's clipboard.
	s := newOSCScanner()
	s.feed([]byte("\x1b]52;c;?\x07"))
	_, clip, _ := s.drain()
	if len(clip) != 0 {
		t.Errorf("clipboard = %q, want empty for a query", clip)
	}
}

func TestOSC52RejectsInvalidBase64(t *testing.T) {
	s := newOSCScanner()
	s.feed([]byte("\x1b]52;c;!!!not base64!!!\x07"))
	_, clip, _ := s.drain()
	if len(clip) != 0 {
		t.Errorf("clipboard = %q, want empty", clip)
	}
}

func TestBellIsDetected(t *testing.T) {
	s := newOSCScanner()
	s.feed([]byte("some output\x07more"))
	bell, _, _ := s.drain()
	if !bell {
		t.Error("bell not detected")
	}
	// drain resets, so the next poll must not see a stale bell — otherwise a
	// session that rang once looks like it is asking for attention forever.
	bell, _, _ = s.drain()
	if bell {
		t.Error("bell survived drain")
	}
}

func TestOSCTerminatorIsNotCountedAsABell(t *testing.T) {
	// An OSC sequence ends with BEL. Counting that as an application bell would
	// mark a session "waiting" every single time the shell set its title —
	// which is on every prompt.
	s := newOSCScanner()
	s.feed([]byte("\x1b]0;title\x07"))
	bell, _, titles := s.drain()
	if bell {
		t.Error("the OSC terminator was miscounted as an application bell")
	}
	if len(titles) != 1 {
		t.Errorf("titles = %q, want the title to still be parsed", titles)
	}
}

func TestOSCSplitAcrossReads(t *testing.T) {
	// PTY reads land on arbitrary boundaries. A scanner without carry-over
	// misses sequences at a low but steady rate, which reads as flakiness.
	full := "\x1b]0;split title\x07"
	for cut := 1; cut < len(full); cut++ {
		s := newOSCScanner()
		s.feed([]byte(full[:cut]))
		s.feed([]byte(full[cut:]))
		_, _, titles := s.drain()
		if len(titles) != 1 || titles[0] != "split title" {
			t.Errorf("cut at %d: titles = %q, want [split title]", cut, titles)
		}
	}
}

func TestOSC52SplitAcrossReads(t *testing.T) {
	full := osc52("clipboard across reads")
	for cut := 1; cut < len(full); cut++ {
		s := newOSCScanner()
		s.feed([]byte(full[:cut]))
		s.feed([]byte(full[cut:]))
		_, clip, _ := s.drain()
		if len(clip) != 1 || clip[0] != "clipboard across reads" {
			t.Errorf("cut at %d: clipboard = %q", cut, clip)
		}
	}
}

func TestUnterminatedOSCDoesNotGrowForever(t *testing.T) {
	// A pane can emit an OSC introducer and never terminate it. Buffering that
	// without a bound hands any session a way to exhaust the panel's memory.
	s := newOSCScanner()
	s.feed([]byte("\x1b]0;"))
	big := make([]byte, maxPending*2)
	for i := range big {
		big[i] = 'a'
	}
	s.feed(big)
	if len(s.pending) > maxPending {
		t.Errorf("pending grew to %d, want at most %d", len(s.pending), maxPending)
	}
}

func TestNotificationCountsAsAttention(t *testing.T) {
	// OSC 9 is how several agents say "I need you". The text is usually a
	// restatement of what is already on screen, so only the signal is kept.
	s := newOSCScanner()
	s.feed([]byte("\x1b]9;Claude needs your approval\x07"))
	bell, _, _ := s.drain()
	if !bell {
		t.Error("OSC 9 notification did not raise attention")
	}
}

func TestPlainOutputProducesNothing(t *testing.T) {
	s := newOSCScanner()
	s.feed([]byte("\x1b[31mordinary coloured output\x1b[0m\nline two\n"))
	bell, clip, titles := s.drain()
	if bell || len(clip) != 0 || len(titles) != 0 {
		t.Errorf("plain output produced bell=%v clip=%q titles=%q", bell, clip, titles)
	}
}
