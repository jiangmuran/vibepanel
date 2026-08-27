package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// The point of a revision rather than a timestamp: every interesting conflict
// happens inside one second, which is all the resolution updated_at has.
func TestNoteRevisionCatchesWritesInTheSameSecond(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	const pid = "p-notes"
	if _, err := db.CreateProject(ctx, pid, "notes", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	loaded, err := db.GetNote(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}

	first, err := db.SetNoteIfUnchanged(ctx, pid, "written by A", loaded.Rev)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if first.Rev != loaded.Rev+1 {
		t.Errorf("rev = %d, want %d", first.Rev, loaded.Rev+1)
	}

	// B loaded at the same moment as A and saves right behind it. Both writes
	// land in the same second, so a timestamp precondition would let this
	// through and A's text would be gone with nothing said.
	_, err = db.SetNoteIfUnchanged(ctx, pid, "written by B", loaded.Rev)
	if !errors.Is(err, ErrNoteStale) {
		t.Fatalf("second write: %v, want ErrNoteStale", err)
	}
	got, err := db.GetNote(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "written by A" {
		t.Errorf("content = %q; the stale write was applied", got.Content)
	}

	// Reloading is what makes the write legal, and it must then succeed.
	if _, err := db.SetNoteIfUnchanged(ctx, pid, "written by B", got.Rev); err != nil {
		t.Errorf("write after reloading: %v", err)
	}

	// An unconditional write still moves the revision, or a client holding the
	// old one would be told it is current.
	forced, err := db.SetNote(ctx, pid, "written by the CLI")
	if err != nil {
		t.Fatal(err)
	}
	if forced.Rev <= got.Rev+1 {
		t.Errorf("rev after an unconditional write = %d, want more than %d", forced.Rev, got.Rev+1)
	}
}

// A session changing state counts as its project being active.
//
// Projects are ordered by last_active_at, and that column was written in
// exactly one place: creating a session. "Most active first" therefore meant
// "most recently given a new session first", and a project with a session
// waiting for a human stayed wherever it was — which is the one thing the
// ordering exists to prevent.
func TestSessionStateChangeMakesItsProjectRecent(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	first, err := db.CreateProject(ctx, "p-first", "first", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Distinct timestamps: now() has second resolution, and two projects made
	// in the same second fall through to created_at, which is equally tied —
	// leaving the order arbitrary and the precondition below meaningless.
	time.Sleep(1100 * time.Millisecond)
	second, err := db.CreateProject(ctx, "p-second", "second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.CreateSession(ctx, Session{
		ID: "s-quiet", ProjectID: first.ID, TmuxName: "vp_quiet",
		State: session.StateWorking,
	})
	if err != nil {
		t.Fatal(err)
	}

	// `second` was created last, so it leads until something happens in `first`.
	before, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].ID != second.ID {
		t.Fatalf("expected the newest project first to begin with, got %q", before[0].Name)
	}

	time.Sleep(1100 * time.Millisecond)
	if err := db.SetSessionState(ctx, sess.ID, session.StateWaiting, session.SourceHook); err != nil {
		t.Fatal(err)
	}

	after, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].ID != first.ID {
		t.Errorf("the project with a session now waiting is at position %d, not the top",
			indexOfProject(after, first.ID))
	}
}

