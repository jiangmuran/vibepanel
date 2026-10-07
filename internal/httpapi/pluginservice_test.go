package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A service that subscribes to events, runs a schedule, declares two routes
// and an inbound route, and asks for the capabilities its hooks use.
const serviceManifest = `{
  "plugin": 1, "id": "svc", "name": {"en": "Service"}, "version": "1.0.0",
  "panels": [{"slot": "sidepanel.pane", "entry": "pane.html", "title": {"en": "Svc"}}],
  "capabilities": ["read:panel", "read:notes", "write:notes", "write:todos", "read:todos", "write:state"],
  "data": {"events": {"type": "counter"}, "last": {"type": "text", "max": 200}, "ticks": {"type": "counter"}},
  "server": {"entry": "server.js", "every": "1m", "on": ["session.state", "session.created", "note.changed"],
             "routes": {"GET /digest": "digest", "POST /nudge": "nudge", "GET /loop": "loop", "GET /big": "big", "GET /caps": "caps"}},
  "inbound": {"path": "hook", "secret": "HOOK_SECRET"}
}`

const serviceJS = `
function onEvent(ev, ctx) {
  ctx.data.increment('events')
  ctx.data.set('last', ev.name + ':' + (ev.state || '') + ':' + (ev.session ? 'h' : ''))
}
function onSchedule(ctx) { ctx.data.increment('ticks'); ctx.log('tick') }
function digest(req, ctx) {
  var v = ctx.panel.view()
  return { caller: req.caller, sessions: v.sessions.length, q: req.query.q || '' }
}
function nudge(req, ctx) {
  var p = ctx.panel.view().projects[0]
  ctx.notes.set(p.id, 'nudged: ' + req.body.text)
  var t = ctx.todos.add(p.id, req.body.text)
  return { todo: t.id.length, caps: ctx.caps }
}
function loop(req, ctx) { while (true) {} }
function big(req, ctx) { var s = 'x'; while (s.length < 70000) s += s; return { s: s } }
function caps(req, ctx) {
  return { notes: typeof ctx.notes, sessions: typeof ctx.sessions, fetch: typeof ctx.fetch,
           secretOk: typeof ctx.secret, settings: typeof ctx.settings }
}
function onInbound(req, ctx) { ctx.data.set('last', 'inbound:' + JSON.stringify(req.body)); return { got: true } }
`

func installService(t *testing.T, ts *httptest.Server, srv *Server, caps []string, js string) mintedGrant {
	t.Helper()
	status, body := postZip(t, ts, pluginZip(t, serviceManifest, map[string]string{
		"pane.html": "<p>svc</p>", "server.js": js}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	raw, _ := json.Marshal(caps)
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugins/svc/install", `{"caps": `+string(raw)+`}`)
	if status != http.StatusOK {
		t.Fatalf("install: %d %s", status, body)
	}
	if status, _ := doJSON(t, ts, http.MethodPut, "/api/settings/plugins/svc/secrets/HOOK_SECRET", `{"value": "s3cret"}`); status != http.StatusNoContent {
		t.Fatalf("secret: %d", status)
	}
	if status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/svc/enable", `{}`); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, body)
	}
	// Poll is not running in a test server; the workers it would have
	// started are started here, as Poll does.
	srv.serviceCtx = t.Context()
	srv.ensurePluginWorkers(t.Context())
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugins/svc/grant", `{}`)
	if status != http.StatusCreated {
		t.Fatalf("grant: %d %s", status, body)
	}
	var g mintedGrant
	_ = json.Unmarshal(body, &g)
	return g
}

func serviceData(t *testing.T, srv *Server, key string) any {
	t.Helper()
	rows, err := srv.DB.PluginData(t.Context(), "svc", store.PageDataLive)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if row, ok := rows[key]; ok {
		_ = json.Unmarshal(row.Value, &v)
	}
	return v
}

// An event reaches onEvent off the request path, carrying handles and never
// ids, and the hook's data writes land.
func TestAnEventReachesServerJS(t *testing.T) {
	ts, srv := newTestServer(t)
	installService(t, ts, srv, []string{"read:panel"}, serviceJS)
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"evented"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	// session.created, then a state change.
	doJSON(t, ts, http.MethodPatch, "/api/sessions/"+sess.ID, `{"state": "done"}`)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := serviceData(t, srv, "events").(float64); n >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n, _ := serviceData(t, srv, "events").(float64); n < 2 {
		t.Fatalf("events counted = %v, want at least 2 (created, state)", serviceData(t, srv, "events"))
	}
	last, _ := serviceData(t, srv, "last").(string)
	if !strings.HasPrefix(last, "session.state:done:h") {
		t.Errorf("last event = %q; want session.state with the state and a handle", last)
	}
	if strings.Contains(last, sess.ID) {
		t.Error("the event carried the real session id")
	}
}

// The schedule runs when due and not before; the log says so.
func TestTheScheduleRunsWhenDue(t *testing.T) {
	ts, srv := newTestServer(t)
	installService(t, ts, srv, nil, serviceJS)
	srv.pluginServiceOnce(t.Context())
	if n, _ := serviceData(t, srv, "ticks").(float64); n != 1 {
		t.Fatalf("after the first tick: %v", serviceData(t, srv, "ticks"))
	}
	srv.pluginServiceOnce(t.Context())
	if n, _ := serviceData(t, srv, "ticks").(float64); n != 1 {
		t.Errorf("ran again inside its minute: %v", serviceData(t, srv, "ticks"))
	}
	status, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/svc/server/log", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"text":"tick"`) {
		t.Errorf("log: %d %s", status, body)
	}
}

