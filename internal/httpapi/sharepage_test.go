package httpapi

import (
	"context"
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

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A share page is somebody's HTML on the panel's origin, drawn for people who
// did not write it. Every test here is about the shape of what that HTML can
// reach, rather than about how a page looks.

// callJSON makes one request as the signed-in owner and returns the status and
// body, without failing on a status: several of these tests are about refusals.
func callJSON(t *testing.T, ts *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

func anonGET(t *testing.T, ts *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	client := anonymousClient(t)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

type testPage struct {
	dir  string
	page store.SharePage
}

func pageDir(t *testing.T, manifest string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{
		"vibepanel.json": manifest,
		"index.html":     `<!doctype html><link rel="stylesheet" href="style.css"><script src="vibepanel.js"></script>`,
		"style.css":      "body { color: red }",
		"app.js":         "VibePanel.connect()",
		"icon.svg":       `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
	}
	for k, v := range files {
		all[k] = v
	}
	for p, body := range all {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// newPublishedPage adopts a directory as a page and publishes it.
func newPublishedPage(t *testing.T, ts *httptest.Server, manifest string, files map[string]string) testPage {
	t.Helper()
	dir := pageDir(t, manifest, files)
	body, _ := json.Marshal(map[string]string{"name": "Lobby", "sourceDir": dir})
	page := postJSON[store.SharePage](t, ts, "/api/settings/pages", string(body))
	postJSON[map[string]int](t, ts, "/api/settings/pages/"+page.ID+"/publish", `{"note":"first"}`)
	return testPage{dir: dir, page: page}
}

func pageLink(t *testing.T, ts *httptest.Server, pageID, extra string) freshShare {
	t.Helper()
	return newShare(t, ts, `{"name":"wall","detail":"names","pageId":"`+pageID+`"`+extra+`}`)
}

func snapshotGET(t *testing.T, ts *httptest.Server, token string) (int, shareSnapshot, *http.Response) {
	t.Helper()
	res, body := anonGET(t, ts, "/api/share/"+token+"/v1/snapshot")
	var out shareSnapshot
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode snapshot: %v: %s", err, body)
		}
	}
	return res.StatusCode, out, res
}

const sessionsManifest = `{"sdk":1,"name":"Lobby","sections":["sessions"],
	"params":[{"key":"title","type":"text","default":"Lobby"},{"key":"warnAt","type":"number","min":1,"max":9}]}`

// Red line 8, as a list. A share token reaches GETs, all of them named here,
// and nothing else under either prefix. Adding a route to the share surface is
// adding a line to this list, which is the decision this test exists to make
// visible.
func TestAShareTokenReachesOnlyTheseRoutes(t *testing.T) {
	_, srv := newTestServer(t)
	want := []string{
		"GET /api/share/{token}/v1/snapshot",
		"GET /share/{token}",
		"GET /share/{token}/*",
		// The one write, and its preflight: docs/page-backend.md §6.
		"OPTIONS /api/share/{token}/v1/actions/{name}",
		"POST /api/share/{token}/v1/actions/{name}",
	}
	var got []string
	err := chi.Walk(srv.Routes().(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if strings.HasPrefix(route, "/api/share/") || strings.HasPrefix(route, "/share/") {
				got = append(got, method+" "+route)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the share surface is\n  %s\nwant\n  %s\nA route added here is reachable by anybody holding "+
			"a share link. If that is the intent, add it to this list and to red line 8.",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestEveryFileOfAPageIsServedInsideTheSandbox(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	link := pageLink(t, ts, p.page.ID, "")

	// Without the slash, relative URLs would resolve against /share/.
	res, _ := anonGET(t, ts, "/share/"+link.Token)
	if res.StatusCode != http.StatusPermanentRedirect || res.Header.Get("Location") != "/share/"+link.Token+"/" {
		t.Errorf("no-slash = %d → %q, want a redirect to the slash", res.StatusCode, res.Header.Get("Location"))
	}

	for _, path := range []string{"", "style.css", "app.js", "icon.svg", "vibepanel.js"} {
		res, body := anonGET(t, ts, "/share/"+link.Token+"/"+path)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%q = %d: %s", path, res.StatusCode, body)
			continue
		}
		csp := res.Header.Get("Content-Security-Policy")
		// Every file, not only the document: a stylesheet or an SVG opened in
		// the address bar is a document too, and without the sandbox it would
		// run on the panel's origin.
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%q is served without the sandbox: %s", path, csp)
		}
		if strings.Contains(csp, "allow-same-origin") {
			t.Errorf("%q: allow-same-origin beside allow-scripts hands over the panel's origin", path)
		}
		if !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%q: a handed-out page is framable: %s", path, csp)
		}
		connect := directive(csp, "connect-src")
		for _, src := range strings.Fields(connect) {
			if !strings.HasPrefix(src, ts.URL+"/api/share/"+link.Token+"/v1/") &&
				src != ts.URL+"/share/"+link.Token+"/" {
				t.Errorf("%q: connect-src names %s; a page reaches its own snapshot and files and nothing else",
					path, src)
			}
		}
		if strings.Contains(csp, "'self'") {
			t.Errorf("%q: 'self' matches nothing from an opaque origin and would refuse the page's own files", path)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%q served without nosniff", path)
		}
		if res.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%q served without no-referrer; the token is in the URL", path)
		}
		if path == "vibepanel.js" && string(body) != string(pages.SDK) {
			t.Error("the SDK served is not the binary's")
		}
	}

	for _, path := range []string{"vibepanel.json", "missing.css", "../vibepanel.json", ".git/HEAD"} {
		if res, _ := anonGET(t, ts, "/share/"+link.Token+"/"+path); res.StatusCode == http.StatusOK {
			t.Errorf("%q was served", path)
		}
	}
}

func directive(csp, name string) string {
	for _, part := range strings.Split(csp, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, name+" ") {
			return strings.TrimPrefix(part, name+" ")
		}
	}
	return ""
}

func TestAScriptHostIsNamedOnlyWhenTheManifestAsks(t *testing.T) {
	csp := sharePageCSP("https://panel.example", "tok", nil, false)
	if strings.Contains(csp, "jsdelivr") || strings.Contains(csp, "https:;") || strings.Contains(csp, " https: ") {
		t.Errorf("a page that asked for no host can load from one: %s", csp)
	}
	csp = sharePageCSP("https://panel.example", "tok", []string{"cdn.jsdelivr.net"}, true)
	if !strings.Contains(directive(csp, "script-src"), "https://cdn.jsdelivr.net/") {
		t.Errorf("the allowed host is missing: %s", csp)
	}
	if strings.Contains(directive(csp, "connect-src"), "jsdelivr") {
		t.Error("a script host widened connect-src")
	}
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Error("a preview is not framable by the panel")
	}
	if got := sharePageCSP("", "tok", nil, false); strings.Contains(got, "/share/tok/") {
		t.Errorf("no origin should mean nothing loads, not a relative source: %s", got)
	}
}

// The snapshot carries its own capability. A signed-in session in the same
// browser must not make a bad token good, and the answer must be readable from
// a sandbox without ever being readable with credentials.
func TestTheSnapshotAnswersToTheTokenAlone(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	link := pageLink(t, ts, p.page.ID, "")

	res, err := ts.Client().Get(ts.URL + "/api/share/not-a-real-token-at-all-xxxxx/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a bad token with the owner's cookie = %d, want 401", res.StatusCode)
	}
	// The refusal is readable from a page too. Without this a revoked wall
	// cannot see its own 401, and says "reconnecting" forever.
	if res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("a refused snapshot is not readable from a sandboxed page; a revoked wall cannot say so")
	}

	status, snap, res := snapshotGET(t, ts, link.Token)
	if status != http.StatusOK {
		t.Fatalf("snapshot = %d", status)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("a sandboxed page cannot read its own snapshot without CORS")
	}
	if res.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("the snapshot allows credentials; it must never be readable with a cookie")
	}
	if snap.V != 1 || snap.Page == nil || snap.Page.Version != 1 || snap.Page.Draft {
		t.Errorf("page = %+v, v = %d", snap.Page, snap.V)
	}
	if snap.Page.ID == p.page.ID {
		t.Error("the page's real id was disclosed")
	}
	if !reflect.DeepEqual(snap.Sections, []string{"sessions"}) {
		t.Errorf("sections = %v", snap.Sections)
	}
	if snap.Spend != nil || snap.Repo != nil || snap.Trend != nil {
		t.Error("a section the page did not ask for was sent")
	}
	if snap.Params["title"] != "Lobby" || snap.Params["warnAt"] != 1.0 {
		t.Errorf("params = %v; every declared key, with its default", snap.Params)
	}
}

// Parameters are only echoed. With them changed and nothing else, every other
// key of the snapshot is the same -- which is what keeps "a page can only
// subtract" true with a free-text field in it.
func TestParametersChangeNothingButParameters(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, `{"sdk":1,"name":"x","sections":["sessions","spend"],
		"params":[{"key":"title","type":"text"},{"key":"unit","type":"enum","values":["a","b"]}]}`, nil)
	link := pageLink(t, ts, p.page.ID, `,"params":{"title":"Kitchen","unit":"a"}`)
	row, err := srv.DB.ShareLinkByID(context.Background(), link.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret := auth.HashToken(link.Token)

	a, status, msg := srv.buildShareSnapshot(context.Background(), shareContext{link: row, secret: secret})
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, msg)
	}
	row.Params = map[string]any{"title": "<b>anything at all</b>", "unit": "b", "sections": "spend"}
	srv.snapshots = snapshotMemo{}
	b, status, msg := srv.buildShareSnapshot(context.Background(), shareContext{link: row, secret: secret})
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, msg)
	}
	if a.Params["title"] == b.Params["title"] {
		t.Fatal("the parameters did not change; this test compares nothing")
	}
	if _, stray := b.Params["sections"]; stray {
		t.Error("an undeclared parameter reached the page")
	}
	a.Params, b.Params = nil, nil
	a.At, b.At = 0, 0
	a.Machine, b.Machine = shareMachine{}, shareMachine{}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("changing parameters changed the rest of the snapshot:\n%s\n%s", ja, jb)
	}
}

func TestParameterValuesAreCheckedWhenTheyAreSet(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	link := pageLink(t, ts, p.page.ID, "")
	for _, body := range []string{
		`{"pageId":"` + p.page.ID + `","params":{"warnAt":99}}`,
		`{"pageId":"` + p.page.ID + `","params":{"nope":1}}`,
		`{"pageId":"","params":{"title":"x"}}`,
		`{"pageId":"no-such-page"}`,
		`{"pageId":"` + p.page.ID + `","pinVersion":7}`,
	} {
		if status, out := callJSON(t, ts, http.MethodPut, "/api/settings/shares/"+link.ID+"/page", body); status != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", body, status, out)
		}
	}
	if status, out := callJSON(t, ts, http.MethodPut, "/api/settings/shares/"+link.ID+"/page",
		`{"pageId":"`+p.page.ID+`","params":{"warnAt":4,"title":"Kitchen"}}`); status != http.StatusNoContent {
		t.Fatalf("a valid change = %d %s", status, out)
	}
	time.Sleep(snapshotMemoTTL)
	if _, snap, _ := snapshotGET(t, ts, link.Token); snap.Params["title"] != "Kitchen" {
		t.Errorf("params = %v", snap.Params)
	}
}

// What a viewer sends about itself -- the id, the viewport -- and nothing else
// a page might put on the query string decides what the snapshot carries.
func TestWhatAPageSendsCannotChangeItsSnapshot(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	link := pageLink(t, ts, p.page.ID, "")
	var first []byte
	for i, q := range []string{"", "?v=abc&w=1&h=1", "?sections=spend,repo&detail=names&scope=&days=371&params=x"} {
		time.Sleep(snapshotMemoTTL)
		res, body := anonGET(t, ts, "/api/share/"+link.Token+"/v1/snapshot"+q)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%q = %d", q, res.StatusCode)
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		delete(m, "at")
		delete(m, "machine")
		b, _ := json.Marshal(m)
		if i == 0 {
			first = b
			continue
		}
		if string(b) != string(first) {
			t.Errorf("query %q changed the snapshot:\n%s\n%s", q, first, b)
		}
	}
}

func TestTwentyScreensOnOneLinkCostOneBuild(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	link := pageLink(t, ts, p.page.ID, "")
	row, _ := srv.DB.ShareLinkByID(context.Background(), link.ID)
	key := row.ID + "|v1"

	before := srv.snapshots.builds.Load()
	start := time.Now()
	for i := 0; i < 20; i++ {
		if status, _, _ := snapshotGET(t, ts, link.Token); status != http.StatusOK {
			t.Fatalf("poll %d = %d", i, status)
		}
	}
	builds := srv.snapshots.builds.Load() - before
	// One build per window the polls spanned: twenty polls in well under a
	// second is one, and a slow machine that straddled a boundary is two.
	allowed := int64(time.Since(start)/snapshotMemoTTL) + 1
	if builds > allowed {
		t.Errorf("twenty polls in %v built the snapshot %d times, want at most %d",
			time.Since(start).Round(time.Millisecond), builds, allowed)
	}
	srv.snapshots.mu.Lock()
	_, ok := srv.snapshots.entries[key]
	srv.snapshots.mu.Unlock()
	if !ok {
		t.Fatalf("nothing memoised under %q", key)
	}
	var m snapshotMemo
	at := time.Now()
	m.put("k", at, shareReading{At: 1})
	if _, hit := m.get("k", at.Add(snapshotMemoTTL)); hit {
		t.Error("the memo outlived its window")
	}
	for i := 0; i < snapshotMemoCap+10; i++ {
		m.put(strings.Repeat("x", i+1), at, shareReading{})
	}
	if len(m.entries) > snapshotMemoCap {
		t.Errorf("the memo grew to %d entries", len(m.entries))
	}
}

// An address that draws nothing is answered by a page that says so, and never
// by the panel's own bundle.
//
// `/share/<token>` used to fall through to the SPA for a token that drew a
// board or nothing. With boards gone the SPA has nothing to draw there -- and
// the panel's bundle on an address a stranger holds is its sign-in page. So an
// unknown, revoked or unconverted link gets a static page with no script, and
// the snapshot says 410 for a link that still draws no page.
func TestALinkThatDrawsNothingSaysSoAndIsNeverThePanel(t *testing.T) {
	ts, srv := newTestServer(t)
	_, rootBody := anonGET(t, ts, "/")

	gone := func(t *testing.T, path string) {
		t.Helper()
		res, body := anonGET(t, ts, path)
		csp := res.Header.Get("Content-Security-Policy")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, res.StatusCode)
		}
		if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") ||
			!strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("%s: content type %q, CSP %q; want inert HTML", path,
				res.Header.Get("Content-Type"), csp)
		}
		if len(rootBody) > 0 && string(body) == string(rootBody) {
			t.Errorf("%s answered with the panel's own bundle", path)
		}
		if strings.Contains(string(body), "<script") || !strings.Contains(string(body), "no longer works") {
			t.Errorf("%s is not the page that says the link is gone:\n%s", path, body)
		}
	}

	gone(t, "/share/not-a-real-token")
	gone(t, "/share/not-a-real-token/")
	gone(t, "/share/not-a-real-token/index.html")

	p := newPublishedPage(t, ts, sessionsManifest, nil)
	link := pageLink(t, ts, p.page.ID, "")
	if res, _ := anonGET(t, ts, "/share/"+link.Token+"/"); !strings.Contains(
		res.Header.Get("Content-Security-Policy"), "allow-scripts") {
		t.Fatal("a link that draws a page does not serve it")
	}

	// A page a link draws cannot be deleted out from under the wall.
	if status, _ := callJSON(t, ts, http.MethodDelete, "/api/settings/pages/"+p.page.ID, ""); status != http.StatusConflict {
		t.Errorf("deleting a page a link draws = %d, want 409", status)
	}

	// A link written by a build with boards and not converted: the row is
	// valid, and it draws nothing.
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET page_id = '' WHERE id = ?`, link.ID); err != nil {
		t.Fatal(err)
	}
	gone(t, "/share/"+link.Token+"/")
	if status, _, _ := snapshotGET(t, ts, link.Token); status != http.StatusGone {
		t.Errorf("the snapshot of a link that draws no page = %d, want 410", status)
	}

	// And a revoked one.
	revoked := pageLink(t, ts, p.page.ID, "")
	revokeShare(t, ts, revoked.ID)
	gone(t, "/share/"+revoked.Token+"/")

	entries, err := srv.DB.RecentAudit(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Event == "share.rejected" {
			return
		}
	}
	t.Error("a guessed page address recorded nothing")
}

func TestAPreviewLinkDrawsTheDraftAndIsNotAWall(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	prev := postJSON[struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}](t, ts, "/api/settings/pages/"+p.page.ID+"/preview", `{"detail":"counts"}`)

	// An edit in the directory is on screen at once, without a publish.
	if err := os.WriteFile(filepath.Join(p.dir, "style.css"), []byte("body { color: blue }"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, body := anonGET(t, ts, "/share/"+prev.Token+"/style.css")
	if res.StatusCode != http.StatusOK || string(body) != "body { color: blue }" {
		t.Errorf("preview style.css = %d %q", res.StatusCode, body)
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Error("the Preview pane cannot frame its own preview")
	}
	status, snap, _ := snapshotGET(t, ts, prev.Token)
	if status != http.StatusOK || snap.Page == nil || !snap.Page.Draft || snap.Detail != "counts" {
		t.Errorf("preview snapshot = %d %+v detail %q", status, snap.Page, snap.Detail)
	}

	// A draft file behind a symlink is not served, as publish would refuse it.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(secret, []byte("private"), 0o600)
	_ = os.Symlink(secret, filepath.Join(p.dir, "leak.txt"))
	if res, _ := anonGET(t, ts, "/share/"+prev.Token+"/leak.txt"); res.StatusCode == http.StatusOK {
		t.Error("a symlink out of the draft was served")
	}

	// A draft whose manifest is broken says why, to the pane.
	_ = os.WriteFile(filepath.Join(p.dir, "vibepanel.json"), []byte(`{"sdk":1,"name":"x","sections":["nope"]}`), 0o644)
	time.Sleep(snapshotMemoTTL)
	res, body = anonGET(t, ts, "/api/share/"+prev.Token+"/v1/snapshot")
	if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "unknown section") {
		t.Errorf("a broken draft manifest = %d %s", res.StatusCode, body)
	}

	// Not listed, not editable, and gone when it expires.
	list := getJSON[[]store.ShareLink](t, ts, "/api/settings/shares")
	for _, l := range list {
		if l.ID == prev.ID {
			t.Error("a preview link is in the settings list")
		}
	}
	if status, _ := callJSON(t, ts, http.MethodPut, "/api/settings/shares/"+prev.ID+"/page", `{"pageId":""}`); status != http.StatusNotFound {
		t.Errorf("re-pointing a preview link = %d", status)
	}
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET expires_at = 1 WHERE id = ?`, prev.ID); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := snapshotGET(t, ts, prev.Token); status != http.StatusUnauthorized {
		t.Errorf("an expired preview = %d", status)
	}
	if status, _ := callJSON(t, ts, http.MethodPost, "/api/settings/pages/"+p.page.ID+"/preview/"+prev.ID+"/renew", "{}"); status != http.StatusGone {
		t.Errorf("renewing an expired preview = %d, want 410: a dead token must not come back", status)
	}
}

// A trial ends by itself, decided on read: nothing has to run for the wall to
// come back, and a restart cannot leave one running.
func TestATrialOnAScreenEndsByItself(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	link := pageLink(t, ts, p.page.ID, "")

	_ = os.WriteFile(filepath.Join(p.dir, "style.css"), []byte("body { color: green }"), 0o644)
	trial := postJSON[map[string]any](t, ts, "/api/settings/pages/"+p.page.ID+"/trial",
		`{"linkId":"`+link.ID+`","minutes":5}`)
	cand := int(trial["version"].(float64))
	if cand != 2 {
		t.Fatalf("candidate = %d", cand)
	}
	if _, body := anonGET(t, ts, "/share/"+link.Token+"/style.css"); string(body) != "body { color: green }" {
		t.Errorf("the screen is not showing the trial: %q", body)
	}
	if _, snap, _ := snapshotGET(t, ts, link.Token); snap.Page.Version != cand {
		t.Errorf("snapshot version = %d during the trial", snap.Page.Version)
	}
	pg := getJSON[pageDetail](t, ts, "/api/settings/pages/"+p.page.ID)
	if pg.Page.PublishedVersion != 1 {
		t.Errorf("a trial published the page: %d", pg.Page.PublishedVersion)
	}

	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET pin_until = ? WHERE id = ?`,
		time.Now().Unix()-1, link.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(snapshotMemoTTL)
	if _, body := anonGET(t, ts, "/share/"+link.Token+"/style.css"); string(body) != "body { color: red }" {
		t.Errorf("after the trial the screen shows %q", body)
	}
	if _, snap, _ := snapshotGET(t, ts, link.Token); snap.Page.Version != 1 {
		t.Errorf("after the trial the snapshot is v%d", snap.Page.Version)
	}
	if status, _ := callJSON(t, ts, http.MethodPost, "/api/settings/pages/"+p.page.ID+"/trial/"+link.ID+"/keep", "{}"); status != http.StatusNotFound {
		t.Errorf("keeping a trial that has ended = %d", status)
	}

	// Kept, it is history.
	trial = postJSON[map[string]any](t, ts, "/api/settings/pages/"+p.page.ID+"/trial", `{"linkId":"`+link.ID+`"}`)
	kept := postJSON[map[string]int](t, ts, "/api/settings/pages/"+p.page.ID+"/trial/"+link.ID+"/keep", "{}")
	if kept["version"] != int(trial["version"].(float64)) {
		t.Errorf("kept %v, trial was %v", kept, trial)
	}
	pg = getJSON[pageDetail](t, ts, "/api/settings/pages/"+p.page.ID)
	if pg.Page.PublishedVersion != kept["version"] {
		t.Errorf("published = %d after keeping v%d", pg.Page.PublishedVersion, kept["version"])
	}
	history, _ := os.ReadFile(filepath.Join(p.dir, ".vibepanel", "HISTORY.md"))
	if !strings.Contains(string(history), "v1") || !strings.Contains(string(history), "kept after a trial") {
		t.Errorf("history:\n%s", history)
	}
}

func TestTheErrorsThePaneSawReachTheDraftDirectory(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	var many []map[string]any
	for i := 0; i < maxReportedErrors+5; i++ {
		many = append(many, map[string]any{"kind": "error", "message": strings.Repeat("m", 900), "line": i})
	}
	body, _ := json.Marshal(map[string]any{"errors": many})
	if status, out := callJSON(t, ts, http.MethodPut, "/api/settings/pages/"+p.page.ID+"/errors", string(body)); status != http.StatusNoContent {
		t.Fatalf("errors = %d %s", status, out)
	}
	raw, err := os.ReadFile(filepath.Join(p.dir, ".vibepanel", "errors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Errors []pageError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Errors) != maxReportedErrors || len([]rune(got.Errors[0].Message)) != 500 {
		t.Errorf("%d errors, first %d long; both are bounded", len(got.Errors), len(got.Errors[0].Message))
	}
}

// The fixture a page is tested against looks like what that page would
// really receive: its own sections, the rest null, its parameters filled.
func TestAFixtureIsShapedToThePage(t *testing.T) {
	fx := PageFixtures()
	for _, name := range []string{"busy", "empty", "counts", "hostile", "stale", "revoked"} {
		raw, ok := fx[name]
		if !ok {
			t.Fatalf("no %s fixture", name)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		var f pageFixture
		if err := dec.Decode(&f); err != nil {
			t.Errorf("%s does not decode as a fixture: %v", name, err)
		}
	}
	m, err := pages.ParseManifest([]byte(`{"sdk":1,"name":"x","sections":["repo"],
		"params":[{"key":"title","type":"text","default":"Hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var f pageFixture
	if err := json.Unmarshal(shapeFixture(fx["busy"], m, nil, false), &f); err != nil {
		t.Fatal(err)
	}
	s := f.Snapshot
	if s.Spend != nil || s.Trend != nil || len(s.Sessions) != 0 {
		t.Error("a section the page did not ask for is in its fixture")
	}
	if s.Repo == nil || s.Repo.PRs != nil || len(s.Repo.Days) != 0 {
		t.Errorf("repo options were not applied: %+v", s.Repo)
	}
	if s.Params["title"] != "Hi" {
		t.Errorf("params = %v", s.Params)
	}
	counts := map[string]any{}
	_ = json.Unmarshal(fx["counts"], &counts)
	if strings.Contains(string(fx["counts"]), "billing-api") {
		t.Error("the counts fixture carries a name")
	}
}

func TestTheSDKTypesMatchTheSnapshot(t *testing.T) {
	const path = "../pages/sdk/vibepanel.d.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	for _, tc := range []struct {
		name string
		row  any
	}{
		{"Snapshot", shareSnapshot{}},
		{"SnapshotPage", shareSnapshotPage{}},
		{"Machine", shareMachine{}},
		{"Counts", shareCounts{}},
		{"Project", shareProject{}},
		{"Session", shareSession{}},
		{"SpendTotals", shareSpendTotals{}},
		{"SpendBucket", shareSpendBucket{}},
		{"SpendGroup", shareSpendGroup{}},
		{"Spend", shareSpend{}},
		{"Todos", shareTodos{}},
		{"TodosProject", shareTodosProject{}},
		{"Trend", shareTrend{}},
		{"TrendPoint", shareTrendPoint{}},
		{"FlowTotals", shareFlowTotals{}},
		{"FlowBucket", shareFlowBucket{}},
		{"Flow", shareFlow{}},
		{"FeedEntry", shareFeedEntry{}},
		{"Feed", shareFeed{}},
		{"RepoTotals", shareRepoTotals{}},
		{"RepoDay", shareRepoDay{}},
		{"RepoProject", shareRepoProject{}},
		{"RepoPRs", shareRepoPRs{}},
		{"Repo", shareRepo{}},
		{"SnapshotAction", snapshotAction{}},
		{"SourceResult", sourceResult{}},
		{"DataSpec", pages.DataSpec{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := jsonKeys(t, tc.row)
			declared := interfaceFields(t, string(src), tc.name, path)
			if missing := difference(declared, sent); len(missing) > 0 {
				t.Errorf("vibepanel.d.ts declares %v on %s and the panel does not send them. "+
					"A published page reading them gets undefined.", missing, tc.name)
			}
			if extra := difference(sent, declared); len(extra) > 0 {
				t.Errorf("the panel sends %v on %s and vibepanel.d.ts does not declare them. "+
					"The SDK is a published contract; a field nobody declared is one nobody reviewed.",
					extra, tc.name)
			}
		})
	}
}

func getJSON[T any](t *testing.T, ts *httptest.Server, path string) T {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("GET %s: %s: %s", path, res.Status, b)
	}
	var out T
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}
