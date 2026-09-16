package usage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// claudeLine writes one assistant record the way Claude Code writes it.
func claudeLine(ts, session, cwd, msgID, reqID, model string, in, out, cw, cr int64) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":%q,"cwd":%q,`+
		`"requestId":%q,"message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,`+
		`"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`,
		ts, session, cwd, reqID, msgID, model, in, out, cw, cr)
}

func read(t *testing.T, tool Tool, body string) File {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	s := &Scanner{Loc: time.UTC}
	f := s.ReadFile(tool, path)
	if f.Problem != "" {
		t.Fatalf("read: %s", f.Problem)
	}
	return f
}

func total(f File) Counts {
	var c Counts
	for _, b := range f.Buckets {
		c.Add(b.Counts)
	}
	return c
}

// One API response is written as one line per content block, all carrying the
// same usage object.
//
// Measured on a real 89 MB transcript before this existed: 13,869 usage-bearing
// lines for 6,563 actual requests, and 14,118,636 "output tokens" against a
// true 5,954,333 — an over-count of 2.37x, in the direction that flatters.
// Nothing about the inflated number looks wrong, which is why it needs a test
// rather than an eye.
func TestOneResponseWithSeveralBlocksIsCountedOnce(t *testing.T) {
	const ts = "2026-08-23T12:00:00.000Z"
	body := strings.Join([]string{
		claudeLine(ts, "s1", "/p", "msg_1", "req_1", "opus", 2, 240, 900, 26000),
		claudeLine(ts, "s1", "/p", "msg_1", "req_1", "opus", 2, 240, 900, 26000),
		claudeLine(ts, "s1", "/p", "msg_1", "req_1", "opus", 2, 240, 900, 26000),
	}, "\n") + "\n"

	got := total(read(t, ToolClaude, body))
	if got.Requests != 1 {
		t.Errorf("three lines of one response counted as %d requests, want 1", got.Requests)
	}
	if got.Output != 240 {
		t.Errorf("output %d, want 240; the same usage object was added once per content block",
			got.Output)
	}
}

// The second shape of duplicate, and the one a sliding window cannot see.
//
// A resumed Claude Code session replays its entire history back into the same
// transcript, so records already counted reappear far later in the file. On
// this machine 57,296 duplicates are adjacent and 466 sit exactly 1,787
// usage-lines apart. A window of any affordable size passes the adjacent case
// and silently double-counts the replayed prefix — which is why the seen-set
// covers the whole file, and why the ingest cursor is a whole file rather than
// a byte offset.
func TestAReplayedTranscriptPrefixIsNotCountedTwice(t *testing.T) {
	var lines []string
	first := claudeLine("2026-08-23T12:00:00.000Z", "s1", "/p", "msg_a", "req_a", "opus", 10, 100, 0, 0)
	lines = append(lines, first)
	for i := 0; i < 40; i++ {
		lines = append(lines, claudeLine("2026-08-23T12:00:00.000Z", "s1", "/p",
			fmt.Sprintf("msg_f%d", i), fmt.Sprintf("req_f%d", i), "opus", 1, 1, 0, 0))
	}
	// The replay: the very first record again, long after it scrolled out of
	// any window somebody might have been tempted to keep.
	lines = append(lines, first)

	got := total(read(t, ToolClaude, strings.Join(lines, "\n")+"\n"))
	if got.Output != 100+40 {
		t.Errorf("output %d, want %d; the replayed prefix was counted a second time",
			got.Output, 140)
	}
}

func codexTokenCount(ts string, in, cached, cw, out int64) string {
	return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"token_count",`+
		`"info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,`+
		`"cache_write_input_tokens":%d,"output_tokens":%d}}}}`, ts, in, cached, cw, out)
}

func codexMeta(ts, session, cwd string) string {
	return fmt.Sprintf(`{"type":"session_meta","timestamp":%q,"payload":`+
		`{"session_id":%q,"cwd":%q}}`, ts, session, cwd)
}

