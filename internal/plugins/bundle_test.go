package plugins

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const paneManifest = `{"plugin":1,"id":"pane","name":{"en":"Pane"},"version":"1.0.0",
  "theme":{"file":"theme.css","name":{"en":"Paper"}},
  "panels":[{"slot":"sidepanel.pane","entry":"pane.html","title":{"en":"Pane"}}]}`

const paneTheme = ":root[data-theme='ext-pane'] { --vp-bg: #fff; }"

func writeDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadDirReadsAPlugin(t *testing.T) {
	dir := writeDir(t, map[string]string{
		ManifestFile: paneManifest, "theme.css": paneTheme, "pane.html": "<h1>hi</h1>",
		"lib/util.cjs": "module.exports = 1", "AGENTS.md": "# notes", "README.md": "x",
		SDKFile: "// stale copy", "notes.txt": "x", "bin/tool": "\x7fELF", ".git/config": "x",
	})
	b, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range b.Files {
		paths = append(paths, f.Path)
	}
	if got := strings.Join(paths, ","); got != "lib/util.cjs,notes.txt,pane.html,theme.css" {
		t.Errorf("files = %s", got)
	}
	ignored := map[string]string{}
	for _, i := range b.Ignored {
		ignored[i.Path] = i.Reason
	}
	if ignored["AGENTS.md"] == "" || ignored[SDKFile] == "" || ignored["bin/tool"] == "" {
		t.Errorf("ignored = %v; the scaffold's files, a stale SDK and a binary are left out with a reason", ignored)
	}
	if b.Hash() == "" || b.Hash() != b.Hash() {
		t.Error("hash")
	}
}

func TestReadDirRefuses(t *testing.T) {
	t.Run("a symlink", func(t *testing.T) {
		dir := writeDir(t, map[string]string{ManifestFile: paneManifest, "theme.css": paneTheme, "pane.html": "x"})
		if err := os.Symlink("/etc/hostname", filepath.Join(dir, "leak.txt")); err != nil {
			t.Skip(err)
		}
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a missing entry", func(t *testing.T) {
		dir := writeDir(t, map[string]string{ManifestFile: paneManifest, "theme.css": paneTheme})
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "pane.html") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a theme that fails its lint", func(t *testing.T) {
		dir := writeDir(t, map[string]string{ManifestFile: paneManifest, "pane.html": "x",
			"theme.css": ".vp-control { color: red }"})
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "theme.css") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a file that is not what its name says", func(t *testing.T) {
		dir := writeDir(t, map[string]string{ManifestFile: paneManifest, "theme.css": paneTheme, "pane.html": "x",
			"a.png": "not a png"})
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "what its name says") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no manifest", func(t *testing.T) {
		if _, err := ReadDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), ManifestFile) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestReadFileAgreesWithReadDir(t *testing.T) {
	dir := writeDir(t, map[string]string{ManifestFile: paneManifest, "theme.css": paneTheme, "pane.html": "<p>x</p>"})
	f, err := ReadFile(dir, "pane.html")
	if err != nil || string(f.Data) != "<p>x</p>" {
		t.Fatalf("ReadFile: %v %q", err, f.Data)
	}
	for _, rel := range []string{ManifestFile, "AGENTS.md", "../x", ".git/config", "nope.html", "node_modules/a.js"} {
		if _, err := ReadFile(dir, rel); err == nil {
			t.Errorf("%s was served", rel)
		}
	}
}

func TestArchiveRoundTrips(t *testing.T) {
	dir := writeDir(t, map[string]string{ManifestFile: paneManifest, "theme.css": paneTheme, "pane.html": "<p>x</p>",
		"lib/a.js": "1"})
	b, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteArchive(&buf, b.Raw, b.Files); err != nil {
		t.Fatal(err)
	}
	back, err := ReadArchive(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if back.Hash() != b.Hash() {
		t.Errorf("hash changed across the archive: %s vs %s", back.Hash(), b.Hash())
	}
	if back.Manifest.ID != "pane" || len(back.Files) != 3 {
		t.Errorf("read back %+v", back)
	}
}

func TestAnArchiveIsReadByThePublishRules(t *testing.T) {
	zipOf := func(files map[string]string) []byte {
		var buf bytes.Buffer
		var list []File
		for p, body := range files {
			if p == ManifestFile {
				continue
			}
			list = append(list, File{Path: p, Data: []byte(body)})
		}
		_ = WriteArchive(&buf, []byte(files[ManifestFile]), list)
		return buf.Bytes()
	}
	if _, err := ReadArchive(zipOf(map[string]string{"theme.css": paneTheme, "pane.html": "x"})); err == nil ||
		!strings.Contains(err.Error(), ManifestFile) {
		t.Errorf("no manifest: %v", err)
	}
	if _, err := ReadArchive(zipOf(map[string]string{ManifestFile: paneManifest, "theme.css": paneTheme})); err == nil ||
		!strings.Contains(err.Error(), "pane.html") {
		t.Errorf("missing entry: %v", err)
	}
	if _, err := ReadArchive([]byte("not a zip")); err == nil || !strings.Contains(err.Error(), "zip") {
		t.Errorf("not a zip: %v", err)
	}
}

func TestValidPath(t *testing.T) {
	for _, ok := range []string{"a.html", "lib/a.cjs", "img/x.png", "deep/er/x.json", "README2.md"} {
		if !ValidPath(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"", "/a.html", "../a.html", ".hidden.html", "a/.b/c.html", "node_modules/a.js",
		ManifestFile, SDKFile, TypesFile, UIFile, "AGENTS.md", "tool", "a.exe", "a b.html", "a.wasm"} {
		if ValidPath(bad) {
			t.Errorf("%s accepted", bad)
		}
	}
}
