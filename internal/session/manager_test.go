package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	// Point the suite at another tmux without editing anything:
	//	TEST_TMUX_BIN=/path/to/tmux go test ./...
	if bin := os.Getenv("TEST_TMUX_BIN"); bin != "" {
		c.Bin = bin
	}
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

// TestReconnectReplaysRecentOutput covers the hot path: the ring still exists,
// so a second viewer is served from memory.
//
// Named for scrollback once, which it never touched — one line of output fits
// on the visible screen, and the visible screen is served by the repaint. What
// it actually pins is that the ring keeps recent output across a disconnect,
// which is worth pinning and is a different claim.
func TestReconnectReplaysRecentOutput(t *testing.T) {
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

// Taking the grid when it is already the right size still has to tell people.
//
// EventSize is the only message that carries who the controller is — each
// connection recomputes it when forwarding one. Resize returns early when the
// dimensions do not change, so without an explicit broadcast an ownership
// transfer between two windows of the same size reached nobody: the new owner
// went on being offered a grid it already held, and the previous owner went on
// believing its window drove the session while its resizes were ignored.
func TestTakingControlAtTheSameSizeStillTellsEveryone(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_takeover"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	live, err := m.Attach(ctx, "s1", name, 100, 30)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	desktop, _ := live.Subscribe("desktop") // first subscriber owns the grid
	defer live.Unsubscribe(desktop)
	phone, _ := live.Subscribe("phone")
	defer live.Unsubscribe(phone)

	// Subscribe queues a size event of its own, and the shell produces output.
	// Wait for quiet so that anything seen afterwards was caused by the takeover.
	drain := func(sub *Subscriber) {
		for {
			select {
			case <-sub.Events:
			case <-time.After(300 * time.Millisecond):
				return
			}
		}
	}
	drain(desktop)
	drain(phone)

	cols, rows := live.Size()
	if err := live.TakeControl("phone", cols, rows); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	if got := live.Controller(); got != "phone" {
		t.Fatalf("controller = %q, want phone", got)
	}
	if c, r := live.Size(); c != cols || r != rows {
		t.Fatalf("grid moved to %dx%d; the point of this case is that it does not", c, r)
	}

	waitForSize := func(sub *Subscriber) bool {
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev, ok := <-sub.Events:
				if !ok {
					return false
				}
				if ev.Kind == EventSize {
					return true
				}
			case <-deadline:
				return false
			}
		}
	}
	for label, sub := range map[string]*Subscriber{"the new owner": phone, "the previous owner": desktop} {
		if !waitForSize(sub) {
			t.Errorf("%s was never told the grid changed hands", label)
		}
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
	//
	// The same client id, and that is the whole point. This used to read
	// `desktop-2`, because the server minted an identity per connection and a
	// returning viewer was therefore a stranger — so the rule had to be "the
	// next subscriber, whoever it is, takes an unowned grid". Which is exactly
	// how the phone two comments above ends up with it: not by arriving, which
	// is guarded, but by reconnecting once the desktop is gone. The identity is
	// now the browser's own and survives a reconnect, so this can say what it
	// means.
	back, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(back)
	if got := live.Controller(); got != "desktop" {
		t.Errorf("controller after the owner returned = %q, want %q", got, "desktop")
	}
}

