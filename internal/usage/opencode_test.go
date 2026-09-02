package usage

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// A cut-down opencode database: the two tables the reader is allowed to touch,
// and nothing else.
//
// The omission is the assertion. The real file also holds account.access_token,
// account.refresh_token and credential.value, and this fixture deliberately
// does not define those tables -- so a query that grows a `SELECT *` across a
// join, or reaches for a column "while we are in here", fails to compile its
// SQL against this schema rather than quietly loading somebody's OAuth tokens.
func opencodeDB(t *testing.T, dir string, msgs []opencodeMsg, sessions map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, dbFile)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test fixture
	for _, ddl := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, data TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	for id, dirName := range sessions {
		if _, err := db.Exec(`INSERT INTO session (id, directory) VALUES (?, ?)`, id, dirName); err != nil {
			t.Fatal(err)
		}
	}
	for i, m := range msgs {
		blob, err := json.Marshal(m.payload())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO message (id, session_id, data) VALUES (?, ?, ?)`,
			m.ID, m.Session, string(blob)); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	return path
}

type opencodeMsg struct {
	ID             string
	Session        string
	Role           string
	Model          string
	CreatedMillis  int64
	Input, Output  int64
	Reasoning      int64
	CacheR, CacheW int64
	OmitTokens     bool
	OmitTimestamp  bool
}

func (m opencodeMsg) payload() map[string]any {
	p := map[string]any{"role": m.Role, "modelID": m.Model}
	if !m.OmitTimestamp {
		p["time"] = map[string]any{"created": m.CreatedMillis}
	}
	if !m.OmitTokens {
		p["tokens"] = map[string]any{
			"input": m.Input, "output": m.Output, "reasoning": m.Reasoning,
			// The field opencode's own aggregation sums to, carried so the test
			// can assert the panel reproduces it rather than recomputing it the
			// same way twice.
			"total": m.Input + m.Output + m.Reasoning + m.CacheR + m.CacheW,
			"cache": map[string]any{"read": m.CacheR, "write": m.CacheW},
		}
	}
	return p
}

// TestOpencodeReadsItsLedger pins the four columns and what each one means.
//
// The reading that had to be got right is `input`. Codex's input_tokens
// *includes* the cached part and the reader subtracts it; opencode's does not,
// and applying the same subtraction here would report roughly a quarter of the
// spend with nothing failing anywhere. So the fixture gives a message a small
// fresh input beside a large cache read -- 109 against 8192 is what the real
// database looks like -- and the assertion is that they stay separate.
func TestOpencodeReadsItsLedger(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	// 2026-03-04 07:30 UTC is 15:30 on the 4th at UTC+8, and 2026-03-04 17:00
	// UTC is 01:00 on the *5th*. Two instants, two local days, one of which is
	// a different UTC day -- which is what makes this test about the zone.
	day4 := time.Date(2026, 3, 4, 7, 30, 0, 0, time.UTC).UnixMilli()
	day5 := time.Date(2026, 3, 4, 17, 0, 0, 0, time.UTC).UnixMilli()

	dir := t.TempDir()
	path := opencodeDB(t, dir,
		[]opencodeMsg{
			{ID: "m1", Session: "s1", Role: "assistant", Model: "gpt-x",
				CreatedMillis: day4, Input: 109, Output: 275, Reasoning: 115, CacheR: 8192},
			{ID: "m2", Session: "s1", Role: "assistant", Model: "gpt-x",
				CreatedMillis: day4, Input: 10, Output: 20, CacheW: 5},
			{ID: "m3", Session: "s1", Role: "assistant", Model: "gpt-x",
				CreatedMillis: day5, Input: 1, Output: 2},
			// Not a request: an assistant turn that spent nothing.
			{ID: "m4", Session: "s1", Role: "assistant", Model: "gpt-x", CreatedMillis: day4},
			// Not an assistant message -- and it carries a tokens object anyway.
			// It has to, or this row proves nothing: with an empty one it is
			// dropped by the zero-total branch and the role filter could be
			// deleted without failing anything. That is exactly what happened
			// the first time this test was written.
			{ID: "m5", Session: "s1", Role: "user", CreatedMillis: day4,
				Input: 7000, Output: 7000},
			// Undatable. Must be skipped and counted, never filed under today.
			{ID: "m6", Session: "s1", Role: "assistant", Model: "gpt-x",
				OmitTimestamp: true, Input: 999999},
		},
		map[string]string{"s1": "/home/someone/work"},
	)

	s := &Scanner{Loc: loc}
	res, err := readOpencode(path, s.loc())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.skipped != 1 {
		t.Fatalf("skipped=%d, want 1 (the message with no timestamp)", res.skipped)
	}

	// Summed per day, not indexed by it. Indexing kept one bucket per day and
	// silently dropped the others, so a row that landed under a *different*
	// model -- which is what the user message does, having none -- was invisible
	// to every assertion below. The role filter could be deleted with this test
	// still green.
	byDay := map[string]*Bucket{}
	for _, b := range res.buckets {
		d := byDay[b.Day]
		if d == nil {
			d = &Bucket{Day: b.Day, Model: b.Model, CWD: b.CWD}
			byDay[b.Day] = d
		}
		d.Counts.Add(b.Counts)
	}
	if len(byDay) != 2 {
		t.Fatalf("days=%v, want two local days", byDay)
	}
	if len(res.buckets) != 2 {
		t.Fatalf("buckets=%d, want 2 -- an extra one means something that is not "+
			"an assistant message was counted: %+v", len(res.buckets), res.buckets)
	}

	got, ok := byDay["2026-03-04"]
	if !ok || got == nil {
		t.Fatalf("no bucket for 2026-03-04; got %v", byDay)
	}
	// m1 + m2. Fresh input stays fresh: 109+10, not 109+10 minus the cache.
	if got.Input != 119 {
		t.Errorf("Input=%d, want 119 -- opencode's input excludes the cached part, "+
			"so nothing may be subtracted from it", got.Input)
	}
	// Reasoning is production and folds into output: (275+115) + 20.
	if got.Output != 410 {
		t.Errorf("Output=%d, want 410 (output plus reasoning)", got.Output)
	}
	if got.CacheRead != 8192 || got.CacheWrite != 5 {
		t.Errorf("cache read=%d write=%d, want 8192/5", got.CacheRead, got.CacheWrite)
	}
	if got.Requests != 2 {
		t.Errorf("Requests=%d, want 2 -- the empty assistant turn is not a request", got.Requests)
	}
	if got.CWD != "/home/someone/work" {
		t.Errorf("CWD=%q, want the session's directory", got.CWD)
	}
	if got.Model != "gpt-x" {
		t.Errorf("Model=%q", got.Model)
	}

	// The invariant the Codex reader is held to as well: what the panel reports
	// is what the agent itself wrote down. m1+m2 totals, summed from the field
	// opencode stores rather than recomputed.
	const stored = (109 + 275 + 115 + 8192) + (10 + 20 + 5)
	if got.Total() != stored {
		t.Errorf("Total()=%d, want %d -- the panel's total must be opencode's own",
			got.Total(), stored)
	}

	// The instant that crosses midnight at UTC+8 landed on the next local day.
	if _, ok := byDay["2026-03-05"]; !ok {
		t.Errorf("no bucket for 2026-03-05; the zone was not applied: %v", byDay)
	}
}

// TestOpencodeWalkFindsOneDatabase pins the shape of the walk: a directory
// listing found no ledger here once already, and concluded there was none.
func TestOpencodeWalkFindsOneDatabase(t *testing.T) {
	home := t.TempDir()
	s := DefaultScanner(home)
	root := s.Roots()[ToolOpencode]

	// No opencode at all.
	refs, src, err := s.Walk(ToolOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if src.Found || src.Problem != "not found" || len(refs) != 0 {
		t.Fatalf("absent: found=%v problem=%q refs=%d", src.Found, src.Problem, len(refs))
	}

	// Installed, but no ledger yet. "spent nothing" and "has no ledger" are
	// different claims and only one of them is ever true.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	refs, src, err = s.Walk(ToolOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if src.Problem != "no "+dbFile || len(refs) != 0 {
		t.Fatalf("no ledger: problem=%q refs=%d", src.Problem, len(refs))
	}

	// With one, exactly one Ref, carrying the cursor the pass skips it by.
	path := opencodeDB(t, root, nil, nil)
	refs, src, err = s.Walk(ToolOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Path != path {
		t.Fatalf("refs=%v, want exactly the database", refs)
	}
	if refs[0].Size <= 0 || refs[0].ModifiedAt <= 0 {
		t.Fatalf("ref carries no cursor: %+v", refs[0])
	}
	if !src.Found || src.Files != 1 || !src.Complete {
		t.Fatalf("source: found=%v files=%d complete=%v", src.Found, src.Files, src.Complete)
	}
}
