package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/session"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db.CreateProject(ctx, "p1", "Proj", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	db.Close()

	// Reopening must not wipe or re-apply anything: an upgrade that dropped the
	// user's projects on restart would be catastrophic and silent.
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db.Close()

	v, err := db.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d, want %d", v, schemaVersion)
	}
	ps, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(ps) != 1 || ps[0].ID != "p1" {
		t.Fatalf("projects after reopen = %+v, want the one created before", ps)
	}
}

func TestSessionOrderMatchesStateSortWeight(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// The ORDER BY in listSessions hard-codes the state ranking as a CASE
	// expression. If someone reorders session.AllStates or changes SortWeight
	// without touching the SQL, the sidebar silently sorts by the old rules.
	// This test is the thing that catches that.
	for i, st := range []session.State{session.StateDone, session.StateWorking, session.StateWaiting} {
		_, err := db.CreateSession(ctx, Session{
			ID: string(rune('a' + i)), ProjectID: "p",
			TmuxName: "vp_" + string(rune('a'+i)), State: st,
		})
		if err != nil {
			t.Fatalf("CreateSession %v: %v", st, err)
		}
	}

	got, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}

	gotStates := make([]session.State, len(got))
	for i, s := range got {
		gotStates[i] = s.State
	}
	wantStates := append([]session.State(nil), session.AllStates...)
	sort.SliceStable(wantStates, func(i, j int) bool {
		return wantStates[i].SortWeight() < wantStates[j].SortWeight()
	})
	for i := range wantStates {
		if gotStates[i] != wantStates[i] {
			t.Fatalf("SQL order %v disagrees with State.SortWeight order %v", gotStates, wantStates)
		}
	}
}

func TestPinnedSessionBeatsState(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	mk := func(id string, st session.State) {
		if _, err := db.CreateSession(ctx, Session{
			ID: id, ProjectID: "p", TmuxName: "vp_" + id, State: st,
		}); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}
	mk("waiting", session.StateWaiting)
	mk("done", session.StateDone)

	// Explicit user intent outranks the automatic ranking; that is the whole
	// point of a pin.
	if err := db.SetSessionPinned(ctx, "done", true); err != nil {
		t.Fatalf("SetSessionPinned: %v", err)
	}
	got, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if got[0].ID != "done" {
		t.Errorf("first session = %q, want the pinned one", got[0].ID)
	}
}

