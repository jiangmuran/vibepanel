package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestCooldownLetsOneThroughPerWindow(t *testing.T) {
	c := NewCooldown(time.Minute)
	now := time.Now()

	if !c.Allow("blocked", "203.0.113.7", now) {
		t.Fatal("the first event from a source must be recorded")
	}
	for i := 1; i <= 400; i++ {
		if c.Allow("blocked", "203.0.113.7", now.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatalf("request %d was recorded again inside the window", i)
		}
	}
	// A flood that goes on gets a row per window, not one row and then
	// silence: an hour of it should read as an hour of it.
	if !c.Allow("blocked", "203.0.113.7", now.Add(61*time.Second)) {
		t.Error("nothing was recorded after the window passed")
	}
}

func TestCooldownIsPerSourceAndBucketsIPv6(t *testing.T) {
	c := NewCooldown(time.Minute)
	now := time.Now()

	if !c.Allow("blocked", "203.0.113.7", now) || !c.Allow("blocked", "198.51.100.9", now) {
		t.Fatal("two different addresses must each be recorded")
	}
	// A /64 is one machine's worth of addresses. Keying on the full address
	// would let anyone with an IPv6 prefix write a row per request by simply
	// counting up, which is the hole this closes.
	if !c.Allow("blocked", "2001:db8::1", now) {
		t.Fatal("the first address in a prefix must be recorded")
	}
	if c.Allow("blocked", "2001:db8::2", now.Add(time.Millisecond)) {
		t.Error("a second address in the same /64 was treated as a new source")
	}
	if !c.Allow("blocked", "2001:db8:1::1", now.Add(time.Millisecond)) {
		t.Error("a different /64 was treated as the same source")
	}
}

func TestCooldownDoesNotGrowWithoutBound(t *testing.T) {
	// The map is the same unbounded growth moved into memory, and worse:
	// restarting the panel cannot sweep it, because the panel is what dies.
	c := NewCooldown(time.Minute)
	now := time.Now()
	for i := 0; i < maxCooled*3; i++ {
		c.Allow("blocked", fmt.Sprintf("198.51.100.%d:%d", i%256, i), now)
	}
	c.mu.Lock()
	n := len(c.seen)
	c.mu.Unlock()
	if n > maxCooled {
		t.Errorf("tracking %d sources, cap is %d", n, maxCooled)
	}
}

func TestNilCooldownAllows(t *testing.T) {
	// The zero Auth in tests that do not care about this must not silently
	// stop auditing.
	var c *Cooldown
	if !c.Allow("blocked", "203.0.113.7", time.Now()) {
		t.Error("a nil cooldown must let everything through")
	}
}

func TestCooldownKeepsEventsApart(t *testing.T) {
	// One address can make more than one kind of noise in the same minute — an
	// allowlist refusal on one endpoint and a bad setup token on another. A
	// window shared across events would record whichever arrived first and
	// lose the fact that the other happened at all.
	c := NewCooldown(time.Minute)
	now := time.Now()

	if !c.Allow("blocked", "203.0.113.7", now) {
		t.Fatal("the first blocked event must be recorded")
	}
	if !c.Allow("setup.rejected", "203.0.113.7", now.Add(time.Millisecond)) {
		t.Error("a different event from the same address was swallowed")
	}
	if c.Allow("blocked", "203.0.113.7", now.Add(2*time.Millisecond)) {
		t.Error("the first event stopped being gated once a second one appeared")
	}
}