// Codex reports input_tokens *including* the cached part; Claude reports it
// excluding. Adding the raw fields into one column makes Codex look like it
// re-sent its whole context uncached on every turn: this machine's largest
// rollout would read as 52.4M fresh input tokens where 50.7M of them were
// cache reads.
func TestCodexInputExcludesTheCachedPart(t *testing.T) {
	body := strings.Join([]string{
		codexMeta("2026-08-27T09:00:00.000Z", "c1", "/p"),
		codexTokenCount("2026-08-27T09:00:01.000Z", 12334, 3840, 0, 263),
	}, "\n") + "\n"

	got := total(read(t, ToolCodex, body))
	if got.Input != 12334-3840 {
		t.Errorf("input %d, want %d; the cached part was counted as fresh input",
			got.Input, 12334-3840)
	}
	if got.CacheRead != 3840 {
		t.Errorf("cacheRead %d, want 3840", got.CacheRead)
	}
	if got.Total() != 12334+263 {
		t.Errorf("total %d, want %d; the split must not change the sum", got.Total(), 12334+263)
	}
}

// Codex re-emits token_count with an unchanged last_token_usage when only the
// rate limits moved. Summing `last` over this machine's largest rollout gives
// 53,309,297 tokens where Codex's own final total_token_usage says 52,519,697.
// Differencing the running total reproduces that figure exactly, which is the
// check that settled which field to read.
func TestCodexCountsTheDeltaOfItsRunningTotal(t *testing.T) {
	body := strings.Join([]string{
		codexMeta("2026-08-27T09:00:00.000Z", "c1", "/p"),
		codexTokenCount("2026-08-27T09:00:01.000Z", 1000, 0, 0, 100),
		// The same event again: a rate-limit refresh, no new request.
		codexTokenCount("2026-08-27T09:00:02.000Z", 1000, 0, 0, 100),
		codexTokenCount("2026-08-27T09:00:03.000Z", 2500, 0, 0, 250),
	}, "\n") + "\n"

	got := total(read(t, ToolCodex, body))
	if got.Output != 250 {
		t.Errorf("output %d, want 250 — the running total was summed instead of differenced",
			got.Output)
	}
	if got.Requests != 2 {
		t.Errorf("requests %d, want 2; the repeated event was counted as a third", got.Requests)
	}
}

// If a running total ever goes backwards, the answer is the new total counted
// once. Not observed on 138 rollouts — compaction does not reset it — but the
// alternative is a negative token count on somebody's dashboard, and there is
// no reading of "minus four million tokens" that is less wrong.
func TestADecreasingCodexTotalIsNotANegativeNumber(t *testing.T) {
	body := strings.Join([]string{
		codexMeta("2026-08-27T09:00:00.000Z", "c1", "/p"),
		codexTokenCount("2026-08-27T09:00:01.000Z", 5000, 0, 0, 500),
		codexTokenCount("2026-08-27T09:00:02.000Z", 100, 0, 0, 10),
	}, "\n") + "\n"

	got := total(read(t, ToolCodex, body))
	if got.Output != 510 {
		t.Errorf("output %d, want 510", got.Output)
	}
	if got.Input < 0 || got.Output < 0 || got.CacheRead < 0 || got.CacheWrite < 0 {
		t.Errorf("a negative count reached the caller: %+v", got)
	}
}

// A day is the day the person lived through, not the UTC day.
//
// Transcripts stamp UTC. At UTC+8 a session that ran at 07:00 local on the
// 24th is stamped 23:00 on the 23rd, and bucketing by the raw stamp puts a
// whole working morning on the previous day — which is the shape of error
// nobody notices, because the total is right and only the bars moved.
func TestDaysAreLocalDaysNotUTC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	body := claudeLine("2026-08-23T23:30:00.000Z", "s1", "/p", "m", "r", "opus", 1, 1, 0, 0) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	east := time.FixedZone("UTC+8", 8*3600)
	f := (&Scanner{Loc: east}).ReadFile(ToolClaude, path)
	if len(f.Buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(f.Buckets))
	}
	if f.Buckets[0].Day != "2026-08-24" {
		t.Errorf("day %q, want 2026-08-24; the UTC stamp was used as the local day",
			f.Buckets[0].Day)
	}
}

// One enormous record must not end the file.
//
// A transcript line is a whole message and a message carries whatever a command
// printed; the longest on this machine is 5,756,466 bytes. bufio.Scanner's
// token limit stops the *file* rather than the line, so one oversized tool
// result would silently discard every request after it — with a plausible
// number left on screen and nothing saying half the day was thrown away.
func TestAnOverlongRecordIsSkippedAndTheRestIsStillRead(t *testing.T) {
	huge := `{"type":"assistant","message":{"usage":{"x":"` +
		strings.Repeat("y", maxLine+16) + `"}}}`
	body := strings.Join([]string{
		claudeLine("2026-08-23T12:00:00.000Z", "s1", "/p", "m1", "r1", "opus", 1, 10, 0, 0),
		huge,
		claudeLine("2026-08-23T12:00:01.000Z", "s1", "/p", "m2", "r2", "opus", 1, 20, 0, 0),
	}, "\n") + "\n"

	f := read(t, ToolClaude, body)
	if got := total(f); got.Output != 30 {
		t.Errorf("output %d, want 30; the records after the oversized line were lost", got.Output)
	}
	if f.Skipped != 1 {
		t.Errorf("skipped %d, want 1; a record that could not be read must be reported, "+
			"or the totals are a lower bound that claims to be exact", f.Skipped)
	}
}

