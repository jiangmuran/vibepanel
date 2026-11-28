package store

import (
	"context"
	"testing"
)

func put(t *testing.T, db *DB, path, tool string, rows ...UsageRow) {
	t.Helper()
	if err := db.ReplaceUsageFile(context.Background(), UsageFile{
		Path: path, Tool: tool, Size: int64(len(path)), ModifiedAt: 1, Rows: rows,
	}); err != nil {
		t.Fatalf("replace %s: %v", path, err)
	}
}

func row(day, session, cwd, model string, in, out int64) UsageRow {
	return UsageRow{Day: day, Session: session, CWD: cwd, Model: model,
		Input: in, Output: out, Requests: 1}
}

// Reading a transcript again must replace what it said, not add to it.
//
// This is the property that lets the ingest cursor be a whole file rather than
// a byte offset: a transcript that grew is re-read from the start, so its
// earlier records arrive a second time. Without the delete they land on top of
// themselves and every active session's numbers climb on every pass — slowly,
// plausibly, and only for the sessions somebody is actually using.
func TestRereadingATranscriptReplacesItsRowsRatherThanAddingToThem(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude", row("2026-08-20", "s1", "/p", "opus", 10, 20))
	put(t, db, "/t/a.jsonl", "claude",
		row("2026-08-20", "s1", "/p", "opus", 10, 20),
		row("2026-08-21", "s1", "/p", "opus", 5, 5))

	days, err := db.UsageByDay(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("got %d days, want 2: %+v", len(days), days)
	}
	if days[0].Output != 20 {
		t.Errorf("2026-08-20 output %d, want 20; the re-read was added to the first read",
			days[0].Output)
	}
}

