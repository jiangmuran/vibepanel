package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A session must not disappear because of the name of the directory it is in.
//
// The field separator is 0x1f and list-sessions puts one session per line, so a
// working directory containing either takes the record apart: parseInfo counts
// the wrong number of fields and the line is dropped. Measured before this was
// fixed: two of three sessions vanished from the listing — the one under a
// directory with a newline in its name and the one with a 0x1f.
//
// Vanishing is not the end of it. The poller treats a session it cannot see as
// gone and writes that to the database, so a running session is announced as
// dead because of where it happens to be sitting.
func TestSessionsSurviveControlCharactersInTheirPath(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.EnsureServer(ctx); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dirs := map[string]string{
		"vp_plain":   base,
		"vp_newline": filepath.Join(base, "c\nd"),
		"vp_unit":    filepath.Join(base, "a\x1fb"),
	}
	for name, dir := range dirs {
		if dir != base {
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatalf("mkdir %q: %v", dir, err)
			}
		}
		if err := c.Create(ctx, CreateOptions{
			Name: name, Dir: dir, Width: 80, Height: 24,
			Command: []string{"sleep", "60"},
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	time.Sleep(1500 * time.Millisecond)

	infos, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]Info{}
	for _, i := range infos {
		seen[i.Name] = i
	}
	for name := range dirs {
		info, ok := seen[name]
		if !ok {
			t.Errorf("%s is missing from the listing; the panel would call a running session dead",
				name)
			continue
		}
		// The control character is replaced rather than carried through: a
		// value that can still take a record apart has only moved the problem.
		if strings.ContainsAny(info.Path, "\x00\x01\x1f\n\t") {
			t.Errorf("%s kept a control character in its path: %q", name, info.Path)
		}
		if _, gerr := c.Get(ctx, name); gerr != nil {
			t.Errorf("Get(%s): %v", name, gerr)
		}
	}
}
