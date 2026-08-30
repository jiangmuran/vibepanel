package httpapi

import (
	"os"
	"path/filepath"
	"testing"
)

// A second paste of the same name gets a new name, not an error.
//
// Pasting a screenshot at an agent produces `image.png` every time, from every
// operating system, so the second paste of a session always failed with
// `409 image.png already exists` and the suggested fix was to go and rename a
// file the person never named. 「粘贴文件不应当以文件重复为由报错 应该自动加-1 -2」.
//
// What must survive the change is the reason the refusal existed: an upload
// may never quietly replace a file an agent is working on.
func TestASecondUploadOfTheSameNameIsRenamedNotRefused(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte("the original"), 0o600); err != nil {
		t.Fatal(err)
	}

	var names []string
	for i := 0; i < 3; i++ {
		f, target, err := createUnique(dir, "image.png")
		if err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
		if _, err := f.WriteString("new"); err != nil {
			t.Fatal(err)
		}
		f.Close()
		names = append(names, filepath.Base(target))
	}

	want := []string{"image-1.png", "image-2.png", "image-3.png"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("upload %d landed at %q, want %q", i, names[i], want[i])
		}
	}

	// The suffix goes before the extension: image-1.png is a picture and
	// image.png-1 is not.
	for _, n := range names {
		if filepath.Ext(n) != ".png" {
			t.Errorf("%q lost its extension", n)
		}
	}

	// And the file that was already there is untouched. This is the property
	// the 409 existed to protect and the only one that may not be traded away.
	got, err := os.ReadFile(filepath.Join(dir, "image.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the original" {
		t.Errorf("the existing file was overwritten: %q", got)
	}
}

// A name with no extension keeps the suffix at the end.
func TestUniqueNamesWithoutAnExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, target, err := createUnique(dir, "notes")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(target) != "notes-1" {
		t.Errorf("landed at %q, want notes-1", filepath.Base(target))
	}
}
