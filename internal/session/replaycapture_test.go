package session

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// A ring that has wrapped sends every subscribe that cannot resume to tmux for
// the pane. These pin what that costs and what it leaves behind.

// overflow writes to the ring until it has dropped something, the way a
// minute of an agent's spinner does.
func overflow(t *testing.T, live *Live) {
	t.Helper()
	chunk := bytes.Repeat([]byte("spinner-redraw "), 1024)
	live.mu.Lock()
	defer live.mu.Unlock()
	for i := 0; !live.ring.Overflowed(); i++ {
		if i > 4096 {
			t.Fatal("the ring never overflowed")
		}
		_, _ = live.ring.Write(chunk)
	}
}

// A panel restart or a network blip reconnects every viewer of a session in
// the same instant. Each capture is three tmux processes reading up to ten
// thousand lines, and they would all be reading the same pane.
func TestViewersArrivingTogetherShareOneCapture(t *testing.T) {
	live := attachedFor(t, "vp_capture_shared")
	overflow(t, live)

	var calls atomic.Int32
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	live.captureSnapshot = func(context.Context, int) (tmux.Snapshot, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return tmux.Snapshot{History: "HISTORY_FROM_TMUX", Screen: "SCREEN_FROM_TMUX", PaneHeight: 24}, nil
	}

	const viewers = 8
	replays := make([]Replay, viewers)
	var wg sync.WaitGroup
	for i := 0; i < viewers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub, r := live.SubscribeFrom("viewer", true, Resume{})
			replays[i] = r
			live.Unsubscribe(sub)
		}(i)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no capture was started for a wrapped ring")
	}
	// Long enough for the other seven to have reached the capture if nothing
	// were holding them to the first.
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Fatalf("%d viewers arriving together made %d captures, want 1", viewers, n)
	}
	for i, r := range replays {
		if !strings.Contains(string(r.Data), "HISTORY_FROM_TMUX") || !strings.Contains(string(r.Data), "SCREEN_FROM_TMUX") {
			t.Errorf("viewer %d was not given the shared capture", i)
		}
	}
}

// The pump must not wait on tmux: the capture runs with the session's lock
// released, so output keeps reaching the ring and the viewers already there.
func TestACaptureDoesNotHoldTheSession(t *testing.T) {
	live := attachedFor(t, "vp_capture_unlocked")
	overflow(t, live)

	entered := make(chan struct{})
	release := make(chan struct{})
	live.captureSnapshot = func(context.Context, int) (tmux.Snapshot, error) {
		close(entered)
		<-release
		return tmux.Snapshot{Screen: "SCREEN_FROM_TMUX", PaneHeight: 24}, nil
	}

	got := make(chan Replay, 1)
	go func() {
		sub, r := live.SubscribeFrom("late", true, Resume{})
		live.Unsubscribe(sub)
		got <- r
	}()
	<-entered

	wrote := make(chan struct{})
	go func() {
		live.mu.Lock()
		_, _ = live.ring.Write([]byte("WRITTEN_DURING_CAPTURE"))
		live.mu.Unlock()
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("a write to the session waited on the capture")
	}
	close(release)

	r := <-got
	// What landed while tmux ran follows the capture, so nothing is missing.
	text := string(r.Data)
	screen, during := strings.Index(text, "SCREEN_FROM_TMUX"), strings.Index(text, "WRITTEN_DURING_CAPTURE")
	if screen < 0 || during < screen {
		t.Fatalf("replay has the capture at %d and the bytes written during it at %d, want both, in that order", screen, during)
	}
	if end, total := r.Offset+int64(len(r.Data)), live.ring.Total(); end != total {
		t.Fatalf("offset + len(data) = %d, ring total = %d", end, total)
	}
}

// A subscribe that leaves sentUntil at zero looks fine until the terminal is
// hidden and shown again: the catch-up then starts from the beginning of a
// ring that no longer has its beginning, so the viewer is cleared and sent the
// whole ring -- megabytes of spinner, on top of the scrollback the capture had
// just rebuilt.
func TestShowingATerminalRebuiltFromACaptureSendsOnlyWhatItMissed(t *testing.T) {
	live := attachedFor(t, "vp_capture_sentuntil")
	overflow(t, live)
	live.captureSnapshot = func(context.Context, int) (tmux.Snapshot, error) {
		return tmux.Snapshot{History: "HISTORY_FROM_TMUX", Screen: "SCREEN_FROM_TMUX", PaneHeight: 24}, nil
	}

	sub, replay := live.SubscribeFrom("desktop", false, Resume{})
	defer live.Unsubscribe(sub)
	if !strings.Contains(string(replay.Data), "HISTORY_FROM_TMUX") {
		t.Fatal("the subscribe did not go through the capture; this test is about the one that does")
	}
	<-sub.Events // the grid

	live.SetHidden(sub, true)
	missed := []byte("written-while-hidden\r\n")
	live.mu.Lock()
	_, _ = live.ring.Write(missed)
	live.broadcastLocked(Event{Kind: EventOutput, Data: missed})
	live.mu.Unlock()

	live.SetHidden(sub, false)
	select {
	case ev := <-sub.Events:
		sub.Took(ev)
		if ev.Kind != EventCatchUp || !ev.Replay.Continued || string(ev.Replay.Data) != string(missed) {
			t.Fatalf("catch-up = kind %v, continued %v, %d bytes; want the %d bytes written while hidden, continuing",
				ev.Kind, ev.Replay.Continued, len(ev.Replay.Data), len(missed))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no catch-up arrived")
	}
}
