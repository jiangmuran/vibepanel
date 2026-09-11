package session

import (
	"testing"
	"time"
)

// The browser keeps recently viewed terminals mounted, so a subscription no
// longer means that somebody is looking. These pin the rules that put control
// back on what does: hiding is leaving, showing is arriving. See SetHidden.

// graceHasPassed moves the session's clock past the grace period measured from
// the last departure, and fails if no departure was ever recorded.
func graceHasPassed(t *testing.T, live *Live) time.Time {
	t.Helper()
	live.mu.Lock()
	defer live.mu.Unlock()
	left := live.lastControllerAt
	if left.IsZero() {
		t.Fatal("no departure was timestamped, so the grace period can never expire")
	}
	live.now = func() time.Time { return left.Add(controllerGrace + time.Second) }
	return left
}

func TestHidingATerminalGivesTheGridUpTheWayLeavingDoes(t *testing.T) {
	live := attachedFor(t, "vp_vis_leave")

	desktop, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(desktop)
	if err := live.Resize("desktop", 144, 46); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	live.SetHidden(desktop, true)
	if got := live.Controller(); got != "" {
		t.Fatalf("controller = %q after the desktop switched to another session, want it unowned", got)
	}
	if cols, rows := live.Size(); cols != 144 || rows != 46 {
		t.Errorf("grid = %dx%d, want it frozen at 144x46", cols, rows)
	}

	// Moments later it is still theirs, as after a reload: glancing at another
	// session must not cost the desktop its grid.
	soon, _ := live.Subscribe("phone")
	if got := live.Controller(); got != "" {
		t.Errorf("controller = %q moments after the owner looked away, want it still frozen", got)
	}
	live.Unsubscribe(soon)

	// What was measured on the build without this: long after the desktop moved
	// on, the next viewer was still passive.
	graceHasPassed(t, live)
	later, _ := live.Subscribe("phone")
	defer live.Unsubscribe(later)
	if got := live.Controller(); got != "phone" {
		t.Errorf("controller = %q, want %q: a terminal nobody can see is holding the grid "+
			"of a session somebody just opened", got, "phone")
	}
}

func TestShowingATerminalAgainTakesBackAGridNobodyElseTook(t *testing.T) {
	live := attachedFor(t, "vp_vis_back")

	desktop, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(desktop)
	live.SetHidden(desktop, true)
	live.SetHidden(desktop, false)
	if got := live.Controller(); got != "desktop" {
		t.Errorf("controller = %q after switching back, want %q: the desktop would scale its own "+
			"session into a corner and be offered control of a grid nobody else holds", got, "desktop")
	}
}

func TestShowingATerminalDoesNotTakeAGridSomebodyElseHolds(t *testing.T) {
	live := attachedFor(t, "vp_vis_nosteal")

	desktop, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(desktop)
	phone, _ := live.Subscribe("phone")
	defer live.Unsubscribe(phone)

	live.SetHidden(desktop, true)
	if err := live.TakeControl("phone", 60, 20); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	live.SetHidden(desktop, false)

	if got := live.Controller(); got != "phone" {
		t.Errorf("controller = %q, want %q: switching back is not a claim on a grid somebody took", got, "phone")
	}
	if cols, rows := live.Size(); cols != 60 || rows != 20 {
		t.Errorf("grid = %dx%d, want the phone's 60x20", cols, rows)
	}
}

func TestAHiddenResubscribeDoesNotRestartTheGracePeriod(t *testing.T) {
	live := attachedFor(t, "vp_vis_resub")

	desktop, _ := live.Subscribe("desktop")
	live.SetHidden(desktop, true)
	live.mu.Lock()
	left := live.lastControllerAt
	live.mu.Unlock()

	// The socket drops, and the browser resubscribes every terminal it has
	// mounted, this one still off-screen.
	live.Unsubscribe(desktop)
	again, _ := live.SubscribeHidden("desktop")
	defer live.Unsubscribe(again)

	if got := live.Controller(); got != "" {
		t.Fatalf("controller = %q after a hidden terminal resubscribed; it arrived as a viewer", got)
	}
	live.mu.Lock()
	at := live.lastControllerAt
	live.mu.Unlock()
	if !at.Equal(left) {
		t.Fatalf("departure moved from %v to %v: every reconnect would restart the grace period", left, at)
	}

	graceHasPassed(t, live)
	phone, _ := live.Subscribe("phone")
	defer live.Unsubscribe(phone)
	if got := live.Controller(); got != "phone" {
		t.Errorf("controller = %q, want %q", got, "phone")
	}
}

func TestAHiddenConnectionOfTheSameViewerDoesNotKeepTheGrid(t *testing.T) {
	// One client id can hold two subscriptions to a session: a duplicated tab,
	// or a socket reconnected before the old one is reaped. The release keeps
	// the grid while another of them is still driving it. One kept off-screen
	// is not driving anything.
	live := attachedFor(t, "vp_vis_dup")

	visible, _ := live.Subscribe("desktop")
	offscreen, _ := live.SubscribeHidden("desktop")
	defer live.Unsubscribe(offscreen)

	live.Unsubscribe(visible)
	if got := live.Controller(); got != "" {
		t.Errorf("controller = %q after the only visible connection left, want it unowned", got)
	}
}

func TestAVisibilityChangeThatChangesNothingClaimsNothing(t *testing.T) {
	live := attachedFor(t, "vp_vis_noop")

	desktop, _ := live.Subscribe("desktop")
	phone, _ := live.Subscribe("phone")
	defer live.Unsubscribe(phone)
	live.Unsubscribe(desktop)
	graceHasPassed(t, live)

	// The phone was on screen the whole time. Saying so again is not arriving.
	live.SetHidden(phone, false)
	if got := live.Controller(); got != "" {
		t.Errorf("controller = %q after a viewer repeated that it was visible", got)
	}

	// And a subscriber that has already gone cannot come back on screen: the
	// message can arrive after the socket's own teardown.
	live.SetHidden(desktop, true)
	live.SetHidden(desktop, false)
	if got := live.Controller(); got != "" {
		t.Errorf("controller = %q, claimed by a subscriber that no longer exists", got)
	}
}
