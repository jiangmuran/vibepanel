package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/chat"
	"github.com/jiangmuran/vibepanel/internal/secret"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A minimal adapter the HTTP tests can register: it records what it is sent
// and lets a test feed it a message.
type memAdapter struct {
	mu   sync.Mutex
	sent []chat.Outbound
	sink chat.Sink
	ok   chan struct{}
	once sync.Once
}

func (m *memAdapter) Kind() string { return "mem" }
func (m *memAdapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{Proactive: true, Edit: true, Buttons: true, QuoteRefs: true}
}
func (m *memAdapter) Run(ctx context.Context, sink chat.Sink) error {
	m.mu.Lock()
	m.sink = sink
	m.mu.Unlock()
	// The bridge restarts every channel on every write, with the same
	// adapter instance from the factory above.
	m.once.Do(func() { close(m.ok) })
	<-ctx.Done()
	return ctx.Err()
}
func (m *memAdapter) Send(ctx context.Context, to chat.Peer, o chat.Outbound) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, o)
	return "r1", nil
}
func (m *memAdapter) Edit(ctx context.Context, to chat.Peer, ref string, o chat.Outbound) error {
	return nil
}
func (m *memAdapter) SendImage(ctx context.Context, to chat.Peer, png []byte, caption string) (string, error) {
	return "img", nil
}
func (m *memAdapter) Typing(ctx context.Context, to chat.Peer, on bool) error   { return nil }
func (m *memAdapter) Ack(ctx context.Context, a chat.Action, text string) error { return nil }

var memOnce sync.Once
var memCurrent *memAdapter
var memMu sync.Mutex

func init() {
	memOnce.Do(func() {
		chat.Register(chat.Factory{
			Kind: "mem", Label: "Memory",
			Fields: []chat.Field{{Name: "token", Label: "Token", Secret: true}, {Name: "name", Label: "Name"}},
			New: func(json.RawMessage, chat.Env) (chat.Adapter, error) {
				memMu.Lock()
				defer memMu.Unlock()
				return memCurrent, nil
			},
		})
		chat.Register(chat.Factory{
			Kind: "picky", Label: "Picky",
			Fields: []chat.Field{{Name: "key", Label: "Key"}},
			New: func(raw json.RawMessage, _ chat.Env) (chat.Adapter, error) {
				var v map[string]string
				_ = json.Unmarshal(raw, &v)
				if v["key"] == "" {
					return nil, errors.New("picky: key is required")
				}
				return memCurrent, nil
			},
		})
	})
}

// An empty form for a channel whose adapter needs a value is a 400 that
// names the value. It was a panic in the handler, and a 500.
func TestAChannelFormTheAdapterRefusesIsA400(t *testing.T) {
	ts, srv := newTestServer(t)
	attachChat(t, srv)
	code, body := doJSON(t, ts, http.MethodPut, "/api/chat/channels/picky", `{"enabled":true,"values":{}}`)
	// The adapter's own words, which the page translates, and nothing else.
	if code != http.StatusBadRequest || !strings.Contains(string(body), `"picky: key is required"`) {
		t.Fatalf("empty form: %d %s", code, body)
	}
}

// A name loses what would break a line or reorder the text around it, and a
// refused request changes nothing, the name included.
func TestPeerNamesAreCleanAndARefusedPatchChangesNothing(t *testing.T) {
	ts, srv := newTestServer(t)
	attachChat(t, srv)
	ctx := context.Background()
	_ = srv.DB.PutChatPeer(ctx, store.ChatPeer{Channel: "mem", PeerID: "p", Status: store.PeerPending, Mode: store.ModeNormal})
	if code, _ := doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/p", `{"display":"x","status":"paired"}`); code != http.StatusConflict {
		t.Fatalf("refused patch: %d", code)
	}
	if p, _ := srv.DB.GetChatPeer(ctx, "mem", "p"); p.Display != "" {
		t.Fatalf("a refused patch saved the name: %+v", p)
	}
	if code, _ := doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/p", "{\"display\":\"Lin\\n\\u202eevil\"}"); code != 200 {
		t.Fatalf("rename: %d", code)
	}
	if p, _ := srv.DB.GetChatPeer(ctx, "mem", "p"); p.Display != "Linevil" {
		t.Fatalf("name %q", p.Display)
	}
}