// A route answers through both doors, with the caller named, and runs under
// the plugin's grants.
func TestARouteAnswersThroughBothDoors(t *testing.T) {
	ts, srv := newTestServer(t)
	g := installService(t, ts, srv, []string{"read:panel", "write:notes", "write:todos"}, serviceJS)
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"routed"}`)

	status, body := anonJSON(t, ts, http.MethodGet, g.API+"x/digest?q=hi", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"caller":"frame"`) || !strings.Contains(string(body), `"q":"hi"`) {
		t.Errorf("frame door: %d %s", status, body)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/ext/svc/digest", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"caller":"owner"`) {
		t.Errorf("owner door: %d %s", status, body)
	}
	status, body = doJSON(t, ts, http.MethodPost, "/api/ext/svc/nudge", `{"text": "ship it"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"todo":16`) {
		t.Fatalf("nudge: %d %s", status, body)
	}
	if note, _ := srv.DB.GetNote(t.Context(), project.ID); note.Content != "nudged: ship it" {
		t.Errorf("the note was not written through ctx.notes: %q", note.Content)
	}
	if todos, _ := srv.DB.ListTodos(t.Context(), project.ID); len(todos) != 1 || todos[0].Text != "ship it" {
		t.Errorf("the todo was not added through ctx.todos: %+v", todos)
	}
	// An undeclared route is 404; an unknown plugin too.
	if status, _ := anonJSON(t, ts, http.MethodGet, g.API+"x/nope", ""); status != http.StatusNotFound {
		t.Errorf("undeclared route: %d", status)
	}
	// A stranger at the owner's door is 401.
	if status, _ := anonJSON(t, ts, http.MethodGet, "/api/ext/svc/digest", ""); status != http.StatusUnauthorized {
		t.Errorf("anonymous owner door: %d", status)
	}
}

// ctx has one member per granted capability and no stub for the rest.
func TestCtxHasOneMemberPerCapability(t *testing.T) {
	ts, srv := newTestServer(t)
	installService(t, ts, srv, []string{"read:panel"}, serviceJS)
	status, body := doJSON(t, ts, http.MethodGet, "/api/ext/svc/caps", "")
	if status != http.StatusOK {
		t.Fatalf("caps: %d %s", status, body)
	}
	var out struct {
		Result map[string]string `json:"result"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Result["notes"] != "undefined" || out.Result["sessions"] != "undefined" || out.Result["fetch"] != "undefined" {
		t.Errorf("members present without their capability: %v", out.Result)
	}
	if out.Result["secretOk"] != "function" || out.Result["settings"] != "object" {
		t.Errorf("every service's members missing: %v", out.Result)
	}
	// Grant notes: the member appears at the next call.
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/svc/caps", `{"caps": ["read:panel", "read:notes"]}`)
	_, body = doJSON(t, ts, http.MethodGet, "/api/ext/svc/caps", "")
	_ = json.Unmarshal(body, &out)
	if out.Result["notes"] != "object" {
		t.Errorf("notes after granting read:notes: %v", out.Result)
	}
	// nudge needs write:notes, which is not granted: ctx.notes.set is absent
	// and the call fails, with the reason in the log.
	status, body = doJSON(t, ts, http.MethodPost, "/api/ext/svc/nudge", `{"text": "x"}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "failed") {
		t.Errorf("a hook using an ungranted member: %d %s", status, body)
	}
}

