package pages

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lintOf(t *testing.T, manifest string, files map[string]string) []Problem {
	t.Helper()
	all := map[string]string{"vibepanel.json": manifest}
	for k, v := range files {
		all[k] = v
	}
	_, problems := LintDir(writeTree(t, all))
	return problems
}

func codes(ps []Problem) map[string]int {
	out := map[string]int{}
	for _, p := range ps {
		out[p.Code]++
	}
	return out
}

func TestLintNamesWhatThePolicyWillRefuse(t *testing.T) {
	ps := lintOf(t, minimalManifest, map[string]string{
		"index.html": `<!doctype html>
<script src="vibepanel.js"></script>
<script src="https://unpkg.com/react.js"></script>
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2">
<a href="https://example.com">docs</a>
<form></form>
<script>
  el.innerHTML = row.name
  localStorage.setItem('x', 1)
  fetch('https://api.example.com/x')
  eval('1')
  alert('hi')
</script>`,
		"style.css": `@font-face { src: url("https://fonts.gstatic.com/x.woff2") }`,
	})
	got := codes(ps)
	for code, n := range map[string]int{
		"external-script": 2, "external-request": 3, "external-link": 1, "html-sink": 1,
		"storage": 1, "eval": 1, "modal": 1, "form": 1,
	} {
		if got[code] != n {
			t.Errorf("%s reported %d times, want %d (all: %v)", code, got[code], n, got)
		}
	}
	for _, p := range ps {
		if p.Line == 0 && p.Code != "no-sdk" {
			t.Errorf("%s has no line number", p.Code)
		}
		if p.Fix == "" {
			t.Errorf("%s says what is wrong and not what to do", p.Code)
		}
	}
	// The allowed host, named as the fix rather than as a refusal.
	for _, p := range ps {
		if p.Code == "external-script" && strings.HasPrefix(p.Message, "a script from cdn.jsdelivr.net") &&
			!strings.Contains(p.Fix, "scriptHosts") {
			t.Errorf("an allowed host's fix does not point at scriptHosts: %s", p.Fix)
		}
	}
}

func TestLintAcceptsTheHostTheManifestAllows(t *testing.T) {
	ps := lintOf(t, `{"sdk":1,"name":"t","scriptHosts":["cdn.jsdelivr.net"]}`, map[string]string{
		"index.html": `<script src="vibepanel.js"></script><script src="https://cdn.jsdelivr.net/npm/chart.js"></script>`,
	})
	if len(ps) != 0 {
		t.Errorf("problems on a page that does what it may: %v", ps)
	}
}

func TestLintNoticesAPageWithNoSDK(t *testing.T) {
	ps := lintOf(t, minimalManifest, map[string]string{"index.html": "<p>hello</p>"})
	if codes(ps)["no-sdk"] != 1 {
		t.Errorf("problems = %v", ps)
	}
}

// Every template is what an agent starts from and copies the habits of. One
// that trips the lint teaches the habit the lint exists to stop.
func TestEveryTemplateIsCleanAndValid(t *testing.T) {
	tpls := Templates()
	if len(tpls) == 0 {
		t.Fatal("no templates; the embed is empty")
	}
	for _, tpl := range tpls {
		t.Run(tpl.ID, func(t *testing.T) {
			tfs, err := TemplateFS(tpl.ID)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			err = fs.WalkDir(tfs, ".", func(p string, d fs.DirEntry, werr error) error {
				if werr != nil || d.IsDir() {
					return werr
				}
				data, rerr := fs.ReadFile(tfs, p)
				if rerr != nil {
					return rerr
				}
				full := filepath.Join(dir, filepath.FromSlash(p))
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return err
				}
				return os.WriteFile(full, data, 0o644)
			})
			if err != nil {
				t.Fatal(err)
			}
			b, problems := LintDir(dir)
			if b.Files == nil {
				t.Fatalf("the template is not a readable page: %v", problems)
			}
			for _, p := range problems {
				t.Errorf("%s", p)
			}
		})
	}
}
