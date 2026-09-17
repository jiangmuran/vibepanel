package claudelog

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Lines as Claude Code 2.1.273 wrote them, trimmed of fields nothing reads.
const (
	prompt      = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"Write a 900-word essay about rivers"},"timestamp":"2026-09-16T15:53:46.279Z"}`
	answer      = `{"type":"assistant","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"# The Vital Arteries"}]},"timestamp":"2026-09-16T15:53:51.316Z"}`
	interrupted = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"timestamp":"2026-09-16T15:53:51.336Z"}`
	rejected    = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use.","is_error":true,"tool_use_id":"t"}]},"timestamp":"2026-09-16T15:53:59.339Z"}`
	forTool     = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]},"timestamp":"2026-09-16T15:53:59.340Z"}`
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestScanFindsBothKindsOfInterrupt(t *testing.T) {
	got := Scan(strings.NewReader(prompt+"\n"+answer+"\n"+interrupted+"\n"), time.Time{})
	if want := ts("2026-09-16T15:53:51.336Z"); !got.Equal(want) {
		t.Fatalf("an interrupted answer: got %v, want %v", got, want)
	}
	got = Scan(strings.NewReader(rejected+"\n"+forTool+"\n"), got)
	if want := ts("2026-09-16T15:53:59.340Z"); !got.Equal(want) {
		t.Fatalf("an interrupted tool: got %v, want %v", got, want)
	}
}

// Text that mentions the marker is not the marker: a prompt quoting it, an
// answer explaining it, a tool result containing it, a subagent's transcript
// line. Each would end a turn that is still going.
func TestScanIgnoresTheMarkerQuoted(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"content":"why does it say [Request interrupted by user] here?"},"timestamp":"2026-09-16T16:00:00Z"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"see: [Request interrupted by user]"}]},"timestamp":"2026-09-16T16:00:01Z"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]},"timestamp":"2026-09-16T16:00:02Z"}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"[Request interrupted by user]"}]},"timestamp":"2026-09-16T16:00:03Z"}`,
		`{"type":"user","isSidechain":true,"message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]},"timestamp":"2026-09-16T16:00:04Z"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"},{"type":"text","text":"and more"}]},"timestamp":"2026-09-16T16:00:05Z"}`,
	}
	for _, l := range lines {
		if got := Scan(strings.NewReader(l+"\n"), time.Time{}); !got.IsZero() {
			t.Errorf("read as an interrupt at %v:\n%s", got, l)
		}
	}
}

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheWatcherFollowsWhatIsAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	write(t, path, prompt, answer)
	var w Watcher
	if _, ok := w.Interrupted("s", path); ok {
		t.Fatal("an interrupt found in a transcript without one")
	}
	write(t, path, interrupted)
	at, ok := w.Interrupted("s", path)
	if !ok || !at.Equal(ts("2026-09-16T15:53:51.336Z")) {
		t.Fatalf("got %v %v after the interrupt was appended", at, ok)
	}
	// Half a line: the agent is in the middle of writing it.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(forTool[:40])
	f.Close()
	if at2, _ := w.Interrupted("s", path); !at2.Equal(at) {
		t.Fatalf("half a line moved the answer to %v", at2)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(forTool[40:] + "\n")
	f.Close()
	if at3, _ := w.Interrupted("s", path); !at3.Equal(ts("2026-09-16T15:53:59.340Z")) {
		t.Fatalf("the finished line was not read: %v", at3)
	}
}

// /clear starts a new transcript, and what the old one said is over.
func TestANewTranscriptStartsClean(t *testing.T) {
	dir := t.TempDir()
	old, next := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	write(t, old, interrupted)
	write(t, next, prompt)
	var w Watcher
	if _, ok := w.Interrupted("s", old); !ok {
		t.Fatal("setup: no interrupt in the old transcript")
	}
	if at, ok := w.Interrupted("s", next); ok {
		t.Fatalf("the old transcript's interrupt at %v carried into the new one", at)
	}
}

// Only the end of a long transcript is read the first time.
func TestTheWatcherStartsNearTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	write(t, path, interrupted)
	filler := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 1000) + `"},"timestamp":"2026-09-16T16:00:00Z"}`
	for i := 0; i < (initialTail/len(filler))+10; i++ {
		write(t, path, filler)
	}
	var w Watcher
	if at, ok := w.Interrupted("s", path); ok {
		t.Fatalf("an interrupt %d KiB back was read at %v; the first read should be the tail", initialTail>>10, at)
	}
}

// The path came from a hook document. A FIFO there would block the poller on
// open until something wrote to it.
func TestAFIFOIsNotOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan bool)
	go func() {
		var w Watcher
		_, ok := w.Interrupted("s", path)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a FIFO yielded an interrupt")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}

func TestAMissingOrEmptyPathFindsNothing(t *testing.T) {
	var w Watcher
	if _, ok := w.Interrupted("s", ""); ok {
		t.Fatal("an empty path yielded an interrupt")
	}
	if _, ok := w.Interrupted("s", filepath.Join(t.TempDir(), "gone.jsonl")); ok {
		t.Fatal("a missing file yielded an interrupt")
	}
}

func TestRetainForgetsTheSessionsNotListed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	write(t, path, interrupted)
	var w Watcher
	w.Interrupted("a", path)
	w.Interrupted("b", path)
	w.Retain([]string{"b"})
	if _, ok := w.tail["a"]; ok {
		t.Fatal("a was kept")
	}
	if _, ok := w.tail["b"]; !ok {
		t.Fatal("b was dropped")
	}
}

// When the tail starts exactly at a line, that line is whole and is read; the
// first version skipped it as the partial line a tail usually starts inside.
func TestATailThatStartsOnALineReadsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	filler := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 1000) + `"},"timestamp":"2026-09-16T16:00:00Z"}`
	// Pad so that the interrupt line begins exactly initialTail bytes from
	// the end.
	pad := initialTail - len(interrupted) - 1
	var b strings.Builder
	b.WriteString(filler + "\n")
	for b.Len() < pad {
		b.WriteString(filler[:min(len(filler), pad-b.Len()-1)] + "\n")
	}
	if b.Len() != pad {
		t.Skipf("could not pad to %d (got %d)", pad, b.Len())
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("y", 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, path, interrupted)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(b.String())
	f.Close()
	info, _ := os.Stat(path)
	if start := info.Size() - initialTail; start != 11 {
		t.Fatalf("setup: tail starts at %d, want 11, the interrupt line's first byte", start)
	}
	var w Watcher
	if _, ok := w.Interrupted("s", path); !ok {
		t.Fatal("the interrupt the tail starts on was skipped")
	}
}

// A transcript left unread while its session sat at done, which has grown by
// more than a tail since, is picked up at its end rather than crawled through
// one read at a time -- during which the interrupt at the end went unseen.
func TestAWatcherFarBehindJumpsToTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	write(t, path, prompt)
	var w Watcher
	w.Interrupted("s", path)
	big := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 64<<10) + `"},"timestamp":"2026-09-16T16:00:00Z"}`
	for i := 0; i < (maxRead/len(big))*3; i++ {
		write(t, path, big)
	}
	write(t, path, interrupted)
	if _, ok := w.Interrupted("s", path); !ok {
		t.Fatal("the interrupt at the end of a transcript far ahead was not seen on the first read")
	}
}
