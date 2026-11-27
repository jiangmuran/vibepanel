package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/resources"
	"github.com/jiangmuran/vibepanel/internal/store"
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

func tmuxNames(t *testing.T, srv *Server) map[string]bool {
	t.Helper()
	live, err := srv.Tmux.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, i := range live {
		out[i.Name] = true
	}
	return out
}

// Ending a project's sessions from the resources page is the sidebar's end,
// repeated: tmux, the row, and one audit row for the whole. The project and
// the other project's sessions stay.
func TestEndingAProjectEndsItsSessionsAndOnlyThose(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	mine := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"mine"}`)
	other := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"other"}`)
	var ended []store.Session
	for range 2 {
		ended = append(ended, postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+mine.ID+`","command":["sleep","300"]}`))
	}
	kept := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+other.ID+`","command":["sleep","300"]}`)
	// An archived project's sessions are the ones nothing else lists.
	postJSON[store.Project](t, ts, "/api/projects/"+mine.ID+"/archive", `{}`)

	code, body := doJSON(t, ts, http.MethodPost, "/api/resources/projects/"+mine.ID+"/end", ``)
	if code != http.StatusOK || string(body) != "{\"ended\":2}\n" {
		t.Fatalf("end: %d %s", code, body)
	}
	live := tmuxNames(t, srv)
	for _, s := range ended {
		if live[s.TmuxName] {
			t.Errorf("%s is still running", s.TmuxName)
		}
		if _, err := srv.DB.GetSession(ctx, s.ID); err == nil {
			t.Errorf("%s still has a row", s.ID)
		}
	}
	if !live[kept.TmuxName] {
		t.Error("another project's session was ended")
	}
	if _, err := srv.DB.GetSession(ctx, kept.ID); err != nil {
		t.Errorf("another project's row: %v", err)
	}
	if p, err := srv.DB.GetProject(ctx, mine.ID); err != nil || p.ArchivedAt == nil {
		t.Errorf("the project was deleted or restored: %+v %v", p, err)
	}
	rows, _ := srv.DB.RecentAuditPrefix(ctx, "resources.project_end", 5)
	if len(rows) != 1 || rows[0].Detail != "mine: 2" {
		t.Errorf("audit: %+v", rows)
	}
}

func TestEndingAProjectNeedsAProjectAndASignedInPerson(t *testing.T) {
	ts, _ := newTestServer(t)
	if code, _ := doJSON(t, ts, http.MethodPost, "/api/resources/projects/nope/end", ``); code != http.StatusNotFound {
		t.Errorf("an unknown project = %d, want 404", code)
	}
	p := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"p"}`)
	anon := &http.Client{}
	res, err := anon.Post(ts.URL+"/api/resources/projects/"+p.ID+"/end", "application/json", strings.NewReader(``))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a credential = %d, want 401", res.StatusCode)
	}
}

// What a page shows beside a session comes from the poller's rows.
func TestTheResourcePageNamesASessionsProjectAndWhetherItIsArchived(t *testing.T) {
	_, srv := newTestServer(t)
	at := int64(1700000000)
	srv.publishResourceSessions(
		[]store.Session{{ID: "s1", ProjectID: "p1", TmuxName: "vp_1", Title: "t", CreatedAt: 5, LastOutputAt: at}},
		[]store.Project{{ID: "p1", Name: "old", ArchivedAt: &at}})
	srv.noteResourceInput("s1")
	got := srv.ResourceSessions()
	if len(got) != 1 || got[0].ProjectName != "old" || !got[0].Archived || got[0].CreatedAt != 5 || got[0].LastOutput != at || got[0].LastInput.IsZero() {
		t.Fatalf("%+v", got)
	}
}

// A refused kill leaves a row. The success path records one; the refusal --
// a button pressed on a process the panel could not prove belonged to the
// named session -- used to be invisible, and "nothing happened, no row" is
// the shape an abuse probe looks like from the inside.
func TestARefusedKillIsAudited(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Resources = resources.New(resources.Env{})
	code, body := doJSON(t, ts, http.MethodPost, "/api/resources/kill",
		`{"sessionId":"nosuchsession","pid":999999,"start":1}`)
	if code != http.StatusNotFound {
		t.Fatalf("kill naming no session = %d %s, want 404", code, body)
	}
	rows, err := srv.DB.RecentAuditPrefix(context.Background(), "resources.", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Event == "resources.kill_refused" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no resources.kill_refused row among %d rows", len(rows))
	}
}
