package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
)

func selfSignedCertPEM(t *testing.T, dnsName string, notAfter time.Time) (certPEM, keyPEM []byte, cert tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsName},
		Issuer:                pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{dnsName},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, cert
}

func TestShareProbeEndpointAndShareHostAccess(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"

	// Unknown and malformed nonces must answer 404.
	for _, p := range []string{
		"/.well-known/vibepanel-probe/short",
		"/.well-known/vibepanel-probe/0123456789abcdef0123456789abcdef",
	} {
		if res, _ := anonGET(t, ts, p); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, res.StatusCode)
		}
	}

	// Set a dedicated share origin so shareHostOnly middleware is active.
	code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json",
		[]byte(`{"shareOrigin":"https://share.example.com"}`))
	if code != http.StatusOK {
		t.Fatalf("set shareOrigin = %d: %s", code, body)
	}

	nonce, entry, cleanup, err := srv.pb.probes.mint()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Probe succeeds even when arriving on the isolated share host, recording Host.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/.well-known/vibepanel-probe/"+nonce, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "share.example.com"
	res, err := anonymousClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("probe on share host = %d, want 200", res.StatusCode)
	}
	entry.mu.Lock()
	hit, seen := entry.hit, entry.seenHost
	entry.mu.Unlock()
	if !hit || seen != "share.example.com" {
		t.Errorf("probe hit=%v seenHost=%q, want true / share.example.com", hit, seen)
	}

	// After cleanup, the same nonce answers 404.
	cleanup()
	if res, _ := anonGET(t, ts, "/.well-known/vibepanel-probe/"+nonce); res.StatusCode != http.StatusNotFound {
		t.Errorf("expired probe = %d, want 404", res.StatusCode)
	}
}

func TestParseNginxStreamDefault(t *testing.T) {
	cases := []struct {
		name string
		dump string
		want string
	}{
		{
			name: "no stream block",
			dump: "http {\n  server {\n    listen 443 ssl http2;\n  }\n}\n",
			want: "",
		},
		{
			name: "stream without 443",
			dump: "stream {\n  map $ssl_preread_server_name $u {\n    default 127.0.0.1:9000;\n  }\n  server {\n    listen 8443;\n  }\n}\n",
			want: "",
		},
		{
			name: "sni multiplexer on 443",
			dump: `
# comment stream {
stream {
    map $ssl_preread_server_name $sni_upstream {
        dev2.1.quaesto.com unix:/run/nginx-vibepanel-h2.sock;
        default 127.0.0.1:14443; # fallback vhosts
    }
    server {
        listen 443;
        listen [::]:443;
        ssl_preread on;
        proxy_pass $sni_upstream;
    }
}
http {
    server { listen 80; }
}
`,
			want: "127.0.0.1:14443 ssl http2",
		},
		{
			name: "stream sni with proxy_protocol preserves proxy_protocol",
			dump: `
stream {
    map $ssl_preread_server_name $sni_upstream {
        default 127.0.0.1:14443;
    }
    server {
        listen 443;
        ssl_preread on;
        proxy_protocol on;
        proxy_pass $sni_upstream;
    }
}
`,
			want: "127.0.0.1:14443 ssl http2 proxy_protocol",
		},
	}
	for _, tc := range cases {
		if got := parseNginxStreamDefault(tc.dump); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// sharePublicIP is what the share domain resolves to in these tests: an
// address the real guard (publicAddr) lets through, so the tests run with the
// guard in place and only the dial replaced. 203.0.113.0/24, the documentation
// range, is refused by it, which is right and is why it is not used here.
const sharePublicIP = "93.184.215.14"

// dialByPort stands in for the network: a connection to any address on a port
// in the table lands on that local port, anything else is refused. Every
// address asked for is recorded.
type dialByPort struct {
	mu      sync.Mutex
	ports   map[string]string
	dialled []string
}

func (d *dialByPort) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.dialled = append(d.dialled, addr)
	_, port, _ := net.SplitHostPort(addr)
	local, ok := d.ports[port]
	d.mu.Unlock()
	if !ok {
		return nil, errors.New("connect: connection refused")
	}
	return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+local)
}

func (d *dialByPort) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dialled...)
}

func lookupAlways(ip string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
}

// shareTLSServer serves h over TLS with a certificate for share.example.com,
// returning its port and a pool that trusts it.
func shareTLSServer(t *testing.T, h http.Handler) (port string, pool *x509.CertPool) {
	t.Helper()
	certPEM, _, tlsCert := selfSignedCertPEM(t, "share.example.com", time.Now().Add(30*24*time.Hour))
	pool = x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	tlsSrv := httptest.NewUnstartedServer(h)
	tlsSrv.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	tlsSrv.StartTLS()
	t.Cleanup(tlsSrv.Close)
	_, port, _ = net.SplitHostPort(tlsSrv.Listener.Addr().String())
	return port, pool
}

