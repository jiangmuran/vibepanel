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
	"net/netip"
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
//       an isolated `# Managed by vibepanel` vhost (temp file and rename, the
//       previous file put back on any failure), obtains a Let's Encrypt
//       certificate via HTTP-01 webroot, and reloads nginx.
//
// The check makes outbound requests to a name somebody typed, so it goes
// through the guard page sources use (pagesources.go): https, a domain and
// never an address, every resolved address public, the checked address the
// one dialled, no redirect followed. Both handlers write an audit row for
// every attempt, refused ones included.

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
	// allowAddr, dial and rootCAs are sourceFetcher's seams, passed straight
	// to it: tests replace them to reach a server on loopback and to see
	// which address was dialled, and nothing else does.
	allowAddr func(netip.Addr) bool
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	rootCAs   *x509.CertPool
	// settle is how long to wait between the checks that follow a provision,
	// while nginx's new workers or a fresh certificate come up. Nil means the
	// real waits; a test passes an empty list.
	settle []time.Duration
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

// errNotPublic is the whole of what a caller learns about a name that
// resolves somewhere the panel will not connect to. Nothing was dialled, so
// there is no refused-or-timed-out to report, and that is the point: the
// difference between those two is a port scan of the network the panel sits
// in, run by whoever can type a hostname into a settings page.
const errNotPublic = "the domain resolves to an address that is not public (loopback, private, link-local or reserved); nothing was contacted"

// probeClients builds the three clients one check uses, all aimed at the same
// already-checked address: strict HTTPS, HTTPS without certificate
// verification (to tell "this panel behind a bad certificate" from "something
// else"), and plain HTTP on port 80 (whether an HTTP-01 challenge could get
// in). ok is false when the guard refuses the name, and then nothing may be
// sent anywhere.
//
// addrs is what the caller resolved and showed; the guard is handed those
// same addresses rather than resolving again, so the address that was checked,
// the address that is reported and the address that is dialled are one value
// and a resolver answering differently the second time changes nothing.
func (p shareOriginProvisioner) probeClients(ctx context.Context, origin, hostname string, addrs []netip.Addr) (strict, insecure, plain *http.Client, ok bool) {
	lookup := func(ctx context.Context, h string) ([]netip.Addr, error) {
		if h == hostname {
			return addrs, nil
		}
		// The guard's fake-ip probe asks about a second, made-up name.
		ips, err := p.resolveIPs(ctx, h)
		if err != nil {
			return nil, err
		}
		return parseAddrs(ips), nil
	}
	f := &sourceFetcher{resolve: lookup, allowAddr: p.allowAddr, dial: p.dial, rootCAs: p.rootCAs}
	build := func() *http.Client {
		c, problem := f.guardedClient(ctx, origin+"/", shareProbeTime)
		if problem != "" {
			return nil
		}
		c.Timeout = shareProbeTime
		// Still never followed. The guard's own policy turns a redirect into
		// an error, and here a 301 is an answer worth reporting: something
		// other than this panel is on that port.
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}
	if strict = build(); strict == nil {
		return nil, nil, nil, false
	}
	insecure = build()
	if tr, isTr := insecure.Transport.(*http.Transport); isTr {
		tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // diagnostic only: carries a random nonce, never a credential
	}
	dial := p.dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: shareProbeTime}).DialContext
	}
	target := net.JoinHostPort(addrs[0].Unmap().String(), "80")
	plain = &http.Client{
		Timeout: shareProbeTime,
		Transport: &http.Transport{
			Proxy: nil,
			// The checked address again, on the port a CA would knock on.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dial(ctx, network, target)
			},
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: shareProbeTime,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return strict, insecure, plain, true
}

