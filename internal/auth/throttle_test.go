package auth

import (
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
