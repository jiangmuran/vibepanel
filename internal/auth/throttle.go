package auth

import (
	"math"
	"sync"
	"time"
)

// Throttle slows down repeated failed sign-ins from one source.
//
// The panel may be reachable from the open internet, where an unthrottled
// login endpoint is a free password-guessing service. Delay rather than
// lockout: locking an account out means anyone who knows the username can
// deny the owner access, which trades one problem for a worse one.
type Throttle struct {
	// Base is the delay after the first failure; it doubles with each one.
	Base time.Duration
	// Max caps the delay, so a long-running attack does not lock the real user
	// out for hours after they eventually type it right.
	Max time.Duration
	// Forget is how long a source has to behave before its history is dropped.
	Forget time.Duration

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	failures int
	last     time.Time
}

// NewThrottle returns a throttle with sensible defaults.
func NewThrottle() *Throttle {
	return &Throttle{
		Base:    500 * time.Millisecond,
		Max:     30 * time.Second,
		Forget:  15 * time.Minute,
		entries: map[string]*entry{},
	}
}

// Delay returns how long this source must wait before its next attempt is
// considered, and whether it is currently inside that window.
func (t *Throttle) Delay(key string, now time.Time) (wait time.Duration, blocked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweep(now)

	e, ok := t.entries[key]
	if !ok || e.failures == 0 {
		return 0, false
	}
	required := t.backoff(e.failures)
	elapsed := now.Sub(e.last)
	if elapsed >= required {
		return 0, false
	}
	return required - elapsed, true
}

// Fail records a failed attempt.
func (t *Throttle) Fail(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweep(now)

	e, ok := t.entries[key]
	if !ok {
		e = &entry{}
		t.entries[key] = e
	}
	e.failures++
	e.last = now
}

// Succeed clears a source's history.
func (t *Throttle) Succeed(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// Failures reports the recorded failure count, for tests and the settings page.
func (t *Throttle) Failures(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[key]; ok {
		return e.failures
	}
	return 0
}

func (t *Throttle) backoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	// Doubling, capped. The exponent is bounded before the shift so that a
	// long attack cannot overflow the duration into something negative.
	exp := failures - 1
	if exp > 20 {
		exp = 20
	}
	d := time.Duration(float64(t.Base) * math.Pow(2, float64(exp)))
	if d > t.Max || d <= 0 {
		return t.Max
	}
	return d
}

// sweep drops sources that have been quiet. Called under the lock from the
// other methods, so the map cannot grow without bound from a spray of
// single attempts across many addresses.
func (t *Throttle) sweep(now time.Time) {
	for k, e := range t.entries {
		if now.Sub(e.last) > t.Forget {
			delete(t.entries, k)
		}
	}
}