func TestResizingDoesNotLookLikeSessionOutput(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_resizequiet"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "echo hello; exec sleep 60"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	var mu sync.Mutex
	var visibleAfterResize bool
	var resizedAt time.Time
	m.OnSignals = func(sig Signals) {
		mu.Lock()
		defer mu.Unlock()
		if !resizedAt.IsZero() && sig.Visible {
			visibleAfterResize = true
		}
	}

	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	viewer, _ := live.Subscribe("viewer")
	defer live.Unsubscribe(viewer)
	time.Sleep(600 * time.Millisecond) // let the attach burst settle

	mu.Lock()
	resizedAt = time.Now()
	mu.Unlock()

	// Opening a session in the browser resizes its grid, which makes tmux
	// repaint. If that repaint counts as output, merely looking at a session
	// that was waiting for you clears the state that said so.
	if err := live.Resize("viewer", 132, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if visibleAfterResize {
		t.Error("the repaint caused by a resize was reported as session output")
	}
}

// Attaching the same session from several goroutines must produce one
// attachment and one tmux client.
//
// The whole size arbitration rests on the panel being tmux's only client:
// `window-size latest` means the grid follows whichever client resized most
// recently, so a second client turns every resize into a fight and the pane
// reflows under a running TUI. Attach checked the map, released the lock, and
// then spent a hundred milliseconds in `capture-pane` and `pty.Start` before
// writing its result back — a window two callers can both walk through. The
// poller attaches every live session and a subscribe attaches on demand, so
// the two callers exist.
func TestConcurrentAttachMakesOneClient(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_race"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sleep", "30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	const n = 8
	var wg sync.WaitGroup
	lives := make([]*Live, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			lives[i], errs[i] = m.Attach(ctx, "s1", name, 80, 24)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Attach %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if lives[i] != lives[0] {
			t.Errorf("caller %d got a different attachment; the loser's PTY is orphaned", i)
		}
	}

	// tmux's own count, not ours: a second client that the manager has
	// forgotten about is exactly the one that does the damage.
	//
	// Polled, because Attach returns when the PTY is started and the tmux
	// client registers with the server a moment later — asking once made the
	// test read zero whenever the machine was busy, which is exactly when the
	// whole suite runs. Any count above one fails immediately; one is confirmed
	// again after a pause, so a straggler cannot slip in behind the check.
	deadline := time.Now().Add(10 * time.Second)
	clients := 0
	for time.Now().Before(deadline) {
		clients = countClients(t, tm, name)
		if clients > 1 {
			t.Fatalf("tmux has %d clients on the session, want 1", clients)
		}
		if clients == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if clients != 1 {
		t.Fatalf("no tmux client ever attached")
	}
	time.Sleep(500 * time.Millisecond)
	if again := countClients(t, tm, name); again != 1 {
		t.Fatalf("tmux settled on %d clients, want 1", again)
	}
}

func countClients(t *testing.T, tm *tmux.Client, name string) int {
	t.Helper()
	out, err := exec.Command(tm.Bin, "-S", tm.SocketPath(), "list-clients", "-t", "="+name).Output()
	if err != nil {
		// No clients at all is an error exit from tmux, not a broken test.
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// A viewer that subscribes while output is flowing must see every byte exactly
// once — not twice.
//
// Replay and live delivery are two paths to the same viewer, and the join
// between them is the subscribe. The ring used to be written before the
// broadcast with the lock released in between, so a viewer registering in that
// gap took a snapshot that already contained the chunk and was then sent it
// again. On screen that is a line printed twice, which in a terminal is
// indistinguishable from the program having done it.
//
// The output has to still be flowing while the subscribes happen. The first
// version of this printed four hundred lines, which a shell finishes inside a
// second, and then subscribed forty times to a session that had gone quiet —
// so it passed with the bug deliberately reintroduced. A test for a race that
// never races is worth less than no test, because it is counted as cover.
//
// What this does and does not prove, measured rather than assumed. With the
// old ordering restored it still passes: the real window is the few
// instructions between the ring write and the lock, and seven hundred
// subscribes do not land in it. Widen that window by fifty microseconds and it
// fails on the second attempt. So it catches a regression that reintroduces
// the gap in any form a person would write, and it cannot certify the absence
// of the nanosecond-wide original — which is also the measure of how rare that
// original was. The fix is correct by construction, not by this test; this is
// here to notice if somebody takes it apart.
func TestSubscribingDuringOutputDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_dup"
	const lines = 20000
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 200, Height: 50,
		Command: []string{"sh", "-c",
			"i=0; while [ $i -lt " + strconv.Itoa(lines) + " ]; do echo LINE-$i; i=$((i+1)); done; sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m := NewManager(tm, 4<<20)
	defer m.DetachAll()
	live, err := m.Attach(ctx, "s1", name, 200, 50)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Subscribe over and over for as long as the shell is still printing. Each
	// viewer lives a few milliseconds — long enough to be handed a chunk it
	// might already have in its replay, short enough to get many attempts in.
	stop := time.After(6 * time.Second)
	attempts := 0
	overlaps := 0
	for {
		select {
		case <-stop:
			if attempts < 50 {
				t.Fatalf("only %d subscribes in the window; the test is not exercising anything",
					attempts)
			}
			t.Logf("%d subscribes during live output", attempts)
			return
		default:
		}
		attempts++

		sub, replay := live.Subscribe("viewer")
		seen := append([]byte(nil), replay...)
		read := time.After(8 * time.Millisecond)
	drain:
		for {
			select {
			case ev, ok := <-sub.Events:
				if !ok {
					break drain
				}
				if ev.Kind == EventOutput {
					seen = append(seen, ev.Data...)
				}
			case <-read:
				break drain
			}
		}
		live.Unsubscribe(sub)

		// Only the boundary matters. A line wholly inside the replay, or wholly
		// in the live stream, cannot be doubled by this; what can is a line
		// delivered by both paths. So look at what the live part contained and
		// ask whether the replay already had it.
		full := string(seen)
		liveOnly := full[min(len(replay), len(full)):]
		replayed := full[:min(len(replay), len(full))]
		for _, marker := range markers(liveOnly) {
			if strings.Contains(replayed, marker) {
				overlaps++
				t.Fatalf("attempt %d: %q was in the replay and sent again live (%d overlaps)",
					attempts, marker, overlaps)
			}
		}
	}
}

// markers pulls whole "LINE-<n>\r\n" tokens out of a slice of terminal output.
//
// A token with an escape sequence inside it is not one. tmux repaints the
// screen when it falls behind, and a repainted line arrives as
// "LINE-1\x1b[K\r\n" -- the same text the pane already printed, redrawn. That
// is the terminal doing its job, not the same bytes delivered twice, and
// counting it as a duplicate made this test fail on a two-core CI runner while
// passing on anything wider. Reproduced here under `taskset -c 0,1`, which is
// the only reason it was identifiable as a repaint rather than as the race
// this test is named for.
//
// Plain lines are still compared, and a genuine double-delivery would double
// those: the window this test exists to notice is between the ring write and
// the broadcast, and what is in flight there is ordinary output.
func markers(s string) []string {
	var out []string
	for _, part := range strings.Split(s, "LINE-")[1:] {
		end := strings.Index(part, "\r\n")
		if end <= 0 {
			continue // truncated at the edge of this slice; not a whole line
		}
		if strings.ContainsRune(part[:end], 0x1b) {
			continue // a repaint of a line, not a second delivery of it
		}
		out = append(out, "LINE-"+part[:end]+"\r\n")
	}
	return out
}

// Everything at once, against one session, with the race detector watching.
//
// The two races found by reading — Attach building two attachments, and the
// ring being written outside the lock that Subscribe snapshots under — were
// both in code that read as correct. This is the other way of looking: drive
// every entry point on a Live concurrently and let -race decide, then close it
// underneath them and require the close to finish and nothing to panic.
//
// It is not a proof. It is the cheapest way to find the third one.
func TestConcurrentUseOfALiveSession(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_chaos"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		// Something that prints, so the pump is doing work throughout.
		Command: []string{"sh", "-c", "i=0; while :; do echo line-$i; i=$((i+1)); done"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m := NewManager(tm, 256<<10)
	defer m.DetachAll()
	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Viewers coming and going, each reading what it is sent.
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sub, _ := live.Subscribe(fmt.Sprintf("viewer-%d", n))
				deadline := time.After(40 * time.Millisecond)
			drain:
				for {
					select {
					case _, ok := <-sub.Events:
						if !ok {
							break drain
						}
					case <-deadline:
						break drain
					case <-stop:
						break drain
					}
				}
				live.Unsubscribe(sub)
			}
		}(i)
	}

	// Input, resizes and control changes from several clients at once.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("viewer-%d", n)
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = live.Write(id, []byte("x"))
				_ = live.Resize(id, 80+n, 24+n)
				_ = live.TakeControl(id, 100+n, 30+n)
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Let it run, then pull the floor out.
	time.Sleep(700 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		m.Detach("s1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Detach did not return while the session was in use; something holds a lock " +
			"across the close")
	}

	close(stop)
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("callers were still blocked ten seconds after the session closed")
	}
}

// The manager itself, under the same treatment.
//
// Attach and Detach against the same names from several goroutines, while
// others ask what is live. The claim that Attach takes before building an
// attachment is what makes this safe; before it existed, this test is how the
// eight-clients bug would have announced itself without anybody reading the
// code.
func TestConcurrentAttachAndDetach(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	names := []string{"vp_c1", "vp_c2", "vp_c3"}
	for _, n := range names {
		if err := tm.Create(ctx, tmux.CreateOptions{
			Name: n, Dir: t.TempDir(), Width: 80, Height: 24,
			Command: []string{"sleep", "60"},
		}); err != nil {
			t.Fatalf("Create %s: %v", n, err)
		}
	}

	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("s%d", n%len(names))
			name := names[n%len(names)]
			for {
				select {
				case <-stop:
					return
				default:
				}
				switch n % 4 {
				case 0, 1:
					if l, err := m.Attach(ctx, id, name, 80, 24); err == nil && l != nil {
						_ = l.Controller()
					}
				case 2:
					m.Detach(id)
				case 3:
					_ = m.LiveIDs()
					_, _ = m.Get(id)
				}
			}
		}(i)
	}

	time.Sleep(600 * time.Millisecond)
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("attach/detach callers did not finish; something is deadlocked")
	}

	// However the interleaving went, tmux must not be left with more clients
	// than sessions: an attachment the manager forgot is one nothing can close.
	m.DetachAll()
	// Longer than the two seconds close() gives a tmux client to exit on its
	// own before killing it. Half a second is not a leak, it is impatience.
	time.Sleep(3 * time.Second)
	for _, n := range names {
		out, err := exec.Command(tm.Bin, "-S", tm.SocketPath(), "list-clients", "-t", "="+n).Output()
		if err != nil {
			continue // no clients at all is an error exit, which is what we want
		}
		count := 0
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.TrimSpace(line) != "" {
				count++
			}
		}
		if count > 0 {
			t.Errorf("%s still has %d client(s) after DetachAll:\n%s", n, count, out)
		}
	}
}

