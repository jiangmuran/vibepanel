package browse

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// An empty directory lists as an empty array, never as null.
//
// Go marshals a nil slice to `null`, and the frontend reads entries.length
// directly — so this returning nil blanked the entire panel for anybody whose
// project directory happened to have nothing in it yet, which is the normal
// state of a project you have just created.
func TestEmptyDirectoryListsAsAnEmptyArray(t *testing.T) {
	root := t.TempDir()
	listing, err := List(root, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listing.Entries == nil {
		t.Error("Entries is nil, which marshals to null")
	}
	encoded, err := json.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"entries":[]`) {
		t.Errorf("encoded as %s, want an empty array for entries", encoded)
	}
}

// Fuzzing the one function that is a security boundary.
//
// Resolve decides whether a path the browser sent stays inside the project
// directory. The existing tests cover the ways a person would try — "..",
// absolute paths, a symlink pointing out — which is to say the ways somebody
// thought of.
//
// Fuzzing the relative path alone would have been theatre. It is collapsed on
// the third line of Resolve, by `Clean("/" + rel)`, so no string can climb out
// of the root textually and twenty-three million executions of that input find
// nothing because there is nothing there to find. The escape that matters
// needs the *filesystem* to cooperate, so the fuzzer gets to build that too: it
// controls where a symlink inside the root points.
//
// The property is simple enough to state exactly: whatever comes back either
// is the root or lives under it. Anything else is a file the panel had no
// business handing over.
func FuzzResolveStaysInsideTheRoot(f *testing.F) {
	root := f.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "deeper"), 0o755); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("x"), 0o644); err != nil {
		f.Fatal(err)
	}
	outside := f.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		f.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		f.Fatal(err)
	}

	for _, seed := range []struct{ rel, target string }{
		{"", ""},
		{"sub/file.txt", ""},
		{"..", ".."},
		{"link", outside},
		{"link/secret", outside},
		{"link", "/etc"},
		{"link/../link", outside},
		{"sub/../link", filepath.Join(outside, "secret")},
		{"link", "sub"},
		{strings.Repeat("../", 40), outside},
	} {
		f.Add(seed.rel, seed.target)
	}

	f.Fuzz(func(t *testing.T, rel, target string) {
		link := filepath.Join(root, "link")
		_ = os.Remove(link)
		if target != "" {
			// A target the fuzzer chose. Most will be nonsense; the ones that
			// are not are the whole point.
			if err := os.Symlink(target, link); err != nil {
				return
			}
			defer os.Remove(link)
		}

		got, rerr := Resolve(realRoot, rel)
		if rerr != nil {
			return // refusing is always allowed
		}
		if got != realRoot && !strings.HasPrefix(got, realRoot+string(filepath.Separator)) {
			t.Fatalf("Resolve(%q) with link -> %q returned %q, which is outside %q",
				rel, target, got, realRoot)
		}
	})
}

// A big directory must not lose its subdirectories.
//
// The cap used to be applied to the ReadDir order, which is sorted by
// filename, and the directories-first sort ran afterwards on what was left.
// So 2500 files named a00000… plus one subdirectory named "zzz-important"
// returned 2000 files, zero directories, and nothing to say the listing was
// partial: the tree ended and the panel looked like it had told the truth.
func TestLargeDirectoryKeepsItsSubdirectories(t *testing.T) {
	root := t.TempDir()
	const files = maxEntries + 500
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("a%05d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "zzz-important"), 0o755); err != nil {
		t.Fatal(err)
	}

	l, err := List(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != maxEntries {
		t.Errorf("entries = %d, want the cap %d", len(l.Entries), maxEntries)
	}
	if !l.Truncated {
		t.Error("Truncated is false; the panel would present a partial listing as complete")
	}
	if l.Total != files+1 {
		t.Errorf("Total = %d, want %d", l.Total, files+1)
	}
	var found bool
	for _, e := range l.Entries {
		if e.Name == "zzz-important" {
			found = true
			if !e.IsDir {
				t.Error("zzz-important is not reported as a directory")
			}
		}
	}
	if !found {
		t.Error("the subdirectory is missing, so it cannot be navigated into at all")
	}
}

// Readable has to carry information, because a button is wired to it.
func TestReadableDistinguishesRegularFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	l, err := List(root, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"plain.txt": true, "dir": false, "pipe": false}
	for _, e := range l.Entries {
		if w, ok := want[e.Name]; ok && e.Readable != w {
			t.Errorf("%s: Readable = %v, want %v", e.Name, e.Readable, w)
		}
	}
}

func TestASymlinkOutOfTheProjectIsNotOfferedForDownload(t *testing.T) {
	// Readable is what makes the panel render a download link, and the
	// download resolves symlinks and refuses anything that leaves the project.
	// A link to /etc/passwd in a project — one `ln -s`, or anything under
	// node_modules — was a regular file, so the button appeared and clicking
	// it answered `403 outside the project`.
	//
	// A control that cannot do what it offers teaches people the panel is
	// unreliable rather than that the file is out of bounds.
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ordinary.txt"), []byte("yes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("yes"), 0o600); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escaping.txt")))
	must(os.Symlink(outside, filepath.Join(root, "escaping-dir")))
	must(os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "internal-link.txt")))

	listing, err := List(root, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]Entry{}
	for _, e := range listing.Entries {
		got[e.Name] = e
	}
	if len(got) != 5 {
		t.Fatalf("listed %d entries, want 5: %v", len(got), got)
	}

	for _, tc := range []struct {
		name     string
		readable bool
		escapes  bool
	}{
		{"ordinary.txt", true, false},
		{"target.txt", true, false},
		// A symlink that stays inside is a perfectly good file to offer.
		{"internal-link.txt", true, false},
		{"escaping.txt", false, true},
		{"escaping-dir", false, true},
	} {
		e, ok := got[tc.name]
		if !ok {
			t.Errorf("%s is not listed at all; hiding a file is its own kind of lie", tc.name)
			continue
		}
		if e.Readable != tc.readable {
			t.Errorf("%s: readable = %v, want %v", tc.name, e.Readable, tc.readable)
		}
		if e.Escapes != tc.escapes {
			t.Errorf("%s: escapes = %v, want %v", tc.name, e.Escapes, tc.escapes)
		}
	}
}