func parseAddrs(ips []string) []netip.Addr {
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if a, err := netip.ParseAddr(ip); err == nil {
			out = append(out, a)
		}
	}
	return out
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
		snippet = renderNginxShareHTTPS(hostname, host, listen, p.webroot, upstream,
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

// auditActor is who a sharing audit row is about. The rows are written with a
// context that outlives the request: a provision runs for a minute and a half,
// and somebody closing the tab during it must not be how its row goes missing.
func (s *Server) auditActor(r *http.Request) (ctx context.Context, username, ip string) {
	if u, ok := currentUserFrom(r); ok {
		username = u.Username
	}
	return context.WithoutCancel(r.Context()), username, s.clientIP(r)
}

// auditText makes something a caller typed safe to put in an audit row before
// it has been parsed: bounded, and with nothing in it that breaks a log line.
func auditText(raw string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	if len(clean) > 120 {
		clean = clean[:120]
	}
	if clean == "" {
		return "(empty)"
	}
	return clean
}

const errCheckNeedsDomain = "the check needs an https:// origin on a domain name, not an address"

func (s *Server) handleCheckShareOrigin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Origin string `json:"origin"`
	}
	if !decode(w, r, &req) {
		return
	}
	actx, who, ip := s.auditActor(r)
	refuse := func(subject, why string) {
		s.audit(actx, "sharing.origin_checked", who, ip, subject+" refused: "+why)
		writeErr(w, http.StatusBadRequest, why)
	}
	origin, scheme, host, hostname, err := normalizeShareOriginInput(req.Origin)
	if err != nil {
		refuse(auditText(req.Origin), err.Error())
		return
	}
	// Before anything is resolved or dialled. An address literal or a plain
	// http:// origin is a request to connect to wherever the caller says; a
	// share origin that can be checked is a name with a certificate.
	if scheme != "https" || !validPublicDomain(hostname) {
		refuse(host, errCheckNeedsDomain)
		return
	}
	if why := s.collidesWithPanelHost(r, host); why != "" {
		refuse(host, why)
		return
	}
	res := s.checkShareOrigin(r.Context(), origin, scheme, host, hostname, r.Host)
	s.audit(actx, "sharing.origin_checked", who, ip, host+" "+res.Status)
	writeJSON(w, http.StatusOK, res)
}

const errOwnAddress = "share origin must be different from the panel's own address; leave it empty to use the panel's address"

