package store

import (
	"context"
	"errors"
	"testing"
)

// The global note survives its project having never existed.
//
// notes.project_id is `REFERENCES projects(id) ON DELETE CASCADE`, so the
// obvious implementation -- a reserved id in that table -- needs a fake
// project row to point at and is deleted with it. This is the test that says
// the note is not in that table.
func TestTheGlobalNoteNeedsNoProject(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	n, err := db.GetGlobalNote(ctx)
	if err != nil {
		t.Fatalf("GetGlobalNote on an empty database: %v", err)
	}
	if n.Content != "" || n.Rev != 0 {
		t.Errorf("a database that never had one gave %+v, want a blank note at rev 0", n)
	}

	saved, err := db.SetGlobalNote(ctx, "kept")
	if err != nil {
		t.Fatalf("SetGlobalNote: %v", err)
	}
	if saved.Rev != 1 || saved.Content != "kept" {
		t.Errorf("first write gave %+v", saved)
	}
	if saved.ProjectID != GlobalNoteID {
		t.Errorf("ProjectID = %q, want %q -- the editor keys its state by this",
			saved.ProjectID, GlobalNoteID)
	}

	back, err := db.GetGlobalNote(ctx)
	if err != nil || back.Content != "kept" {
		t.Errorf("read back %+v, %v", back, err)
	}
}

// Two tabs, and the second one is told rather than silently winning.
//
// The reason this matters more here than for a project note: the global note
// is reachable from every screen, so having it open twice is the ordinary case
// and not the unlucky one.
func TestTheGlobalNoteRefusesAStaleWrite(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	first, err := db.SetGlobalNoteIfUnchanged(ctx, "one", 0)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := db.SetGlobalNoteIfUnchanged(ctx, "two", first.Rev); err != nil {
		t.Fatalf("a write based on the current revision: %v", err)
	}
	// The tab that loaded before either write.
	if _, err := db.SetGlobalNoteIfUnchanged(ctx, "three", first.Rev); !errors.Is(err, ErrNoteStale) {
		t.Errorf("a write based on an old revision gave %v, want ErrNoteStale", err)
	}
	now, _ := db.GetGlobalNote(ctx)
	if now.Content != "two" {
		t.Errorf("the stale write landed anyway: %q", now.Content)
	}
}

// A project's note and the global one are different notes.
func TestTheGlobalNoteIsNotAProjectNote(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	p, err := db.CreateProject(ctx, "p1", "p", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetNote(ctx, p.ID, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetGlobalNote(ctx, "global"); err != nil {
		t.Fatal(err)
	}
	pn, _ := db.GetNote(ctx, p.ID)
	gn, _ := db.GetGlobalNote(ctx)
	if pn.Content != "project" || gn.Content != "global" {
		t.Errorf("they share storage: project=%q global=%q", pn.Content, gn.Content)
	}
}
