package pages

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalManifest = `{"sdk":1,"name":"t","sections":["sessions"]}`

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func writeTree(t *testing.T, files map[string]string) string {
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

func TestReadingAPageDirectory(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"vibepanel.json":      minimalManifest,
		"index.html":          `<script src="vibepanel.js"></script>`,
		"style.css":           "body{}",
		"img/logo.png":        string(pngBytes),
		"fixtures/busy.json":  "{}",
		"vibepanel.js":        "// an old copy",
		"vibepanel.d.ts":      "// types",
		"AGENTS.md":           "# notes",
		".vibepanel/shots/x":  "shot",
		".git/HEAD":           "ref",
		"node_modules/a/b.js": "x",
		".env":                "SECRET=1",
	})
	b, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range b.Files {
		got = append(got, f.Path)
	}
	want := "fixtures/busy.json img/logo.png index.html style.css"
	if strings.Join(got, " ") != want {
		t.Errorf("files = %v, want %s", got, want)
	}
	for _, f := range b.Files {
		if strings.Contains(f.Path, ".env") || strings.HasPrefix(f.Path, ".") {
			t.Errorf("a dot path was published: %s", f.Path)
		}
	}
	ignored := map[string]string{}
	for _, i := range b.Ignored {
		ignored[i.Path] = i.Reason
	}
	for _, p := range []string{"vibepanel.js", "vibepanel.d.ts", "AGENTS.md"} {
		if ignored[p] == "" {
			t.Errorf("%s was neither published nor reported as ignored", p)
		}
	}
}

func TestAPageDirectoryIsRefusedRatherThanTrimmed(t *testing.T) {
	base := map[string]string{"vibepanel.json": minimalManifest, "index.html": "<p>hi</p>"}
	with := func(extra map[string]string) string {
		files := map[string]string{}
		for k, v := range base {
			files[k] = v
		}
		for k, v := range extra {
			files[k] = v
		}
		return writeTree(t, files)
	}

	t.Run("no index", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"vibepanel.json": minimalManifest})
		if _, err := ReadDir(dir); !errors.Is(err, ErrNoIndex) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no manifest", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"index.html": "x"})
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), ManifestFile) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a script that is really a binary", func(t *testing.T) {
		dir := with(map[string]string{"app.js": "var a = 1\x00\x00"})
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "what its name says") {
			t.Fatalf("err = %v", err)
		}
	})
	// The one that matters: a file named as an image that is really markup.
	t.Run("an image that is really text", func(t *testing.T) {
		dir := with(map[string]string{"logo.png": "<script>alert(1)</script>"})
		if _, err := ReadDir(dir); err == nil {
			t.Fatal("a .png holding markup was accepted")
		}
	})
	t.Run("too large", func(t *testing.T) {
		dir := with(map[string]string{"big.txt": strings.Repeat("a", MaxFileBytes+1)})
		if _, err := ReadDir(dir); err == nil {
			t.Fatal("an oversized file was accepted")
		}
	})
	t.Run("too many", func(t *testing.T) {
		extra := map[string]string{}
		for i := 0; i < MaxFiles; i++ {
			extra[filepath.Join("many", strings.Repeat("a", 1+i%5)+string(rune('a'+i%26))+strings.Repeat("b", i/26)+".txt")] = "x"
		}
		dir := with(extra)
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("too much in total", func(t *testing.T) {
		extra := map[string]string{}
		for i := 0; i < 3; i++ {
			extra["part"+string(rune('a'+i))+".txt"] = strings.Repeat("a", MaxFileBytes)
		}
		dir := with(extra)
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "in total") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("odd characters", func(t *testing.T) {
		dir := with(map[string]string{"my page.css": "x"})
		if _, err := ReadDir(dir); err == nil {
			t.Fatal("a path with a space was accepted")
		}
	})
	t.Run("a symlink", func(t *testing.T) {
		dir := with(nil)
		outside := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "secret.txt")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("err = %v", err)
		}
	})
}

// The Preview pane reads one file at a time and must agree with the publish:
// a file it would show and publish would refuse is a preview of something that
// cannot ship. And a refused path answers exactly like an absent one.
func TestReadingOneDraftFileAgreesWithThePublish(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"vibepanel.json": minimalManifest, "index.html": "hi", "css/a.css": "x", ".env": "SECRET=1",
	})
	outside := filepath.Join(t.TempDir(), "passwd.txt")
	if err := os.WriteFile(outside, []byte("root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "out.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "index.html"), filepath.Join(dir, "in.html")); err != nil {
		t.Fatal(err)
	}
	// A symlinked directory that stays inside the root. The file at the end of
	// it is a regular file, so only the path comparison refuses it -- and the
	// publish refuses the link, so the preview must too.
	if err := os.Symlink(filepath.Join(dir, "css"), filepath.Join(dir, "styles")); err != nil {
		t.Fatal(err)
	}

	if f, err := ReadFile(dir, "css/a.css"); err != nil || string(f.Data) != "x" {
		t.Fatalf("a plain file: %v %q", err, f.Data)
	}
	for _, rel := range []string{"out.txt", "in.html", "styles/a.css", ".env", "../passwd.txt", "css/../index.html",
		"vibepanel.json", "vibepanel.js", "/etc/passwd", "missing.html", "a\\b.css"} {
		if _, err := ReadFile(dir, rel); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile(%q) = %v, want not-exist", rel, err)
		}
	}
}

func TestTheFingerprintMovesWhenAFileChanges(t *testing.T) {
	dir := writeTree(t, map[string]string{"vibepanel.json": minimalManifest, "index.html": "one"})
	a, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("two!"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := Fingerprint(dir)
	if a == b {
		t.Error("editing index.html did not change the fingerprint")
	}
	if err := os.WriteFile(filepath.Join(dir, "vibepanel.json"), []byte(minimalManifest+" "), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := Fingerprint(dir)
	if b == c {
		t.Error("editing the manifest did not change the fingerprint")
	}
	if err := os.WriteFile(filepath.Join(dir, ".scratch"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := Fingerprint(dir)
	if c != d {
		t.Error("a dotfile changed the fingerprint; it is not part of the page")
	}
}
