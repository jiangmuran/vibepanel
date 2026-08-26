package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// An UPDATE that changes nothing does not write anything.
//
// This pins a property of SQLite rather than of this package, which is worth
// the file because a comment in internal/httpapi now reasons from it. The
// poller calls UpdateSessionRuntime for every live session on every two-second
// tick, and the obvious reading — `UPDATE sessions SET cwd = ?, command = ?`
// has no value comparison, so at two dozen sessions that is twenty-four writes
// a second at idle, forever, to the disk the projects live on — was written
// down as a finding and is wrong. Measured, with two instruments that agree:
//
//	1000 identical calls: data_version +0, WAL +0 bytes
//	1000 changing calls:  WAL +4,120,032 bytes, one page each
//
// If a future SQLite stops eliding these, this fails and the guard the finding
// asked for is worth adding after all. That is the only reason to keep it.
func TestAnUpdateThatChangesNothingDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	p, err := db.CreateProject(ctx, "prj1", "p", dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := db.CreateSession(ctx, Session{ID: "ses1", ProjectID: p.ID, TmuxName: "vp_x", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionRuntime(ctx, s.ID, "/tmp", "bash"); err != nil {
		t.Fatal(err)
	}

	// data_version moves when *another* connection commits, so it answers "did
	// a write happen" without depending on the WAL file's size, which
	// auto-checkpointing resets underneath a naive reading. The first attempt
	// at this measured the WAL directly and read 0 -> 0 after a thousand
	// writes, which proved nothing at all.
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	dataVersion := func() int64 {
		var v int64
		if err := other.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	// Positive control. Without it, an instrument that never moves would read
	// as SQLite eliding every write, which is the conclusion under test.
	ctrl0 := dataVersion()
	if err := db.UpdateSessionRuntime(ctx, s.ID, "/other", "zsh"); err != nil {
		t.Fatal(err)
	}
	if dataVersion() == ctrl0 {
		t.Fatal("data_version did not move for a write that changed the values; " +
			"the instrument is broken and everything below it would be meaningless")
	}
	if err := db.UpdateSessionRuntime(ctx, s.ID, "/tmp", "bash"); err != nil {
		t.Fatal(err)
	}

	const n = 500
	before := dataVersion()
	for i := 0; i < n; i++ {
		if err := db.UpdateSessionRuntime(ctx, s.ID, "/tmp", "bash"); err != nil {
			t.Fatal(err)
		}
	}
	if after := dataVersion(); after != before {
		t.Errorf("%d identical updates moved data_version %d -> %d; SQLite is no longer "+
			"eliding them, so the poller really is writing on every tick and the "+
			"comment in internal/httpapi that says otherwise is now wrong", n, before, after)
	}

	// Second instrument. Auto-checkpointing off, or the file is truncated out
	// from under the comparison.
	if _, err := db.SQL().ExecContext(ctx, "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	walSize := func() int64 {
		fi, serr := os.Stat(path + "-wal")
		if serr != nil {
			return 0
		}
		return fi.Size()
	}
	w0 := walSize()
	for i := 0; i < n; i++ {
		if err := db.UpdateSessionRuntime(ctx, s.ID, "/tmp", "bash"); err != nil {
			t.Fatal(err)
		}
	}
	w1 := walSize()
	for i := 0; i < n; i++ {
		cwd := "/tmp/a"
		if i%2 == 1 {
			cwd = "/tmp/b"
		}
		if err := db.UpdateSessionRuntime(ctx, s.ID, cwd, "bash"); err != nil {
			t.Fatal(err)
		}
	}
	w2 := walSize()
	if w1 != w0 {
		t.Errorf("%d identical updates grew the WAL by %d bytes", n, w1-w0)
	}
	if w2 <= w1 {
		t.Errorf("%d updates that changed the row grew the WAL by %d bytes; the "+
			"comparison above is not measuring anything", n, w2-w1)
	}
}
