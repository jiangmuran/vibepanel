package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// oneSession resolves every id to the same tmux session.
type oneSession struct{ name string }

func (r oneSession) Resolve(context.Context, string) (string, int, int, error) {
	return r.name, 0, 0, nil
}
func (oneSession) RecordSize(context.Context, string, int, int) error { return nil }

// served is a real tmux session behind a real socket handler.
type served struct {
	url     string
	live    *session.Live
	handler *Handler
}

// serveSession starts a tmux session on a throwaway socket, lets script finish
// writing, attaches it and serves it over a WebSocket.
func serveSession(t *testing.T, name, script string) served {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx := context.Background()
	socket := tmux.BoundSocketName("vibepanel-ws-" + strconv.Itoa(os.Getpid()) + "-" + t.Name())
	tm := tmux.New(socket, t.TempDir())
	if err := tm.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	t.Cleanup(func() {
		_ = tm.KillServer(context.Background())
		_ = os.Remove(tm.SocketPath())
	})

	dir := t.TempDir()
	done := filepath.Join(dir, "written")
	if err := tm.Create(ctx, tmux.CreateOptions{
		Name: name, Dir: dir, Width: 80, Height: 24,
		Command: []string{"sh", "-c", script + "; touch " + done + "; exec sleep 60"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The replay is primed from tmux's history at attach, so the output has to
	// be there first.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session never finished writing its output")
		}
	}
	time.Sleep(300 * time.Millisecond)

	m := session.NewManager(tm, 1<<20)
	t.Cleanup(m.DetachAll)
	live, err := m.Attach(ctx, "s1", name, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	h := &Handler{Manager: m, Resolve: oneSession{name}}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return served{url: "ws" + strings.TrimPrefix(srv.URL, "http"), live: live, handler: h}
}

type received struct {
	typ  websocket.MessageType
	data []byte
}

// viewer is one browser connection: everything it is sent, in order.
type viewer struct {
	t    *testing.T
	conn *websocket.Conn
	in   chan received
}

func dialViewer(t *testing.T, s served, client string) *viewer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, s.url+"?client="+client, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadLimit(4 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	v := &viewer{t: t, conn: conn, in: make(chan received, 4096)}
	// One reader for the life of the connection. A read whose context expires
	// closes a coder/websocket connection, so waiting is done on the channel.
	go func() {
		defer close(v.in)
		for {
			typ, data, rerr := conn.Read(ctx)
			if rerr != nil {
				return
			}
			v.in <- received{typ, data}
		}
	}()
	return v
}

func (v *viewer) send(msg map[string]any) {
	v.t.Helper()
	b, _ := json.Marshal(msg)
	if err := v.conn.Write(context.Background(), websocket.MessageText, b); err != nil {
		v.t.Fatalf("write: %v", err)
	}
}

// await skips everything until a control message of this type arrives.
func (v *viewer) await(kind string) ServerMessage {
	v.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case r, ok := <-v.in:
			if !ok {
				v.t.Fatalf("the connection closed while waiting for %q", kind)
			}
			if r.typ != websocket.MessageText {
				continue
			}
			var msg ServerMessage
			if err := json.Unmarshal(r.data, &msg); err != nil {
				continue
			}
			if msg.Type == MsgError {
				v.t.Fatalf("waiting for %q, the server said: %s", kind, msg.Message)
			}
			if msg.Type == kind {
				return msg
			}
		case <-timeout:
			v.t.Fatalf("no %q message within five seconds", kind)
		}
	}
}

func TestReplayFramesSplitInOrderAtTheBoundary(t *testing.T) {
	if got := replayFrames(7, nil); len(got) != 0 {
		t.Errorf("an empty snapshot is %d frames, want none", len(got))
	}
	for _, n := range []int{1, replayChunk, replayChunk + 1, 3*replayChunk - 1} {
		snapshot := make([]byte, n)
		for i := range snapshot {
			snapshot[i] = byte(i * 31)
		}
		frames := replayFrames(7, snapshot)
		if want := (n + replayChunk - 1) / replayChunk; len(frames) != want {
			t.Errorf("%d bytes became %d frames, want %d", n, len(frames), want)
		}
		var joined []byte
		for _, f := range frames {
			ref, payload, err := DecodeData(f)
			if err != nil || ref != 7 || f[0] != FrameReplay {
				t.Fatalf("frame is not replay for stream 7: type %d ref %d err %v", f[0], ref, err)
			}
			joined = append(joined, payload...)
		}
		if !bytes.Equal(joined, snapshot) {
			t.Errorf("%d bytes did not survive being split", n)
		}
	}
}

