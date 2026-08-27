package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

func ingester(t *testing.T, claudeRoot string) (*Ingester, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() }) //nolint:errcheck // temp file
	return &Ingester{
		Scanner: &Scanner{ClaudeRoot: claudeRoot, CodexRoot: filepath.Join(t.TempDir(), "none"),
			Loc: time.UTC},
		DB: db,
	}, db
}

func writeTranscript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// The cursor. A second pass over transcripts nothing has touched must open no
// files at all — which is the entire reason this is incremental. On the machine
// this was written on the difference is 3.09 seconds against 35 milliseconds,
// every thirty seconds, forever, on a box meant to stay up for months.
func TestASecondPassReadsNothingWhenNothingChanged(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "a.jsonl",
		claudeLine("2026-08-20T10:00:00.000Z", "s1", "/p", "m1", "r1", "opus", 1, 10, 0, 0)+"\n")
	in, _ := ingester(t, root)

	first := in.RunNow(context.Background())
	if first.Read != 1 || first.Seen != 1 {
		t.Fatalf("first pass read %d of %d, want 1 of 1", first.Read, first.Seen)
	}
	second := in.RunNow(context.Background())
	if second.Read != 0 {
		t.Errorf("second pass re-read %d transcripts although none had changed; "+
			"the (size, mtime) cursor is not being consulted", second.Read)
	}
	if second.Seen != 1 {
		t.Errorf("second pass saw %d transcripts, want 1", second.Seen)
	}
}

// A transcript that grew is re-read whole, and what it said before is replaced
// rather than added to. The two halves have to hold together: re-reading
// without replacing is the double-count, replacing without re-reading is the
// lost history.
func TestAGrownTranscriptIsReReadAndReplacesWhatItSaid(t *testing.T) {
	root := t.TempDir()
	line1 := claudeLine("2026-08-20T10:00:00.000Z", "s1", "/p", "m1", "r1", "opus", 1, 10, 0, 0)
	path := writeTranscript(t, root, "a.jsonl", line1+"\n")
	in, db := ingester(t, root)
	ctx := context.Background()

	in.RunNow(ctx)

	// Appended to, the way an agent does it. The mtime has to move or the
	// cursor is right to skip it; a same-second write is what os.Chtimes is
	// for here rather than a sleep.
	line2 := claudeLine("2026-08-20T11:00:00.000Z", "s1", "/p", "m2", "r2", "opus", 1, 5, 0, 0)
	if err := os.WriteFile(path, []byte(line1+"\n"+line2+"\n"), 0o600); err != nil {
		t.Fatalf("append: %v", err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	p := in.RunNow(ctx)
	if p.Read != 1 {
		t.Fatalf("the grown transcript was not re-read (read %d)", p.Read)
	}
	days, err := db.UsageByDay(ctx, store.UsageFilter{})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 || days[0].Output != 15 {
		t.Fatalf("got %+v, want one day of 15 output tokens; the first record was counted twice "+
			"or the second was lost", days)
	}
}

// A transcript deleted between passes takes its numbers with it.
func TestAPassForgetsTranscriptsThatHaveGone(t *testing.T) {
	root := t.TempDir()
	path := writeTranscript(t, root, "a.jsonl",
		claudeLine("2026-08-20T10:00:00.000Z", "s1", "/p", "m1", "r1", "opus", 1, 10, 0, 0)+"\n")
	writeTranscript(t, root, "b.jsonl",
		claudeLine("2026-08-20T10:00:00.000Z", "s2", "/p", "m2", "r2", "opus", 1, 3, 0, 0)+"\n")
	in, db := ingester(t, root)
	ctx := context.Background()
	in.RunNow(ctx)

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	in.RunNow(ctx)

	days, err := db.UsageByDay(ctx, store.UsageFilter{})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 || days[0].Output != 3 {
		t.Errorf("got %+v, want only b.jsonl's 3 output tokens; a deleted transcript "+
			"is still being counted", days)
	}
}

// An agent that is not installed reports as unread, not as zero.
func TestAPassReportsAToolItCouldNotFind(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "a.jsonl",
		claudeLine("2026-08-20T10:00:00.000Z", "s1", "/p", "m1", "r1", "opus", 1, 10, 0, 0)+"\n")
	in, _ := ingester(t, root)

	p := in.RunNow(context.Background())
	var codex *Source
	for i := range p.Sources {
		if p.Sources[i].Tool == ToolCodex {
			codex = &p.Sources[i]
		}
	}
	if codex == nil {
		t.Fatal("the pass said nothing at all about Codex")
	}
	if codex.Found {
		t.Error("a Codex directory that does not exist reported Found")
	}
	if codex.Problem == "" {
		t.Error("nothing said why Codex contributed nothing, so the panel would show a zero")
	}
}

// Until a pass has finished there is no answer, and "no answer" is not zero.
// The panel reads Pass.At to decide whether to render a total or say it is
// still reading.
func TestAnIngesterWithNoCompletedPassSaysSo(t *testing.T) {
	in, _ := ingester(t, t.TempDir())
	p, running := in.Status()
	if !p.At.IsZero() {
		t.Error("a fresh ingester claims a pass has completed")
	}
	if running {
		t.Error("a fresh ingester claims a pass is running")
	}
}
