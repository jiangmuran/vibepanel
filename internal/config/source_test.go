package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Placed here for the reason given at the top of deps_test.go: this asserts a
// property of the repository rather than of this package, and needs to live
// somewhere `go test ./...` reaches.
//
// Source files must not contain Unicode bidirectional overrides.
//
// These characters reorder the text after them without changing a byte, so a
// line can be displayed to a reviewer in an order that does not match what the
// compiler reads — the Trojan Source class, CVE-2021-42574. The panel already
// takes this seriously for text it renders: `safeText` strips exactly this
// family out of session titles and file names, because a file called
// `report\u202Efdp.exe` is shown by every browser as `reportexe.pdf` next to a
// download button.
//
// It was not taking it seriously for its own source. The check exists because
// the first draft of the build-log entry *about* that fix pasted the real
// character in as an example, which silently reversed the rest of the line in
// every renderer that shows the file. If it can arrive by accident while
// writing the paragraph warning about it, it can arrive in a patch.
//
// It then failed on this very comment the first time it ran, for the same
// reason. Three accidents in one afternoon, all of them while writing about
// the character. Hence the escape above.
func TestNoBidirectionalOverridesInSource(t *testing.T) {
	// The attack-relevant set: the embedding/override pairs and the isolates.
	// Not the plain marks (200E/200F), which do not reorder a run and do turn
	// up legitimately in prose.
	forbidden := map[rune]string{
		0x202A: "LEFT-TO-RIGHT EMBEDDING",
		0x202B: "RIGHT-TO-LEFT EMBEDDING",
		0x202C: "POP DIRECTIONAL FORMATTING",
		0x202D: "LEFT-TO-RIGHT OVERRIDE",
		0x202E: "RIGHT-TO-LEFT OVERRIDE",
		0x2066: "LEFT-TO-RIGHT ISOLATE",
		0x2067: "RIGHT-TO-LEFT ISOLATE",
		0x2068: "FIRST STRONG ISOLATE",
		0x2069: "POP DIRECTIONAL ISOLATE",
	}

	// Extensions rather than a binary sniff: the point is to cover what a human
	// reviews, and a sniff would quietly start skipping a file the day it gained
	// a stray byte.
	text := map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".mjs": true,
		".css": true, ".html": true, ".md": true, ".json": true, ".sh": true,
		".yml": true, ".yaml": true, ".sql": true, ".conf": true, ".service": true,
	}
	named := map[string]bool{
		"Makefile": true, "Dockerfile": true, "go.mod": true, "go.sum": true,
		".gitignore": true, ".dockerignore": true,
	}

	root := "../.."
	skipDir := map[string]bool{
		".git": true, "node_modules": true, "dist": true, "testdata": true,
	}

	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !text[strings.ToLower(filepath.Ext(name))] && !named[name] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		for i, r := range string(b) {
			if what, bad := forbidden[r]; bad {
				line := 1 + strings.Count(string(b[:i]), "\n")
				t.Errorf("%s:%d contains U+%04X %s\n"+
					"this reorders the rest of the line when displayed, so what a reviewer "+
					"reads is not what the compiler does; write it as an escape instead",
					path, line, r, what)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// A walk that silently matched nothing would pass forever.
	if checked < 50 {
		t.Fatalf("only %d files checked; the walk is not reaching the repository", checked)
	}
}
