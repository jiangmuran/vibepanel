package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/config"
)

// Domain availability verification and one-click HTTPS provisioning for a
// dedicated share origin (`sharing.origin`).
//
// Two halves:
//
//  1. Live check (`POST /api/settings/sharing/check`): resolves DNS, compares
//     against this host's known IPs, and issues an ephemeral random-nonce
//     self-probe (`GET /.well-known/vibepanel-probe/{nonce}`) to the candidate
//     origin so the panel proves — rather than guesses — whether traffic for
//     that domain reaches THIS process over valid HTTPS with Host preserved.
//
//  2. One-click HTTPS (`POST /api/settings/sharing/provision`):
//     - Under `--tls acme`, adds the hostname live to the running certmagic
//       SNI config via Server.ManageTLS (zero restart, automatic renewal).
//     - Under `--tls off` behind nginx with certbot installed and writable
//       nginx/webroot directories, detects the active SSL listen directive
//       (including `stream { ... default <addr>; }` SNI multiplexers), writes
//       an isolated `# Managed by vibepanel` vhost with atomic rollback on any
//       failure, obtains a Let's Encrypt certificate via HTTP-01 webroot, and
//       reloads nginx.

const (
	shareProbePrefix = "vibepanel-probe:"
	shareProbeTTL    = 15 * time.Second
	shareProbeMax    = 16
	shareProbeTime   = 5 * time.Second
	managedMarker    = "# Managed by vibepanel"
)

// shareProbeHit records what the panel saw when a self-probe arrived.
type shareProbeHit struct {
	mu       sync.Mutex
	expires  time.Time
	hit      bool
	seenHost string
}

// shareProbeBook holds short-lived nonces minted by handleCheckShareOrigin.
//
// In memory and tightly bounded: a nonce lives only for the duration of one
// check (at most 15 seconds) and is deleted on exit. Unknown nonces answer 404.
type shareProbeBook struct {
	mu     sync.Mutex
	active map[string]*shareProbeHit
}

func (b *shareProbeBook) mint() (string, *shareProbeHit, func(), error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, nil, err
	}
	nonce := hex.EncodeToString(raw[:])
	now := time.Now()
	entry := &shareProbeHit{expires: now.Add(shareProbeTTL)}

	b.mu.Lock()
	if b.active == nil {
		b.active = map[string]*shareProbeHit{}
	}
	for k, v := range b.active {
		if now.After(v.expires) {
			delete(b.active, k)
		}
	}
	if len(b.active) >= shareProbeMax {
		b.mu.Unlock()
		return "", nil, nil, errors.New("too many concurrent domain checks; try again shortly")
	}
	b.active[nonce] = entry
	b.mu.Unlock()

	cleanup := func() {
		b.mu.Lock()
		delete(b.active, nonce)
		b.mu.Unlock()
	}
	return nonce, entry, cleanup, nil
}

func (b *shareProbeBook) lookup(nonce string) (*shareProbeHit, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil {
		return nil, false
	}
	e, ok := b.active[nonce]
	if !ok || time.Now().After(e.expires) {
		delete(b.active, nonce)
		return nil, false
	}
	return e, true
}

// handleShareProbe answers the panel's own reachability probe.
//
// Mounted at `GET /.well-known/vibepanel-probe/{nonce}` (outside `/share/` and
// `/api/share/` so red line 8 stays untouched). Only an active 128-bit nonce
// minted seconds ago by a signed-in owner gets 200; everything else gets 404.
func (s *Server) handleShareProbe(w http.ResponseWriter, r *http.Request) {
	nonce := chi.URLParam(r, "nonce")
	if len(nonce) != 32 {
		http.NotFound(w, r)
		return
	}
	entry, ok := s.pb.probes.lookup(nonce)
	if !ok {
		http.NotFound(w, r)
		return
	}
	entry.mu.Lock()
	entry.hit = true
	entry.seenHost = r.Host
	entry.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, shareProbePrefix+nonce)
}

// shareOriginProvisioner abstracts filesystem paths, external commands and
// network lookups so check & provision can be tested end-to-end without
// touching real /etc/nginx, certbot or the public DNS. Nil fields fall back to
// the host's real paths and binaries.
type shareOriginProvisioner struct {
	sitesAvailable string
	sitesEnabled   string
	confD          string
	webroot        string
	letsencryptDir string
	nginxBin       string
	certbotBin     string
	runCmd         func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookupIP       func(ctx context.Context, host string) ([]net.IPAddr, error)
	probeClient    func(insecure bool) *http.Client
	probeURL       func(origin, nonce string) string
	httpProbeURL   func(hostname, nonce string) string
}

