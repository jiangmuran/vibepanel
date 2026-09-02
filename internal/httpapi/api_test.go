package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/usage"
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
	// Point the suite at another tmux without editing anything:
	//	TEST_TMUX_BIN=/path/to/tmux go test ./...
	if bin := os.Getenv("TEST_TMUX_BIN"); bin != "" {
		tm.Bin = bin
	}
	if err := tm.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}
	t.Cleanup(func() {
		_ = tm.KillServer(context.Background())
		// kill-server leaves the socket file behind.
		_ = os.Remove(tm.SocketPath())
	})

	// cfg.DBPath(), not a name of its own. The test server's Cfg has DataDir set
	// to the same directory, and anything that reads the database *through the
	// config* -- the settings page's size, for one -- was looking at a path with
	// no file at it and reporting zero without complaining.
	db, err := store.Open(ctx, config.Config{DataDir: dir}.DBPath())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mgr := session.NewManager(tm, 256<<10)
	t.Cleanup(mgr.DetachAll)

	srv := &Server{
		Cfg:      config.Config{DataDir: dir, Addr: ":0", TmuxSocket: socket, StaticDir: dir},
		DB:       db,
		Tmux:     tm,
		Manager:  mgr,
		Hub:      ws.NewHub(),
		Detector: session.NewDetector(),
		Sampler:  &sysmon.Sampler{DiskPath: dir},
		Auth:     &Auth{Throttle: auth.NewThrottle(), SetupToken: "test-setup-token"},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		// Pointed at empty directories under the test's own tree, never at the
		// running user's home. Without this every test that touches the token
		// endpoints would start a background walk of whoever's machine the
		// suite is on -- gigabytes of somebody's private transcripts, read by
		// a unit test, for no reason.
		Tokens: &usage.Ingester{
			Scanner: &usage.Scanner{
				ClaudeRoot: filepath.Join(dir, "claude", "projects"),
				CodexRoot:  filepath.Join(dir, "codex", "sessions"),
			},
			DB: db,
		},
	}
	mgr.OnSignals = srv.HandleSignals
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	// Every endpoint that matters requires a session, so the tests sign in
	// once through the real setup flow rather than reaching past it.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	ts.Client().Jar = jar
	res, err := ts.Client().Post(ts.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"token":"test-setup-token","username":"tester","password":"a sufficiently long password"}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("setup: %s: %s", res.Status, b)
	}
	return ts, srv
}

// wsDialOptions carries the test session cookie onto WebSocket dials, which do
// not go through the http.Client's jar.
func wsDialOptions(t *testing.T, ts *httptest.Server) *websocket.DialOptions {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	header := http.Header{}
	for _, c := range ts.Client().Jar.Cookies(u) {
		header.Add("Cookie", c.Name+"="+c.Value)
	}
	return &websocket.DialOptions{HTTPHeader: header}
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
	c, _, err := websocket.Dial(ctx, wsURL, wsDialOptions(t, ts))
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
	// Polled, because pane_current_command is eventually consistent and says
	// so: Info.Command's own comment is "never cache the first reading as a
	// session's identity -- poll for it".
	//
	// There are two transient answers now, not one. "tmux" is the pane before
	// its fork has exec'd at all; "bash" is the login shell a launched command
	// runs through, between its start and its exec. That shell is what gives a
	// session the environment the person has -- without it, an agent installed
	// anywhere but /usr/bin dies with status 127 -- and tmux offers no way to
	// set a pane's PATH without one.
	//
	// This read once and accepted only the first transient, so the wrapper
	// turned it red on CI while passing locally. What it should assert is the
	// property that matters: the pane settles on the command that was asked
	// for.
	var rec store.Session
	for i := 0; i < 50; i++ {
		var gerr error
		rec, gerr = srv.DB.GetSession(ctx, sess.ID)
		if gerr != nil {
			t.Fatalf("GetSession: %v", gerr)
		}
		if rec.Command == "sleep" {
			break
		}
		time.Sleep(100 * time.Millisecond)
		if rerr := srv.Reconcile(ctx); rerr != nil {
			t.Fatalf("Reconcile: %v", rerr)
		}
	}
	if rec.Command != "sleep" {
		t.Errorf("command after reconcile settled on %q, want %q", rec.Command, "sleep")
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
	first, _, err := websocket.Dial(ctx, wsURL, wsDialOptions(t, ts))
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
	second, _, err := websocket.Dial(ctx, wsURL, wsDialOptions(t, ts))
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

// TestStateIsPushedToEveryViewer covers what replaced polling: a change made by
// one client reaches every other one without anybody asking.
func TestStateIsPushedToEveryViewer(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	viewers := make([]*websocket.Conn, 2)
	for i := range viewers {
		c, _, err := websocket.Dial(ctx, wsURL, wsDialOptions(t, ts))
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		defer c.CloseNow()
		viewers[i] = c
	}

	// The dial returns before the server-side handler has necessarily
	// registered the connection with the hub, and a change published in that
	// window reaches nobody.
	deadline := time.Now().Add(5 * time.Second)
	for srv.Hub.Connections() < len(viewers) {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d connections registered", srv.Hub.Connections(), len(viewers))
		}
		time.Sleep(20 * time.Millisecond)
	}

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"pushed"}`)

	for i, c := range viewers {
		var got struct {
			Type     string          `json:"t"`
			Projects []store.Project `json:"projects"`
		}
		found := false
		readDeadline := time.Now().Add(10 * time.Second)
		for !found {
			if time.Now().After(readDeadline) {
				t.Fatalf("viewer %d never received a state push", i)
			}
			_, data, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("viewer %d read: %v", i, err)
			}
			if err := json.Unmarshal(data, &got); err != nil {
				continue // binary frame or another message shape
			}
			if got.Type != ws.MsgState {
				continue
			}
			for _, p := range got.Projects {
				if p.ID == project.ID {
					found = true
				}
			}
		}
	}
}

// TestPollDoesNotStampLastOutput pins a semantic fix: last_output_at means
// "when this session last produced output", not "when we last looked". A
// poller that stamps it every tick breaks activity ordering and makes it
// impossible to tell that a session has gone quiet.
func TestPollDoesNotStampLastOutput(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"quiet"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	before, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
	}
	after, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if after.LastOutputAt != before.LastOutputAt {
		t.Errorf("polling moved last_output_at from %d to %d on a silent session",
			before.LastOutputAt, after.LastOutputAt)
	}
}

func TestProjectOrdering(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	// Three projects, created oldest first. Automatic ordering is by recent
	// activity, so creation order is the starting point.
	var ids []string
	for _, name := range []string{"alpha", "beta", "gamma"} {
		p := postJSON[store.Project](t, ts, "/api/projects",
			`{"path":"`+t.TempDir()+`","name":"`+name+`"}`)
		ids = append(ids, p.ID)
		time.Sleep(1100 * time.Millisecond) // last_active_at has second resolution
	}

	order := func() []string {
		ps, err := srv.DB.ListProjects(ctx)
		if err != nil {
			t.Fatalf("ListProjects: %v", err)
		}
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.Name
		}
		return out
	}

	if got := order(); !slices.Equal(got, []string{"gamma", "beta", "alpha"}) {
		t.Fatalf("automatic order = %v, want most recently active first", got)
	}

	// Drag alpha to the top.
	body := `{"ids":["` + ids[0] + `","` + ids[1] + `","` + ids[2] + `"]}`
	res, err := ts.Client().Post(ts.URL+"/api/projects/reorder", "application/json",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("reorder status = %d, want 204", res.StatusCode)
	}
	if got := order(); !slices.Equal(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("manual order = %v, want the order that was sent", got)
	}

	// Activity must not disturb an explicit order — that is the whole point of
	// setting one.
	if err := srv.DB.TouchProject(ctx, ids[2]); err != nil {
		t.Fatalf("TouchProject: %v", err)
	}
	if got := order(); !slices.Equal(got, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("activity reshuffled a manual order: %v", got)
	}

	manual, err := srv.DB.ProjectOrderIsManual(ctx)
	if err != nil || !manual {
		t.Errorf("ProjectOrderIsManual = %v, %v; want true", manual, err)
	}

	// Back to automatic.
	res, err = ts.Client().Post(ts.URL+"/api/projects/reorder", "application/json",
		strings.NewReader(`{"auto":true}`))
	if err != nil {
		t.Fatalf("auto: %v", err)
	}
	res.Body.Close()
	if got := order(); !slices.Equal(got, []string{"gamma", "beta", "alpha"}) {
		t.Errorf("order after returning to automatic = %v, want activity order", got)
	}
	if manual, _ := srv.DB.ProjectOrderIsManual(ctx); manual {
		t.Error("ProjectOrderIsManual is still true after clearing")
	}
}

func TestReorderRejectsUnknownProject(t *testing.T) {
	ts, _ := newTestServer(t)
	p := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"only"}`)

	// A client working from a stale list must fail loudly rather than have the
	// server quietly write positions for the ids it recognises and drop the
	// rest, which would leave the sidebar in an order nobody chose.
	res, err := ts.Client().Post(ts.URL+"/api/projects/reorder", "application/json",
		strings.NewReader(`{"ids":["`+p.ID+`","does-not-exist"]}`))
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestHookRequiresTheToken(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"hooked"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	body := `{"sessionId":"` + sess.ID + `","state":"waiting"}`
	post := func(auth string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	// This endpoint has no session cookie behind it — an agent hook runs
	// outside the browser — so the token is the only thing standing between a
	// local process and the ability to rewrite session state.
	if code := post(""); code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", code)
	}
	if code := post("Bearer wrong"); code != http.StatusUnauthorized {
		t.Errorf("bad token: status = %d, want 401", code)
	}

	token, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatalf("HookToken: %v", err)
	}
	if code := post("Bearer " + token); code != http.StatusNoContent {
		t.Fatalf("good token: status = %d, want 204", code)
	}

	rec, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec.State != session.StateWaiting || rec.StateSource != session.SourceHook {
		t.Errorf("state = %q from %q, want %q from %q",
			rec.State, rec.StateSource, session.StateWaiting, session.SourceHook)
	}
}

