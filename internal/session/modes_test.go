package session

import (
	"strings"
	"testing"
)

func TestModeTrackerFollowsSetAndReset(t *testing.T) {
	m := newModeTracker()
	m.feed([]byte("hello\x1b[?1000h\x1b[?1002h\x1b[?1006h\x1b[?2004hworld"))
	if got, want := string(m.prefix()), "\x1b[?1000h\x1b[?1002h\x1b[?1006h\x1b[?2004h"; got != want {
		t.Fatalf("prefix = %q, want %q", got, want)
	}
	m.feed([]byte("\x1b[?1006l\x1b[?1002l\x1b[?1000l"))
	if got, want := string(m.prefix()), "\x1b[?2004h"; got != want {
		t.Fatalf("after reset, prefix = %q, want %q", got, want)
	}
}

func TestModeTrackerReadsAcrossChunks(t *testing.T) {
	m := newModeTracker()
	// Cut at every point of the sequence.
	seq := "\x1b[?1002;1006h"
	for cut := 1; cut < len(seq); cut++ {
		m = newModeTracker()
		m.feed([]byte("abc" + seq[:cut]))
		m.feed([]byte(seq[cut:] + "def"))
		if got, want := string(m.prefix()), "\x1b[?1002h\x1b[?1006h"; got != want {
			t.Fatalf("cut at %d: prefix = %q, want %q", cut, got, want)
		}
	}
}

func TestModeTrackerIgnoresOtherSequences(t *testing.T) {
	m := newModeTracker()
	m.feed([]byte("\x1b[31m\x1b[?25l\x1b[2J\x1b[H\x1b]0;title\x07\x1b[?1049h\x1b[?2026h\x1b[?5n\x1b=\x1bP+q\x1b\\"))
	// A hidden cursor is a reset of a mode that starts on, and is replayed as
	// the reset; alternate screen and synchronized output are never replayed;
	// nothing else here is a mode.
	if got, want := string(m.prefix()), "\x1b[?25l"; got != want {
		t.Fatalf("prefix = %q, want %q", got, want)
	}
}

func TestModeTrackerDropsALoneEscapeFollowedByText(t *testing.T) {
	m := newModeTracker()
	m.feed([]byte("\x1b"))
	m.feed(make([]byte, 1000))
	m.feed([]byte("\x1b[?1000h"))
	if got, want := string(m.prefix()), "\x1b[?1000h"; got != want {
		t.Fatalf("prefix = %q, want %q", got, want)
	}
	if len(m.pending) != 0 {
		t.Fatalf("pending should be empty, has %d bytes", len(m.pending))
	}
}

func TestModeTrackerReplaysNothingForTheDefaults(t *testing.T) {
	m := newModeTracker()
	m.feed([]byte("\x1b[?25h\x1b[?7h\x1b[?2004l"))
	if got := m.prefix(); len(got) != 0 {
		t.Fatalf("defaults should replay nothing, got %q", got)
	}
}

func TestAutowrapOffIsReplayedToALateViewer(t *testing.T) {
	m := newModeTracker()
	// A full-screen app: alternate screen, autowrap off, hidden cursor, mouse on.
	m.feed([]byte("\x1b[?1049h\x1b[?7l\x1b[?25l\x1b[?1002h\x1b[?1006h"))
	got := string(m.prefix())
	// Autowrap off is the one that stops a full-width footer from wrapping and
	// doubling. The alternate screen is never replayed.
	if !strings.Contains(got, "\x1b[?7l") {
		t.Fatalf("prefix does not disable autowrap: %q", got)
	}
	if strings.Contains(got, "\x1b[?1049") {
		t.Fatalf("prefix replays the alternate screen, which it must not: %q", got)
	}
	if !strings.Contains(got, "\x1b[?25l") || !strings.Contains(got, "\x1b[?1002h") || !strings.Contains(got, "\x1b[?1006h") {
		t.Fatalf("prefix is missing the cursor or mouse modes: %q", got)
	}
}