// A transcript that is gone keeps what it counted.
//
// It used to take its numbers with it, and Claude Code deletes transcripts
// after thirty days by default: the month view and the year heatmap emptied
// themselves a month at a time. A retired file keeps its rows, is marked so the
// pass never tries to read it, and survives the rebuild a zone change asks for,
// because there is nothing left to rebuild it from.
func TestARetiredTranscriptKeepsItsNumbers(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude", row("2026-08-20", "s1", "/p", "opus", 10, 20))
	put(t, db, "/t/b.jsonl", "claude", row("2026-08-20", "s2", "/p", "opus", 1, 2))
	if err := db.RetireUsageFiles(ctx, []string{"/t/a.jsonl"}); err != nil {
		t.Fatalf("retire: %v", err)
	}

	days, err := db.UsageByDay(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 || days[0].Output != 22 {
		t.Fatalf("after a.jsonl went the day is %+v, want 22 output tokens; what it counted went with it", days)
	}
	stamps, err := db.UsageStamps(ctx)
	if err != nil {
		t.Fatalf("stamps: %v", err)
	}
	if !stamps["/t/a.jsonl"].Gone || stamps["/t/b.jsonl"].Gone {
		t.Errorf("stamps %+v: only a.jsonl is gone", stamps)
	}

	if _, err := db.ForgetEveryUsageFile(ctx); err != nil {
		t.Fatalf("forget every: %v", err)
	}
	days, err = db.UsageByDay(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 || days[0].Output != 20 {
		t.Errorf("after a rebuild the day is %+v, want a.jsonl's 20 kept and b.jsonl's to be re-read", days)
	}
}

// A request is held by the transcript that counted it, and a retired
// transcript keeps holding it, so a copy elsewhere stays uncounted after the
// original is deleted.
func TestARequestIsHeldByOneTranscript(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.ReplaceUsageFile(ctx, UsageFile{Path: "/t/a.jsonl", Tool: "claude", Keys: []int64{1, 2}}); err != nil {
		t.Fatalf("put a: %v", err)
	}
	held, err := db.UsageKeysHeldElsewhere(ctx, "/t/b.jsonl", []int64{2, 3})
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if !held[2] || held[3] {
		t.Errorf("held for b = %v, want only 2", held)
	}
	if held, _ := db.UsageKeysHeldElsewhere(ctx, "/t/a.jsonl", []int64{1, 2}); len(held) != 0 {
		t.Errorf("a's own keys reported as held elsewhere: %v", held)
	}
	if err := db.RetireUsageFiles(ctx, []string{"/t/a.jsonl"}); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if held, _ := db.UsageKeysHeldElsewhere(ctx, "/t/b.jsonl", []int64{2}); !held[2] {
		t.Error("a retired transcript stopped holding its requests; a copy of one would now count again")
	}
	// Many keys: SQLite's parameter limit is why the query is chunked.
	many := make([]int64, 2500)
	for i := range many {
		many[i] = int64(i + 10)
	}
	if err := db.ReplaceUsageFile(ctx, UsageFile{Path: "/t/c.jsonl", Tool: "claude", Keys: many}); err != nil {
		t.Fatalf("put c: %v", err)
	}
	if held, err := db.UsageKeysHeldElsewhere(ctx, "/t/d.jsonl", many); err != nil || len(held) != len(many) {
		t.Errorf("held %d of %d keys (err %v)", len(held), len(many), err)
	}
}

// The project filter is a directory, and a directory is not a string prefix.
//
// /home/me/api and /home/me/api-v2 share nine characters. A prefix match on
// the bare path folds the second project's spend into the first, and the
// number that results is wrong in the direction nobody checks: it is bigger,
// and bigger is what you expect when you have been busy.
func TestTheProjectFilterDoesNotMatchASiblingDirectory(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude", row("2026-08-20", "s1", "/home/me/api", "opus", 10, 20))
	put(t, db, "/t/b.jsonl", "claude", row("2026-08-20", "s2", "/home/me/api-v2", "opus", 100, 200))
	put(t, db, "/t/c.jsonl", "claude", row("2026-08-20", "s3", "/home/me/api/web", "opus", 1, 2))

	days, err := db.UsageByDay(ctx, UsageFilter{CWDPrefix: "/home/me/api"})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 {
		t.Fatalf("got %d days, want 1", len(days))
	}
	if days[0].Output != 22 {
		t.Errorf("output %d, want 22 (the project and its subdirectory, not the sibling)",
			days[0].Output)
	}
}

// A path with a LIKE wildcard in it must filter to itself and nothing else.
//
// Directories really are named things like `100%-coverage`, and a filter built
// as a LIKE pattern from that path matches every sibling whose name starts
// with `100`. Not hypothetical enough to skip: the filter value is a project
// path the user chose.
func TestAWildcardInAProjectPathIsNotAWildcard(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude", row("2026-08-20", "s1", "/w/100%cov", "opus", 1, 1))
	put(t, db, "/t/b.jsonl", "claude", row("2026-08-20", "s2", "/w/100xcov", "opus", 50, 50))

	days, err := db.UsageByDay(ctx, UsageFilter{CWDPrefix: "/w/100%cov"})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 || days[0].Output != 1 {
		t.Errorf("got %+v; the %% was treated as a wildcard and matched a sibling", days)
	}
}

// A project path with a non-ASCII character in it still matches the work done
// below it.
//
// SQLite's substr() counts characters, Go's len() counts bytes, and the two
// only agree while every byte is ASCII. Passing the byte length asked for more
// characters than the prefix has, so the comparison was false for every row
// inside the directory and only the project root itself -- matched by the
// separate `cwd = ?` clause -- survived. The reading that reaches a wall is
// then a confident zero, which is the one answer this surface exists to
// refuse.
func TestANonASCIIProjectPathMatchesItsSubdirectories(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude", row("2026-08-20", "s1", "/home/me/项目/api", "opus", 1, 1))
	put(t, db, "/t/b.jsonl", "claude", row("2026-08-20", "s2", "/home/me/项目/api/web", "opus", 10, 20))
	put(t, db, "/t/c.jsonl", "claude", row("2026-08-20", "s3", "/home/me/项目/other", "opus", 100, 200))

	days, err := db.UsageByDay(ctx, UsageFilter{CWDPrefix: "/home/me/项目/api"})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 {
		t.Fatalf("got %d days, want 1", len(days))
	}
	if days[0].Output != 21 {
		t.Errorf("output %d, want 21 (the project and its subdirectory, not the sibling)",
			days[0].Output)
	}
}

// Months are cut from the local-zone day string, never re-parsed as a date.
//
// strftime('%Y-%m', day) would have SQLite read the string as a UTC date and
// hand back the previous month for the first of every month for anyone east of
// Greenwich — a total that is right and a monthly chart that is off by one bar.
func TestMonthsComeFromTheLocalDayString(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude",
		row("2026-08-01", "s1", "/p", "opus", 1, 1),
		row("2026-08-31", "s1", "/p", "opus", 1, 1),
		row("2026-09-01", "s1", "/p", "opus", 1, 1))

	months, err := db.UsageByMonth(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("by month: %v", err)
	}
	if len(months) != 2 {
		t.Fatalf("got %d months, want 2: %+v", len(months), months)
	}
	if months[0].Day != "2026-08" || months[0].Requests != 2 {
		t.Errorf("first month %+v, want 2026-08 with 2 requests", months[0])
	}
}

// A tool whose transcripts were all read and held nothing must still appear,
// with its file count, so the panel can say "read, and empty" instead of
// leaving a reader to guess between that and "never looked".
func TestAToolWithNoSpendStillReportsWhatWasRead(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude", row("2026-08-20", "s1", "/p", "opus", 1, 1))
	put(t, db, "/t/empty.jsonl", "codex")

	byTool, err := db.UsageByTool(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("by tool: %v", err)
	}
	codex, ok := byTool["codex"]
	if !ok {
		t.Fatal("codex is absent although one of its transcripts was read; " +
			"the panel cannot tell that apart from codex never having been looked for")
	}
	if codex.Files != 1 || codex.Total() != 0 {
		t.Errorf("codex reports %+v, want one file read and nothing spent", codex)
	}
}

// The session table is capped, and the caller is told what it did not get.
// A list that silently ends is the file browser's truncation bug again.
func TestTheSessionListSaysHowManyItLeftOut(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		put(t, db, "/t/"+string(rune('a'+i))+".jsonl", "claude",
			row("2026-08-20", "s"+string(rune('a'+i)), "/p", "opus", int64(i+1), 1))
	}
	got, total, err := db.UsageBySession(ctx, UsageFilter{}, 2)
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want the limit of 2", len(got))
	}
	if total != 5 {
		t.Errorf("total %d, want 5; a capped list that does not say so implies it is complete", total)
	}
	if got[0].Input < got[1].Input {
		t.Error("the capped list is not the biggest sessions, so the cap hides the wrong ones")
	}
}

