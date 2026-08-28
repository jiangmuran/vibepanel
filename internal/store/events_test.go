package store

import (
	"context"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
)

func TestTheFlowSeriesFillsTheQuietBuckets(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	// Midnight, so the buckets line up with the hours below whatever time the
	// suite happens to run at.
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	for _, at := range []int64{start + 3600, start + 3600 + 60, start + 5*3600} {
		if err := db.RecordSessionEvent(ctx, SessionEvent{
			At: at, SessionID: "s1", ProjectID: "p1",
			From: session.StateWorking, To: session.StateDone,
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := db.SessionEventSeries(ctx, start, start+6*3600, 3600, EventScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("six hours asked for, %d buckets back", len(rows))
	}
	// The gaps are the shape of the day. A series built only from the buckets
	// that have rows in them is a chart with no quiet afternoons in it.
	want := []int{0, 2, 0, 0, 0, 1}
	for i, n := range want {
		if rows[i].Finished != n {
			t.Errorf("bucket %d finished %d, want %d", i, rows[i].Finished, n)
		}
		if rows[i].At != start+int64(i*3600) {
			t.Errorf("bucket %d is stamped %d, want %d", i, rows[i].At, start+int64(i*3600))
		}
	}
}

// An empty log is zeroes, not an error.
//
// SUM over no rows is NULL in SQLite rather than 0, and a scan into an int
// fails on it -- which is the shape of a panel on its first morning, exactly
// when somebody is looking at the wall they have just hung.
func TestCountingAnEmptyLogIsZeroRatherThanAFailure(t *testing.T) {
	db := openTest(t)
	got, err := db.CountSessionEvents(context.Background(), 0, EventScope{})
	if err != nil {
		t.Fatalf("counting an empty log failed: %v", err)
	}
	if got.Started != 0 || got.Finished != 0 || got.WaitEnded != 0 {
		t.Errorf("an empty log counted %+v", got)
	}
}

func TestTheLogIsNarrowedByScope(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	now := time.Now().Unix()
	for _, ev := range []SessionEvent{
		{At: now, SessionID: "a", ProjectID: "p1", From: session.StateWorking, To: session.StateDone},
		{At: now, SessionID: "b", ProjectID: "p2", From: session.StateWorking, To: session.StateDone},
		{At: now, SessionID: "c", ProjectID: "p2", From: session.StateWorking, To: session.StateDone},
	} {
		if err := db.RecordSessionEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		scope EventScope
		want  int
	}{
		{"the whole panel", EventScope{}, 3},
		{"one project", EventScope{ProjectID: "p2"}, 2},
		{"one session", EventScope{SessionID: "a"}, 1},
		// A session id wins over a project id, because a session-scoped link is
		// about one session and nothing else.
		{"a session inside a project", EventScope{ProjectID: "p2", SessionID: "b"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.CountSessionEvents(ctx, 0, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			if got.Finished != tc.want {
				t.Errorf("counted %d, want %d", got.Finished, tc.want)
			}
		})
	}
}

// The dwell is what makes the queue answerable, and a negative one would
// subtract from an average.
func TestANegativeDwellIsStoredAsZero(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.RecordSessionEvent(ctx, SessionEvent{
		At: time.Now().Unix(), SessionID: "a", ProjectID: "p",
		From: session.StateWaiting, To: session.StateWorking, ForSeconds: -900,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := db.CountSessionEvents(ctx, 0, EventScope{})
	if err != nil {
		t.Fatal(err)
	}
	if got.WaitSeconds != 0 {
		t.Errorf("a clock that went backwards stored %ds of waiting", got.WaitSeconds)
	}
}

// A session deleted out from under the log leaves the log alone.
//
// Deliberately no foreign key: a cascade here would rewrite a chart that has
// already been looked at, silently, on a screen with nobody in front of it.
func TestDeletingASessionDoesNotDeleteYesterday(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	p, err := db.CreateProject(ctx, "p1", "p", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.CreateSession(ctx, Session{
		ID: "s1", ProjectID: p.ID, TmuxName: "vp_x", Title: "one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordSessionEvent(ctx, SessionEvent{
		At: time.Now().Unix(), SessionID: sess.ID, ProjectID: p.ID,
		From: session.StateWorking, To: session.StateDone,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.CountSessionEvents(ctx, 0, EventScope{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Finished != 1 {
		t.Errorf("deleting a session removed %d of its events from the log", 1-got.Finished)
	}
}
