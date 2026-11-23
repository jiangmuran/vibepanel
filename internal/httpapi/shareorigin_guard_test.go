package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
)

// requestOnHost sends one anonymous request to ts claiming to be for host,
// following nothing.
func requestOnHost(t *testing.T, ts *httptest.Server, host, method, reqPath string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+reqPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	client := anonymousClient(t)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s as %s: %v", method, reqPath, host, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

// panelBundleMarker is in the panel's index.html and in nothing the share
// host is allowed to answer with.
const panelBundleMarker = `<div id="root">`

// withBundle gives the test server a frontend to serve. Without one the
// catch-all answers 503 "frontend not built" on every host, and a test that
// asserts "no bundle on the share host" passes with the middleware removed.
func withBundle(t *testing.T, srv *Server) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(srv.Cfg.StaticDir, "index.html"),
		[]byte("<!doctype html><title>panel</title>"+panelBundleMarker+"</div>"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setShareOrigin(t *testing.T, ts *httptest.Server, origin string) {
	t.Helper()
	code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json",
		[]byte(`{"shareOrigin":"`+origin+`"}`))
	if code != http.StatusOK {
		t.Fatalf("set shareOrigin %s = %d: %s", origin, code, body)
	}
}

// The share host is its name, however the Host header spells it.
//
// It was compared as an exact `host:port`, so isolation failed open: a
// trailing dot or another port was "not the share host", and the request got
// the panel's sign-in page on the public domain.
func TestTheShareHostIsANameOnAnyPort(t *testing.T) {
	ts, srv := newTestServer(t)
	withBundle(t, srv)
	srv.Cfg.Domain = "panel.example.com"
	setShareOrigin(t, ts, "https://share.example.com")

	// The control: the panel's own address serves the bundle, so a 404 below
	// is the middleware and not a test server with nothing to serve.
	if code, body, _ := requestOnHost(t, ts, "panel.example.com", http.MethodGet, "/"); code != http.StatusOK || !strings.Contains(body, panelBundleMarker) {
		t.Fatalf("GET / on the panel's host = %d, bundle = %v; want 200 with the bundle", code, strings.Contains(body, panelBundleMarker))
	}

	for _, host := range []string{
		"share.example.com",
		"share.example.com.",
		"share.example.com..",
		"share.example.com:443",
		"share.example.com:444",
		"share.example.com.:8443",
		"SHARE.Example.COM",
	} {
		for _, p := range []string{"/", "/api/health", "/api/auth/status", "/ws"} {
			code, body, _ := requestOnHost(t, ts, host, http.MethodGet, p)
			if code != http.StatusNotFound {
				t.Errorf("Host %q GET %s = %d, want 404", host, p, code)
			}
			if strings.Contains(body, panelBundleMarker) {
				t.Errorf("Host %q GET %s served the panel's bundle", host, p)
			}
		}
	}

	// And the other side of the same rule: the panel's own name on another
	// port cannot be made the share origin, because it would be confined too.
	for _, origin := range []string{"https://panel.example.com:9443", "http://panel.example.com.:81"} {
		code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json",
			[]byte(`{"shareOrigin":"`+origin+`"}`))
		if code != http.StatusBadRequest {
			t.Errorf("shareOrigin %s = %d (%s), want 400: it is the panel's own name", origin, code, body)
		}
	}
}

