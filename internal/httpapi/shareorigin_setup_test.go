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
	"testing"
	"time"

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
	}
	for _, tc := range cases {
		if got := parseNginxStreamDefault(tc.dump); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCheckShareOriginStatuses(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.TLSMode = config.TLSOff

	// Lockout guard: checking the panel's own domain returns 400.
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
	got := postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"share.example.com"}`)
	if got.Status != "needs_dns" || got.Ready || got.DNSOK {
		t.Errorf("missing DNS: got status=%q ready=%v dnsOk=%v", got.Status, got.Ready, got.DNSOK)
	}
	if got.Origin != "https://share.example.com" || len(got.HostIPs) == 0 || got.HostIPs[0] != "203.0.113.10" {
		t.Errorf("missing DNS origin/hostIps = %q / %v", got.Origin, got.HostIPs)
	}

	// 2. Ready over valid HTTPS with Host forwarded properly.
	certPEM, _, tlsCert := selfSignedCertPEM(t, "share.example.com", time.Now().Add(30*24*time.Hour))
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	tlsSrv := httptest.NewUnstartedServer(srv.Routes())
	tlsSrv.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	tlsSrv.StartTLS()
	defer tlsSrv.Close()

	_, tlsPort, _ := net.SplitHostPort(tlsSrv.Listener.Addr().String())
	makeClient := func(trustPool *x509.CertPool, insecure bool) *http.Client {
		return &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+tlsPort)
				},
				TLSClientConfig: &tls.Config{
					RootCAs:            trustPool,
					InsecureSkipVerify: insecure, //nolint:gosec // test server
				},
			},
		}
	}

	srv.pb.provisioner = &shareOriginProvisioner{
		lookupIP: func(_ context.Context, host string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
		},
		probeClient: func(insecure bool) *http.Client {
			return makeClient(pool, insecure)
		},
		runCmd: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, errors.New("no nginx")
		},
	}
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if !got.Ready || got.Status != "ready" || !got.TLSOK || !got.Reachable || !got.HostForwarded || !got.DNSMatchesHost {
		t.Errorf("ready check = %+v", got)
	}

	// 3. Untrusted / wrong-domain TLS cert -> needs_https with insecure reachability confirmed.
	emptyPool := x509.NewCertPool()
	srv.pb.provisioner.probeClient = func(insecure bool) *http.Client {
		return makeClient(emptyPool, insecure)
	}
	srv.pb.provisioner.httpProbeURL = func(_, nonce string) string {
		return ts.URL + "/.well-known/vibepanel-probe/" + nonce
	}
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if got.Status != "needs_https" || got.Ready || got.TLSOK || !got.Reachable || !got.HTTPPortOpen {
		t.Errorf("needs_https check = %+v", got)
	}

	// 4. Proxy reaches this panel but strips/rewrites Host header -> host_mismatch.
	rewriteProxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1:9884"
		srv.Routes().ServeHTTP(w, r)
	}))
	rewriteProxy.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	rewriteProxy.StartTLS()
	defer rewriteProxy.Close()
	_, rwPort, _ := net.SplitHostPort(rewriteProxy.Listener.Addr().String())
	srv.pb.provisioner.probeClient = func(insecure bool) *http.Client {
		return &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+rwPort)
				},
				TLSClientConfig: &tls.Config{RootCAs: pool, InsecureSkipVerify: insecure}, //nolint:gosec
			},
		}
	}
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if got.Status != "host_mismatch" || got.Ready || !got.Reachable || got.HostForwarded || got.SeenHost != "127.0.0.1:9884" {
		t.Errorf("host_mismatch check = %+v", got)
	}

	// 5. Foreign server answering HTTP 200 (not this panel) -> needs_proxy.
	foreignSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("welcome to nginx"))
	}))
	foreignSrv.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	foreignSrv.StartTLS()
	defer foreignSrv.Close()
	_, fPort, _ := net.SplitHostPort(foreignSrv.Listener.Addr().String())
	srv.pb.provisioner.probeClient = func(insecure bool) *http.Client {
		return &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+fPort)
				},
				TLSClientConfig: &tls.Config{RootCAs: pool, InsecureSkipVerify: insecure}, //nolint:gosec
			},
		}
	}
	got = postJSON[shareOriginCheckResponse](t, ts, "/api/settings/sharing/check", `{"origin":"https://share.example.com"}`)
	if got.Status != "needs_proxy" || got.Ready || got.Reachable {
		t.Errorf("needs_proxy check = %+v", got)
	}
}

func TestProvisionShareOriginACME(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Cfg.Domain = "panel.example.com"
	srv.Cfg.TLSMode = config.TLSACME

	var managed []string
	srv.ManageTLS = func(_ context.Context, domain string) error {
		managed = append(managed, domain)
		return nil
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

	// With DNS resolving, ManageTLS is invoked, setting saved, and both audit events recorded.
	srv.pb.provisioner.lookupIP = func(_ context.Context, _ string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
	}
	srv.pb.provisioner.probeClient = func(bool) *http.Client {
		return &http.Client{
			Timeout: time.Second,
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection refused")
			}),
		}
	}
	res := postJSON[shareOriginProvisionResult](t, ts, "/api/settings/sharing/provision", `{"origin":"https://share.example.com"}`)
	if res.Mode != "acme" || res.ShareOrigin != "https://share.example.com" {
		t.Errorf("acme provision result = %+v", res)
	}
	if len(managed) != 1 || managed[0] != "share.example.com" {
		t.Errorf("ManageTLS domains = %v, want [share.example.com]", managed)
	}
	if got := getJSON[sharingSettingsResponse](t, ts, "/api/settings/sharing"); got.ShareOrigin != "https://share.example.com" {
		t.Errorf("saved shareOrigin = %q", got.ShareOrigin)
	}
	if !auditHas(t, srv, "sharing.origin_provisioned", "acme https://share.example.com") {
		t.Error("missing sharing.origin_provisioned audit")
	}
	if !auditHas(t, srv, "sharing.origin_changed", "https://share.example.com") {
		t.Error("missing sharing.origin_changed audit")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
		lookupIP: func(_ context.Context, _ string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
		},
		probeClient: func(bool) *http.Client {
			return &http.Client{
				Timeout: time.Second,
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("offline test")
				}),
			}
		},
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
		"proxy_set_header Host $host;",
		"return 301 https://$host$request_uri;",
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