// An excluded directory takes its subdirectories with it, and nothing that
// merely shares its name as a prefix.
func TestAnExcludedDirectoryIsADirectoryNotAPrefix(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude", row("2026-08-20", "s1", "/w", "opus", 0, 1))
	put(t, db, "/t/b.jsonl", "claude", row("2026-08-20", "s2", "/w/api", "opus", 0, 10))
	put(t, db, "/t/c.jsonl", "claude", row("2026-08-20", "s3", "/w/api/web", "opus", 0, 100))
	put(t, db, "/t/d.jsonl", "claude", row("2026-08-20", "s4", "/w/api-v2", "opus", 0, 1000))

	days, err := db.UsageByDay(ctx, UsageFilter{CWDPrefix: "/w", Exclude: []string{"/w/api/"}})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 || days[0].Output != 1001 {
		t.Errorf("got %+v, want output 1001: the directory and its sibling, "+
			"not the excluded directory or anything below it", days)
	}
}

func TestDailyModelBucketsKeepTheActualDayAndRespectEveryFilter(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	put(t, db, "/t/claude.jsonl", "claude",
		row("2026-11-27", "a", "/work", "opus", 10, 3),
		row("2026-11-28", "a", "/work", "opus", 20, 7),
		row("2026-11-28", "b", "/work/docs", "sonnet", 30, 11),
		row("2026-11-28", "c", "/work/nested", "opus", 100, 100),
		row("2026-11-28", "d", "/work-other", "opus", 200, 200))
	put(t, db, "/t/codex.jsonl", "codex",
		row("2026-11-28", "e", "/work", "gpt-5-codex", 40, 13))

	f := UsageFilter{From: "2026-11-28", To: "2026-11-28", Tool: "claude",
		CWDPrefix: "/work", Exclude: []string{"/work/nested"}}
	got, err := db.UsageByDayModel(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Day != "2026-11-28" || got[0].Model != "opus" ||
		got[0].Input != 20 || got[0].Output != 7 || got[1].Model != "sonnet" || got[1].Output != 11 {
		t.Fatalf("filtered daily models: %+v", got)
	}

	all, err := db.UsageByDayModel(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	days, err := db.UsageByDay(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range days {
		var sum UsageTotals
		for _, bucket := range all {
			if bucket.Day == day.Day {
				sum.Input += bucket.Input
				sum.Output += bucket.Output
				sum.CacheRead += bucket.CacheRead
				sum.CacheWrite += bucket.CacheWrite
				sum.Requests += bucket.Requests
			}
		}
		if sum != day.UsageTotals {
			t.Fatalf("%s: model buckets %+v disagree with daily totals %+v", day.Day, sum, day.UsageTotals)
		}
	}
	empty, err := db.UsageByDayModel(ctx, UsageFilter{From: "2027-01-01"})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty breakdown must be an array: %+v, %v", empty, err)
	}
}