func TestHookRejectsGarbage(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	token, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatalf("HookToken: %v", err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"hooked"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	// The body is written by whatever the user put in their agent config.
	cases := []struct {
		name, body string
		want       int
	}{
		{"unknown state", `{"sessionId":"` + sess.ID + `","state":"banana"}`, http.StatusBadRequest},
		{"unknown session", `{"sessionId":"nope","state":"done"}`, http.StatusNotFound},
		{"empty body", `{}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

func TestSessionEnvironmentCarriesTheHookVariables(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"env"}`)
	// The hook script reads these out of its environment and no-ops when they
	// are absent. If they stop being injected, hooks silently stop reporting
	// and everything falls back to the heuristic with no error anywhere.
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","printf '%s|%s|%s' \"$VIBEPANEL_SESSION_ID\" \"$VIBEPANEL_TOKEN\" \"$VIBEPANEL_URL\"; sleep 60"]}`)

	token, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatalf("HookToken: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, cerr := srv.Tmux.Capture(ctx, sess.TmuxName)
		if cerr != nil {
			t.Fatalf("Capture: %v", cerr)
		}
		if strings.Contains(out, sess.ID+"|"+token+"|") {
			if !strings.Contains(out, "127.0.0.1") {
				t.Errorf("VIBEPANEL_URL is not loopback: %q", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook variables never reached the pane; capture was %q", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestPollerTracksStateFromOutput(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"states"}`)

	// htop rather than a shell loop: when a shell is running something, the
	// pane's foreground process is that something, so `pane_current_command`
	// reading "sh" genuinely means the shell is sitting at its prompt. A shell
	// loop alternates between "sh" and "sleep" and would be testing the wrong
	// thing.
	busy := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["htop","-d","2"]}`)
	if _, err := srv.Manager.Attach(ctx, busy.ID, busy.TmuxName, 80, 24); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	waitState := func(id string, want session.State, within time.Duration) {
		t.Helper()
		deadline := time.Now().Add(within)
		var last session.State
		for {
			if err := srv.pollOnce(ctx); err != nil {
				t.Fatalf("pollOnce: %v", err)
			}
			rec, err := srv.DB.GetSession(ctx, id)
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			last = rec.State
			if last == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("state = %q, want %q", last, want)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// A TUI redrawing is what "working" looks like from outside.
	waitState(busy.ID, session.StateWorking, 10*time.Second)

	// A session that printed once and is back at a shell is finished.
	//
	// This used to run `exec sleep 60` and expect done, on the old rule that
	// silence meant finished. It passed by catching a transient: for the first
	// moment the pane's command is `sh`, and only after the exec does it become
	// `sleep` — so whether it saw "done" depended on when the poller happened
	// to look. Under the rule that replaced it, what decides is what is
	// running, and `exec sh` is the thing that means nothing is.
	quiet := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","echo started; exec sh"]}`)
	if _, err := srv.Manager.Attach(ctx, quiet.ID, quiet.TmuxName, 80, 24); err != nil {
		t.Fatalf("Attach quiet: %v", err)
	}
	waitState(quiet.ID, session.StateDone, 12*time.Second)

	// And the other half of that rule, which is the part with teeth: a process
	// that is still there has not finished, however long it stays quiet. This
	// is what used to be announced as done — a green check against a session
	// mid-task, which is the panel giving a confident wrong answer to the only
	// question it exists to answer.
	thinking := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	if _, err := srv.Manager.Attach(ctx, thinking.ID, thinking.TmuxName, 80, 24); err != nil {
		t.Fatalf("Attach thinking: %v", err)
	}
	waitState(thinking.ID, session.StateWorking, 12*time.Second)
	// Long enough that any activity window has expired several times over.
	time.Sleep(3 * time.Second)
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	rec, err := srv.DB.GetSession(ctx, thinking.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != session.StateWorking {
		t.Errorf("a silent `sleep` reads as %q; the process is still running and nothing has "+
			"finished", rec.State)
	}
}

func TestBellMarksASessionWaiting(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"bell"}`)

	// This is the state the whole panel exists to surface: an agent that
	// stopped and wants a human. Without hooks the terminal bell is the only
	// unambiguous signal available.
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","sleep 0.5; printf 'need you\\a'; exec sleep 60"]}`)
	if _, err := srv.Manager.Attach(ctx, sess.ID, sess.TmuxName, 80, 24); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	deadline := time.Now().Add(12 * time.Second)
	var last session.State
	for {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		rec, err := srv.DB.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		last = rec.State
		if last == session.StateWaiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %q, want %q after the pane rang the bell", last, session.StateWaiting)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestManualOverrideSurvivesThePoller(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"manual"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+sess.ID,
		strings.NewReader(`{"state":"waiting"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	res.Body.Close()

	// The poller consults the detector, so an override recorded only in the
	// database is undone on the next tick — which makes the control useless.
	for i := 0; i < 4; i++ {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	rec, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec.State != session.StateWaiting || rec.StateSource != session.SourceManual {
		t.Errorf("state = %q from %q, want the override to survive polling",
			rec.State, rec.StateSource)
	}
}

func TestScratchTerminalInheritsTheParentDirectory(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	root := t.TempDir()
	sub := filepath.Join(root, "worktree")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+root+`","name":"nested"}`)
	// A session that has moved out of the project root, as an agent working in
	// a worktree would have.
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","cd `+sub+`; exec sleep 60"]}`)

	// Let the poller notice where it went.
	deadline := time.Now().Add(6 * time.Second)
	for {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		rec, err := srv.DB.GetSession(ctx, parent.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if rec.CWD == sub {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("parent cwd = %q, want %q", rec.CWD, sub)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Opening a shell next to an agent and landing somewhere else is the kind
	// of small wrongness that makes a panel feel untrustworthy.
	child := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+parent.ID+`","command":[]}`)
	if child.CWD != sub {
		t.Errorf("scratch terminal cwd = %q, want the agent's %q", child.CWD, sub)
	}
	if !child.Scratch {
		t.Error("the new session is not in the strip")
	}

	// The two are separate questions now. A session started *near* another one
	// without asking for the strip is an ordinary session that happens to begin
	// in the right directory, and conflating them is what made the strip
	// change every time the selected agent did.
	near := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","nearSessionId":"`+parent.ID+`","command":[]}`)
	if near.CWD != sub {
		t.Errorf("nearSessionId cwd = %q, want %q", near.CWD, sub)
	}
	if near.Scratch {
		t.Error("nearSessionId alone put a session in the strip")
	}
}

// A shell opened where another shell is, which used to be refused.
//
// Nesting was rejected because "the terminals belonging to this session" was
// ambiguous once a scratch terminal could have its own. Nothing belongs to a
// session any more, so there is no ambiguity and nothing to reject: asking for
// a new shell in the directory the current shell is sitting in is an ordinary
// request, and it was answered with a 400.
func TestAScratchTerminalCanStartWhereAnotherOneIs(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"nest"}`)
	agent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	first := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+agent.ID+`","command":["sleep","60"]}`)

	second := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+first.ID+`","command":[]}`)
	if !second.Scratch {
		t.Error("the second shell is not in the strip")
	}
	if second.CWD != first.CWD {
		t.Errorf("cwd = %q, want the shell it was opened beside: %q", second.CWD, first.CWD)
	}
}

// Closing an agent leaves the project's terminals running.
//
// The inverse of what this asserted, and the inversion is the point. The strip
// held a build and a `git log` for the repository, and deleting the agent you
// happened to open them from took them with it.
func TestDeletingASessionLeavesTheProjectsTerminals(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"cascade"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	child := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+parent.ID+`","command":["sleep","60"]}`)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+parent.ID, nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}

	// Both halves: no cascade in SQLite, and no teardown in tmux. Either one
	// alone would empty the strip.
	if ok, _ := srv.Tmux.Has(ctx, child.TmuxName); !ok {
		t.Error("the terminal's tmux session was killed with an agent it does not belong to")
	}
	if _, err := srv.DB.GetSession(ctx, child.ID); err != nil {
		t.Errorf("the terminal's row went with the agent: %v", err)
	}
}

func TestChildTerminalsKeepTheirOrder(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"order"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	var want []string
	for i := 0; i < 3; i++ {
		c := postJSON[store.Session](t, ts, "/api/sessions",
			`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+parent.ID+`","command":["sleep","60"]}`)
		want = append(want, c.ID)
	}

	// These are tabs in a strip. A tab strip that reorders itself while you
	// are using it is hostile, so they order by creation and not by state.
	kids, err := srv.DB.ListScratchSessions(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListScratchSessions: %v", err)
	}
	got := make([]string, len(kids))
	for i, k := range kids {
		got[i] = k.ID
	}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want creation order %v", got, want)
	}
}

