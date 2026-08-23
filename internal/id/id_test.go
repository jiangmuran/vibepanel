package id

import (
	"regexp"
	"strings"
	"testing"
)

func TestNewIsHexAndUnique(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		v := New()
		if !re.MatchString(v) {
			t.Fatalf("id %q is not 16 hex chars", v)
		}
		if seen[v] {
			t.Fatalf("duplicate id %q after %d draws", v, i)
		}
		seen[v] = true
	}
}

func TestTmuxNamesAreNeverPrefixesOfEachOther(t *testing.T) {
	// tmux resolves targets by prefix unless '=' is used. Fixed-length names
	// mean that even if a caller forgets the '=', it cannot silently address
	// the wrong session.
	a, b := TmuxName(New()), TmuxName(New())
	if len(a) != len(b) {
		t.Fatalf("names have different lengths: %q vs %q", a, b)
	}
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		t.Fatalf("one name is a prefix of the other: %q, %q", a, b)
	}
}
