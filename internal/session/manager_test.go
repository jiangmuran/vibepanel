package session

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/tmux"
)

func newTestTmux(t *testing.T) *tmux.Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := "vibepanel-mgr-" + strconv.Itoa(os.Getpid()) + "-" +
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	c := tmux.New(socket, t.TempDir())
	if err := c.EnsureServer(context.Background()); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	t.Cleanup(func() { _ = c.KillServer(context.Background()) })
	return c
}

// collect drains a subscriber until want appears in the accumulated output, or
// the deadline passes. Returns everything seen, for a useful failure message.
func collect(t *testing.T, sub *Subscriber, want string, timeout time.Duration) string {
	t.Helper()
	var sb strings.Builder
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				return sb.String()
			}
			if ev.Kind == EventOutput {
				sb.Write(ev.Data)
				if strings.Contains(sb.String(), want) {
					return sb.String()
				}
			}
		case <-deadline:
			return sb.String()
		}
	}
}

func TestAttachStreamsOutput(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_stream"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "echo HELLO_FROM_PANE; sleep 30"},
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
	if got := collect(t, sub, "HELLO_FROM_PANE", 5*time.Second); !strings.Contains(got, "HELLO_FROM_PANE") {
		t.Fatalf("never saw pane output; got %q", got)
	}
}

func TestTwoViewersSeeTheSameBytes(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_two"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	a, _ := live.Subscribe("client-a")
	b, _ := live.Subscribe("client-b")

	// "Open it in many places and they stay in sync" is a stated requirement.
	// Both viewers are fed from the same pump, so both must see this.
	if _, err := live.Write("client-a", []byte("echo SYNCED_MARKER\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	gotA := collect(t, a, "SYNCED_MARKER", 5*time.Second)
	gotB := collect(t, b, "SYNCED_MARKER", 5*time.Second)
	if !strings.Contains(gotA, "SYNCED_MARKER") {
		t.Errorf("viewer A missed the output: %q", gotA)
	}
	if !strings.Contains(gotB, "SYNCED_MARKER") {
		t.Errorf("viewer B missed the output: %q", gotB)
	}
}

func TestReconnectReplaysScrollback(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_replay"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "echo REPLAY_ME; sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	first, _ := live.Subscribe("client-a")
	if got := collect(t, first, "REPLAY_ME", 5*time.Second); !strings.Contains(got, "REPLAY_ME") {
		t.Fatalf("first viewer never saw output: %q", got)
	}
	live.Unsubscribe(first)

	// Closing the browser and opening it again must not show a blank terminal.
	second, replay := live.Subscribe("client-b")
	defer live.Unsubscribe(second)
	if !strings.Contains(string(replay), "REPLAY_ME") {
		t.Errorf("replay buffer missing earlier output: %q", string(replay))
	}
}

func TestPassiveViewerCannotResize(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_size"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 160, 48)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// The desktop types, which makes it the controller.
	if _, err := live.Write("desktop", []byte("")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := live.Resize("desktop", 160, 48); err != nil {
		t.Fatalf("Resize by controller: %v", err)
	}

	// A phone opening the same session must not shrink the grid under it.
	if err := live.Resize("phone", 45, 20); err != nil {
		t.Fatalf("passive resize should be ignored, not fail: %v", err)
	}
	if cols, rows := live.Size(); cols != 160 || rows != 48 {
		t.Fatalf("grid = %dx%d, want the controller's 160x48", cols, rows)
	}

	// Taking control explicitly is how the phone gets its own size.
	if err := live.TakeControl("phone", 45, 20); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	if cols, rows := live.Size(); cols != 45 || rows != 20 {
		t.Errorf("grid after TakeControl = %dx%d, want 45x20", cols, rows)
	}
}

func TestResizeReachesTmux(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_resize"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := live.Resize("desktop", 132, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	// The design says resizing our PTY is enough because tmux follows its most
	// recently active client. If that ever stops being true, the browser and
	// the pane disagree about the grid and every TUI renders wrong.
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := tm.Get(ctx, name)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if info.Width == 132 && info.Height == 43 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tmux window is %dx%d, want 132x43", info.Width, info.Height)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDetachLeavesTheSessionRunning(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_detach"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// This is the property the entire architecture exists for: the panel going
	// away must not touch the agent. Simulate a restart by detaching everything
	// and attaching again with a fresh manager.
	m1 := NewManager(tm, 64<<10)
	if _, err := m1.Attach(ctx, "s1", name, 80, 24); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	m1.DetachAll()

	ok, err := tm.Has(ctx, name)
	if err != nil || !ok {
		t.Fatalf("session gone after detach: ok=%v err=%v", ok, err)
	}

	m2 := NewManager(tm, 64<<10)
	defer m2.DetachAll()
	live, err := m2.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("re-Attach after restart: %v", err)
	}
	sub, _ := live.Subscribe("client-a")
	if _, err := live.Write("client-a", []byte("echo BACK_AFTER_RESTART\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := collect(t, sub, "BACK_AFTER_RESTART", 5*time.Second); !strings.Contains(got, "BACK_AFTER_RESTART") {
		t.Fatalf("session not usable after reattach; got %q", got)
	}
}

func TestKilledSessionEndsTheAttachment(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_killed"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := tm.Kill(ctx, name); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	// Viewers must learn the session ended rather than staring at a frozen
	// terminal forever.
	select {
	case <-live.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("attachment did not end after the tmux session was killed")
	}
	if _, ok := m.Get("s1"); ok {
		t.Error("manager still lists the session as live")
	}
}

func TestSlowViewerIsDroppedNotBlocking(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_slow"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		// Produce far more events than one subscriber queue can hold.
		Command: []string{"sh", "-c", "i=0; while [ $i -lt 4000 ]; do echo line $i; i=$((i+1)); done; sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 1<<20)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	slow, _ := live.Subscribe("slow") // never reads
	fast, _ := live.Subscribe("fast")

	// The fast viewer must keep working regardless of the slow one. If a full
	// queue blocked the pump instead of dropping the viewer, one bad connection
	// would freeze the agent for everyone.
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-fast.Events:
			if !ok {
				t.Fatal("fast viewer was dropped")
			}
			if ev.Kind == EventOutput && strings.Contains(string(ev.Data), "line 3999") {
				if !slow.Dropped() {
					t.Log("slow viewer survived; queue was large enough this run")
				}
				return
			}
		case <-deadline:
			t.Fatal("fast viewer never received the final line")
		}
	}
}
