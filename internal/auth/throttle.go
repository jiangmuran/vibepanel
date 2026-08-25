package auth

import (
	"math"
	"net"
	"sort"
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

	mu        sync.Mutex
	entries   map[string]*entry
	lastSweep time.Time
}

// maxEntries bounds how many sources are remembered at once.
//
// Generous: a busy panel sees a handful of addresses, and this only exists so
// that memory cannot be driven by whoever is attacking. Well past it, the
// oldest histories are dropped early — those are the ones closest to being
// forgotten anyway, and unbounded growth is the worse of the two problems.
const maxEntries = 4096

type entry struct {
	failures int
	last     time.Time
}

// sourceKey buckets an address so that one attacker counts as one source.
//
// Keying on the exact address is keying on nothing when the address is IPv6.
// The smallest allocation anyone is given is a /64, so somebody changing the
// last four groups between attempts has eighteen quintillion keys and never
// meets the same counter twice. Measured: fifty failures from one /64, never
// throttled once.
//
// A /64 is the right bucket precisely because it is the unit that gets handed
// out. Narrower and it is evadable; wider and one person's guessing throttles
// their neighbours.
//
// Anything that does not parse as an address is passed through, so a caller
// keying on something other than an IP still gets a throttle.
func sourceKey(key string) string {
	ip := net.ParseIP(key)
	if ip == nil {
		return key
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
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
	key = sourceKey(key)
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
	key = sourceKey(key)
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
	// sweep ran *before* the insert above, so the map can sit one entry over
	// its bound forever: the cap is only consulted on entry, and every call
	// adds one after it has been consulted. Enforcing after the mutation is
	// what makes maxEntries the maximum rather than the maximum plus whatever
	// arrived since it was last checked.
	t.evict()
}

// Succeed clears a source's history.
func (t *Throttle) Succeed(key string) {
	key = sourceKey(key)
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// Failures reports the recorded failure count, for tests and the settings page.
func (t *Throttle) Failures(key string) int {
	key = sourceKey(key)
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

// sweep drops sources that have been quiet, so the map cannot grow without
// bound from a spray of single attempts across many addresses.
//
// Not on every attempt. Walking the whole map costs O(sources) under the mutex
// that every sign-in takes, and the number of sources is chosen by whoever is
// attacking — one host with a routed IPv6 prefix can present a new address per
// request. Doing it eagerly meant the throttle degraded fastest under exactly
// the load it exists to survive, and took real sign-ins down with it.
//
// So: on a timer, or when the map has grown past the point where its size is
// somebody else's decision.
func (t *Throttle) sweep(now time.Time) {
	overCap := len(t.entries) > maxEntries
	if !overCap && !t.lastSweep.IsZero() && now.Sub(t.lastSweep) < t.Forget/4 {
		return
	}
	t.lastSweep = now

	for k, e := range t.entries {
		if now.Sub(e.last) > t.Forget {
			delete(t.entries, k)
		}
	}
	t.evict()
}

// evict drops the oldest histories when the map is over its bound.
//
// Reached when the histories being kept are all recent: a spray across many
// addresses arrives faster than any age cutoff can retire it. Dropping
// "everything older than X" achieves nothing there — every entry is younger
// than X — so the map would go on growing while every request paid to walk it,
// which is the failure the sweep exists to avoid.
//
// Evict by rank instead: sort by last-seen and drop the oldest down to the cap.
// O(n log n), but only on the calls that find the map over its bound, and it
// actually brings it back under.
//
// Ranked by how much each entry is worth keeping, not only by age.
//
// Dropping the oldest was letting an attacker with a lot of addresses do more
// than shorten its own backoff: it flushed everybody else's history too, so
// rotation was a reset button for the whole throttle. Measured — an address
// with six failures against it was forgotten because somebody else arrived
// from eight thousand.
//
// Fewest failures first. An entry with one failure against it is nearly
// noise; an entry with six is the thing this structure exists to remember.
// Displacing it now costs an attacker six real failures per address rather
// than one request, and with /64 bucketing those are bounded per allocation.
//
// A source that can still present enough addresses shortens its own backoff.
// The forget window already offers that after fifteen quiet minutes, and the
// alternative is memory chosen by the attacker.
func (t *Throttle) evict() {
	if len(t.entries) <= maxEntries {
		return
	}
	type ranked struct {
		key      string
		failures int
		last     time.Time
	}
	all := make([]ranked, 0, len(t.entries))
	for k, e := range t.entries {
		all = append(all, ranked{k, e.failures, e.last})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].failures != all[j].failures {
			return all[i].failures < all[j].failures
		}
		return all[i].last.Before(all[j].last)
	})
	for _, a := range all[:len(all)-maxEntries] {
		delete(t.entries, a.key)
	}
}
