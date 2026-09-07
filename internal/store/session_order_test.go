package store

import (
	"context"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
)

// Two agents in one project, both working, both printing, must hold still.
//
// This is the reported defect, and it needed no special conditions: open two
// Claude Code sessions in the same project and watch the sidebar swap them
// about once a second, forever.
//
// The mechanism is entirely in the tie-break. Both rows carry the same state
// and no manual position, so the order fell to last_output_at, which the PTY
// pump stamps at most once a second per session — each on whatever phase its
// own output happens to land on, and at one-second resolution. So for part of
// every second one of the two holds the newer stamp and overtakes the other,
// and for the rest they are equal and fall back to age. Nothing about either
// session has changed; the list moves anyway.
//
// It is not only visual. The poller compares the serialised snapshot against
// the last one to decide whether to push, and the array *order* is part of
// that — so this also turned every other poll into a broadcast to every
// viewer.
func TestABusySessionDoesNotOvertakeItsNeighbour(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := db.CreateSession(ctx, Session{
			ID: id, ProjectID: "p", TmuxName: "vp_" + id, State: session.StateWorking,
		}); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}
	// b was opened after a. Age is the last tie-break, so this is what the
	// order settles to — and what a stamp arriving for a used to jump it above.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE sessions SET created_at = ? WHERE id = 'a'`, now()-120); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE sessions SET created_at = ? WHERE id = 'b'`, now()-60); err != nil {
		t.Fatal(err)
	}

	order := func() string {
		rows, err := db.ListSessions(ctx)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("got %d sessions, want 2", len(rows))
		}
		return rows[0].ID + rows[1].ID
	}

	// Ten seconds of two agents printing, sampled either side of each stamp.
	// a's output lands early in the second and b's late, which is the ordinary
	// case: they were started at different moments and neither is synchronised
	// to anything.
	seen := make([]string, 0, 20)
	base := time.Now().Unix()
	// Both have printed before the first sample. A session's *first* output is
	// a real change of order and belongs at the top of the list; what this test
	// is about is the steady state after that.
	for _, id := range []string{"a", "b"} {
		if err := db.TouchSessionOutput(ctx, id, base-1); err != nil {
			t.Fatal(err)
		}
	}
	for sec := base; sec < base+10; sec++ {
		if err := db.TouchSessionOutput(ctx, "a", sec); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, order())
		if err := db.TouchSessionOutput(ctx, "b", sec); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, order())
	}
	for _, got := range seen {
		if got != seen[0] {
			t.Fatalf("the sidebar reordered itself while nothing but the clock changed: %v", seen)
		}
	}
}

// A session that has gone quiet still sinks below one that is producing
// output. The bucket is there to stop the list twitching, not to stop it
// ordering: without this the fix above would be indistinguishable from
// deleting the term.
func TestALongSilentSessionSinksBelowABusyOne(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, id := range []string{"busy", "silent"} {
		if _, err := db.CreateSession(ctx, Session{
			ID: id, ProjectID: "p", TmuxName: "vp_" + id, State: session.StateDone,
		}); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}
	// The silent one is the *newer* row, so age alone would put it first. Only
	// the quiet-time term can push it down.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE sessions SET created_at = ? WHERE id = 'busy'`, now()-3600); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchSessionOutput(ctx, "busy", now()); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchSessionOutput(ctx, "silent", now()-3600); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if rows[0].ID != "busy" {
		t.Errorf("first session = %q, want the one that printed a moment ago", rows[0].ID)
	}
}