// A detach issued while an attach is being built must win.
//
// Attach spends real time before it installs anything — capture-pane, then
// starting a PTY — and during that window the session is in neither map a
// caller can see. A Detach arriving then found nothing, returned, and the
// attach installed itself afterwards into a manager the caller had just been
// told was empty.
//
// The consequences are bounded in the panel as it stands: every caller of
// Detach either kills the tmux session immediately afterwards or is on the way
// out of the process. That is a property of today's callers rather than of the
// manager, which is why this is a test rather than a comment.
func TestDetachDuringAttachWins(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_lost"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sleep", "60"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	// Wait for the attach to have claimed the session before detaching.
	//
	// Firing the detach immediately instead tests the opposite thing: a detach
	// that arrives *before* an attach starts should not stop it, because
	// "attach after detach" is an ordinary sequence. The window that matters is
	// between the claim and the install, and the claim is observable from
	// inside the package.
	for i := 0; i < 5; i++ {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Attach(ctx, "s1", name, 80, 24)
		}()

		claimed := false
		for w := 0; w < 500; w++ {
			m.mu.Lock()
			_, building := m.attaching["s1"]
			_, live := m.live["s1"]
			m.mu.Unlock()
			if building && !live {
				claimed = true
				break
			}
			if live {
				break // too slow: it finished before we looked
			}
			time.Sleep(time.Millisecond)
		}
		if !claimed {
			wg.Wait()
			m.Detach("s1")
			continue // this attempt did not reproduce the window; try again
		}

		m.Detach("s1")
		wg.Wait()

		if l, ok := m.Get("s1"); ok {
			m.Detach("s1")
			t.Fatalf("attempt %d: the session is attached after a detach was asked for while it "+
				"was being built; nothing that called Detach can close it (%p)", i, l)
		}
	}
}