// What a subscribe actually puts on the wire. The unit test above says what
// replayFrames does; this says that subscribe is what uses it.
func TestAReplayReachesTheBrowserInBoundedFrames(t *testing.T) {
	// 1500 lines of 64 characters is ~97 KiB of history, inside tmux's default
	// history-limit of 2000 and more than one frame's worth.
	s := serveSession(t, "vp_ws_replay",
		`i=0; while [ $i -lt 1500 ]; do printf '%064d\n' $i; i=$((i+1)); done`)
	v := dialViewer(t, s, "replay-viewer")
	v.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24})
	confirmed := v.await(MsgSubscribed)
	if confirmed.ReplayBytes <= replayChunk || confirmed.ReplayChunks < 2 {
		t.Fatalf("subscription did not announce the replay accurately: %d bytes in %d chunks", confirmed.ReplayBytes, confirmed.ReplayChunks)
	}

	var frames [][]byte
	for quiet := false; !quiet; {
		select {
		case r := <-v.in:
			if r.typ == websocket.MessageBinary && len(r.data) > 0 && r.data[0] == FrameReplay {
				frames = append(frames, r.data)
			}
		case <-time.After(700 * time.Millisecond):
			quiet = true
		}
	}

	var joined []byte
	for i, f := range frames {
		if len(f)-binaryHeaderLen > replayChunk {
			t.Errorf("replay frame %d carries %d bytes, more than %d", i, len(f)-binaryHeaderLen, replayChunk)
		}
		joined = append(joined, f[binaryHeaderLen:]...)
	}
	if len(joined) <= replayChunk {
		t.Fatalf("the snapshot was %d bytes, not enough to need a second frame; "+
			"this test is not reaching the path it is about", len(joined))
	}
	if len(frames) < 2 {
		t.Errorf("a %d byte snapshot went out as %d frame: a phone waits for all of it "+
			"before the terminal shows anything", len(joined), len(frames))
	}
	// Lines from the history, not the last one printed. The snapshot is the
	// history primed at attach, and measured here it ended at line 1476 of
	// 1499: the screenful below that is not in it as text.
	early := bytes.Index(joined, []byte(fmt.Sprintf("%064d", 100)))
	late := bytes.Index(joined, []byte(fmt.Sprintf("%064d", 1400)))
	if early < 0 || late < 0 || early > late {
		t.Errorf("the scrollback did not arrive whole and in order (line 100 at %d, line 1400 at %d)", early, late)
	}
}

func TestVisibilityMovesTheGridAndTellsTheViewer(t *testing.T) {
	s := serveSession(t, "vp_ws_visible", "true")
	const client = "visible-viewer"

	a := dialViewer(t, s, client)
	a.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24})
	if !a.await(MsgSubscribed).Controlling {
		t.Fatal("the first viewer was not given the grid; nothing below means anything")
	}
	a.await(MsgSize) // the one every subscribe starts with

	a.send(map[string]any{"t": MsgVisibility, "sessionId": "s1", "hidden": true})
	if a.await(MsgSize).Controlling {
		t.Error("a terminal that went off-screen was told it still controls the grid")
	}
	if got := s.live.Controller(); got != "" {
		t.Errorf("controller = %q after the only viewer hid its terminal, want it unowned", got)
	}

	// The same browser on a second socket, resubscribing a terminal it keeps
	// off-screen: what a reconnect sends.
	b := dialViewer(t, s, client)
	b.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24, "hidden": true})
	b.await(MsgSubscribed)
	if got := s.live.Controller(); got != "" {
		t.Errorf("controller = %q after a hidden subscribe; it arrived as a viewer and claimed the grid", got)
	}

	a.send(map[string]any{"t": MsgVisibility, "sessionId": "s1", "hidden": false})
	if !a.await(MsgSize).Controlling {
		t.Error("a terminal back on screen was not told it controls the grid again, so it " +
			"scales its own session and never publishes its size")
	}
	if got := s.live.Controller(); got != client {
		t.Errorf("controller = %q after coming back on screen, want %q", got, client)
	}
}

