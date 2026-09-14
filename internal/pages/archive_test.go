package pages

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func zipOf(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const archiveManifest = `{"sdk":1,"name":"Lobby","sections":["sessions"]}`

func TestAnExportedPageImportsAsTheSamePage(t *testing.T) {
	var buf bytes.Buffer
	files := []File{
		{Path: "index.html", Data: []byte("<!doctype html><p>hi</p>")},
		{Path: "js/app.js", Data: []byte("console.log(1)")},
	}
	if err := WriteArchive(&buf, []byte(archiveManifest), files); err != nil {
		t.Fatal(err)
	}
	b, err := ReadArchive(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if b.Manifest.Name != "Lobby" || len(b.Files) != 2 || string(b.Files[1].Data) != "console.log(1)" {
		t.Errorf("round trip = %+v", b)
	}
	var again bytes.Buffer
	_ = WriteArchive(&again, []byte(archiveManifest), files)
	if !bytes.Equal(buf.Bytes(), again.Bytes()) {
		t.Error("exporting the same page twice gave different bytes")
	}
}

// "Compress this folder" wraps everything in the folder's name, and a zip of a
// whole page directory carries the companions a page never serves.
func TestAZippedPageDirectoryImports(t *testing.T) {
	b, err := ReadArchive(zipOf(t, map[string]string{
		"page-lobby/vibepanel.json":     archiveManifest,
		"page-lobby/index.html":         "<p>hi</p>",
		"page-lobby/AGENTS.md":          "# notes",
		"page-lobby/vibepanel.js":       "old sdk",
		"page-lobby/.git/config":        "[core]",
		"__MACOSX/page-lobby/._index":   "junk",
		"page-lobby/fixtures/busy.json": `{"v":1}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Files) != 2 || !b.Has("index.html") || !b.Has("fixtures/busy.json") {
		t.Errorf("files = %+v", b.Files)
	}
}

func TestAnArchiveIsReadByThePublishRules(t *testing.T) {
	for _, tc := range []struct {
		why     string
		entries map[string]string
		want    string
	}{
		{"no manifest", map[string]string{"index.html": "x"}, "no vibepanel.json"},
		{"no index", map[string]string{"vibepanel.json": archiveManifest, "a.css": "x"}, "index.html"},
		{"a path out of the page", map[string]string{"vibepanel.json": archiveManifest,
			"index.html": "x", "../evil.js": "x"}, "a path in a page"},
		{"a script named as an image", map[string]string{"vibepanel.json": archiveManifest,
			"index.html": "x", "logo.png": "alert(1)"}, "does not contain what its name says"},
		{"a manifest that does not validate", map[string]string{"vibepanel.json": `{"sdk":1}`,
			"index.html": "x"}, "name is required"},
		{"a file over the limit", map[string]string{"vibepanel.json": archiveManifest,
			"index.html": strings.Repeat("a", MaxFileBytes+1)}, "larger than"},
	} {
		_, err := ReadArchive(zipOf(t, tc.entries))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.why, err, tc.want)
		}
	}
	if _, err := ReadArchive([]byte("not a zip")); err == nil {
		t.Error("garbage was read as an archive")
	}
}
