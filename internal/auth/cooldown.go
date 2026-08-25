package auth

import (
	"sync"
	"time"
)

// Cooldown answers "have I written this one down recently?" for events that
// somebody outside can trigger as fast as they can make requests.
//
// The allowlist rejection in RequireAuth is one such event. It happens before
// any authentication, is not behind the login throttle, and wrote a database
// row every time: measured at 237 rows/sec from one loopback client running
// curl in a loop, growing the database from 4 KiB to 156 KiB in 400 requests.
// A real client with keep-alive is faster, and the disk it fills is the one
// holding the projects the panel exists to manage.
//
// That path only exists when the operator turns the IP allowlist on, which is
// the hardening option — so enabling the hardening was what opened an
// unauthenticated write into the database.
//
// The journal line is still emitted every time, because that is what a
// fail2ban rule reads and a ban decision needs the individual requests. Only
// the database write is gated: what the settings page needs from fifty
// identical rows is the fact that an address is hammering, which one row per
// minute says just as well.
type Cooldown struct {
	mu     sync.Mutex
	window time.Duration
	seen   map[string]time.Time

	// nextSweep is the earliest moment a sweep could free anything: the oldest
	// surviving entry's expiry, computed by the sweep that left it there.
	//
	// Without it a full map swept on every call, and the caller is a
	// pre-authentication path. Measured with the map at its cap and every entry
	// inside its window: 82ns per call became 25.2µs, a factor of 307, with the
	// mutex held for all of it. The flood that fills the map is what pays
	// nothing and the panel is what pays.
	nextSweep time.Time

	// sweeps counts full scans of the map. Here so that the cost this type was
	// changed to avoid is a test assertion rather than a stopwatch: the symptom
	// is "how often does it scan", and timing it on a shared machine is how a
	// test becomes flaky.
	sweeps int
}

// maxCooled bounds how many sources are tracked at once.
//
// Without it the map is the same unbounded growth moved from the disk into
// memory, which is worse: it cannot be swept by restarting the panel because
// the panel is what dies.
const maxCooled = 4096

// NewCooldown returns a gate that lets one event per source through per window.
func NewCooldown(window time.Duration) *Cooldown {
	return &Cooldown{window: window, seen: map[string]time.Time{}}
}

// Allow reports whether an event from this source is worth recording again.
//
// A source that keeps hammering does not refresh its own entry, so it gets one
// record per window rather than none: an attack that goes on for an hour reads
// as sixty rows, not as one row and then silence.
//
// Bucketed per event as well as per source. One address can produce more than
// one kind of noise in the same minute, and a shared window would record
// whichever arrived first and lose the existence of the other.
func (c *Cooldown) Allow(bucket, key string, now time.Time) bool {
	if c == nil {
		return true
	}
	k := bucket + "|" + sourceKey(key)

	c.mu.Lock()
	defer c.mu.Unlock()

	if last, ok := c.seen[k]; ok && now.Sub(last) < c.window {
		return false
	}
	if len(c.seen) >= maxCooled && !now.Before(c.nextSweep) {
		c.sweep(now)
	}
	if len(c.seen) >= maxCooled {
		// Still full: every tracked source is inside its window, which is a
		// distributed flood. Let it through rather than silently un-auditing
		// it — the row is the only record that it happened at all, and the
		// trim on the table is what bounds the damage from here.
		return true
	}
	c.seen[k] = now
	return true
}

// sweep drops sources whose window has passed, and records when sweeping could
// be worth doing again.
//
// Exact rather than a guess at an interval: nothing can expire before the
// oldest survivor does, so a second sweep before that frees precisely the same
// set as the first one and is pure cost.
func (c *Cooldown) sweep(now time.Time) {
	c.sweeps++
	var oldest time.Time
	for k, last := range c.seen {
		if now.Sub(last) >= c.window {
			delete(c.seen, k)
			continue
		}
		if oldest.IsZero() || last.Before(oldest) {
			oldest = last
		}
	}
	if oldest.IsZero() {
		// Nothing left to wait for.
		c.nextSweep = time.Time{}
		return
	}
	c.nextSweep = oldest.Add(c.window)
}