func TestScratchTerminalsAreNotAllNamedAfterTheirDirectory(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+dir+`","name":"naming"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	// A main shell is named after where it is, which distinguishes one in a
	// worktree from one at the repo root. Scratch terminals all live in the
	// same directory as the session above them, so the same rule would give a
	// strip of identical tabs. The UI numbers those instead.
	var kids []store.Session
	for i := 0; i < 2; i++ {
		kids = append(kids, postJSON[store.Session](t, ts, "/api/sessions",
			`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+parent.ID+`","command":[]}`))
	}
	for i := 0; i < 4; i++ {
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	base := filepath.Base(dir)
	for _, k := range kids {
		rec, err := srv.DB.GetSession(ctx, k.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if rec.Title == base {
			t.Errorf("scratch terminal was named after its directory (%q)", rec.Title)
		}
	}
}

func TestNotesRoundTrip(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"noted"}`)

	// A project that has never been written to opens a blank editor rather
	// than an error.
	res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/notes")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	var note store.Note
	if err := json.NewDecoder(res.Body).Decode(&note); err != nil {
		t.Fatalf("decode: %v", err)
	}
	res.Body.Close()
	if note.Content != "" {
		t.Errorf("new project's note = %q, want empty", note.Content)
	}

	put := func(body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPut,
			ts.URL+"/api/projects/"+project.ID+"/notes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		return r
	}

	r := put(`{"content":"# plan\nship it"}`)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", r.StatusCode)
	}

	res, _ = ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/notes")
	json.NewDecoder(res.Body).Decode(&note) //nolint:errcheck
	res.Body.Close()
	if note.Content != "# plan\nship it" {
		t.Errorf("note = %q", note.Content)
	}
	if note.UpdatedAt == 0 {
		t.Error("updatedAt was not set")
	}

	// Bounded so that one save cannot be arbitrarily large.
	r = put(`{"content":"` + strings.Repeat("x", maxNoteBytes+1) + `"}`)
	r.Body.Close()
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized note status = %d, want 413", r.StatusCode)
	}
}

func TestTodos(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"listy"}`)
	base := ts.URL + "/api/projects/" + project.ID + "/todos"

	first := postJSON[store.Todo](t, ts, "/api/projects/"+project.ID+"/todos", `{"text":"write it"}`)
	second := postJSON[store.Todo](t, ts, "/api/projects/"+project.ID+"/todos", `{"text":"test it"}`)

	list := func() []store.Todo {
		res, err := ts.Client().Get(base)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer res.Body.Close()
		var out []store.Todo
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	got := list()
	if len(got) != 2 || got[0].ID != first.ID {
		t.Fatalf("list = %+v, want the order they were added in", got)
	}

	// Ticking something off moves it down but does not remove it: seeing what
	// you just finished is most of the value of ticking it.
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/todos/"+first.ID,
		strings.NewReader(`{"done":true}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	res.Body.Close()

	got = list()
	if len(got) != 2 {
		t.Fatalf("a completed item disappeared: %+v", got)
	}
	if got[0].ID != second.ID || !got[1].Done {
		t.Errorf("order = %+v, want outstanding first", got)
	}

	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/api/todos/"+second.ID, nil)
	res, _ = ts.Client().Do(req)
	res.Body.Close()
	if len(list()) != 1 {
		t.Error("delete did not remove the item")
	}
}

func TestTodoRejectsEmptyText(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"empty"}`)
	// Whitespace is not an item, and an unclickable blank row is worse than a
	// refused request.
	res, err := ts.Client().Post(ts.URL+"/api/projects/"+project.ID+"/todos",
		"application/json", strings.NewReader(`{"text":"   "}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

func TestFileBrowserStaysInsideTheProject(t *testing.T) {
	ts, _ := newTestServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+root+`","name":"browse"}`)

	get := func(path string) (int, string) {
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID +
			"/files?path=" + url.QueryEscape(path))
		if err != nil {
			t.Fatalf("GET %q: %v", path, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if code, body := get("src"); code != http.StatusOK || !strings.Contains(body, "main.go") {
		t.Errorf("listing src = %d %s", code, body)
	}

	// Every one of these arrives from a URL.
	for _, escape := range []string{"..", "../..", "/etc", "../../../../etc/passwd"} {
		code, body := get(escape)
		if code == http.StatusOK && strings.Contains(body, "passwd") {
			t.Errorf("escaped the project with %q: %s", escape, body)
		}
	}
}

func TestSystemEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := ts.Client().Get(ts.URL + "/api/system")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	var sample sysmon.Sample
	if err := json.NewDecoder(res.Body).Decode(&sample); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sample.MemTotal == 0 || sample.Cores == 0 {
		t.Errorf("sample looks empty: %+v", sample)
	}
}

func TestStateGuessedOnlyWhenAnAgentIsRunningUnreported(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	t.Setenv("HOME", t.TempDir()) // never touch the real ~/.claude
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"guessing"}`)

	state := func() bool {
		res, err := ts.Client().Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer res.Body.Close()
		var out struct {
			StateGuessed bool `json:"stateGuessed"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.StateGuessed
	}

	// A shell is not an agent, and nobody needs telling that its state is
	// inferred.
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	if err := srv.DB.UpdateSessionRuntime(ctx, sess.ID, "/tmp", "bash"); err != nil {
		t.Fatal(err)
	}
	if state() {
		t.Error("a plain shell was reported as a guessed agent")
	}

	// An agent with nothing reporting for it is exactly the case worth saying
	// out loud: Claude Code does not ring the bell when it stops for a
	// decision, so the heuristic will never see "waiting".
	if err := srv.DB.UpdateSessionRuntime(ctx, sess.ID, "/tmp", "claude"); err != nil {
		t.Fatal(err)
	}
	if !state() {
		t.Error("an unreported agent session was not flagged")
	}

	// Once anything has reported, the notice has to go away by itself.
	if err := srv.DB.SetSessionState(ctx, sess.ID, session.StateWaiting, session.SourceHook); err != nil {
		t.Fatal(err)
	}
	if state() {
		t.Error("the notice survived a hook report")
	}
}

// A crash and a success are the two things the sidebar most needs to tell
// apart, and until the exit status was carried through they were the same row.
func TestACrashedSessionIsNotReportedAsDone(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"exits"}`)

	// Dies the first time and survives the second, so a working restart is
	// distinguishable from a command that simply crashes again immediately.
	flag := filepath.Join(dir, "flag")
	script := `test -f ` + flag + ` || { touch ` + flag + `; exit 3; }; sleep 60`
	crashed := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"crashed","command":["bash","-c",`+quote(script)+`]}`)
	alive := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"alive","command":["sleep","60"]}`)

	find := func(id string) store.Session {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer res.Body.Close()
		var out struct {
			Sessions []store.Session `json:"sessions"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, s := range out.Sessions {
			if s.ID == id {
				return s
			}
		}
		t.Fatalf("session %s missing from the snapshot", id)
		return store.Session{}
	}
	// The poller decides this, so it has to be waited for rather than read
	// once -- and `settled` is why there are two conditions rather than one.
	//
	// `Exited` and `ExitStatus` come from two tmux fields read by the same
	// poll, and a pane can be seen dead in one poll and carry its status in
	// the next: pane_dead_status is empty for a pane that was killed rather
	// than exited, and empty reads as 0. So "exited, status 0" is both a
	// legitimate clean exit and a reading taken half a beat early, and this
	// test cannot tell them apart from one sample.
	//
	// It read once and failed in CI on a slower machine, reporting `exit
	// status = 0, want 3` for a script whose only job is to exit 3. Fifteen
	// runs here never reproduced it. The deadline is still what fails the
	// test, so a status that never settles is still a failure rather than a
	// hang.
	waitExit := func(id string, want bool, settled func(store.Session) bool) store.Session {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		var got store.Session
		for time.Now().Before(deadline) {
			if err := srv.pollOnce(ctx); err != nil {
				t.Fatalf("poll: %v", err)
			}
			if got = find(id); got.Exited == want && (settled == nil || settled(got)) {
				return got
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("session %s: exited never became %v (state=%q exited=%v status=%d)",
			id, want, got.State, got.Exited, got.ExitStatus)
		return got
	}

	dead := waitExit(crashed.ID, true, func(s store.Session) bool { return s.ExitStatus != 0 })
	if dead.ExitStatus != 3 {
		t.Errorf("exit status = %d, want 3 — without it a crash cannot be told from a clean exit",
			dead.ExitStatus)
	}
	if live := find(alive.ID); live.Exited {
		t.Error("a running session was reported as exited")
	}

	res, err := ts.Client().Post(ts.URL+"/api/sessions/"+crashed.ID+"/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("restart: status %d", res.StatusCode)
	}
	// Cleared immediately by the handler rather than at the next tick, because
	// a button that takes a poll interval to visibly do anything gets pressed
	// again.
	if again := find(crashed.ID); again.Exited {
		t.Error("the session still claimed to be dead right after being restarted")
	}
	if back := waitExit(crashed.ID, false, nil); back.ExitStatus != 0 {
		t.Errorf("exit status = %d after a successful restart, want 0", back.ExitStatus)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Deleting a project kills its sessions; the detector has to be told to forget
// them too, exactly as deleting a single session does.
//
// The leak is small — one tracker per deleted session, for the life of the
// process — but the asymmetry is not: two paths doing almost the same thing,
// one of them doing it incompletely, is what turns into a real bug later.
func TestDeletingAProjectForgetsItsSessions(t *testing.T) {
	ts, srv := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"forgettable"}`)

	before := srv.Detector.Tracked()
	for i := 0; i < 2; i++ {
		sess := postJSON[store.Session](t, ts, "/api/sessions",
			`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
		// Evaluate is what creates a tracker, and the poller calls it for every
		// live session. Do it directly so the test does not race a timer.
		srv.Detector.Evaluate(sess.ID, session.Observation{}, time.Now())
	}
	if got := srv.Detector.Tracked(); got != before+2 {
		t.Fatalf("tracked = %d, want %d after two sessions", got, before+2)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/projects/"+project.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE returned %d", res.StatusCode)
	}

	// Asserting straight after the response would be testing a race, not the
	// cleanup: the poller calls Evaluate for every row it lists, so a poll that
	// lands between the handler's Forget and the delete rebuilds both trackers.
	// That is why Forget alone was never enough. What has to hold is that the
	// history does not survive the next authoritative pass over the session
	// list — which is a thing that can be asked for directly.
	if err := srv.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if got := srv.Detector.Tracked(); got != before {
		t.Errorf("tracked = %d after deleting the project, want %d; trackers were left behind",
			got, before)
	}
}

// Whether hooks are installed is answered from a cache, because the state
// snapshot asks on every broadcast and answering properly means reading and
// parsing a file in the user's home directory.
//
// The cache is only safe if the panel's own install drops it: otherwise the
// "states are being guessed" notice keeps telling somebody to install hooks
// for up to a TTL after they just did, which reads as the button not working.
func TestHookStatusIsCachedButNotStaleAfterInstalling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{Cfg: config.Config{DataDir: t.TempDir()}}

	if s.hooksAreInstalled() {
		t.Fatal("reported installed with no settings file at all")
	}

	script, err := s.scriptPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.InstallClaude(script); err != nil {
		t.Fatal(err)
	}

	// Still the cached answer: nothing has told this Server to look again.
	if s.hooksAreInstalled() {
		t.Error("the cache was bypassed; every state broadcast would read the file")
	}

	s.forgetHookStatus()
	if !s.hooksAreInstalled() {
		t.Error("dropping the cache did not pick up the install")
	}

	// And the other direction, so the notice comes back when hooks are removed.
	if _, err := hooks.UninstallClaude(script); err != nil {
		t.Fatal(err)
	}
	s.forgetHookStatus()
	if s.hooksAreInstalled() {
		t.Error("still reported installed after removal")
	}
}