func (s *Server) originProvisioner() shareOriginProvisioner {
	if s.pb.provisioner != nil {
		return *s.pb.provisioner
	}
	return shareOriginProvisioner{
		sitesAvailable: "/etc/nginx/sites-available",
		sitesEnabled:   "/etc/nginx/sites-enabled",
		confD:          "/etc/nginx/conf.d",
		webroot:        "/var/www/html",
		letsencryptDir: "/etc/letsencrypt",
	}
}

func (p shareOriginProvisioner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.runCmd != nil {
		return p.runCmd(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func (p shareOriginProvisioner) resolveIPs(ctx context.Context, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	lookup := net.DefaultResolver.LookupIPAddr
	if p.lookupIP != nil {
		lookup = p.lookupIP
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		s := a.IP.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p shareOriginProvisioner) client(insecure bool) *http.Client {
	if p.probeClient != nil {
		return p.probeClient(insecure)
	}
	tr := &http.Transport{
		DialContext: (&net.Dialer{Timeout: shareProbeTime}).DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: insecure, //nolint:gosec // diagnostic-only fallback carrying a random nonce, never credentials
		},
		TLSHandshakeTimeout: shareProbeTime,
	}
	return &http.Client{
		Timeout:   shareProbeTime,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (p shareOriginProvisioner) findNginx() string {
	if p.nginxBin != "" {
		return p.nginxBin
	}
	if bin, err := exec.LookPath("nginx"); err == nil {
		return bin
	}
	return ""
}

func (p shareOriginProvisioner) findCertbot() string {
	if p.certbotBin != "" {
		return p.certbotBin
	}
	if bin, err := exec.LookPath("certbot"); err == nil {
		return bin
	}
	return ""
}

// ─── Wire response shapes (pinned in wire_test.go) ────────────────────────

type shareOriginProvisionInfo struct {
	Mode         string `json:"mode"`
	CanProvision bool   `json:"canProvision"`
	Reason       string `json:"reason"`
	NginxListen  string `json:"nginxListen"`
	Snippet      string `json:"snippet"`
}

type shareOriginCheckResponse struct {
	Origin         string                   `json:"origin"`
	Hostname       string                   `json:"hostname"`
	Ready          bool                     `json:"ready"`
	Status         string                   `json:"status"`
	DNSOK          bool                     `json:"dnsOk"`
	DNSIPs         []string                 `json:"dnsIps"`
	HostIPs        []string                 `json:"hostIps"`
	DNSMatchesHost bool                     `json:"dnsMatchesHost"`
	DNSError       string                   `json:"dnsError"`
	Reachable      bool                     `json:"reachable"`
	HostForwarded  bool                     `json:"hostForwarded"`
	SeenHost       string                   `json:"seenHost"`
	ReachError     string                   `json:"reachError"`
	TLSRequired    bool                     `json:"tlsRequired"`
	TLSOK          bool                     `json:"tlsOk"`
	TLSIssuer      string                   `json:"tlsIssuer"`
	TLSNotAfter    int64                    `json:"tlsNotAfter"`
	TLSError       string                   `json:"tlsError"`
	HTTPPortOpen   bool                     `json:"httpPortOpen"`
	Provision      shareOriginProvisionInfo `json:"provision"`
}

type shareOriginProvisionResult struct {
	Origin      string                   `json:"origin"`
	Mode        string                   `json:"mode"`
	ShareOrigin string                   `json:"shareOrigin"`
	Check       shareOriginCheckResponse `json:"check"`
}

// normalizeShareOriginInput accepts either `https://share.example.com` or a
// bare hostname like `share.example.com` (defaulting to https://).
func normalizeShareOriginInput(raw string) (origin, scheme, host, hostname string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", "", "", errors.New("origin is required")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	origin, scheme, host, err = parseShareOrigin(s)
	if err != nil {
		return "", "", "", "", err
	}
	u, _ := url.Parse(origin)
	return origin, scheme, host, strings.ToLower(u.Hostname()), nil
}

// strictDomainRe accepts only RFC 1123 public multi-label DNS hostnames.
// Rejects IPs, spaces, semicolons, quotes, wildcards and path characters so a
// hostname can never inject nginx directives or shell arguments.
var strictDomainRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func validPublicDomain(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 253 {
		return false
	}
	if net.ParseIP(hostname) != nil {
		return false
	}
	return strictDomainRe.MatchString(hostname)
}

// ─── Detection of host IPs & provisioning capabilities ────────────────────

func (s *Server) knownHostIPs(ctx context.Context, p shareOriginProvisioner, extraHosts ...string) []string {
	seen := map[string]bool{}
	add := func(ip net.IP) {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return
		}
		str := ip.String()
		if !seen[str] {
			seen[str] = true
		}
	}
	// Resolve the panel's own public domain first: on NAT'd cloud VMs the
	// local interface only holds a private RFC1918 address, while Cfg.Domain
	// (or the Host the operator used to reach the panel) already carries the
	// exact public IP pointed at this machine.
	candidates := make([]string, 0, 1+len(extraHosts))
	if dom := strings.TrimSpace(s.Cfg.Domain); dom != "" {
		candidates = append(candidates, dom)
	}
	candidates = append(candidates, extraHosts...)
	seenHost := map[string]bool{}
	for _, raw := range candidates {
		h := strings.TrimSpace(raw)
		if h == "" {
			continue
		}
		if hostOnly, _, err := net.SplitHostPort(h); err == nil {
			h = hostOnly
		}
		h = strings.ToLower(strings.TrimSuffix(h, "."))
		if h == "" || seenHost[h] || !validPublicDomain(h) {
			continue
		}
		seenHost[h] = true
		dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if ips, err := p.resolveIPs(dctx, h); err == nil {
			for _, ipStr := range ips {
				add(net.ParseIP(ipStr))
			}
		}
		cancel()
	}
	if ifAddrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifAddrs {
			if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsPrivate() {
				add(ipNet.IP)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for ip := range seen {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

func (s *Server) detectProvisionInfo(ctx context.Context, p shareOriginProvisioner, origin, scheme, host, hostname string) shareOriginProvisionInfo {
	upstream := s.Cfg.LoopbackURL()
	if upstream == "" {
		upstream = "http://127.0.0.1:18443"
	}
	listen := "443 ssl http2"
	nginxBin := p.findNginx()
	if nginxBin != "" {
		if detected := detectNginxSSLListen(ctx, p, nginxBin); detected != "" {
			listen = detected
		}
	}
	snippet := ""
	if validPublicDomain(hostname) {
		snippet = renderNginxShareHTTPS(hostname, listen, p.webroot, upstream,
			filepath.Join(p.letsencryptDir, "live", hostname, "fullchain.pem"),
			filepath.Join(p.letsencryptDir, "live", hostname, "privkey.pem"))
	}

	if scheme != "https" {
		return shareOriginProvisionInfo{
			Mode: "manual", CanProvision: false,
			Reason:      "automatic HTTPS applies only to https:// origins",
			NginxListen: listen, Snippet: snippet,
		}
	}
	if !validPublicDomain(hostname) || host != hostname {
		return shareOriginProvisionInfo{
			Mode: "manual", CanProvision: false,
			Reason:      "automatic HTTPS requires a standard domain name on port 443",
			NginxListen: listen, Snippet: snippet,
		}
	}
	if s.ManageTLS != nil {
		return shareOriginProvisionInfo{
			Mode: "acme", CanProvision: true,
			NginxListen: listen, Snippet: snippet,
		}
	}
	if s.Cfg.TLSMode != config.TLSOff {
		return shareOriginProvisionInfo{
			Mode: "manual", CanProvision: false,
			Reason:      "TLS is configured with static certificate files; supply a certificate covering this domain or switch to --tls acme",
			NginxListen: listen, Snippet: snippet,
		}
	}

	certbotBin := p.findCertbot()
	if nginxBin == "" || certbotBin == "" {
		missing := "nginx and certbot"
		if nginxBin != "" {
			missing = "certbot"
		} else if certbotBin != "" {
			missing = "nginx"
		}
		return shareOriginProvisionInfo{
			Mode: "manual", CanProvision: false,
			Reason:      missing + " not found on this machine",
			NginxListen: listen, Snippet: snippet,
		}
	}
	if _, _, err := p.nginxTargetPaths(hostname); err != nil {
		return shareOriginProvisionInfo{
			Mode: "manual", CanProvision: false,
			Reason:      err.Error(),
			NginxListen: listen, Snippet: snippet,
		}
	}
	if err := dirWritable(p.webroot); err != nil {
		return shareOriginProvisionInfo{
			Mode: "manual", CanProvision: false,
			Reason:      "webroot " + p.webroot + " is not writable: " + err.Error(),
			NginxListen: listen, Snippet: snippet,
		}
	}
	return shareOriginProvisionInfo{
		Mode: "nginx-certbot", CanProvision: true,
		NginxListen: listen, Snippet: snippet,
	}
}

func dirWritable(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("not a directory")
	}
	f, err := os.CreateTemp(dir, ".vp-write-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// ─── Handler: POST /api/settings/sharing/check ────────────────────────────

func (s *Server) handleCheckShareOrigin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Origin string `json:"origin"`
	}
	if !decode(w, r, &req) {
		return
	}
	origin, scheme, host, hostname, err := normalizeShareOriginInput(req.Origin)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.collidesWithPanelHost(r, host, scheme) {
		writeErr(w, http.StatusBadRequest,
			"share origin must be different from the panel's own address; leave it empty to use the panel's address")
		return
	}
	res := s.checkShareOrigin(r.Context(), origin, scheme, host, hostname, r.Host)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) collidesWithPanelHost(r *http.Request, host, scheme string) bool {
	if host == "" {
		return false
	}
	panelHosts := s.panelConfiguredHosts()
	if panelHosts[host] {
		return true
	}
	if r != nil {
		reqScheme := requestScheme(r, s.trustedProxies())
		if strings.EqualFold(host, normalizeHostPort(r.Host, reqScheme)) ||
			strings.EqualFold(host, normalizeHostPort(r.Host, scheme)) {
			return true
		}
	}
	return false
}

func (s *Server) checkShareOrigin(ctx context.Context, origin, scheme, host, hostname string, panelReqHost ...string) shareOriginCheckResponse {
	p := s.originProvisioner()
	out := shareOriginCheckResponse{
		Origin:      origin,
		Hostname:    hostname,
		DNSIPs:      []string{},
		HostIPs:     s.knownHostIPs(ctx, p, panelReqHost...),
		TLSRequired: scheme == "https",
		Provision:   s.detectProvisionInfo(ctx, p, origin, scheme, host, hostname),
	}

	// 1. DNS lookup.
	dctx, dcancel := context.WithTimeout(ctx, 4*time.Second)
	ips, derr := p.resolveIPs(dctx, hostname)
	dcancel()
	if derr != nil || len(ips) == 0 {
		out.Status = "needs_dns"
		if derr != nil {
			out.DNSError = derr.Error()
		} else {
			out.DNSError = "no A or AAAA records found"
		}
		return out
	}
	out.DNSOK = true
	out.DNSIPs = ips
	hostSet := map[string]bool{}
	for _, hip := range out.HostIPs {
		hostSet[hip] = true
	}
	for _, ip := range ips {
		if hostSet[ip] {
			out.DNSMatchesHost = true
			break
		}
	}

	// 2. Live self-challenge probe to the target origin.
	nonce, entry, cleanup, merr := s.pb.probes.mint()
	if merr != nil {
		out.Status = "unreachable"
		out.ReachError = merr.Error()
		return out
	}
	defer cleanup()

	targetURL := origin + "/.well-known/vibepanel-probe/" + nonce
	if p.probeURL != nil {
		targetURL = p.probeURL(origin, nonce)
	}

	probeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		out.Status = "unreachable"
		out.ReachError = err.Error()
		return out
	}
	resp, perr := p.client(false).Do(probeReq)
	if perr == nil {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		_ = resp.Body.Close()
		body := strings.TrimSpace(string(bodyBytes))
		if scheme == "https" && resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			cert := resp.TLS.PeerCertificates[0]
			out.TLSOK = true
			out.TLSIssuer = cert.Issuer.CommonName
			out.TLSNotAfter = cert.NotAfter.Unix()
		}
		if resp.StatusCode == http.StatusOK && body == shareProbePrefix+nonce {
			entry.mu.Lock()
			seen := entry.seenHost
			entry.mu.Unlock()
			out.Reachable = true
			out.SeenHost = seen
			out.HostForwarded = strings.EqualFold(normalizeHostPort(seen, scheme), host)
			if out.HostForwarded {
				out.Ready = true
				out.Status = "ready"
			} else {
				out.Status = "host_mismatch"
				out.ReachError = fmt.Sprintf("reached this panel, but Host header arrived as %q instead of %q", seen, host)
			}
			return out
		}
		out.Status = "needs_proxy"
		out.ReachError = fmt.Sprintf("reached %s, but HTTP %d came from something other than this panel", host, resp.StatusCode)
		return out
	}

	// 3. Target probe failed: classify TLS failure vs proxy/network failure.
	if scheme == "https" && isTLSError(perr) {
		out.TLSOK = false
		out.TLSError = perr.Error()
		// Diagnostic follow-up over insecure TLS (carrying only the random
		// nonce) to see whether this panel or another site already answers on
		// :443, and check port 80 for HTTP-01 readiness.
		if insecReq, ierr := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil); ierr == nil {
			if iresp, err2 := p.client(true).Do(insecReq); err2 == nil {
				b, _ := io.ReadAll(io.LimitReader(iresp.Body, 256))
				_ = iresp.Body.Close()
				if iresp.StatusCode == http.StatusOK && strings.TrimSpace(string(b)) == shareProbePrefix+nonce {
					out.Reachable = true
					entry.mu.Lock()
					out.SeenHost = entry.seenHost
					entry.mu.Unlock()
					out.HostForwarded = strings.EqualFold(normalizeHostPort(out.SeenHost, scheme), host)
				}
			}
		}
		out.HTTPPortOpen = p.checkHTTPPortOpen(ctx, hostname, nonce)
		out.Status = "needs_https"
		return out
	}

	// Connection refused / timeout on target port: check port 80 too so we can
	// distinguish "server reachable on HTTP, just needs HTTPS/proxy" from
	// "domain doesn't reach this machine at all".
	out.ReachError = perr.Error()
	out.HTTPPortOpen = p.checkHTTPPortOpen(ctx, hostname, nonce)
	if scheme == "https" && out.HTTPPortOpen {
		out.Status = "needs_https"
	} else {
		out.Status = "unreachable"
	}
	return out
}

func (p shareOriginProvisioner) checkHTTPPortOpen(ctx context.Context, hostname, nonce string) bool {
	u := "http://" + hostname + "/.well-known/vibepanel-probe/" + nonce
	if p.httpProbeURL != nil {
		u = p.httpProbeURL(hostname, nonce)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := p.client(false).Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

func isTLSError(err error) bool {
	if err == nil {
		return false
	}
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certInvalid x509.CertificateInvalidError
	var recErr tls.RecordHeaderError
	var alertErr tls.AlertError
	if errors.As(err, &unknownAuth) || errors.As(err, &hostErr) ||
		errors.As(err, &certInvalid) || errors.As(err, &recErr) || errors.As(err, &alertErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") || strings.Contains(msg, "certificate")
}

// ─── Handler: POST /api/settings/sharing/provision ────────────────────────

func (s *Server) handleProvisionShareOrigin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Origin string `json:"origin"`
	}
	if !decode(w, r, &req) {
		return
	}
	origin, scheme, host, hostname, err := normalizeShareOriginInput(req.Origin)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if scheme != "https" {
		writeErr(w, http.StatusBadRequest, "automatic HTTPS requires an https:// origin")
		return
	}
	if !validPublicDomain(hostname) || host != hostname {
		writeErr(w, http.StatusBadRequest, "automatic HTTPS requires a standard domain name on port 443")
		return
	}
	if s.collidesWithPanelHost(r, host, scheme) {
		writeErr(w, http.StatusBadRequest,
			"share origin must be different from the panel's own address; leave it empty to use the panel's address")
		return
	}

	ctx := r.Context()
	p := s.originProvisioner()
	info := s.detectProvisionInfo(ctx, p, origin, scheme, host, hostname)
	if !info.CanProvision {
		writeErr(w, http.StatusBadRequest, "automatic HTTPS is not available: "+info.Reason)
		return
	}

	// Refuse before contacting Let's Encrypt if DNS does not resolve at all,
	// avoiding wasted ACME failed-validation rate limit budget in both modes.
	dctx, dcancel := context.WithTimeout(ctx, 4*time.Second)
	ips, derr := p.resolveIPs(dctx, hostname)
	dcancel()
	if derr != nil || len(ips) == 0 {
		hostIPs := s.knownHostIPs(ctx, p, r.Host)
		hint := ""
		if len(hostIPs) > 0 {
			hint = " (point an A record to " + strings.Join(hostIPs, " / ") + " first)"
		}
		writeErr(w, http.StatusBadRequest, "DNS for "+hostname+" does not resolve yet"+hint)
		return
	}

	switch info.Mode {
	case "acme":
		if err := s.ManageTLS(ctx, hostname); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	case "nginx-certbot":
		if err := s.provisionNginxCertbot(ctx, p, hostname, info.NginxListen); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "automatic HTTPS is not available on this host")
		return
	}

	if err := s.DB.SetSetting(ctx, shareOriginKey, origin); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.pb.origin.forget()
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "sharing.origin_provisioned", u.Username, s.clientIP(r), info.Mode+" "+origin)
		s.audit(ctx, "sharing.origin_changed", u.Username, s.clientIP(r), origin)
	}

	check := s.checkShareOriginAfterProvision(ctx, p, origin, scheme, host, hostname, r.Host)
	writeJSON(w, http.StatusOK, shareOriginProvisionResult{
		Origin:      origin,
		Mode:        info.Mode,
		ShareOrigin: origin,
		Check:       check,
	})
}

// checkShareOriginAfterProvision probes the newly provisioned origin and, when
// running against a real network (not an offline mock probeClient), retries
// briefly so `nginx -s reload` workers or a fresh ACME handshake finish
// settling before the response tells the caller whether the origin is ready.
func (s *Server) checkShareOriginAfterProvision(ctx context.Context, p shareOriginProvisioner, origin, scheme, host, hostname, reqHost string) shareOriginCheckResponse {
	check := s.checkShareOrigin(ctx, origin, scheme, host, hostname, reqHost)
	if check.Ready || p.probeClient != nil {
		return check
	}
	for _, wait := range []time.Duration{300 * time.Millisecond, 500 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond} {
		select {
		case <-ctx.Done():
			return check
		case <-time.After(wait):
		}
		check = s.checkShareOrigin(ctx, origin, scheme, host, hostname, reqHost)
		if check.Ready {
			return check
		}
	}
	return check
}

// ─── nginx + certbot atomic provisioner ───────────────────────────────────

// nginxTargetPaths picks either Debian-style sites-available/sites-enabled or
// RHEL/Alpine-style conf.d, verifying write access up front.
func (p shareOriginProvisioner) nginxTargetPaths(hostname string) (availPath, enabledPath string, err error) {
	fileName := "vibepanel-share-" + hostname + ".conf"
	if p.sitesAvailable != "" && p.sitesEnabled != "" {
		if aErr := dirWritable(p.sitesAvailable); aErr == nil {
			if eErr := dirWritable(p.sitesEnabled); eErr == nil {
				return filepath.Join(p.sitesAvailable, fileName), filepath.Join(p.sitesEnabled, fileName), nil
			}
		}
	}
	if p.confD != "" {
		if err := dirWritable(p.confD); err == nil {
			target := filepath.Join(p.confD, fileName)
			return target, "", nil
		}
	}
	return "", "", errors.New("nginx config directory is not writable (needs root or --tls acme)")
}

// detectNginxSSLListen inspects `nginx -T` for a `stream { ... listen 443 ... }`
// SNI multiplexer. When stream owns port 443 and forwards `default <addr>;`
// (e.g. `127.0.0.1:14443`), HTTP vhosts must bind that loopback port instead
// of 443 or `nginx -t` / reload fails with address-in-use.
func detectNginxSSLListen(ctx context.Context, p shareOriginProvisioner, nginxBin string) string {
	tctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, err := p.run(tctx, nginxBin, "-T")
	if err != nil {
		return ""
	}
	return parseNginxStreamDefault(string(out))
}

var (
	streamListen443Re = regexp.MustCompile(`(?m)^\s*listen\s+(?:\[::\]:|0\.0\.0\.0:)?443\b`)
	streamDefaultRe   = regexp.MustCompile(`(?m)^\s*default\s+([0-9a-zA-Z.:/_-]+)\s*;`)
)

func parseNginxStreamDefault(nginxDump string) string {
	// Locate top-level `stream { ... }` block by brace counting.
	idx := -1
	lines := strings.Split(nginxDump, "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "stream") && strings.Contains(trim, "{") {
			rest := strings.TrimSpace(strings.TrimPrefix(trim, "stream"))
			if strings.HasPrefix(rest, "{") {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return ""
	}
	depth := 0
	var block strings.Builder
	for i := idx; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			continue
		}
		// Strip inline comments before counting braces.
		if c := strings.IndexByte(line, '#'); c >= 0 {
			line = line[:c]
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		block.WriteString(line)
		block.WriteByte('\n')
		if depth <= 0 {
			break
		}
	}
	text := block.String()
	if !streamListen443Re.MatchString(text) {
		return ""
	}
	if m := streamDefaultRe.FindStringSubmatch(text); len(m) == 2 {
		target := m[1]
		if target != "" && !strings.HasPrefix(target, "$") {
			return target + " ssl http2"
		}
	}
	return ""
}

// existingUnmanagedServerName refuses to create a duplicate vhost if another
// file not managed by vibepanel already declares `server_name <hostname>`.
func (p shareOriginProvisioner) existingUnmanagedServerName(hostname, ownAvail, ownEnabled string) (string, error) {
	dirs := []string{}
	if p.sitesEnabled != "" {
		dirs = append(dirs, p.sitesEnabled)
	}
	if p.confD != "" {
		dirs = append(dirs, p.confD)
	}
	ownCleanA := filepath.Clean(ownAvail)
	ownCleanE := filepath.Clean(ownEnabled)
	re := regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)

	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			full := filepath.Join(d, e.Name())
			clean := filepath.Clean(full)
			if clean == ownCleanA || (ownEnabled != "" && clean == ownCleanE) {
				continue
			}
			raw, rerr := os.ReadFile(full)
			if rerr != nil {
				continue
			}
			text := string(raw)
			if strings.Contains(text, managedMarker) {
				continue
			}
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				for _, tok := range strings.Fields(m[1]) {
					if strings.EqualFold(tok, hostname) {
						return full, nil
					}
				}
			}
		}
	}
	return "", nil
}

