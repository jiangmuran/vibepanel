package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestNothingUnderWebIsPartOfThisModule(t *testing.T) {
	// npm packages ship whatever they like, and one of them ships Go: flatted
	// carries golang/pkg/flatted/flatted.go. `go build ./...`, `go vet ./...`
	// and `go test ./...` were all compiling and checking a third-party file
	// that arrives and changes with `npm ci`, and `go test -cover ./...`
	// listed it among this project's packages.
	//
	// web/go.mod is what stops it. Go has no exclude directive; a nested module
	// is the mechanism, and it is one deletion away from coming back.
	//
	// Asking the toolchain rather than looking for the file, because the file
	// existing is not the property that matters.
	// From the module root. `./...` is relative to the working directory, and
	// the first version of this test ran it here — where the answer is three
	// packages under internal/webui and never anything from web/. It passed
	// with web/go.mod deleted.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("cannot run go list: %v", err)
	}
	if !strings.Contains(string(out), "/internal/session") {
		t.Fatalf("go list did not return this project's packages, so nothing was "+
			"compared:\n%s", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(line, "/web/") {
			t.Errorf("%s is part of this module. Something under web/ is being "+
				"compiled, vetted and tested as though we wrote it.", line)
		}
	}
}
