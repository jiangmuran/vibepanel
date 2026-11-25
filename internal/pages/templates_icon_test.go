package pages

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A share page with no icon of its own makes the browser ask for /favicon.ico
// at the panel's root, and the page's own CSP refuses it: Firefox logged a
// Content-Security-Policy error on every load of every wall. An explicit
// inline `data:` icon satisfies img-src without triggering a /favicon.ico
// request and gives browser tabs a real favicon instead of a blank globe.
// Every built-in template, and every page in one.
func TestEveryTemplatePageDeclaresItsIcon(t *testing.T) {
	err := filepath.WalkDir("templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), `rel="icon"`) || strings.Contains(string(b), `href="data:,"`) {
			t.Errorf("%s must declare a non-empty inline icon so the tab has a favicon without asking for /favicon.ico", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
