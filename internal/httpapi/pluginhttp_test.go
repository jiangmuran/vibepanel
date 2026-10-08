package httpapi

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// A process on the panel's port (pluginhttp.go). The process under test is
// `sleep`, and the listener on its socket is this test's own, so what is
// checked is the door and not the plugin: what reaches the socket, what
// comes back, and who gets through.

func httpProcessManifest(auth string) string {
	secret := ""
	if auth == "hmac" {
		secret = `,"secret":"HOOK_SECRET"`
	}
	return `{
  "plugin": 1, "id": "door", "name": {"en": "Door"}, "version": "1.0.0",
  "process": {"command": ["sh", "cmd.sh"], "env": ["HOOK_SECRET"],
              "http": {"auth": "` + auth + `"` + secret + `, "stream": true, "maxBody": "1k"}}
}`
}

type doorEcho struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  string            `json:"query"`
	Body   string            `json:"body"`
	Header map[string]string `json:"header"`
}

// installDoor installs the plugin, starts its process and opens this test's
// listener on the socket the panel named. The listener answers by path:
// most echo the request; a few return things the door must clean.
func installDoor(t *testing.T, ts *httptest.Server, srv *Server, auth string) (echo func(*http.Request) doorEcho, proxySecret string) {
	t.Helper()
	status, body := postZip(t, ts, pluginZip(t, httpProcessManifest(auth), map[string]string{"cmd.sh": "sleep 60"}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	if status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/door/install", `{"caps":[]}`); status != http.StatusOK {
		t.Fatalf("install: %d %s", status, body)
	}
	if status, _ := doJSON(t, ts, http.MethodPut, "/api/settings/plugins/door/secrets/HOOK_SECRET", `{"value":"s3cret"}`); status != http.StatusNoContent {
		t.Fatalf("secret: %d", status)
	}
	if status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/door/enable", `{}`); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, body)
	}
	srv.serviceCtx = t.Context()
	srv.ensurePluginProcesses(t.Context())
	var sock string
	waitFor(t, "the process to start", 5000, func() bool {
		s, secret, ok := srv.pluginSocketFor("door")
		sock, proxySecret = s, secret
		return ok
	})
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		e := doorEcho{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(raw), Header: map[string]string{}}
		for k := range r.Header {
			e.Header[k] = r.Header.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(e)
	})
	mux.HandleFunc("/cookie", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "vp_session=stolen; Path=/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	for p, ct := range map[string]string{"/html": "text/html; charset=utf-8", "/svg": "image/svg+xml", "/png": "image/png", "/csv": "text/csv"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Security-Policy", "default-src *")
			_, _ = w.Write([]byte("<svg onload=alert(1)>"))
		})
	}
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: tick %d\n\n", i)
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		<-r.Context().Done()
	})
	go func() { _ = http.Serve(ln, mux) }()
	echo = func(r *http.Request) doorEcho {
		t.Helper()
		res, err := ts.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: %d %s", r.Method, r.URL.Path, res.StatusCode, raw)
		}
		var e doorEcho
		_ = json.Unmarshal(raw, &e)
		return e
	}
	return echo, proxySecret
}

func doorReq(t *testing.T, ts *httptest.Server, method, path, body string, header map[string]string) *http.Request {
	t.Helper()
	r, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	return r
}

