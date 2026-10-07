package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every template is a plugin the panel would install without a refusal, with
// every file its manifest names present, its theme passing the lint, and
// nothing in it the bundle reader would silently ignore. A template that
// drifts from the manifest or the SDK is the first thing a new author sees
// fail, so this is pinned here rather than found by them.
func TestEveryTemplateIsAPluginThatInstalls(t *testing.T) {
	list := Templates()
	if len(list) != len(templateOrder) {
		t.Fatalf("Templates() lists %d of %d: one did not parse", len(list), len(templateOrder))
	}
	for _, tpl := range list {
		t.Run(tpl.ID, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "p")
			if err := Scaffold(dir, tpl.ID, "my-"+tpl.ID, "My "+tpl.ID); err != nil {
				t.Fatalf("scaffold: %v", err)
			}
			b, err := ReadDir(dir)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if b.Manifest.ID != "my-"+tpl.ID || b.Manifest.Name.EN != "My "+tpl.ID {
				t.Errorf("id/name not filled: %q %q", b.Manifest.ID, b.Manifest.Name.EN)
			}
			// The spec and the SDK copies are ignored on purpose, with that
			// reason; anything else ignored is a template file that does not
			// reach the panel.
			for _, ig := range b.Ignored {
				if ig.Reason != "the panel provides its own" {
					t.Errorf("the reader ignores %s: %s", ig.Path, ig.Reason)
				}
			}
			if sc := Describe(b.Manifest, nil, "99.0.0"); sc.Refused != nil {
				t.Errorf("the install screen refuses it: %s", sc.Refused.Text.EN)
			}
			if b.Manifest.Theme != nil {
				css, _ := os.ReadFile(filepath.Join(dir, b.Manifest.Theme.File)) //nolint:gosec // test dir
				if _, err := LintTheme(css, b.Manifest.ID); err != nil {
					t.Errorf("theme lint: %v", err)
				}
			}
			for _, must := range []string{"AGENTS.md", "CLAUDE.md", "README.md", TypesFile, SDKFile, ".gitignore", ManifestFile} {
				if _, err := os.Stat(filepath.Join(dir, must)); err != nil {
					t.Errorf("missing %s", must)
				}
			}
			if strings.Contains(string(mustRead(t, filepath.Join(dir, ManifestFile))), "__") {
				t.Error("a placeholder survived in plugin.json")
			}
		})
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p) //nolint:gosec // test dir
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The template's rungs are read from its manifest, so the gallery says what
// each one climbs without a second list to keep in step.
func TestTemplatesSayTheirRungs(t *testing.T) {
	got := map[string]Rungs{}
	for _, tpl := range Templates() {
		got[tpl.ID] = tpl.Rungs
	}
	if !got["theme"].Theme || got["theme"].Panel || got["theme"].Service {
		t.Errorf("theme: %+v", got["theme"])
	}
	if !got["pane"].Panel || got["pane"].Service {
		t.Errorf("pane: %+v", got["pane"])
	}
	if !got["service"].Service || !got["service"].Panel {
		t.Errorf("service: %+v", got["service"])
	}
	if !got["process"].Process || got["process"].Panel {
		t.Errorf("process: %+v", got["process"])
	}
	if f := got["full"]; !f.Theme || !f.Panel || !f.Service {
		t.Errorf("full: %+v", f)
	}
}

// Nothing is ever overwritten: the directory may be somebody's work.
func TestScaffoldRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Scaffold(dir, "pane", "my-pane", "")
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v, want the not-empty refusal", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err == nil {
		t.Error("it wrote plugin.json beside the person's file")
	}
	if err := Scaffold(filepath.Join(t.TempDir(), "x"), "nope", "my-pane", ""); err == nil || !strings.Contains(err.Error(), "no template") {
		t.Errorf("unknown template: %v", err)
	}
	if err := Scaffold(filepath.Join(t.TempDir(), "x"), "pane", "Bad ID", ""); err == nil {
		t.Error("an invalid id was accepted")
	}
}

// The spec an agent reads carries the sentences that matter: what the sandbox
// enforces and what is not theirs to do.
func TestTheScaffoldedSpecSaysWhatTheSandboxEnforces(t *testing.T) {
	spec := strings.Join(strings.Fields(string(AgentsFile)), " ")
	for _, must := range []string{
		"handle", "capabilities", "Degrade; do not ask", "Do not **install, grant or enable**",
		"vibepanel plugin check", "vibepanel plugin describe", "server.js", "VIBEPANEL_PLUGIN_URL",
	} {
		if !strings.Contains(spec, must) {
			t.Errorf("AGENTS.md does not say %q", must)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Stand-up board":               "stand-up-board",
		"  My Plugin!!  ":              "my-plugin",
		"站会":                           "plugin",
		"3d view":                      "plugin-3d-view",
		"ab":                           "plugin-ab",
		"x":                            "plugin-x",
		strings.Repeat("a", 60) + " b": strings.Repeat("a", 40),
	} {
		if got := Slug(in); got != want || !ValidID(got) {
			t.Errorf("Slug(%q) = %q (valid %v), want %q", in, got, ValidID(got), want)
		}
	}
}
