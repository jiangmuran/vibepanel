package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// What `vibepanel page data` and `page run` do, through the functions they
// call: the manifest's rules hold from a shell as they do from a screen.
func TestTheCLIChangesDataAndRunsServerJSByThePagesRules(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, serverManifest, serverFiles)
	ctx := context.Background()
	db := srv.DB
	page, _ := db.SharePageByID(ctx, p.page.ID)

	if got, err := ChangePageData(ctx, db, page, store.PageDataDraft, "set", "announcement", "from a shell"); err != nil || got != "from a shell" {
		t.Fatalf("set = %v, %v", got, err)
	}
	for _, bad := range []struct {
		key   string
		value any
	}{{"votes", 3.0}, {"announcement", strings.Repeat("x", 41)}, {"nope", "x"}} {
		if _, err := ChangePageData(ctx, db, page, store.PageDataDraft, "set", bad.key, bad.value); err == nil {
			t.Errorf("set %s = %v was accepted", bad.key, bad.value)
		}
	}
	values, _, _ := PageDataValues(ctx, db, page, store.PageDataDraft)
	live, _, _ := PageDataValues(ctx, db, page, store.PageDataLive)
	if values["announcement"] != "from a shell" || live["announcement"] != "" {
		t.Errorf("draft %v, live %v", values["announcement"], live["announcement"])
	}
	if !auditHas(t, srv, "page.data_changed", "draft: set announcement") {
		t.Error("a CLI change left no audit row")
	}

	// An action runs as a visitor when a visitor may run it, with the
	// visitor's limits: tally may set announcement, sneak may not set notes.
	run, err := RunPageServer(ctx, db, page, "action", "tally", nil, nil, false)
	if res, _ := run.Result.(map[string]any); err != nil || res["total"] != float64(1) {
		t.Fatalf("run action tally = %+v, %v", run, err)
	}
	if _, err := RunPageServer(ctx, db, page, "action", "sneak", nil, nil, false); err == nil || !strings.Contains(err.Error(), "writes") {
		t.Errorf("run action sneak = %v", err)
	}
	if _, err := RunPageServer(ctx, db, page, "action", "tally", map[string]any{"extra": 1.0}, nil, false); err == nil {
		t.Error("a payload the action does not declare was run")
	}
	run, err = RunPageServer(ctx, db, page, "action", "reset", nil, nil, true)
	if err != nil || len(run.Log) == 0 || run.Log[len(run.Log)-1].Text != `reset {"n":1}` {
		t.Errorf("run action reset = %+v, %v", run, err)
	}
	if run, err := RunPageServer(ctx, db, page, "schedule", "", nil, nil, false); err != nil || run.Result != nil {
		t.Errorf("run schedule = %+v, %v", run, err)
	}
	values, _, _ = PageDataValues(ctx, db, page, store.PageDataDraft)
	if values["votes"] != float64(10) || values["notes"] != "by admin" {
		t.Errorf("after the runs: %v", values)
	}
	snap, _ := json.Marshal(map[string]any{"sessions": []any{}})
	run, err = RunPageServer(ctx, db, page, "transform", "", nil, snap, false)
	if tr, _ := run.Result.(map[string]any); err != nil || tr["votes"] != float64(10) || tr["seesNotes"] != false || tr["sessions"] != true {
		t.Errorf("run transform = %+v, %v", run, err)
	}

	// The CLI prints its log; it does not overwrite the panel's server.log.
	if _, err := os.Stat(filepath.Join(p.dir, ".vibepanel", "server.log")); err == nil {
		t.Error("the CLI wrote .vibepanel/server.log")
	}
}