func TestTheOwnerDoorStripsCredentialsAndNamesTheCaller(t *testing.T) {
	ts, srv := newTestServer(t)
	echo, secret := installDoor(t, ts, srv, "owner")
	e := echo(doorReq(t, ts, http.MethodPost, "/api/plugin-http/door/things/1?x=2", `{"a":1}`,
		map[string]string{"Content-Type": "application/json", "X-Vibepanel-Caller": "owner:forged"}))
	if e.Method != "POST" || e.Path != "/things/1" || e.Query != "x=2" || e.Body != `{"a":1}` {
		t.Errorf("the request as the process saw it: %+v", e)
	}
	if e.Header["Cookie"] != "" || e.Header["Authorization"] != "" {
		t.Errorf("the owner's credentials reached the process: %+v", e.Header)
	}
	if e.Header["X-Vibepanel-Caller"] != "owner:tester" {
		t.Errorf("caller = %q, want owner:tester (the forged header must be replaced)", e.Header["X-Vibepanel-Caller"])
	}
	if secret == "" || e.Header["X-Vibepanel-Proxy"] != secret {
		t.Errorf("the per-start secret did not reach the process: %q vs %q", e.Header["X-Vibepanel-Proxy"], secret)
	}
	if e.Header["X-Forwarded-Prefix"] != "/api/plugin-http/door" {
		t.Errorf("prefix: %q", e.Header["X-Forwarded-Prefix"])
	}
	// Nobody: 401, and the process is not asked.
	res, _ := anonGET(t, ts, "/api/plugin-http/door/things")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", res.StatusCode)
	}
	// The path may not climb out of the mount.
	for _, p := range []string{"/api/plugin-http/door/a/%2e%2e/%2e%2e/api/state", "/api/plugin-http/door/a\\..\\b"} {
		r, _ := http.NewRequest(http.MethodGet, ts.URL+p, nil)
		res, err := ts.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == http.StatusOK && !strings.Contains(string(raw), `"path":"/`) {
			t.Errorf("%s: %d %s", p, res.StatusCode, raw)
		}
		if strings.Contains(string(raw), "..") {
			t.Errorf("%s reached the process with .. in it: %s", p, raw)
		}
	}
	// The body cap the manifest asked for.
	r := doorReq(t, ts, http.MethodPost, "/api/plugin-http/door/big", strings.Repeat("x", 2048), nil)
	res, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Errorf("a 2 KiB body passed a 1k cap")
	}
	if status, _ := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/door/process", ""); status != http.StatusOK {
		t.Fatal("status")
	}
	_, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/door/process", "")
	if !strings.Contains(string(body), `"mount":"/api/plugin-http/door/"`) || !strings.Contains(string(body), `"socketUp":true`) {
		t.Errorf("status: %s", body)
	}
}

func TestTheDoorCleansWhatTheProcessAnswers(t *testing.T) {
	ts, srv := newTestServer(t)
	installDoor(t, ts, srv, "owner")
	get := func(p string) *http.Response {
		t.Helper()
		res, err := ts.Client().Get(ts.URL + "/api/plugin-http/door" + p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	if res := get("/cookie"); res.Header.Get("Set-Cookie") != "" {
		t.Errorf("a Set-Cookie came through: %q", res.Header.Get("Set-Cookie"))
	}
	// The panel's own middleware adds its frame-ancestors policy to every
	// response, so the header is read as a list: the door's "sandbox" must be
	// in it for what may not render, and the process's own policy never.
	csp := func(res *http.Response) string {
		return strings.Join(res.Header.Values("Content-Security-Policy"), " | ")
	}
	for _, p := range []string{"/html", "/svg"} {
		res := get(p)
		if !contains(res.Header.Values("Content-Security-Policy"), "sandbox") || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
			t.Errorf("%s: CSP %q disposition %q; it would render on the panel's origin", p, csp(res), res.Header.Get("Content-Disposition"))
		}
		if strings.Contains(csp(res), "default-src") {
			t.Errorf("%s: the process's own policy came through: %q", p, csp(res))
		}
	}
	for _, p := range []string{"/png", "/csv", "/things"} {
		res := get(p)
		if contains(res.Header.Values("Content-Security-Policy"), "sandbox") || res.Header.Get("Content-Disposition") != "" {
			t.Errorf("%s: %q %q; a renderable type was made an attachment", p, csp(res), res.Header.Get("Content-Disposition"))
		}
		if strings.Contains(csp(res), "default-src") {
			t.Errorf("%s: the process's own policy came through: %q", p, csp(res))
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", p)
		}
	}
	// A stream: the first event arrives while the response is still open.
	r, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/plugin-http/door/sse", nil)
	r.Header.Set("Accept", "text/event-stream")
	res, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: tick 0") {
		t.Errorf("first event: %q %v", line, err)
	}
}

