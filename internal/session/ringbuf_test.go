package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRingBufferBelowCapacity(t *testing.T) {
	r := NewRingBuffer(64)
	r.Write([]byte("hello "))
	r.Write([]byte("world"))

	if got := string(r.Snapshot()); got != "hello world" {
		t.Errorf("Snapshot() = %q, want %q", got, "hello world")
	}
	if r.Overflowed() {
		t.Error("Overflowed() = true for a buffer that never filled")
	}
	if r.Total() != 11 {
		t.Errorf("Total() = %d, want 11", r.Total())
	}
}

func TestRingBufferEvictsOldest(t *testing.T) {
	r := NewRingBuffer(10)
	r.Write([]byte("abcdefghij")) // exactly full
	if got := string(r.Snapshot()); got != "abcdefghij" {
		t.Fatalf("Snapshot() = %q", got)
	}
	r.Write([]byte("klm"))

	// The oldest three bytes must be gone and the rest still in order. A
	// wrap-around bug here shows up as scrambled scrollback on reconnect,
	// which is easy to misread as a terminal problem.
	if got := string(r.Snapshot()); got != "defghijklm" {
		t.Errorf("Snapshot() after wrap = %q, want %q", got, "defghijklm")
	}
	if !r.Overflowed() {
		t.Error("Overflowed() = false after dropping data")
	}
}

func TestRingBufferWriteLargerThanCapacity(t *testing.T) {
	r := NewRingBuffer(8)
	// An agent dumping a large file in one write must leave the buffer holding
	// the most recent bytes, not the first ones.
	r.Write([]byte("aaaaaaaabbbbbbbb"))
	if got := string(r.Snapshot()); got != "bbbbbbbb" {
		t.Errorf("Snapshot() = %q, want the last 8 bytes", got)
	}
	if r.Total() != 16 {
		t.Errorf("Total() = %d, want 16", r.Total())
	}
}

func TestRingBufferManySmallWrites(t *testing.T) {
	// Exercises the wrap arithmetic repeatedly; an off-by-one only shows up
	// after the buffer has turned over several times.
	r := NewRingBuffer(16)
	var want bytes.Buffer
	for i := 0; i < 200; i++ {
		chunk := []byte{byte('a' + i%26)}
		r.Write(chunk)
		want.Write(chunk)
	}
	all := want.Bytes()
	if got, expect := string(r.Snapshot()), string(all[len(all)-16:]); got != expect {
		t.Errorf("Snapshot() = %q, want %q", got, expect)
	}
}

func TestRingBufferReset(t *testing.T) {
	r := NewRingBuffer(8)
	r.Write([]byte("0123456789"))
	r.Reset()
	if r.Len() != 0 || len(r.Snapshot()) != 0 {
		t.Errorf("after Reset: Len=%d Snapshot=%q", r.Len(), r.Snapshot())
	}
	if r.Overflowed() {
		t.Error("Reset should clear the overflow flag")
	}
}