func TestCheckShareOriginStatuses(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.TLSMode = config.TLSOff
	_, tsPort, _ := net.SplitHostPort(ts.Listener.Addr().String())

	// Lockout guard: checking the panel's own domain returns 400.
	resetOriginCheck(t, srv)
	if code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/check", "application/json",
		[]byte(`{"origin":"panel.example.com"}`)); code != http.StatusBadRequest {
		t.Fatalf("panel collision = %d (%s), want 400", code, body)
	}

	// 1. DNS missing -> needs_dns with panel host IP hint populated.
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host == "panel.example.com" {
				return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
			}
			return nil, errors.New("no such host")
		},
		nginxBin:   "nginx-missing",
		certbotBin: "certbot-missing",
		runCmd: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, errors.New("not installed")
		},
	}
	resetOriginCheck(t, srv)
	got := postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"share.example.com"}`)
	if got.Status != "needs_dns" || got.Ready || got.DNSOK {
		t.Errorf("missing DNS: got status=%q ready=%v dnsOk=%v", got.Status, got.Ready, got.DNSOK)
	}
	if got.Origin != "https://share.example.com" || len(got.HostIPs) == 0 || got.HostIPs[0] != "203.0.113.10" {
		t.Errorf("missing DNS origin/hostIps = %q / %v", got.Origin, got.HostIPs)
	}
	if !auditHas(t, srv, "sharing.origin_checked", "share.example.com needs_dns") {
		t.Error("a check left no audit row naming the host and the outcome")
	}

	// 2. Ready over valid HTTPS with Host forwarded properly.
	tlsPort, pool := shareTLSServer(t, srv.Routes())
	net1 := &dialByPort{ports: map[string]string{"443": tlsPort}}
	noNginx := func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, errors.New("no nginx") }
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: lookupAlways(sharePublicIP),
		dial:     net1.dial,
		rootCAs:  pool,
		runCmd:   noNginx,
	}
	resetOriginCheck(t, srv)
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if !got.Ready || got.Status != "ready" || !got.TLSOK || !got.Reachable || !got.HostForwarded || !got.DNSMatchesHost {
		t.Errorf("ready check = %+v", got)
	}
	// The address that was resolved and checked is the one connected to.
	if d := net1.seen(); len(d) != 1 || d[0] != sharePublicIP+":443" {
		t.Errorf("dialled %v, want exactly the checked address %s:443", d, sharePublicIP)
	}
	if !auditHas(t, srv, "sharing.origin_checked", "share.example.com ready") {
		t.Error("a ready check left no audit row")
	}

	// 3. Untrusted TLS cert -> needs_https with insecure reachability confirmed.
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: lookupAlways(sharePublicIP),
		dial:     (&dialByPort{ports: map[string]string{"443": tlsPort, "80": tsPort}}).dial,
		rootCAs:  x509.NewCertPool(),
		runCmd:   noNginx,
	}
	resetOriginCheck(t, srv)
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if got.Status != "needs_https" || got.Ready || got.TLSOK || !got.Reachable || !got.HTTPPortOpen {
		t.Errorf("needs_https check = %+v", got)
	}

	// 4. Proxy reaches this panel but strips/rewrites Host header -> host_mismatch.
	rwPort, rwPool := shareTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1:9884"
		srv.Routes().ServeHTTP(w, r)
	}))
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: lookupAlways(sharePublicIP),
		dial:     (&dialByPort{ports: map[string]string{"443": rwPort}}).dial,
		rootCAs:  rwPool,
		runCmd:   noNginx,
	}
	resetOriginCheck(t, srv)
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if got.Status != "host_mismatch" || got.Ready || !got.Reachable || got.HostForwarded || got.SeenHost != "127.0.0.1:9884" {
		t.Errorf("host_mismatch check = %+v", got)
	}

	// 5. Foreign server answering HTTP 200 (not this panel) -> needs_proxy.
	fPort, fPool := shareTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("welcome to nginx"))
	}))
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: lookupAlways(sharePublicIP),
		dial:     (&dialByPort{ports: map[string]string{"443": fPort}}).dial,
		rootCAs:  fPool,
		runCmd:   noNginx,
	}
	resetOriginCheck(t, srv)
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if got.Status != "needs_proxy" || got.Ready || got.Reachable {
		t.Errorf("needs_proxy check = %+v", got)
	}

	// 6. A redirect is an answer from something else, reported and not followed.
	var followed atomic.Bool
	rPort, rPool := shareTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: lookupAlways(sharePublicIP),
		dial:     (&dialByPort{ports: map[string]string{"443": rPort}}).dial,
		rootCAs:  rPool,
		runCmd:   noNginx,
	}
	resetOriginCheck(t, srv)
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if got.Status != "needs_proxy" || followed.Load() {
		t.Errorf("redirecting host: status %q, redirect followed = %v; want needs_proxy and not followed", got.Status, followed.Load())
	}
}

func TestProvisionShareOriginACME(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.TLSMode = config.TLSACME

	var managed []string
	var manageErr error
	srv.ManageTLS = func(_ context.Context, domain string) error {
		managed = append(managed, domain)
		return manageErr
	}

	// Missing DNS refuses before invoking ManageTLS.
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host == "panel.example.com" {
				return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
			}
			return nil, errors.New("nxdomain")
		},
	}
	if code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/provision", "application/json",
		[]byte(`{"origin":"share.example.com"}`)); code != http.StatusBadRequest || !strings.Contains(string(body), "203.0.113.10") {
		t.Fatalf("acme missing DNS = %d (%s), want 400 with host IP hint", code, body)
	}
	if len(managed) != 0 {
		t.Fatalf("ManageTLS called despite missing DNS: %v", managed)
	}
	if !auditHas(t, srv, "sharing.origin_provision_refused", "share.example.com: DNS for share.example.com does not resolve yet") {
		t.Error("a provision refused for DNS left no audit row")
	}

	// The CA says no: 502, nothing saved, and a row saying which host and why.
	offline := &dialByPort{}
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: lookupAlways(sharePublicIP),
		dial:     offline.dial,
		settle:   []time.Duration{},
	}
	manageErr = errors.New("acme: rate limited")
	if code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/provision", "application/json",
		[]byte(`{"origin":"https://share.example.com"}`)); code != http.StatusBadGateway {
		t.Fatalf("acme failure = %d, want 502", code)
	}
	if !auditHas(t, srv, "sharing.origin_provision_failed", "acme share.example.com: acme: rate limited") {
		t.Error("a failed provision left no audit row")
	}
	manageErr = nil
	managed = nil

	// The certificate is obtained but the domain does not reach this panel:
	// the setting is left alone, and the answer and the log both say so.
	res := postJSON[shareOriginProvisionResult](t, ts, "/api/settings/sharing/provision", `{"origin":"https://share.example.com"}`)
	if res.Mode != "acme" || res.Check.Ready || res.ShareOrigin != "" {
		t.Errorf("provision with the domain unreachable = %+v, want mode acme, not ready, shareOrigin empty", res)
	}
	if len(managed) != 1 || managed[0] != "share.example.com" {
		t.Errorf("ManageTLS domains = %v, want [share.example.com]", managed)
	}
	if got := getJSON[sharingSettingsResponse](t, ts, "/api/settings/sharing"); got.ShareOrigin != "" {
		t.Errorf("a provision whose check was %q saved shareOrigin = %q; want it left unset", res.Check.Status, got.ShareOrigin)
	}
	if !auditHas(t, srv, "sharing.origin_provisioned", "acme https://share.example.com not saved: check says unreachable") {
		t.Error("missing the audit row for a provision that was not saved")
	}
	if auditHas(t, srv, "sharing.origin_changed", "https://share.example.com") {
		t.Error("sharing.origin_changed was written although nothing changed")
	}

	// With the domain reaching this panel, the setting is saved and both rows written.
	tlsPort, pool := shareTLSServer(t, srv.Routes())
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: lookupAlways(sharePublicIP),
		dial:     (&dialByPort{ports: map[string]string{"443": tlsPort}}).dial,
		rootCAs:  pool,
		settle:   []time.Duration{},
	}
	res = postJSON[shareOriginProvisionResult](t, ts, "/api/settings/sharing/provision", `{"origin":"https://share.example.com"}`)
	if res.Mode != "acme" || !res.Check.Ready || res.ShareOrigin != "https://share.example.com" {
		t.Errorf("acme provision result = %+v", res)
	}
	if got := getJSON[sharingSettingsResponse](t, ts, "/api/settings/sharing"); got.ShareOrigin != "https://share.example.com" {
		t.Errorf("saved shareOrigin = %q", got.ShareOrigin)
	}
	if !auditHas(t, srv, "sharing.origin_provisioned", "acme https://share.example.com saved") {
		t.Error("missing sharing.origin_provisioned audit")
	}
	if !auditHas(t, srv, "sharing.origin_changed", "https://share.example.com") {
		t.Error("missing sharing.origin_changed audit")
	}
}

func TestProvisionShareOriginNginxCertbotFlowAndRollback(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.Addr = "127.0.0.1:9884"
	srv.Cfg.TLSMode = config.TLSOff

	tmp := t.TempDir()
	sitesAvail := filepath.Join(tmp, "sites-available")
	sitesEnabled := filepath.Join(tmp, "sites-enabled")
	webroot := filepath.Join(tmp, "webroot")
	leDir := filepath.Join(tmp, "letsencrypt")
	for _, d := range []string{sitesAvail, sitesEnabled, webroot, leDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// What the share domain reaches once it is set up: this panel, over TLS.
	tlsPort, pool := shareTLSServer(t, srv.Routes())

	var calls []string
	failCertbot := false
	streamDump := `
