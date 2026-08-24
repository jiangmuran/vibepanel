// Package auth handles who is allowed in.
//
// The panel hands out a writable terminal, so this is the only thing between a
// stranger and the machine. Everything here is deliberately boring: standard
// algorithms, standard parameters, constant-time comparisons, and no clever
// shortcuts.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters.
//
// 64 MiB and two passes is the middle of the OWASP guidance: enough that a
// stolen hash is expensive to attack, cheap enough that a login on a small
// server does not stall. They are written into the encoded hash, so raising
// them later does not invalidate existing passwords.
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 64 * 1024
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// ErrBadHash means a stored hash could not be parsed.
var ErrBadHash = errors.New("auth: malformed password hash")

// MinPasswordLength is the shortest password accepted.
//
// Length, not composition rules: character-class requirements push people
// towards "Password1!" and this panel may be reachable from the open internet,
// where the only thing that helps is a long secret.
const MinPasswordLength = 12

// HashPassword returns an encoded argon2id hash in PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	threads := uint8(runtime.NumCPU())
	if threads > 4 {
		threads = 4
	}
	if threads == 0 {
		threads = 1
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, threads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches an encoded hash.
//
// The parameters come from the hash rather than from the constants above, so
// that raising the cost later does not lock anyone out of their account.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrBadHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrBadHash
	}
	if version != argon2.Version {
		return false, fmt.Errorf("auth: unsupported argon2 version %d", version)
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, ErrBadHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrBadHash
	}

	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	// Constant time: a comparison that returns early on the first wrong byte
	// leaks how much of a guess was right.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// DummyVerify spends the same work as a real verification.
//
// Called when the username does not exist, so that "no such user" takes as
// long as "wrong password". Without it, response time tells an attacker which
// usernames are real, which is the first half of guessing a password.
func DummyVerify(password string) {
	// A fixed hash of a fixed string, at the current parameters.
	const reference = "$argon2id$v=19$m=65536,t=2,p=4$" +
		"AAAAAAAAAAAAAAAAAAAAAA$" +
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, _ = VerifyPassword(password, reference)
}