// attachedFor is the preamble every arbitration test below needs: a real tmux
// session, attached, at a known grid.
func attachedFor(t *testing.T, name string) *Live {
	t.Helper()
	ctx := context.Background()
	tm := newTestTmux(t)
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 120, Height: 40,
		Command: []string{"sh", "-c", "sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	t.Cleanup(m.DetachAll)
	live, err := m.Attach(ctx, "s1", name, 120, 40)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return live
}

func TestAReconnectDoesNotTakeAGridSomebodyElseWasDriving(t *testing.T) {
	// The freeze on unsubscribe exists to stop "the phone glancing at the
	// session from across the room" reflowing an agent when the desktop's page
	// closes. It only ever protected the instant of departure: the controller
	// was cleared, and the very next subscribe — from anyone — took the grid.
	//
	// Measured against the real panel before this was fixed. Desktop owns
	// 112x34, phone joins and correctly stays passive, desktop's tab closes,
	// and then the phone merely reloads: 46x34. A reload is not a request for
	// anything, and on a phone it is what happens when the browser feels like
	// it.
	live := attachedFor(t, "vp_arb_steal")

	desktop, _ := live.Subscribe("desktop")
	if got := live.Controller(); got != "desktop" {
		t.Fatalf("controller = %q, want the first viewer to own an untouched session", got)
	}
	phone, _ := live.Subscribe("phone")
	if got := live.Controller(); got != "desktop" {
		t.Fatalf("controller = %q; arriving must not take the grid", got)
	}

	live.Unsubscribe(desktop)
	live.Unsubscribe(phone)
	again, _ := live.Subscribe("phone")
	defer live.Unsubscribe(again)
	if got := live.Controller(); got != "" {
		t.Errorf("controller = %q after the phone reconnected; the grid was frozen for "+
			"the viewer who left, and reconnecting is not asking for it", got)
	}
	if c, r := live.Size(); c != 120 || r != 40 {
		t.Errorf("grid is %dx%d, want it frozen at 120x40", c, r)
	}
}

func TestTheViewerWhoLeftReclaimsItsOwnGrid(t *testing.T) {
	// The other half, and the reason this is keyed on identity rather than
	// simply never releasing the grid: a dropped socket must not cost the
	// person driving the session their own window.
	live := attachedFor(t, "vp_arb_back")

	desktop, _ := live.Subscribe("desktop")
	live.Unsubscribe(desktop)
	back, _ := live.Subscribe("desktop")
	defer live.Unsubscribe(back)
	if got := live.Controller(); got != "desktop" {
		t.Errorf("controller = %q; the viewer that owned it came back and had to ask", got)
	}
}

func TestTakingControlWorksOnAGridFrozenForSomebodyElse(t *testing.T) {
	// The escape hatch, which is what makes the rule above affordable.
	//
	// Declining to hand the grid to a reconnecting stranger is only reasonable
	// because pressing the button always works. If a later change ever guards
	// TakeControl with the same identity check that guards Subscribe, a viewer
	// whose colleague closed their laptop is stuck scaling a grid it cannot
	// have, with a button that does nothing.
	live := attachedFor(t, "vp_arb_take")

	desktop, _ := live.Subscribe("desktop")
	live.Unsubscribe(desktop) // frozen at 120x40, remembered for "desktop"

	phone, _ := live.Subscribe("phone")
	defer live.Unsubscribe(phone)
	if got := live.Controller(); got != "" {
		t.Fatalf("controller = %q, want the grid still frozen", got)
	}
	if err := live.TakeControl("phone", 45, 20); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	if got := live.Controller(); got != "phone" {
		t.Errorf("controller = %q after the phone pressed the button", got)
	}
	if c, r := live.Size(); c != 45 || r != 20 {
		t.Errorf("grid is %dx%d, want the taker's 45x20", c, r)
	}
}

func TestAFreshViewerIsFilledWithoutWaitingForOutput(t *testing.T) {
	// A session that has printed everything it is going to print, and is now
	// sitting idle, still has to fill a terminal that opens on it. After a
	// panel restart the ring is empty, so nothing in the panel can supply
	// that; what does is tmux repainting the visible screen on attach, which
	// arrives through the pump like any other output.
	//
	// This is the guarantee the replay priming was written for. The priming
	// could not deliver it: it fetched the history *above* the screen, and
	// tmux's attach begins with ESC[?1049h, so what it fetched was covered by
	// the alternate screen a millisecond later. Deleting it changed nothing
	// anyone could see. The repaint was doing the work all along, and it is
	// the thing worth a test — without it, every session opened after a
	// restart is a blank rectangle attached to a live process.
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_fresh"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 10,
		Command: []string{"sh", "-c", "for i in $(seq 1 40); do echo HISTLINE_$i; done; sleep 30"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	// Everything is printed and over with before anything attaches, so no live
	// output can arrive to paper over a missing repaint.
	time.Sleep(1500 * time.Millisecond)

	live, err := m.Attach(ctx, "s1", name, 80, 10)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sub, _ := live.Subscribe("client-a")
	defer live.Unsubscribe(sub)

	if got := collect(t, sub, "HISTLINE_40", 8*time.Second); !strings.Contains(got, "HISTLINE_40") {
		t.Errorf("a viewer opening an idle session was never sent its screen; it would "+
			"show a blank terminal until the session happened to print something: %q", got)
	}
}

func TestDetachAllDoesNotTakeTwoSecondsPerSession(t *testing.T) {
	// A tmux client does not exit when its PTY closes, so close() falls
	// through to the timer that kills it two seconds later. That timer is the
	// normal path, not the exception, and closing serially made shutdown cost
	// two seconds per attached session: measured 2025ms for one, 8030ms for
	// four, 16033ms for eight.
	//
	// The unit sets TimeoutStopSec=20 and says stopping "must not wait on
	// anything". Past ten sessions it waits longer than that and systemd
	// SIGKILLs the panel — on the setup this panel exists for, which runs a
	// couple of dozen agents.
	//
	// The bound is deliberately loose. What is being pinned is that the cost
	// does not scale with the count, not the two seconds itself, and a loaded
	// machine should not make this flake.
	ctx := context.Background()
	tm := newTestTmux(t)
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	const n = 6
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("vp_detach_%d", i)
		if err := tm.Create(ctx, tmux.CreateOptions{
			Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
			Command: []string{"sh", "-c", "sleep 60"},
		}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		if _, err := m.Attach(ctx, fmt.Sprintf("s%d", i), name, 80, 24); err != nil {
			t.Fatalf("Attach %s: %v", name, err)
		}
	}

	start := time.Now()
	m.DetachAll()
	took := time.Since(start)
	if took > 8*time.Second {
		t.Errorf("detaching %d sessions took %v; serially that is two seconds each, and "+
			"systemd stops waiting at twenty", n, took.Round(time.Millisecond))
	}
}

func TestDetachOfAKilledSessionDoesNotWaitForItself(t *testing.T) {
	// The pump's cleanup used to call close(), close() waited on the channel
	// that same goroutine closes afterwards, and it was released only by the
	// pumpDrain timeout. Two seconds on every teardown, and it looked exactly
	// like the killer timer below it, which is also two seconds — so the
	// arithmetic agreed with the wrong cause.
	//
	// Measured through the HTTP API before and after: deleting one session
	// 2015ms → 14ms, deleting a project with five sessions 10029ms → 25ms.
	//
	// This kills the tmux session first, which is what the delete paths do:
	// the client then exits on its own, so nothing here is waiting on the
	// killer timer and the pump's self-wait is the only thing left to catch.
	ctx := context.Background()
	tm := newTestTmux(t)
	m := NewManager(tm, 64<<10)
	defer m.DetachAll()

	const name = "vp_selfwait"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "sleep 60"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.Attach(ctx, "s", name, 80, 24); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	if err := tm.Kill(ctx, name); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	start := time.Now()
	m.Detach("s")
	if took := time.Since(start); took > pumpDrain/2 {
		t.Errorf("detaching a session whose tmux session is already gone took %v; "+
			"the pump is waiting for itself again", took.Round(time.Millisecond))
	}
}

func TestTmuxDoesNotTakeTheAlternateScreenOrDiscardScrolledLines(t *testing.T) {
	// Two lines in the embedded tmux config decide whether the panel has any
	// scrollback at all, and neither is visible from anywhere else.
	//
	// A tmux client's first write to its terminal is ESC[?1049h. The alternate
	// screen has no scrollback by definition, so everything the panel rendered
	// was on a buffer that keeps nothing: 20,000 lines of tmux history per
	// session, not one of them reachable, on any device. And tmux scrolls with
	// CSI Ps S, which discards what goes off the top — only a line feed at the
	// bottom margin hands a line to the terminal to keep.
	//
	// stress-check drives a browser and scrolls, which is the property a person
	// has. This is the same two facts at the byte level, so a config change
	// that breaks them fails in seconds rather than in a twenty-minute browser
	// run — and says which of the two it was.
	ctx := context.Background()
	tm := newTestTmux(t)
	m := NewManager(tm, 1<<20)
	defer m.DetachAll()

	const name = "vp_scrollbytes"
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "i=1; while [ $i -le 200 ]; do echo ROW_$i; i=$((i+1)); done; exec sleep 60"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	live, err := m.Attach(ctx, "s", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sub, replay := live.Subscribe("probe")
	var got bytes.Buffer
	got.Write(replay)
	deadline := time.After(6 * time.Second)
collect:
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				break collect
			}
			if ev.Kind == EventOutput {
				got.Write(ev.Data)
			}
		case <-deadline:
			break collect
		}
	}

	if got.Len() < 200 {
		t.Fatalf("only %d bytes arrived; the session produced nothing to judge", got.Len())
	}
	if bytes.Contains(got.Bytes(), []byte("\x1b[?1049h")) {
		t.Error("tmux put this PTY on the alternate screen, which has no scrollback: " +
			"terminal-overrides needs smcup@ and rmcup@")
	}
	if su := regexp.MustCompile(`\x1b\[[0-9]*S`).FindAll(got.Bytes(), -1); len(su) > 0 {
		t.Errorf("tmux scrolled with CSI Ps S %d times, which throws the lines away "+
			"instead of handing them to the terminal: terminal-overrides needs indn@", len(su))
	}
}

