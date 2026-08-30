package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/auth"
)

// sessionToken digs the signed-in cookie out of the helper's jar.
func sessionToken(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range ts.Client().Jar.Cookies(u) {
		if c.Name == "vibepanel_session" {
			return c.Value
		}
	}
	t.Fatal("no session cookie; the helper's sign-in has changed shape")
	return ""
}

// asHost sends one request with the session cookie and a Host of its own.
//
// The cookie is attached by hand rather than left to the client\'s jar, and
// that is not tidiness. With `req.Host` overridden the jar attaches nothing, so
// every request in this file arrived unauthenticated and every assertion for a
// 401 passed -- including with the binding deleted. The tests were green and
// checked nothing, which mutation testing is the only reason anybody found out.
func asHost(t *testing.T, ts *httptest.Server, method, host, path, body string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.AddCookie(&http.Cookie{Name: "vibepanel_session", Value: sessionToken(t, ts)})
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A sign-in belongs to the address it was made at.
//
// Cookies are not scoped by port, and for SameSite a different port on the
// same host is the same site. So before the binding, a session issued by the
// panel on :18443 was sent by the browser to everything else on the machine,
// and a page served by anything else on the machine could drive the panel as
// the signed-in user. The capability at the end of that is a shell, which is
// why this is worth a login per address.
func TestASessionDoesNotFollowTheCookieToAnotherAddress(t *testing.T) {
	ts, _ := newTestServer(t)
	self := strings.TrimPrefix(ts.URL, "http://")

	// At the address the sign-in was made at.
	res := asHost(t, ts, http.MethodGet, self, "/api/state", "")
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("at the address it was issued at: got %d, want 200", res.StatusCode)
	}

	// The same cookie and the same listener, under another name. This is what
	// the browser sends when the panel is reached at a second port or address.
	res2 := asHost(t, ts, http.MethodGet, "panel.example.com:9999", "/api/state", "")
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Errorf("at a different address: got %d, want 401 -- the session followed the cookie", res2.StatusCode)
	}

	// And it still works where it belongs: a mismatch must not revoke it.
	res3 := asHost(t, ts, http.MethodGet, self, "/api/state", "")
	res3.Body.Close()
	if res3.StatusCode != http.StatusOK {
		t.Errorf("back at its own address: got %d, want 200 -- a wrong Host revoked the session", res3.StatusCode)
	}
}

// A session created before the column existed is bound where it is first
// presented, rather than refused.
//
// Rejecting them would sign everybody out on upgrade to close a hole they were
// not being attacked through, and a release that logs the whole userbase out
// is a release that gets rolled back.
func TestASessionFromBeforeTheBindingIsBoundOnFirstUse(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := t.Context()

	// Put the row back the way an upgraded database has it.
	r, err := srv.DB.SQL().ExecContext(ctx,
		`UPDATE auth_sessions SET origin = '' WHERE token_hash = ?`,
		auth.HashToken(sessionToken(t, ts)))
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := r.RowsAffected(); n != 1 {
		t.Fatalf("unbinding the row affected %d rows, want 1", n)
	}

	// First use binds it, under a name that is not the one the sign-in
	// happened at -- which is the point: there is nothing to compare with yet.
	res := asHost(t, ts, http.MethodGet, "first.example.com", "/api/state", "")
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an unbound session: got %d, want 200", res.StatusCode)
	}
	var origin string
	if err := srv.DB.SQL().QueryRowContext(ctx, `SELECT origin FROM auth_sessions`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "http://first.example.com" {
		t.Errorf("after first use the row says %q, want %q", origin, "http://first.example.com")
	}

	// And from now on it is bound to that one.
	res2 := asHost(t, ts, http.MethodGet, "second.example.com", "/api/state", "")
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Errorf("after binding, a second address: got %d, want 401", res2.StatusCode)
	}
}

// A write whose Origin is another page is refused before it reaches a handler.
//
// This is the half SameSite=Strict does not cover. `http://host:7681` posting
// to `https://host:18443` is same-site by the cookie's rules, so the session
// rides along; the Origin header is the only thing that says where it really
// came from.
func TestAWriteFromAnotherOriginIsRefused(t *testing.T) {
	ts, _ := newTestServer(t)

	body := `{"name":"from a page next door","path":"/tmp"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1:7681")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(res.Body)
		t.Errorf("a cross-origin POST: got %d, want 403 (%s)", res.StatusCode, strings.TrimSpace(string(b)))
	}

	// The panel's own page still works, and so does a client that sends no
	// Origin at all -- curl and the CLI, which is what the bearer token is for.
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", ts.URL)
	res2, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode == http.StatusForbidden {
		t.Errorf("the panel's own page was refused as cross-origin")
	}

	// A read is never refused for its Origin. Navigations carry one too, and
	// a GET cannot change anything.
	req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req3.Header.Set("Origin", "http://127.0.0.1:7681")
	res3, err := ts.Client().Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusOK {
		t.Errorf("a cross-origin GET: got %d, want 200", res3.StatusCode)
	}
}
