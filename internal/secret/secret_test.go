package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestASealedValueOpensOnlyForItsOwnContextAndKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KeyFile)
	box, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}

	sealed := box.Seal([]byte("the token"), "share:abc")
	if bytes.Contains(sealed, []byte("the token")) {
		t.Fatal("the sealed value contains the plaintext")
	}
	got, err := box.Unseal(sealed, "share:abc")
	if err != nil || string(got) != "the token" {
		t.Fatalf("unseal = %q, %v", got, err)
	}
	if _, err := box.Unseal(sealed, "share:other"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a value moved to another row opened: %v", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := box.Unseal(tampered, "share:abc"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a tampered value opened: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.Unseal(sealed, "share:abc"); err != nil || string(got) != "the token" {
		t.Errorf("reopening the key lost it: %q, %v", got, err)
	}

	other, err := Open(filepath.Join(t.TempDir(), KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Unseal(sealed, "share:abc"); !errors.Is(err, ErrCorrupt) {
		t.Error("a value opened under a different key")
	}
}

func TestAKeyFileOfTheWrongLengthIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), KeyFile)
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("a truncated key was accepted")
	}
}
