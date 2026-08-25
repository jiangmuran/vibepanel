package httpapi

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// TestChallengeStoreIsBounded pins the cap on in-flight WebAuthn ceremonies.
//
// `login/begin` has to be public — it is how you sign in — and it allocates,
// with every put and take sweeping the whole map for expired entries. So
// without a bound, an anonymous caller chooses how much memory the panel uses
// and how much CPU it spends, and the cost per request grows with the damage
// already done.
//
// Measured against a local panel, one laptop, 25 seconds:
//
//	before: 70,238 requests, none refused, RSS +21 MiB, rate falling from
//	        ~6,300/s to ~1,300/s as the sweep grew
//	after:  389,344 requests, 4,095 accepted and the rest refused with 503,
//	        RSS flat, health latency unchanged
//
// The point of the cap is not that it refuses. It is that the cost of an
// attack becomes flat instead of quadratic, and password sign-in — which is
// never the only door — keeps working throughout.
func TestChallengeStoreIsBounded(t *testing.T) {
	c := newChallengeStore()
	for i := 0; i < maxChallenges; i++ {
		if _, err := c.put(webauthn.SessionData{}, ""); err != nil {
			t.Fatalf("put %d of %d failed: %v", i, maxChallenges, err)
		}
	}
	if _, err := c.put(webauthn.SessionData{}, ""); !errors.Is(err, errTooManyChallenges) {
		t.Fatalf("the %dth ceremony was accepted (err = %v); an anonymous caller sets the "+
			"size of this map", maxChallenges+1, err)
	}
	if got := challengeStatus(errTooManyChallenges); got != http.StatusServiceUnavailable {
		t.Errorf("a full store answers %d; 503 is what it is, and 500 sends whoever is on "+
			"call looking for a bug that is not there", got)
	}
}

func TestAFullChallengeStoreRecovers(t *testing.T) {
	// The cap must be a queue, not a wall: once the flood stops, the entries
	// age out and sign-in works again without restarting the panel.
	c := newChallengeStore()
	for i := 0; i < maxChallenges; i++ {
		if _, err := c.put(webauthn.SessionData{}, ""); err != nil {
			t.Fatalf("filling: %v", err)
		}
	}
	c.mu.Lock()
	for _, e := range c.items {
		e.expires = time.Now().Add(-time.Second)
	}
	c.mu.Unlock()

	if _, err := c.put(webauthn.SessionData{}, ""); err != nil {
		t.Fatalf("a store full of expired ceremonies still refuses: %v", err)
	}
	c.mu.Lock()
	n := len(c.items)
	c.mu.Unlock()
	if n != 1 {
		t.Errorf("after the sweep the store holds %d entries, want just the new one", n)
	}
}
