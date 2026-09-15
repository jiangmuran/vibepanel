package httpapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Sources: data the panel fetches for a page. docs/page-backend.md §4.
//
// The page never gets the network; the panel does, on its behalf, and only to
// hosts the owner approved, only over https, only to public addresses, never
// following a redirect. What makes that last list hold is where it is checked:
// on the address the name resolved to, and then that exact address is dialled,
// so a name that answers differently a moment later cannot move the request.

// sourceWatchIdle is how long after the last look a page's sources and
// schedule keep running.
const sourceWatchIdle = 5 * time.Minute

// sourceTick is how often a watched page's due sources are looked at.
const sourceTick = 15 * time.Second

// sourceResult is one source's last fetch, as a page sees it.
type sourceResult struct {
	OK        bool   `json:"ok"`
	FetchedAt int64  `json:"fetchedAt"`
	Status    int    `json:"status"`
	Error     string `json:"error"`
	Value     any    `json:"value"`
}

type sourcesState struct {
	mu      sync.Mutex
	results map[string]*sourceResult
	lastRun map[string]time.Time
	watched map[string]time.Time
	running map[string]bool
	// fetcher is swapped by tests to reach a TLS server on loopback; nil is
	// the real one, which refuses loopback.
	fetcher *sourceFetcher
	// tick overrides sourceTick in tests.
	tick time.Duration
}

func sourceKey(pageID, ns, key string) string { return pageID + "|" + ns + "|" + key }

// sourceHost is the host a source's URL reaches, with its port when it is
// not 443, which is what an owner approves.
func sourceHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if p := u.Port(); p != "" && p != "443" {
		return strings.ToLower(u.Hostname()) + ":" + p
	}
	return strings.ToLower(u.Hostname())
}

// sourceResults is every declared source's last result, from memory.
func (s *Server) sourceResults(pageID, ns string, m pages.Manifest) map[string]*sourceResult {
	st := &s.pb.sources
	st.mu.Lock()
	defer st.mu.Unlock()
	out := map[string]*sourceResult{}
	for _, src := range m.Sources {
		if r, ok := st.results[sourceKey(pageID, ns, src.Key)]; ok {
			cp := *r
			out[src.Key] = &cp
			continue
		}
		out[src.Key] = &sourceResult{Error: "not fetched yet"}
	}
	return out
}

// markWatched records that something is looking at a page's namespace, and
// starts its background loop if the page has anything to run and the loop is
// not running. The loop stops by itself when nothing has looked for
// sourceWatchIdle -- there is no ticker for a page nobody is watching, and none
// at all for a page with no sources and no schedule.
func (s *Server) markWatched(pageID, ns string, m pages.Manifest) {
	if len(m.Sources) == 0 && (m.Server == nil || m.Server.Every == "") {
		return
	}
	st := &s.pb.sources
	key := pageID + "|" + ns
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.watched == nil {
		st.watched, st.running = map[string]time.Time{}, map[string]bool{}
	}
	st.watched[key] = time.Now()
	if st.running[key] {
		return
	}
	st.running[key] = true
	go s.watchLoop(pageID, ns)
}