// A background tab keeps a grid it holds and never claims one it does not.
// See session.Subscriber.background for why both halves.
func TestABackgroundTabKeepsTheGridButNeverClaimsItOverTheWire(t *testing.T) {
	s := serveSession(t, "vp_ws_bg", "true")

	const macID = "mac-background-tab"
	mac := dialViewer(t, s, macID)
	mac.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 107, "rows": 32})
	if !mac.await(MsgSubscribed).Controlling {
		t.Fatal("initial foreground viewer was not given control")
	}
	mac.await(MsgSize)

	// The phone's owner switches to another app for a moment. The session
	// stays theirs: releasing here handed it to the desktop at its width.
	mac.send(map[string]any{"t": MsgVisibility, "sessionId": "s1", "hidden": false, "background": true})
	if !mac.await(MsgSize).Controlling {
		t.Error("a backgrounded tab was told it no longer controls its grid")
	}
	if got := s.live.Controller(); got != macID {
		t.Fatalf("controller = %q after the tab went to the background, want %q kept", got, macID)
	}

	// Hiding is still leaving.
	mac.send(map[string]any{"t": MsgVisibility, "sessionId": "s1", "hidden": true, "background": true})
	if mac.await(MsgSize).Controlling {
		t.Error("a hidden tab was still told it controls the grid")
	}

	// A sleeping laptop's reconnect, backgrounded, must not take the grid
	// nobody holds.
	bg := dialViewer(t, s, "sleeping-mac")
	bg.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 107, "rows": 32, "hidden": false, "background": true})
	if bg.await(MsgSubscribed).Controlling {
		t.Error("a backgrounded reconnect claimed control of the grid")
	}
	if got := s.live.Controller(); got != "" {
		t.Errorf("controller = %q after background subscribe, want unowned", got)
	}
}

// gatedResolver holds every Resolve until gate closes, so a test can put
// messages between a subscribe and its registration.
type gatedResolver struct {
	Resolver
	started chan struct{}
	gate    chan struct{}
}

func (r gatedResolver) Resolve(ctx context.Context, id string) (string, int, int, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	select {
	case <-r.gate:
	case <-ctx.Done():
		return "", 0, 0, ctx.Err()
	}
	return r.Resolver.Resolve(ctx, id)
}

// A resize sent while the subscribe is still attaching is applied when it
// registers, not dropped. See pendingSub.sizes.
func TestAResizeDuringASlowSubscribeIsApplied(t *testing.T) {
	s := serveSession(t, "vp_ws_pending_resize", "true")
	gate := gatedResolver{Resolver: s.handler.Resolve, started: make(chan struct{}, 1), gate: make(chan struct{})}
	s.handler.Resolve = gate

	v := dialViewer(t, s, "phone")
	v.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24})
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscribe never reached Resolve")
	}
	// The keyboard opens while the attach is still running.
	v.send(map[string]any{"t": MsgResize, "sessionId": "s1", "cols": 100, "rows": 30})
	// readLoop handles messages in order: the pong means the resize was read.
	v.send(map[string]any{"t": MsgPing})
	v.await(MsgPong)
	close(gate.gate)

	got := v.await(MsgSubscribed)
	if !got.Controlling {
		t.Fatal("the only viewer was not given the grid; nothing below means anything")
	}
	if got.Cols != 100 || got.Rows != 30 {
		t.Errorf("subscribed with %dx%d, want the 100x30 sent while it was attaching", got.Cols, got.Rows)
	}
	if cols, rows := s.live.Size(); cols != 100 || rows != 30 {
		t.Errorf("grid = %dx%d, want 100x30", cols, rows)
	}
}
