package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// TestSnapshotIsStableWhenNothingChanges pins the property the poll loop is
// built on: two snapshots taken with nothing happening in between must be the
// same bytes.
//
// The poller broadcasts only when the payload differs from the last one, and
// the comment there says why — "a tick that broadcasts regardless is polling
// again, just with the cost moved onto every connected viewer". That check is
// only worth anything if an unchanged panel really does serialise identically.
//
// It did not. The `live` array came from ranging over a map, so its order was
// reshuffled on every call, the byte comparison saw a difference every time,
// and an idle panel with two or more attached sessions pushed a full state
// snapshot to every viewer every two seconds forever. The saving the check
// exists to make was never made, and nothing failed loudly enough to say so —
// which is exactly why this is a test and not a comment.
func TestSnapshotIsStableWhenNothingChanges(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"stable"}`)

	// Four, because the bug is invisible with one and coin-flip with two.
	for i := 0; i < 4; i++ {
		sess := postJSON[store.Session](t, ts, "/api/sessions",
			`{"projectId":"`+project.ID+`","command":["sleep","600"]}`)
		if _, err := srv.Manager.Attach(ctx, sess.ID, sess.TmuxName, 80, 24); err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
	}

	var first []string
	for i := 0; i < 40; i++ {
		payload := srv.snapshot(ctx)
		if payload == nil {
			t.Fatalf("snapshot %d returned nil", i)
		}
		var got struct {
			Live []string `json:"live"`
		}
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("snapshot %d unmarshal: %v", i, err)
		}
		if len(got.Live) != 4 {
			t.Fatalf("snapshot %d: %d live sessions, want 4", i, len(got.Live))
		}
		if first == nil {
			first = got.Live
			continue
		}
		for j := range first {
			if got.Live[j] != first[j] {
				t.Fatalf("snapshot %d reordered the live list:\n first %v\n  then %v\n"+
					"every viewer is being sent a full state push on every poll tick",
					i, first, got.Live)
			}
		}
	}
}
