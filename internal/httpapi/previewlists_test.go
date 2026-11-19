package httpapi

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The two listing routes through the server: what each answers, what each
// refuses and with which status, and that the preview route sends neither
// kind's bytes.
func TestTheListingRoutes(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	write := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixtures := filepath.Join("..", "..", "web", "scripts", "fixtures", "preview", "files", "archives")
	for _, n := range []string{"bundle.zip", "bundle.tar.gz"} {
		b, err := os.ReadFile(filepath.Join(fixtures, n))
		if err != nil {
			t.Fatal(err)
		}
		write(n, b)
	}
	var many bytes.Buffer
	z := zip.NewWriter(&many)
	for i := 0; i < browse.ArchiveMaxEntries+1; i++ {
		_, _ = z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("f%d", i), Method: zip.Store})
	}
	_ = z.Close()
	write("many.zip", many.Bytes())
	write("broken.zip", append([]byte("PK\x03\x04"), bytes.Repeat([]byte{1}, 100)...))
	write("notes.txt", []byte("just text\n"))
	write("fake.db", append([]byte("SQLite format 3\x00"), make([]byte, 2032)...))
	db, err := sql.Open("sqlite", filepath.Join(root, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`PRAGMA journal_mode=WAL`, `CREATE TABLE t (a TEXT, b BLOB)`, `INSERT INTO t VALUES ('one', x'00ff')`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"lists"}`)
	get := func(route, path, extra string) (*http.Response, []byte) {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/" + route + "?path=" + url.QueryEscape(path) + extra)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	before, _ := os.ReadDir(root)

	res, body := get("archive-entries", "bundle.zip", "")
	var l browse.ArchiveListing
	if err := json.Unmarshal(body, &l); err != nil || res.StatusCode != 200 || l.Format != "zip" || len(l.Entries) != 2 {
		t.Errorf("zip: %d %s", res.StatusCode, body)
	}
	res, body = get("archive-entries", "bundle.tar.gz", "")
	if res.StatusCode != 200 || !strings.Contains(string(body), `"format":"tar.gz"`) {
		t.Errorf("tar.gz: %d %s", res.StatusCode, body)
	}
	res, body = get("archive-entries", "many.zip", "")
	if res.StatusCode != 200 || res.Header.Get("X-Preview-Truncated") != "true" || !strings.Contains(string(body), `"truncated":true`) {
		t.Errorf("many: %d %q", res.StatusCode, res.Header.Get("X-Preview-Truncated"))
	}
	for _, c := range []struct {
		route, path, extra string
		code               int
		says               string
	}{
		{"archive-entries", "broken.zip", "", 422, "damaged"},
		{"archive-entries", "notes.txt", "", 415, "not a zip"},
		{"archive-entries", "../outside.zip", "", 0, ""},
		{"sqlite", "notes.txt", "", 415, "not a SQLite"},
		{"sqlite", "fake.db", "", 422, "database"},
		{"sqlite", "app.db", "&table=" + url.QueryEscape(`t"; DROP TABLE t; --`), 400, "no table or view"},
		{"sqlite", "app.db", "&table=" + url.QueryEscape("PRAGMA writable_schema=1"), 400, "no table or view"},
	} {
		res, body := get(c.route, c.path, c.extra)
		if c.code == 0 {
			// Outside the project: refused by the resolver, whatever it says.
			if res.StatusCode < 400 || res.StatusCode >= 500 {
				t.Errorf("%s %s: %d", c.route, c.path, res.StatusCode)
			}
			continue
		}
		var e struct{ Error string }
		_ = json.Unmarshal(body, &e)
		if res.StatusCode != c.code || !strings.Contains(e.Error, c.says) {
			t.Errorf("%s %s%s: %d %s, want %d saying %q", c.route, c.path, c.extra, res.StatusCode, body, c.code, c.says)
		}
	}

	res, body = get("sqlite", "app.db", "&table=t")
	var d browse.DBListing
	if err := json.Unmarshal(body, &d); err != nil || res.StatusCode != 200 || d.Table != "t" || len(d.Rows) != 1 {
		t.Fatalf("sqlite: %d %s", res.StatusCode, body)
	}
	if b, _ := json.Marshal(d.Rows[0]); string(b) != `["one",{"blob":2}]` {
		t.Errorf("row %s", b)
	}

	// The preview route says what each is and sends none of its bytes; the
	// viewer reads them from the routes above.
	for path, kind := range map[string]string{"bundle.zip": "archive", "app.db": "sqlite"} {
		res, body := get("preview", path, "")
		if res.Header.Get("X-Preview-Kind") != kind || len(body) != 0 {
			t.Errorf("preview %s: %q with %d bytes, want %s and none", path, res.Header.Get("X-Preview-Kind"), len(body), kind)
		}
	}

	// Nothing was left in the project: no -wal, no -shm, no journal.
	after, _ := os.ReadDir(root)
	names := func(es []os.DirEntry) (out []string) {
		for _, e := range es {
			out = append(out, e.Name())
		}
		return
	}
	if !slices.Equal(names(before), names(after)) {
		t.Errorf("the project held %v and holds %v", names(before), names(after))
	}
}
