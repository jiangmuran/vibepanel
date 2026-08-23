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
	t.Cleanup(func() {
		_ = c.KillServer(context.Background())
		_ = os.Remove(c.SocketPath())
	})
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

	// Subscribing to an unowned session is what makes a viewer the controller.
	desktop, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(desktop)
	phoneSub, _ := live.Subscribe("phone")
	defer live.Unsubscribe(phoneSub)

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
	// Subscribing is what makes a viewer the owner of an unowned grid; a
	// resize from anyone else is ignored by design.
	desktop, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(desktop)
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

func TestFirstViewerTakesControl(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_firstctl"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 120, 32)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Nobody owns a freshly attached session. If the first viewer is not given
	// control it renders passively, scaling the stored grid into a corner of a
	// window it could have filled — and the user is shown a "take control"
	// button for a session nobody else is using.
	a, _ := live.Subscribe("first")
	defer live.Unsubscribe(a)
	if got := live.Controller(); got != "first" {
		t.Fatalf("controller after first subscribe = %q, want %q", got, "first")
	}

	// A second viewer must not steal it just by showing up.
	b, _ := live.Subscribe("second")
	defer live.Unsubscribe(b)
	if got := live.Controller(); got != "first" {
		t.Errorf("controller after second subscribe = %q, want it to stay with %q", got, "first")
	}
}

func TestTypingDoesNotStealTheGrid(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_nosteal"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 120, 32)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	desktop, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(desktop)
	phone, _ := live.Subscribe("phone")
	defer live.Unsubscribe(phone)

	if got := live.Controller(); got != "desktop" {
		t.Fatalf("controller = %q, want the first viewer", got)
	}

	// Answering a prompt from a phone must not reflow the desktop that is
	// mid-edit. Writing is not a claim on the grid — and it cannot be, because
	// xterm sends device-attribute and focus replies down the same channel, so
	// a viewer would take the grid just by loading the page.
	if _, err := live.Write("phone", []byte("y\n")); err != nil {
		t.Fatalf("Write from a passive viewer must be allowed: %v", err)
	}
	if got := live.Controller(); got != "desktop" {
		t.Errorf("controller after the phone typed = %q, want it to stay %q", got, "desktop")
	}
	if cols, rows := live.Size(); cols != 120 || rows != 32 {
		t.Errorf("grid = %dx%d, want the desktop's 120x32", cols, rows)
	}

	// The explicit gesture still works.
	if err := live.TakeControl("phone", 45, 20); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	if got := live.Controller(); got != "phone" {
		t.Errorf("controller after TakeControl = %q, want %q", got, "phone")
	}
}

func TestControlIsFrozenNotHandedOverWhenTheControllerLeaves(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_ctlfreeze"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 120, 32)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	desktop, _ := live.Subscribe("desktop")
	phone, _ := live.Subscribe("phone")
	defer live.Unsubscribe(phone)

	if err := live.Resize("desktop", 147, 46); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	// The desktop reloads its page, which closes its connection. Handing the
	// grid to the phone here would reflow a 147-column agent view down to
	// phone width, and the desktop would come back to find it that way.
	live.Unsubscribe(desktop)
	if got := live.Controller(); got != "" {
		t.Errorf("controller after the owner left = %q, want it unowned", got)
	}
	if cols, rows := live.Size(); cols != 147 || rows != 46 {
		t.Errorf("grid = %dx%d, want it frozen at 147x46", cols, rows)
	}

	// The phone is still passive and cannot move it by resizing its window.
	if err := live.Resize("phone", 13, 30); err != nil {
		t.Fatalf("passive resize: %v", err)
	}
	if cols, _ := live.Size(); cols != 147 {
		t.Errorf("a passive viewer moved the frozen grid to %d columns", cols)
	}

	// The desktop comes back and reclaims it by subscribing.
	back, _ := live.Subscribe("desktop-2")
	defer live.Unsubscribe(back)
	if got := live.Controller(); got != "desktop-2" {
		t.Errorf("controller after the owner returned = %q, want %q", got, "desktop-2")
	}
}
