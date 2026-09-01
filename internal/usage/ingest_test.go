package usage

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// A root the pass could not look at is not a root whose transcripts are gone.
//
// The sweep that forgets deleted transcripts was driven by "everything the
// last pass knew about that this pass did not see", and a walk that could not
// enumerate its root returns an empty list — so one moment of a missing
// directory (a remount, an agent updating itself, a chmod on a subdirectory)
// read as every transcript having been deleted and took every rolled-up day
// with it, cascade and all. The panel then reports zero, which is the one
// answer this package exists to refuse, and the whole corpus is re-read from
// cold when the directory comes back.
func TestARootThatCannotBeReadDoesNotForgetItsTranscripts(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "a.jsonl",
		claudeLine("2026-08-20T10:00:00.000Z", "s1", "/p", "m1", "r1", "opus", 1, 10, 0, 0)+"\n")
	in, db := ingester(t, root)
	ctx := context.Background()
	in.RunNow(ctx)

	// Moved away rather than emptied: the transcripts still exist, this pass
	// just cannot see them.
	away := filepath.Join(filepath.Dir(root), "moved-"+filepath.Base(root))
	if err := os.Rename(root, away); err != nil {
		t.Fatalf("rename: %v", err)
	}
	in.RunNow(ctx)

	days, err := db.UsageByDay(ctx, store.UsageFilter{})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 || days[0].Output != 10 {
		t.Fatalf("got %+v, want the 10 output tokens already counted; a root that "+
			"could not be read was treated as a root whose files were deleted", days)
	}
	stamps, err := db.UsageStamps(ctx)
	if err != nil {
		t.Fatalf("stamps: %v", err)
	}
	if len(stamps) != 1 {
		t.Errorf("the cursor holds %d transcripts, want 1; the whole corpus will be "+
			"re-read from cold when the directory comes back", len(stamps))
	}

	// And the sweep still works once the root is readable again.
	if err := os.Rename(away, root); err != nil {
		t.Fatalf("rename back: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "a.jsonl")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	in.RunNow(ctx)
	if days, err = db.UsageByDay(ctx, store.UsageFilter{}); err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 0 {
		t.Errorf("got %+v, want nothing; a transcript deleted from a root that was "+
			"read must still take its numbers with it", days)
	}
}

// The zone is never changed underneath a pass that is walking.
//
// Run under -race, which is where this fails: the settings handler used to
// assign Scanner.Loc from the request goroutine, and a pass reads it for every
// record it buckets on a background one. The detector's own report of it named
// Scanner.loc() at usage.go:550 against a bare write from the HTTP goroutine.
//
// The wipe in the same handler is the half that outlives the race: a pass that
// started before it holds a snapshot of the cursor taken before it, so the
// transcripts it goes on to read get rows in the old zone and cursors that
// look current, and no later pass ever opens them again. Rezone waits for the
// pass instead, which is why the labels below can be asserted at all.
func TestTheZoneCannotChangeUnderARunningPass(t *testing.T) {
	root := t.TempDir()
	// 23:30 UTC is already the next day in Shanghai. Most of the day the two
	// zones agree and a test using `now` would prove nothing.
	const at = "2026-08-20T23:30:00.000Z"
	// Enough transcripts that a pass is still walking when the zone changes;
	// the whole failure is an overlap.
	const files = 300
	for i := 0; i < files; i++ {
		writeTranscript(t, root, "s"+strconv.Itoa(i)+".jsonl",
			claudeLine(at, "s"+strconv.Itoa(i), "/p", "m1", "r"+strconv.Itoa(i), "opus", 1, 1, 0, 0)+"\n")
	}
	in, db := ingester(t, root)
	ctx := context.Background()
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}

	for i := 0; i < 8; i++ {
		in.Ensure(true)
		zone := time.UTC
		if i%2 == 1 {
			zone = shanghai
		}
		if _, err := in.Rezone(ctx, zone); err != nil {
			t.Fatalf("rezone: %v", err)
		}
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		if _, running := in.Status(); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a pass is still running half a minute later")
		}
		time.Sleep(5 * time.Millisecond)
	}
	in.RunNow(ctx)

	// The last zone set was Shanghai, and every transcript has to be labelled
	// in it: one still carrying the UTC day is one that was read while the
	// zone was being changed and will never be read again.
	days, err := db.UsageByDay(ctx, store.UsageFilter{})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	total := int64(0)
	for _, d := range days {
		if d.Day != "2026-08-21" {
			t.Errorf("%d requests are labelled %s, which is the zone that was left behind",
				d.Requests, d.Day)
		}
		total += d.Requests
	}
	if total != files {
		t.Errorf("counted %d requests, want %d", total, files)
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

// The frontend has its own copy of MinInterval and they have to agree.
//
// The spend footer used to read `输出 51.5M · 近 30 天 · 11秒钟前读的`, and by
// this constant's own definition eleven seconds is *current* — so the panel was
// announcing a scan it had just run rather than saying anything about the
// figures. It now says the age only past this window, which makes the number a
// mirror of this one: too small and the footer flickers on and off between
// passes, too large and a genuinely stale reading claims to be fresh.
func TestTheSpendFreshnessWindowAgreesWithTheFrontend(t *testing.T) {
	const path = "../../web/src/components/panels/ago.ts"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := regexp.MustCompile(`SPEND_CURRENT_MS = ([0-9_]+)`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s no longer declares SPEND_CURRENT_MS, so nothing pins the two together", path)
	}
	got, err := strconv.Atoi(strings.ReplaceAll(m[1], "_", ""))
	if err != nil {
		t.Fatalf("SPEND_CURRENT_MS is not a number: %q", m[1])
	}
	if want := int(MinInterval / time.Millisecond); got != want {
		t.Errorf("SPEND_CURRENT_MS is %d ms and usage.MinInterval is %d ms. "+
			"The panel decides a reading is worth dating from the frontend "+
			"number and decides when to take a new one from this one; drift "+
			"between them shows as a footer that flickers, or as a stale "+
			"figure with nothing saying so.", got, want)
	}
}