// collidesWithPanelHost says why host may not become the share origin, or "".
//
// By name, not by `host:port`: the share host is recognised by name on any
// port (classifyShareHost), so a share origin on the panel's own name and
// another port would confine the panel too.
//
// The request's own Host is only evidence when a browser sent it. The command
// line talks to the panel on loopback, so `vibepanel settings share-origin
// https://<the panel's public name>` on a panel with no VIBEPANEL_DOMAIN
// passed every check here and turned the whole panel into the "this link no
// longer works" page. What the panel does know without being told is where
// people are signed in from: every session is bound to the origin it was
// made at, so a name with a live session on it is the panel's.
func (s *Server) collidesWithPanelHost(r *http.Request, host string) string {
	name := hostnameOnly(host)
	if name == "" {
		return ""
	}
	if s.panelHostnames()[name] {
		return errOwnAddress
	}
	if r == nil {
		return ""
	}
	if hostnameOnly(r.Host) == name {
		return errOwnAddress
	}
	if s.DB == nil {
		return ""
	}
	origins, err := s.DB.LiveAuthSessionOrigins(r.Context())
	if err != nil {
		// Not knowing is a refusal: the mistake this prevents costs the owner
		// the panel, and asking again costs nothing.
		return "the panel cannot read its sign-in sessions to check this address; try again"
	}
	for _, o := range origins {
		if u, perr := url.Parse(o); perr == nil && hostnameOnly(u.Host) == name {
			return "a signed-in session reaches the panel at " + name +
				", so it is the panel's own address; sign that session out first if the panel has moved"
		}
	}
	return ""
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
	// The handler refuses these already; this is the same rule where the
	// dialling happens, so no second caller can forget it.
	if scheme != "https" || !validPublicDomain(hostname) {
		out.Status = "not_public"
		out.ReachError = errCheckNeedsDomain
		return out
	}

	// 1. DNS lookup.
	dctx, dcancel := context.WithTimeout(ctx, 4*time.Second)
	ips, derr := p.resolveIPs(dctx, hostname)
	dcancel()
	addrs := parseAddrs(ips)
	if derr != nil || len(addrs) == 0 {
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

	// 2. The guard. A share domain pointing at this machine's public address
	// is the ordinary case and passes like any other public address; one that
	// resolves into loopback or private space is refused here, with one fixed
	// sentence and no connection attempted.
	strict, insecure, plain, ok := p.probeClients(ctx, origin, hostname, addrs)
	if !ok {
		out.Status = "not_public"
		out.ReachError = errNotPublic
		return out
	}

	// 3. Live self-challenge probe to the target origin.
	nonce, entry, cleanup, merr := s.pb.probes.mint()
	if merr != nil {
		out.Status = "unreachable"
		out.ReachError = merr.Error()
		return out
	}
	defer cleanup()

	targetURL := origin + "/.well-known/vibepanel-probe/" + nonce
	probeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		out.Status = "unreachable"
		out.ReachError = err.Error()
		return out
	}
	resp, perr := strict.Do(probeReq)
	if perr == nil {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		_ = resp.Body.Close()
		body := strings.TrimSpace(string(bodyBytes))
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
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

	// 4. Target probe failed: classify TLS failure vs proxy/network failure.
	if isTLSError(perr) {
		out.TLSOK = false
		out.TLSError = perr.Error()
		// Diagnostic follow-up over insecure TLS (carrying only the random
		// nonce) to see whether this panel or another site already answers on
		// :443, and check port 80 for HTTP-01 readiness.
		if insecReq, ierr := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil); ierr == nil {
			if iresp, err2 := insecure.Do(insecReq); err2 == nil {
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
		out.HTTPPortOpen = checkHTTPPortOpen(ctx, plain, hostname, nonce)
		out.Status = "needs_https"
		return out
	}

	// Connection refused / timeout on target port: check port 80 too so we can
	// distinguish "server reachable on HTTP, just needs HTTPS/proxy" from
	// "domain doesn't reach this machine at all". The error text is a public
	// address's, which is the only kind that gets this far.
	out.ReachError = perr.Error()
	out.HTTPPortOpen = checkHTTPPortOpen(ctx, plain, hostname, nonce)
	if out.HTTPPortOpen {
		out.Status = "needs_https"
	} else {
		out.Status = "unreachable"
	}
	return out
}

// checkHTTPPortOpen asks whether anything answers plain HTTP on port 80 of the
// checked address. The one request here that is not https, and it carries a
// nonce to a path and reads nothing back: whether a CA's HTTP-01 request could
// arrive is a question about port 80 and has no https form.
func checkHTTPPortOpen(ctx context.Context, plain *http.Client, hostname, nonce string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+hostname+"/.well-known/vibepanel-probe/"+nonce, nil)
	if err != nil {
		return false
	}
	resp, err := plain.Do(req)
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
	actx, who, ip := s.auditActor(r)
	// Every way out of this handler leaves a row: this is the request that
	// rewrites the web server's configuration and asks a CA for a certificate,
	// and "somebody tried and it was refused" is as much part of that record
	// as the time it worked.
	refuse := func(code int, subject, why string) {
		s.audit(actx, "sharing.origin_provision_refused", who, ip, subject+": "+why)
		writeErr(w, code, why)
	}
	origin, scheme, host, hostname, err := normalizeShareOriginInput(req.Origin)
	if err != nil {
		refuse(http.StatusBadRequest, auditText(req.Origin), err.Error())
		return
	}
	if scheme != "https" {
		refuse(http.StatusBadRequest, host, "automatic HTTPS requires an https:// origin")
		return
	}
	if !validPublicDomain(hostname) || host != hostname {
		refuse(http.StatusBadRequest, host, "automatic HTTPS requires a standard domain name on port 443")
		return
	}
	if why := s.collidesWithPanelHost(r, host); why != "" {
		refuse(http.StatusBadRequest, host, why)
		return
	}

	// One at a time, panel-wide. Two of these at once read the same vhost as
	// "the previous state", each wrote its own and each rolled back to what
	// it had read, so the loser's rollback could delete the winner's working
	// site; and two certbots for one name spend the CA's failed-validation
	// budget twice. The second caller is told, not queued: by the time the
	// first finishes, what the second would have done is out of date.
	if !s.pb.provisioning.TryLock() {
		refuse(http.StatusConflict, host, "another HTTPS setup is running; wait for it to finish")
		return
	}
	defer s.pb.provisioning.Unlock()

	ctx := r.Context()
	p := s.originProvisioner()
	info := s.detectProvisionInfo(ctx, p, origin, scheme, host, hostname)
	if !info.CanProvision {
		refuse(http.StatusBadRequest, host, "automatic HTTPS is not available: "+info.Reason)
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
		refuse(http.StatusBadRequest, host, "DNS for "+hostname+" does not resolve yet"+hint)
		return
	}

	failed := func(code int, why string) {
		s.audit(actx, "sharing.origin_provision_failed", who, ip, info.Mode+" "+host+": "+why)
		writeErr(w, code, why)
	}
	switch info.Mode {
	case "acme":
		if err := s.ManageTLS(ctx, hostname); err != nil {
			failed(http.StatusBadGateway, err.Error())
			return
		}
	case "nginx-certbot":
		if err := s.provisionNginxCertbot(ctx, p, hostname, info.NginxListen); err != nil {
			failed(http.StatusBadGateway, err.Error())
			return
		}
	default:
		refuse(http.StatusBadRequest, host, "automatic HTTPS is not available on this host")
		return
	}

	// The setting is saved only if the domain now demonstrably reaches this
	// panel. It used to be saved first and checked afterwards: a certificate
	// that was issued while the proxy still pointed elsewhere left every
	// handed-out link on a name that did not serve it, and the response said
	// "provisioned". Not saving costs nothing -- the certificate and the vhost
	// stay, the report says what is still missing, and Save is one click once
	// it is fixed.
	check := s.checkShareOriginAfterProvision(ctx, p, origin, scheme, host, hostname, r.Host)
	saved := ""
	if check.Ready {
		if err := s.DB.SetSetting(actx, shareOriginKey, origin); err != nil {
			s.audit(actx, "sharing.origin_provision_failed", who, ip, info.Mode+" "+host+": could not save the setting")
			s.writeStoreErr(w, err)
			return
		}
		s.pb.origin.forget()
		s.audit(actx, "sharing.origin_provisioned", who, ip, info.Mode+" "+origin+" saved")
		s.audit(actx, "sharing.origin_changed", who, ip, origin)
		saved = origin
	} else {
		s.audit(actx, "sharing.origin_provisioned", who, ip, info.Mode+" "+origin+" not saved: check says "+check.Status)
		saved, _, _ = s.shareOrigin(actx)
	}
	writeJSON(w, http.StatusOK, shareOriginProvisionResult{
		Origin:      origin,
		Mode:        info.Mode,
		ShareOrigin: saved,
		Check:       check,
	})
}

// checkShareOriginAfterProvision probes the newly provisioned origin, retrying
// briefly so `nginx -s reload` workers or a fresh ACME handshake finish
// settling before the response tells the caller whether the origin is ready.
func (s *Server) checkShareOriginAfterProvision(ctx context.Context, p shareOriginProvisioner, origin, scheme, host, hostname, reqHost string) shareOriginCheckResponse {
	check := s.checkShareOrigin(ctx, origin, scheme, host, hostname, reqHost)
	waits := p.settle
	if waits == nil {
		waits = []time.Duration{300 * time.Millisecond, 500 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond}
	}
	for _, wait := range waits {
		if check.Ready {
			return check
		}
		select {
		case <-ctx.Done():
			return check
		case <-time.After(wait):
		}
		check = s.checkShareOrigin(ctx, origin, scheme, host, hostname, reqHost)
	}
	return check
}

// ─── nginx + certbot provisioner ───────────────────────────────────

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

	// Snapshot prior state for rollback.
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
			_ = writeFileAtomic(availPath, prevAvail, 0o644)
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
		if err := writeFileAtomic(availPath, []byte(conf), 0o644); err != nil {
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
		// A site that is already serving HTTPS keeps doing so while its
		// certificate is renewed. The first version always swapped in the
		// HTTP-only challenge vhost here, so renewing a certificate with two
		// weeks left took every handed-out link off the air for as long as
		// certbot ran, up to ninety seconds, and for good if certbot failed
		// between the two reloads. The HTTPS vhost this file writes already
		// answers the challenge on port 80 from the same webroot, so there is
		// nothing to swap.
		if !servesManagedHTTPS(prevAvail, hadAvail && (enabledPath == "" || hadEnabled), p.webroot) {
			httpOnly := renderNginxShareHTTPChallenge(hostname, p.webroot, upstream)
			if err := writeAndReload(httpOnly); err != nil {
				rollback()
				return err
			}
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
	fullConf := renderNginxShareHTTPS(hostname, hostname, sslListen, p.webroot, upstream, certPath, keyPath)
	if err := writeAndReload(fullConf); err != nil {
		rollback()
		return err
	}
	return nil
}

// servesManagedHTTPS is whether the vhost already in place is this file's own
// HTTPS one, enabled, answering the ACME challenge from the webroot certbot is
// about to write into. Anything else -- no file, somebody's edit that dropped
// the challenge location, a different webroot -- gets the challenge vhost.
func servesManagedHTTPS(conf []byte, enabled bool, webroot string) bool {
	text := string(conf)
	return enabled &&
		strings.Contains(text, managedMarker) &&
		strings.Contains(text, "ssl_certificate ") &&
		strings.Contains(text, "location /.well-known/acme-challenge/") &&
		strings.Contains(text, "root "+webroot+";")
}

// writeFileAtomic replaces path with data by writing a sibling and renaming
// it over.
//
// os.WriteFile truncates the file and then fills it, and nginx may be reading
// it at that moment: a reload started by anything else on the machine (logrotate,
// certbot's own deploy hook, another site's deploy) parses whatever is there,
// and half a server block is a syntax error that takes the reload -- every
// site's reload -- down with it. A rename is one step: a reader gets the old
// file or the new one.
//
// The sibling's name starts with a dot and does not end in .conf, so neither
// `include conf.d/*.conf` nor `include sites-enabled/*` picks it up if this
// process dies before the rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Chmod(perm)
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
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

// The two vhosts below send the share host's name upstream as a literal, never
// `$host`.
//
// `$host` is whatever the client put in its Host header. If this server block
// is nginx's default for its socket -- the only site on the machine, or the
// first one loaded -- nginx hands it requests for every name, and with `$host`
// each of them reached the panel under the name the client chose: a name that
// is not the share host, so the panel answered as itself, sign-in page and
// all, through the one door that exists to keep it off the public domain.
// A literal makes everything that comes through this vhost a share-host
// request, whatever it claimed to be. hostHeader has passed strictDomainRe
// (and a numeric port check) before it gets here, so it cannot carry a
// directive. The redirect names the host the same way, for the same reason:
// `https://$host` sends a visitor wherever their Host header said.
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
        proxy_set_header Host %s;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location / {
        return 404;
    }
}
`, managedMarker, hostname, hostname, webroot, upstream, hostname)
}

func renderNginxShareHTTPS(hostname, hostHeader, sslListen, webroot, upstream, certPath, keyPath string) string {
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
        proxy_set_header Host %s;
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
        return 301 https://%s$request_uri;
    }
}
`, managedMarker, hostname, listenBlock, hostname, certPath, keyPath, upstream, hostHeader, hostname, webroot, hostHeader)
}
