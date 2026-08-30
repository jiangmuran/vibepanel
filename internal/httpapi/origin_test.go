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

// Behind a TLS-terminating proxy, a write must not be refused for the scheme.
//
// This is the shape that broke a real deployment: nginx terminates TLS and
// forwards to the panel over plaintext loopback, so the browser sends
// `Origin: https://panel.example.com` while the panel sees a plain HTTP
// request. Comparing the two literally makes every POST a cross-origin write,
// and the panel answers 403 to everything that changes anything -- 「无法创建
// 底部终端」, with a bare 403 in the console and nothing saying why.
//
// The panel cannot infer its own public address behind an arbitrary proxy, so
// it is told: the configured domain is trusted as an origin, and a proxy the
// operator has listed may say so with X-Forwarded-Proto.
func TestAWriteThroughATlsTerminatingProxyIsAllowed(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"

	body := `{"name":"through the proxy","path":"/tmp"}`

	// What nginx sends: the public Host, and the browser's https Origin.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects", strings.NewReader(body))
	req.Host = "panel.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://panel.example.com")
	req.AddCookie(&http.Cookie{Name: "vibepanel_session", Value: sessionToken(t, ts)})
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusForbidden {
		b, _ := io.ReadAll(res.Body)
		t.Errorf("a write through a TLS-terminating proxy: 403 %s", strings.TrimSpace(string(b)))
	}

	// And a page on another host is still refused. The point of the above is
	// to trust one more origin, not to stop checking.
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects", strings.NewReader(body))
	req2.Host = "panel.example.com"
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", "https://panel.example.com.evil.test")
	req2.AddCookie(&http.Cookie{Name: "vibepanel_session", Value: sessionToken(t, ts)})
	res2, err := ts.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusForbidden {
		t.Errorf("a lookalike host was accepted: %d", res2.StatusCode)
	}
}

// The writes that recover a misconfiguration must not be the ones it blocks.
//
// This is the failure that made the origin check worth rewriting rather than
// tuning. Behind nginx every write was refused, and three of the things a
// person then tried are themselves writes: saving the settings that would fix
// it, marking the first-run tour as read, and opening a terminal. So the panel
// re-showed the wizard on every refresh, refused to save the setting named in
// its own error message, and could not create a session -- with a bare 403 in
// the console.
//
// A control that locks the operator out of the setting that would fix it is
// not a control.
func TestThePanelCanStillBeConfiguredThroughAProxy(t *testing.T) {
	ts, _ := newTestServer(t)
	token := sessionToken(t, ts)

	// Exactly what nginx sends: the public Host, and the browser's https
	// Origin for the same name. No VIBEPANEL_DOMAIN, no VIBEPANEL_PUBLIC_ORIGINS
	// -- an operator who has not configured anything yet is the whole point.
	proxied := func(method, path, body string) *http.Response {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, ts.URL+path, r)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "panel.example.com"
		req.Header.Set("Origin", "https://panel.example.com")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "vibepanel_session", Value: token})
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	for _, c := range []struct {
		what, method, path, body string
	}{
		{"mark the tour as read", http.MethodPost, "/api/settings/tour", ""},
		{"save the settings", http.MethodPut, "/api/settings/env", `{"values":{"VIBEPANEL_DOMAIN":"panel.example.com"}}`},
		{"create a session", http.MethodPost, "/api/sessions", `{"projectId":"nope"}`},
	} {
		res := proxied(c.method, c.path, c.body)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		// Not asserting success -- the session create has no project and the
		// env write may have nowhere to write. Asserting that the *origin* is
		// not what stopped it.
		if res.StatusCode == http.StatusForbidden {
			t.Errorf("%s through a proxy: 403 %s", c.what, strings.TrimSpace(string(b)))
		}
	}
}
