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

// The three cases the tests above do not reach, all of them multi-byte.
//
// The finding this came from said both existing tests feed pure ASCII, where a
// byte slice and a rune slice agree. That is wrong: TestTruncateTitleDoesNotSplitACharacter
// has been there since the original commit and drives 中, é and 🙂, and it
// catches a byte-sliced truncation.
//
// What it does not catch is a byte-counted *guard*. Changing only
// `utf8.RuneCountInString(title) <= MaxTitleRunes` to `len(title) <= …` and
// leaving the rune walk alone passes all three: the ASCII exact-limit case has
// len == runes, and the multi-byte case feeds 2000 runes, which is over the
// limit either way. A title of exactly MaxTitleRunes CJK characters is 768
// bytes, so that version cuts a title it should not touch -- measured, and this
// is the only test that says so.
//
// The marker on a multi-byte title and the content of what is kept are the
// other two: one test asserts the marker on ASCII, and nothing asserts that the
// text before it is the text that went in.
func TestTruncateTitleCountsRunesNotBytes(t *testing.T) {
	// Three bytes each, so byte arithmetic and rune arithmetic cannot agree.
	const cjk = "工"

	// A title exactly at the limit is not touched. 256 runes, 768 bytes.
	exact := strings.Repeat(cjk, MaxTitleRunes)
	if TruncateTitle(exact) != exact {
		t.Errorf("a title of exactly %d runes was cut; only its byte length is over",
			MaxTitleRunes)
	}

	got := TruncateTitle(strings.Repeat(cjk, MaxTitleRunes+50))
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a multi-byte title that was cut does not say so: %q", got[len(got)-9:])
	}
	// Every rune before the marker is the character that went in. A cut that
	// split one leaves U+FFFD here, which is valid UTF-8 and still wrong.
	if body := strings.TrimSuffix(got, "…"); body != strings.Repeat(cjk, MaxTitleRunes-1) {
		t.Errorf("the kept text is not %d copies of the input character; it ends %q",
			MaxTitleRunes-1, body[max(0, len(body)-9):])
	}
}
