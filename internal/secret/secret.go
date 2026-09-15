// Package secret keeps small values encrypted at rest under a key that is not
// in the database.
//
// Two things are stored this way: a share link's token, so its address can be
// shown again, and a page source's secrets. Both used to be (or would have
// been) the kind of value a copied database hands over. The key lives in a
// file beside the database, mode 0600, so a copy of the database alone opens
// nothing; the database and the key together do, and they are on the machine
// the panel runs on.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KeyFile is the key's name inside the data directory.
const KeyFile = "secrets.key"

const keyLen = 32

// ErrCorrupt is a sealed value that does not open under this key: tampered
// with, moved to another row, or sealed under a key that has since changed.
var ErrCorrupt = errors.New("secret: the value does not open under this key")

// Box seals and opens values with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// Open reads the key at path, creating it with fresh random bytes when it does
// not exist.
//
// Created with O_EXCL, so two panels racing on a first start cannot each write
// a different key and have one of them encrypt under a key the other then
// overwrites. A key file of the wrong length is refused rather than padded: a
// truncated key is a key that opens nothing sealed before it.
func Open(path string) (*Box, error) {
	key, err := os.ReadFile(path) //nolint:gosec // the panel's own data directory
	if errors.Is(err, fs.ErrNotExist) {
		key, err = create(path)
	}
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	if len(key) != keyLen {
		return nil, fmt.Errorf("secret: %s is %d bytes, not %d", path, len(key), keyLen)
	}
	// Best effort, like the database's own restrict(): a key readable by
	// others is still the key, and refusing to start over a mode the panel
	// cannot set would take the whole panel down for it.
	_ = os.Chmod(path, 0o600)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	return &Box{aead: aead}, nil
}

func create(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the panel's own data directory
	if errors.Is(err, fs.ErrExist) {
		// Somebody else created it between our read and our create: theirs is
		// the key.
		return os.ReadFile(path) //nolint:gosec // as above
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, err
	}
	return key, f.Close()
}

// Seal encrypts plaintext, bound to context: a value sealed for one row does
// not open when read back for another, so copying a ciphertext between rows
// in the database does not move the secret with it.
func (b *Box) Seal(plaintext []byte, context string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		// crypto/rand does not fail on the platforms this builds for; a nonce
		// that is not random is a key recovery, so this is not recoverable.
		panic("secret: " + err.Error())
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte(context))
}

// Unseal decrypts what Seal produced for the same context.
func (b *Box) Unseal(sealed []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrCorrupt
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		return nil, ErrCorrupt
	}
	return out, nil
}