func TestHostnameOnly(t *testing.T) {
	for in, want := range map[string]string{
		"share.example.com":       "share.example.com",
		"Share.Example.com.":      "share.example.com",
		"share.example.com:444":   "share.example.com",
		"share.example.com.:444":  "share.example.com",
		"share.example.com:bogus": "share.example.com",
		"[::1]:8080":              "::1",
		"[::1]":                   "::1",
		"":                        "",
	} {
		if got := hostnameOnly(in); got != want {
			t.Errorf("hostnameOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// Red line 8: an unknown share path never gets the panel's bundle.
//
// The check read the decoded path and the router matches the escaped one, so
// `/share%2Fx` passed as a share path, matched no share route and fell through
// to the catch-all.
func TestAnEscapedPathOnTheShareHostNeverGetsThePanel(t *testing.T) {
	ts, srv := newTestServer(t)
	withBundle(t, srv)
	srv.Cfg.Domain = "panel.example.com"
	setShareOrigin(t, ts, "https://share.example.com")

	for _, p := range []string{
		"/share%2Fx",
		"/share%2Fx/",
		"/share%2fx%2Findex.html",
		"/api%2Fshare%2Fx",
		"/api%2Fshare%2Fx%2Fv1%2Fsnapshot",
		"/api/share%2Fx",
		"/%73hare%2Fx",
		"/.well-known%2Fvibepanel-probe%2F0123456789abcdef0123456789abcdef",
	} {
		code, body, hdr := requestOnHost(t, ts, "share.example.com", http.MethodGet, p)
		if code != http.StatusNotFound {
			t.Errorf("GET %s on the share host = %d, want 404", p, code)
		}
		if strings.Contains(body, panelBundleMarker) {
			t.Errorf("GET %s on the share host served the panel's bundle", p)
		}
		if strings.HasPrefix(p, "/api") && !strings.Contains(hdr.Get("Content-Type"), "application/json") {
			t.Errorf("GET %s on the share host answered %q, want the API's JSON 404", p, hdr.Get("Content-Type"))
		}
	}

	// What must keep working: a file whose name needs escaping.
	for raw, want := range map[string]bool{
		"/share/tok/a%20b.png":            true,
		"/share/tok/":                     true,
		"/api/share/tok/v1/snapshot":      true,
		"/share%2Ftok":                    false,
		"/share/../api/health":            false,
		"/share/%2e%2e/api/health":        false,
		"/api%2Fshare%2Ftok/v1/snapshot":  false,
		"/share/":                         false,
		"/api/share/tok/../../auth/login": false,
	} {
		u, err := url.Parse("http://share.example.com" + raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := isShareHostPath(http.MethodGet, u); got != want {
			t.Errorf("isShareHostPath(GET %s) = %v, want %v", raw, got, want)
		}
	}
}

// One failed read must not decide the answer for the life of the process.
func TestAFailedReadOfTheShareOriginIsNotRemembered(t *testing.T) {
	var c shareOriginCache
	reads := 0
	read := func(context.Context) (string, error) {
		reads++
		if reads == 1 {
			return "", context.Canceled
		}
		return "https://share.example.com", nil
	}
	if _, _, _, err := c.get(context.Background(), read); err == nil {
		t.Fatal("a read that failed was reported as an answer")
	}
	origin, _, host, err := c.get(context.Background(), read)
	if err != nil || origin != "https://share.example.com" || host != "share.example.com" {
		t.Fatalf("after one failed read, get = %q %q %v; want the stored origin. "+
			"The failure was remembered as \"no share origin\", which turns isolation off until restart", origin, host, err)
	}
	if _, _, _, _ = c.get(context.Background(), read); reads != 2 {
		t.Errorf("a successful read was not cached: %d reads", reads)
	}
}

// While the setting cannot be read, no host is served as the panel: this one
// may be the share host, and the panel on the share host is the failure.
func TestNothingIsServedAsThePanelWhileTheShareOriginCannotBeRead(t *testing.T) {
	ts, srv := newTestServer(t)
	withBundle(t, srv)
	srv.Cfg.Domain = "panel.example.com"
	setShareOrigin(t, ts, "https://share.example.com")

	// A restart, and then a database that cannot answer.
	srv.pb.origin.forget()
	if err := srv.DB.Close(); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"/", "/api/health", "/share/whatever/"} {
		code, body, _ := requestOnHost(t, ts, "share.example.com", http.MethodGet, p)
		if code != http.StatusServiceUnavailable {
			t.Errorf("GET %s with the setting unreadable = %d, want 503", p, code)
		}
		if strings.Contains(body, panelBundleMarker) {
			t.Errorf("GET %s with the setting unreadable served the panel's bundle on what may be the share host", p)
		}
	}
}

// The check connects only to public addresses, and to the one it checked.
func TestTheCheckDialsOnlyPublicAddresses(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.TLSMode = config.TLSOff

	network := &dialByPort{}
	var lookups []string
	var answer []string
	var mu sync.Mutex
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: func(_ context.Context, host string) ([]net.IPAddr, error) {
			mu.Lock()
			defer mu.Unlock()
			lookups = append(lookups, host)
			if strings.HasSuffix(host, ".test") {
				// The guard's fake-ip probe: a real resolver says no.
				return nil, errors.New("no such host")
			}
			out := make([]net.IPAddr, 0, len(answer))
			for _, a := range answer {
				out = append(out, net.IPAddr{IP: net.ParseIP(a)})
			}
			return out, nil
		},
		dial:   network.dial,
		runCmd: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no nginx") },
	}

	for _, ips := range [][]string{
		{"127.0.0.1"},
		{"::1"},
		{"10.0.0.5"},
		{"192.168.1.10"},
		{"172.16.3.4"},
		{"169.254.169.254"}, // cloud metadata
		{"fd00:ec2::254"},
		{"100.64.0.1"},
		{"198.18.0.7"},
		{"0.0.0.0"},
		{"::ffff:127.0.0.1"},
		// One public and one private answer is a name somebody arranged.
		{sharePublicIP, "192.168.1.10"},
	} {
		mu.Lock()
		answer = ips
		mu.Unlock()
		got := postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
		if got.Status != "not_public" || got.Ready || got.Reachable {
			t.Errorf("resolving to %v: status %q ready=%v, want not_public", ips, got.Status, got.Ready)
		}
		// One fixed sentence: nothing that tells a closed port from a silent one.
		if got.ReachError != errNotPublic || got.TLSError != "" {
			t.Errorf("resolving to %v: reachError %q tlsError %q, want only the fixed sentence", ips, got.ReachError, got.TLSError)
		}
	}
	if d := network.seen(); len(d) != 0 {
		t.Errorf("the check connected to %v; a name resolving to a non-public address must not be dialled at all", d)
	}
	if !auditHas(t, srv, "sharing.origin_checked", "share.example.com not_public") {
		t.Error("a refused check left no audit row")
	}

	// An address, a plain-http origin: refused before anything is resolved.
	mu.Lock()
	lookups = nil
	mu.Unlock()
	for _, origin := range []string{
		"https://127.0.0.1",
		"https://127.0.0.1:8443",
		"https://10.0.0.5",
		"https://169.254.169.254",
		"https://[::1]",
		"https://93.184.215.14",
		"http://share.example.com",
		"http://share.example.com:8080",
		"https://localhost",
	} {
		code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/check", "application/json",
			[]byte(`{"origin":"`+origin+`"}`))
		if code != http.StatusBadRequest {
			t.Errorf("check %s = %d (%s), want 400", origin, code, body)
		}
	}
	mu.Lock()
	if len(lookups) != 0 {
		t.Errorf("refused origins were still looked up: %v", lookups)
	}
	mu.Unlock()
	if d := network.seen(); len(d) != 0 {
		t.Errorf("refused origins were still dialled: %v", d)
	}
	if !auditHas(t, srv, "sharing.origin_checked", "169.254.169.254 refused: "+errCheckNeedsDomain) {
		t.Error("a check refused for its origin left no audit row")
	}

	// The ordinary case still works: a share domain on a public address, which
	// is what a panel's own machine has. Port 443 refused, port 80 open.
	_, tsPort, _ := net.SplitHostPort(ts.Listener.Addr().String())
	network.mu.Lock()
	network.ports = map[string]string{"80": tsPort}
	network.mu.Unlock()
	mu.Lock()
	answer = []string{sharePublicIP}
	mu.Unlock()
	got := postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com:8443"}`)
	if got.Status != "needs_https" || !got.HTTPPortOpen {
		t.Errorf("public address with only port 80 open: %+v, want needs_https", got)
	}
	want := map[string]bool{sharePublicIP + ":8443": true, sharePublicIP + ":80": true}
	for _, d := range network.seen() {
		if !want[d] {
			t.Errorf("dialled %s; only the checked address %s may be dialled", d, sharePublicIP)
		}
	}
}

// Two provisions at once are one provision and one refusal.
func TestOnlyOneProvisionRunsAtATime(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.TLSMode = config.TLSACME

	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex
	srv.ManageTLS = func(context.Context, string) error {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		return errors.New("stop here")
	}
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: lookupAlways(sharePublicIP),
		dial:     (&dialByPort{}).dial,
		settle:   []time.Duration{},
	}

	done := make(chan int, 1)
	go func() {
		code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/provision", "application/json",
			[]byte(`{"origin":"https://share.example.com"}`))
		done <- code
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first provision never started")
	}

	code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/provision", "application/json",
		[]byte(`{"origin":"https://other.example.com"}`))
	close(release)
	if code != http.StatusConflict {
		t.Errorf("a second provision while one runs = %d (%s), want 409", code, body)
	}
	if first := <-done; first != http.StatusBadGateway {
		t.Errorf("the first provision = %d, want the 502 its fake CA gives", first)
	}
	mu.Lock()
	if calls != 1 {
		t.Errorf("the CA was asked %d times, want once", calls)
	}
	mu.Unlock()
	if !auditHas(t, srv, "sharing.origin_provision_refused", "other.example.com: another HTTPS setup is running") {
		t.Error("the refused second provision left no audit row")
	}

	// And the lock is given back.
	if code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/provision", "application/json",
		[]byte(`{"origin":"https://share.example.com"}`)); code != http.StatusBadGateway {
		t.Errorf("a provision after the first finished = %d, want 502 (it ran)", code)
	}
}

