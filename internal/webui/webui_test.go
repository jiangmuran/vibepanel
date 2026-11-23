package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
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

// TestTheManifestIsServedAsAManifest.
//
// `.webmanifest` is not in Go's MIME table, so ServeContent sniffs the body
// and answers `text/plain; charset=utf-8`. Nothing about that looks wrong from
// the server side -- 200, valid JSON, the right bytes -- and it costs the
// whole file: a browser that will not parse the manifest does not install a
// web app, "add to home screen" leaves a bookmark, and a bookmark gets none of
// `theme_color`, `background_color` or `display: standalone`.
//
// That is 「上下仍然有白边」, reported three times against a panel whose own
// surfaces measure dark in every one of the six theme states. The bars are
// drawn by the browser, from a file it declined to read.
func TestTheManifestIsServedAsAManifest(t *testing.T) {
	h := Handler("")
	for _, tc := range []struct{ path, want string }{
		{"/manifest.webmanifest", "application/manifest+json"},
		// The neighbours, so a change here that reaches for a broader brush
		// has to notice it is repainting these too.
		{"/icon.svg", "image/svg+xml"},
		{"/icon-192.png", "image/png"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", tc.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: Content-Type %q, want %q", tc.path, got, tc.want)
		}
	}
}

func gzTestHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	js := strings.Repeat("console.log('vibepanel');\n", 400)
	for rel, body := range map[string]string{
		"index.html":       `<div id="root"></div>`,
		"assets/app-1.js":  js,
		"assets/pic-1.png": strings.Repeat("x", 5000),
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Handler(dir), js
}

func TestBundleIsServedGzippedWhenAccepted(t *testing.T) {
	h, js := gzTestHandler(t)
	do := func(method, enc string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/assets/app-1.js", nil)
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do("GET", "gzip, br")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("no gzip: %v", rec.Header())
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("Vary = %q", rec.Header().Get("Vary"))
	}
	wire := rec.Body.Len()
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != js {
		t.Error("body does not gunzip to the original")
	}
	if wire >= len(js) || rec.Header().Get("Content-Length") != strconv.Itoa(wire) {
		t.Errorf("length %d / header %q", wire, rec.Header().Get("Content-Length"))
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q", cc)
	}
	gzTag := rec.Header().Get("ETag")

	id := do("GET", "")
	if id.Header().Get("Content-Encoding") != "" || id.Body.String() != js {
		t.Error("identity request got an encoded body")
	}
	if id.Header().Get("Vary") != "Accept-Encoding" {
		t.Error("identity answer lacks Vary")
	}
	if id.Header().Get("ETag") == gzTag {
		t.Error("ETag is the same for both encodings")
	}
	if rec := do("GET", "gzip;q=0"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 was compressed")
	}
	if head := do("HEAD", "gzip"); head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("HEAD = %d %v", head.Code, head.Header())
	}
	if part := do("GET", "gzip", "Range", "bytes=0-9"); part.Code != http.StatusPartialContent || part.Body.Len() != 10 {
		t.Errorf("Range = %d len %d", part.Code, part.Body.Len())
	}
	if nm := do("GET", "gzip", "If-None-Match", gzTag); nm.Code != http.StatusNotModified {
		t.Errorf("If-None-Match = %d", nm.Code)
	}

	// The SPA fallback is index.html and is compressible too.
	req := httptest.NewRequest("GET", "/projects/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("fallback = %d %v", rr.Code, rr.Header())
	}
}

func TestAnImageIsNeverCompressed(t *testing.T) {
	h, _ := gzTestHandler(t)
	req := httptest.NewRequest("GET", "/assets/pic-1.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 5000 {
		t.Errorf("png encoded: %v len %d", rec.Header(), rec.Body.Len())
	}
}

// Embedded files all carry a zero ModTime, and Vite's hashed asset names are
// fixed-width (`index-DGv2PUHW.js` -> `index-BfVW1Vhb.js`), so dist/index.html
// keeps the exact same byte count across upgrades. A size+mtime ETag answered
// 304 Not Modified on reload after a binary upgrade and kept serving the old
// bundle from the browser cache.
func TestUpgradeWithSameLengthEmbeddedIndexDoesNotAnswer304(t *testing.T) {
	pad := strings.Repeat("<!-- vibepanel -->\n", 80)
	v1 := pad + `<script type="module" src="/assets/index-DGv2PUHW.js"></script>`
	v2 := pad + `<script type="module" src="/assets/index-BfVW1Vhb.js"></script>`
	if len(v1) != len(v2) {
		t.Fatalf("fixture lengths differ: %d vs %d", len(v1), len(v2))
	}

	h1 := spaHandler{fsys: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(v1)}}, gz: &gzCache{}}
	h2 := spaHandler{fsys: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(v2)}}, gz: &gzCache{}}

	for _, enc := range []string{"gzip", ""} {
		req1 := httptest.NewRequest(http.MethodGet, "/", nil)
		if enc != "" {
			req1.Header.Set("Accept-Encoding", enc)
		}
		rec1 := httptest.NewRecorder()
		h1.ServeHTTP(rec1, req1)
		etag1 := rec1.Header().Get("ETag")
		if rec1.Code != http.StatusOK || etag1 == "" {
			t.Fatalf("enc=%q v1 status=%d etag=%q", enc, rec1.Code, etag1)
		}

		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		if enc != "" {
			req2.Header.Set("Accept-Encoding", enc)
		}
		req2.Header.Set("If-None-Match", etag1)
		rec2 := httptest.NewRecorder()
		h2.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("enc=%q upgraded index answered %d for stale ETag %s, want 200", enc, rec2.Code, etag1)
		}
		if etag2 := rec2.Header().Get("ETag"); etag2 == "" || etag2 == etag1 {
			t.Fatalf("enc=%q etag did not change across upgrade: %q -> %q", enc, etag1, etag2)
		}
	}
}