func TestThePluginTokenDoor(t *testing.T) {
	ts, srv := newTestServer(t)
	echo, _ := installDoor(t, ts, srv, "token")
	// The owner's own session is not enough on a token door, and nothing is.
	res, _ := anonGET(t, ts, "/api/plugin-http/door/x")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", res.StatusCode)
	}
	if status, _ := doJSON(t, ts, http.MethodGet, "/api/plugin-http/door/x", ""); status != http.StatusUnauthorized {
		t.Errorf("the cookie alone: %d", status)
	}
	status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/door/tokens", `{"name":"the glasses"}`)
	if status != http.StatusCreated {
		t.Fatalf("mint: %d %s", status, body)
	}
	var made struct {
		Token string `json:"token"`
		Row   struct {
			ID string `json:"id"`
		} `json:"row"`
		Mount string `json:"mount"`
	}
	_ = json.Unmarshal(body, &made)
	if made.Token == "" || made.Mount != "/api/plugin-http/door/" {
		t.Fatalf("mint: %s", body)
	}
	bearer := map[string]string{"Authorization": "Bearer " + made.Token}
	e := echo(doorReq(t, ts, http.MethodGet, "/api/plugin-http/door/x", "", bearer))
	if e.Header["X-Vibepanel-Caller"] != "token:the glasses" || e.Header["Authorization"] != "" {
		t.Errorf("caller: %+v", e.Header)
	}
	// Last use is recorded; the list never shows the token.
	_, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins/door/tokens", "")
	if !strings.Contains(string(body), `"name":"the glasses"`) || strings.Contains(string(body), made.Token) || !strings.Contains(string(body), `"lastUsedAt":1`) {
		t.Errorf("list: %s", body)
	}
	// The token is this plugin's mount and nothing else on the panel.
	anon := anonymousClient(t)
	for _, p := range []string{"/api/state", "/api/settings/plugins", "/api/ext/door/x", "/api/plugin/" + made.Token + "/v1/view"} {
		r, _ := http.NewRequest(http.MethodGet, ts.URL+p, nil)
		r.Header.Set("Authorization", "Bearer "+made.Token)
		res, err := anon.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusNotFound {
			t.Errorf("%s with a plugin token: %d", p, res.StatusCode)
		}
	}
	// Revoked: refused, and the row says so.
	if status, _ := doJSON(t, ts, http.MethodDelete, "/api/settings/plugins/door/tokens/"+made.Row.ID, ""); status != http.StatusNoContent {
		t.Fatalf("revoke: %d", status)
	}
	r := doorReq(t, ts, http.MethodGet, "/api/plugin-http/door/x", "", bearer)
	res, err := anon.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", res.StatusCode)
	}
	_, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins/door/tokens", "")
	if !strings.Contains(string(body), `"revokedAt":1`) {
		t.Errorf("after revoke: %s", body)
	}
}

func TestTheDoorAcrossOriginsIsTheOwnersListAndBearerOnly(t *testing.T) {
	ts, srv := newTestServer(t)
	installDoor(t, ts, srv, "token")
	_, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/door/tokens", `{"name":"phone"}`)
	var made struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &made)
	anon := anonymousClient(t)
	call := func(method, origin string, bearer bool) *http.Response {
		t.Helper()
		r, _ := http.NewRequest(method, ts.URL+"/api/plugin-http/door/x", nil)
		r.Header.Set("Origin", origin)
		if bearer {
			r.Header.Set("Authorization", "Bearer "+made.Token)
		}
		res, err := anon.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	// Not on the list: refused, preflight and request alike, token or not.
	if res := call(http.MethodOptions, "https://glasses.example", false); res.StatusCode != http.StatusForbidden {
		t.Errorf("preflight from an unlisted origin: %d", res.StatusCode)
	}
	if res := call(http.MethodGet, "https://glasses.example", true); res.StatusCode != http.StatusForbidden {
		t.Errorf("a request from an unlisted origin with a good token: %d", res.StatusCode)
	}
	// The owner lists it. Exact: a bad one is refused with the reason.
	if status, body := doJSON(t, ts, http.MethodPut, "/api/settings/plugins/door/origins", `{"origins":["https://glasses.example/path"]}`); status != http.StatusBadRequest {
		t.Errorf("an origin with a path: %d %s", status, body)
	}
	if status, body := doJSON(t, ts, http.MethodPut, "/api/settings/plugins/door/origins", `{"origins":["HTTPS://Glasses.Example"]}`); status != http.StatusOK || !strings.Contains(string(body), `"https://glasses.example"`) {
		t.Fatalf("origins: %d %s", status, body)
	}
	res := call(http.MethodOptions, "https://glasses.example", false)
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "https://glasses.example" ||
		res.Header.Get("Access-Control-Allow-Credentials") != "" || !strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Errorf("preflight: %d %v", res.StatusCode, res.Header)
	}
	if res := call(http.MethodGet, "https://glasses.example", true); res.StatusCode != http.StatusOK || res.Header.Get("Access-Control-Allow-Origin") != "https://glasses.example" {
		t.Errorf("a listed origin with a token: %d %v", res.StatusCode, res.Header)
	}
	if res := call(http.MethodGet, "https://other.example", true); res.StatusCode != http.StatusForbidden {
		t.Errorf("another origin: %d", res.StatusCode)
	}
	// On an owner door a cookie never counts across origins.
	srv2ts, srv2 := newTestServer(t)
	installDoor(t, srv2ts, srv2, "owner")
	doJSON(t, srv2ts, http.MethodPut, "/api/settings/plugins/door/origins", `{"origins":["https://glasses.example"]}`)
	r, _ := http.NewRequest(http.MethodGet, srv2ts.URL+"/api/plugin-http/door/x", nil)
	r.Header.Set("Origin", "https://glasses.example")
	res2, err := srv2ts.Client().Do(r) // the signed-in client: cookie attached
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Errorf("an owner door, cross-origin, cookie only: %d, want 401", res2.StatusCode)
	}
}