// nginxFixture is a throwaway /etc/nginx and /etc/letsencrypt with fake
// binaries, wired to a server whose share domain reaches it over TLS.
type nginxFixture struct {
	avail, enabled string
	leDir, webroot string
	// atCertbot is the vhost's content at the moment certbot ran.
	atCertbot []string
	certbots  int
}

func newNginxFixture(t *testing.T, srv *Server) *nginxFixture {
	t.Helper()
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.Addr = "127.0.0.1:9884"
	srv.Cfg.TLSMode = config.TLSOff

	tmp := t.TempDir()
	f := &nginxFixture{leDir: filepath.Join(tmp, "letsencrypt"), webroot: filepath.Join(tmp, "webroot")}
	sitesAvail, sitesEnabled := filepath.Join(tmp, "sites-available"), filepath.Join(tmp, "sites-enabled")
	for _, d := range []string{sitesAvail, sitesEnabled, f.webroot, f.leDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.avail = filepath.Join(sitesAvail, "vibepanel-share-share.example.com.conf")
	f.enabled = filepath.Join(sitesEnabled, "vibepanel-share-share.example.com.conf")

	tlsPort, pool := shareTLSServer(t, srv.Routes())
	srv.pb.provisioner = &shareOriginProvisioner{
		sitesAvailable: sitesAvail,
		sitesEnabled:   sitesEnabled,
		webroot:        f.webroot,
		letsencryptDir: f.leDir,
		nginxBin:       "/fake/nginx",
		certbotBin:     "/fake/certbot",
		lookupIP:       lookupAlways(sharePublicIP),
		dial:           (&dialByPort{ports: map[string]string{"443": tlsPort}}).dial,
		rootCAs:        pool,
		settle:         []time.Duration{},
		runCmd: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "/fake/certbot" {
				f.certbots++
				b, _ := os.ReadFile(f.avail)
				f.atCertbot = append(f.atCertbot, string(b))
				f.writeCert(t, 60*24*time.Hour)
			}
			return []byte("ok"), nil
		},
	}
	return f
}

