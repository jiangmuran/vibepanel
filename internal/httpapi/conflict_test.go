package httpapi_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merge left a pair of conflict markers committed inside docs/api.md
// and nothing noticed for two merges. Every check passed: the file is only read
// by TestTheAPIDocCoversEveryRoute, which looks for lines starting with "### ",
// and a conflict marker is not one. The half of the document the marker
// swallowed simply stopped being checked, and the webhook section was later
// dropped by a third merge that inherited the mess.
//
// Markers are not valid in any language here, so this needs no per-type rule.
func TestNoConflictMarkersAreCommitted(t *testing.T) {
	// Not "<<<<<<<" written inline: this file would then fail on itself.
	markers := []string{
		strings.Repeat("<", 7) + " ",
		strings.Repeat(">", 7) + " ",
		"\n" + strings.Repeat("=", 7) + "\n",
	}

	skip := map[string]bool{
		".git": true, "node_modules": true, "dist": true,
		".claude": true, "web/dist": true,
	}

	root := "../.."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".ts", ".tsx", ".css", ".md", ".sh", ".mjs", ".js", ".json", ".sql", ".conf", ".yml", ".yaml":
		default:
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		for _, m := range markers {
			if strings.Contains(text, m) {
				rel, _ := filepath.Rel(root, path)
				line := 1 + strings.Count(text[:strings.Index(text, m)], "\n")
				t.Errorf("%s:%d has a conflict marker committed in it. "+
					"A resolution was left half-done; the file is not what either "+
					"side meant it to be.", rel, line)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
