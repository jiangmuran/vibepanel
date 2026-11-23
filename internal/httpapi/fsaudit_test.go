package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fsRows is the audit rows the /api/fs routes wrote, oldest last.
func fsRows(t *testing.T, srv *Server) []string {
	t.Helper()
	rows, err := srv.DB.RecentAuditPrefix(context.Background(), "files.", 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Event+" "+r.Detail)
	}
	return out
}

func fsGet(t *testing.T, ts interface {
	Client() *http.Client
}, rawURL string) (int, http.Header, string) {
	t.Helper()
	res, err := ts.Client().Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(b)
}

// A file outside every project, opened by one click, is one row however many
// requests the click makes; a download is its own row; the same file inside a
// project, and a resolve, leave none.
func TestFSReadsOutsideAProjectAreAuditedOnceAMinute(t *testing.T) {
	ts, srv, root, _ := writeFixture(t)
	outside := filepath.Join(t.TempDir(), "plan.html")
	if err := os.WriteFile(outside, []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := "?path=" + url.QueryEscape(outside)

	for _, route := range []string{"preview", "preview", "preview/render", "inline", "inline", "archive-entries", "sqlite"} {
		fsGet(t, ts, ts.URL+"/api/fs/"+route+q)
	}
	rows := fsRows(t, srv)
	if len(rows) != 1 || !strings.HasPrefix(rows[0], "files.read ") {
		t.Fatalf("a click's requests wrote %d rows %v, want one files.read", len(rows), rows)
	}
	for _, r := range rows {
		if !strings.Contains(r, outside) {
			t.Errorf("row %q does not name the absolute path", r)
		}
	}

	fsGet(t, ts, ts.URL+"/api/fs/download"+q)
	rows = fsRows(t, srv)
	if len(rows) != 2 || !strings.HasPrefix(rows[0], "files.download ") || !strings.Contains(rows[0], "via download") {
		t.Fatalf("after a download: %v, want a files.download row on top", rows)
	}

	// Inside a project, and a resolve: nothing.
	inside := filepath.Join(root, "notes.txt")
	before := len(fsRows(t, srv))
	fsGet(t, ts, ts.URL+"/api/fs/preview?path="+url.QueryEscape(inside))
	fsGet(t, ts, ts.URL+"/api/fs/download?path="+url.QueryEscape(inside))
	fsGet(t, ts, ts.URL+"/api/fs/resolve?target="+url.QueryEscape(outside))
	if got := len(fsRows(t, srv)); got != before {
		t.Errorf("reads inside a project and a resolve wrote %d rows", got-before)
	}

	// The window closes: a second click a minute later is a second row.
	srv.fsReads.mu.Lock()
	for k := range srv.fsReads.seen {
		srv.fsReads.seen[k] = time.Now().Add(-2 * fsReadWindow)
	}
	srv.fsReads.mu.Unlock()
	fsGet(t, ts, ts.URL+"/api/fs/preview"+q)
	if got := len(fsRows(t, srv)); got != before+1 {
		t.Errorf("a read after the window wrote %d new rows, want 1", got-before)
	}
}

func TestTheReadWindowIsBounded(t *testing.T) {
	var a fsReadAudit
	now := time.Now()
	if !a.first("k", now) || a.first("k", now.Add(time.Second)) || !a.first("k", now.Add(fsReadWindow+time.Second)) {
		t.Fatal("first must be true, then false inside the window, then true after it")
	}
	for i := 0; i < 3*fsReadCap; i++ {
		a.first(strings.Repeat("x", i%7)+string(rune('a'+i%26))+time.Duration(i).String(), now)
	}
	if len(a.seen) > fsReadCap {
		t.Errorf("the window map holds %d entries, cap is %d", len(a.seen), fsReadCap)
	}
}

// The panel's own database and key, and the credential directories, are marked
// in the row; an ordinary file is not.
func TestCredentialFilesAreMarkedInTheAuditRow(t *testing.T) {
	ts, srv, _, _ := writeFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{".ssh", ".claude", ".codex"} {
		if err := os.Mkdir(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	plain := filepath.Join(t.TempDir(), "plain.txt")
	files := map[string]bool{
		filepath.Join(home, ".ssh", "id_ed25519"):       true,
		filepath.Join(home, ".claude", "settings.json"): true,
		filepath.Join(home, ".codex", "auth.json"):      true,
		filepath.Join(srv.Cfg.DataDir, "secrets.key"):   true,
		plain: false,
	}
	for p := range files {
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fsGet(t, ts, ts.URL+"/api/fs/preview?path="+url.QueryEscape(p))
	}
	rows := strings.Join(fsRows(t, srv), "\n")
	for p, secret := range files {
		var line string
		for _, r := range strings.Split(rows, "\n") {
			if strings.Contains(r, p) {
				line = r
			}
		}
		if line == "" {
			t.Errorf("no row for %s", p)
			continue
		}
		if has := strings.Contains(line, secretMark); has != secret {
			t.Errorf("%s: marked=%v, want %v (%q)", p, has, secret, line)
		}
	}
}

func TestAWriteThroughFSFileIsAuditedAndGuarded(t *testing.T) {
	ts, srv, root, _ := writeFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.Mkdir(filepath.Join(home, ".ssh"), 0o700)

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	put := func(path, match, body string) (int, string) {
		code, resp, _ := putFile(t, ts, ts.URL+"/api/fs/file?path="+url.QueryEscape(path), match, []byte(body))
		return code, resp
	}

	// No precondition, a stale one, and a relative path: all refused, nothing written.
	if code, _ := put(outside, "", "new\n"); code != http.StatusPreconditionRequired {
		t.Errorf("no If-Match: %d, want 428", code)
	}
	if code, _ := put(outside, contentETag([]byte("something else\n")), "new\n"); code != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: %d, want 412", code)
	}
	if code, _ := put("relative.txt", contentETag([]byte("old\n")), "new\n"); code != http.StatusBadRequest {
		t.Errorf("relative path: %d, want 400", code)
	}
	if got := readFile(t, outside); got != "old\n" {
		t.Fatalf("a refused write changed the file to %q", got)
	}
	if rows := fsRows(t, srv); len(rows) != 0 {
		t.Fatalf("refused writes left rows %v", rows)
	}

	// A FIFO is not a file to write.
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := put(fifo, contentETag(nil), "x\n"); code != http.StatusBadRequest || !strings.Contains(body, "not a regular file") {
		t.Errorf("FIFO: %d %s, want 400 not a regular file", code, body)
	}

	if code, body := put(outside, contentETag([]byte("old\n")), "new\n"); code != http.StatusOK {
		t.Fatalf("matching If-Match: %d %s", code, body)
	}
	if got := readFile(t, outside); got != "new\n" {
		t.Errorf("file is %q after a good write", got)
	}

	key := filepath.Join(home, ".ssh", "authorized_keys")
	_ = os.WriteFile(key, []byte("a\n"), 0o600)
	if code, body := put(key, contentETag([]byte("a\n")), "b\n"); code != http.StatusOK {
		t.Fatalf("credential write: %d %s", code, body)
	}
	inside := filepath.Join(root, "notes.txt")
	if code, body := put(inside, contentETag([]byte("first\n")), "second\n"); code != http.StatusOK {
		t.Fatalf("in-project write: %d %s", code, body)
	}

	rows := fsRows(t, srv)
	if len(rows) != 3 {
		t.Fatalf("rows %v, want three writes", rows)
	}
	want := []struct {
		has, hasNot string
	}{
		{inside + ", 7 bytes", "outside any project"},
		{secretMark + key + " (outside any project), 2 bytes", ""},
		{outside + " (outside any project), 4 bytes", secretMark},
	}
	for i, w := range want {
		if !strings.HasPrefix(rows[i], "files.write ") || !strings.Contains(rows[i], w.has) ||
			(w.hasNot != "" && strings.Contains(rows[i], w.hasNot)) {
			t.Errorf("row %d = %q, want files.write containing %q and not %q", i, rows[i], w.has, w.hasNot)
		}
	}
}

// A page outside every project is rendered under the same containment as one
// inside: the headers are the whole of it.
func TestRenderOutsideAProjectCarriesTheSameSandbox(t *testing.T) {
	ts, _, root, at := writeFixture(t)
	page := "<script>1</script><p>x</p>"
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(outside, []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, scripts := range []string{"", "&scripts=1"} {
		_, in, _ := fsGet(t, ts, strings.Replace(at("page.html"), "/file?", "/preview/render?", 1)+scripts)
		code, out, _ := fsGet(t, ts, ts.URL+"/api/fs/preview/render?path="+url.QueryEscape(outside)+scripts)
		if code != http.StatusOK {
			t.Fatalf("render outside: %d", code)
		}
		csp := out.Get("Content-Security-Policy")
		if !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") || csp != in.Get("Content-Security-Policy") {
			t.Errorf("scripts=%q: outside CSP %q, inside %q", scripts, csp, in.Get("Content-Security-Policy"))
		}
		for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Content-Disposition", "Cache-Control", "Content-Type", "Referrer-Policy", "X-Frame-Options"} {
			if in.Get(h) != out.Get(h) {
				t.Errorf("scripts=%q: %s is %q outside a project and %q inside", scripts, h, out.Get(h), in.Get(h))
			}
		}
	}
}

// The walk in TestEverythingRequiresASession answers every route with a
// placeholder path; this one asks for a real file, so a route that checked the
// session after opening the file would still be caught.
func TestFSRoutesNeedASessionForARealFile(t *testing.T) {
	ts, _, root, _ := writeFixture(t)
	anon := anonymousClient(t)
	p := url.QueryEscape(filepath.Join(root, "notes.txt"))
	for _, route := range []string{"preview", "preview/render", "inline", "download", "archive-entries", "sqlite"} {
		res, err := anon.Get(ts.URL + "/api/fs/" + route + "?path=" + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized || strings.Contains(string(body), "first") {
			t.Errorf("GET /api/fs/%s without a session: %d", route, res.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/fs/file?path="+p, strings.NewReader("pwned\n"))
	req.Header.Set("If-Match", contentETag([]byte("first\n")))
	res, err := anon.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || readFile(t, filepath.Join(root, "notes.txt")) != "first\n" {
		t.Errorf("PUT /api/fs/file without a session: %d", res.StatusCode)
	}
}

// Every route that can show a file leaves its own row, named by route. One
// file per route, because the window would otherwise make the second route's
// missing audit look like coalescing.
func TestEveryFSReadRouteIsAudited(t *testing.T) {
	ts, srv, _, _ := writeFixture(t)
	dir := t.TempDir()
	for _, route := range []string{"preview", "preview/render", "inline", "download", "archive-entries", "sqlite"} {
		p := filepath.Join(dir, strings.ReplaceAll(route, "/", "-")+".txt")
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fsGet(t, ts, ts.URL+"/api/fs/"+route+"?path="+url.QueryEscape(p))
		want := "files.read "
		if route == "download" {
			want = "files.download "
		}
		rows := fsRows(t, srv)
		via := strings.TrimPrefix(route, "preview/")
		if route == "preview" {
			via = route
		}
		if len(rows) == 0 || !strings.HasPrefix(rows[0], want) || !strings.Contains(rows[0], p) || !strings.HasSuffix(rows[0], "via "+via) {
			t.Errorf("%s: newest row %v, want %q naming %s via %s", route, rows, want, p, via)
		}
		if n := len(rows); n != indexOf(route) {
			t.Errorf("%s: %d rows so far, want %d", route, n, indexOf(route))
		}
	}
}

func indexOf(route string) int {
	for i, r := range []string{"preview", "preview/render", "inline", "download", "archive-entries", "sqlite"} {
		if r == route {
			return i + 1
		}
	}
	return -1
}
