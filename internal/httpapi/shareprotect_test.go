package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/store"
)

var iframeGrantRe = regexp.MustCompile(`src="/share-u/([A-Za-z0-9_-]+)/"`)

func TestAnUnlockGrantReachesOnlyTheseRoutes(t *testing.T) {
	_, srv := newTestServer(t)
	want := []string{
		"GET /api/share-u/{grant}/v1/snapshot",
		"GET /share-u/{grant}",
		"GET /share-u/{grant}/*",
		"OPTIONS /api/share-u/{grant}/v1/actions/{name}",
		"POST /api/share-u/{grant}/v1/actions/{name}",
	}
	var got []string
	err := chi.Walk(srv.Routes().(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if strings.HasPrefix(route, "/api/share-u/") || strings.HasPrefix(route, "/share-u/") {
				got = append(got, method+" "+route)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the unlock grant surface is\n  %s\nwant\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func extractGrant(t *testing.T, htmlBody string) string {
	t.Helper()
	m := iframeGrantRe.FindStringSubmatch(htmlBody)
	if len(m) != 2 {
		t.Fatalf("expected unlock shell iframe grant in body, got:\n%s", htmlBody)
	}
	return m[1]
}

func newVisitorClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func doVisitor(t *testing.T, c *http.Client, method, rawURL, body, ua, xff, host string) (int, string, http.Header) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost && body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if host != "" {
		req.Host = host
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

func TestShareSharedAndCustomPasswordLifecycle(t *testing.T) {
	ts, srv := newTestServer(t)
	nets, err := auth.ParseCIDRs([]string{"127.0.0.1/32", "::1/128"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth.TrustedProxies = nets
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)

	// 1. Before setting a global shared password, a new link defaults to public ("none").
	pubLink := pageLink(t, ts, p.page.ID, `,"interactive":true`)
	if code, body, _ := doVisitor(t, newVisitorClient(t), http.MethodGet, ts.URL+"/share/"+pubLink.Token+"/", "",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/130.0.0.0 Safari/537.36", "203.0.113.5", ""); code != http.StatusOK || !strings.Contains(body, "vibepanel.js") {
		t.Fatalf("public link GET = %d: %s", code, body)
	}

	// 2. Reject short shared password (<6 chars).
	if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing/password", "application/json", []byte(`{"password":"12345"}`)); code != http.StatusBadRequest {
		t.Fatalf("short shared password = %d, want 400", code)
	}

	// 3. Set global shared password and verify it can be viewed/copied by owner.
	code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing/password", "application/json", []byte(`{"password":"team-secret-1"}`))
	if code != http.StatusOK {
		t.Fatalf("set shared password = %d: %s", code, body)
	}
	var st sharingSettingsResponse
	if err := json.Unmarshal(body, &st); err != nil || !st.SharedPasswordSet || !st.SharedPasswordViewable || st.PublicLinkCount != 1 {
		t.Fatalf("unexpected sharing settings: %+v err=%v", st, err)
	}
	code, pwBody, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/sharing/password", "", nil)
	if code != http.StatusOK || !strings.Contains(string(pwBody), `"team-secret-1"`) {
		t.Fatalf("view shared password = %d: %s", code, pwBody)
	}

	// 4. New links now automatically default to "shared".
	sharedA := pageLink(t, ts, p.page.ID, `,"interactive":true`)
	sharedB := pageLink(t, ts, p.page.ID, ``)
	// And a custom-password link.
	customC := pageLink(t, ts, p.page.ID, `,"accessMode":"custom","password":"only-for-c"`)

	// Custom password viewable by owner.
	code, cPwBody, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+customC.ID+"/password", "", nil)
	if code != http.StatusOK || !strings.Contains(string(cPwBody), `"only-for-c"`) {
		t.Fatalf("view custom password = %d: %s", code, cPwBody)
	}

	// 5. Unauthenticated requests to protected links are gated on HTML, assets, snapshot, and actions.
	visitor := newVisitorClient(t)
	code, gateBody, gateHdr := doVisitor(t, visitor, http.MethodGet, ts.URL+"/share/"+sharedA.Token+"/", "",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) MicroMessenger/8.0.0", "198.51.100.7", "")
	if code != http.StatusOK || !strings.Contains(gateBody, `action="unlock"`) || strings.Contains(gateBody, "vibepanel.js") {
		t.Fatalf("expected password gate, got %d:\n%s", code, gateBody)
	}
	if !strings.Contains(gateHdr.Get("Content-Security-Policy"), "form-action 'self'") {
		t.Errorf("gate CSP = %q", gateHdr.Get("Content-Security-Policy"))
	}
	if code, _, _ := doVisitor(t, visitor, http.MethodGet, ts.URL+"/share/"+sharedA.Token+"/vibepanel.js", "", "", "198.51.100.7", ""); code != http.StatusUnauthorized {
		t.Fatalf("direct asset on protected link = %d, want 401", code)
	}
	if code, _, _ := doVisitor(t, visitor, http.MethodGet, ts.URL+"/api/share/"+sharedA.Token+"/v1/snapshot", "", "", "198.51.100.7", ""); code != http.StatusUnauthorized {
		t.Fatalf("direct snapshot on protected link = %d, want 401", code)
	}

	// 6. Wrong password fails with 401 and records failure.
	code, wrongBody, _ := doVisitor(t, visitor, http.MethodPost, ts.URL+"/share/"+sharedA.Token+"/unlock",
		url.Values{"password": {"wrong-pw"}}.Encode(),
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) MicroMessenger/8.0.0", "198.51.100.7", "")
	if code != http.StatusUnauthorized || !strings.Contains(wrongBody, "Incorrect password") {
		t.Fatalf("wrong password = %d: %s", code, wrongBody)
	}

	// Immediate second try is throttled (429).
	code, _, thHdr := doVisitor(t, visitor, http.MethodPost, ts.URL+"/share/"+sharedA.Token+"/unlock",
		url.Values{"password": {"wrong-pw"}}.Encode(), "", "198.51.100.7", "")
	if code != http.StatusTooManyRequests || thHdr.Get("Retry-After") == "" {
		t.Fatalf("expected 429 throttle on immediate retry, got %d Retry-After=%q", code, thHdr.Get("Retry-After"))
	}

	// 7. Correct shared password from a fresh IP unlocks sharedA AND sharedB (shared scope),
	//    but NOT customC.
	visitor2 := newVisitorClient(t)
	code, _, hdr := doVisitor(t, visitor2, http.MethodPost, ts.URL+"/share/"+sharedA.Token+"/unlock",
		url.Values{"password": {"team-secret-1"}}.Encode(),
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile Safari/604.1",
		"198.51.100.42", "")
	if code != http.StatusSeeOther || hdr.Get("Location") != "/share/"+sharedA.Token+"/" {
		t.Fatalf("unlock sharedA = %d -> %q", code, hdr.Get("Location"))
	}
	setCookie := hdr.Get("Set-Cookie")
	if !strings.Contains(setCookie, "HttpOnly") || !strings.Contains(setCookie, "Path=/share/") {
		t.Fatalf("shared cookie flags wrong: %q", setCookie)
	}

	// Opening sharedA returns the outer shell with an iframe grant.
	code, shellA, _ := doVisitor(t, visitor2, http.MethodGet, ts.URL+"/share/"+sharedA.Token+"/", "",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile Safari/604.1",
		"198.51.100.42", "")
	if code != http.StatusOK {
		t.Fatalf("unlocked shellA = %d: %s", code, shellA)
	}
	grantA := extractGrant(t, shellA)

	// Opening sharedB with the SAME visitor2 cookie succeeds WITHOUT re-entering password!
	code, shellB, _ := doVisitor(t, visitor2, http.MethodGet, ts.URL+"/share/"+sharedB.Token+"/", "", "", "198.51.100.42", "")
	if code != http.StatusOK {
		t.Fatalf("sharedB with shared cookie = %d: %s", code, shellB)
	}
	_ = extractGrant(t, shellB)

	// Opening customC with visitor2's shared cookie STILL shows the password gate!
	code, gateC, _ := doVisitor(t, visitor2, http.MethodGet, ts.URL+"/share/"+customC.Token+"/", "", "", "198.51.100.42", "")
	if code != http.StatusOK || !strings.Contains(gateC, `action="unlock"`) {
		t.Fatalf("customC must not unlock with shared cookie: %d %s", code, gateC)
	}

	// 8. Inner iframe serves sandboxed page files + live snapshot + visitor actions via grantA.
	anon := anonymousClient(t)
	safariUA := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile Safari/604.1"
	code, frameHTML, frameHdr := doVisitor(t, anon, http.MethodGet, ts.URL+"/share-u/"+grantA+"/", "", safariUA, "198.51.100.42", "")
	if code != http.StatusOK || !strings.Contains(frameHTML, "vibepanel.js") {
		t.Fatalf("grantA index = %d: %s", code, frameHTML)
	}
	csp := frameHdr.Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") || !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("grantA CSP invalid: %q", csp)
	}
	if code, _, _ := doVisitor(t, anon, http.MethodGet, ts.URL+"/api/share-u/"+grantA+"/v1/snapshot", "", safariUA, "198.51.100.42", ""); code != http.StatusOK {
		t.Fatalf("grantA snapshot = %d, want 200", code)
	}
	reqAct, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/share-u/"+grantA+"/v1/actions/vote", strings.NewReader(`{}`))
	reqAct.Header.Set("Content-Type", "application/json")
	reqAct.Header.Set("User-Agent", safariUA)
	reqAct.Header.Set("X-Forwarded-For", "198.51.100.42")
	resAct, err := anon.Do(reqAct)
	if err != nil {
		t.Fatal(err)
	}
	resAct.Body.Close()
	if resAct.StatusCode != http.StatusOK {
		t.Fatalf("grantA visitor action = %d, want 200", resAct.StatusCode)
	}

	// 9. Cross-surface isolation: grantA cannot hit panel API, admin API, or /api/share/.
	for _, probe := range []struct {
		url    string
		header string
		val    string
	}{
		{ts.URL + "/api/settings/shares", "Authorization", "Bearer " + grantA},
		{ts.URL + "/api/settings/shares", "Cookie", "vibepanel_session=" + grantA},
		{ts.URL + "/api/share/" + grantA + "/v1/snapshot", "", ""},
		{ts.URL + "/api/page-admin/" + grantA + "/v1/snapshot", "", ""},
	} {
		req, _ := http.NewRequest(http.MethodGet, probe.url, nil)
		if probe.header != "" {
			req.Header.Set(probe.header, probe.val)
		}
		res, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("grantA on %s (%s) = %d, want 401", probe.url, probe.header, res.StatusCode)
		}
	}

	// 10. Changing global shared password IMMEDIATELY invalidates visitor2's cookie AND grantA!
	code, _, _ = sendRaw(t, ts, http.MethodPut, "/api/settings/sharing/password", "application/json", []byte(`{"password":"team-secret-2"}`))
	if code != http.StatusOK {
		t.Fatalf("rotate shared password = %d", code)
	}
	if code, _, _ := doVisitor(t, anon, http.MethodGet, ts.URL+"/api/share-u/"+grantA+"/v1/snapshot", "", "", "198.51.100.42", ""); code != http.StatusUnauthorized {
		t.Fatalf("grantA still worked after shared password change: %d", code)
	}
	if code, body, _ := doVisitor(t, visitor2, http.MethodGet, ts.URL+"/share/"+sharedA.Token+"/", "", "", "198.51.100.42", ""); code != http.StatusOK || !strings.Contains(body, `action="unlock"`) {
		t.Fatalf("visitor2 cookie still worked after shared password change: %d %s", code, body)
	}

	// 11. Owner peek link STILL bypasses password gate.
	peek := postJSON[struct {
		Token string `json:"token"`
	}](t, ts, "/api/settings/shares/"+sharedA.ID+"/view", `{}`)
	if code, body, _ := doVisitor(t, newVisitorClient(t), http.MethodGet, ts.URL+"/share/"+peek.Token+"/", "", "", "", ""); code != http.StatusOK || !strings.Contains(body, "vibepanel.js") {
		t.Fatalf("owner peek of protected link = %d: %s", code, body)
	}

	// 12. Batch apply shared password protects existing pubLink (1 updated) and leaves customC custom.
	applied := postJSON[struct {
		Updated int64 `json:"updated"`
	}](t, ts, "/api/settings/sharing/password/apply-all", `{}`)
	if applied.Updated != 1 {
		t.Fatalf("apply-all updated = %d, want 1", applied.Updated)
	}
	if code, body, _ := doVisitor(t, newVisitorClient(t), http.MethodGet, ts.URL+"/share/"+pubLink.Token+"/", "", "", "", ""); code != http.StatusOK || !strings.Contains(body, `action="unlock"`) {
		t.Fatalf("pubLink not gated after apply-all: %d %s", code, body)
	}

	// 13. Clearing shared password is refused while links still use it.
	if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing/password", "application/json", []byte(`{"password":""}`)); code != http.StatusConflict {
		t.Fatalf("clearing shared password while in use = %d, want 409", code)
	}

	// 14. Verify per-IP access stats recorded real client IPs, UA summaries, visits, unlocks, fails, actions.
	code, statsRaw, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+sharedA.ID+"/access-stats", "", nil)
	if code != http.StatusOK {
		t.Fatalf("access-stats = %d: %s", code, statsRaw)
	}
	var stats shareAccessStatsResponse
	if err := json.Unmarshal(statsRaw, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.UniqueIPs != 2 || stats.TotalVisits != 1 || stats.TotalUnlockOK != 1 || stats.TotalUnlockFail != 1 || stats.LoopbackOnly {
		t.Fatalf("unexpected sharedA stats: %+v", stats)
	}
	uaByIP := map[string]string{}
	for _, r := range stats.IPs {
		uaByIP[r.IP] = r.LastUA
	}
	if uaByIP["198.51.100.7"] != "WeChat · iOS" || uaByIP["198.51.100.42"] != "Safari · iOS" {
		t.Fatalf("unexpected UA summaries: %+v", uaByIP)
	}

	// 15. Dedicated share origin host isolation allows unlock & /share-u/ and refuses arbitrary POSTs
	// (including trailing-slash and dot-segment unlock paths that would miss chi and hit the SPA).
	srv.Cfg.Domain = "panel.example.com"
	withBundle(t, srv)
	setShareOrigin(t, ts, "https://share.example.com")
	for _, badPath := range []string{
		"/share/" + sharedA.Token + "/not-unlock",
		"/share/" + sharedA.Token + "/unlock/",
		"/share/x/../" + sharedA.Token + "/unlock",
	} {
		c, b, _ := requestOnHost(t, ts, "share.example.com", http.MethodPost, badPath)
		if c != http.StatusNotFound || strings.Contains(b, "root") {
			t.Errorf("POST %s on share host = %d (want 404, never SPA): %.120s", badPath, c, b)
		}
	}
	visitorHost := newVisitorClient(t)
	code, _, hdr = doVisitor(t, visitorHost, http.MethodPost, ts.URL+"/share/"+sharedA.Token+"/unlock",
		url.Values{"password": {"team-secret-2"}}.Encode(), "", "198.51.100.99", "share.example.com")
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Set-Cookie"), "Secure") {
		t.Fatalf("unlock on https share host = %d cookie=%q", code, hdr.Get("Set-Cookie"))
	}
	sharedCookieHdr := hdr.Get("Set-Cookie")

	// Per-link revoke on a shared-mode link forces existing vp_share_shared holders to re-enter the password on that link.
	visitorRev := newVisitorClient(t)
	code, _, _ = doVisitor(t, visitorRev, http.MethodPost, ts.URL+"/share/"+sharedA.Token+"/unlock",
		url.Values{"password": {"team-secret-2"}}.Encode(), "", "198.51.100.101", "")
	if code != http.StatusSeeOther {
		t.Fatalf("unlock sharedA on http = %d", code)
	}
	if code, body, _ := doVisitor(t, visitorRev, http.MethodGet, ts.URL+"/share/"+sharedA.Token+"/", "", "", "198.51.100.101", ""); code != http.StatusOK || !strings.Contains(body, "/share-u/") {
		t.Fatalf("sharedA should be unlocked before per-link revoke: %d %.120s", code, body)
	}
	time.Sleep(1100 * time.Millisecond)
	if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/shares/"+sharedA.ID+"/access", "application/json", []byte(`{"revokeSessions":true}`)); code != http.StatusNoContent {
		t.Fatalf("per-link revoke on sharedA = %d, want 204", code)
	}
	if code, body, _ := doVisitor(t, visitorRev, http.MethodGet, ts.URL+"/share/"+sharedA.Token+"/", "", "", "198.51.100.101", ""); code != http.StatusOK || !strings.Contains(body, `action="unlock"`) {
		t.Fatalf("sharedA remained unlocked for old vp_share_shared after per-link revoke: %d %.120s", code, body)
	}
	if code, body, _ := doVisitor(t, visitorRev, http.MethodGet, ts.URL+"/share/"+sharedB.Token+"/", "", "", "198.51.100.101", ""); code != http.StatusOK || !strings.Contains(body, "/share-u/") {
		t.Fatalf("sharedB should stay unlocked after revoking only sharedA: %d %.120s", code, body)
	}

	// IPv6 /64 subnet rotation shares one throttle bucket.
	var saw429 bool
	for i := 1; i <= 6; i++ {
		v6 := fmt.Sprintf("2001:db8:abcd:1234::%d", i)
		c, _, _ := doVisitor(t, newVisitorClient(t), http.MethodPost, ts.URL+"/share/"+sharedA.Token+"/unlock",
			url.Values{"password": {"wrong-v6-pw"}}.Encode(), "", v6, "")
		if c == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Fatal("rotating addresses inside one IPv6 /64 bypassed unlockThrottle")
	}

	// Locked links refuse access-mode mutations.
	if code, _, _ := sendRaw(t, ts, http.MethodPatch, "/api/settings/shares/"+sharedB.ID, "application/json", []byte(`{"locked":true}`)); code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("lock sharedB = %d", code)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/shares/"+sharedB.ID+"/access", "application/json", []byte(`{"accessMode":"none"}`)); code != http.StatusConflict {
		t.Fatalf("changing accessMode on locked link = %d, want 409", code)
	}
	_, _, _ = sendRaw(t, ts, http.MethodPatch, "/api/settings/shares/"+sharedB.ID, "application/json", []byte(`{"locked":false}`))

	// 16. CLI EnsurePageShareLink automatically uses "shared" when a global shared password is set,
	// and mints a fresh link rather than mutating an existing one when given a custom password.
	pageRow, err := srv.DB.SharePageByID(context.Background(), p.page.ID)
	if err != nil {
		t.Fatal(err)
	}
	cliRes, err := EnsurePageShareLink(context.Background(), srv.DB, srv.Cfg, pageRow, ShareLinkOptions{ForceNew: true})
	if err != nil {
		t.Fatal(err)
	}
	if cliRes.AccessMode != string(store.ShareAccessShared) {
		t.Fatalf("CLI minted link accessMode = %q, want shared", cliRes.AccessMode)
	}
	cliCustom, err := EnsurePageShareLink(context.Background(), srv.DB, srv.Cfg, pageRow, ShareLinkOptions{
		ForceNew:       true,
		AccessMode:     store.ShareAccessCustom,
		RandomPassword: true,
	})
	if err != nil {
		t.Fatalf("CLI random custom link: %v", err)
	}
	if cliCustom.AccessMode != string(store.ShareAccessCustom) || len(cliCustom.GeneratedPassword) < minSharePasswordLen {
		t.Fatalf("CLI random custom link unexpected result: %+v", cliCustom)
	}
	cliTok := strings.Trim(strings.TrimPrefix(cliCustom.URL, strings.Split(cliCustom.URL, "/share/")[0]+"/share/"), "/")
	code, _, _ = doVisitor(t, newVisitorClient(t), http.MethodPost, ts.URL+"/share/"+cliTok+"/unlock",
		url.Values{"password": {cliCustom.GeneratedPassword}}.Encode(), "", "198.51.100.77", "share.example.com")
	if code != http.StatusSeeOther {
		t.Fatalf("unlock CLI random custom link = %d, want 303", code)
	}
	// Passing CustomPassword mints a fresh link without overwriting cliCustom's password.
	cliFreshCustom, err := EnsurePageShareLink(context.Background(), srv.DB, srv.Cfg, pageRow, ShareLinkOptions{
		AccessMode:     store.ShareAccessCustom,
		CustomPassword: "cli-chosen-pw",
	})
	if err != nil {
		t.Fatalf("CLI custom password: %v", err)
	}
	if cliFreshCustom.Reused || cliFreshCustom.ID == cliCustom.ID || cliFreshCustom.AccessMode != string(store.ShareAccessCustom) {
		t.Fatalf("CLI custom password mutated an existing link instead of minting a fresh one: %+v", cliFreshCustom)
	}
	freshTok := strings.Trim(strings.TrimPrefix(cliFreshCustom.URL, strings.Split(cliFreshCustom.URL, "/share/")[0]+"/share/"), "/")
	code, _, _ = doVisitor(t, newVisitorClient(t), http.MethodPost, ts.URL+"/share/"+freshTok+"/unlock",
		url.Values{"password": {"cli-chosen-pw"}}.Encode(), "", "198.51.100.78", "share.example.com")
	if code != http.StatusSeeOther {
		t.Fatalf("unlock CLI fresh custom link = %d, want 303", code)
	}
	// And cliCustom still opens with its own generated password.
	code, _, _ = doVisitor(t, newVisitorClient(t), http.MethodPost, ts.URL+"/share/"+cliTok+"/unlock",
		url.Values{"password": {cliCustom.GeneratedPassword}}.Encode(), "", "198.51.100.79", "share.example.com")
	if code != http.StatusSeeOther {
		t.Fatalf("cliCustom lost its password when a second custom link was minted: %d", code)
	}

	// 17. Custom password cookie isolation, cross-surface rejection, and rotation invalidation.
	visitorC := newVisitorClient(t)
	code, _, hdr = doVisitor(t, visitorC, http.MethodPost, ts.URL+"/share/"+customC.Token+"/unlock",
		url.Values{"password": {"only-for-c"}}.Encode(), "", "198.51.100.55", "")
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Set-Cookie"), "Path=/share/"+customC.Token+"/") {
		t.Fatalf("customC unlock = %d cookie=%q", code, hdr.Get("Set-Cookie"))
	}
	linkCookieHdr := hdr.Get("Set-Cookie")
	for _, rawCookie := range []string{sharedCookieHdr, linkCookieHdr} {
		kv := strings.SplitN(rawCookie, ";", 2)[0]
		_, val, _ := strings.Cut(kv, "=")
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
		req.Header.Set("Cookie", kv+"; vp_session="+val)
		req.Header.Set("Authorization", "Bearer "+val)
		resp, rerr := anon.Do(req)
		if rerr != nil {
			t.Fatal(rerr)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unlock cookie accepted on /api/state: %d", resp.StatusCode)
		}
	}
	code, shellC, _ := doVisitor(t, visitorC, http.MethodGet, ts.URL+"/share/"+customC.Token+"/", "", "", "198.51.100.55", "")
	if code != http.StatusOK {
		t.Fatalf("shellC = %d", code)
	}
	grantC := extractGrant(t, shellC)
	if code, _, _ := doVisitor(t, anon, http.MethodGet, ts.URL+"/api/share-u/"+grantC+"/v1/snapshot", "", "", "", ""); code != http.StatusOK {
		t.Fatalf("grantC snapshot = %d, want 200", code)
	}
	// Rotate customC's token -> old grantC stops working immediately.
	if code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/shares/"+customC.ID+"/rotate", "application/json", []byte(`{}`)); code != http.StatusOK {
		t.Fatalf("rotate customC = %d", code)
	}
	if code, _, _ := doVisitor(t, anon, http.MethodGet, ts.URL+"/api/share-u/"+grantC+"/v1/snapshot", "", "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("grantC still worked after rotate: %d", code)
	}

	// Silence unused auth import check if refactored.
	_ = auth.HashToken
}

