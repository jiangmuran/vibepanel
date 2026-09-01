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

// A transcript that is gone takes its numbers with it. Otherwise the totals
// become a record of every transcript that ever existed, which nothing on disk
// backs up any more.
func TestADeletedTranscriptTakesItsNumbersWithIt(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	put(t, db, "/t/a.jsonl", "claude", row("2026-08-20", "s1", "/p", "opus", 10, 20))
	put(t, db, "/t/b.jsonl", "claude", row("2026-08-20", "s2", "/p", "opus", 1, 2))
	if err := db.ForgetUsageFiles(ctx, []string{"/t/a.jsonl"}); err != nil {
		t.Fatalf("forget: %v", err)
	}

	days, err := db.UsageByDay(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	if len(days) != 1 || days[0].Output != 2 {
		t.Fatalf("after forgetting a.jsonl the day is %+v; its rows outlived it", days)
	}
	stamps, err := db.UsageStamps(ctx)
	if err != nil {
		t.Fatalf("stamps: %v", err)
	}
	if _, ok := stamps["/t/a.jsonl"]; ok {
		t.Error("the cursor still holds a transcript that was forgotten, so it will never be re-read")
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
