package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A relative --static-dir is what anyone would type, and it used to answer 404
// for every request: the handler compared a path that filepath.Abs had
// resolved against the unresolved string it had been given, so the containment
// check rejected everything while the files sat right there.
func TestStaticDirServesFromARelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "site", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("site/index.html", `<div id="root"></div>`)
	write("site/assets/app-abc123.js", "console.log(1)")

	// So that "site" is a relative path resolving to the directory above.
	t.Chdir(root)

	h := Handler("site")
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := get("/"); rec.Code != http.StatusOK || rec.Body.String() != `<div id="root"></div>` {
		t.Errorf("GET / = %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/assets/app-abc123.js"); rec.Code != http.StatusOK {
		t.Errorf("GET an asset = %d, want 200", rec.Code)
	} else if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Error("a fingerprinted asset was served without a Cache-Control header")
	}

	// An unknown path is a client-side route, not a 404.
	if rec := get("/projects/whatever"); rec.Code != http.StatusOK {
		t.Errorf("GET an app route = %d, want the index", rec.Code)
	}

	// index.html must never be cached hard, or an upgrade leaves the browser
	// pointing at asset names that no longer exist.
	if cc := get("/").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", cc)
	}
}

// Containment still holds after the fix. Worth its own case because the check
// was previously "wrong in the safe direction", and a fix that made it wrong
// in the other direction would look identical from the working path above.
func TestStaticDirRefusesToLeaveItsRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "site", "index.html"), []byte("app"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file next to the served directory, which no request may reach.
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("not for serving"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	h := Handler("site")
	for _, path := range []string{
		"/../secret.txt",
		"/..%2fsecret.txt",
		"/site/../../secret.txt",
		"/assets/../../secret.txt",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if body := rec.Body.String(); body == "not for serving" {
			t.Errorf("%s served the file above the root", path)
		}
	}
}
