package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateTitleBoundsWhatAnAgentPrinted(t *testing.T) {
	// Measured before this existed: a pane emitting a 200,000-character title
	// put all of it in the database and took the state snapshot from 705 bytes
	// to 200,710 — a snapshot that is broadcast to every viewer whenever it
	// changes.
	long := strings.Repeat("A", 200000)
	got := TruncateTitle(long)
	if n := utf8.RuneCountInString(got); n != MaxTitleRunes {
		t.Errorf("truncated to %d runes, want %d", n, MaxTitleRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a title that was cut short must say so; without the marker it " +
			"reads as the whole name")
	}
}

func TestTruncateTitleLeavesShortOnesAlone(t *testing.T) {
	for _, s := range []string{"", "claude", "some/path/with spaces", strings.Repeat("x", MaxTitleRunes)} {
		if got := TruncateTitle(s); got != s {
			t.Errorf("TruncateTitle(%d runes) changed it to %d", utf8.RuneCountInString(s), utf8.RuneCountInString(got))
		}
	}
}

func TestTruncateTitleDoesNotSplitACharacter(t *testing.T) {
	// Bytes rather than runes would cut a multi-byte character in half and put
	// invalid UTF-8 in the database, which renders as nothing at all — the
	// failure this panel keeps finding in other guises.
	for _, r := range []string{"中", "é", "🙂"} {
		long := strings.Repeat(r, 2000)
		got := TruncateTitle(long)
		if !utf8.ValidString(got) {
			t.Errorf("truncating a title of %q produced invalid UTF-8", r)
		}
		if n := utf8.RuneCountInString(got); n != MaxTitleRunes {
			t.Errorf("truncating %q gave %d runes, want %d", r, n, MaxTitleRunes)
		}
	}
}

func TestScannerBoundsTitlesItReads(t *testing.T) {
	// The scanner's copy does not go through the store: it is broadcast to
	// every viewer as a title event the moment it arrives.
	s := newOSCScanner()
	s.feed([]byte("\x1b]2;" + strings.Repeat("A", 50000) + "\x07"))
	_, _, titles := s.drain()
	if len(titles) != 1 {
		t.Fatalf("got %d titles, want 1", len(titles))
	}
	if n := utf8.RuneCountInString(titles[0]); n != MaxTitleRunes {
		t.Errorf("broadcast a title of %d runes, want %d", n, MaxTitleRunes)
	}
}
