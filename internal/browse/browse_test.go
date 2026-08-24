package browse

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "src", "deep"), 0o755))
	must(os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o644))
	must(os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0o644))
	must(os.WriteFile(filepath.Join(root, "Aardvark.txt"), []byte("a"), 0o644))
	return root
}

func TestListSortsDirectoriesFirst(t *testing.T) {
	root := setup(t)
	l, err := List(root, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if l.Parent != nil {
		t.Errorf("root has a parent: %v", *l.Parent)
	}
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name)
	}
	// Directories first, then case-insensitive by name. Matching what every
	// file browser does means nobody has to think about it.
	want := []string{"src", "Aardvark.txt", "README.md"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v", names, want)
		}
	}
}

func TestListSubdirectoryHasAParent(t *testing.T) {
	root := setup(t)
	l, err := List(root, "src/deep")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if l.Path != "src/deep" {
		t.Errorf("path = %q", l.Path)
	}
	if l.Parent == nil || *l.Parent != "src" {
		t.Errorf("parent = %v, want src", l.Parent)
	}
}

func TestTraversalIsRefused(t *testing.T) {
	root := setup(t)
	// Every one of these arrives from a URL.
	for _, rel := range []string{
		"..",
		"../..",
		"../../etc",
		"src/../../..",
		"/etc",
		"/etc/passwd",
		"src/../../../../../../etc",
	} {
		t.Run(rel, func(t *testing.T) {
			abs, err := Resolve(root, rel)
			if err == nil {
				// Collapsing against "/" first means most of these resolve
				// back inside the root rather than erroring — which is fine,
				// as long as they stay inside it.
				realRoot, _ := filepath.EvalSymlinks(root)
				if !within(realRoot, abs) {
					t.Fatalf("resolved outside the root: %q", abs)
				}
				return
			}
			if !errors.Is(err, ErrOutsideRoot) && !os.IsNotExist(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSymlinkOutOfTheRootIsRefused(t *testing.T) {
	root := setup(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// A textual prefix check passes this happily: "<root>/escape" starts with
	// the root. Only resolving both sides catches it.
	if _, err := Resolve(root, "escape"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("following a symlink out of the root = %v, want ErrOutsideRoot", err)
	}
	if _, err := List(root, "escape"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("listing through a symlink out of the root = %v, want ErrOutsideRoot", err)
	}
}

func TestSymlinkInsideTheRootIsFine(t *testing.T) {
	root := setup(t)
	if err := os.Symlink(filepath.Join(root, "src"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	l, err := List(root, "link")
	if err != nil {
		t.Fatalf("List through an internal symlink: %v", err)
	}
	if len(l.Entries) == 0 {
		t.Error("no entries through the symlink")
	}
}

func TestListingAFileIsAnError(t *testing.T) {
	root := setup(t)
	if _, err := List(root, "README.md"); err == nil {
		t.Error("listing a file should fail")
	}
}