func (s *Server) watchLoop(pageID, ns string) {
	st := &s.pb.sources
	key := pageID + "|" + ns
	tick := st.tick
	if tick <= 0 {
		tick = sourceTick
	}
	defer func() {
		st.mu.Lock()
		st.running[key] = false
		st.mu.Unlock()
	}()
	for {
		st.mu.Lock()
		idle := time.Since(st.watched[key]) > sourceWatchIdle
		st.mu.Unlock()
		if idle {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if !s.watchOnce(ctx, pageID, ns) {
			cancel()
			return
		}
		cancel()
		time.Sleep(tick)
	}
}

// watchOnce runs a page's due sources and its schedule; false when the page is
// gone and the loop should stop.
func (s *Server) watchOnce(ctx context.Context, pageID, ns string) bool {
	page, err := s.DB.SharePageByID(ctx, pageID)
	if err != nil {
		return !errors.Is(err, store.ErrNotFound) && s.DB != nil
	}
	m, err := s.pageManifestFor(ctx, page, ns)
	if err != nil {
		return true
	}
	s.refreshSources(ctx, page, ns, m, false)
	s.runSchedule(ctx, page, ns, m)
	return true
}

// refreshSources fetches every source that is due (or all, when force).
func (s *Server) refreshSources(ctx context.Context, page store.SharePage, ns string, m pages.Manifest, force bool) {
	if len(m.Sources) == 0 {
		return
	}
	hosts, err := s.DB.PageHosts(ctx, page.ID)
	if err != nil {
		return
	}
	approved := map[string]bool{}
	for _, h := range hosts {
		approved[h] = true
	}
	st := &s.pb.sources
	for _, src := range m.Sources {
		k := sourceKey(page.ID, ns, src.Key)
		st.mu.Lock()
		if st.lastRun == nil {
			st.lastRun, st.results = map[string]time.Time{}, map[string]*sourceResult{}
		}
		due := force || time.Since(st.lastRun[k]) >= src.Interval()
		if due {
			st.lastRun[k] = time.Now()
		}
		fetcher := st.fetcher
		st.mu.Unlock()
		if !due {
			continue
		}
		var res *sourceResult
		if !approved[sourceHost(src.URL)] {
			res = &sourceResult{Error: "host not approved"}
		} else {
			res = s.fetchSource(ctx, fetcher, page.ID, src)
		}
		st.mu.Lock()
		prev := st.results[k]
		// A failed fetch keeps the last good value, marked not ok: a wall
		// showing yesterday's weather with "stale" beats one showing nothing
		// because the API hiccupped.
		if !res.OK && prev != nil && prev.OK {
			res.Value = prev.Value
		}
		st.results[k] = res
		st.mu.Unlock()
	}
}

// fetchSource does one fetch with the page's secrets in its headers.
func (s *Server) fetchSource(ctx context.Context, f *sourceFetcher, pageID string, src pages.SourceSpec) *sourceResult {
	var secrets []string
	lookup := func(name string) (string, bool) {
		enc, err := s.DB.PageSecretSealed(ctx, pageID, name)
		if err != nil {
			return "", false
		}
		box, err := s.secretBox()
		if err != nil {
			return "", false
		}
		v, err := box.Unseal(enc, pageSecretContext(pageID, name))
		if err != nil {
			return "", false
		}
		secrets = append(secrets, string(v))
		return string(v), true
	}
	headers := map[string]string{}
	for name, value := range src.Headers {
		expanded, err := pages.ExpandSecrets(value, lookup)
		if err != nil {
			return &sourceResult{Error: err.Error(), FetchedAt: time.Now().Unix()}
		}
		headers[name] = expanded
	}
	if f == nil {
		f = &sourceFetcher{}
	}
	res := f.fetch(ctx, src, headers)
	res.Error = redactSecrets(res.Error, secrets)
	return res
}

// redactSecrets takes every secret out of a message before it is stored where
// settings, the admin API or a log can show it.
//
// No error the fetcher writes today quotes a header, so this is the second
// line rather than the first: the next error message somebody adds -- a
// transport error that prints its request, a body excerpt -- is the one that
// would quote a token back to a page's admin screen. Longest first, so a
// secret that contains another is not left half-visible.
func redactSecrets(msg string, secrets []string) string {
	sorted := append([]string(nil), secrets...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, secret := range sorted {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[secret]")
		}
	}
	return msg
}

func pageSecretContext(pageID, name string) string { return "page-secret:" + pageID + ":" + name }

// ─── the fetch ────────────────────────────────────────────────────────────

// sourceFetcher does one guarded HTTPS GET.
type sourceFetcher struct {
	// resolve and publicOnly are the guard; tests replace them to reach a
	// server on loopback, and nothing else does.
	resolve    func(ctx context.Context, host string) ([]netip.Addr, error)
	allowAddr  func(netip.Addr) bool
	rootCAs    *x509.CertPool
	serverName string
}

func (f *sourceFetcher) fetch(ctx context.Context, src pages.SourceSpec, headers map[string]string) *sourceResult {
	res := &sourceResult{FetchedAt: time.Now().Unix()}
	u, err := url.Parse(src.URL)
	if err != nil || u.Scheme != "https" {
		res.Error = "only https sources are fetched"
		return res
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	resolve := f.resolve
	if resolve == nil {
		resolve = func(ctx context.Context, h string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		}
	}
	allow := f.allowAddr
	if allow == nil {
		allow = publicAddr
	}
	ctx, cancel := context.WithTimeout(ctx, src.TimeoutOrDefault())
	defer cancel()

	var addrs []netip.Addr
	if literal, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{literal}
	} else if addrs, err = resolve(ctx, host); err != nil || len(addrs) == 0 {
		res.Error = "the host does not resolve"
		return res
	}
	// Every address must be public, not just the first: a name that answers
	// one public and one private address is a name somebody arranged.
	for _, a := range addrs {
		if !allow(a) {
			res.Error = "the host resolves to an address a source may not reach"
			return res
		}
	}
	target := net.JoinHostPort(addrs[0].Unmap().String(), port)
	serverName := host
	if f.serverName != "" {
		serverName = f.serverName
	}

	dialer := &net.Dialer{Timeout: src.TimeoutOrDefault()}
	transport := &http.Transport{
		Proxy: nil,
		// The checked address, never the name: see the file comment.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, target)
		},
		TLSClientConfig:        &tls.Config{ServerName: serverName, RootCAs: f.rootCAs, MinVersion: tls.VersionTLS12},
		DisableKeepAlives:      true,
		ResponseHeaderTimeout:  src.TimeoutOrDefault(),
		MaxResponseHeaderBytes: 64 << 10,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("a source may not redirect")
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		res.Error = "bad request"
		return res
	}
	req.Header.Set("User-Agent", "vibepanel-source/1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "may not redirect") {
			res.Error = "the source redirected, and a redirect is a second URL nobody approved"
		} else {
			res.Error = "fetch failed: " + trimNetError(err)
		}
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		res.Error = "the source redirected, and a redirect is a second URL nobody approved"
		return res
	}
	limit := src.MaxBytesOrDefault()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		res.Error = "reading the body failed"
		return res
	}
	if len(body) > limit {
		res.Error = fmt.Sprintf("the response is larger than %d bytes", limit)
		return res
	}
	if resp.StatusCode >= 400 {
		res.Error = fmt.Sprintf("the source answered %d", resp.StatusCode)
		return res
	}
	if src.Parse == "text" {
		res.Value, res.OK = string(body), true
		return res
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		res.Error = "the response is not JSON"
		return res
	}
	res.Value, res.OK = v, true
	return res
}

