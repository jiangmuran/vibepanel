package pages

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAScaffoldIsAWorkingPage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lobby")
	fixtures := map[string][]byte{"busy": []byte(`{"status":"live"}`)}
	if err := Scaffold(dir, "blank", "大厅 wall", fixtures); err != nil {
		t.Fatal(err)
	}
	b, err := ReadDir(dir)
	if err != nil {
		t.Fatalf("a fresh scaffold is not a readable page: %v", err)
	}
	if b.Manifest.Name != "大厅 wall" {
		t.Errorf("manifest name = %q", b.Manifest.Name)
	}
	for _, p := range []string{"AGENTS.md", "CLAUDE.md", "README.md", ".gitignore", SDKFile, TypesFile, ArchitectureFile, "fixtures/busy.json"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s was not written: %v", p, err)
		}
	}
	if !SDKCurrent(dir) {
		t.Error("a fresh scaffold's SDK copy is not current")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	if !strings.HasPrefix(string(raw), "{\n  \"sdk\": 1,\n  \"name\"") {
		t.Errorf("the rewritten manifest does not read top-down:\n%s", raw)
	}
}

// A scaffold pointed at somebody's work must not delete it.
func TestAScaffoldNeverWritesOverAnything(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "index.html")
	if err := os.WriteFile(mine, []byte("my work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Scaffold(dir, "blank", "x", nil); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(mine); string(data) != "my work" {
		t.Errorf("the file became %q", data)
	}
	if err := Scaffold(filepath.Join(t.TempDir(), "x"), "nope", "x", nil); err == nil {
		t.Error("an unknown template was accepted")
	}
	if err := writeNew(mine, []byte("again")); err == nil {
		t.Error("writeNew replaced an existing file")
	}
}

func TestAForkCopiesThePageUnderANewName(t *testing.T) {
	src := writeTree(t, map[string]string{
		"vibepanel.json": minimalManifest, "index.html": "<p>original</p>",
		"fixtures/busy.json": `{"mine":true}`,
	})
	b, err := ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "fork")
	if err := ScaffoldFrom(dst, "Fork", b, map[string][]byte{"busy": []byte("{}"), "empty": []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	nb, err := ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	if nb.Manifest.Name != "Fork" {
		t.Errorf("name = %q", nb.Manifest.Name)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "fixtures", "busy.json")); string(data) != `{"mine":true}` {
		t.Errorf("an edited fixture was replaced: %s", data)
	}
	if _, err := os.Stat(filepath.Join(dst, "fixtures", "empty.json")); err != nil {
		t.Error("a missing fixture was not added")
	}
}

func TestTheMetaDirectoryIsNotFollowedThroughALink(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(dir, MetaDir)); err != nil {
		t.Fatal(err)
	}
	if err := WriteMeta(dir, "errors.json", []byte("{}")); err == nil {
		t.Error("wrote through a symlinked .vibepanel")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "errors.json")); err == nil {
		t.Error("the file landed where the link pointed")
	}
	if err := WriteMeta(t.TempDir(), "../x", []byte("{}")); err == nil {
		t.Error("a meta name with a slash was accepted")
	}

	clean := t.TempDir()
	if err := AppendHistory(clean, "v1 · first"); err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(clean, "v2 · second\nwith a newline"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(clean, MetaDir, "HISTORY.md"))
	if strings.Count(string(data), "\n- ") != 2 || !strings.Contains(string(data), "second with a newline") {
		t.Errorf("history:\n%s", data)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Lobby wall": "lobby-wall", "大厅": "page", "  --A__b--  ": "a-b",
		strings.Repeat("x", 60): strings.Repeat("x", 40),
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// A template's manifest is rewritten with the page's name, and a rewrite that
// kept only the keys it knew dropped the kiosk's data and actions: a page that
// linted clean and had nothing to vote with.
func TestAScaffoldKeepsEveryManifestKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kiosk")
	if err := Scaffold(dir, "kiosk", "Front desk", nil); err != nil {
		t.Fatal(err)
	}
	b, problems := LintDir(dir)
	if len(problems) != 0 {
		t.Errorf("problems = %v", problems)
	}
	m := b.Manifest
	if m.Name != "Front desk" || len(m.Data) != 4 || len(m.Actions) != 2 || m.Admin == nil || m.Admin.Entry != "admin/index.html" {
		t.Errorf("the kiosk's manifest after scaffolding: %+v", m)
	}
	if !m.Capabilities().VisitorActions {
		t.Error("the kiosk has no visitor actions")
	}
}

// ARCHITECTURE.md in a page directory is docs/page-backend.md, byte for byte.
// Two copies of a security design that are allowed to differ are two designs,
// and the one an agent reads is the one a reviewer never opens.
func TestArchitectureIsThePageBackendDocument(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "page-backend.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != string(Architecture) {
		t.Error("internal/pages/scaffold/ARCHITECTURE.md differs from docs/page-backend.md; " +
			"copy the document over it")
	}
}