func TestTrimPartialEscape(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// The common case after eviction: the buffer starts in the middle
			// of an SGR sequence. Without trimming, the terminal prints "31m"
			// as literal text on the first line.
			name: "mid CSI parameters",
			in:   "31mred text",
			want: "red text",
		},
		{
			name: "clean start with ESC",
			in:   "\x1b[31mred",
			want: "\x1b[31mred",
		},
		{
			name: "ordinary text is untouched",
			in:   "just some output",
			want: "just some output",
		},
		{
			name: "newline means we are not inside a sequence",
			in:   "tail of a line\nnext",
			want: "tail of a line\nnext",
		},
		{
			name: "mid OSC terminated by BEL",
			in:   "0;some title\x07rest",
			want: "rest",
		},
		{
			// Real text that happens to begin with letters must not be eaten:
			// 'j' is in the CSI final-byte range, but the bytes before it are
			// ordinary letters, not parameters.
			name: "text beginning with letters",
			in:   "hello",
			want: "hello",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(trimPartialEscape([]byte(tc.in))); got != tc.want {
				t.Errorf("trimPartialEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTrimPartialEscapeKnownFalsePositive(t *testing.T) {
	// Digits followed by a letter are exactly what a truncated CSI sequence
	// looks like, so real output shaped that way gets trimmed. Pinned as a
	// test so the behaviour is a documented trade-off rather than a surprise.
	if got := string(trimPartialEscape([]byte("89abcdef"))); got != "bcdef" {
		t.Errorf("trimPartialEscape(\"89abcdef\") = %q, want %q (known false positive)", got, "bcdef")
	}
}

func TestTrimPartialEscapeKnownFalseNegative(t *testing.T) {
	// The mirror of the false positive above, and the more tempting one to
	// "fix". The scan assumes the CSI introducer went with the ESC, so it
	// handles "31mhello" and leaves "[31mhello" alone — the buffer cut between
	// the ESC and the '[', and the replay opens with a literal [31m.
	//
	// Left alone deliberately. Skipping a leading '[' does clean that up, and
	// it was measured against ordinary output at the same time:
	//
	//	"[31mhello"           -> "hello"                 fixed
	//	"[1] done"            -> " done"                 eaten
	//	"[1]+  Done  sleep 5" -> "+  Done  sleep 5"      eaten
	//
	// "[1]" is bash's job-control prefix, so the trade is a visible artifact
	// nobody can mistake for output against silent deletion of text that was
	// really there. This panel exists to answer what an agent did; the version
	// that quietly drops characters is the worse one, and a reader has no way
	// to tell it happened.
	for in, want := range map[string]string{
		"[31mhello":           "[31mhello",
		"[2Jclear":            "[2Jclear",
		"[1]+  Done  sleep 5": "[1]+  Done  sleep 5",
	} {
		if got := string(trimPartialEscape([]byte(in))); got != want {
			t.Errorf("trimPartialEscape(%q) = %q, want %q — see the comment above "+
				"before changing this; the alternative eats real text", in, got, want)
		}
	}
}

func TestSnapshotOnlyTrimsAfterOverflow(t *testing.T) {
	// A buffer that never wrapped starts exactly where the session started, so
	// its first bytes are real output and must never be trimmed.
	r := NewRingBuffer(64)
	r.Write([]byte("31mnot an escape fragment"))
	if got := string(r.Snapshot()); got != "31mnot an escape fragment" {
		t.Errorf("Snapshot() = %q; a non-overflowed buffer must not be trimmed", got)
	}
}

func TestRingBufferConcurrentWriters(t *testing.T) {
	// The PTY pump writes while HTTP handlers snapshot. Run with -race.
	r := NewRingBuffer(4096)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			r.Write([]byte(strings.Repeat("x", 32)))
		}
	}()
	for i := 0; i < 500; i++ {
		_ = r.Snapshot()
		_ = r.Len()
	}
	<-done
}

// TestTheReplayIsSmallEnoughToClickThrough is a tripwire on one number, and on
// the thing that decides what that number costs.
//
// DefaultRingSize does not read like a latency budget, and that is how it came
// to be one. Snapshot is its only reader, Subscribe is Snapshot's only caller,
// and the panel resubscribes every time somebody selects a session -- so this
// constant is the number of bytes a person waits for on every click, and
// Attach primes the buffer full on the first tick, so it is not "up to"
// anything: it is what gets sent.
//
// The budget is 2 MiB *because the socket compresses*. Measured on a pane out
// of a live panel: 764 KB of a shell's history goes to 59 KB with gzip, about
// thirteen to one, which is what terminal output does. Uncompressed, 2 MiB was
// 2.6 seconds over a 20 Mbit link and shrinking it to 512 KiB produced the
// opposite complaint -- 「无法滚动，少渲染了好多东西」 -- because 512 KiB is six
// per cent of that pane.
//
// So the two are checked together. A build that raises the ring is fine; a
// build that turns the compression off underneath it is the 2.6 seconds again,
// and nothing else in the tree would say so.
func TestTheReplayIsSmallEnoughToClickThrough(t *testing.T) {
	const budget = 2 << 20
	if DefaultRingSize > budget {
		t.Errorf("DefaultRingSize is %d bytes; every session switch sends all of it to the "+
			"browser, and %d is the ceiling for a click that feels immediate on a link that "+
			"is not the loopback. Raise the budget deliberately, with a measurement.",
			DefaultRingSize, budget)
	}

	// The other half. Read as text because internal/session must not import
	// internal/ws to ask -- and because what is being pinned is a decision
	// written in one line of another package, which is exactly the shape that
	// gets changed by somebody who has not read this file.
	src, err := os.ReadFile(filepath.Join("..", "ws", "conn.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "CompressionMode: websocket.CompressionNoContextTakeover") {
		t.Errorf("the socket no longer compresses, and this buffer is %d bytes on the "+
			"strength of it doing so. Either put the compression back or bring the ring "+
			"down to something that is bearable uncompressed.", DefaultRingSize)
	}
}
