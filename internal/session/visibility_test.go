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

func TestAHiddenTerminalReceivesNoLiveOutputUntilShown(t *testing.T) {
	live := attachedFor(t, "vp_vis_quiet")

	sub, initial := live.Subscribe("desktop")
	defer live.Unsubscribe(sub)
	// Drain initial EventSize queued on subscribe.
	drainNonOutput := func() {
		for {
			select {
			case ev := <-sub.Events:
				sub.Took(ev)
				if ev.Kind == EventOutput || ev.Kind == EventCatchUp {
					t.Fatalf("unexpected output event %v while draining control events", ev.Kind)
				}
			default:
				return
			}
		}
	}
	drainNonOutput()
	_ = initial

	live.SetHidden(sub, true)

	// Simulate an off-screen TUI spinning while the viewer is on another
	// session: write into the ring and broadcast under the same lock, as the
	// pump does.
	chunks := [][]byte{
		[]byte("spinner-frame-1\r\n"),
		[]byte("spinner-frame-2\r\n"),
		[]byte("spinner-frame-3\r\n"),
	}
	for _, c := range chunks {
		live.mu.Lock()
		_, _ = live.ring.Write(c)
		live.broadcastLocked(Event{Kind: EventOutput, Data: c})
		live.mu.Unlock()
	}

	select {
	case ev := <-sub.Events:
		t.Fatalf("hidden subscriber received %+v while off-screen; live output should wait until shown", ev)
	default:
	}

	// Bring the terminal back on screen: the missed slice arrives in one
	// EventCatchUp, continuing the stream without clearing the screen.
	live.SetHidden(sub, false)

	select {
	case ev := <-sub.Events:
		sub.Took(ev)
		if ev.Kind != EventCatchUp {
			t.Fatalf("first event on show = %v, want EventCatchUp", ev.Kind)
		}
		if !ev.Replay.Continued {
			t.Errorf("Replay.Continued = false, want true when the ring still holds the gap")
		}
		want := "spinner-frame-1\r\nspinner-frame-2\r\nspinner-frame-3\r\n"
		if got := string(ev.Replay.Data); got != want {
			t.Errorf("catch-up data = %q, want %q", got, want)
		}
	default:
		t.Fatal("showing a hidden terminal that missed output sent no EventCatchUp")
	}

	// Subsequent live output flows normally again.
	after := []byte("live-again\r\n")
	live.mu.Lock()
	_, _ = live.ring.Write(after)
	live.broadcastLocked(Event{Kind: EventOutput, Data: after})
	live.mu.Unlock()

	select {
	case ev := <-sub.Events:
		sub.Took(ev)
		if ev.Kind != EventOutput || string(ev.Data) != string(after) {
			t.Fatalf("after show, got %+v, want live EventOutput %q", ev, after)
		}
	default:
		t.Fatal("live output did not resume after SetHidden(false)")
	}
}

func TestAHiddenReconnectDefersCatchUpUntilShown(t *testing.T) {
	live := attachedFor(t, "vp_vis_defer_resub")

	first, snap := live.SubscribeFrom("desktop", false, Resume{})
	since := snap.Offset + int64(len(snap.Data))
	live.SetHidden(first, true)

	missed := []byte("written-while-disconnected\r\n")
	live.mu.Lock()
	_, _ = live.ring.Write(missed)
	live.broadcastLocked(Event{Kind: EventOutput, Data: missed})
	live.mu.Unlock()
	live.Unsubscribe(first)

	// Reconnect while still off-screen: send nothing now, remember where the
	// viewer stopped, and catch up only when shown.
	again, resub := live.SubscribeFrom("desktop", true, Resume{Stream: snap.Stream, Since: since})
	defer live.Unsubscribe(again)
	if len(resub.Data) != 0 || !resub.Continued || resub.Offset != since {
		t.Fatalf("hidden reconnect returned len=%d continued=%v offset=%d, want 0 bytes continuing from %d",
			len(resub.Data), resub.Continued, resub.Offset, since)
	}
	// Drain initial EventSize.
	<-again.Events

	live.SetHidden(again, false)
	ev := <-again.Events
	again.Took(ev)
	if ev.Kind != EventCatchUp || !ev.Replay.Continued || string(ev.Replay.Data) != string(missed) {
		t.Fatalf("catch-up after hidden reconnect = %+v (%q), want Continued %q",
			ev.Kind, ev.Replay.Data, missed)
	}
}

