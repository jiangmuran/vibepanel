package store

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// What the scrollback archive costs on disk, at the size it is designed for.
//
// The bound is two numbers -- 2,000 lines and 256 KiB per session -- and both
// were chosen against a budget rather than picked. This is the budget, asserted
// rather than described: two dozen sessions, every one of them at the byte cap,
// which is the worst case the design admits.
//
// A test rather than a note in a comment because the cost is a property of the
// storage, not of the capture: putting the blob on the sessions row instead of
// in its own table would not change any of these bytes, and would drag every
// one of them through ListSessions on every poll tick. That change would pass
// every other test in this package.
func TestTheScrollbackArchiveFitsItsBudget(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "cost.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	proj, err := db.CreateProject(ctx, "cost", "cost", dir)
	if err != nil {
		t.Fatal(err)
	}

	const sessions = 24
	const perSession = 256 << 10
	blob := bytes.Repeat([]byte("x"), perSession)

	start := time.Now()
	for i := range sessions {
		id := fmt.Sprintf("s%02d", i)
		if _, err := db.CreateSession(ctx, Session{
			ID: id, ProjectID: proj.ID, TmuxName: "vp_" + id,
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.PutScrollback(ctx, Scrollback{
			SessionID: id, CapturedAt: 1, Lines: 2000, Content: blob,
		}); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)

	rows, stored, err := db.ScrollbackBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows != sessions {
		t.Fatalf("archived %d sessions, want %d", rows, sessions)
	}

	// 6 MiB of content. Anything much above that means a cap stopped biting.
	const budget = int64(sessions) * perSession
	if stored != budget {
		t.Errorf("the archive holds %d bytes for %d sessions; the per-session cap is %d",
			stored, sessions, perSession)
	}

	// And on disk, where the overhead lives. Checkpointed first: without it the
	// pages are in the WAL and the main file reads as almost empty, which is a
	// measurement of nothing.
	if _, err := db.SQL().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("24 sessions at the byte cap: %d bytes of content, %d bytes of database, "+
		"written in %v", stored, info.Size(), elapsed)

	// A generous ceiling. The point is to fail loudly if the storage ever grows
	// a multiple -- a history of captures rather than one row per session would
	// do exactly that, and would look correct from every other angle.
	const ceiling = 3 * budget
	if info.Size() > ceiling {
		t.Errorf("the database is %d bytes for %d bytes of archived scrollback; something is "+
			"keeping more than one capture per session", info.Size(), stored)
	}
}
