package auth

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func TestThrottleAllowsAFirstAttempt(t *testing.T) {
	th := NewThrottle()
	if _, blocked := th.Delay("1.2.3.4", t0); blocked {
		t.Error("a source with no history was blocked")
	}
}

func TestThrottleBacksOffExponentially(t *testing.T) {
	th := NewThrottle()
	th.Fail("1.2.3.4", t0)

	// Immediately after a failure the source has to wait.
	wait, blocked := th.Delay("1.2.3.4", t0)
	if !blocked || wait != th.Base {
		t.Fatalf("after one failure: wait %v blocked %v, want %v", wait, blocked, th.Base)
	}
	// And is free again once it has.
	if _, blocked := th.Delay("1.2.3.4", t0.Add(th.Base)); blocked {
		t.Error("still blocked after waiting out the delay")
	}

	th.Fail("1.2.3.4", t0.Add(th.Base))
	wait, _ = th.Delay("1.2.3.4", t0.Add(th.Base))
	if wait != 2*th.Base {
		t.Errorf("after two failures: wait %v, want %v", wait, 2*th.Base)
	}
}

func TestThrottleIsCapped(t *testing.T) {
	th := NewThrottle()
	// A long attack must not leave the real user locked out for hours once
	// they eventually type it right.
	for i := 0; i < 200; i++ {
		th.Fail("attacker", t0)
	}
	wait, blocked := th.Delay("attacker", t0)
	if !blocked {
		t.Fatal("not blocked after 200 failures")
	}
	if wait > th.Max || wait <= 0 {
		t.Errorf("wait = %v, want a positive value no greater than %v", wait, th.Max)
	}
}

func TestThrottleIsPerSource(t *testing.T) {
	th := NewThrottle()
	for i := 0; i < 5; i++ {
		th.Fail("attacker", t0)
	}
	// One source guessing must not slow anybody else down; that would be a
	// denial of service anyone could trigger.
	if _, blocked := th.Delay("someone-else", t0); blocked {
		t.Error("an unrelated source was blocked")
	}
}

func TestSuccessClearsHistory(t *testing.T) {
	th := NewThrottle()
	th.Fail("1.2.3.4", t0)
	th.Succeed("1.2.3.4")
	if _, blocked := th.Delay("1.2.3.4", t0); blocked {
		t.Error("still throttled after a successful sign-in")
	}
	if n := th.Failures("1.2.3.4"); n != 0 {
		t.Errorf("failures = %d after success", n)
	}
}

func TestQuietSourcesAreForgotten(t *testing.T) {
	th := NewThrottle()
	th.Fail("1.2.3.4", t0)
	// Otherwise a spray across many addresses grows the map without bound.
	if _, blocked := th.Delay("1.2.3.4", t0.Add(th.Forget+time.Minute)); blocked {
		t.Error("a source that behaved for the forget window is still throttled")
	}
	if n := th.Failures("1.2.3.4"); n != 0 {
		t.Errorf("history survived the forget window: %d", n)
	}
}

// A spray across many addresses must not choose how much the panel remembers.
//
// The first attempt at this bounded nothing: it dropped entries older than the
// forget window, then older than half of it, but in a fast spray every entry is
// newer than both — so the map grew anyway and every request paid to walk it.
func TestThrottleBoundsWhatItRemembers(t *testing.T) {
	th := NewThrottle()
	now := t0
	// Far more sources than a real panel sees, arriving far faster than any age
	// cutoff could retire them.
	for i := 0; i < maxEntries*3; i++ {
		th.Fail(fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256), now)
		now = now.Add(time.Millisecond)
	}

	th.mu.Lock()
	held := len(th.entries)
	th.mu.Unlock()
	if held > maxEntries {
		t.Errorf("remembered %d sources with a cap of %d; the map grows with the attack",
			held, maxEntries)
	}

	// And the ones it kept are the recent ones, so a source still guessing right
	// now is still being slowed down.
	recent := fmt.Sprintf("10.%d.%d.%d", (maxEntries*3-1)/65536, ((maxEntries*3-1)/256)%256, (maxEntries*3-1)%256)
	if n := th.Failures(recent); n == 0 {
		t.Error("the most recent source was evicted; eviction is dropping the wrong end")
	}
}

func TestThrottleIsConcurrencySafe(t *testing.T) {
	// Run with -race. Every request touches this.
	th := NewThrottle()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			th.Fail("shared", t0)
		}
	}()
	for i := 0; i < 500; i++ {
		th.Delay("shared", t0)
		th.Failures("shared")
	}
	<-done
}

func TestOneAttackerIsOneSourceOnIPv6(t *testing.T) {
	// Keying on the exact address is keying on nothing when the address is
	// IPv6. The smallest allocation anyone gets is a /64, so an attacker who
	// changes the last four groups between attempts has 18 quintillion keys
	// and never meets the same counter twice.
	//
	// A password guessed at that rate is a password guessed.
	th := NewThrottle()
	now := time.Now()
	for i := 0; i < 50; i++ {
		addr := fmt.Sprintf("2001:db8:1:2::%x", i+1)
		if _, blocked := th.Delay(addr, now); blocked {
			return // throttled: the addresses are being treated as one source
		}
		th.Fail(addr, now)
		now = now.Add(10 * time.Millisecond)
	}
	t.Error("fifty failures from one /64 were never throttled; every address in it " +
		"belongs to the same person and the counter never saw two of them")
}

func TestRotatingAddressesDoNotEraseTheHistoryOfOthers(t *testing.T) {
	// The second-order problem, and the worse one. Entries are bounded, and
	// past the bound the oldest are dropped — so an attacker rotating
	// addresses does not merely evade its own counter, it flushes everybody
	// else's. Rotation becomes a reset button for the whole throttle.
	th := NewThrottle()
	now := time.Now()

	const victim = "203.0.113.7"
	for i := 0; i < 6; i++ {
		th.Fail(victim, now)
		now = now.Add(time.Millisecond)
	}
	if th.Failures(victim) == 0 {
		t.Fatal("the setup did not record any failures")
	}

	// Enough distinct sources to go well past the bound.
	for i := 0; i < maxEntries*2; i++ {
		th.Fail(fmt.Sprintf("2001:db8:%x:%x::1", i/65536, i%65536), now)
		now = now.Add(time.Microsecond)
	}

	if th.Failures(victim) == 0 {
		t.Error("an address with six failures against it was forgotten because somebody " +
			"else arrived from a lot of addresses; rotation resets the throttle for everyone")
	}
}