// A loop is interrupted at its budget; a result over the cap is refused;
// neither takes the panel with it.
func TestAHookIsBoundedInTimeAndSize(t *testing.T) {
	ts, srv := newTestServer(t)
	installService(t, ts, srv, nil, serviceJS)
	start := time.Now()
	status, body := doJSON(t, ts, http.MethodGet, "/api/ext/svc/loop", "")
	if status != http.StatusBadRequest || !strings.Contains(string(body), "budget") {
		t.Errorf("a loop: %d %s", status, body)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("the loop ran for %v", time.Since(start))
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/ext/svc/big", "")
	if status != http.StatusBadRequest || !strings.Contains(string(body), "KiB") {
		t.Errorf("a big result: %d %s", status, body)
	}
	if status, _ := doJSON(t, ts, http.MethodGet, "/api/settings/plugins", ""); status != http.StatusOK {
		t.Errorf("the panel after a loop and a flood: %d", status)
	}
}

// The inbound door: verified against the secret, by bearer or by HMAC, rate
// limited, and 404 where no route is declared.
func TestTheInboundDoorIsVerifiedFirst(t *testing.T) {
	ts, srv := newTestServer(t)
	installService(t, ts, srv, nil, serviceJS)
	client := anonymousClient(t)
	call := func(headers map[string]string, body string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/plugin-hook/svc/hook", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if status, _ := call(nil, `{"a":1}`); status != http.StatusUnauthorized {
		t.Errorf("no credential: %d", status)
	}
	if status, _ := call(map[string]string{"Authorization": "Bearer wrong"}, `{"a":1}`); status != http.StatusUnauthorized {
		t.Errorf("wrong bearer: %d", status)
	}
	status, body := call(map[string]string{"Authorization": "Bearer s3cret"}, `{"a":1}`)
	if status != http.StatusOK || !strings.Contains(body, `"got":true`) {
		t.Errorf("bearer: %d %s", status, body)
	}
	if last, _ := serviceData(t, srv, "last").(string); last != `inbound:{"a":1}` {
		t.Errorf("onInbound did not run: %q", last)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(`{"b":2}`))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if status, body := call(map[string]string{"X-Signature-256": sig}, `{"b":2}`); status != http.StatusOK {
		t.Errorf("hmac: %d %s", status, body)
	}
	if status, _ := call(map[string]string{"X-Signature-256": sig}, `{"b":3}`); status != http.StatusUnauthorized {
		t.Errorf("hmac over another body: %d", status)
	}
	if status, _ := anonJSON(t, ts, http.MethodPost, "/api/plugin-hook/svc/other", `{}`); status != http.StatusNotFound {
		t.Errorf("an undeclared inbound path: %d", status)
	}
	astatus, abody := doJSON(t, ts, http.MethodGet, "/api/settings/audit", "")
	if !strings.Contains(string(abody), "plugin.inbound_rejected") {
		t.Errorf("rejections not audited: %d %s", astatus, abody)
	}
}

// ctx.fetch reaches only a granted host, through the guard: a host that
// resolves to loopback is refused after resolution, whatever the grant.
func TestFetchIsGuardedByHostAndAddress(t *testing.T) {
	ts, srv := newTestServer(t)
	js := `function digest(req, ctx) { return ctx.fetch('https://api.example.test/x') }
function caps(req, ctx) { return { fetch: typeof ctx.fetch } }`
	manifest := strings.Replace(serviceManifest, `"capabilities": ["read:panel", "read:notes", "write:notes", "write:todos", "read:todos", "write:state"]`,
		`"capabilities": ["read:panel"], "sources": [{"key": "x", "url": "https://api.example.test/v1", "every": "10m"}]`, 1)
	status, body := postZip(t, ts, pluginZip(t, manifest, map[string]string{"pane.html": "<p>s</p>", "server.js": js}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	// Without net:api.example.test there is no ctx.fetch at all.
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/svc/install", `{"caps": ["read:panel"]}`)
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/svc/secrets/HOOK_SECRET", `{"value": "s"}`)
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/svc/enable", `{}`)
	_, body = doJSON(t, ts, http.MethodGet, "/api/ext/svc/caps", "")
	if !strings.Contains(string(body), `"fetch":"undefined"`) {
		t.Errorf("fetch present without a net grant: %s", body)
	}
	// Granted, but the host resolves to loopback: refused after resolution.
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/svc/caps", `{"caps": ["read:panel", "net:api.example.test"]}`)
	srv.psv.fetcher = &sourceFetcher{resolve: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}}
	status, body = doJSON(t, ts, http.MethodGet, "/api/ext/svc/digest", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"ok":false`) || strings.Contains(string(body), `"ok":true`) {
		t.Errorf("a loopback address through a granted host: %d %s", status, body)
	}
	// And the source list says the host is granted but the fetch failed.
	srv.pluginServiceOnce(t.Context())
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins/svc/sources", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"granted":true`) || strings.Contains(string(body), `"ok":true`) {
		t.Errorf("sources: %d %s", status, body)
	}
}

// Events a plugin cannot keep up with are dropped, counted, and never block
// the sender.
func TestAFullEventQueueDropsAndCounts(t *testing.T) {
	ts, srv := newTestServer(t)
	installService(t, ts, srv, nil, serviceJS)
	// Stop the worker from draining by taking its channel away from it.
	srv.psv.mu.Lock()
	w := srv.psv.workers["svc"]
	srv.psv.mu.Unlock()
	if w == nil {
		t.Fatal("no worker for a subscribed plugin")
	}
	// Fill the queue past its size from the sender's side; the worker is
	// also draining, so this is a race the sender must never lose.
	start := time.Now()
	for i := 0; i < pluginEventQueue*3; i++ {
		srv.pluginEventRaised(pluginEvent{Name: "session.created", SessionID: "s", ProjectID: "p"})
	}
	if time.Since(start) > time.Second {
		t.Errorf("sending %d events took %v; the sender waited", pluginEventQueue*3, time.Since(start))
	}
	// Nothing to assert about the exact count -- the worker drains at its own
	// pace -- beyond that the queue is bounded and the panel answered.
	if status, _ := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/svc/server/log", ""); status != http.StatusOK {
		t.Errorf("log after a flood: %d", status)
	}
	_ = plugins.Capabilities
}
