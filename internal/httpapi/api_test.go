package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/ws"
)

// newTestServer wires a complete panel against a throwaway tmux socket and
// database, and returns it behind a real HTTP listener.
func newTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()

	socket := "vibepanel-api-" + strconv.Itoa(os.Getpid()) + "-" +
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	tm := tmux.New(socket, dir)
	if err := tm.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	t.Cleanup(func() {
		_ = tm.KillServer(context.Background())
		// kill-server leaves the socket file behind.
		_ = os.Remove(tm.SocketPath())
	})

	db, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mgr := session.NewManager(tm, 256<<10)
	t.Cleanup(mgr.DetachAll)

	srv := &Server{
		Cfg:     config.Config{DataDir: dir, Addr: ":0", TmuxSocket: socket, StaticDir: dir},
		DB:      db,
		Tmux:    tm,
		Manager: mgr,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, srv
}

func postJSON[T any](t *testing.T, ts *httptest.Server, path string, body string) T {
	t.Helper()
	res, err := ts.Client().Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("POST %s: %s: %s", path, res.Status, b)
	}
	var out T
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

func TestHealth(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := ts.Client().Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("health = %v", body)
	}
	if body["tmuxVersion"] == "" {
		t.Error("health did not report a tmux version")
	}
}

func TestUnknownAPIPathIsJSONNotHTML(t *testing.T) {
	// The SPA catch-all is mounted last. If it ever shadowed /api, a typo in a
	// fetch would return an HTML document with status 200 and the client would
	// fail while parsing rather than at the request.
	ts, _ := newTestServer(t)
	res, err := ts.Client().Get(ts.URL + "/api/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want JSON", ct)
	}
}

func TestCreateProjectRejectsMissingDirectory(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := ts.Client().Post(ts.URL+"/api/projects", "application/json",
		strings.NewReader(`{"path":"/definitely/not/here"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

// TestTerminalRoundTrip is the end-to-end proof for this milestone: a browser
// subscribes over the WebSocket, types a command, and sees its output.
func TestTerminalRoundTrip(t *testing.T) {
	ts, _ := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","PS1=; export PS1; exec sh"]}`)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	send(ws.ClientMessage{Type: ws.MsgSubscribe, SessionID: sess.ID, Cols: 80, Rows: 24})

	// Wait for the subscription to be confirmed and note the stream reference.
	var ref uint32
	deadline := time.Now().Add(10 * time.Second)
	for ref == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never received a subscribed confirmation")
		}
		typ, data, rerr := c.Read(ctx)
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg ws.ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		switch msg.Type {
		case ws.MsgSubscribed:
			ref = msg.Ref
		case ws.MsgError:
			t.Fatalf("server error: %s", msg.Message)
		}
	}

	if err := c.Write(ctx, websocket.MessageBinary,
		ws.EncodeData(ref, []byte("echo ROUND_TRIP_OK\n"))); err != nil {
		t.Fatalf("write input: %v", err)
	}

	var seen strings.Builder
	deadline = time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("never saw the command output; got %q", seen.String())
		}
		typ, data, rerr := c.Read(ctx)
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		gotRef, payload, derr := ws.DecodeData(data)
		if derr != nil || gotRef != ref {
			continue
		}
		seen.Write(payload)
		// The echoed command itself also contains the marker, so require the
		// output line: two occurrences means the shell actually ran it.
		if strings.Count(seen.String(), "ROUND_TRIP_OK") >= 2 {
			return
		}
	}
}

func TestSessionSurvivesServerRestart(t *testing.T) {
	// The property the whole architecture exists for, asserted at the HTTP
	// layer: tearing the server down and building a new one against the same
	// tmux socket must leave the session usable.
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	// Simulate a restart: detach everything, as shutdown does.
	srv.Manager.DetachAll()

	ok, err := srv.Tmux.Has(ctx, sess.TmuxName)
	if err != nil || !ok {
		t.Fatalf("tmux session gone after detach: ok=%v err=%v", ok, err)
	}

	// Reconcile is what a fresh process runs at startup.
	if err := srv.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	rec, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec.Command != "sleep" && rec.Command != "tmux" {
		t.Errorf("command after reconcile = %q, want the live command", rec.Command)
	}
}

func TestDeleteSessionKillsTmux(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+sess.ID, nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}
	// A row removed while its tmux session lives on would leave a process
	// nothing in the UI can ever reach again.
	if ok, _ := srv.Tmux.Has(ctx, sess.TmuxName); ok {
		t.Error("tmux session survived the delete")
	}
}

func TestRenamingASessionStopsAutomaticTitles(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["htop"]}`)

	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+sess.ID,
		strings.NewReader(`{"title":"my important task"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	res.Body.Close()

	// The poller keeps deriving names from tmux. A rename must survive it,
	// otherwise the tab the user named silently reverts to "htop".
	for i := 0; i < 3; i++ {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	rec, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec.Title != "my important task" {
		t.Errorf("title = %q, want the manual name to survive polling", rec.Title)
	}
}

func TestPollDerivesTitleFromCommand(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["htop"]}`)

	// pane_title defaults to the hostname, which is identical for every session
	// on the box. The running command is the useful name.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		rec, err := srv.DB.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if rec.Title == "htop" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("title = %q, want %q", rec.Title, "htop")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestReplayIsTaggedSeparately pins the fix for a bug only a real browser
// found: the replay buffer contains the terminal capability queries the
// application sent, and a freshly created xterm answers them as it parses.
// That answer goes to the shell, which types it at the prompt — so every page
// reload was injecting something like "[?1;2c" into the running session.
//
// The client suppresses its responses while parsing replay, which it can only
// do if the frame is distinguishable from live output.
func TestReplayIsTaggedSeparately(t *testing.T) {
	ts, _ := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","echo REPLAY_TAG_MARKER; exec sh"]}`)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	// First viewer: fills the ring buffer, then leaves.
	first, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	b, _ := json.Marshal(ws.ClientMessage{Type: ws.MsgSubscribe, SessionID: sess.ID, Cols: 80, Rows: 24})
	if err := first.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("first viewer never saw the marker")
		}
		typ, data, rerr := first.Read(ctx)
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		if typ == websocket.MessageBinary {
			_, payload, derr := ws.DecodeData(data)
			if derr == nil && strings.Contains(string(payload), "REPLAY_TAG_MARKER") {
				break
			}
		}
	}
	_ = first.CloseNow()

	// Second viewer: its scrollback must arrive tagged as replay.
	second, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer second.CloseNow()
	if err := second.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("subscribe second: %v", err)
	}

	deadline = time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("second viewer never received its scrollback")
		}
		typ, data, rerr := second.Read(ctx)
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		if typ != websocket.MessageBinary {
			continue
		}
		if !strings.Contains(string(data), "REPLAY_TAG_MARKER") {
			continue
		}
		if data[0] != ws.FrameReplay {
			t.Fatalf("scrollback arrived as frame type 0x%02x, want FrameReplay (0x%02x); "+
				"the client cannot suppress its terminal responses without this",
				data[0], ws.FrameReplay)
		}
		return
	}
}