stream {
    map $ssl_preread_server_name $u {
        default 127.0.0.1:14443;
    }
    server {
        listen 443;
        proxy_pass $u;
    }
}
`
	srv.pb.provisioner = &shareOriginProvisioner{
		sitesAvailable: sitesAvail,
		sitesEnabled:   sitesEnabled,
		webroot:        webroot,
		letsencryptDir: leDir,
		nginxBin:       "/fake/nginx",
		certbotBin:     "/fake/certbot",
		lookupIP:       lookupAlways(sharePublicIP),
		dial:           (&dialByPort{ports: map[string]string{"443": tlsPort}}).dial,
		rootCAs:        pool,
		settle:         []time.Duration{},
		runCmd: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if name == "/fake/nginx" && len(args) > 0 && args[0] == "-T" {
				return []byte(streamDump), nil
			}
			if name == "/fake/certbot" {
				if failCertbot {
					return []byte("challenge failed"), errors.New("exit status 1")
				}
				// Simulate certbot writing fullchain.pem + privkey.pem.
				liveDir := filepath.Join(leDir, "live", "share.example.com")
				_ = os.MkdirAll(liveDir, 0o755)
				certPEM, keyPEM, _ := selfSignedCertPEM(t, "share.example.com", time.Now().Add(60*24*time.Hour))
				_ = os.WriteFile(filepath.Join(liveDir, "fullchain.pem"), certPEM, 0o644)
				_ = os.WriteFile(filepath.Join(liveDir, "privkey.pem"), keyPEM, 0o600)
				return []byte("ok"), nil
			}
			return []byte("ok"), nil
		},
	}

	// 1. Certbot failure rolls back newly written vhost + symlink cleanly and does not save setting.
	failCertbot = true
	if code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/provision", "application/json",
		[]byte(`{"origin":"https://share.example.com"}`)); code != http.StatusBadGateway {
		t.Fatalf("certbot failure = %d, want 502", code)
	}
	availFile := filepath.Join(sitesAvail, "vibepanel-share-share.example.com.conf")
	enabledFile := filepath.Join(sitesEnabled, "vibepanel-share-share.example.com.conf")
	if _, err := os.Stat(availFile); !os.IsNotExist(err) {
		t.Errorf("rollback did not remove %s", availFile)
	}
	if _, err := os.Lstat(enabledFile); !os.IsNotExist(err) {
		t.Errorf("rollback did not remove symlink %s", enabledFile)
	}
	if got := getJSON[sharingSettingsResponse](t, ts, "/api/settings/sharing"); got.ShareOrigin != "" {
		t.Errorf("failed provision still saved shareOrigin=%q", got.ShareOrigin)
	}

	// 2. Unmanaged conflicting server_name is refused without touching nginx.
	conflictPath := filepath.Join(sitesEnabled, "existing.conf")
	if err := os.WriteFile(conflictPath, []byte("server {\n  server_name share.example.com;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failCertbot = false
	if code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/provision", "application/json",
		[]byte(`{"origin":"https://share.example.com"}`)); code != http.StatusBadGateway || !strings.Contains(string(body), "already configures server_name") {
		t.Fatalf("conflict = %d (%s), want 502 naming existing site", code, body)
	}
	_ = os.Remove(conflictPath)

	// 3. Full happy path: Phase 1 -> certbot -> Phase 2 using detected 127.0.0.1:14443 ssl http2.
	calls = nil
	res := postJSON[shareOriginProvisionResult](t, ts, "/api/settings/sharing/provision", `{"origin":"https://share.example.com"}`)
	if res.Mode != "nginx-certbot" || res.ShareOrigin != "https://share.example.com" {
		t.Fatalf("provision result = %+v", res)
	}
	confBytes, err := os.ReadFile(availFile)
	if err != nil {
		t.Fatalf("vhost missing: %v", err)
	}
	conf := string(confBytes)
	for _, want := range []string{
		"# Managed by vibepanel",
		"listen 127.0.0.1:14443 ssl http2;",
		"server_name share.example.com;",
		"proxy_pass http://127.0.0.1:9884;",
		"proxy_set_header Host share.example.com;",
		"return 301 https://share.example.com$request_uri;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("generated vhost missing %q:\n%s", want, conf)
		}
	}
	if _, err := os.Lstat(enabledFile); err != nil {
		t.Errorf("enabled symlink missing: %v", err)
	}

	// 4. Re-running with valid cert on disk reuses cert and skips certbot.
	calls = nil
	_ = postJSON[shareOriginProvisionResult](t, ts, "/api/settings/sharing/provision", `{"origin":"https://share.example.com"}`)
	for _, c := range calls {
		if strings.HasPrefix(c, "/fake/certbot") {
			t.Errorf("re-run invoked certbot despite valid cert on disk: %v", calls)
		}
	}

	// 5. Rejects non-https, IP address, and custom port.
	for _, bad := range []string{
		`{"origin":"http://share.example.com"}`,
		`{"origin":"https://203.0.113.10"}`,
		`{"origin":"https://share.example.com:8443"}`,
		`{"origin":"https://panel.example.com"}`,
	} {
		if code, _, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/provision", "application/json", []byte(bad)); code != http.StatusBadRequest {
			t.Errorf("provision %s = %d, want 400", bad, code)
		}
	}
}

func TestTrimCmdErrPrefersCertbotDetail(t *testing.T) {
	sample := `Certbot failed to authenticate some domains (authenticator: webroot). The Certificate Authority reported these problems:
  Domain: share.example.com
  Type:   dns
  Detail: During secondary validation: DNS problem: NXDOMAIN looking up A for share.example.com

