package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
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
		Cfg:      config.Config{DataDir: dir, Addr: ":0", TmuxSocket: socket, StaticDir: dir},
		DB:       db,
		Tmux:     tm,
		Manager:  mgr,
		Hub:      ws.NewHub(),
		Detector: session.NewDetector(),
		Sampler:  &sysmon.Sampler{DiskPath: dir},
		Auth:     &Auth{Throttle: auth.NewThrottle(), SetupToken: "test-setup-token"},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
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

	// A session that printed once and then went quiet is finished.
	quiet := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sh","-c","echo started; exec sleep 60"]}`)
	if _, err := srv.Manager.Attach(ctx, quiet.ID, quiet.TmuxName, 80, 24); err != nil {
		t.Fatalf("Attach quiet: %v", err)
	}
	waitState(quiet.ID, session.StateDone, 12*time.Second)
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
		`{"projectId":"`+project.ID+`","parentSessionId":"`+parent.ID+`","command":[]}`)
	if child.CWD != sub {
		t.Errorf("scratch terminal cwd = %q, want the parent's %q", child.CWD, sub)
	}
	if child.ParentID == nil || *child.ParentID != parent.ID {
		t.Errorf("parent = %v, want %q", child.ParentID, parent.ID)
	}
}

func TestScratchTerminalsCannotNest(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"nest"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	child := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","parentSessionId":"`+parent.ID+`","command":["sleep","60"]}`)

	// A tab strip of tab strips is not a thing anyone asked for, and allowing
	// it would make "the terminals belonging to this session" ambiguous.
	res, err := ts.Client().Post(ts.URL+"/api/sessions", "application/json",
		strings.NewReader(`{"projectId":"`+project.ID+`","parentSessionId":"`+child.ID+`","command":[]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

func TestDeletingASessionTakesItsTerminalsWithIt(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"cascade"}`)
	parent := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	child := postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","parentSessionId":"`+parent.ID+`","command":["sleep","60"]}`)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+parent.ID, nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}

	// The row cascades away in SQLite, but the tmux session does not. Leaving
	// it behind is a process nothing in the UI can ever reach again.
	if ok, _ := srv.Tmux.Has(ctx, child.TmuxName); ok {
		t.Error("the scratch terminal's tmux session survived its parent")
	}
	if _, err := srv.DB.GetSession(ctx, child.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("child row survived: %v", err)
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
			`{"projectId":"`+project.ID+`","parentSessionId":"`+parent.ID+`","command":["sleep","60"]}`)
		want = append(want, c.ID)
	}

	// These are tabs in a strip. A tab strip that reorders itself while you
	// are using it is hostile, so they order by creation and not by state.
	kids, err := srv.DB.ListChildSessions(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildSessions: %v", err)
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
			`{"projectId":"`+project.ID+`","parentSessionId":"`+parent.ID+`","command":[]}`))
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