// A session whose tmux session disappears must stop claiming to be alive.
//
// tmux sessions can go without the panel being involved: `tmux kill-session`
// from a shell, the server being killed, a reboot. The poller used to skip any
// row it could not find and leave it exactly as it was, so a session that had
// been waiting for a human kept showing an orange triangle indefinitely. The
// panel exists to answer "which of these needs me", and a permanent wrong
// answer is worse than no answer at all.
func TestVanishedSessionStopsLookingAlive(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"vanishing"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	// Waiting is the state that matters: it is the one that puts the session at
	// the top of the list and asks for a person.
	if err := srv.DB.SetSessionState(ctx, sess.ID, session.StateWaiting,
		session.SourceManual); err != nil {
		t.Fatal(err)
	}

	// Behind the panel's back, the way a person with a shell would do it.
	if err := srv.Tmux.Kill(ctx, sess.TmuxName); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}

	after, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Exited {
		t.Error("the row still says the session is running")
	}
	if after.ExitStatus != store.ExitStatusVanished {
		t.Errorf("exit status = %d, want ExitStatusVanished (%d); a status that looks like a "+
			"wait status claims we watched it end", after.ExitStatus, store.ExitStatusVanished)
	}
	if after.State == session.StateWaiting {
		t.Error("it still sorts to the top asking for a human")
	}
}

// A client that connects is told the state at once, without waiting for
// something to change.
//
// The frontend has no other source: it renders from the state message and
// nothing else. The message used to be sent only when something changed, so a
// panel opened while every session was quiet rendered an empty page and stayed
// empty — thirty-one bytes of body, no error, nothing to see. Which is the
// case the panel exists for: coming back to find out which agents finished
// while you were away is exactly the moment when nothing is happening.
func TestNewConnectionIsToldTheStateImmediately(t *testing.T) {
	ts, _ := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"quiet"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	// Settle, so that nothing is in flight when the client arrives.
	time.Sleep(2 * time.Second)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	readCtx, cancelRead := context.WithTimeout(ctx, 5*time.Second)
	defer cancelRead()
	for {
		typ, data, rerr := c.Read(readCtx)
		if rerr != nil {
			t.Fatalf("nothing arrived on a quiet panel, so the page would be blank: %v", rerr)
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg struct {
			Type     string            `json:"t"`
			Projects []json.RawMessage `json:"projects"`
			Sessions []json.RawMessage `json:"sessions"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Type != ws.MsgState {
			continue
		}
		if len(msg.Projects) != 1 || len(msg.Sessions) != 1 {
			t.Fatalf("the first snapshot has %d projects and %d sessions, want 1 and 1",
				len(msg.Projects), len(msg.Sessions))
		}
		return
	}
}

// Restarting a session whose tmux session has disappeared has to work.
//
// The restart button is shown on exactly the rows that have exited, and a
// vanished session is one of them — but Respawn needs a session to respawn
// into, so the one button offered on those rows answered 500. A new tmux
// session under the same name keeps the row, its title and everything written
// about it; only the process is new, which is what restart means here.
func TestRestartingAVanishedSessionRecreatesIt(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"restartable"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	if err := srv.Tmux.Kill(ctx, sess.TmuxName); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}

	res, err := ts.Client().Post(ts.URL+"/api/sessions/"+sess.ID+"/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST restart: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("restart returned %d: %s", res.StatusCode, body)
	}

	alive, err := srv.Tmux.Has(ctx, sess.TmuxName)
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Error("no tmux session came back")
	}
	after, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Exited {
		t.Error("the row still says it has exited")
	}
	if after.ExitStatus == store.ExitStatusVanished {
		t.Error("the vanished marker was left behind, so the badge still reads gone")
	}
}

// The API under everything at once.
//
// A Server carries caches that HTTP handlers and the poller both touch: the
// hook token behind a sync.Once, the coalesced state snapshot, and whether the
// reporter script is installed. Each has its own guard and each looks right.
// The two races already found in this project were also in code that looked
// right, and both were found by running it rather than by reading it.
func TestConcurrentAPITraffic(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"busy"}`)

	// A few sessions to act on, so the workers are not all creating.
	var ids []string
	for i := 0; i < 3; i++ {
		s := postJSON[store.Session](t, ts, "/api/sessions",
			`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
		ids = append(ids, s.ID)
	}

	stop := make(chan struct{})
	time.AfterFunc(2*time.Second, func() { close(stop) })
	var wg sync.WaitGroup
	var failures atomic.Int64
	var firstErr atomic.Value

	fail := func(format string, args ...any) {
		failures.Add(1)
		firstErr.CompareAndSwap(nil, fmt.Sprintf(format, args...))
	}

	get := func(path string) {
		res, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			fail("GET %s: %v", path, err)
			return
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		if res.StatusCode >= 500 {
			fail("GET %s returned %d", path, res.StatusCode)
		}
	}

	// Readers of everything the browser polls.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			paths := []string{"/api/state", "/api/settings", "/api/health"}
			for {
				select {
				case <-stop:
					return
				default:
				}
				get(paths[n%len(paths)])
			}
		}(i)
	}

	// Writers: renames and state changes on the same handful of sessions.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := ids[n%len(ids)]
			states := []string{"working", "waiting", "done"}
			for {
				select {
				case <-stop:
					return
				default:
				}
				body := fmt.Sprintf(`{"title":"t%d"}`, n)
				if n%2 == 0 {
					body = fmt.Sprintf(`{"state":"%s"}`, states[n%len(states)])
				}
				req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+id,
					strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				res, err := ts.Client().Do(req)
				if err != nil {
					fail("PATCH: %v", err)
					continue
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode >= 500 {
					fail("PATCH returned %d", res.StatusCode)
				}
			}
		}(i)
	}

	// Hook reports, which arrive from inside sessions and take the token path.
	token, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatalf("HookToken: %v", err)
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				body := `{"sessionId":"` + ids[n%len(ids)] + `","state":"waiting"}`
				req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state",
					strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+token)
				res, herr := ts.Client().Do(req)
				if herr != nil {
					fail("hook: %v", herr)
					continue
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode >= 500 {
					fail("hook returned %d", res.StatusCode)
				}
			}
		}(i)
	}

	// And the poller, which is what runs alongside all of this in production.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if perr := srv.pollOnce(ctx); perr != nil {
				fail("pollOnce: %v", perr)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("API callers did not finish; something is deadlocked")
	}
	if n := failures.Load(); n > 0 {
		t.Errorf("%d requests failed under concurrent load; the first was: %v", n, firstErr.Load())
	}
}

func TestTheNoticeOnlyAsksForSomethingThatCanBeDone(t *testing.T) {
	// This test has been both ways round, and the second one was wrong for a
	// reason the first could not have known.
	//
	// It once cleared as soon as the hook file existed, which stopped
	// explaining at the worst moment: an agent reads its hooks when it starts,
	// so sessions already open keep guessing, and the notice saying so vanished
	// on the click meant to fix it. So it was changed to require an actual
	// report -- true, and it turned the notice into a panel that nags its owner
	// about work he has already done. Measured on his machine: reporter
	// installed, `notify` on line 8 of config.toml above the first table,
	// running the script by hand moved a session to waiting/hook in a second.
	// Codex's notify fires on turn completion and nothing else, so sessions
	// mid-turn had never had anything to send.
	//
	// A banner exists to offer something to press. "This one session has never
	// reported" is a fact about one row, and belongs on that row.
	ts, srv := newTestServer(t)
	ctx := context.Background()
	t.Setenv("HOME", t.TempDir()) // never touch the real ~/.claude
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"guessing"}`)

	read := func() (guessed, installed bool) {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer res.Body.Close()
		var out struct {
			StateGuessed   bool `json:"stateGuessed"`
			HooksInstalled bool `json:"hooksInstalled"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.StateGuessed, out.HooksInstalled
	}

	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	if err := srv.DB.UpdateSessionRuntime(ctx, sess.ID, "/tmp", "claude"); err != nil {
		t.Fatal(err)
	}
	if guessed, installed := read(); !guessed || installed {
		t.Fatalf("before installing: guessed=%v installed=%v, want true/false", guessed, installed)
	}

	res, err := ts.Client().Post(ts.URL+"/api/settings/hooks", "application/json", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("install returned %d", res.StatusCode)
	}

	guessed, installed := read()
	if !installed {
		t.Error("the payload does not say the hooks are installed, so the notice cannot " +
			"offer the right way out")
	}
	// Installed is enough to stop asking.
	//
	// This used to require an actual report, on the reasoning that a session
	// opened before the install keeps guessing until it restarts. True, and it
	// made the panel nag its owner about work he had already done: measured on
	// his own machine, the reporter was installed, `notify` was on line 8 of
	// config.toml above the first table, and running the script by hand moved a
	// session to waiting/hook in one second. Codex's notify fires on turn
	// completion and nothing else, so three sessions mid-turn had never had
	// anything to send -- and every screen told him to go and install hooks.
	//
	// A banner exists to offer something to press. "This one session has never
	// reported" is a fact about one row.
	if guessed {
		t.Error("the notice is still up after the hooks were installed, so it is asking " +
			"for something that has already been done")
	}

	// A report keeps it quiet too, which is the case where the hooks were
	// installed outside the panel.
	if err := srv.DB.SetSessionState(ctx, sess.ID, session.StateWaiting, session.SourceHook); err != nil {
		t.Fatal(err)
	}
	if guessed, _ := read(); guessed {
		t.Error("the notice survived a hook report")
	}
}

func TestEditingATodo(t *testing.T) {
	// A todo list you cannot correct is a todo list you rewrite. Editing was
	// reachable from the UI, handled by the API and stored by SetTodoText, and
	// tested at none of those levels — the store function had no test at all
	// and the harness only ever added and ticked.
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"listy"}`)
	todo := postJSON[store.Todo](t, ts, "/api/projects/"+project.ID+"/todos",
		`{"text":"renew the certificate"}`)

	textOf := func() string {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/projects/" + project.ID + "/todos")
		if err != nil {
			t.Fatalf("GET todos: %v", err)
		}
		defer res.Body.Close()
		var out []store.Todo
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, item := range out {
			if item.ID == todo.ID {
				return item.Text
			}
		}
		t.Fatalf("todo %s is gone", todo.ID)
		return ""
	}

	patch := func(body string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPatch,
			ts.URL+"/api/todos/"+todo.ID, strings.NewReader(body))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("PATCH: %v", err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}

	if code := patch(`{"text":"renew the certificate before it expires"}`); code != http.StatusOK {
		t.Fatalf("editing returned %d", code)
	}
	if got := textOf(); got != "renew the certificate before it expires" {
		t.Errorf("text = %q after an edit", got)
	}

	// Whitespace is not a new name. Blanking a row leaves something nobody can
	// identify or click back into, which is why the inline editor refuses it
	// locally too — this is the second line of the same defence, for a client
	// that does not.
	if code := patch(`{"text":"   "}`); code != http.StatusBadRequest {
		t.Errorf("emptying a todo returned %d, want 400", code)
	}
	if got := textOf(); got != "renew the certificate before it expires" {
		t.Errorf("a refused edit changed the text to %q", got)
	}

	// Done and text travel on the same endpoint and must not interfere.
	if code := patch(`{"done":true}`); code != http.StatusOK {
		t.Fatalf("ticking returned %d", code)
	}
	if got := textOf(); got != "renew the certificate before it expires" {
		t.Errorf("ticking an item rewrote its text to %q", got)
	}
}

func TestASessionIsNotStartedInADirectoryThatIsGone(t *testing.T) {
	// tmux falls back to $HOME when -c names a directory that is not there,
	// and says nothing. So a project whose directory had been removed — a
	// worktree pruned, a mount gone, a rename — produced a session running in
	// the user's home directory, filed in the sidebar under the project it was
	// not in.
	//
	// Measured before the fix: POST returned 201 and pane_current_path was
	// /home/jmr. "Refactor this" starting in somebody's home directory is the
	// wrong kind of surprise from a panel that runs coding agents.
	ts, srv := newTestServer(t)
	ctx := context.Background()

	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+dir+`","name":"worktree"}`)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove the project directory: %v", err)
	}

	res, err := ts.Client().Post(ts.URL+"/api/sessions", "application/json",
		strings.NewReader(`{"projectId":"`+project.ID+`","command":["sleep","60"]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("creating a session in a missing directory returned %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), dir) {
		t.Errorf("the refusal does not name the directory: %s", body)
	}
	rows, err := srv.DB.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("%d session rows were written for a session that was refused", len(rows))
	}
}

func TestAScratchTerminalFallsBackToTheProjectRoot(t *testing.T) {
	// The other half, and the reason the check is not a blanket refusal. A
	// scratch terminal opens in its parent's working directory, which the agent
	// may have cd'd into and which may since have gone. The project root is
	// still somewhere useful to be, and is not a lie about where you are.
	ts, srv := newTestServer(t)
	ctx := context.Background()

	root := t.TempDir()
	gone := filepath.Join(root, "build")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+root+`","name":"proj"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	if err := srv.DB.UpdateSessionRuntime(ctx, parent.ID, gone, "bash"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	child := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+parent.ID+`","command":["sleep","60"]}`)
	if child.CWD != root {
		t.Errorf("a scratch terminal whose parent's directory had gone opened in %q, want the "+
			"project root %q", child.CWD, root)
	}
}

func TestDeletingAProjectDoesNotWaitTwoSecondsPerSession(t *testing.T) {
	// Every session is attached from the moment it is created, so deleting a
	// project tears down that many attachments. Detaching one costs the two
	// seconds it takes for the timer to kill a tmux client that will not exit
	// on its own; killing the tmux session first makes the client exit by
	// itself and the detach returns in under a millisecond.
	//
	// Measured through the API: one session 2015ms → 14ms, a project with five
	// 10029ms → 25ms. The order in tearDownSession is the whole difference, and
	// nothing else would notice it being swapped back.
	//
	// The bound is loose on purpose. What is pinned is that this does not scale
	// at seconds per session; a busy machine should not make it flake.
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	const n = 4
	for i := 0; i < n; i++ {
		postJSON[store.Session](t, ts, "/api/sessions",
			`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/projects/"+project.ID, nil)
	start := time.Now()
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	res.Body.Close()
	took := time.Since(start)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}
	if took > 3*time.Second {
		t.Errorf("deleting a project with %d sessions took %v; that is the "+
			"per-session detach wait back again", n, took.Round(time.Millisecond))
	}
}

func TestRestoreStateReadsWhatWasWrittenDown(t *testing.T) {
	// The detector's evidence lives in memory and the database has the answer
	// it produced. Nothing carried one into the other at startup, so a restart
	// turned every waiting session into a working one — permanently, since
	// nothing was going to ring a second time.
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	if err := srv.DB.SetSessionState(ctx, sess.ID, session.StateWaiting, session.SourceHeuristic); err != nil {
		t.Fatalf("SetSessionState: %v", err)
	}
	// Forget, rather than swapping in a fresh detector: Server.Detector is set
	// once at startup and read from the pump's goroutine without a lock, so
	// reassigning it under a running server is a race — and -race said so,
	// after the test passed on its own without it. What a restart leaves for
	// this session is an empty tracker, which is what Forget produces.
	srv.Detector.Forget(sess.ID)
	if err := srv.RestoreState(ctx); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	st, src := srv.Detector.Evaluate(sess.ID, session.Observation{}, time.Now())
	if st != session.StateWaiting {
		t.Errorf("state after a restart = %q from %q, want %q; the agent is still "+
			"sitting on its question", st, src, session.StateWaiting)
	}
}

func TestAKilledAgentIsNotRecordedAsACleanExit(t *testing.T) {
	// The poller wrote #{pane_dead_status}, which tmux leaves empty for a
	// process that was killed rather than one that returned. So an agent the
	// OOM killer took was stored with status 0 — the number a task that
	// finished its work has — and the sidebar's "something failed here"
	// indicator, which tests exitStatus !== 0, never fired for it.
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","exec sleep 600"]}`)

	var pid int
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		info, err := srv.Tmux.Get(ctx, sess.TmuxName)
		if err == nil && info.PID > 0 && info.Command == "sleep" {
			pid = info.PID
			break
		}
	}
	if pid == 0 {
		t.Fatal("the pane never started its command")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}

	var row store.Session
	for i := 0; i < 40; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := srv.pollOnce(ctx); err != nil {
			t.Fatalf("pollOnce: %v", err)
		}
		got, err := srv.DB.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got.Exited {
			row = got
			break
		}
	}
	if !row.Exited {
		t.Fatal("the poller never noticed the pane had died")
	}
	// Two claims, and only one of them is the panel's.
	//
	// The panel's claim is faithfulness: whatever tmux says about how the pane
	// ended is what the row says. That is asserted on every run.
	//
	// The other claim -- that a SIGKILL reads as 137 -- belongs to tmux, and
	// tmux can lose it. Measured on tmux 3.4, roughly one run in ten: the pane
	// is reported dead the moment its pty closes, before the server has reaped
	// the child, so pane_dead_status and pane_dead_signal are both 0 and stay
	// that way. The killed pid is still in /proc as a zombie at that point,
	// which is how this was identified. There is nothing the panel can compute
	// from a wait status tmux never collected, so this reports the loss rather
	// than blaming the code under it.
	now, gerr := srv.Tmux.Get(ctx, sess.TmuxName)
	if gerr != nil {
		t.Fatalf("Get after the kill: %v", gerr)
	}
	if row.ExitStatus != now.ExitStatus() {
		_, statErr := os.Stat("/proc/" + strconv.Itoa(pid))
		t.Errorf("stored exit status = %d but tmux says %d (dead=%v status=%d signal=%d); "+
			"killed pid %d still present: %v",
			row.ExitStatus, now.ExitStatus(), now.Dead, now.DeadStatus, now.DeadSignal,
			pid, statErr == nil)
	}
	if row.ExitStatus != 128+int(syscall.SIGKILL) {
		if now.DeadStatus == 0 && now.DeadSignal == 0 {
			t.Skipf("tmux %s marked the pane dead without collecting a wait status, "+
				"so 137 was never available to store; the panel stored what tmux had (%d)",
				tmuxVersionFor(t, srv), row.ExitStatus)
		}
		t.Errorf("stored exit status = %d, want %d; a killed agent must not look "+
			"like one that finished", row.ExitStatus, 128+int(syscall.SIGKILL))
	}
}

