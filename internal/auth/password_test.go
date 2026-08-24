package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestHashAndVerify(t *testing.T) {
	const pw = "a reasonably long passphrase"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash is not PHC-encoded argon2id: %q", hash)
	}
	// The plaintext must not survive anywhere in the encoding.
	if strings.Contains(hash, pw) {
		t.Error("the hash contains the password")
	}

	ok, err := VerifyPassword(pw, hash)
	if err != nil || !ok {
		t.Errorf("VerifyPassword(correct) = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(pw+"x", hash)
	if err != nil || ok {
		t.Errorf("VerifyPassword(wrong) = %v, %v", ok, err)
	}
}

func TestHashesAreSalted(t *testing.T) {
	// Two identical passwords must not produce the same hash, or a leaked
	// database tells an attacker which accounts share one.
	a, _ := HashPassword("the same password")
	b, _ := HashPassword("the same password")
	if a == b {
		t.Error("two hashes of the same password are identical")
	}
	if ok, _ := VerifyPassword("the same password", b); !ok {
		t.Error("the second hash does not verify")
	}
}

func TestVerifyUsesTheParametersFromTheHash(t *testing.T) {
	// Raising the cost later must not lock anyone out, so verification reads
	// the parameters out of the stored hash rather than assuming the current
	// constants. This is a hash made at deliberately different settings.
	const pw = "an old password"
	salt := []byte("sixteen-byte-slt")
	var oldTime, oldMemory uint32 = 1, 8 * 1024
	var oldThreads uint8 = 1
	key := argon2.IDKey([]byte(pw), salt, oldTime, oldMemory, oldThreads, 32)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, oldMemory, oldTime, oldThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))

	if ok, err := VerifyPassword(pw, encoded); err != nil || !ok {
		t.Errorf("a hash made at older parameters does not verify: %v, %v", ok, err)
	}
	if ok, _ := VerifyPassword(pw+"!", encoded); ok {
		t.Error("a wrong password verified against the older parameters")
	}
}

func TestMalformedHashesAreRejected(t *testing.T) {
	for _, bad := range []string{
		"",
		"not a hash",
		"$argon2i$v=19$m=65536,t=2,p=4$c2FsdA$aGFzaA",  // wrong variant
		"$argon2id$v=99$m=65536,t=2,p=4$c2FsdA$aGFzaA", // wrong version
		"$argon2id$v=19$nonsense$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=2,p=4$!!!$aGFzaA",
	} {
		if ok, err := VerifyPassword("anything", bad); ok || err == nil {
			t.Errorf("VerifyPassword(_, %q) = %v, %v; want false and an error", bad, ok, err)
		}
	}
}