// A missing agent is unknown, never zero.
//
// The whole point of the panel. If Codex is not installed, "Codex: 0 tokens" is
// a claim about Codex; the truth is a claim about this machine, and the two
// have to look different on screen.
func TestAMissingTranscriptDirectoryIsUnknownNotZero(t *testing.T) {
	s := &Scanner{ClaudeRoot: filepath.Join(t.TempDir(), "nope"), Loc: time.UTC}
	refs, src, err := s.Walk(ToolClaude)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("found %d transcripts under a directory that does not exist", len(refs))
	}
	if src.Found {
		t.Error("a directory that does not exist reported Found; the UI would render a zero")
	}
	if src.Problem == "" {
		t.Error("nothing said why the directory could not be read")
	}
}

// The one place the panel reads outside its own data directory.
//
// A symlink inside the transcript tree pointing anywhere else must not be
// followed. Not because a transcript is dangerous, but because "walk this
// directory" quietly becoming "walk wherever a link points" is how a reader
// scoped to one directory ends up reading a home directory.
//
// Both shapes, because they are stopped by different things and only one of
// them is stopped by this package. A symlinked *directory* is not descended
// into by filepath.WalkDir at all — that is a library default reached by
// writing nothing, so it is asserted here rather than assumed. A symlinked
// *file* is reported by the walk like any other entry, and it is the
// IsRegular check that refuses it; the first version of this test used only a
// linked directory, so removing that check changed nothing and the test still
// passed.
func TestTheWalkDoesNotFollowALinkOutOfItsRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A link that ends in .jsonl, so nothing but the file-type check stands
	// between the walk and a file outside the root.
	if err := os.Symlink(filepath.Join(outside, "secret.jsonl"),
		filepath.Join(root, "linked.jsonl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	refs, _, err := (&Scanner{ClaudeRoot: root, Loc: time.UTC}).Walk(ToolClaude)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, r := range refs {
		if strings.Contains(r.Path, "secret") || strings.Contains(r.Path, "linked") {
			t.Fatalf("the walk followed a symlink out of its root and reached %s", r.Path)
		}
	}
	if len(refs) != 1 {
		t.Fatalf("got %d transcripts (%v), want just the one real file inside the root",
			len(refs), refs)
	}
}

// A file with no usage at all is a real answer, and it must be a row rather
// than an absence: without it the cursor never records the file and every pass
// reads it again forever.
func TestATranscriptWithNoUsageStillProducesAStamp(t *testing.T) {
	f := read(t, ToolClaude, `{"type":"user","message":{"content":"hi"}}`+"\n")
	if len(f.Buckets) != 0 {
		t.Errorf("got %d buckets from a transcript with no usage", len(f.Buckets))
	}
	if f.Size == 0 || f.ModifiedAt == 0 {
		t.Errorf("no stamp recorded: size %d modified %d", f.Size, f.ModifiedAt)
	}
}

// ─── the measurement ──────────────────────────────────────────────────────