func TestThePanelSaysWhenItHasStoppedRecording(t *testing.T) {
	// A panel whose database cannot be written answers every request, serves
	// every terminal, and records nothing. Measured with the database's writes
	// capped: the eleventh rename returned `500 store: exec: disk I/O error`,
	// so the person who pressed a button was told — and /api/health went on
	// answering `"ok": true` while the snapshot said nothing at all. The
	// terminals kept working, which is the architecture doing its job and
	// exactly why nothing else looked wrong.
	ts, srv := newTestServer(t)

	health := func() map[string]any {
		res, err := ts.Client().Get(ts.URL + "/api/health")
		if err != nil {
			t.Fatalf("GET health: %v", err)
		}
		defer res.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body
	}

	if body := health(); body["ok"] != true {
		t.Fatalf("a healthy panel says %v", body)
	}

	srv.noteStale(errors.New("store: exec: disk I/O error (778)"))
	// One failure is a blip — a tmux call that lost a race with a delete — and
	// a banner that comes and goes is one people learn to ignore.
	if body := health(); body["ok"] != true {
		t.Errorf("a single failure already says the panel is unhealthy: %v", body)
	}

	// Backdate it past the grace period, which is what a failure that keeps
	// happening looks like.
	srv.staleMu.Lock()
	srv.staleSince = time.Now().Add(-2 * staleGrace)
	srv.staleMu.Unlock()

	body := health()
	if body["ok"] != false {
		t.Errorf("health says %v on a panel that cannot write to its database", body)
	}
	if s, _ := body["stale"].(string); !strings.Contains(s, "disk I/O error") {
		t.Errorf("health does not say why: %v", body)
	}

	state, err := srv.buildState(context.Background())
	if err != nil {
		t.Fatalf("buildState: %v", err)
	}
	if !strings.Contains(state.Stale, "disk I/O error") {
		t.Errorf("the snapshot carries %q; every viewer should be told, not just "+
			"whoever happened to press a button", state.Stale)
	}
}