func trimNetError(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return msg[i+2:]
	}
	return msg
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	thisNet   = netip.MustParsePrefix("0.0.0.0/8")
	ietf      = netip.MustParsePrefix("192.0.0.0/24")
	benchmark = netip.MustParsePrefix("198.18.0.0/15")
	reserved  = netip.MustParsePrefix("240.0.0.0/4")
	testNets  = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/32")}
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
	teredo    = netip.MustParsePrefix("2001::/32")
	orchid    = netip.MustParsePrefix("2001:10::/28")
)

// publicAddr reports whether a source may reach an address: not loopback,
// private, link-local (which is where cloud metadata lives, 169.254.169.254
// and fd00:ec2::254 both), CGNAT, multicast, unspecified, reserved, or an IPv6
// form that carries an IPv4 address inside it.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range append([]netip.Prefix{cgnat, thisNet, ietf, benchmark, reserved, nat64, sixToFour, teredo, orchid}, testNets...) {
		if p.Contains(a) {
			return false
		}
	}
	if a.Is4() && a == netip.MustParseAddr("255.255.255.255") {
		return false
	}
	return true
}

// ─── the settings routes ──────────────────────────────────────────────────

func (s *Server) registerPageSourceRoutes(r chi.Router) {
	r.Get("/settings/pages/{pageID}/sources", s.handlePageSources)
	r.Get("/settings/pages/{pageID}/hosts", s.handleGetPageHosts)
	r.Put("/settings/pages/{pageID}/hosts", s.handlePutPageHosts)
	r.Get("/settings/pages/{pageID}/secrets", s.handleGetPageSecrets)
	r.Put("/settings/pages/{pageID}/secrets/{name}", s.handlePutPageSecret)
	r.Delete("/settings/pages/{pageID}/secrets/{name}", s.handleDeletePageSecret)
	r.Get("/settings/pages/{pageID}/server/log", s.handlePageServerLog)
}

// pageSourceRow is a declared source as settings shows it.
type pageSourceRow struct {
	Key       string             `json:"key"`
	URL       string             `json:"url"`
	Host      string             `json:"host"`
	Approved  bool               `json:"approved"`
	Every     string             `json:"every"`
	OK        bool               `json:"ok"`
	FetchedAt int64              `json:"fetchedAt"`
	Status    int                `json:"status"`
	Error     string             `json:"error"`
	Secrets   []pageSourceSecret `json:"secrets"`
}

