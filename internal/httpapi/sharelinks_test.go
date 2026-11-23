package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/secret"
)

// A link's address can be shown again, and a copy of the database alone still
// opens nothing: the sealed token needs the key file beside it.
func TestALinkAddressCanBeCopiedAgainAndIsSealedAtRest(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)

	code, body, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+link.ID+"/url", "", nil)
	if code != http.StatusOK {
		t.Fatalf("url = %d: %s", code, body)
	}
	var got struct{ URL, Token string }
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != link.Token || !strings.HasSuffix(got.URL, "/share/"+link.Token+"/") {
		t.Errorf("url = %+v, want the token the link was made with", got)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodGet, "/api/share/"+got.Token+"/v1/snapshot", "", nil); code != http.StatusOK {
		t.Errorf("the address shown again does not work: %d", code)
	}

	var enc []byte
	if err := srv.DB.SQL().QueryRowContext(context.Background(),
		`SELECT token_enc FROM share_links WHERE id = ?`, link.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || bytes.Contains(enc, []byte(link.Token)) {
		t.Fatalf("token_enc = %q; it must hold the token sealed, never in the clear", enc)
	}
	info, err := os.Stat(filepath.Join(srv.Cfg.DataDir, secret.KeyFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the secrets key is %v, %v; want a 0600 file in the data directory", info, err)
	}

	// Sealed for one row, it does not open for another.
	other := newShare(t, ts, `{"name":"other"}`)
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET token_enc = ? WHERE id = ?`, enc, other.ID); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+other.ID+"/url", "", nil); code != http.StatusConflict ||
		strings.Contains(string(body), link.Token) {
		t.Errorf("a sealed token moved to another row opened there: %d %s", code, body)
	}

	// Nothing the list sends is the token.
	_, list, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares", "", nil)
	if strings.Contains(string(list), link.Token) {
		t.Error("the list carries a token")
	}
}

// A link made before tokens were sealed cannot be shown again, and says what to
// do; rotating gives any link a new address and ends the old one.
func TestALinkWithNoSealedTokenIsRotatedNotRevealed(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"old"}`)
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET token_enc = x'' WHERE id = ?`, link.ID); err != nil {
		t.Fatal(err)
	}
	links := getJSON[[]map[string]any](t, ts, "/api/settings/shares")
	for _, l := range links {
		if l["id"] == link.ID && l["copyable"] != false {
			t.Errorf("an unsealed link is listed as copyable: %v", l)
		}
	}
	code, body, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+link.ID+"/url", "", nil)
	if code != http.StatusConflict || !strings.Contains(string(body), `"rotatable":true`) {
		t.Fatalf("url of an old link = %d %s", code, body)
	}

	code, body, _ = sendRaw(t, ts, http.MethodPost, "/api/settings/shares/"+link.ID+"/rotate", "application/json", nil)
	if code != http.StatusOK {
		t.Fatalf("rotate = %d %s", code, body)
	}
	var fresh struct{ Token string }
	_ = json.Unmarshal(body, &fresh)
	if fresh.Token == "" || fresh.Token == link.Token {
		t.Fatalf("rotate returned %q", fresh.Token)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodGet, "/api/share/"+link.Token+"/v1/snapshot", "", nil); code != http.StatusUnauthorized {
		t.Errorf("the old address still works after rotating: %d", code)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodGet, "/api/share/"+fresh.Token+"/v1/snapshot", "", nil); code != http.StatusOK {
		t.Errorf("the new address does not work: %d", code)
	}
	if code, body, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+link.ID+"/url", "", nil); code != http.StatusOK ||
		!strings.Contains(string(body), fresh.Token) {
		t.Errorf("a rotated link is not copyable: %d %s", code, body)
	}
	if !auditHas(t, srv, "share.rotated", "old") {
		t.Error("rotating left no audit row")
	}
}

// Interactive is the owner's switch, off by default, audited, and fixed by a
// lock like the rest of what a link draws; a View copy never has it.
func TestInteractiveIsAnAuditedOwnerSwitch(t *testing.T) {
	ts, srv := newTestServer(t)
	plain := newShare(t, ts, `{"name":"plain"}`)
	on := newShare(t, ts, `{"name":"kiosk","interactive":true}`)
	byID := map[string]map[string]any{}
	for _, l := range getJSON[[]map[string]any](t, ts, "/api/settings/shares") {
		byID[l["id"].(string)] = l
	}
	if byID[plain.ID]["interactive"] != false || byID[on.ID]["interactive"] != true {
		t.Fatalf("interactive = %v / %v", byID[plain.ID]["interactive"], byID[on.ID]["interactive"])
	}
	if !auditHas(t, srv, "share.interactive_changed", "kiosk on") {
		t.Error("making an interactive link left no audit row")
	}

	patch := func(id, body string) int {
		code, _, _ := sendRaw(t, ts, http.MethodPatch, "/api/settings/shares/"+id, "application/json", []byte(body))
		return code
	}
	if code := patch(plain.ID, `{"name":"plain","remark":"","locked":false,"interactive":true}`); code != http.StatusNoContent {
		t.Fatalf("turning interactive on = %d", code)
	}
	if l, _ := srv.DB.ShareLinkByID(context.Background(), plain.ID); !l.Interactive {
		t.Error("PATCH interactive did not take")
	}
	if code := patch(plain.ID, `{"name":"plain","remark":"","locked":true}`); code != http.StatusNoContent {
		t.Fatal("lock")
	}
	if code := patch(plain.ID, `{"interactive":false}`); code != http.StatusConflict {
		t.Errorf("changing interactive on a locked link = %d, want 409", code)
	}
	if l, _ := srv.DB.ShareLinkByID(context.Background(), plain.ID); !l.Interactive {
		t.Error("a locked link's interactive changed")
	}

	code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/shares/"+on.ID+"/view", "application/json", nil)
	if code != http.StatusCreated {
		t.Fatalf("view = %d %s", code, body)
	}
	var peek struct{ Token string }
	_ = json.Unmarshal(body, &peek)
	var interactive bool
	if err := srv.DB.SQL().QueryRow(`SELECT interactive FROM share_links WHERE purpose = 'peek'`).Scan(&interactive); err != nil {
		t.Fatal(err)
	}
	if interactive {
		t.Error("a View copy of an interactive link is interactive")
	}
}

func TestVisitorWritesIsAPanelWideAuditedSwitch(t *testing.T) {
	ts, srv := newTestServer(t)
	got := getJSON[sharingSettingsResponse](t, ts, "/api/settings/sharing")
	if !got.VisitorWrites || got.ShareOrigin != "" {
		t.Errorf("sharing defaults = %+v, want visitorWrites=true shareOrigin=''", got)
	}
	code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json", []byte(`{"visitorWrites":false}`))
	if code != http.StatusOK || srv.visitorWritesAllowed(context.Background()) {
		t.Errorf("turning visitor writes off = %d, allowed %v", code, srv.visitorWritesAllowed(context.Background()))
	}
	if !auditHas(t, srv, "sharing.visitor_writes", "off") {
		t.Error("the switch left no audit row")
	}
	if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json", []byte(`{}`)); code != http.StatusBadRequest {
		t.Errorf("an empty body = %d, want 400", code)
	}
}

func TestShareOriginValidationAndLockoutGuard(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.PublicOrigins = []string{"https://alias.example.com:8443"}

	bad := []string{
		"share.example.com",
		"ftp://share.example.com",
		"https://share.example.com/subpath",
		"https://share.example.com?a=1",
		"https://share.example.com#frag",
		"https://user:pass@share.example.com",
		"https://share.example.com:",
		"https://share.example.com:99999",
		"https://share.example.com/ extra",
		// Self-lockout attempts: current request host, configured domain, public origin.
		ts.URL,
		"https://panel.example.com",
		"https://panel.example.com:443",
		"https://alias.example.com:8443",
	}
	for _, raw := range bad {
		body, _ := json.Marshal(map[string]string{"shareOrigin": raw})
		code, resp, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json", body)
		if code != http.StatusBadRequest {
			t.Errorf("shareOrigin=%q → %d (%s), want 400", raw, code, resp)
		}
	}

	// Normalization: trailing slash, uppercase, default port stripped, visitorWrites preserved.
	code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json",
		[]byte(`{"shareOrigin":"  HTTPS://Share.Example.COM:443/  "}`))
	if code != http.StatusOK {
		t.Fatalf("set valid shareOrigin = %d: %s", code, body)
	}
	var got sharingSettingsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.ShareOrigin != "https://share.example.com" || !got.VisitorWrites {
		t.Errorf("got = %+v, want https://share.example.com and visitorWrites=true", got)
	}
	if !auditHas(t, srv, "sharing.origin_changed", "https://share.example.com") {
		t.Error("setting shareOrigin left no audit row")
	}

	// Clearing resets to default and audits (default).
	code, body, _ = sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json",
		[]byte(`{"shareOrigin":""}`))
	if code != http.StatusOK {
		t.Fatalf("clear shareOrigin = %d: %s", code, body)
	}
	_ = json.Unmarshal(body, &got)
	if got.ShareOrigin != "" {
		t.Errorf("cleared shareOrigin = %q, want empty", got.ShareOrigin)
	}
	if !auditHas(t, srv, "sharing.origin_changed", "(default)") {
		t.Error("clearing shareOrigin left no audit row")
	}
}

func TestShareOriginRewritesAddressesAndIsolatesShareHost(t *testing.T) {
	ts, srv := newTestServer(t)
	withBundle(t, srv)
	srv.Cfg.Domain = "panel.example.com"

	// Publish a page with a visitor action so we test HTML, SDK, snapshot and action POST.
	manifest := `{"sdk":1,"name":"Board","sections":["sessions"],
		"data":{"votes":{"type":"counter"}},
		"actions":{"vote":{"who":"visitor","label":"Vote","effect":{"increment":"votes"},"rate":"10/min"}}}`
	p := newPublishedPage(t, ts, manifest, nil)

	code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json",
		[]byte(`{"shareOrigin":"https://share.example.com"}`))
	if code != http.StatusOK {
		t.Fatalf("set shareOrigin = %d: %s", code, body)
	}

	// 1. Handed-out create / url / rotate all emit the dedicated share origin.
	created := postJSON[struct {
		ID, Token, URL string
	}](t, ts, "/api/settings/shares", `{"name":"wall","detail":"counts","interactive":true,"pageId":"`+p.page.ID+`"}`)
	wantURL := "https://share.example.com/share/" + created.Token + "/"
	if created.URL != wantURL {
		t.Errorf("create url = %q, want %q", created.URL, wantURL)
	}

	copied := getJSON[struct{ URL, Token string }](t, ts, "/api/settings/shares/"+created.ID+"/url")
	if copied.URL != wantURL {
		t.Errorf("copy url = %q, want %q", copied.URL, wantURL)
	}

	rotated := postJSON[struct{ URL, Token string }](t, ts, "/api/settings/shares/"+created.ID+"/rotate", "")
	wantRotated := "https://share.example.com/share/" + rotated.Token + "/"
	if rotated.URL != wantRotated {
		t.Errorf("rotate url = %q, want %q", rotated.URL, wantRotated)
	}

	// Mint a preview link and a peek link: neither may be served on the share host.
	preview := postJSON[struct{ ID, Token string }](t, ts, "/api/settings/pages/"+p.page.ID+"/preview", `{"detail":"counts"}`)
	peek := postJSON[struct{ Token string }](t, ts, "/api/settings/shares/"+created.ID+"/view", "")

	// Helper sending a request to ts with Host: share.example.com (simulating
	// a TLS-terminating reverse proxy with NO X-Forwarded-Proto / TrustedProxies).
	onShareHost := func(method, reqPath, body string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+reqPath, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "share.example.com"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		client := anonymousClient(t)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s on share host: %v", method, reqPath, err)
		}
		defer res.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(res.Body)
		return res, buf.Bytes()
	}

	// 2. Handed-out share page, SDK, snapshot and visitor action work on the share host,
	//    and CSP uses the configured https:// share origin even over plaintext loopback.
	res, html := onShareHost(http.MethodGet, "/share/"+rotated.Token+"/", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("share page on share host = %d: %s", res.StatusCode, html)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "https://share.example.com/share/"+rotated.Token+"/") ||
		!strings.Contains(csp, "https://share.example.com/api/share/"+rotated.Token+"/v1/") {
		t.Errorf("CSP on share host did not use configured https share origin: %s", csp)
	}
	if !strings.Contains(csp, "sandbox allow-scripts") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP lost sandbox/frame-ancestors on share host: %s", csp)
	}

	if res, _ := onShareHost(http.MethodGet, "/share/"+rotated.Token+"/vibepanel.js", ""); res.StatusCode != http.StatusOK {
		t.Errorf("vibepanel.js on share host = %d, want 200", res.StatusCode)
	}
	if res, _ := onShareHost(http.MethodGet, "/api/share/"+rotated.Token+"/v1/snapshot", ""); res.StatusCode != http.StatusOK {
		t.Errorf("snapshot on share host = %d, want 200", res.StatusCode)
	}
	if res, actBody := onShareHost(http.MethodPost, "/api/share/"+rotated.Token+"/v1/actions/vote", `{}`); res.StatusCode != http.StatusOK || !strings.Contains(string(actBody), `"ok":true`) {
		t.Errorf("visitor action on share host = %d %s, want 200 ok", res.StatusCode, actBody)
	}

	// 3. Preview and peek links are refused on the share host, while still working on the panel host.
	for _, internalTok := range []string{preview.Token, peek.Token} {
		if res, _ := onShareHost(http.MethodGet, "/share/"+internalTok+"/", ""); res.StatusCode != http.StatusNotFound {
			t.Errorf("internal token page on share host = %d, want 404", res.StatusCode)
		}
		if res, _ := onShareHost(http.MethodGet, "/api/share/"+internalTok+"/v1/snapshot", ""); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("internal token snapshot on share host = %d, want 401", res.StatusCode)
		}
		if res, _ := anonGET(t, ts, "/share/"+internalTok+"/"); res.StatusCode != http.StatusOK {
			t.Errorf("internal token page on panel host = %d, want 200", res.StatusCode)
		}
	}

	// 4. Every non-share route on the share host answers 404 and never serves the panel SPA or auth.
	blocked := []struct {
		method, path string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/sharing"},
		{http.MethodGet, "/api/health"},
		{http.MethodGet, "/api/auth/status"},
		{http.MethodPost, "/api/auth/login"},
		{http.MethodGet, "/api/state"},
		{http.MethodGet, "/api/settings/sharing"},
		{http.MethodGet, "/ws"},
		{http.MethodGet, "/preview/anything/"},
		{http.MethodGet, "/pages/" + p.page.ID + "/admin/"},
		{http.MethodGet, "/page-admin/anything/"},
		{http.MethodGet, "/share/"},
		{http.MethodGet, "/share/../api/health"},
	}
	for _, tc := range blocked {
		res, b := onShareHost(tc.method, tc.path, "")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s on share host = %d (%s), want 404", tc.method, tc.path, res.StatusCode, b)
		}
		if strings.Contains(string(b), "<div id=\"root\">") {
			t.Errorf("%s %s on share host served the panel SPA bundle", tc.method, tc.path)
		}
	}

	// 5. Panel host remains fully usable.
	if res, _ := anonGET(t, ts, "/api/health"); res.StatusCode != http.StatusOK {
		t.Errorf("panel /api/health = %d, want 200", res.StatusCode)
	}

	// 6. Runtime lockout guard: if Cfg.Domain is later changed to match the
	//    stored share origin, the panel refuses to lock itself out.
	srv.Cfg.Domain = "share.example.com"
	if res, _ := onShareHost(http.MethodGet, "/api/health", ""); res.StatusCode != http.StatusOK {
		t.Errorf("panel host matching stored share origin locked out /api/health: %d", res.StatusCode)
	}
}
