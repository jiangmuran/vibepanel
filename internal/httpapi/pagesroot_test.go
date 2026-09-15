package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/store"
)

func sendRaw(t *testing.T, ts *httptest.Server, method, path, ctype string, body []byte) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out, res.Header
}

func auditHas(t *testing.T, srv *Server, event, detail string) bool {
	t.Helper()
	entries, err := srv.DB.RecentAudit(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Event == event && strings.Contains(e.Detail, detail) {
			return true
		}
	}
	return false
}

// Nobody has to choose where pages go; somebody who does is obeyed, and a
// choice that stops working falls back and says so instead of failing a page.
func TestThePagesDirectoryIsASettingWithAFallback(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	cat := getJSON[pageCatalogue](t, ts, "/api/settings/pages/catalogue")
	if cat.PagesRootInfo.Source != "default" || cat.PagesRootInfo.Setting != "" ||
		cat.PagesRoot != srv.Cfg.PagesDir() {
		t.Fatalf("unset, the root is %+v, want the data directory's pages/", cat.PagesRootInfo)
	}
	if v, _ := srv.DB.GetSetting(ctx, pagesRootKey, "unset"); v != "unset" {
		t.Errorf("reading the default stored %q as a setting", v)
	}

	mine := filepath.Join(t.TempDir(), "my pages")
	code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/pages/root", "application/json",
		[]byte(`{"dir":`+jsonString(mine)+`}`))
	if code != http.StatusOK {
		t.Fatalf("set the root = %d: %s", code, body)
	}
	page := postJSON[store.SharePage](t, ts, "/api/settings/pages", `{"name":"Lobby","template":"blank"}`)
	if want := filepath.Join(mine, "page-lobby"); page.SourceDir != want {
		t.Errorf("with a root set, a page went to %q, want %q", page.SourceDir, want)
	}
	if !auditHas(t, srv, "page.root_changed", mine) {
		t.Error("changing where pages are written left no audit row")
	}

	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relative/dir", filepath.Join(blocker, "pages")} {
		code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/pages/root", "application/json",
			[]byte(`{"dir":`+jsonString(bad)+`}`))
		if code != http.StatusBadRequest {
			t.Errorf("an unusable root %q = %d, want 400", bad, code)
		}
	}
	if v, _ := srv.DB.GetSetting(ctx, pagesRootKey, ""); v != mine {
		t.Errorf("a refused root replaced the setting: %q", v)
	}

	// The chosen directory goes away after it was chosen.
	if err := srv.DB.SetSetting(ctx, pagesRootKey, filepath.Join(blocker, "pages")); err != nil {
		t.Fatal(err)
	}
	got := srv.pagesRoot(ctx)
	if got.Source != "default" || got.Dir != srv.Cfg.PagesDir() || !strings.Contains(got.Problem, blocker) {
		t.Errorf("a broken setting resolved to %+v, want the default and the reason", got)
	}

	code, _, _ = sendRaw(t, ts, http.MethodPut, "/api/settings/pages/root", "application/json", []byte(`{"dir":""}`))
	if v, _ := srv.DB.GetSetting(ctx, pagesRootKey, "x"); code != http.StatusOK || v != "" {
		t.Errorf("clearing the root = %d, setting %q", code, v)
	}
}

func TestADataDirectoryThatCannotBeWrittenFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())
	blocked := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = blocked

	got := ResolvePagesRoot(context.Background(), nil, cfg)
	if want := filepath.Join(home, ".local", "share", "vibepanel", "pages"); got.Dir != want ||
		got.Source != "fallback" || !strings.Contains(got.Problem, blocked) {
		t.Errorf("resolved %+v, want the home directory's pages and why", got)
	}

	// And with the home directory unusable too, a page can still be made.
	fileHome := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(fileHome, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fileHome)
	last := ResolvePagesRoot(context.Background(), nil, cfg)
	if last.Source != "fallback" || !strings.HasPrefix(last.Dir, os.TempDir()) {
		t.Errorf("with nothing else usable, resolved %+v, want the temporary directory", last)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A page goes out as a zip and comes back in as a new page: the same files,
// its own directory, and not published until somebody publishes it here.
func TestAPageTravelsAsAZip(t *testing.T) {
	ts, srv := newTestServer(t)
	page := postJSON[store.SharePage](t, ts, "/api/settings/pages", `{"name":"走廊电视墙","template":"wall"}`)

	// Never published: the directory as it is.
	code, draft, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/pages/"+page.ID+"/export", "", nil)
	if code != http.StatusOK || len(draft) == 0 {
		t.Fatalf("exporting an unpublished page = %d", code)
	}

	postJSON[map[string]int](t, ts, "/api/settings/pages/"+page.ID+"/publish", `{"note":"v1"}`)
	if err := os.WriteFile(filepath.Join(page.SourceDir, "index.html"), []byte("<p>draft only</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, zipped, h := sendRaw(t, ts, http.MethodGet, "/api/settings/pages/"+page.ID+"/export", "", nil)
	if code != http.StatusOK || h.Get("Content-Type") != "application/zip" ||
		!strings.Contains(h.Get("Content-Disposition"), "-v1.zip") {
		t.Fatalf("export = %d %v", code, h)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == "index.html" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			if strings.Contains(string(b), "draft only") {
				t.Error("the export of v1 carried the unpublished draft")
			}
		}
		if strings.HasSuffix(f.Name, ".md") || f.Name == "vibepanel.js" {
			t.Errorf("the export carried %s, which every directory gets anyway", f.Name)
		}
	}
	if code, _, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/pages/"+page.ID+"/export?version=9", "", nil); code != http.StatusNotFound {
		t.Errorf("exporting a version that does not exist = %d", code)
	}

	code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/pages/import?name=Kitchen", "application/zip", zipped)
	if code != http.StatusCreated {
		t.Fatalf("import = %d: %s", code, body)
	}
	var got importedPage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Page.Name != "Kitchen" || got.Page.PublishedVersion != 0 ||
		got.Page.SourceDir != filepath.Join(srv.Cfg.PagesDir(), "page-kitchen") {
		t.Errorf("imported %+v", got.Page)
	}
	for _, f := range []string{"index.html", "AGENTS.md", "vibepanel.js", "fixtures/busy.json"} {
		if _, err := os.Stat(filepath.Join(got.Page.SourceDir, f)); err != nil {
			t.Errorf("the imported directory has no %s", f)
		}
	}
	if !auditHas(t, srv, "page.imported", "Kitchen") {
		t.Error("an import left no audit row")
	}

	if code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/pages/import", "application/zip", []byte("not a zip")); code != http.StatusBadRequest {
		t.Errorf("importing garbage = %d, want 400", code)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/pages/import", "application/zip",
		bytes.Repeat([]byte("x"), 7<<20)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("importing an oversized body = %d, want 413", code)
	}
}