func TestASuccessfulPollDoesNotEraseAFailureThatIsStillHappening(t *testing.T) {
	// A database capped at its current size still lets the poller rewrite
	// pages it has already allocated while a request needing a new one fails.
	// The first version of this signal cleared on every successful poll, so it
	// never once fired against the failure it was written for.
	_, srv := newTestServer(t)
	srv.noteStale(errors.New("store: exec: disk I/O error (778)"))
	srv.staleMu.Lock()
	srv.staleSince = time.Now().Add(-2 * staleGrace)
	srv.staleMu.Unlock()

	srv.clearStale()
	if srv.stale() == "" {
		t.Error("a poll that happened to succeed erased a failure from a moment ago")
	}

	// Once the writes have genuinely stopped failing, it goes away.
	srv.staleMu.Lock()
	srv.staleLast = time.Now().Add(-2 * staleQuiet)
	srv.staleMu.Unlock()
	srv.clearStale()
	if got := srv.stale(); got != "" {
		t.Errorf("still says %q long after the writes started working again", got)
	}
}

func TestBothWaysOfAskingForTheStateAgree(t *testing.T) {
	// handleState and the WebSocket snapshot listed the same fields
	// separately, so adding one to a viewer meant remembering the other. That
	// is how `stale` reached the socket and not the REST answer.
	ts, srv := newTestServer(t)
	ctx := context.Background()
	postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"test"}`)

	res, err := ts.Client().Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("GET state: %v", err)
	}
	defer res.Body.Close()
	var rest map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&rest); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var pushed map[string]json.RawMessage
	if err := json.Unmarshal(srv.snapshot(ctx), &pushed); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	delete(pushed, "t") // the frame type, which REST has no need of

	for k := range pushed {
		if _, ok := rest[k]; !ok {
			t.Errorf("%q is pushed over the socket and missing from /api/state", k)
		}
	}
	for k := range rest {
		if _, ok := pushed[k]; !ok {
			t.Errorf("%q is in /api/state and never pushed", k)
		}
	}
}

func TestAFailedRequestRecordsThatTheDatabaseIsNotWorking(t *testing.T) {
	// Not by calling noteStale directly: the point is that the path a real
	// failing request takes goes through it. Removing the one line from
	// writeStoreErr passed every other test in this file.
	ts, srv := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	// A database that cannot be written at all. Everything after this fails,
	// which is the situation being described.
	if err := srv.DB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/sessions/"+sess.ID,
		strings.NewReader(`{"title":"renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	res.Body.Close()
	// 503, not 401. The session is very likely fine; the panel cannot look it
	// up. Answering "sign in required" sends somebody to a login form that
	// goes to the same broken database, and the login throttle then locks
	// them out of a panel that was only ever short of disk space.
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 from a database that cannot be read", res.StatusCode)
	}

	srv.staleMu.Lock()
	recorded := !srv.staleSince.IsZero()
	reason := srv.staleReason
	srv.staleMu.Unlock()
	if !recorded {
		t.Error("a request failed against the database and the panel did not write it down; " +
			"the only person who would ever find out is whoever pressed the button")
	}
	if recorded && reason == "" {
		t.Error("recorded a failure with no reason to show")
	}
}

// Killing a session forgets its output-debounce entry as well as its tracker.
//
// outputSeen holds one timestamp per session so that last_output_at is not
// written on every chunk. Nothing removed an entry, so an id that will never
// appear again kept a timestamp until the process exited — the same leak the
// Detector.Forget call beside it was added to close, one field over, missed
// because the two were written at different times.
//
// Small on its own. Pinned because "two paths doing almost the same thing, one
// of them doing it incompletely" is the shape this project keeps paying for,
// and a leak nobody measures is the version of it that never gets found.
func TestKillingASessionForgetsItsOutputDebounce(t *testing.T) {
	ts, srv := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"debounced"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	// Visible is what makes the debounce record anything; a chunk of cursor
	// moves deliberately does not count as output.
	srv.HandleSignals(session.Signals{SessionID: sess.ID, Visible: true, Bytes: 8})

	srv.outMu.Lock()
	_, recorded := srv.outputSeen[sess.ID]
	srv.outMu.Unlock()
	if !recorded {
		t.Fatal("no debounce entry after visible output, so this test would pass on anything")
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+sess.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete session: %d", res.StatusCode)
	}

	srv.outMu.Lock()
	_, left := srv.outputSeen[sess.ID]
	srv.outMu.Unlock()
	if left {
		t.Error("the debounce entry outlived the session it belongs to")
	}
}

// What a database that cannot be READ does to a WebSocket that is already open.
//
// The comment on stillAuthorized asks for this measurement by name, and says
// why it is not an argument: "both directions have been argued convincingly in
// this file's history and the arguments were wrong."
//
// The shape of the question. currentUser has three outcomes — signed in, not
// signed in, and "the database cannot say". An ordinary request gets 503 for
// the third and keeps its session. stillAuthorized discards the error and
// returns the bool, so the third outcome closes the socket, and every viewer
// disconnects at once during a storage fault — which is when somebody most
// wants to look at the panel. A database that cannot be *written* does not
// reach here at all: currentUser only reads, and that case was already
// measured to leave the socket up with a banner.
//
// This pins what actually happens rather than deciding whether it is right.
// Changing it is a real decision with a real cost either way; changing it by
// accident is not.
func TestAnUnreadableDatabaseClosesOpenSockets(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	c, _, err := websocket.Dial(ctx, wsURL, wsDialOptions(t, ts))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	// Prove the socket is alive before breaking anything, or a connection that
	// was never up would look like the behaviour under test.
	if _, _, err := c.Read(ctx); err != nil {
		t.Fatalf("no snapshot on a healthy panel, so this test proves nothing: %v", err)
	}

	// Reads fail from here on. Closing is the bluntest form of "the database
	// cannot say" and is what a disk that has gone away looks like.
	if err := srv.DB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// The revalidation ticker is 5s, so give it two turns plus slack.
	readCtx, readCancel := context.WithTimeout(ctx, 13*time.Second)
	defer readCancel()
	started := time.Now()
	for {
		if _, _, err := c.Read(readCtx); err != nil {
			if readCtx.Err() != nil {
				t.Fatal("the socket stayed open through an unreadable database; " +
					"if that is now deliberate, the comment on stillAuthorized needs " +
					"rewriting, because it says the opposite")
			}
			// Closed. Which timer closed it matters: the state poller runs
			// every 2s and also touches the database, so a close at 2s would
			// mean something else entirely and this test would be reading a
			// coincidence. Revalidation is the only 5s timer on this path.
			if took := time.Since(started); took < 3*time.Second {
				t.Fatalf("the socket closed after %v, too soon to be the 5s "+
					"revalidation — something other than stillAuthorized is "+
					"closing it, and this test is measuring the wrong thing", took)
			}
			return
		}
	}
}