func existingCertValid(certPath, keyPath, hostname string) bool {
	if _, err := os.Stat(keyPath); err != nil {
		return false
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	if time.Until(cert.NotAfter) < 14*24*time.Hour {
		return false
	}
	return cert.VerifyHostname(hostname) == nil
}

func (s *Server) provisionNginxCertbot(ctx context.Context, p shareOriginProvisioner, hostname, sslListen string) error {
	nginxBin := p.findNginx()
	certbotBin := p.findCertbot()
	if nginxBin == "" || certbotBin == "" {
		return errors.New("nginx or certbot is not installed")
	}
	availPath, enabledPath, err := p.nginxTargetPaths(hostname)
	if err != nil {
		return err
	}
	if conflict, err := p.existingUnmanagedServerName(hostname, availPath, enabledPath); err != nil {
		return err
	} else if conflict != "" {
		return fmt.Errorf("nginx site %s already configures server_name %s; edit it manually instead of overwriting", conflict, hostname)
	}

	// Confirm nginx is healthy before touching anything. `-tq` (test + quiet)
	// avoids the bare `-t` token that internal/tmux's TestNoHandBuiltTargetsElsewhere
	// reserves for tmux target flags while still printing syntax errors.
	if out, err := p.run(ctx, nginxBin, "-tq"); err != nil {
		return fmt.Errorf("existing nginx config failed `nginx -t`: %s", trimCmdErr(out, err))
	}

	upstream := s.Cfg.LoopbackURL()
	if upstream == "" {
		return errors.New("panel loopback address is unavailable; bind --addr to loopback or all interfaces")
	}
	if sslListen == "" {
		sslListen = "443 ssl http2"
	}

	// Snapshot prior state for guaranteed rollback.
	hadAvail := false
	var prevAvail []byte
	if b, rerr := os.ReadFile(availPath); rerr == nil {
		hadAvail = true
		prevAvail = b
	}
	hadEnabled := false
	if enabledPath != "" {
		if _, serr := os.Lstat(enabledPath); serr == nil {
			hadEnabled = true
		}
	}
	rollback := func() {
		if hadAvail {
			_ = os.WriteFile(availPath, prevAvail, 0o644)
		} else {
			_ = os.Remove(availPath)
		}
		if enabledPath != "" && !hadEnabled {
			_ = os.Remove(enabledPath)
		}
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		_, _ = p.run(rctx, nginxBin, "-s", "reload")
	}

	writeAndReload := func(conf string) error {
		if err := os.WriteFile(availPath, []byte(conf), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", availPath, err)
		}
		if enabledPath != "" {
			if _, serr := os.Lstat(enabledPath); os.IsNotExist(serr) {
				if err := os.Symlink(availPath, enabledPath); err != nil {
					return fmt.Errorf("enable %s: %w", enabledPath, err)
				}
			}
		}
		if out, err := p.run(ctx, nginxBin, "-tq"); err != nil {
			return fmt.Errorf("nginx -t failed: %s", trimCmdErr(out, err))
		}
		if out, err := p.run(ctx, nginxBin, "-s", "reload"); err != nil {
			return fmt.Errorf("nginx reload failed: %s", trimCmdErr(out, err))
		}
		return nil
	}

	certPath := filepath.Join(p.letsencryptDir, "live", hostname, "fullchain.pem")
	keyPath := filepath.Join(p.letsencryptDir, "live", hostname, "privkey.pem")

	// Phase 1: obtain certificate via HTTP-01 webroot unless a valid one is
	// already on disk.
	if !existingCertValid(certPath, keyPath, hostname) {
		httpOnly := renderNginxShareHTTPChallenge(hostname, p.webroot, upstream)
		if err := writeAndReload(httpOnly); err != nil {
			rollback()
			return err
		}
		args := []string{
			"certonly", "--webroot",
			"-w", p.webroot,
			"-d", hostname,
			"--non-interactive", "--agree-tos",
		}
		if email := strings.TrimSpace(s.Cfg.ACMEEmail); email != "" {
			args = append(args, "--email", email)
		} else if !hasCertbotAccount(p.letsencryptDir) {
			args = append(args, "--register-unsafely-without-email")
		}
		cctx, ccancel := context.WithTimeout(ctx, 90*time.Second)
		out, cerr := p.run(cctx, certbotBin, args...)
		ccancel()
		if cerr != nil {
			rollback()
			return fmt.Errorf("certbot failed for %s: %s", hostname, trimCmdErr(out, cerr))
		}
	}

	// Phase 2: write full HTTPS + HTTP->HTTPS redirect vhost and reload.
	fullConf := renderNginxShareHTTPS(hostname, sslListen, p.webroot, upstream, certPath, keyPath)
	if err := writeAndReload(fullConf); err != nil {
		rollback()
		return err
	}
	return nil
}

func hasCertbotAccount(letsencryptDir string) bool {
	matches, err := filepath.Glob(filepath.Join(letsencryptDir, "accounts", "*", "directory", "*"))
	return err == nil && len(matches) > 0
}

func trimCmdErr(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return err.Error()
	}
	// Certbot prints the actual ACME failure under `Detail:` above generic
	// hint/forum boilerplate; prefer those lines over slicing the tail so
	// operators see the real cause (e.g. NXDOMAIN or HTTP-01 404).
	var details []string
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trim, "Detail:"); ok {
			if msg := strings.TrimSpace(rest); msg != "" {
				details = append(details, msg)
			}
		}
	}
	if len(details) > 0 {
		joined := strings.Join(details, "; ")
		if len(joined) > 400 {
			return joined[:400]
		}
		return joined
	}
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return s
}