Hint: The Certificate Authority failed to download the temporary challenge files created by Certbot. Ensure that the listed domains serve their content from the provided --webroot-path/-w and that files created there can be downloaded from the internet.

Some challenges have failed.
Ask for help or search for solutions at https://community.letsencrypt.org. See the logfile /var/log/letsencrypt/letsencrypt.log or re-run Certbot with -v for more details.
`
	got := trimCmdErr([]byte(sample), errors.New("exit status 1"))
	want := "During secondary validation: DNS problem: NXDOMAIN looking up A for share.example.com"
	if got != want {
		t.Errorf("trimCmdErr = %q, want %q", got, want)
	}
}

// resetOriginCheck clears the sharing/check cooldown. The status tests call
// the check half a dozen times in a second while swapping provisioner stubs;
// real callers read one answer before asking again.
func resetOriginCheck(t *testing.T, srv *Server) {
	t.Helper()
	srv.pb.protect.mu.Lock()
	srv.pb.protect.checkCool = nil
	srv.pb.protect.mu.Unlock()
}

// The check resolves a typed name and probes up to three URLs per call, and
// provision was already held to one at a time. A second check from the same
// address inside two seconds is refused rather than spent.
func TestTheOriginCheckHasACooldown(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return nil, errors.New("no dns in this test")
		},
	}
	code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/check", "application/json",
		[]byte(`{"origin":"https://share.example.com"}`))
	if code != http.StatusOK {
		t.Fatalf("first check = %d (%s), want 200", code, body)
	}
	code, body, _ = sendRaw(t, ts, http.MethodPost, "/api/settings/sharing/check", "application/json",
		[]byte(`{"origin":"https://share.example.com"}`))
	if code != http.StatusTooManyRequests {
		t.Fatalf("immediate second check = %d (%s), want 429", code, body)
	}
}

// The probe is a capability surface like the share routes: an address the
// operator's allowlist excludes cannot reach it. AUDIT.md #1 was this
// omission on another route, paid for once already.
func TestTheProbeRouteHonoursTheAddressAllowlist(t *testing.T) {
	ts, srv := newTestServer(t)
	nets, err := auth.ParseCIDRs([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Auth.Allow = nets
	nonce, _, cleanup, err := srv.pb.probes.mint()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if res, _ := anonGET(t, ts, "/.well-known/vibepanel-probe/"+nonce); res.StatusCode != http.StatusForbidden {
		t.Errorf("probe from an excluded address = %d, want 403", res.StatusCode)
	}
}