// The audit log is cut back while the panel runs, not only when it starts.
//
// It was trimmed once, in main, and nowhere else. The panel is built to run for
// months — the install instructions turn on lingering precisely so it survives
// a logout — so the only bound on the table was "somebody restarts it".
//
// That matters beyond housekeeping. Cooldown's overflow case lets an event
// through unrecorded-in-memory when a flood is too widely distributed to gate,
// and names this trim as what limits the damage: "the trim on the table is what
// bounds the damage from here." It was written believing this ran. Under a
// sustained flood, restarting the panel is not a bound, and the flood is the
// thing writing the rows.
func TestTheAuditLogIsTrimmedWhileTheServerRuns(t *testing.T) {
	_, srv := newTestServer(t)
	ctx := context.Background()

	const keep = 5
	for i := 0; i < keep*4; i++ {
		if err := srv.DB.Audit(ctx, store.AuditEntry{
			Event: "blocked", IP: fmt.Sprintf("10.0.0.%d", i), At: time.Now().Unix(),
		}); err != nil {
			t.Fatalf("audit: %v", err)
		}
	}
	before, err := srv.DB.RecentAudit(ctx, 1000)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(before) <= keep {
		t.Fatalf("only %d rows before trimming, so this test would pass on a no-op", len(before))
	}

	srv.TrimEvery = 20 * time.Millisecond
	srv.AuditKeep = keep
	pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	go srv.Poll(pollCtx)

	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, err := srv.DB.RecentAudit(ctx, 1000)
		if err != nil {
			t.Fatalf("list audit: %v", err)
		}
		if len(rows) <= keep {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit log still holds %d rows with a cap of %d after three seconds "+
				"of a 20ms trim ticker; nothing is trimming it while the panel runs",
				len(rows), keep)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Renaming, pinning and re-ordering a project.
//
// PATCH /projects/{id} had no test and no browser check: measured at 0.0%
// statement coverage with -coverpkg across the whole module. The checks rename
// a todo and a session; nothing renamed a project. Naming things is the reason
// this panel exists, and the one endpoint that does it for a project was
// running unobserved.
//
// The last case is the one worth having. `clearSortIndex` and `sortIndex` can
// arrive together, and the handler takes the clear — a null sortIndex is
// indistinguishable from an absent one after decoding, which is why the flag
// exists at all. Get that precedence backwards and "sort by activity" writes a
// position instead of removing one, which is how the sidebar previously lost
// the arrangement it was supposed to be able to return to.
func TestPatchingAProject(t *testing.T) {
	ts, _ := newTestServer(t)
	patch := func(id, body string) store.Project {
		t.Helper()
		req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/projects/"+id,
			strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("PATCH %s: %d", body, res.StatusCode)
		}
		var p store.Project
		if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"before"}`)

	if got := patch(project.ID, `{"name":"after"}`); got.Name != "after" {
		t.Errorf("name = %q after renaming, want %q", got.Name, "after")
	}
	if got := patch(project.ID, `{"pinned":true}`); !got.Pinned {
		t.Error("pinned did not stick")
	}
	// A field the request leaves out must not be reset by the ones it carries.
	if got := patch(project.ID, `{"sortIndex":3}`); got.Name != "after" || !got.Pinned {
		t.Errorf("patching sortIndex disturbed the rest: %+v", got)
	} else if got.SortIndex == nil || *got.SortIndex != 3 {
		t.Errorf("sortIndex = %v, want 3", got.SortIndex)
	}
	if got := patch(project.ID, `{"clearSortIndex":true,"sortIndex":9}`); got.SortIndex != nil {
		t.Errorf("sortIndex = %v with clearSortIndex set; the clear must win, or "+
			"returning to automatic ordering writes a position instead of removing one",
			*got.SortIndex)
	}

	// A name longer than the cap is truncated rather than stored whole: the
	// field is one somebody eventually pastes a file into.
	long := strings.Repeat("x", session.MaxTitleRunes*2)
	if got := patch(project.ID, `{"name":"`+long+`"}`); len([]rune(got.Name)) > session.MaxTitleRunes {
		t.Errorf("stored a %d-rune project name; the cap is %d",
			len([]rune(got.Name)), session.MaxTitleRunes)
	}
}

// A delete finishes even if the tab that asked for it is gone.
//
// Both delete paths ran on the request's context, and Go cancels that the
// moment the client disconnects. Both loop over sessions killing them one at a
// time, so a tab closed just after the click left some tmux sessions dead with
// their rows intact -- a batch of GONE from doing nothing wrong. The comment on
// tearDownSession reasoned carefully about which half should fail first and not
// at all about the half being cancelled, while notifyState's writers already
// detached with a background context for exactly this reason.
//
// The handler is called directly with a context that is already cancelled,
// because a real disconnect mid-loop is a race and a race is not a test. That
// is a stricter version of the same fault: if anything in the path still reads
// the request's cancellation, nothing gets deleted at all.
func TestADeleteFinishesEvenIfTheTabIsClosed(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","300"]}`)
	// A scratch terminal in the same project. It is not deleted with the agent
	// -- the strip belongs to the project -- and it is here so the project
	// delete below, which does loop, has more than one turn to be cancelled in
	// the middle of.
	child := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","scratch":true,"nearSessionId":"`+parent.ID+`","command":["sleep","300"]}`)

	// Positive control: both are really there before anything is deleted.
	live, err := srv.Tmux.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := map[string]bool{}
	for _, i := range live {
		names[i.Name] = true
	}
	if !names[parent.TmuxName] || !names[child.TmuxName] {
		t.Fatalf("the sessions were not running before the delete, so this proves nothing: %v", names)
	}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", parent.ID)
	dead, cancel := context.WithCancel(context.WithValue(ctx, chi.RouteCtxKey, rctx))
	cancel()

	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+parent.ID, nil).WithContext(dead)
	srv.handleDeleteSession(httptest.NewRecorder(), req)

	live, err = srv.Tmux.List(ctx)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	still := []string{}
	gone := true
	for _, i := range live {
		if i.Name == parent.TmuxName {
			still = append(still, i.Name)
		}
		if i.Name == child.TmuxName {
			gone = false
		}
	}
	if len(still) != 0 {
		t.Errorf("%v survived a delete whose request context was cancelled; the panel has "+
			"forgotten them and tmux has not, which is a process nothing can reach", still)
	}
	if gone {
		t.Error("the project's scratch terminal was killed with the agent above it")
	}
	if _, err := srv.DB.GetSession(ctx, parent.ID); err == nil {
		t.Error("the row survived the delete, so the panel will show a session whose tmux " +
			"session is gone")
	}

	// The other delete path, which loops over every session in a project and so
	// has more turns to be cancelled in. Same fix, and worth its own turn here
	// because "both" was what the finding said and one of them passing is how
	// half a fix ships.
	proj2 := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"doomed"}`)
	var doomed []store.Session
	for i := 0; i < 3; i++ {
		doomed = append(doomed, postJSON[store.Session](t, ts, "/api/sessions",
			`{"projectId":"`+proj2.ID+`","command":["sleep","300"]}`))
	}

	prctx := chi.NewRouteContext()
	prctx.URLParams.Add("id", proj2.ID)
	pdead, pcancel := context.WithCancel(context.WithValue(ctx, chi.RouteCtxKey, prctx))
	pcancel()
	preq := httptest.NewRequest(http.MethodDelete, "/api/projects/"+proj2.ID, nil).WithContext(pdead)
	srv.handleDeleteProject(httptest.NewRecorder(), preq)

	live, err = srv.Tmux.List(ctx)
	if err != nil {
		t.Fatalf("list after project delete: %v", err)
	}
	running := map[string]bool{}
	for _, i := range live {
		running[i.Name] = true
	}
	var orphans []string
	for _, d := range doomed {
		if running[d.TmuxName] {
			orphans = append(orphans, d.TmuxName)
		}
	}
	if len(orphans) != 0 {
		t.Errorf("%d of %d sessions survived a cancelled project delete: %v",
			len(orphans), len(doomed), orphans)
	}
}

// A rejected hook token leaves a trace.
//
// Three paths an unauthenticated caller can reach fail in a way worth
// recording: the allowlist refusal, a bad setup token, and a bad token on
// /api/hook/state. The first two wrote a row through auditFromOutside; the
// third wrote nothing, so the runbook's diagnosis for "somebody is hammering
// this panel" -- GROUP BY event over audit_log -- could not see a hook probe at
// all. The tell that it was not decided rather than chosen: the other two carry
// a comment explaining the audit choice, and this one explained only its
// constant-time compare.
//
// The token has full entropy, so this is about noticing an attempt rather than
// preventing one.
func TestARejectedHookTokenIsAudited(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	before, err := srv.DB.RecentAudit(ctx, 200)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state",
		strings.NewReader(`{"sessionId":"whatever","state":"waiting"}`))
	req.Header.Set("Authorization", "Bearer not-the-token")
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a bad hook token answered %s, want 401; this test is not exercising the "+
			"refusal it means to", res.Status)
	}

	after, err := srv.DB.RecentAudit(ctx, 200)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	var found bool
	for _, e := range after {
		if e.Event == "hook.rejected" {
			found = true
		}
	}
	if !found {
		t.Errorf("a rejected hook token wrote no audit row (%d before, %d after); a probe of "+
			"this endpoint is invisible to the query the runbook sends people to",
			len(before), len(after))
	}
}

// Restarting a session that is already running must be refused.
//
// Respawn is `respawn-pane -k`, which kills whatever is there, and nothing on
// the server asked whether there was anything to kill: the comment on the
// handler reasons about the tmux session being *gone* and not about it still
// working. The only guard was the frontend, which renders the button only when
// the session has exited.
//
// The reachable case is the one this panel exists for. Two viewers: A restarts
// a dead session, B's tab still holds the snapshot from before and still offers
// the button, and B's click kills the agent A just started. The window is one
// round trip wide, because notifyState follows the restart -- which describes a
// race rather than a safe interval.
//
// The pid is the assertion. A 409 with the process replaced anyway would be a
// worse bug than no 409 at all.
func TestRestartingALiveSessionIsRefused(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"test"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","300"]}`)

	var before tmux.Info
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, err := srv.Tmux.Get(ctx, sess.TmuxName)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if info.PID != 0 && info.Command == "sleep" {
			before = info
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session never started running (%+v), so this test is not looking at "+
				"a live session", info)
		}
		time.Sleep(50 * time.Millisecond)
	}

	res, err := ts.Client().Post(ts.URL+"/api/sessions/"+sess.ID+"/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusConflict {
		t.Errorf("restarting a running session answered %s (%s), want 409; a second viewer with "+
			"a stale snapshot kills the agent the first one just started",
			res.Status, bytes.TrimSpace(body))
	}
	after, err := srv.Tmux.Get(ctx, sess.TmuxName)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.PID != before.PID {
		t.Errorf("the process was replaced anyway: pid %d -> %d. A refusal that still kills is "+
			"worse than no refusal, because the message says nothing happened.",
			before.PID, after.PID)
	}
}

// The reported database size is the whole database.
//
// The panel runs in journal_mode=WAL, so recent writes live in the -wal file
// until a checkpoint, and a checkpoint can be held off by a long-lived read --
// which this panel has, with four pooled connections and a poller reading every
// two seconds. `os.Stat(DBPath())` alone therefore reports well under what is
// on disk, at the moment somebody is reading it to answer "why is this
// growing", and it disagreed with the runbook's own `du -sh` for a reason
// neither screen explained.
//
// The finding this came from paired it with a claim that the poller's
// unconditional row updates are what fill the WAL. That was measured and is
// false -- SQLite elides an update that changes nothing -- so the growth has
// other sources, and the reporting gap stands on its own.
func TestTheReportedDatabaseSizeIncludesTheWAL(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	// Something in the WAL, and no checkpoint: writes that change the row, so
	// SQLite cannot elide them.
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"sized"}`)
	if _, err := srv.DB.SQL().ExecContext(ctx, "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		if err := srv.DB.RenameProject(ctx, project.ID, fmt.Sprintf("sized-%d", i)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	main, err := os.Stat(srv.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(srv.Cfg.DBPath() + "-wal")
	if err != nil {
		t.Fatalf("no -wal file, so this test is not measuring what it means to: %v", err)
	}
	if wal.Size() == 0 {
		t.Fatal("the -wal file is empty; nothing was left outside the main file to be missed")
	}

	var out map[string]any
	res, err := ts.Client().Get(ts.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	reported, _ := out["dbBytes"].(float64)
	if int64(reported) <= main.Size() {
		t.Errorf("the settings page reports %d bytes and the main file alone is %d with %d more "+
			"in the WAL; an operator comparing this against du finds numbers that disagree",
			int64(reported), main.Size(), wal.Size())
	}
}

// An API token is a credential in its own right, and only that.
//
// The session cookie is a browser's: it expires, it is SameSite=Strict, and
// getting one means posting a password and keeping a jar. An agent told to
// "open a session in the billing project and tell me when it stops" should not
// have to do any of that, and should not be handed the password either -- a
// token can be revoked without changing what you type.
func TestAnAPITokenIsACredential(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	made := postJSON[map[string]any](t, ts, "/api/settings/tokens", `{"name":"an agent"}`)
	token, _ := made["token"].(string)
	tokenID, _ := made["id"].(string)
	if token == "" || tokenID == "" {
		t.Fatalf("no token came back: %+v", made)
	}

	// The token, and nothing else, gets in.
	bare := anonymousClient(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := bare.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("a token answered %s on /api/state, want 200", res.Status)
	}

	// A wrong one does not, and the failure is the same one a stranger gets --
	// not a different code that would say "that token exists but".
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+token+"x")
	res, err = bare.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong token answered %s, want 401", res.Status)
	}

	// Stored as a hash. A database that leaks must not hand over live
	// credentials, which is the same rule the session cookie follows.
	var raw int
	if err := srv.DB.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_tokens WHERE CAST(token_hash AS TEXT) = ?`, token).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 0 {
		t.Error("the token itself is in the database; only its hash should be")
	}

	// Revoking one takes effect at once, which is the reason to have them.
	del, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/tokens/"+tokenID, nil)
	del.Header.Set("Authorization", "Bearer "+token)
	res, err = bare.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("revoking answered %s, want 204", res.Status)
	}
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err = bare.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a revoked token still answered %s; revocation that takes effect later is "+
			"not revocation", res.Status)
	}
}

