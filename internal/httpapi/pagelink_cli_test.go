package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

func TestEnsurePageShareLinkCreatesReusesAndHonorsShareOrigin(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	owner, err := srv.DB.FirstUserID(ctx)
	if err != nil {
		u, cerr := srv.DB.CreateUser(ctx, id.New(), "tester", "hash")
		if cerr != nil {
			t.Fatal(cerr)
		}
		owner = u.ID
	}

	dir := filepath.Join(t.TempDir(), "page-demo")
	if err := pages.Scaffold(dir, "blank", "demo", PageFixtures()); err != nil {
		t.Fatal(err)
	}
	// Standalone prototype with no panel sections.
	if err := os.WriteFile(filepath.Join(dir, "vibepanel.json"), []byte(`{"sdk":1,"name":"demo","sections":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<!doctype html><title>Demo</title><p>Hello share</p>`), 0o644); err != nil {
		t.Fatal(err)
	}

	page, err := srv.DB.CreateSharePage(ctx, id.New(), owner, "demo", dir)
	if err != nil {
		t.Fatal(err)
	}

	cfg := srv.Cfg
	// Unpublished page must be refused.
	if _, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{}); err == nil {
		t.Fatal("expected error for unpublished page")
	}

	// Publish v1.
	ver, err := FreezeDraft(ctx, srv.DB, page, "first", false)
	if err != nil {
		t.Fatal(err)
	}
	page.PublishedVersion = ver

	// Configure a dedicated share origin and verify CLI URLs use it.
	if err := srv.DB.SetSetting(ctx, shareOriginKey, "https://share.example.com"); err != nil {
		t.Fatal(err)
	}

	first, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{})
	if err != nil {
		t.Fatalf("first EnsurePageShareLink: %v", err)
	}
	if !first.Created || first.Reused {
		t.Fatalf("expected created=true reused=false, got %+v", first)
	}
	if !strings.HasPrefix(first.URL, "https://share.example.com/share/") || !strings.HasSuffix(first.URL, "/") {
		t.Fatalf("unexpected share URL: %q", first.URL)
	}
	if first.Detail != string(store.ShareCounts) {
		t.Fatalf("default detail = %q, want counts", first.Detail)
	}

	// Second call reuses the exact same URL.
	second, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{})
	if err != nil {
		t.Fatalf("second EnsurePageShareLink: %v", err)
	}
	if !second.Reused || second.Created || second.URL != first.URL {
		t.Fatalf("expected reused same URL; first=%+v second=%+v", first, second)
	}

	// ForceNew mints a distinct URL.
	third, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{ForceNew: true, Name: "second"})
	if err != nil {
		t.Fatalf("ForceNew EnsurePageShareLink: %v", err)
	}
	if !third.Created || third.URL == first.URL {
		t.Fatalf("expected new distinct URL; first=%q third=%+v", first.URL, third)
	}

	// Verify the minted token actually serves the published page over HTTP.
	token := strings.TrimSuffix(strings.TrimPrefix(first.URL, "https://share.example.com/share/"), "/")
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/share/"+token+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET minted share link status = %d, want 200", resp.StatusCode)
	}

	// Also verify fallback origin logic when no dedicated share origin is set.
	if err := srv.DB.SetSetting(ctx, shareOriginKey, ""); err != nil {
		t.Fatal(err)
	}
	proxyCfg := config.Config{
		Domain:     "panel.example.com",
		TLSMode:    config.TLSOff,
		PublicPort: 443,
		Addr:       "127.0.0.1:9884",
	}
	if got, local := cliShareBaseOrigin(ctx, srv.DB, proxyCfg); got != "https://panel.example.com" || local {
		t.Fatalf("reverse-proxy fallback origin = %q, want https://panel.example.com", got)
	}
}

