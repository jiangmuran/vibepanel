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
	if got := cliShareBaseOrigin(ctx, srv.DB, proxyCfg); got != "https://panel.example.com" {
		t.Fatalf("reverse-proxy fallback origin = %q, want https://panel.example.com", got)
	}
}