func TestAutoTitleDoesNotOverrideManual(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := db.CreateSession(ctx, Session{ID: "s", ProjectID: "p", TmuxName: "vp_s"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := db.SetSessionTitle(ctx, "s", "from pane title", TitleAuto); err != nil {
		t.Fatalf("auto title: %v", err)
	}
	if s, _ := db.GetSession(ctx, "s"); s.Title != "from pane title" {
		t.Fatalf("auto title not applied: %q", s.Title)
	}

	if err := db.SetSessionTitle(ctx, "s", "my name", TitleManual); err != nil {
		t.Fatalf("manual title: %v", err)
	}
	// The shell keeps setting its own title forever. Once the user has renamed
	// the tab, those updates must stop landing.
	if err := db.SetSessionTitle(ctx, "s", "bash", TitleAuto); err != nil {
		t.Fatalf("later auto title: %v", err)
	}
	s, err := db.GetSession(ctx, "s")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.Title != "my name" {
		t.Errorf("title = %q, want the manual name to survive", s.Title)
	}
}

func TestStateChangedAtOnlyMovesOnChange(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := db.CreateSession(ctx, Session{
		ID: "s", ProjectID: "p", TmuxName: "vp_s", State: session.StateWorking,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := db.SetSessionState(ctx, "s", session.StateWaiting, session.SourceHook); err != nil {
		t.Fatalf("SetSessionState: %v", err)
	}
	first, _ := db.GetSession(ctx, "s")

	// Re-reporting the same state is what a polling heuristic does constantly.
	// If that bumped the timestamp, the UI's "waiting for 12 minutes" would
	// reset to zero on every tick and become useless.
	if err := db.SetSessionState(ctx, "s", session.StateWaiting, session.SourceHeuristic); err != nil {
		t.Fatalf("SetSessionState repeat: %v", err)
	}
	second, _ := db.GetSession(ctx, "s")
	if second.StateChangedAt != first.StateChangedAt {
		t.Errorf("state_changed_at moved on a no-op update: %d -> %d",
			first.StateChangedAt, second.StateChangedAt)
	}
}

func TestSetSessionStateRejectsGarbage(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := db.CreateSession(ctx, Session{ID: "s", ProjectID: "p", TmuxName: "vp_s"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Hook payloads are written by users and arrive over HTTP. An unvalidated
	// value here would put a state in the database that no client can render.
	if err := db.SetSessionState(ctx, "s", session.State("banana"), session.SourceHook); err == nil {
		t.Fatal("expected an error for an invalid state")
	}
}

func TestDeleteProjectCascades(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := db.CreateSession(ctx, Session{ID: "s", ProjectID: "p", TmuxName: "vp_s"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := db.DeleteProject(ctx, "p"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	// Requires PRAGMA foreign_keys=ON to be applied per connection; a leftover
	// session row pointing at a missing project would break the sidebar.
	if _, err := db.GetSession(ctx, "s"); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived project deletion: %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if v, err := db.GetSetting(ctx, "theme", "system"); err != nil || v != "system" {
		t.Fatalf("GetSetting default = %q, %v", v, err)
	}
	if err := db.SetSetting(ctx, "theme", "dark"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetSetting(ctx, "theme", "light"); err != nil {
		t.Fatalf("SetSetting overwrite: %v", err)
	}
	if v, _ := db.GetSetting(ctx, "theme", "system"); v != "light" {
		t.Errorf("theme = %q, want light", v)
	}
}

func TestNotFound(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.GetProject(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetProject(missing) = %v, want ErrNotFound", err)
	}
	if err := db.RenameProject(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RenameProject(missing) = %v, want ErrNotFound", err)
	}
}

func TestMigrationUpgradesAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	// Build a database at version 1 by applying only the first migration, the
	// way a release before the scratch-terminal change would have left it.
	raw, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrations[0](tx); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("set version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Data an existing user would have.
	if _, err := raw.Exec(
		`INSERT INTO projects (id, name, path, last_active_at, created_at) VALUES ('p1','Keep','/tmp',1,1)`,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO sessions (id, project_id, tmux_name, created_at) VALUES ('s1','p1','vp_s1',1)`,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	raw.Close()

	// Opening with the current build must upgrade in place, not start over.
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	v, err := db.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != schemaVersion {
		t.Errorf("version = %d, want %d", v, schemaVersion)
	}
	ps, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(ps) != 1 || ps[0].Name != "Keep" {
		t.Fatalf("projects after upgrade = %+v, want the existing one", ps)
	}
	s, err := db.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.ParentID != nil {
		t.Errorf("existing session gained a parent: %v", s.ParentID)
	}

	// And the new column works on the upgraded database.
	if _, err := db.CreateSession(ctx, Session{
		ID: "s2", ProjectID: "p1", TmuxName: "vp_s2", ParentID: strptr("s1"),
	}); err != nil {
		t.Fatalf("CreateSession with a parent: %v", err)
	}
	kids, err := db.ListChildSessions(ctx, "s1")
	if err != nil {
		t.Fatalf("ListChildSessions: %v", err)
	}
	if len(kids) != 1 || kids[0].ID != "s2" {
		t.Errorf("children = %+v, want one", kids)
	}
}

func strptr(s string) *string { return &s }

func TestMigrationIsAppliedOnlyOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "twice.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	db.Close()

	// A migration that re-ran would fail on the duplicate ALTER TABLE, or
	// worse, quietly recreate something.
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db.Close()
	ps, _ := db.ListProjects(ctx)
	if len(ps) != 1 {
		t.Errorf("projects after reopen = %d, want 1", len(ps))
	}
}
