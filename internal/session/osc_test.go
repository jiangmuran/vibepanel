package session

import (
	"encoding/base64"
	"reflect"
	"strings"
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

func TestHasPrintable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"plain text", "hello", true},
		{"text with colour", "\x1b[31mred\x1b[0m", true},
		{"newline only", "\r\n", true},
		{
			// This exact shape is what tmux emits five seconds after a client
			// attaches, when its terminal capability query goes unanswered. It
			// is the terminal being reconfigured, not the session doing
			// anything, and counting it as output reset every session's state.
			name: "tmux re-initialisation",
			in:   "\x1b[?1004h\x1b[?7727h\x1b(B\x1b[m\x1b[?12l\x1b[?25h\x1b[?1006l\x1b[1;1H\x1b[1;32r",
			want: false,
		},
		{"cursor moves only", "\x1b[1;1H\x1b[2;5H\x1b[K", false},
		{"empty", "", false},
		{"osc title only", "\x1b]0;a title\x07", false},
		{"osc title then text", "\x1b]0;a title\x07real output", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPrintable([]byte(tc.in)); got != tc.want {
				t.Errorf("hasPrintable(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTerminalQueryReplies(t *testing.T) {
	// The exact batch tmux sends on attach. Leaving any of it unanswered makes
	// tmux wait five seconds and then re-initialise every session at once.
	attach := "\x1b[?1049h\x1b[?2031h\x1b[?996n\x1b(B\x1b[m\x1b[1;1H\x1b[1;32r" +
		"\x1b[c\x1b[>c\x1b[>q\x1b]10;?\x1b\\\x1b]11;?\x1b\\\x1b[1;1H"
	reply := string(terminalQueryReplies([]byte(attach), 120, 32))

	for _, want := range []string{
		"\x1b[?1;2c",       // DA1
		"\x1b[>0;276;0c",   // DA2
		"\x1bP>|vibepanel", // XTVERSION
		"\x1b]10;rgb:",     // foreground
		"\x1b]11;rgb:",     // background
		"\x1b[?997;",       // colour scheme
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("no answer for %q in %q", want, reply)
		}
	}

	sizes := string(terminalQueryReplies([]byte("\x1b[18t\x1b[14t"), 120, 32))
	if !strings.Contains(sizes, "\x1b[8;32;120t") {
		t.Errorf("character size answer missing from %q", sizes)
	}
	if !strings.Contains(sizes, "\x1b[4;544;960t") {
		t.Errorf("pixel size answer missing from %q", sizes)
	}
}

func TestOrdinaryOutputProducesNoReplies(t *testing.T) {
	// Anything we send goes to the pane as if it were typed, so answering
	// something that was not a question is worse than staying quiet.
	for _, in := range []string{
		"just some text\r\n",
		"\x1b[31mcoloured\x1b[0m",
		"\x1b[2J\x1b[H",
		"a c > q 10;? 18t 14t", // the letters, not the sequences
	} {
		if got := terminalQueryReplies([]byte(in), 80, 24); len(got) != 0 {
			t.Errorf("terminalQueryReplies(%q) = %q, want nothing", in, got)
		}
	}
}