// Thirty wrong passwords from thirty addresses must not lock the link for
// everybody else.
//
// The unlock path used to keep a link-global failure window (30 per link per
// 5 minutes) checked before the password was read. Anyone holding the URL --
// no password needed -- could hold a protected link shut for all of its
// visitors indefinitely, renewed every five minutes, from a plain cross-site
// form; correct passwords got the same 429 as wrong ones. The window is gone
// on purpose (see the layering comment in handleShareUnlock); this test is
// what makes reintroducing any gate that wide before the password check a
// decision that has to argue with a red test first.
func TestAWrongPasswordBurstDoesNotLockTheLinkForOtherVisitors(t *testing.T) {
	ts, srv := newTestServer(t)
	nets, err := auth.ParseCIDRs([]string{"127.0.0.1/32", "::1/128"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth.TrustedProxies = nets
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	custom := pageLink(t, ts, p.page.ID, `,"accessMode":"custom","password":"only-for-c"`)

	wrong := url.Values{"password": {"wrong-pw"}}.Encode()
	for i := 0; i < 30; i++ {
		abuser := newVisitorClient(t)
		code, body, _ := doVisitor(t, abuser, http.MethodPost, ts.URL+"/share/"+custom.Token+"/unlock",
			wrong, "", fmt.Sprintf("203.0.113.%d", 100+i), "")
		if code != http.StatusUnauthorized {
			t.Fatalf("wrong password from address %d = %d, want 401: %s", i, code, body)
		}
	}

	// A different address, the correct password: this is the line the
	// link-global window failed. 303 means the gate read the password.
	if code, body, _ := doVisitor(t, newVisitorClient(t), http.MethodPost, ts.URL+"/share/"+custom.Token+"/unlock",
		url.Values{"password": {"only-for-c"}}.Encode(), "", "198.51.100.200", ""); code != http.StatusSeeOther {
		t.Fatalf("correct password from a fresh address = %d, want 303 (locked for everyone?): %s", code, body)
	}
}

// An unlock must queue behind the password slots, not derive around them.
//
// The sign-in burst test (TestASignInBurstCannotMultiplyTheDerivation) can
// count derivations because its sixteen attempts share one throttle key, so
// the queued ones hit the post-acquire re-check. Unlock visitors are distinct
// by design and never accumulate each other's failures, so there is no count
// to assert here — the property to pin is the gate itself: while every slot
// is held, an unlock waits inside acquirePasswordSlot and runs no derivation;
// deleting that acquire block would let each burst member allocate 64 MiB
// the moment it arrived.
func TestAnUnlockQueuesBehindThePasswordSlots(t *testing.T) {
	ts, srv := newTestServer(t)
	nets, err := auth.ParseCIDRs([]string{"127.0.0.1/32", "::1/128"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth.TrustedProxies = nets
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	custom := pageLink(t, ts, p.page.ID, `,"accessMode":"custom","password":"only-for-c"`)

	for i := 0; i < cap(passwordSlots); i++ {
		passwordSlots <- struct{}{}
	}
	held := cap(passwordSlots)

	done := make(chan int, 1)
	go func() {
		client := newVisitorClient(t)
		res, err := client.Post(ts.URL+"/share/"+custom.Token+"/unlock",
			"application/x-www-form-urlencoded",
			strings.NewReader(url.Values{"password": {"wrong-pw"}}.Encode()))
		if err != nil {
			done <- 0
			return
		}
		res.Body.Close()
		done <- res.StatusCode
	}()

	select {
	case code := <-done:
		t.Fatalf("unlock answered %d while every derivation slot was held: the slot queue is not on this path", code)
	case <-time.After(300 * time.Millisecond):
	}

	// One slot free: the queued unlock proceeds, reaches the password check,
	// and answers 401 like any wrong password.
	<-passwordSlots
	held--
	var code int
	select {
	case code = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("queued unlock never completed after a slot was released")
	}
	if code != http.StatusUnauthorized {
		t.Errorf("queued unlock = %d, want 401", code)
	}
	for i := 0; i < held; i++ {
		<-passwordSlots
	}
}

func TestShareUnlockShellForwardsTitleAndFavicon(t *testing.T) {
	ts, _ := newTestServer(t)

	// Configure a global shared password so newly created links default to shared.
	if code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing/password", "application/json", []byte(`{"password":"shell-meta-pw"}`)); code != http.StatusOK {
		t.Fatalf("set shared password = %d: %s", code, body)
	}

	publishNamed := func(name string, files map[string]string) testPage {
		t.Helper()
		manifest := fmt.Sprintf(`{"sdk":1,"name":%q,"sections":[]}`, name)
		dir := pageDir(t, manifest, files)
		body, _ := json.Marshal(map[string]string{"name": name, "sourceDir": dir})
		page := postJSON[store.SharePage](t, ts, "/api/settings/pages", string(body))
		postJSON[map[string]int](t, ts, "/api/settings/pages/"+page.ID+"/publish", `{"note":"first"}`)
		return testPage{dir: dir, page: page}
	}

	customIcon := `data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Crect width='16' height='16' fill='%230f4d92'/%3E%3C/svg%3E`
	pInline := publishNamed("inline-icon-page", map[string]string{
		"index.html": `<!doctype html><head><meta charset="utf-8">` +
			`<link rel="icon" href="` + customIcon + `">` +
			`<title>Q4 Revenue &amp; Metrics</title></head><body><h1>ok</h1></body>`,
	})
	linkInline := pageLink(t, ts, pInline.page.ID, ``)

	pRel := publishNamed("rel-icon-page", map[string]string{
		"brand.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="#0969da"/></svg>`,
		"index.html": `<!doctype html><head><meta charset="utf-8">` +
			`<link rel="icon" href="./brand.svg">` +
			`<title>Brand Report</title></head><body><h1>ok</h1></body>`,
	})
	linkRel := pageLink(t, ts, pRel.page.ID, ``)

	pFallback := publishNamed("Fallback Named Page", map[string]string{
		"index.html": `<!doctype html><head><meta charset="utf-8">` +
			`<link rel="icon" href="data:,">` +
			`<title>vibepanel</title></head><body><h1>ok</h1></body>`,
	})
	linkFallback := pageLink(t, ts, pFallback.page.ID, ``)

	pExternal := publishNamed("external-icon-page", map[string]string{
		"index.html": `<!doctype html><head><meta charset="utf-8">` +
			`<link rel="icon" href="https://evil.example/tracker.png">` +
			`<title>External Refused</title></head><body><h1>ok</h1></body>`,
	})
	linkExternal := pageLink(t, ts, pExternal.page.ID, ``)

	// 1. Before unlocking, the password gate must NOT leak the page's title or custom icon,
	//    and must carry the default vibepanel favicon with img-src data:.
	visitor := newVisitorClient(t)
	code, gateBody, gateHdr := doVisitor(t, visitor, http.MethodGet, ts.URL+"/share/"+linkInline.Token+"/", "", "", "198.51.100.11", "")
	if code != http.StatusOK {
		t.Fatalf("gate status = %d", code)
	}
	if strings.Contains(gateBody, "Q4 Revenue") || strings.Contains(gateBody, "%230f4d92") {
		t.Fatalf("password gate leaked protected page title or icon:\n%s", gateBody)
	}
	if !strings.Contains(gateBody, `<link rel="icon" href="data:image/svg+xml,`) {
		t.Errorf("password gate missing default inline favicon:\n%s", gateBody)
	}
	if !strings.Contains(gateHdr.Get("Content-Security-Policy"), "img-src data:") {
		t.Errorf("password gate CSP missing img-src data:: %q", gateHdr.Get("Content-Security-Policy"))
	}

	// Unlock once with shared password -> covers all four shared links.
	if code, _, _ := doVisitor(t, visitor, http.MethodPost, ts.URL+"/share/"+linkInline.Token+"/unlock",
		url.Values{"password": {"shell-meta-pw"}}.Encode(), "", "198.51.100.11", ""); code != http.StatusSeeOther {
		t.Fatalf("unlock = %d, want 303", code)
	}

	// 2. Inline data:image/svg+xml icon + escaped HTML title forwarded to outer shell.
	code, shellInline, shellHdr := doVisitor(t, visitor, http.MethodGet, ts.URL+"/share/"+linkInline.Token+"/", "", "", "198.51.100.11", "")
	if code != http.StatusOK {
		t.Fatalf("shellInline = %d", code)
	}
	if !strings.Contains(shellInline, "<title>Q4 Revenue &amp; Metrics</title>") {
		t.Errorf("shellInline missing forwarded title:\n%s", shellInline)
	}
	if !strings.Contains(shellInline, "%230f4d92") {
		t.Errorf("shellInline missing custom inline SVG favicon:\n%s", shellInline)
	}
	if !strings.Contains(shellHdr.Get("Content-Security-Policy"), "img-src 'self' data:") {
		t.Errorf("shellInline CSP missing img-src 'self' data:: %q", shellHdr.Get("Content-Security-Policy"))
	}

	// 3. Relative bundle icon is rewritten to /share-u/{grant}/brand.svg and serves 200.
	code, shellRel, _ := doVisitor(t, visitor, http.MethodGet, ts.URL+"/share/"+linkRel.Token+"/", "", "", "198.51.100.11", "")
	if code != http.StatusOK {
		t.Fatalf("shellRel = %d", code)
	}
	grantRel := extractGrant(t, shellRel)
	wantRelHref := `/share-u/` + grantRel + `/brand.svg`
	if !strings.Contains(shellRel, "<title>Brand Report</title>") || !strings.Contains(shellRel, `href="`+wantRelHref+`"`) {
		t.Errorf("shellRel did not rewrite title/relative icon (want %q):\n%s", wantRelHref, shellRel)
	}
	code, iconBody, iconHdr := doVisitor(t, anonymousClient(t), http.MethodGet, ts.URL+wantRelHref, "", "", "198.51.100.11", "")
	if code != http.StatusOK || !strings.Contains(iconHdr.Get("Content-Type"), "image/svg+xml") || !strings.Contains(iconBody, "#0969da") {
		t.Fatalf("GET rewritten icon %s = %d ct=%q body=%q", wantRelHref, code, iconHdr.Get("Content-Type"), iconBody)
	}

	// 4. Blank-template "vibepanel" title and empty "data:," icon fall back to page name and default vibepanel icon.
	code, shellFallback, _ := doVisitor(t, visitor, http.MethodGet, ts.URL+"/share/"+linkFallback.Token+"/", "", "", "198.51.100.11", "")
	if code != http.StatusOK {
		t.Fatalf("shellFallback = %d", code)
	}
	if !strings.Contains(shellFallback, "<title>Fallback Named Page</title>") {
		t.Errorf("shellFallback did not fall back to page name:\n%s", shellFallback)
	}
	if !strings.Contains(shellFallback, "%230A84FF") {
		t.Errorf("shellFallback did not fall back to default vibepanel favicon:\n%s", shellFallback)
	}

	// 5. External https:// icon is refused and falls back to the default vibepanel icon.
	code, shellExternal, _ := doVisitor(t, visitor, http.MethodGet, ts.URL+"/share/"+linkExternal.Token+"/", "", "", "198.51.100.11", "")
	if code != http.StatusOK {
		t.Fatalf("shellExternal = %d", code)
	}
	if strings.Contains(shellExternal, "evil.example") || !strings.Contains(shellExternal, "%230A84FF") {
		t.Errorf("shellExternal allowed external icon or missed fallback:\n%s", shellExternal)
	}
}