// attachChat gives a test server a running bridge with one "mem" channel.
func attachChat(t *testing.T, srv *Server) *memAdapter {
	t.Helper()
	ad := &memAdapter{ok: make(chan struct{})}
	memMu.Lock()
	memCurrent = ad
	memMu.Unlock()
	box, err := secret.Open(filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.Chat = chat.New(chat.Deps{
		DB: srv.DB, Term: ChatTerminal(srv.Tmux), Box: box, Log: srv.Log,
		Coalesce:  200 * time.Millisecond,
		PublicURL: func() string { return "https://panel.test" },
		Audit:     func(ctx context.Context, event, detail string) { srv.audit(ctx, event, "chat", "", detail) },
	})
	srv.Chat.Start(ctx)
	if err := srv.Chat.WriteChannel(ctx, "mem", true, chat.ChannelConfig{Values: map[string]string{"token": "sekrit", "name": "bot"}}); err != nil {
		t.Fatal(err)
	}
	<-ad.ok
	return ad
}

func doJSON(t *testing.T, ts *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func TestAHookReportCarriesTheAgentsMessageAndTranscript(t *testing.T) {
	ts, srv := newTestServer(t)
	ad := attachChat(t, srv)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"hooked"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	token, _ := srv.HookToken(ctx)
	if err := srv.DB.PutChatPeer(ctx, store.ChatPeer{Channel: "mem", PeerID: "me", Status: store.PeerPaired, Mode: store.ModeNormal}); err != nil {
		t.Fatal(err)
	}

	report := func(state, doc string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state?sessionId="+sess.ID+"&state="+state, strings.NewReader(doc))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	// The new shape: state in the query, the agent's document as the body.
	if code := report("waiting", `{"hook_event_name":"PermissionRequest","transcript_path":"/home/x/.claude/projects/p/t.jsonl","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`); code != http.StatusNoContent {
		t.Fatalf("status %d", code)
	}
	msgs, _ := srv.DB.ListSessionMessages(ctx, sess.ID, 0)
	if len(msgs) != 1 || msgs[0].Kind != "prompt" || msgs[0].Text != "Bash: go test ./..." {
		t.Fatalf("messages: %+v", msgs)
	}
	tr, ok, _ := srv.DB.GetSessionTranscript(ctx, sess.ID)
	if !ok || tr.Path != "/home/x/.claude/projects/p/t.jsonl" {
		t.Fatalf("transcript: %+v %v", tr, ok)
	}
	// The Notification that follows the same prompt is not a second message.
	report("waiting", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`)
	msgs, _ = srv.DB.ListSessionMessages(ctx, sess.ID, 0)
	if len(msgs) != 1 {
		t.Fatalf("the notification was stored twice: %+v", msgs)
	}
	// An empty body is the old script: the state still lands.
	if code := report("done", ""); code != http.StatusNoContent {
		t.Fatalf("empty body: %d", code)
	}
	// And the legacy body shape still works.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/hook/state", strings.NewReader(`{"sessionId":"`+sess.ID+`","state":"working"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	res, _ := ts.Client().Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("legacy body: %d", res.StatusCode)
	}
	row, _ := srv.DB.GetSession(ctx, sess.ID)
	if row.State != session.StateWorking {
		t.Fatalf("state %q", row.State)
	}
	// A document over the cap loses the message and keeps the state.
	huge := `{"hook_event_name":"Stop","last_assistant_message":"` + strings.Repeat("a", 300<<10) + `"}`
	if code := report("done", huge); code != http.StatusNoContent {
		t.Fatalf("huge: %d", code)
	}
	msgs, _ = srv.DB.ListSessionMessages(ctx, sess.ID, 0)
	if len(msgs) != 1 {
		t.Fatalf("an over-size message was stored: %d", len(msgs))
	}
	// The bridge was told: a Stop with a message reaches the paired peer.
	report("done", `{"hook_event_name":"Stop","last_assistant_message":"All green."}`)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ad.mu.Lock()
		n := len(ad.sent)
		ad.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ad.mu.Lock()
	defer ad.mu.Unlock()
	if len(ad.sent) == 0 || ad.sent[len(ad.sent)-1].Card == nil || !strings.Contains(ad.sent[len(ad.sent)-1].Card.Body, "All green.") {
		t.Fatalf("pushed: %+v", ad.sent)
	}
}

func TestChatSettingsRedactSecretsAndMergeThemOnWrite(t *testing.T) {
	ts, srv := newTestServer(t)
	attachChat(t, srv)
	ctx := context.Background()
	code, body := doJSON(t, ts, http.MethodGet, "/api/chat", "")
	if code != 200 {
		t.Fatalf("GET /api/chat: %d %s", code, body)
	}
	var view chatSettingsView
	_ = json.Unmarshal(body, &view)
	if !view.Available || len(view.Channels) != 1 || view.Channels[0].Kind != "mem" {
		t.Fatalf("view: %s", body)
	}
	ch := view.Channels[0]
	if _, leaked := ch.Values["token"]; leaked || !ch.SecretSet["token"] || ch.Values["name"] != "bot" {
		t.Fatalf("secret handling: %+v", ch)
	}
	if strings.Contains(string(body), "sekrit") {
		t.Fatal("the secret is in the response somewhere")
	}
	if !ch.Health.Running {
		t.Fatalf("health: %+v", ch.Health)
	}
	// A write with the secret left empty keeps it; a new name replaces it.
	code, _ = doJSON(t, ts, http.MethodPut, "/api/chat/channels/mem", `{"enabled":true,"values":{"token":"","name":"renamed"}}`)
	if code != http.StatusNoContent {
		t.Fatalf("PUT: %d", code)
	}
	_, cfg, err := srv.Chat.ReadChannel(ctx, "mem")
	if err != nil || cfg.Values["token"] != "sekrit" || cfg.Values["name"] != "renamed" {
		t.Fatalf("after merge: %+v %v", cfg, err)
	}
	// An unknown adapter is 404, not a row.
	if code, _ = doJSON(t, ts, http.MethodPut, "/api/chat/channels/nope", `{"enabled":true,"values":{}}`); code != http.StatusNotFound {
		t.Fatalf("unknown kind: %d", code)
	}
}

func TestChatRoutesAreValidatedAndPreviewed(t *testing.T) {
	ts, srv := newTestServer(t)
	attachChat(t, srv)
	ctx := context.Background()
	if code, _ := doJSON(t, ts, http.MethodPut, "/api/chat/routes", `{"rules":[{"enabled":true,"screenshot":"maybe"}],"default":{"to":["*"]}}`); code != http.StatusBadRequest {
		t.Fatalf("bad table accepted: %d", code)
	}
	code, body := doJSON(t, ts, http.MethodPut, "/api/chat/routes", `{"rules":[{"id":"r1","name":"quiet","enabled":true,"match":{"tools":["shell"]},"to":[]}],"default":{"to":["*"],"match":{"states":["waiting","done"]},"body":true}}`)
	if code != 200 {
		t.Fatalf("PUT routes: %d %s", code, body)
	}
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"p"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	_ = srv.DB.PutChatPeer(ctx, store.ChatPeer{Channel: "mem", PeerID: "me", Status: store.PeerPaired, Mode: store.ModeNormal})
	code, body = doJSON(t, ts, http.MethodPost, "/api/chat/routes/preview", `{"sessionId":"`+sess.ID+`"}`)
	if code != 200 {
		t.Fatalf("preview: %d %s", code, body)
	}
	var pv routePreviewResponse
	_ = json.Unmarshal(body, &pv)
	// sleep is a shell to the panel, so the quiet rule matches and nobody is told.
	if pv.Decision.Send || pv.Decision.Rule != "quiet" || len(pv.Peers) != 0 {
		t.Fatalf("preview: %+v", pv)
	}
	if code, _ = doJSON(t, ts, http.MethodPost, "/api/chat/routes/preview", `{"sessionId":"nope"}`); code != http.StatusNotFound {
		t.Fatalf("unknown session: %d", code)
	}
}

func TestAKeyProfileWithTextInItIsRefusedOutLoud(t *testing.T) {
	ts, srv := newTestServer(t)
	attachChat(t, srv)
	for _, body := range []string{
		`{"codex":{"approve":["rm -rf /"],"deny":["n"],"interrupt":["Escape"],"submit":["Enter"]}}`,
		`{"codex":{"approve":["y"],"deny":["n; reboot"],"interrupt":["Escape"],"submit":["Enter"]}}`,
		`{"":{"approve":["y"],"deny":["n"],"interrupt":["Escape"],"submit":["Enter"]}}`,
		`{"claude":{"approve":["Entr"],"deny":["Escape"],"interrupt":["Escape"],"submit":["Enter"]}}`,
	} {
		if code, _ := doJSON(t, ts, http.MethodPut, "/api/chat/keys", body); code != http.StatusBadRequest {
			t.Fatalf("%s accepted with %d", body, code)
		}
	}
	code, resp := doJSON(t, ts, http.MethodPut, "/api/chat/keys", `{"codex":{"approve":["y","Enter"],"deny":["n"],"interrupt":["C-c"],"submit":["Enter"]}}`)
	if code != 200 || !strings.Contains(string(resp), `"C-c"`) {
		t.Fatalf("good profile: %d %s", code, resp)
	}
	raw, _ := srv.DB.GetSetting(context.Background(), chat.ToolsKey, "")
	if !strings.Contains(raw, `"C-c"`) {
		t.Fatalf("stored: %s", raw)
	}
}

func TestPairingAndPeerChangesGoThroughTheBridge(t *testing.T) {
	ts, srv := newTestServer(t)
	ad := attachChat(t, srv)
	ctx := context.Background()
	// A stranger says hello through the adapter.
	ad.mu.Lock()
	sink := ad.sink
	ad.mu.Unlock()
	sink.Inbound(ctx, chat.Inbound{PeerID: "stranger", PeerName: "Jo", Text: "hi"})
	var peers []store.ChatPeer
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		peers, _ = srv.DB.ListChatPeers(ctx)
		if len(peers) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(peers) != 1 || peers[0].Status != store.PeerPending {
		t.Fatalf("peers: %+v", peers)
	}
	// Not by pressing a button: the code is what says the row is them. Nor
	// by giving a stranger a mode, which pairing used to carry over.
	if code, _ := doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/stranger", `{"status":"paired"}`); code != http.StatusConflict {
		t.Fatalf("paired without a code: %d", code)
	}
	if code, _ := doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/stranger", `{"mode":"advanced"}`); code != http.StatusConflict {
		t.Fatalf("a mode for a stranger: %d", code)
	}
	if code, _ := doJSON(t, ts, http.MethodPost, "/api/chat/pair", `{"code":"000000"}`); code != http.StatusNotFound && peers[0].PairingCode != "000000" {
		t.Fatalf("wrong code: %d", code)
	}
	code, body := doJSON(t, ts, http.MethodPost, "/api/chat/pair", `{"code":"`+peers[0].PairingCode+`"}`)
	if code != 200 || !strings.Contains(string(body), `"status":"paired"`) {
		t.Fatalf("pair: %d %s", code, body)
	}
	code, body = doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/stranger", `{"mode":"advanced"}`)
	if code != 200 || !strings.Contains(string(body), `"mode":"advanced"`) {
		t.Fatalf("patch mode: %d %s", code, body)
	}
	if code, _ = doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/stranger", `{"mode":"god"}`); code != http.StatusBadRequest {
		t.Fatalf("bad mode: %d", code)
	}
	if code, _ = doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/stranger", `{"status":"blocked"}`); code != 200 {
		t.Fatalf("block: %d", code)
	}
	p, _ := srv.DB.GetChatPeer(ctx, "mem", "stranger")
	if p.Status != store.PeerBlocked || p.Mode != store.ModeAdvanced {
		t.Fatalf("peer: %+v", p)
	}
	// Unblocking is not a way to paired either.
	if code, _ = doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/stranger", `{"status":"paired"}`); code != http.StatusConflict {
		t.Fatalf("blocked to paired: %d", code)
	}
	// A 微信 id has an @ in it, which arrives escaped.
	_ = srv.DB.PutChatPeer(ctx, store.ChatPeer{Channel: "mem", PeerID: "spam@im.wechat", Status: store.PeerPending, Mode: store.ModeNormal})
	if code, body = doJSON(t, ts, http.MethodPatch, "/api/chat/peers/mem/spam%40im.wechat", `{"status":"blocked","display":"  推销  "}`); code != 200 {
		t.Fatalf("an id with an @: %d %s", code, body)
	}
	if q, _ := srv.DB.GetChatPeer(ctx, "mem", "spam@im.wechat"); q.Status != store.PeerBlocked || q.Display != "推销" {
		t.Fatalf("after patch: %+v", q)
	}
	if code, _ = doJSON(t, ts, http.MethodDelete, "/api/chat/peers/mem/spam%40im.wechat", ""); code != http.StatusNoContent {
		t.Fatalf("delete an id with an @: %d", code)
	}
	if code, _ = doJSON(t, ts, http.MethodDelete, "/api/chat/peers/mem/stranger", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if _, err := srv.DB.GetChatPeer(ctx, "mem", "stranger"); err != store.ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	code, body = doJSON(t, ts, http.MethodGet, "/api/chat/log?n=10", "")
	if code != 200 || !strings.Contains(string(body), "chat.paired") {
		t.Fatalf("log: %d %s", code, body)
	}
}