type pageSourceSecret struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// sourcesManifest is the manifest whose sources settings describes: the
// published one, or the draft's for a page never published.
func (s *Server) sourcesManifest(ctx context.Context, page store.SharePage) (pages.Manifest, string) {
	if m, err := s.pageManifestFor(ctx, page, store.PageDataLive); err == nil {
		return m, store.PageDataLive
	}
	m, _ := s.pageManifestFor(ctx, page, store.PageDataDraft)
	return m, store.PageDataDraft
}

func (s *Server) handlePageSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	m, ns := s.sourcesManifest(ctx, page)
	hosts, err := s.DB.PageHosts(ctx, page.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	secrets, err := s.DB.PageSecrets(ctx, page.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	approved, set := map[string]bool{}, map[string]bool{}
	for _, h := range hosts {
		approved[h] = true
	}
	for _, sec := range secrets {
		set[sec.Name] = true
	}
	results := s.sourceResults(page.ID, ns, m)
	out := []pageSourceRow{}
	for _, src := range m.Sources {
		res := results[src.Key]
		row := pageSourceRow{Key: src.Key, URL: src.URL, Host: sourceHost(src.URL), Every: src.Every,
			OK: res.OK, FetchedAt: res.FetchedAt, Status: res.Status, Error: res.Error, Secrets: []pageSourceSecret{}}
		row.Approved = approved[row.Host]
		for _, name := range src.Secrets() {
			row.Secrets = append(row.Secrets, pageSourceSecret{Name: name, Set: set[name]})
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetPageHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.DB.PageHosts(r.Context(), chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"hosts": hosts})
}

// handlePutPageHosts replaces a page's approved hosts. A host is a name, with
// ":port" when it is not 443 -- never a URL, a wildcard or an address range,
// because approving "api.example.com" must approve exactly that.
func (s *Server) handlePutPageHosts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	var req struct {
		Hosts []string `json:"hosts"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Hosts) > 32 {
		writeErr(w, http.StatusBadRequest, "at most 32 hosts")
		return
	}
	clean := []string{}
	seen := map[string]bool{}
	for _, h := range req.Hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if !validApprovedHost(h) {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("%q is not a host name (with :port when not 443)", h))
			return
		}
		if !seen[h] {
			seen[h] = true
			clean = append(clean, h)
		}
	}
	sort.Strings(clean)
	if err := s.DB.SetPageHosts(ctx, page.ID, clean); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.forgetSources(page.ID)
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "page.hosts_changed", u.Username, s.clientIP(r), page.Name+": "+strings.Join(clean, " "))
	}
	writeJSON(w, http.StatusOK, map[string][]string{"hosts": clean})
}

// forgetSources drops a page's results and schedule, so a change of hosts or
// secrets is fetched on the next tick rather than the next interval.
func (s *Server) forgetSources(pageID string) {
	st := &s.pb.sources
	st.mu.Lock()
	defer st.mu.Unlock()
	for k := range st.lastRun {
		if strings.HasPrefix(k, pageID+"|") {
			delete(st.lastRun, k)
			delete(st.results, k)
		}
	}
}

func validApprovedHost(h string) bool {
	name, port, hasPort := strings.Cut(h, ":")
	if hasPort {
		if port == "443" || port == "" || len(port) > 5 || strings.Trim(port, "0123456789") != "" {
			return false
		}
	}
	if name == "" || len(name) > 253 || strings.ContainsAny(name, "/*?#@ ") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return false
		}
	}
	return true
}

func (s *Server) handleGetPageSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, err := s.DB.PageSecrets(r.Context(), chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, secrets)
}

func (s *Server) handlePutPageSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	name := chi.URLParam(r, "name")
	if !pages.SecretName.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "a secret's name is A-Z, 0-9 and _, starting with a letter")
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Value == "" || len(req.Value) > 4096 || strings.ContainsAny(req.Value, "\r\n\x00") {
		writeErr(w, http.StatusBadRequest, "a secret is 1 to 4096 bytes with no line breaks")
		return
	}
	box, err := s.secretBox()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "the panel's secrets key cannot be read: "+err.Error())
		return
	}
	if err := s.DB.SetPageSecret(ctx, page.ID, name, box.Seal([]byte(req.Value), pageSecretContext(page.ID, name))); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.forgetSources(page.ID)
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "page.secret_set", u.Username, s.clientIP(r), page.Name+": "+name)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeletePageSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	name := chi.URLParam(r, "name")
	if err := s.DB.DeletePageSecret(ctx, page.ID, name); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.forgetSources(page.ID)
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "page.secret_deleted", u.Username, s.clientIP(r), page.Name+": "+name)
	}
	w.WriteHeader(http.StatusNoContent)
}