func TestTheAdvanceSignalIsComputedFromWhatTmuxSends(t *testing.T) {
	// The rule that an animation does not clear a bell is covered in
	// detect_test with hand-made Signals. Nothing covered the other half —
	// the pump deciding what Advanced means — so inverting that line passed
	// every test in the package.
	//
	// This drives real panes through the real pump into the real detector.
	ctx := context.Background()
	tm := newTestTmux(t)
	m := NewManager(tm, 1<<20)
	defer m.DetachAll()

	det := NewDetector()
	m.OnSignals = func(sig Signals) { det.Observe(sig.SessionID, sig, time.Now()) }

	// ShellOnly is passed as false throughout: what is under test is the
	// signal, not the classification of the pane's command.
	start := func(id, script string) {
		if err := tm.Create(ctx, tmux.CreateOptions{
			Name: id, Dir: t.TempDir(), Width: 80, Height: 24,
			Command: []string{"sh", "-c", script},
		}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
		if _, err := m.Attach(ctx, id, id, 80, 24); err != nil {
			t.Fatalf("Attach %s: %v", id, err)
		}
	}

	// Each script waits before it rings.
	//
	// Without that the bell fires in the same millisecond the pane starts,
	// which is a race against the panel's own attach: tmux latches a bell that
	// rang with nobody watching, and this test has no poller to read the latch
	// back -- that backstop belongs to reconciliation, not to the Manager. On
	// tmux 3.6 the attach won often enough that the race was invisible; on 3.4
	// it lost every time and the failure read as "the advance signal is wrong",
	// which is the thing this test is actually about and was not what broke.

	// Rings, then redraws one line in place — an agent waiting with a live
	// "esc to interrupt" under its question.
	start("vp_sig_spin",
		"sleep 1; printf 'proceed?\\a'; while :; do for c in '|' '/' '-'; do printf '\\r%s waiting' \"$c\"; sleep 0.2; done; done")
	// Rings, then produces output — an agent that was answered and went on.
	start("vp_sig_work",
		"sleep 1; printf 'proceed?\\a'; sleep 1; i=0; while :; do i=$((i+1)); printf 'reading file %d\\n' \"$i\"; sleep 0.2; done")

	time.Sleep(9 * time.Second)

	if st, _ := det.Evaluate("vp_sig_spin", Observation{}, time.Now()); st != StateWaiting {
		t.Errorf("an agent that rang and is animating reads as %q, want %q", st, StateWaiting)
	}
	if st, _ := det.Evaluate("vp_sig_work", Observation{}, time.Now()); st != StateWorking {
		t.Errorf("an agent that rang and went back to work reads as %q, want %q", st, StateWorking)
	}
}

// A repaint is not progress, and an older tmux repaints constantly.
//
// The bytes below are real: captured from a PTY attached to tmux 3.4 -- what
// Ubuntu 24.04 LTS ships -- watching a pane run an agent's spinner and, in the
// second case, an agent printing lines. On 3.4 the spinner arrives as a
// whole-screen repaint several times a second, and the old rule (`the chunk
// contains a line feed`) called every one of them progress. That cleared the
// bell on an agent that was still waiting for an answer, which is the one state
// this panel exists to report. tmux 3.6 repaints far less eagerly, so every
// test here passed on the machine the rule was written on.
func TestARepaintIsNotProgress(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chunk string
		want  bool
	}{
		{
			"tmux 3.4 repainting a spinner where it stands",
			"\x1b[1;1H\x1b[1;24r\x1b[1;10H\x1b[?25l\x1b[H- waiting\x1b[K\r\n\x1b[K\r\n\x1b[K\r\n\x1b[K\r\n",
			false,
		},
		{"the ESC[0K spelling of the same erase", "x\x1b[0K\r\ny\x1b[0K\r\n", false},
		{"an agent printing a line", "reading file 12\r\n", true},
		{"a repaint that then prints something new", "\x1b[K\r\n\x1b[K\r\nreading file 13\r\n", true},
		{"a bare line feed", "done\n", true},
		{"a spinner with no line feed at all", "\r| waiting", false},
		{"nothing", "", false},
		// A line feed at the very start cannot have been erased first.
		{"a leading line feed", "\nhello", true},
	} {
		if got := advanced([]byte(tc.chunk)); got != tc.want {
			t.Errorf("%s: advanced = %v, want %v (%q)", tc.name, got, tc.want, tc.chunk)
		}
	}
}

