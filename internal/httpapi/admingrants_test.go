package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// openAdmin follows the owner's own path into an admin page: the signed-in
// client asks /pages/{id}/admin/, and is sent to the grant's address.
func openAdmin(t *testing.T, ts *httptest.Server, pageID, query string) string {
	t.Helper()
	client := *ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Get(ts.URL + "/pages/" + pageID + "/admin/" + query)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/page-admin/") {
		t.Fatalf("opening the admin page = %d → %q", res.StatusCode, loc)
	}
	return strings.Split(strings.TrimPrefix(loc, "/page-admin/"), "/")[0]
}

func adminCall(t *testing.T, ts *httptest.Server, method, grant, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+"/api/page-admin/"+grant+"/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := anonymousClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

func TestAnAdminPageNeedsTheLoginAndRunsWithoutIt(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)

	// Not signed in: the sign-in page, and no grant.
	res, _ := anonGET(t, ts, "/pages/"+p.page.ID+"/admin/")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("an anonymous open = %d → %q, want the sign-in page", res.StatusCode, res.Header.Get("Location"))
	}

	grant := openAdmin(t, ts, p.page.ID, "")
	res, body := anonGET(t, ts, "/page-admin/"+grant+"/admin/index.html")
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="admin"`) {
		t.Fatalf("the admin page = %d: %s", res.StatusCode, body)
	}
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"sandbox allow-scripts allow-forms", "frame-ancestors 'self'",
		"/api/page-admin/" + grant + "/v1/", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("admin CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Error("an admin page is served with allow-same-origin")
	}
	if res, _ := anonGET(t, ts, "/page-admin/"+grant+"/server.js"); res.StatusCode != http.StatusNotFound {
		t.Errorf("server.js through a grant = %d, want 404", res.StatusCode)
	}
	if res, _ := anonGET(t, ts, "/page-admin/"+grant+"/vibepanel.js"); res.StatusCode != http.StatusOK {
		t.Errorf("the SDK through a grant = %d", res.StatusCode)
	}

	// The admin API: admin data, admin actions, links without tokens.
	code, snap := adminCall(t, ts, http.MethodGet, grant, "/snapshot", "")
	if code != http.StatusOK {
		t.Fatalf("admin snapshot = %d %v", code, snap)
	}
	if _, ok := snap["data"].(map[string]any)["notes"]; !ok {
		t.Error("the admin snapshot has no admin-visibility data")
	}
	if _, ok := snap["actions"].(map[string]any)["announce"]; !ok {
		t.Error("the admin snapshot does not list admin actions")
	}
	if code, out := adminCall(t, ts, http.MethodPut, grant, "/data/notes", `{"value":"secret plans"}`); code != http.StatusOK {
		t.Errorf("admin set = %d %v", code, out)
	}
	if code, out := adminCall(t, ts, http.MethodPost, grant, "/actions/announce", `{"value":"from admin"}`); code != http.StatusOK || out["ok"] != true {
		t.Errorf("admin action = %d %v", code, out)
	}
	if code, out := adminCall(t, ts, http.MethodPost, grant, "/actions/vote", `{}`); code != http.StatusNotFound {
		t.Errorf("a visitor-only action through a grant = %d %v", code, out)
	}
	pageLink(t, ts, p.page.ID, "")
	_, links := adminCall(t, ts, http.MethodGet, grant, "/links", "")
	raw, _ := json.Marshal(links)
	if !strings.Contains(string(raw), `"name":"wall"`) || strings.Contains(string(raw), "token") || strings.Contains(string(raw), "/share/") {
		t.Errorf("links through a grant = %s", raw)
	}
	link := pageLink(t, ts, p.page.ID, "")
	if _, snap, _ := snapshotGET(t, ts, link.Token); snap.Data["announcement"] != "from admin" {
		t.Errorf("an admin action did not reach a screen: %v", snap.Data)
	}

	// A preflight is answered without a credential check.
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/page-admin/"+grant+"/v1/data/notes", nil)
	if res, err := anonymousClient(t).Do(req); err != nil || res.StatusCode != http.StatusNoContent ||
		res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("admin preflight = %v %v", res, err)
	}
}

// A grant dies with the session that minted it.
func TestAnAdminGrantEndsWithItsSession(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	grant := openAdmin(t, ts, p.page.ID, "")
	if code, _ := adminCall(t, ts, http.MethodGet, grant, "/data", ""); code != http.StatusOK {
		t.Fatalf("a fresh grant = %d", code)
	}
	code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/auth/logout", "application/json", []byte(`{}`))
	if code >= 300 {
		t.Fatalf("logout = %d", code)
	}
	if code, _ = adminCall(t, ts, http.MethodGet, grant, "/data", ""); code != http.StatusUnauthorized {
		t.Errorf("a grant after sign-out = %d, want 401", code)
	}
	if res, _ := anonGET(t, ts, "/page-admin/"+grant+"/admin/index.html"); res.StatusCode == http.StatusOK {
		t.Error("the admin page still serves after sign-out")
	}
}

// Each credential is an unknown string everywhere but its own routes.
func TestCredentialsDoNotCrossBetweenSurfaces(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	grant := openAdmin(t, ts, p.page.ID, "")
	link := pageLink(t, ts, p.page.ID, `,"interactive":true`)

	if code, _ := adminCall(t, ts, http.MethodGet, link.Token, "/data", ""); code != http.StatusUnauthorized {
		t.Errorf("a share token on the admin API = %d", code)
	}
	if res, _ := anonGET(t, ts, "/api/share/"+grant+"/v1/snapshot"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a grant on the share API = %d", res.StatusCode)
	}
	for _, how := range []string{"bearer", "cookie"} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/settings/pages/"+p.page.ID+"/data", nil)
		if how == "bearer" {
			req.Header.Set("Authorization", "Bearer "+grant)
		} else {
			req.Header.Set("Cookie", "vibepanel_session="+grant)
		}
		res, err := anonymousClient(t).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("a grant as a %s on a settings route = %d", how, res.StatusCode)
		}
	}
	// And a grant is not minted from an API token: there is no session to end it.
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	tok := postJSON[map[string]any](t, ts, "/api/settings/tokens", `{"name":"script"}`)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/pages/"+p.page.ID+"/admin/", nil)
	req.Header.Set("Authorization", "Bearer "+tok["token"].(string))
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if strings.HasPrefix(res.Header.Get("Location"), "/page-admin/") {
		t.Error("an API token minted an admin grant")
	}
}

func TestAnAdminGrantReachesOnlyTheseRoutes(t *testing.T) {
	_, srv := newTestServer(t)
	want := []string{
		"DELETE /api/page-admin/{grant}/v1/data/{key}",
		"GET /api/page-admin/{grant}/v1/data",
		"GET /api/page-admin/{grant}/v1/links",
		"GET /api/page-admin/{grant}/v1/snapshot",
		"GET /api/page-admin/{grant}/v1/sources",
		"GET /page-admin/{grant}",
		"GET /page-admin/{grant}/*",
		"POST /api/page-admin/{grant}/v1/actions/{name}",
		"POST /api/page-admin/{grant}/v1/data/{key}/append",
		"POST /api/page-admin/{grant}/v1/data/{key}/increment",
		"PUT /api/page-admin/{grant}/v1/data/{key}",
	}
	var got []string
	if err := chi.Walk(srv.Routes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/page-admin/") || strings.HasPrefix(route, "/page-admin/") {
			got = append(got, method+" "+route)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the admin surface is\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestAPageWithNoAdminPageMintsNothing(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	client := *ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Get(ts.URL + "/pages/" + p.page.ID + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("admin for a page without one = %d", res.StatusCode)
	}
}