// TestFullPassCost is how the numbers in the comments above and in migration
// v10 were obtained, kept so they can be obtained again.
//
// Skipped unless VIBEPANEL_USAGE_BENCH is set, because it reads whatever
// transcripts happen to be in the running user's home directory — which is
// nothing on CI and gigabytes on a machine somebody works on. A test whose
// result depends on the developer's history is not a gate; this one is a
// stopwatch with a place to live.
func TestFullPassCost(t *testing.T) {
	if os.Getenv("VIBEPANEL_USAGE_BENCH") == "" {
		t.Skip("set VIBEPANEL_USAGE_BENCH=1 to measure a pass over this machine's transcripts")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	s := DefaultScanner(home)
	for _, tool := range Tools {
		refs, src, err := s.Walk(tool)
		if err != nil {
			t.Fatalf("walk %s: %v", tool, err)
		}
		if !src.Found {
			t.Logf("%s: %s", tool, src.Problem)
			continue
		}
		started := time.Now()
		var buckets, skipped int
		for _, ref := range refs {
			f := s.ReadFile(tool, ref.Path)
			buckets += len(f.Buckets)
			skipped += f.Skipped
		}
		el := time.Since(started)
		t.Logf("%s: %d files, %.2f MB, read in %v (%.0f MB/s), %d buckets, %d skipped",
			tool, src.Files, float64(src.Bytes)/(1<<20), el.Round(time.Millisecond),
			float64(src.Bytes)/(1<<20)/el.Seconds(), buckets, skipped)
	}
}

// TestIngestPassCost measures the three passes that matter: the first one, the
// one where nothing has changed, and the one where a single active transcript
// has been appended to. The gap between the first and the second is what the
// (size, mtime) cursor buys.
//
// Same skip as above, and for the same reason.
func TestIngestPassCost(t *testing.T) {
	if os.Getenv("VIBEPANEL_USAGE_BENCH") == "" {
		t.Skip("set VIBEPANEL_USAGE_BENCH=1 to measure ingest against this machine's transcripts")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close() //nolint:errcheck // temp file

	in := &Ingester{Scanner: DefaultScanner(home), DB: db}
	cold := in.RunNow(context.Background())
	t.Logf("cold:  %d/%d files read in %v", cold.Read, cold.Seen, cold.Duration.Round(time.Millisecond))

	idle := in.RunNow(context.Background())
	t.Logf("idle:  %d/%d files read in %v", idle.Read, idle.Seen, idle.Duration.Round(time.Millisecond))

	// Pretend the largest transcript has just been appended to, which is what
	// an active session looks like to the cursor.
	refs, _, err := in.Scanner.Walk(ToolClaude)
	if err != nil || len(refs) == 0 {
		t.Skip("no Claude transcripts to touch")
	}
	biggest := refs[0]
	for _, r := range refs {
		if r.Size > biggest.Size {
			biggest = r
		}
	}
	if err := db.ForgetUsageFiles(context.Background(), []string{biggest.Path}); err != nil {
		t.Fatalf("forget: %v", err)
	}
	one := in.RunNow(context.Background())
	t.Logf("one %.0f MB transcript changed: %d/%d files read in %v",
		float64(biggest.Size)/(1<<20), one.Read, one.Seen, one.Duration.Round(time.Millisecond))
}

// claudeStreamLine is a line written while a response is still streaming: the
// usage object is there, and its output_tokens is a placeholder.
func claudeStreamLine(ts, msgID, reqID string, out int64, stop string) string {
	stopJSON := "null"
	if stop != "" {
		stopJSON = fmt.Sprintf("%q", stop)
	}
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":"s1","cwd":"/p",`+
		`"requestId":%q,"message":{"id":%q,"model":"opus","stop_reason":%s,`+
		`"usage":{"input_tokens":6,"output_tokens":%d,"cache_creation_input_tokens":300,`+
		`"cache_read_input_tokens":40000}}}`, ts, reqID, msgID, stopJSON, out)
}

// The lines of one streamed response do not agree, and the first is the wrong
// one to keep.
//
// The shape is copied from a real transcript of September 2026: a thinking
// block and three tool calls each written with output_tokens 2, and the last
// line, the one with a stop_reason, carrying 352. Keeping the first line is how
// a month that produced 25.1M output tokens was reported as 13.1M.
func TestAStreamedResponseCountsItsFinalOutputNotItsPlaceholder(t *testing.T) {
	const ts = "2026-09-15T01:53:34.641Z"
	body := strings.Join([]string{
		claudeStreamLine(ts, "msg_1", "req_1", 2, ""),
		claudeStreamLine(ts, "msg_1", "req_1", 2, ""),
		claudeStreamLine(ts, "msg_1", "req_1", 2, ""),
		claudeStreamLine(ts, "msg_1", "req_1", 352, "tool_use"),
	}, "\n") + "\n"

	got := total(read(t, ToolClaude, body))
	if got.Output != 352 {
		t.Errorf("output %d, want 352; a streaming placeholder was kept instead of the "+
			"figure the response finished with", got.Output)
	}
	if got.Requests != 1 {
		t.Errorf("requests %d, want 1", got.Requests)
	}
	if got.Input != 6 || got.CacheRead != 40000 || got.CacheWrite != 300 {
		t.Errorf("the other fields moved: %+v", got)
	}
}

// The final figure wins wherever it sits in the file, including before a
// replayed placeholder. A resumed session copies its history back into the
// transcript, so "the last line" is not a rule that can be relied on.
func TestTheFinalOutputWinsWhateverOrderTheLinesAreIn(t *testing.T) {
	const ts = "2026-09-15T01:53:34.641Z"
	body := strings.Join([]string{
		claudeStreamLine(ts, "msg_1", "req_1", 905, "tool_use"),
		claudeStreamLine(ts, "msg_2", "req_2", 10, "end_turn"),
		claudeStreamLine(ts, "msg_1", "req_1", 3, ""),
	}, "\n") + "\n"

	got := total(read(t, ToolClaude, body))
	if got.Output != 915 {
		t.Errorf("output %d, want 915; a later placeholder overwrote a final figure", got.Output)
	}
}

// Claude Code's own "<synthetic>" records carry a usage object of zeros: an
// interrupted turn, an error shown in the transcript. No request reached a
// model, and counting one moves "tokens per request" for nothing.
func TestASyntheticRecordWithNoTokensIsNotARequest(t *testing.T) {
	const ts = "2026-09-15T01:53:34.641Z"
	body := strings.Join([]string{
		claudeLine(ts, "s1", "/p", "msg_1", "req_1", "opus", 1, 10, 0, 0),
		claudeLine(ts, "s1", "/p", "msg_2", "", "<synthetic>", 0, 0, 0, 0),
	}, "\n") + "\n"

	f := read(t, ToolClaude, body)
	if got := total(f); got.Requests != 1 {
		t.Errorf("requests %d, want 1; a record that spent nothing was counted", got.Requests)
	}
	for _, b := range f.Buckets {
		if b.Model == "<synthetic>" {
			t.Errorf("a bucket was written for a model that never ran: %+v", b)
		}
	}
}

// Lines of one request that disagree about more than output keep one line's
// usage whole. A request on this machine has (8 out, 139533 cache read, 2058
// cache write) on one line and (1624, 131527, 0) on the other; taking each
// field's maximum reported a request neither line describes.
func TestADisagreeingRequestKeepsOneLinesUsageWhole(t *testing.T) {
	const ts = "2026-09-10T08:00:00.000Z"
	body := strings.Join([]string{
		claudeLine(ts, "s1", "/p", "msg_1", "req_1", "opus", 2, 8, 2058, 139533),
		claudeLine(ts, "s1", "/p", "msg_1", "req_1", "opus", 2, 1624, 0, 131527),
	}, "\n") + "\n"

	got := total(read(t, ToolClaude, body))
	want := Counts{Input: 2, Output: 1624, CacheRead: 131527, CacheWrite: 0, Requests: 1}
	if got != want {
		t.Errorf("got %+v, want %+v: the fields of two different lines were combined", got, want)
	}
}

func codexForkMeta(ts, id, session, forkedFrom, mode, cwd string) string {
	return fmt.Sprintf(`{"type":"session_meta","timestamp":%q,"payload":`+
		`{"id":%q,"session_id":%q,"forked_from_id":%q,"cwd":%q,"history_mode":%q}}`,
		ts, id, session, forkedFrom, cwd, mode)
}

func codexTurn(ts, model string) string {
	return fmt.Sprintf(`{"type":"turn_context","timestamp":%q,"payload":{"model":%q,"cwd":"/p"}}`,
		ts, model)
}

// A forked Codex thread replays its parent's token counts, and they are the
// parent's spend, already counted in the parent's own rollout.
//
// The shape is the one on this machine: the fork's one session_meta, the
// parent's running totals in the same second with no turn_context before them,
// and then the fork's first turn, whose total carries on from the parent's.
// Differencing the replay counted six forks' parents again: 366M tokens, on the
// day of the fork, under no model.
func TestAForkedCodexThreadDoesNotCountItsParentsHistory(t *testing.T) {
	body := strings.Join([]string{
		codexForkMeta("2026-07-19T11:26:32.300Z", "fork", "parent", "parent", "legacy", "/p"),
		codexTokenCount("2026-07-19T11:26:32.369Z", 5000, 0, 0, 838),
		codexTokenCount("2026-07-19T11:26:32.370Z", 80000000, 78000000, 0, 159469),
		codexTokenCount("2026-07-19T11:26:32.371Z", 82000000, 80000000, 0, 159469),
		codexTurn("2026-07-19T11:26:33.000Z", "gpt-6"),
		codexTokenCount("2026-07-19T11:26:40.000Z", 82010000, 80008000, 0, 159569),
	}, "\n") + "\n"

	f := read(t, ToolCodex, body)
	got := total(f)
	if got.Total() != 10000+100 {
		t.Errorf("total %d, want %d; the parent's replayed history was counted as the fork's",
			got.Total(), 10100)
	}
	if got.Requests != 1 {
		t.Errorf("requests %d, want 1", got.Requests)
	}
	for _, b := range f.Buckets {
		if b.Model == "" {
			t.Errorf("a bucket with no model: %+v", b)
		}
	}
}

// Holding counts back until the first turn is only for a fork. A rollout that
// was not forked counts from its first event, turn_context or not: an older
// Codex that never wrote one would otherwise report nothing at all.
func TestAnUnforkedCodexThreadCountsBeforeAnyTurnContext(t *testing.T) {
	body := strings.Join([]string{
		codexMeta("2026-07-19T11:00:00.000Z", "c1", "/p"),
		codexTokenCount("2026-07-19T11:00:01.000Z", 1000, 0, 0, 100),
	}, "\n") + "\n"

	if got := total(read(t, ToolCodex, body)); got.Total() != 1100 {
		t.Errorf("total %d, want 1100; an unforked thread was treated as a replay", got.Total())
	}
}

// A subagent in the newer "paginated" mode copies no counts. It carries
// forked_from_id like a fork, then its parent's session_meta, and nothing may
// be held back from it -- including, for a subagent that never got as far as
// a turn_context, the counts it did write.
func TestAPaginatedCodexSubagentCountsItsOwnWork(t *testing.T) {
	for name, lines := range map[string][]string{
		"with a turn": {
			codexForkMeta("2026-09-07T01:01:14.679Z", "child", "parent", "parent", "paginated", "/p"),
			codexForkMeta("2026-09-07T01:01:14.680Z", "parent", "parent", "", "paginated", "/p"),
			codexTurn("2026-09-07T01:01:14.680Z", "gpt-6"),
			codexTokenCount("2026-09-07T01:01:21.003Z", 16137, 0, 0, 68),
		},
		"without one": {
			codexForkMeta("2026-09-07T01:01:14.679Z", "child", "parent", "parent", "paginated", "/p"),
			codexForkMeta("2026-09-07T01:01:14.680Z", "parent", "parent", "", "paginated", "/p"),
			codexTokenCount("2026-09-07T01:01:21.003Z", 16137, 0, 0, 68),
		},
	} {
		body := strings.Join(lines, "\n") + "\n"
		if got := total(read(t, ToolCodex, body)); got.Total() != 16205 {
			t.Errorf("%s: total %d, want 16205", name, got.Total())
		}
	}
}

// Two lines with the same output and different cache figures: the later line
// is the one kept, which is the tie rule the reader states.
func TestATiedRequestKeepsTheLaterLine(t *testing.T) {
	const ts = "2026-09-10T08:00:00.000Z"
	body := strings.Join([]string{
		claudeLine(ts, "s1", "/p", "msg_1", "req_1", "opus", 2, 50, 100, 1000),
		claudeLine(ts, "s1", "/p", "msg_1", "req_1", "opus", 2, 50, 0, 2000),
	}, "\n") + "\n"

	got := total(read(t, ToolClaude, body))
	if got.CacheRead != 2000 || got.CacheWrite != 0 {
		t.Errorf("got %+v, want the second line's cache figures", got)
	}
}

// Only the first session_meta says what a file is. A subagent writes its own
// and then its parent's, which names no fork; if a replaying thread ever
// carried its parent's meta the same way, the last one must not decide that it
// replays nothing.
func TestTheFirstSessionMetaDecidesWhetherAThreadReplays(t *testing.T) {
	body := strings.Join([]string{
		codexForkMeta("2026-07-19T11:26:32.300Z", "fork", "parent", "parent", "legacy", "/p"),
		codexForkMeta("2026-07-19T11:26:32.300Z", "parent", "parent", "", "legacy", "/p"),
		codexTokenCount("2026-07-19T11:26:32.369Z", 80000000, 78000000, 0, 159469),
		codexTurn("2026-07-19T11:26:33.000Z", "gpt-6"),
		codexTokenCount("2026-07-19T11:26:40.000Z", 80010000, 78008000, 0, 159569),
	}, "\n") + "\n"

	if got := total(read(t, ToolCodex, body)); got.Total() != 10100 {
		t.Errorf("total %d, want 10100; a later session_meta overrode the first", got.Total())
	}
}
