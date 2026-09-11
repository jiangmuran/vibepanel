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
	url  string
	live *session.Live
}

// serveSession starts a tmux session on a throwaway socket, lets script finish
// writing, attaches it and serves it over a WebSocket.
func serveSession(t *testing.T, name, script string) served {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx := context.Background()
	socket := "vibepanel-ws-" + strconv.Itoa(os.Getpid()) + "-" + t.Name()
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
	srv := httptest.NewServer(&Handler{Manager: m, Resolve: oneSession{name}})
	t.Cleanup(srv.Close)
	return served{url: "ws" + strings.TrimPrefix(srv.URL, "http"), live: live}
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
	v.await(MsgSubscribed)

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