func (f *nginxFixture) writeCert(t *testing.T, validFor time.Duration) {
	t.Helper()
	liveDir := filepath.Join(f.leDir, "live", "share.example.com")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, _ := selfSignedCertPEM(t, "share.example.com", time.Now().Add(validFor))
	if err := os.WriteFile(filepath.Join(liveDir, "fullchain.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "privkey.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func provisionShare(t *testing.T, ts *httptest.Server) shareOriginProvisionResult {
	t.Helper()
	return postJSON[shareOriginProvisionResult](t, ts, "/api/settings/sharing/provision", `{"origin":"https://share.example.com"}`)
}

// The vhost is replaced in one step, never truncated and refilled.
func TestTheVhostIsReplacedNotRewritten(t *testing.T) {
	ts, srv := newTestServer(t)
	f := newNginxFixture(t, srv)

	if res := provisionShare(t, ts); !res.Check.Ready {
		t.Fatalf("first provision not ready: %+v", res.Check)
	}
	before, err := os.ReadFile(f.avail)
	if err != nil {
		t.Fatal(err)
	}

	// nginx, part-way through reading the site when the next provision runs.
	// Marked, so the two versions differ.
	marked := append([]byte("# read by a reload in progress\n"), before...)
	if err := os.WriteFile(f.avail, marked, 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(f.avail)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	provisionShare(t, ts)

	seen, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(seen) != string(marked) {
		t.Errorf("a reader that opened the vhost before it was rewritten read %d bytes that are not the file it opened (%d bytes): "+
			"the file was written in place, so a reload in progress parses a truncated or mixed site", len(seen), len(marked))
	}
	after, _ := os.ReadFile(f.avail)
	if string(after) != string(before) {
		t.Errorf("the vhost on disk after re-provisioning is not the generated one:\n%s", after)
	}
	entries, _ := os.ReadDir(filepath.Dir(f.avail))
	for _, e := range entries {
		if e.Name() != filepath.Base(f.avail) {
			t.Errorf("left %s behind in sites-available", e.Name())
		}
	}
}

// Renewing a certificate does not take a working HTTPS site off the air.
func TestRenewalKeepsServingHTTPS(t *testing.T) {
	ts, srv := newTestServer(t)
	f := newNginxFixture(t, srv)

	// First time: nothing is there, so the challenge vhost goes in first.
	provisionShare(t, ts)
	if f.certbots != 1 || strings.Contains(f.atCertbot[0], "ssl_certificate") || !strings.Contains(f.atCertbot[0], "acme-challenge") {
		t.Fatalf("first provision: certbot ran %d time(s) against\n%s\nwant once, against the HTTP challenge vhost", f.certbots, strings.Join(f.atCertbot, "\n---\n"))
	}

	// The certificate comes within two weeks of expiry; the site is still up.
	f.writeCert(t, 5*24*time.Hour)
	provisionShare(t, ts)
	if f.certbots != 2 {
		t.Fatalf("certbot ran %d time(s), want a second run for the expiring certificate", f.certbots)
	}
	during := f.atCertbot[1]
	if !strings.Contains(during, "ssl_certificate ") || !strings.Contains(during, "proxy_pass http://127.0.0.1:9884;") {
		t.Errorf("while certbot renewed, the vhost was:\n%s\nwant the HTTPS site still in place: "+
			"every handed-out link is down for as long as certbot runs otherwise", during)
	}

	// A vhost somebody edited the challenge location out of cannot renew in
	// place, so that one does get the challenge vhost.
	edited := strings.ReplaceAll(during, "location /.well-known/acme-challenge/", "location /nothing/")
	if err := os.WriteFile(f.avail, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	f.writeCert(t, 5*24*time.Hour)
	provisionShare(t, ts)
	if f.certbots != 3 || strings.Contains(f.atCertbot[2], "ssl_certificate") {
		t.Errorf("with the challenge location edited out, certbot ran against:\n%s\nwant the HTTP challenge vhost", f.atCertbot[len(f.atCertbot)-1])
	}
}

// What nginx sends upstream as Host is the share host's name, not the client's.
func TestTheVhostNamesItsHostLiterally(t *testing.T) {
	https := renderNginxShareHTTPS("share.example.com", "share.example.com", "443 ssl http2", "/var/www/html",
		"http://127.0.0.1:9884", "/le/fullchain.pem", "/le/privkey.pem")
	challenge := renderNginxShareHTTPChallenge("share.example.com", "/var/www/html", "http://127.0.0.1:9884")
	for name, conf := range map[string]string{"https": https, "challenge": challenge} {
		if strings.Contains(conf, "$host") || strings.Contains(conf, "$http_host") {
			t.Errorf("the %s vhost passes the client's Host through:\n%s\n"+
				"as nginx's default server it would proxy any name to the panel under that name, "+
				"and the panel would answer as itself", name, conf)
		}
		if !strings.Contains(conf, "proxy_set_header Host share.example.com;") {
			t.Errorf("the %s vhost does not send the literal share host upstream:\n%s", name, conf)
		}
	}
	if !strings.Contains(https, "return 301 https://share.example.com$request_uri;") {
		t.Errorf("the redirect does not name the share host:\n%s", https)
	}

	// The snippet a person pastes, for an origin on its own port.
	ts, srv := newTestServer(t)
	_ = ts
	srv.Cfg.TLSMode = config.TLSOff
	p := shareOriginProvisioner{
		webroot: "/var/www/html", letsencryptDir: "/etc/letsencrypt",
		runCmd: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no nginx") },
	}
	info := srv.detectProvisionInfo(context.Background(), p, "https://share.example.com:8443", "https", "share.example.com:8443", "share.example.com")
	if strings.Contains(info.Snippet, "$host") || !strings.Contains(info.Snippet, "proxy_set_header Host share.example.com:8443;") {
		t.Errorf("the manual snippet:\n%s\nwant the literal host and port, and no $host", info.Snippet)
	}
}

// A name people are signed in at is the panel's, whatever configuration says.
//
// The command line reaches the panel on loopback, so the request's own Host
// says nothing; on a panel with no domain configured this was the whole
// panel turned into the "link no longer works" page by one command.
func TestANameSomebodyIsSignedInAtCannotBecomeTheShareOrigin(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	user, err := srv.DB.UserByName(ctx, "tester")
	if err != nil {
		t.Fatal(err)
	}
	// A browser signed in at the panel's public name, which is in no config.
	if err := srv.DB.CreateAuthSession(ctx, auth.HashToken("live-session"), user.ID, time.Hour,
		"a browser", "198.51.100.7", "https://Panel.Example.NET"); err != nil {
		t.Fatal(err)
	}
	// And one that expired at another name: no longer evidence of anything.
	if err := srv.DB.CreateAuthSession(ctx, auth.HashToken("old-session"), user.ID, -time.Hour,
		"a browser", "198.51.100.7", "https://old.example.net"); err != nil {
		t.Fatal(err)
	}

	for _, origin := range []string{"https://panel.example.net", "https://panel.example.net:8443"} {
		code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json",
			[]byte(`{"shareOrigin":"`+origin+`"}`))
		if code != http.StatusBadRequest || !strings.Contains(string(body), "signed-in session") {
			t.Errorf("shareOrigin %s = %d (%s), want 400 naming the signed-in session", origin, code, body)
		}
	}
	if got := getJSON[sharingSettingsResponse](t, ts, "/api/settings/sharing"); got.ShareOrigin != "" {
		t.Fatalf("shareOrigin = %q after refusals, want unset", got.ShareOrigin)
	}
	for _, route := range []string{"/api/settings/sharing/check", "/api/settings/sharing/provision"} {
		if code, _, _ := sendRaw(t, ts, http.MethodPost, route, "application/json",
			[]byte(`{"origin":"https://panel.example.net"}`)); code != http.StatusBadRequest {
			t.Errorf("POST %s for a signed-in name = %d, want 400", route, code)
		}
	}

	setShareOrigin(t, ts, "https://old.example.net")
}