func indexOfProject(list []Project, id string) int {
	for i, p := range list {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// The first migration is frozen. Editing it is the bug this guards.
//
// The rule in AGENTS.md is "additive steps only, and never an edit to an
// earlier one", and the reason is that a released binary has already run the
// old version of that step on somebody's machine. Change schema.sql and new
// installs get the change while every existing database silently does not —
// the difference showing up later as a query failing somewhere you cannot see.
//
// A pinned hash is the whole check. It is not a value to update when the file
// changes: if this fails, the change belongs in a new migration instead.
//
// (An earlier attempt at this compared the schema of a fresh database against
// one upgraded from v1 and asserted they matched. They cannot differ: both
// paths run migrations[0], which *is* schema.sql, so the comparison was of a
// thing against itself. It passed with a column added to schema.sql, which is
// exactly the change it was meant to catch.)
func TestTheFirstMigrationIsFrozen(t *testing.T) {
	sum := sha256.Sum256([]byte(schemaSQL))
	const pinned = "6ba8e200b650f112b50981ee41bd4d0ea02757edd24d586b6b08d351f325c938"
	if got := hex.EncodeToString(sum[:]); got != pinned {
		t.Errorf("schema.sql has changed (%s).\n"+
			"Every database in the world has already run the previous version of it, and none of "+
			"them will run this one. Add a migration instead; if you are certain this file has "+
			"never shipped, update the pin.", got)
	}
}

// Concurrent writers must not fail, they must wait.
//
// SQLite takes one write lock for the whole database, so two writers collide
// by design; busy_timeout is what turns the collision into a wait instead of
// an error. The DSN sets it and a comment explains why, and nothing had ever
// put enough writers against it to find out whether five seconds is enough.
//
// The panel does this constantly without anybody thinking about it: the poller
// writes state for every session on a timer while a person renames a tab and a
// hook reports from inside a session.
func TestConcurrentWritersDoNotCollide(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	project, err := db.CreateProject(ctx, "p-busy", "busy", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 6; i++ {
		s, cerr := db.CreateSession(ctx, Session{
			ID: fmt.Sprintf("s-%d", i), ProjectID: project.ID,
			TmuxName: fmt.Sprintf("vp_%d", i), State: session.StateWorking,
		})
		if cerr != nil {
			t.Fatal(cerr)
		}
		ids = append(ids, s.ID)
	}

	var wg sync.WaitGroup
	// Non-blocking, with a count kept separately.
	//
	// A plain buffered channel deadlocks the moment there are more failures
	// than buffer: the workers block on the send, never see the stop signal,
	// and the test hangs instead of reporting. Which is what happened the first
	// time this was checked against a deliberately tiny busy_timeout — the
	// mutation that was supposed to prove the test works proved it hangs.
	var failures atomic.Int64
	errs := make(chan error, 64)
	record := func(e error) {
		failures.Add(1)
		select {
		case errs <- e:
		default:
		}
	}
	// Closed, not sent to. time.After delivers its value to exactly one
	// receiver, so a channel shared by twelve goroutines stops one of them
	// and leaves eleven spinning — which is how the first version of this
	// ran for four hundred seconds instead of two.
	stop := make(chan struct{})
	time.AfterFunc(2*time.Second, func() { close(stop) })

	// Writers of every kind the panel actually runs at once.
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := ids[n%len(ids)]
			for {
				select {
				case <-stop:
					return
				default:
				}
				switch n % 4 {
				case 0:
					if e := db.SetSessionState(ctx, id, session.StateWaiting, session.SourceHook); e != nil {
						record(fmt.Errorf("state: %w", e))
					}
				case 1:
					if e := db.SetSessionTitle(ctx, id, fmt.Sprintf("t%d", n), TitleManual); e != nil {
						record(fmt.Errorf("title: %w", e))
					}
				case 2:
					if e := db.TouchProject(ctx, project.ID); e != nil {
						record(fmt.Errorf("touch: %w", e))
					}
				case 3:
					if _, e := db.SetNote(ctx, project.ID, fmt.Sprintf("note %d", n)); e != nil {
						record(fmt.Errorf("note: %w", e))
					}
				}
			}
		}(w)
	}
	// And readers, which WAL is supposed to keep out of the way.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, e := db.ListSessions(ctx); e != nil {
					record(fmt.Errorf("list: %w", e))
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	var first error
	for e := range errs {
		if first == nil {
			first = e
		}
	}
	if n := failures.Load(); n > 0 {
		t.Errorf("%d concurrent operations failed; the first was: %v", n, first)
	}
}

func TestAutomaticOrderingKeepsTheArrangement(t *testing.T) {
	// Switching the sidebar to most-active-first used to run
	// `UPDATE projects SET sort_index = NULL`, so an arrangement somebody sat
	// down and made was destroyed by a clock icon with no confirmation — and
	// the icon then removed itself, because it only renders in manual mode, so
	// there was nothing left to click and no way back.
	//
	// Measured through the UI before the fix: four projects arranged
	// `delta bravo alpha charlie`, one click, `alpha bravo charlie delta`,
	// unrecoverable.
	//
	// The ordering and the positions are two things now, the same way a
	// panel's width and whether it is collapsed had to become two things.
	ctx := context.Background()
	db := openTest(t)

	ids := []string{"proj-a", "proj-b", "proj-c"}
	for i, pid := range ids {
		if _, err := db.CreateProject(ctx, pid, string(rune('a'+i)), t.TempDir()); err != nil {
			t.Fatalf("CreateProject %s: %v", pid, err)
		}
	}
	arranged := []string{ids[2], ids[0], ids[1]}
	if err := db.ReorderProjects(ctx, arranged); err != nil {
		t.Fatalf("ReorderProjects: %v", err)
	}
	if manual, _ := db.ProjectOrderIsManual(ctx); !manual {
		t.Fatal("arranging by hand did not select the manual ordering")
	}

	order := func() []string {
		ps, err := db.ListProjects(ctx)
		if err != nil {
			t.Fatalf("ListProjects: %v", err)
		}
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	same := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	if got := order(); !same(got, arranged) {
		t.Fatalf("after arranging, order = %v, want %v", got, arranged)
	}

	if err := db.SetProjectOrderManual(ctx, false); err != nil {
		t.Fatalf("SetProjectOrderManual(false): %v", err)
	}
	if got := order(); same(got, arranged) {
		t.Error("switching to automatic changed nothing, so this test cannot tell the " +
			"two orderings apart and proves nothing about the one below")
	}
	has, err := db.HasProjectOrder(ctx)
	if err != nil {
		t.Fatalf("HasProjectOrder: %v", err)
	}
	if !has {
		t.Fatal("switching ordering discarded the arrangement; there is nothing to go " +
			"back to, and the control that did it is no longer in the sidebar")
	}

	if err := db.SetProjectOrderManual(ctx, true); err != nil {
		t.Fatalf("SetProjectOrderManual(true): %v", err)
	}
	if got := order(); !same(got, arranged) {
		t.Errorf("going back gave %v, want the arrangement %v", got, arranged)
	}
}

func TestPurgingExpiredSignInsKeepsTheLiveOnes(t *testing.T) {
	// Nothing called this, so the table only grew: one row per sign-in that
	// stopped meaning anything thirty days earlier. Not a security hole —
	// AuthSessionByToken filters on `expires_at > ?`, so an expired row cannot
	// authenticate — but a table that only grows is still a table that only
	// grows.
	//
	// The half worth pinning is the second one: a purge that took a live
	// session with it would sign everybody out at every restart, which is a
	// far louder bug than the one being fixed.
	ctx := context.Background()
	db := openTest(t)

	u, err := db.CreateUser(ctx, "user-1", "someone", "argon2id-hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	live := []byte("live-token-hash")
	dead := []byte("dead-token-hash")
	if err := db.CreateAuthSession(ctx, live, u.ID, time.Hour, "browser", "127.0.0.1"); err != nil {
		t.Fatalf("CreateAuthSession live: %v", err)
	}
	if err := db.CreateAuthSession(ctx, dead, u.ID, -time.Hour, "browser", "127.0.0.1"); err != nil {
		t.Fatalf("CreateAuthSession expired: %v", err)
	}

	n, err := db.PurgeExpiredAuthSessions(ctx)
	if err != nil {
		t.Fatalf("PurgeExpiredAuthSessions: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d rows, want exactly the expired one", n)
	}
	if _, err := db.AuthSessionByToken(ctx, live); err != nil {
		t.Errorf("the live sign-in is gone after a purge: %v", err)
	}
	if _, err := db.AuthSessionByToken(ctx, dead); err == nil {
		t.Error("the expired sign-in is still there")
	}
}

// Migration v7 renames the two audit events that did not share a prefix.
//
// Driven directly rather than by opening an old database, because what matters
// is that the statement finds the rows: an UPDATE with a WHERE that matches
// nothing succeeds, so a migration with the spelling wrong is indistinguishable
// from one that worked.
func TestMigrationRenamesTheOldAuditEvents(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)

	for _, e := range []AuditEntry{
		{Event: "password_changed", Username: "u", IP: "1.2.3.4", Detail: "old spelling"},
		{Event: "password_change_refused", Username: "u", IP: "1.2.3.4", Detail: "old spelling"},
		{Event: "login.failed", Username: "u", IP: "1.2.3.4", Detail: "untouched"},
	} {
		if err := db.Audit(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.Event, err)
		}
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// v7, by number, and the number is asserted rather than assumed.
	//
	// This said "the last migration in the list is the one under test; indexing
	// by a literal would quietly start testing a different one" -- and had it
	// exactly backwards. Migrations are append-only, so `len-1` is the one that
	// moves: adding v8 pointed this test at a table creation and it failed
	// claiming the rename had not happened. A literal is the stable end.
	const auditRename = 7
	if len(migrations) < auditRename {
		t.Fatalf("there are %d migrations and this test wants v%d", len(migrations), auditRename)
	}
	if err := migrations[auditRename-1](tx); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows, err := db.RecentAudit(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range rows {
		seen[r.Event]++
	}
	if seen["password_changed"] != 0 || seen["password_change_refused"] != 0 {
		t.Errorf("the old spellings survived: %v. A history spelled two ways is worse than "+
			"either, because the GROUP BY this rename exists to fix still returns two rows "+
			"for one thing.", seen)
	}
	if seen["password.changed"] != 1 || seen["password.change_refused"] != 1 {
		t.Errorf("the renamed rows are not there: %v", seen)
	}
	if seen["login.failed"] != 1 {
		t.Errorf("the migration touched a row it should not have: %v", seen)
	}
}

// An older binary must refuse a newer database rather than open it.
//
// This is the rollback path, and it is the one place where being permissive
// destroys data silently: an old binary that opened a v9 database would read
// the tables it knows, ignore the columns it does not, and write rows back
// without them. Nothing would look wrong until somebody upgraded again and
// found the values gone.
//
// The check has a careful comment and had no test. Removing the guard lets this
// open a database from the future.
func TestAnOlderBinaryRefusesANewerDatabase(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(dir, "vibepanel.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	have, err := db.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if have != schemaVersion {
		t.Fatalf("a fresh database is at version %d, want %d", have, schemaVersion)
	}

	// A version from the future, the way a newer build would leave it.
	if _, err := db.SQL().ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion+1)); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = Open(ctx, filepath.Join(dir, "vibepanel.db"))
	if err == nil {
		t.Fatal("a database from a newer build opened without complaint; an older binary " +
			"reads the columns it knows and writes rows back without the rest")
	}
	// The message has to name both numbers and say what to do. An operator
	// reading it is mid-rollback and the panel is down.
	for _, want := range []string{
		fmt.Sprintf("%d", schemaVersion+1),
		fmt.Sprintf("%d", schemaVersion),
		"upgrade vibepanel",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}