// publishedCLIPage is a published, panel-less page for the link tests.
func publishedCLIPage(t *testing.T, srv *Server) store.SharePage {
	t.Helper()
	ctx := context.Background()
	owner, err := srv.DB.FirstUserID(ctx)
	if err != nil {
		u, cerr := srv.DB.CreateUser(ctx, id.New(), "tester", "hash")
		if cerr != nil {
			t.Fatal(cerr)
		}
		owner = u.ID
	}
	dir := filepath.Join(t.TempDir(), "page-demo")
	if err := pages.Scaffold(dir, "blank", "demo", PageFixtures()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vibepanel.json"), []byte(`{"sdk":1,"name":"demo","sections":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<!doctype html><p>hi</p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := srv.DB.CreateSharePage(ctx, id.New(), owner, "demo", dir)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := FreezeDraft(ctx, srv.DB, page, "v1", false)
	if err != nil {
		t.Fatal(err)
	}
	page.PublishedVersion = ver
	if err := srv.DB.SetSetting(ctx, shareOriginKey, "https://share.example.com"); err != nil {
		t.Fatal(err)
	}
	return page
}

// The caller prints the returned URL as the answer to what it asked for, so a
// reused link has to be that answer: same expiry, never a locked one.
func TestReusedShareLinkIsWhatWasAskedFor(t *testing.T) {
	_, srv := newTestServer(t)
	ctx := context.Background()
	page := publishedCLIPage(t, srv)
	cfg := srv.Cfg

	forever, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{})
	if err != nil || !forever.Created {
		t.Fatalf("forever: %+v %v", forever, err)
	}

	// An expiry asked for is never satisfied by a link that has none.
	timed, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{ExpiresIn: 3600})
	if err != nil {
		t.Fatal(err)
	}
	if !timed.Created || timed.Reused || timed.URL == forever.URL || timed.ExpiresAt == 0 {
		t.Fatalf("--expires must mint a new expiring link: %+v", timed)
	}
	// And asking again with the same expiry still mints: a new window each time.
	again, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{ExpiresIn: 3600})
	if err != nil || !again.Created || again.URL == timed.URL {
		t.Fatalf("a repeated --expires must not reuse: %+v %v", again, err)
	}

	// No expiry asked for gives the never-expiring link, not the timed one.
	plain, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !plain.Reused || plain.URL != forever.URL || plain.ExpiresAt != 0 {
		t.Fatalf("no --expires should reuse only the link that never expires: %+v", plain)
	}

	// A locked link is somebody's fixed arrangement and is not handed out.
	if err := srv.DB.UpdateShareLink(ctx, forever.ID, forever.Name, "", true); err != nil {
		t.Fatal(err)
	}
	fresh, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.Created || fresh.URL == forever.URL {
		t.Fatalf("a locked link was reused: %+v", fresh)
	}
}

// Interactive is audited when the route makes it (red line 8), and the CLI is
// the same decision made from a shell.
func TestInteractiveCLILinkIsAudited(t *testing.T) {
	_, srv := newTestServer(t)
	ctx := context.Background()
	page := publishedCLIPage(t, srv)

	count := func() int {
		rows, err := srv.DB.RecentAuditPrefix(ctx, "share.interactive_changed", 50)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if _, err := EnsurePageShareLink(ctx, srv.DB, srv.Cfg, page, ShareLinkOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("a non-interactive link wrote %d interactive audit rows", n)
	}
	res, err := EnsurePageShareLink(ctx, srv.DB, srv.Cfg, page, ShareLinkOptions{Interactive: true})
	if err != nil || !res.Created {
		t.Fatalf("%+v %v", res, err)
	}
	rows, _ := srv.DB.RecentAuditPrefix(ctx, "share.interactive_changed", 50)
	if len(rows) != 1 || rows[0].Username != "cli" || !strings.HasSuffix(rows[0].Detail, " on") {
		t.Fatalf("want one cli interactive audit row, got %+v", rows)
	}
}

// A loopback fallback is a link only this machine can open, and the caller is
// told, without the URL changing.
func TestLoopbackShareLinkCarriesAWarning(t *testing.T) {
	_, srv := newTestServer(t)
	ctx := context.Background()
	page := publishedCLIPage(t, srv)
	if err := srv.DB.SetSetting(ctx, shareOriginKey, ""); err != nil {
		t.Fatal(err)
	}
	cfg := srv.Cfg
	cfg.Domain = ""
	res, err := EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "only opens on this machine") {
		t.Fatalf("want a loopback warning, got %+v", res.Warnings)
	}
	cfg.Domain = "panel.example.com"
	cfg.TLSMode = config.TLSOff
	cfg.PublicPort = 443
	res, err = EnsurePageShareLink(ctx, srv.DB, cfg, page, ShareLinkOptions{})
	if err != nil || len(res.Warnings) != 0 {
		t.Fatalf("a public origin should not warn: %+v %v", res, err)
	}
}
