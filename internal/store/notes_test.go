package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Two tabs saving the same note at the same instant: the loser is told the
// note is stale, never that the database is locked.
//
// The revision check used to be a SELECT and then an INSERT inside one
// deferred transaction. database/sql opens that with a bare `begin`, so the
// read took a WAL snapshot and the write had to upgrade the lock afterwards --
// and SQLite does not run the busy handler for an upgrade inside an open read
// transaction, so the DSN's busy_timeout does not apply on this one path. The
// loser got SQLITE_BUSY instead of ErrNoteStale, which the handler turns into
// a 500: no conflict dialog, no refreshed base revision, and the raw driver
// string in the notes panel's status line.
//
// TestNoteRevisionCatchesWritesInTheSameSecond is strictly sequential, so
// nothing in the suite could see this. It takes real concurrency, and only a
// handful of rounds to show up.
func TestEveryLoserOfANoteRaceIsToldItIsStale(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	const pid = "p-notes-race"
	if _, err := db.CreateProject(ctx, pid, "notes", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	const rounds, writers = 40, 4
	for i := 0; i < rounds; i++ {
		base, err := db.GetNote(ctx, pid)
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		errs := make([]error, writers)
		start := make(chan struct{})
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				<-start
				_, errs[w] = db.SetNoteIfUnchanged(ctx, pid, "from writer", base.Rev)
			}(w)
		}
		close(start)
		wg.Wait()

		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrNoteStale):
			default:
				t.Fatalf("round %d: a losing writer got %v, want ErrNoteStale; "+
					"the panel shows that string instead of offering the conflict", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("round %d: %d of %d writers won the same revision", i, won, writers)
		}
	}
}

// The global note is the same race as the project note, and it meets it more
// often: it is reachable from every screen, so two tabs holding it is the
// ordinary case rather than the unlucky one.
//
// It kept the SELECT-then-write inside one deferred transaction after
// SetNoteIfUnchanged stopped using it, so the loser of two simultaneous saves
// got SQLITE_BUSY where it should have been told the note is stale — HTTP 500
// and a driver string in the notes panel instead of the conflict the revision
// counter exists to offer.
func TestEveryLoserOfAGlobalNoteRaceIsToldItIsStale(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	const rounds, writers = 40, 4
	for i := 0; i < rounds; i++ {
		base, err := db.GetGlobalNote(ctx)
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		errs := make([]error, writers)
		start := make(chan struct{})
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				<-start
				_, errs[w] = db.SetGlobalNoteIfUnchanged(ctx, "from writer", base.Rev)
			}(w)
		}
		close(start)
		wg.Wait()

		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrNoteStale):
			default:
				t.Fatalf("round %d: a losing writer got %v, want ErrNoteStale; "+
					"the panel shows that string instead of offering the conflict", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("round %d: %d of %d writers won the same revision", i, won, writers)
		}
	}
}