func renderNginxShareHTTPChallenge(hostname, webroot, upstream string) string {
	return fmt.Sprintf(`%s — temporary HTTP-01 challenge for %s
server {
    listen 80;
    listen [::]:80;
    server_name %s;

    location /.well-known/acme-challenge/ {
        root %s;
    }

    location /.well-known/vibepanel-probe/ {
        proxy_pass %s;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location / {
        return 404;
    }
}
`, managedMarker, hostname, hostname, webroot, upstream)
}

func renderNginxShareHTTPS(hostname, sslListen, webroot, upstream, certPath, keyPath string) string {
	listenBlock := "    listen " + sslListen + ";"
	if strings.HasPrefix(sslListen, "443 ") || sslListen == "443" {
		listenBlock += "\n    listen [::]:" + sslListen + ";"
	}
	return fmt.Sprintf(`%s — dedicated share origin for %s
server {
%s
    server_name %s;

    ssl_certificate %s;
    ssl_certificate_key %s;
    ssl_protocols TLSv1.2 TLSv1.3;

    gzip on;
    gzip_proxied any;
    gzip_comp_level 5;
    gzip_min_length 1024;
    gzip_types text/plain text/css application/javascript text/javascript application/json image/svg+xml;

    location / {
        proxy_pass %s;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
    }
}

server {
    listen 80;
    listen [::]:80;
    server_name %s;

    location /.well-known/acme-challenge/ {
        root %s;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}
`, managedMarker, hostname, listenBlock, hostname, certPath, keyPath, upstream, hostname, webroot)
}