func TestABackgroundTabGivesUpTheGridAndNeverReclaimsItWhileBackgrounded(t *testing.T) {
	live := attachedFor(t, "vp_vis_bg")

	mac, _ := live.Subscribe("mac")
	if err := live.Resize("mac", 107, 32); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	// The browser tab goes into the background (another tab, minimised window
	// or locked screen). It gives up the grid into the grace period, and live
	// output still flows so secondary monitoring views stay fresh.
	live.SetVisibility(mac, false, true)
	if got := live.Controller(); got != "" {
		t.Fatalf("controller = %q after tab went to background, want unowned", got)
	}
	if live.Watched() {
		t.Errorf("Watched() = true with only a background tab subscribed")
	}

	// Bringing the same tab back within grace reclaims the grid seamlessly.
	live.SetVisibility(mac, false, false)
	if got := live.Controller(); got != "mac" {
		t.Fatalf("controller = %q after returning to foreground within grace, want mac", got)
	}

	// Now the tab goes background again and stays away past the grace window;
	// a foreground desktop takes over, then switches away past grace. When
	// the unattended background tab drops and reconnects (with background=true),
	// it must NOT reclaim the grid and shrink it back to 107x32.
	live.SetVisibility(mac, false, true)
	graceHasPassed(t, live)

	desktop, _ := live.Subscribe("desktop")
	if got := live.Controller(); got != "desktop" {
		t.Fatalf("controller = %q, want desktop once background tab's grace expired", got)
	}
	if err := live.Resize("desktop", 298, 65); err != nil {
		t.Fatalf("Resize desktop: %v", err)
	}

	// Desktop switches to another session and stays away past grace.
	live.SetHidden(desktop, true)
	graceHasPassed(t, live)

	// The sleeping background tab drops and reconnects periodically.
	live.Unsubscribe(mac)
	macAgain, _ := live.SubscribeView("mac", false, true, Resume{})
	defer live.Unsubscribe(macAgain)
	if got := live.Controller(); got != "" {
		t.Fatalf("controller = %q after background tab reconnected; a background tab must never steal control", got)
	}
	if err := live.Resize("mac", 107, 32); err != nil {
		t.Fatalf("passive background resize: %v", err)
	}
	if cols, rows := live.Size(); cols != 298 || rows != 65 {
		t.Errorf("grid = %dx%d after background reconnect, want desktop's 298x65 preserved", cols, rows)
	}
	live.Unsubscribe(desktop)
}

func TestWaitingForegroundViewerIsPromotedAutomaticallyWhenGraceExpires(t *testing.T) {
	live := attachedFor(t, "vp_vis_handover")
	live.mu.Lock()
	live.gracePeriod = 40 * time.Millisecond
	live.mu.Unlock()

	phone, _ := live.Subscribe("phone")
	if err := live.Resize("phone", 45, 24); err != nil {
		t.Fatalf("Resize phone: %v", err)
	}

	desktop, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(desktop)
	bgTab, _ := live.SubscribeView("bg-mac", false, true, Resume{})
	defer live.Unsubscribe(bgTab)

	// Drain initial EventSize frames.
	drain := func(s *Subscriber) {
		for {
			select {
			case ev := <-s.Events:
				s.Took(ev)
			default:
				return
			}
		}
	}
	drain(desktop)
	drain(bgTab)

	if got := live.Controller(); got != "phone" {
		t.Fatalf("controller = %q, want phone", got)
	}

	// The user closes their phone browser while the desktop is already watching.
	// Immediately after departure the grid stays frozen (reload protection).
	live.Unsubscribe(phone)
	if got := live.Controller(); got != "" {
		t.Fatalf("controller immediately after phone left = %q, want frozen", got)
	}

	// Once the grace period passes without the phone returning, the waiting
	// foreground desktop is promoted automatically and notified via EventSize.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-desktop.Events:
			desktop.Took(ev)
			if ev.Kind == EventSize && live.Controller() == "desktop" {
				if err := live.Resize("desktop", 200, 60); err != nil {
					t.Fatalf("Resize after promotion: %v", err)
				}
				if cols, rows := live.Size(); cols != 200 || rows != 60 {
					t.Errorf("grid = %dx%d after promoted desktop resized, want 200x60", cols, rows)
				}
				return
			}
		case <-deadline:
			t.Fatalf("waiting desktop was never promoted after grace expired; controller = %q", live.Controller())
		}
	}
}

func TestGraceExpiryNeverPromotesABackgroundOrHiddenViewer(t *testing.T) {
	live := attachedFor(t, "vp_vis_no_bg_promote")
	live.mu.Lock()
	live.gracePeriod = 30 * time.Millisecond
	live.mu.Unlock()

	desktop, _ := live.Subscribe("desktop")
	bg, _ := live.SubscribeView("bg-tab", false, true, Resume{})
	defer live.Unsubscribe(bg)
	hidden, _ := live.SubscribeHidden("hidden-tab")
	defer live.Unsubscribe(hidden)

	live.Unsubscribe(desktop)
	time.Sleep(80 * time.Millisecond)

	if got := live.Controller(); got != "" {
		t.Errorf("controller = %q after grace expired with only background/hidden viewers; want unowned", got)
	}
}
