package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// The reported failure: an agent that animates (Codex's spinner is a
// cursor-addressed repaint several times a second) wraps the ring in under a
// minute, and everything the screen was showing is evicted by frames of the
// animation. Replaying the raw ring then renders the spinner and a blank area
// where the conversation was — 「只能渲染下面的粒子」. tmux has the rendered
// screen regardless, so once the ring has overflowed the replay comes from
// capture-pane, with only the bytes written during the capture from the ring.
func TestAnAnimatedSessionReplaysItsScreenNotItsSpinner(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_anim"
	// A conversation, then an animation that never scrolls: the marker stays
	// in tmux's history while the ring fills with frames and evicts it.
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c",
			`echo CONVERSATION_MARKER; i=0; while [ $i -lt 9000 ]; do printf '\033[20;1Hframe-%05d\033[1;1H' $i; i=$((i+1)); done; sleep 30`},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m := NewManager(tm, 8<<10)
	defer m.DetachAll()
	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for !live.ring.Overflowed() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !live.ring.Overflowed() {
		t.Fatal("the animation never wrapped the ring")
	}

	// What the bug looked like: the raw snapshot holds frames and no marker.
	if raw := string(live.ring.Snapshot()); strings.Contains(raw, "CONVERSATION_MARKER") {
		t.Fatal("the marker was never evicted, so this test measures nothing")
	} else if !strings.Contains(raw, "frame-") {
		t.Fatalf("the ring is not holding the animation it should be full of")
	}

	_, replay := m.Subscribe(ctx, live, "client-a", false)
	text := string(replay)
	if !strings.Contains(text, "CONVERSATION_MARKER") {
		t.Error("the replay lost the conversation to the animation")
	}
	if !strings.Contains(text, "frame-") {
		t.Error("the replay lost the live bytes written during the capture")
	}
}

// The plain path must not change: nothing has been evicted, so the ring's own
// bytes are the faithful replay and no capture is read.
func TestAQuietSessionStillReplaysTheRing(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_quiet"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "echo STILL_HERE; sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m := NewManager(tm, 64<<10)
	defer m.DetachAll()
	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sub, _ := live.Subscribe("client-a")
	if got := collect(t, sub, "STILL_HERE", 5*time.Second); !strings.Contains(got, "STILL_HERE") {
		t.Fatalf("never saw the pane: %q", got)
	}

	_, replay := m.Subscribe(ctx, live, "client-b", false)
	if live.ring.Overflowed() {
		t.Fatal("a quiet session overflowed a 64 KiB ring")
	}
	if !strings.Contains(string(replay), "STILL_HERE") {
		t.Error("the quiet session's replay lost its output")
	}
}