// Scrolling back must reach what happened before anybody was watching.
//
// Without priming, the ring holds only what arrived while a browser was
// attached, and attaching makes tmux repaint one screenful. Open a panel on an
// agent that has been working for an hour and there is that screenful and
// nothing above it -- on every device, with tmux holding twenty thousand lines
// the whole time. That is the reported "无法滚动向上/向下".
func TestAttachBringsThePanesHistoryWithIt(t *testing.T) {
	ctx := context.Background()
	tm := newTestTmux(t)
	const name = "vp_history"
	// Enough lines to be certainly above the visible screen, and numbered so
	// the assertion can say which one it wanted.
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: t.TempDir(), Width: 80, Height: 24,
		Command: []string{"sh", "-c", "i=1; while [ $i -le 200 ]; do echo HIST_$i; i=$((i+1)); done; exec sleep 60"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Let the burst finish before attaching: the point is that this output
	// happened with nobody watching.
	time.Sleep(2500 * time.Millisecond)

	m := NewManager(tm, 1<<20)
	defer m.DetachAll()
	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// The repaint arrives asynchronously; the history does not have to wait for
	// it, but give both a moment so a failure cannot be about timing.
	time.Sleep(1500 * time.Millisecond)

	_, replay := live.Subscribe("viewer")
	text := string(replay)
	// HIST_1 is far above the last screenful of a 24-row pane.
	if !strings.Contains(text, "HIST_1\r") {
		last := text
		if len(last) > 400 {
			last = last[len(last)-400:]
		}
		t.Errorf("the replay does not reach the start of the history; its last 400 bytes are %q", last)
	}
	if !strings.Contains(text, "HIST_200") {
		t.Errorf("the replay is missing the end of the history")
	}
}