// The tools token reaches these five GETs and nothing else, and nothing else
// reaches them. Same shape as the share-token test, for the same reason.
func TestAChatToolsTokenReachesOnlyTheseRoutes(t *testing.T) {
	ts, srv := newTestServer(t)
	attachChat(t, srv)
	want := []string{
		"GET /api/chat/tools/projects",
		"GET /api/chat/tools/sessions",
		"GET /api/chat/tools/sessions/{handle}/messages",
		"GET /api/chat/tools/sessions/{handle}/screen",
		"GET /api/chat/tools/usage",
	}
	var got []string
	err := chi.Walk(srv.Routes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if strings.HasPrefix(route, "/api/chat/tools") {
			got = append(got, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("routes under /api/chat/tools changed:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	token, _ := srv.ChatToolsToken()
	get := func(path, auth string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		// A fresh client: the test server's default client carries the
		// owner's session cookie, and the point is that the cookie does not
		// help here either way.
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := get("/api/chat/tools/sessions", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code := get("/api/chat/tools/sessions", "Bearer nope"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code := get("/api/chat/tools/sessions", "Bearer "+token); code != http.StatusOK {
		t.Fatalf("right token: %d", code)
	}
	// The token is not an API token: it opens nothing under RequireAuth.
	for _, path := range []string{"/api/state", "/api/chat", "/api/settings/audit"} {
		if code := get(path, "Bearer "+token); code != http.StatusUnauthorized {
			t.Fatalf("the tools token reached %s: %d", path, code)
		}
	}
	// And the owner's cookie does not open the tools.
	res, err := ts.Client().Get(ts.URL + "/api/chat/tools/sessions")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the session cookie reached the tools: %d", res.StatusCode)
	}
}

func TestChatToolsDiscloseHandlesAndNotPaths(t *testing.T) {
	ts, srv := newTestServer(t)
	attachChat(t, srv)
	ctx := context.Background()
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"secretproj"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	_, _ = srv.DB.AddSessionMessage(ctx, store.SessionMessage{SessionID: sess.ID, Kind: "assistant", Text: "hello from the agent"})
	token, _ := srv.ChatToolsToken()
	get := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := get("/api/chat/tools/sessions")
	if code != 200 || !strings.Contains(body, `"handle":`) || !strings.Contains(body, "secretproj") {
		t.Fatalf("sessions: %d %s", code, body)
	}
	for _, leak := range []string{dir, sess.ID, sess.TmuxName, `"cwd"`, `"command"`} {
		if strings.Contains(body, leak) {
			t.Fatalf("sessions disclose %q: %s", leak, body)
		}
	}
	var rows []toolSessionView
	_ = json.Unmarshal([]byte(body), &rows)
	h := rows[0].Handle
	code, body = get("/api/chat/tools/sessions/" + itoa(h) + "/messages?n=5")
	if code != 200 || !strings.Contains(body, "hello from the agent") {
		t.Fatalf("messages: %d %s", code, body)
	}
	code, body = get("/api/chat/tools/sessions/" + itoa(h) + "/screen")
	if code != 200 || !strings.Contains(body, `"text"`) {
		t.Fatalf("screen: %d %s", code, body)
	}
	if code, _ = get("/api/chat/tools/sessions/999/screen"); code != http.StatusNotFound {
		t.Fatalf("unknown handle: %d", code)
	}
	code, body = get("/api/chat/tools/usage?days=7")
	if code != 200 || !strings.Contains(body, `"days":7`) {
		t.Fatalf("usage: %d %s", code, body)
	}
	code, body = get("/api/chat/tools/projects")
	if code != 200 || !strings.Contains(body, "secretproj") || strings.Contains(body, dir) {
		t.Fatalf("projects: %d %s", code, body)
	}
}

func TestTheWebhookDoorIs404WithoutAnAdapterBehindIt(t *testing.T) {
	ts, srv := newTestServer(t)
	attachChat(t, srv)
	res, err := http.Post(ts.URL+"/api/chat/hooks/feishu", "application/json", strings.NewReader(`{"type":"url_verification","challenge":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("no adapter: %d", res.StatusCode)
	}
	// mem is not a webhook adapter either.
	res, _ = http.Post(ts.URL+"/api/chat/hooks/mem", "application/json", strings.NewReader(`{}`))
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("non-webhook adapter: %d", res.StatusCode)
	}
}

func TestChatRoutesAnswer503WithoutABridge(t *testing.T) {
	ts, _ := newTestServer(t)
	code, body := doJSON(t, ts, http.MethodGet, "/api/chat", "")
	if code != 200 || !strings.Contains(string(body), `"available":false`) {
		t.Fatalf("GET without bridge: %d %s", code, body)
	}
	if code, _ = doJSON(t, ts, http.MethodPost, "/api/chat/pair", `{"code":"123456"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("pair without bridge: %d", code)
	}
}