func tmuxVersionFor(t *testing.T, srv *Server) string {
	t.Helper()
	v, err := srv.Tmux.Version(context.Background())
	if err != nil {
		return "(unknown version)"
	}
	return v
}

// One failed read of the hook token may not decide the rest of the process.
//
// The memo was a sync.Once over `s.hookToken, s.tokenError = ...`, which
// remembers the error as well as the value: whichever call happened to be first
// -- a hook report, or the session creation that reads the token into the new
// session's environment -- got to disable state reporting permanently if it
// failed once. Everything downstream of that is silent (report.sh swallows its
// own failures, and the settings page reports hooks as installed because it
// reads the agent's config file), so the only symptom is every session back on
// the heuristic until somebody restarts the panel. Red line 3.
func TestATransientFailureDoesNotDisableTheHookTokenForever(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// A closed database stands in for the transient failures this has to
	// survive: busy, locked, a full disk. What matters is only that the first
	// read fails and the next one would work.
	broken, err := store.Open(ctx, config.Config{DataDir: dir}.DBPath())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	broken.Close()

	srv := &Server{DB: broken, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := srv.HookToken(ctx); err == nil {
		t.Fatal("the first read was supposed to fail; this test is checking nothing")
	}

	db, err := store.Open(ctx, config.Config{DataDir: dir}.DBPath())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	srv.DB = db

	token, err := srv.HookToken(ctx)
	if err != nil || token == "" {
		t.Fatalf("HookToken = %q, %v; one failure disabled hook reporting for the "+
			"life of the process", token, err)
	}
}

// A hook report whose client has already hung up may not decide it either.
//
// report.sh runs `curl --max-time 2`, so any report the panel does not answer
// within two seconds is aborted and net/http cancels the handler's context. That
// is what a restart looks like from the outside: the surviving sessions fire
// hooks while RestoreState and the usage ingest are queued in front of one
// SQLite writer. The read is the panel's own errand, not the caller's, so it
// runs on a context the caller cannot cancel.
func TestAHookReportFromAGoneClientDoesNotPoisonTheToken(t *testing.T) {
	_, srv := newTestServer(t)

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/hook/state",
		strings.NewReader(`{"sessionId":"nope","state":"waiting"}`)).WithContext(gone)
	req.Header.Set("Content-Type", "application/json")
	srv.Routes().ServeHTTP(httptest.NewRecorder(), req)

	token, err := srv.HookToken(context.Background())
	if err != nil || token == "" {
		t.Fatalf("HookToken = %q, %v after an aborted report; every later report "+
			"answers 500 and every new session launches with no token", token, err)
	}
}

// Every agent the hooks package can install counts as "reporting is on".
//
// TestEitherAgentCountsAsHooksInstalled walks Claude and Codex, and was written
// when there were two. opencode was added to hooks.Status afterwards and
// hooksAreInstalled was not updated, so a machine wired up through opencode --
// plugin in place, states arriving from it -- was told across the top of the
// sidebar that its states were guesses and to turn on reporting it already had.
// That is the second time the same line has been wrong about the same thing, so
// this walks the install functions rather than naming the agents.
func TestEveryAgentTheHooksPackageKnowsCountsAsInstalled(t *testing.T) {
	for _, agent := range []struct {
		name               string
		install, uninstall func(script string) error
	}{
		{"claude",
			func(s string) error { _, err := hooks.InstallClaude(s); return err },
			func(s string) error { _, err := hooks.UninstallClaude(s); return err }},
		{"codex",
			func(s string) error { _, err := hooks.InstallCodex(s); return err },
			func(s string) error { _, err := hooks.UninstallCodex(s); return err }},
		{"opencode",
			func(string) error { return hooks.InstallOpencode() },
			func(string) error { return hooks.UninstallOpencode() }},
	} {
		t.Run(agent.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			s := &Server{Cfg: config.Config{DataDir: t.TempDir()}}
			if s.hooksAreInstalled() {
				t.Fatal("reported installed with no configuration at all")
			}
			script, err := s.scriptPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := agent.install(script); err != nil {
				t.Fatalf("install: %v", err)
			}
			s.forgetHookStatus()
			if !s.hooksAreInstalled() {
				t.Errorf("%s is wired up and the panel still says the states are "+
					"guessed, so the sidebar asks for reporting that is already on",
					agent.name)
			}
			if err := agent.uninstall(script); err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			s.forgetHookStatus()
			if s.hooksAreInstalled() {
				t.Errorf("still reported installed after removing %s's hook", agent.name)
			}
		})
	}
}

// A session still inside its login shell is not a finished one.
//
// The panel wraps a command it cannot find on PATH -- `claude` in ~/.local/bin
// under systemd, or any version manager's shim -- in `<login shell> -l -c 'exec
// …'`, so for as long as the profile takes to source, `pane_current_command` is
// the shell. The poller fed that to the detector on its own, and the detector
// has no grace for a young session: shell, no bell, no hook report, and it
// returns done. The first poll after creation lands inside that window on any
// machine whose profile is slower than a tick, and the session shows a green
// check, sorts as finished and fires a "finished" webhook before flipping back
// to working on the next tick and firing again.
func TestASessionStillInsideItsLoginShellIsNotReadAsFinished(t *testing.T) {
	// A login shell that takes its time before exec'ing what it was handed,
	// which is what sourcing nvm or conda looks like. Named bash because that
	// is the answer the detector has to survive.
	//
	// Only the session's own command waits: the tmux server is started through
	// $SHELL as well, and delaying that just makes the test take two minutes.
	dir := t.TempDir()
	sh := filepath.Join(dir, "bash")
	const profile = `#!/bin/sh
while [ "$1" = "-l" ]; do shift; done
[ "$1" = "-c" ] && shift
case "$1" in *claude*) sleep 3 ;; esac
exec /bin/sh -c "$1"
`
	if err := os.WriteFile(sh, []byte(profile), 0o755); err != nil {
		t.Fatalf("write shell: %v", err)
	}
	t.Setenv("SHELL", sh)

	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"wrapped"}`)
	// Deliberately not on PATH, which is what makes tmux.LaunchArgv wrap it.
	// The basename is still the agent that was asked for, and that is the fact
	// the row keeps.
	agent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["`+dir+`/nowhere/claude"]}`)
	// The control: an ordinary shell session, which really is a scratch
	// terminal and must keep reading as done.
	shell := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["bash"]}`)

	if err := srv.pollOnce(ctx); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}

	row, err := srv.DB.GetSession(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !session.IsShellCommand(row.Command) {
		t.Skipf("the pane already reports %q, so this machine has no window to "+
			"catch and the test would pass for the wrong reason", row.Command)
	}
	if row.State == session.StateDone {
		t.Errorf("a session launching %v read as done while its pane was still "+
			"%q -- a green check, a finished sort and a webhook, on a session "+
			"that has not started yet", row.LaunchCommand, row.Command)
	}

	ctrl, err := srv.DB.GetSession(ctx, shell.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ctrl.State != session.StateDone {
		t.Errorf("the control shell session read %q, want %q: the fix must not "+
			"make every shell look busy", ctrl.State, session.StateDone)
	}
}

// The webhook fires on a state an agent reported through its own hook.
//
// fireWebhooks had one call site -- the poller, guarded by "the state is not
// what the row says" -- and the hook handler writes the row and tells the
// detector before the poller ever looks, so that guard was false and nothing
// was sent. The panel notified you about a session it was *guessing* about and
// said nothing about one whose agent had the reporter the settings page asks
// you to install. The webhook is the only outbound notification there is.
func TestAHookReportedWaitingFiresTheWebhook(t *testing.T) {
	var hits atomic.Int64
	dest := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer dest.Close()

	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"hooked"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)

	put, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/webhooks",
		strings.NewReader(`[{"name":"phone","method":"GET","url":"`+dest.URL+
			`/x?s={state}","states":["waiting"],"enabled":true}]`))
	put.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(put)
	if err != nil {
		t.Fatalf("put webhooks: %v", err)
	}
	res.Body.Close()

	token, err := srv.HookToken(ctx)
	if err != nil {
		t.Fatalf("HookToken: %v", err)
	}
	report := func(state string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state",
			strings.NewReader(`{"sessionId":"`+sess.ID+`","state":"`+state+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("hook report: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("hook report: status %d", res.StatusCode)
		}
	}
	// The send is in a goroutine and never waited for, which is what keeps a
	// slow destination out of the panel's own path.
	settle := func() int64 {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && hits.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		return hits.Load()
	}

	report("waiting")
	row, err := srv.DB.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.State != session.StateWaiting || row.StateSource != session.SourceHook {
		t.Fatalf("state = %q from %q, want waiting from hook", row.State, row.StateSource)
	}
	if n := settle(); n != 1 {
		t.Fatalf("webhook hits = %d, want 1: an agent asked for a human and "+
			"nothing left the panel", n)
	}

	// A second report of the same state is not a transition. Claude Code's
	// Notification event fires more than once while somebody is away from their
	// desk, and a phone that buzzes on each is a phone that gets muted.
	report("waiting")
	time.Sleep(150 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Errorf("webhook hits = %d after a repeat of the same state, want 1", n)
	}

	// And the poller must not send it again on the next tick.
	if err := srv.pollOnce(ctx); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := hits.Load(); n != 1 {
		t.Errorf("webhook hits = %d after a poll, want 1", n)
	}
}
