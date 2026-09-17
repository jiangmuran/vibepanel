package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/resources"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

func TestChoosingAPresetKeepsTheCustomNumbersAndTheBoost(t *testing.T) {
	ts, srv := newTestServer(t)
	if code, b := doJSON(t, ts, http.MethodPut, "/api/resources/policy",
		`{"mode":"custom","poolPercent":60,"askPercent":70,"autoAct":false,"graceSeconds":45}`); code != http.StatusOK {
		t.Fatalf("custom: %d %s", code, b)
	}
	if code, b := doJSON(t, ts, http.MethodPost, "/api/resources/boost", `{"minutes":30}`); code != http.StatusOK {
		t.Fatalf("boost: %d %s", code, b)
	}
	// The page sends whatever numbers it has beside a preset; they are not
	// the preset's and must not overwrite the custom ones.
	if code, b := doJSON(t, ts, http.MethodPut, "/api/resources/policy",
		`{"mode":"balanced","poolPercent":88,"askPercent":90,"autoAct":true,"graceSeconds":90}`); code != http.StatusOK {
		t.Fatalf("preset: %d %s", code, b)
	}
	got := srv.ResourcePolicy(context.Background())
	if got.Mode != resources.Balanced || got.PoolPercent != 60 || got.AskPercent != 70 || got.AutoAct || got.GraceSeconds != 45 {
		t.Fatalf("custom numbers lost: %+v", got)
	}
	if got.BoostUntil <= time.Now().Unix() {
		t.Fatal("saving the mode ended the boost")
	}
	// A body cannot start a boost of its own.
	doJSON(t, ts, http.MethodPost, "/api/resources/boost", `{"minutes":0}`)
	if code, _ := doJSON(t, ts, http.MethodPut, "/api/resources/policy",
		`{"mode":"balanced","poolPercent":88,"askPercent":90,"autoAct":true,"graceSeconds":90,"boostUntil":9999999999}`); code != http.StatusOK {
		t.Fatalf("with boostUntil: %d", code)
	}
	if srv.ResourcePolicy(context.Background()).BoostUntil != 0 {
		t.Fatal("a policy body started a boost")
	}
}

func TestAPolicyOutOfRangeOrWithStrayKeysIsRefused(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, body := range []string{
		`{"mode":"custom","poolPercent":20,"askPercent":80,"autoAct":false,"graceSeconds":30}`,
		`{"mode":"custom","poolPercent":60,"askPercent":80,"autoAct":true,"graceSeconds":5}`,
		`{"mode":"turbo"}`,
		// What the page sent before policyOnly: the numbers in force, which
		// carry two keys a policy does not have.
		`{"mode":"custom","poolPercent":60,"askPercent":80,"autoAct":true,"graceSeconds":30,"dynamic":true}`,
	} {
		if code, _ := doJSON(t, ts, http.MethodPut, "/api/resources/policy", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code, _ := doJSON(t, ts, http.MethodPost, "/api/resources/boost", `{"minutes":721}`); code != http.StatusBadRequest {
		t.Errorf("a boost past twelve hours: %d", code)
	}
}

// A database that is not answering is this feature's own symptom. Caching the
// default then switched somebody's policy until the next save.
func TestAPolicyReadThatFailsIsNotRemembered(t *testing.T) {
	ts, srv := newTestServer(t)
	if code, b := doJSON(t, ts, http.MethodPut, "/api/resources/policy",
		`{"mode":"performance","poolPercent":88,"askPercent":90,"autoAct":true,"graceSeconds":90}`); code != http.StatusOK {
		t.Fatalf("%d %s", code, b)
	}
	srv.res.mu.Lock()
	srv.res.policy = nil
	srv.res.mu.Unlock()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := srv.ResourcePolicy(cancelled); got.Mode != resources.Balanced {
		t.Fatalf("a failed read did not fall back: %+v", got)
	}
	if got := srv.ResourcePolicy(context.Background()); got.Mode != resources.Performance {
		t.Fatalf("the fallback from a failed read was kept: %+v", got)
	}
}

func TestAnOldTmuxListingIsNotHandedToTheGovernor(t *testing.T) {
	_, srv := newTestServer(t)
	srv.tmuxListMu.Lock()
	srv.tmuxList = []tmux.Info{{Name: "vp_a", PID: 42, PaneID: "%1"}}
	srv.tmuxListAt = time.Now()
	srv.tmuxListMu.Unlock()
	if got := srv.ResourcePanes(); len(got) != 1 || got[0].PID != 42 || got[0].ID != "%1" {
		t.Fatalf("fresh: %+v", got)
	}
	// Pane ids start again from %0 when the server restarts; a listing from
	// before sorted a process into another session's cgroup for good.
	srv.tmuxListMu.Lock()
	srv.tmuxListAt = time.Now().Add(-time.Minute)
	srv.tmuxListMu.Unlock()
	if got := srv.ResourcePanes(); len(got) != 0 {
		t.Fatalf("stale: %+v", got)
	}
}

func TestTheQuestionAloneIsItsOwnRoute(t *testing.T) {
	ts, _ := newTestServer(t)
	code, b := doJSON(t, ts, http.MethodGet, "/api/resources/alert", "")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, b)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["alert"]) != "null" || len(got) != 1 {
		t.Fatalf("%s", b)
	}
}

// A policy that cannot be read is not the default one: both write paths save
// on top of what they read, and saving on top of the default turned a stored
// Performance into Balanced with auto-act on.
func TestAPolicyThatCannotBeReadIsNotSavedOver(t *testing.T) {
	ts, srv := newTestServer(t)
	if code, b := doJSON(t, ts, http.MethodPut, "/api/resources/policy",
		`{"mode":"custom","poolPercent":60,"askPercent":70,"autoAct":false,"graceSeconds":45}`); code != http.StatusOK {
		t.Fatalf("custom: %d %s", code, b)
	}
	ctx := context.Background()
	// The cache would answer without the database; what is under test is the
	// read that fails.
	srv.res.mu.Lock()
	srv.res.policy = nil
	srv.res.mu.Unlock()
	if _, err := srv.DB.SQL().ExecContext(ctx, "ALTER TABLE settings RENAME TO settings_away"); err != nil {
		t.Fatal(err)
	}
	if code, b := doJSON(t, ts, http.MethodPost, "/api/resources/boost", `{"minutes":30}`); code != http.StatusServiceUnavailable {
		t.Fatalf("boost over an unread policy: %d %s", code, b)
	}
	if code, b := doJSON(t, ts, http.MethodPut, "/api/resources/policy", `{"mode":"balanced"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("mode over an unread policy: %d %s", code, b)
	}
	if _, err := srv.DB.SQL().ExecContext(ctx, "ALTER TABLE settings_away RENAME TO settings"); err != nil {
		t.Fatal(err)
	}
	got := srv.ResourcePolicy(ctx)
	if got.Mode != resources.Custom || got.AutoAct || got.PoolPercent != 60 || got.BoostUntil != 0 {
		t.Fatalf("the stored policy changed: %+v", got)
	}
}
