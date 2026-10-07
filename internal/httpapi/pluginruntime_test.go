package httpapi

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A plugin with a pane that asks for every capability the frame rung
// serves, so a grant can be narrowed to any one of them.
const paneManifest = `{
  "plugin": 1, "id": "pane", "name": {"en": "Pane"}, "version": "1.0.0",
  "panels": [{"slot": "sidepanel.pane", "entry": "pane.html", "title": {"en": "Pane"}}],
  "capabilities": ["read:panel", "read:paths", "read:terminal", "read:notes", "read:todos",
    "write:notes", "write:todos", "write:state", "sessions:control", "sessions:create", "sessions:input",
    "read:resources", "read:usage", "read:git", "ui:open", "ui:notify"],
  "data": {"votes": {"type": "counter"}, "note": {"type": "text", "max": 20}}
}`

type mintedGrant struct {
	Grant string `json:"grant"`
	Base  string `json:"base"`
	API   string `json:"api"`
	Dev   bool   `json:"dev"`
}

// installPane adds the pane plugin, installs it with the given grants and
// mints a grant for the owner's session.
func installPane(t *testing.T, ts *httptest.Server, caps []string) mintedGrant {
	t.Helper()
	status, body := postZip(t, ts, pluginZip(t, paneManifest, map[string]string{
		"pane.html": "<!doctype html><script src=\"vibepanel-plugin.js\"></script><p>pane</p>",
		"style.css": "p { color: red }"}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	raw, _ := json.Marshal(caps)
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugins/pane/install", `{"caps": `+string(raw)+`}`)
	if status != http.StatusOK {
		t.Fatalf("install: %d %s", status, body)
	}
	return mintPane(t, ts)
}

func mintPane(t *testing.T, ts *httptest.Server) mintedGrant {
	t.Helper()
	status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/pane/grant", `{}`)
	if status != http.StatusCreated {
		t.Fatalf("grant: %d %s", status, body)
	}
	var g mintedGrant
	_ = json.Unmarshal(body, &g)
	return g
}

// anonJSON is a request with no cookie: what a frame sends.
func anonJSON(t *testing.T, ts *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	client := anonymousClient(t)
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// Red line 10, as a list: a plugin credential reaches /plugin/{grant} and the
// v1 routes the capability table names, and nothing else under either prefix.
func TestAPluginCredentialReachesOnlyTheseRoutes(t *testing.T) {
	_, srv := newTestServer(t)
	want := []string{"GET /plugin/{grant}", "GET /plugin/{grant}/*"}
	for key := range plugins.RouteTable() {
		method, pattern, _ := strings.Cut(key, " ")
		want = append(want, method+" /api/plugin/{cred}/v1"+pattern)
	}
	sort.Strings(want)
	var got []string
	err := chi.Walk(srv.Routes().(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if strings.HasPrefix(route, "/api/plugin/") || strings.HasPrefix(route, "/plugin/") {
				got = append(got, method+" "+route)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the plugin surface is\n  %s\nwant\n  %s\nA route added here is reachable by a plugin's frame. "+
			"If that is the intent, name it in the capability table, which is where the decision is made.",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// Every capability opens exactly the routes the table says. A grant holding
// one capability is sent to every v1 route; the ones the table names for it
// answer something other than 403, and every other named route answers 403.
func TestEveryCapabilityOpensOnlyItsRoutes(t *testing.T) {
	table := plugins.RouteTable()
	routes := make([]string, 0, len(table))
	for key := range table {
		routes = append(routes, key)
	}
	sort.Strings(routes)
	for _, c := range plugins.Capabilities() {
		t.Run(c.Name, func(t *testing.T) {
			ts, _ := newTestServer(t)
			g := installPane(t, ts, []string{c.Name})
			for _, key := range routes {
				if key == "GET /events" {
					continue // a stream; its gate is the same middleware as /view
				}
				method, pattern, _ := strings.Cut(key, " ")
				path := strings.NewReplacer("{h}", "nothing", "{key}", "votes").Replace(pattern)
				body := ""
				if method != http.MethodGet && method != http.MethodDelete {
					body = `{"value": 1, "by": 1, "item": {}, "text": "x", "content": "x", "state": "done", "done": true}`
				}
				status, _ := anonJSON(t, ts, method, g.API+strings.TrimPrefix(path, "/"), body)
				opens := len(table[key]) == 0 || contains(table[key], c.Name)
				if opens && status == http.StatusForbidden {
					t.Errorf("%s: %s refused, and the table says it opens it", c.Name, key)
				}
				if !opens && status != http.StatusForbidden {
					t.Errorf("%s: %s answered %d, and the table does not name it", c.Name, key, status)
				}
			}
		})
	}
}

// A grant is a grant: as a cookie, a bearer or a path segment anywhere else
// it is an unknown string, and a share token or an admin grant on the plugin
// prefix is the same.
func TestAPluginGrantDoesNotCrossSurfaces(t *testing.T) {
	ts, _ := newTestServer(t)
	g := installPane(t, ts, []string{"read:panel"})
	client := anonymousClient(t)
	for _, tc := range []struct{ name, path string }{
		{"the state", "/api/state"}, {"the settings", "/api/settings"}, {"the audit log", "/api/settings/audit"},
		{"the plugins list", "/api/settings/plugins"}, {"a share snapshot", "/api/share/" + g.Grant + "/v1/snapshot"},
		{"an admin API", "/api/page-admin/" + g.Grant + "/v1/data"},
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+g.Grant)
		req.AddCookie(&http.Cookie{Name: "vibepanel_session", Value: g.Grant})
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("a plugin grant on %s: %d, want 401", tc.name, res.StatusCode)
		}
	}
	// And the frame's own routes refuse an unknown string, so the whole
	// surface answers 401 to anything but a live grant.
	for _, path := range []string{"/api/plugin/" + strings.Repeat("x", 32) + "/v1/view", "/plugin/" + strings.Repeat("x", 32) + "/pane.html"} {
		status, _ := anonJSON(t, ts, http.MethodGet, path, "")
		if status != http.StatusUnauthorized {
			t.Errorf("%s with a made-up credential: %d", path, status)
		}
	}
}

// The frame's files carry the sandbox and a connect-src that names this
// grant's API and nothing else; the SDK and the stylesheet come from the
// binary; what the plugin keeps to itself is not served.
func TestAFrameIsServedInsideTheSandbox(t *testing.T) {
	ts, _ := newTestServer(t)
	g := installPane(t, ts, []string{"read:panel"})
	res, body := anonGET(t, ts, g.Base+"pane.html")
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "<p>pane</p>") {
		t.Fatalf("pane.html: %d %s", res.StatusCode, body)
	}
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"sandbox allow-scripts allow-forms", "connect-src " + ts.URL + g.API + " " + ts.URL + g.Base + ";",
		"frame-ancestors 'self'", "default-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q:\n%s", want, csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Error("allow-same-origin beside allow-scripts is the owner's cookie handed to the frame")
	}
	for _, f := range []string{"style.css", plugins.SDKFile, plugins.UIFile} {
		res, body := anonGET(t, ts, g.Base+f)
		if res.StatusCode != http.StatusOK || len(body) == 0 || res.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: %d, csp=%q", f, res.StatusCode, res.Header.Get("Content-Security-Policy"))
		}
	}
	if res, _ := anonGET(t, ts, g.Base+plugins.ManifestFile); res.StatusCode != http.StatusNotFound {
		t.Errorf("plugin.json served: %d", res.StatusCode)
	}
	if res, _ := anonGET(t, ts, g.Base+"../pane.html"); res.StatusCode == http.StatusOK {
		t.Errorf("a path out of the plugin was served")
	}
}

// The view embeds nothing: without read:paths every path field is empty,
// found by walking the struct rather than by a list of names, so a path
// field added later is caught too.
func TestTheViewWithoutReadPathsCarriesNoPath(t *testing.T) {
	ts, _ := newTestServer(t)
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"viewed"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	_ = sess
	g := installPane(t, ts, []string{"read:panel"})
	status, body := anonJSON(t, ts, http.MethodGet, g.API+"view", "")
	if status != http.StatusOK {
		t.Fatalf("view: %d %s", status, body)
	}
	var v pluginView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Projects) != 1 || len(v.Sessions) != 1 {
		t.Fatalf("view has %d projects and %d sessions", len(v.Projects), len(v.Sessions))
	}
	if strings.Contains(string(body), dir) || strings.Contains(string(body), project.ID) || strings.Contains(string(body), sess.ID) {
		t.Errorf("the view carries a path or a real id:\n%s", body)
	}
	for _, row := range []any{v.Projects[0], v.Sessions[0]} {
		rv := reflect.ValueOf(row)
		for i := 0; i < rv.NumField(); i++ {
			name := rv.Type().Field(i).Name
			if (name == "Path" || name == "CWD" || name == "Command") && rv.Field(i).String() != "" {
				t.Errorf("%s.%s = %q without read:paths", rv.Type().Name(), name, rv.Field(i).String())
			}
		}
	}
	if !contains(v.Caps, "read:panel") || contains(v.Caps, "read:paths") {
		t.Errorf("caps = %v", v.Caps)
	}

	// With read:paths the same view carries them.
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/pane/caps", `{"caps": ["read:panel", "read:paths"]}`)
	status, body = anonJSON(t, ts, http.MethodGet, g.API+"view", "")
	_ = json.Unmarshal(body, &v)
	// The project's path is known at creation; a session's command is what
	// the poller reads off the pane later, so only the path is asserted.
	if status != http.StatusOK || v.Projects[0].Path != dir || v.Sessions[0].CWD == "" {
		t.Errorf("with read:paths: %d path=%q cwd=%q", status, v.Projects[0].Path, v.Sessions[0].CWD)
	}
	// Handles are stable within the plugin and different from the ids.
	if v.Sessions[0].ProjectID != v.Projects[0].ID || v.Projects[0].ID == project.ID {
		t.Errorf("handles: session.projectId=%s project.id=%s", v.Sessions[0].ProjectID, v.Projects[0].ID)
	}
}

// A box unticked after the fact is withdrawn from an open frame at its next
// request, not at its next mint; signing out ends the grant.
func TestAGrantFollowsTheDecisionAndTheSession(t *testing.T) {
	ts, _ := newTestServer(t)
	g := installPane(t, ts, []string{"read:panel", "read:notes"})
	if status, _ := anonJSON(t, ts, http.MethodGet, g.API+"view", ""); status != http.StatusOK {
		t.Fatalf("view: %d", status)
	}
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/pane/caps", `{"caps": ["read:notes"]}`)
	if status, body := anonJSON(t, ts, http.MethodGet, g.API+"view", ""); status != http.StatusForbidden || !strings.Contains(string(body), "read:panel") {
		t.Errorf("after unticking read:panel: %d %s", status, body)
	}
	if status, _ := anonJSON(t, ts, http.MethodGet, g.API+"me", ""); status != http.StatusOK {
		t.Errorf("me after unticking: %d", status)
	}
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/pane/disable", `{}`)
	if status, _ := anonJSON(t, ts, http.MethodGet, g.API+"me", ""); status != http.StatusUnauthorized {
		t.Errorf("a disabled plugin's grant still resolves: %d", status)
	}
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/pane/enable", `{}`)
	if status, _ := anonJSON(t, ts, http.MethodGet, g.API+"me", ""); status != http.StatusOK {
		t.Errorf("re-enabled: %d", status)
	}
	doJSON(t, ts, http.MethodPost, "/api/auth/logout", "")
	if status, _ := anonJSON(t, ts, http.MethodGet, g.API+"me", ""); status != http.StatusUnauthorized {
		t.Errorf("after signing out the grant still resolves: %d", status)
	}
}

// What the capabilities open, through handles: notes, todos, a state mark,
// and the plugin's own data.
func TestAFrameWritesThroughHandles(t *testing.T) {
	ts, srv := newTestServer(t)
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"handled"}`)
	sess := postJSON[store.Session](t, ts, "/api/sessions", `{"projectId":"`+project.ID+`","command":["sleep","60"]}`)
	g := installPane(t, ts, []string{"read:panel", "read:notes", "write:notes", "read:todos", "write:todos", "write:state"})
	ctx := t.Context()
	ph := srv.pluginHandle(ctx, "pane", project.ID)
	sh := srv.pluginHandle(ctx, "pane", sess.ID)

	status, body := anonJSON(t, ts, http.MethodPut, g.API+"projects/"+ph+"/notes", `{"content": "from a plugin"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"projectId":"`+ph+`"`) {
		t.Errorf("put note: %d %s", status, body)
	}
	if note, _ := srv.DB.GetNote(ctx, project.ID); note.Content != "from a plugin" {
		t.Errorf("the note was not written: %q", note.Content)
	}
	status, body = anonJSON(t, ts, http.MethodPost, g.API+"projects/"+ph+"/todos", `{"text": "ship it"}`)
	if status != http.StatusCreated {
		t.Fatalf("add todo: %d %s", status, body)
	}
	var todo pluginTodo
	_ = json.Unmarshal(body, &todo)
	if todo.ProjectID != ph || todo.ID == "" || len(todo.ID) != 16 {
		t.Errorf("todo = %+v", todo)
	}
	status, body = anonJSON(t, ts, http.MethodPatch, g.API+"todos/"+todo.ID, `{"done": true}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"done":true`) {
		t.Errorf("patch todo: %d %s", status, body)
	}
	status, body = anonJSON(t, ts, http.MethodGet, g.API+"projects/"+ph+"/todos", "")
	if status != http.StatusOK || !strings.Contains(string(body), "ship it") || strings.Contains(string(body), project.ID) {
		t.Errorf("list todos: %d %s", status, body)
	}
	if status, _ := anonJSON(t, ts, http.MethodDelete, g.API+"todos/"+todo.ID, ""); status != http.StatusNoContent {
		t.Errorf("delete todo: %d", status)
	}
	status, body = anonJSON(t, ts, http.MethodPatch, g.API+"sessions/"+sh+"/state", `{"state": "done"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"state":"done"`) {
		t.Errorf("state: %d %s", status, body)
	}
	if rec, _ := srv.DB.GetSession(ctx, sess.ID); string(rec.State) != "done" {
		t.Errorf("the state was not marked: %s", rec.State)
	}
	// A handle that resolves to nothing is 404, never another plugin's row.
	if status, _ := anonJSON(t, ts, http.MethodGet, g.API+"projects/0123456789abcdef/notes", ""); status != http.StatusNotFound {
		t.Errorf("an unknown handle: %d", status)
	}

	// Data: the plugin's own, by the manifest's schema.
	status, body = anonJSON(t, ts, http.MethodPost, g.API+"data/votes/increment", `{"by": 2}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"value":2`) {
		t.Errorf("increment: %d %s", status, body)
	}
	status, body = anonJSON(t, ts, http.MethodPut, g.API+"data/note", `{"value": "this is far too long for twenty"}`)
	if status != http.StatusBadRequest {
		t.Errorf("a value outside the schema: %d %s", status, body)
	}
	status, body = anonJSON(t, ts, http.MethodGet, g.API+"data", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"votes":2`) {
		t.Errorf("data: %d %s", status, body)
	}
}

// The event stream sends a view on connect and again after a change.
func TestTheEventStreamFollowsTheState(t *testing.T) {
	ts, _ := newTestServer(t)
	g := installPane(t, ts, []string{"read:panel"})
	client := anonymousClient(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+g.API+"events", nil)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(res.Body)
	readEvent := func() string {
		deadline := time.Now().Add(5 * time.Second)
		var data string
		for time.Now().Before(deadline) {
			line, err := reader.ReadString('\n')
			if err != nil {
				return data
			}
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
			if line == "\n" && data != "" {
				return data
			}
		}
		return data
	}
	first := readEvent()
	if !strings.Contains(first, `"projects":[]`) {
		t.Fatalf("first event: %s", first)
	}
	dir := t.TempDir()
	postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"later"}`)
	second := readEvent()
	if !strings.Contains(second, `"name":"later"`) {
		t.Errorf("after a change: %s", second)
	}
}

// Dev mode: the draft directory's files and manifest are what runs, under
// the grants already given; the fingerprint moves when a file does.
func TestDevModeRunsTheDraft(t *testing.T) {
	ts, _ := newTestServer(t)
	g := installPane(t, ts, []string{"read:panel"})
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(plugins.ManifestFile, paneManifest)
	write("pane.html", "<p>draft</p>")
	status, body := doJSON(t, ts, http.MethodPut, "/api/settings/plugins/pane/dev", `{"dev": true, "sourceDir": "`+dir+`"}`)
	if status != http.StatusOK {
		t.Fatalf("dev on: %d %s", status, body)
	}
	g2 := mintPane(t, ts)
	if !g2.Dev {
		t.Error("the grant does not say dev")
	}
	res, b := anonGET(t, ts, g2.Base+"pane.html")
	if res.StatusCode != http.StatusOK || string(b) != "<p>draft</p>" {
		t.Errorf("draft file: %d %q", res.StatusCode, b)
	}
	// The old grant follows too: dev mode is the plugin's state, not the grant's.
	res, b = anonGET(t, ts, g.Base+"pane.html")
	if string(b) != "<p>draft</p>" {
		t.Errorf("the earlier grant still serves the installed file: %q", b)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins/pane/draft/fingerprint", "")
	before := string(body)
	time.Sleep(20 * time.Millisecond)
	write("pane.html", "<p>draft 2</p>")
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins/pane/draft/fingerprint", "")
	if status != http.StatusOK || string(body) == before {
		t.Errorf("fingerprint did not move: %s", body)
	}
	// A draft whose manifest is for another plugin is refused.
	write(plugins.ManifestFile, strings.Replace(paneManifest, `"id": "pane"`, `"id": "other"`, 1))
	status, body = doJSON(t, ts, http.MethodPut, "/api/settings/plugins/pane/dev", `{"dev": true}`)
	if status != http.StatusBadRequest {
		t.Errorf("a draft for another id: %d %s", status, body)
	}
	write(plugins.ManifestFile, paneManifest)
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/pane/dev", `{"dev": false}`)
	res, b = anonGET(t, ts, g.Base+"pane.html")
	if !strings.Contains(string(b), "<p>pane</p>") {
		t.Errorf("after dev off the installed file is not back: %q", b)
	}
}

// A grant needs the panel's own session: a bearer token cannot mint one.
func TestAGrantIsMintedForASessionOnly(t *testing.T) {
	ts, _ := newTestServer(t)
	installPane(t, ts, []string{"read:panel"})
	status, body := doJSON(t, ts, http.MethodPost, "/api/settings/tokens", `{"name": "ci"}`)
	if status != http.StatusCreated {
		t.Fatalf("token: %d %s", status, body)
	}
	var tok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &tok)
	client := anonymousClient(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/plugins/pane/grant", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a bearer token minted a grant: %d", res.StatusCode)
	}
}

// A dozen frames open on a change are a dozen cuts of one state, not a
// dozen reads of every table: the view's state is built once per bus
// generation, and again after the next bump.
func TestThePluginViewIsBuiltOncePerChange(t *testing.T) {
	ts, srv := newTestServer(t)
	g := installPane(t, ts, []string{"read:panel"})
	before := srv.prt.stateBuilds
	for range 3 {
		if status, body := anonJSON(t, ts, http.MethodGet, g.API+"view", ""); status != http.StatusOK {
			t.Fatalf("view: %d %s", status, body)
		}
	}
	if n := srv.prt.stateBuilds - before; n != 1 {
		t.Fatalf("three views in one generation built the state %d times, want 1", n)
	}
	srv.bumpPluginWatchers()
	if status, _ := anonJSON(t, ts, http.MethodGet, g.API+"view", ""); status != http.StatusOK {
		t.Fatal("view after a bump")
	}
	if n := srv.prt.stateBuilds - before; n != 2 {
		t.Fatalf("a view after a bump built the state %d times in all, want 2", n)
	}
}