func TestTheHMACDoor(t *testing.T) {
	ts, srv := newTestServer(t)
	echo, _ := installDoor(t, ts, srv, "hmac")
	anon := anonymousClient(t)
	body := `{"event":"ping"}`
	r, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/plugin-http/door/hook", strings.NewReader(body))
	res, err := anon.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unsigned: %d", res.StatusCode)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(body))
	r = doorReq(t, ts, http.MethodPost, "/api/plugin-http/door/hook", body,
		map[string]string{"X-Signature-256": "sha256=" + hex.EncodeToString(mac.Sum(nil))})
	e := echo(r)
	if e.Body != body || e.Header["X-Vibepanel-Caller"] != "hmac" || e.Header["X-Signature-256"] == "" {
		t.Errorf("signed: %+v", e)
	}
}

func TestTheDoorRateLimitsPerCaller(t *testing.T) {
	ts, srv := newTestServer(t)
	installDoor(t, ts, srv, "owner")
	var last int
	for i := 0; i <= pluginHTTPRateCaller; i++ {
		res, err := ts.Client().Get(ts.URL + "/api/plugin-http/door/x")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		last = res.StatusCode
		if last == http.StatusTooManyRequests {
			if i < pluginHTTPRateCaller {
				t.Fatalf("limited at request %d, the limit is %d", i, pluginHTTPRateCaller)
			}
			break
		}
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("request %d answered %d, want 429", pluginHTTPRateCaller+1, last)
	}
}

// A door is served only while the process runs; disabled, 404; stopped, 503.
func TestTheDoorFollowsTheProcess(t *testing.T) {
	ts, srv := newTestServer(t)
	installDoor(t, ts, srv, "owner")
	if status, _ := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/door/disable", `{}`); status != http.StatusOK {
		t.Fatal("disable")
	}
	srv.ensurePluginProcesses(t.Context())
	if status, _ := doJSON(t, ts, http.MethodGet, "/api/plugin-http/door/x", ""); status != http.StatusNotFound {
		t.Errorf("disabled: %d", status)
	}
	// No door declared at all: 404 and the route is not something a plugin
	// can discover by trying.
	if status, _ := doJSON(t, ts, http.MethodGet, "/api/plugin-http/nothing/x", ""); status != http.StatusNotFound {
		t.Errorf("unknown plugin: %d", status)
	}
}

// The door is exactly two routes under its prefix, on every method.
func TestThePluginHTTPDoorIsOnlyItsPrefix(t *testing.T) {
	_, srv := newTestServer(t)
	var got []string
	_ = chi.Walk(srv.Routes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/plugin-http") {
			got = append(got, route)
		}
		return nil
	})
	sort.Strings(got)
	uniq := []string{}
	for _, r := range got {
		if len(uniq) == 0 || uniq[len(uniq)-1] != r {
			uniq = append(uniq, r)
		}
	}
	want := []string{"/api/plugin-http/{pluginID}", "/api/plugin-http/{pluginID}/*"}
	if strings.Join(uniq, ",") != strings.Join(want, ",") {
		t.Errorf("the door is %v, want %v", uniq, want)
	}
}
