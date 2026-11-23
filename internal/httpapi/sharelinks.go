package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/secret"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Links you can copy again, links visitors can act through, and the one
// switch that turns every visitor write off. docs/page-backend.md §6 and §7.

// secrets is the panel's sealing key, opened on first use.
//
// Lazily, and the error remembered only until it succeeds: a data directory
// that cannot be written at startup should not stop a panel whose links do
// not need the key, and a key that could not be read a moment ago may be
// readable now.
type secrets struct {
	mu  sync.Mutex
	box *secret.Box
}

func (s *Server) secretBox() (*secret.Box, error) {
	s.pb.secrets.mu.Lock()
	defer s.pb.secrets.mu.Unlock()
	if s.pb.secrets.box != nil {
		return s.pb.secrets.box, nil
	}
	box, err := secret.Open(filepath.Join(s.Cfg.DataDir, secret.KeyFile))
	if err != nil {
		return nil, err
	}
	s.pb.secrets.box = box
	return box, nil
}

// shareTokenContext is what a link's sealed token is bound to, so a sealed
// token copied onto another row does not open there.
func shareTokenContext(linkID string) string { return "share-link:" + linkID }

// sealShareToken seals a new link's token, or returns nil when the key cannot
// be opened -- the link is still made, and says it cannot be copied again.
func (s *Server) sealShareToken(linkID, token string) []byte {
	box, err := s.secretBox()
	if err != nil {
		s.Log.Warn("share link address will not be copyable", "err", err)
		return nil
	}
	return box.Seal([]byte(token), shareTokenContext(linkID))
}

func (s *Server) shareAddress(r *http.Request, token string) string {
	if origin, _, _ := s.shareOrigin(r.Context()); origin != "" {
		return origin + "/share/" + token + "/"
	}
	return s.requestOrigin(r) + "/share/" + token + "/"
}

// handleShareURL answers a handed-out link's address, again.
func (s *Server) handleShareURL(w http.ResponseWriter, r *http.Request) {
	linkID := chi.URLParam(r, "shareID")
	enc, err := s.DB.ShareLinkTokenEnc(r.Context(), linkID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if len(enc) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "this link was made before addresses were kept; give it a new address",
			"rotatable": true,
		})
		return
	}
	box, err := s.secretBox()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "the panel's secrets key cannot be read: "+err.Error())
		return
	}
	token, err := box.Unseal(enc, shareTokenContext(linkID))
	if err != nil {
		// A key replaced since the link was made. Said, and the answer is the
		// same as for an old link: a new address.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "this link's address does not open under the panel's current key; give it a new address",
			"rotatable": true,
		})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"url": s.shareAddress(r, string(token)), "token": string(token)})
}

// handleRotateShare gives a link a new address; the old one stops working.
//
// Allowed on a locked link: a lock fixes what a screen draws, and a leaked
// address is exactly the moment somebody must not have to unlock first.
func (s *Server) handleRotateShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	linkID := chi.URLParam(r, "shareID")
	link, err := s.DB.ShareLinkByID(ctx, linkID)
	if err != nil || link.Purpose != "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	token, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.RotateShareLink(ctx, linkID, auth.HashToken(token), s.sealShareToken(linkID, token), token[:8]); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "share.rotated", u.Username, s.clientIP(r), link.Name)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"url": s.shareAddress(r, token), "token": token})
}

// linkMayAct is whether visitor actions may run through a link at all: an
// interactive handed-out link, or a preview link (whose actions write draft
// data), and never a view link; and never while visitor writes are off.
func (s *Server) linkMayAct(ctx context.Context, link store.ShareLink) bool {
	switch link.Purpose {
	case "":
		if !link.Interactive {
			return false
		}
	case store.SharePurposePreview:
	default:
		return false
	}
	return s.visitorWritesAllowed(ctx)
}

// ─── panel-wide sharing settings ──────────────────────────────────────────

const (
	visitorWritesKey = "sharing.visitor_writes"
	shareOriginKey   = "sharing.origin"
)

type sharingSettingsResponse struct {
	VisitorWrites bool   `json:"visitorWrites"`
	ShareOrigin   string `json:"shareOrigin"`
}

// shareOriginCache holds the parsed dedicated origin for share links.
//
// Cached because shareHostOnly runs on every HTTP request, and reading SQLite
// per request would add a lock round-trip to static assets and API calls.
// Invalidated on PUT /api/settings/sharing, the only writer.
type shareOriginCache struct {
	mu     sync.RWMutex
	loaded bool
	origin string
	scheme string
	host   string
}

// get answers the parsed origin, reading it once.
//
// A read that fails is returned and NOT remembered. It used to be remembered
// as "no share origin": one request whose context was cancelled mid-read, or
// one SQLITE_BUSY, marked the cache loaded with nothing in it, and from then
// until the next restart the share host was the panel -- sign-in page, API
// and socket on the public domain, with no error anywhere.
func (c *shareOriginCache) get(ctx context.Context, read func(context.Context) (string, error)) (origin, scheme, host string, err error) {
	c.mu.RLock()
	if c.loaded {
		origin, scheme, host = c.origin, c.scheme, c.host
		c.mu.RUnlock()
		return origin, scheme, host, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return c.origin, c.scheme, c.host, nil
	}
	raw, rerr := read(ctx)
	if rerr != nil {
		return "", "", "", rerr
	}
	// A stored value that no longer parses is a decided answer (no share
	// origin), not a failed read: asking again would give the same string.
	if o, sc, h, perr := parseShareOrigin(raw); perr == nil {
		c.origin, c.scheme, c.host = o, sc, h
	}
	c.loaded = true
	return c.origin, c.scheme, c.host, nil
}

func (c *shareOriginCache) forget() {
	c.mu.Lock()
	c.loaded = false
	c.origin, c.scheme, c.host = "", "", ""
	c.mu.Unlock()
}

// parseShareOrigin validates and normalizes an optional share origin of the
// form `scheme://host[:port]`. Empty input clears the setting.
//
// Paths, queries, fragments and userinfo are refused: vibepanel.js builds its
// API URL from `url.protocol + '//' + url.host + '/api/share/...'`, so a
// subpath would point the SDK at a different path than the page was served
// from.
func parseShareOrigin(raw string) (origin, scheme, host string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", "", nil
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return "", "", "", errors.New("share origin cannot contain whitespace")
	}
	if strings.ContainsAny(s, "?#") {
		return "", "", "", errors.New("share origin must be scheme://host[:port] with no query or fragment")
	}
	s = strings.TrimRight(s, "/")
	u, perr := url.Parse(s)
	if perr != nil {
		return "", "", "", errors.New("share origin must be a valid URL such as https://share.example.com")
	}
	scheme = strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", "", errors.New("share origin must start with http:// or https://")
	}
	if u.User != nil || u.Opaque != "" {
		return "", "", "", errors.New("share origin must be scheme://host[:port]")
	}
	if u.Path != "" {
		return "", "", "", errors.New("share origin must be scheme://host[:port] with no path")
	}
	if u.Host == "" || strings.HasSuffix(u.Host, ":") || u.Hostname() == "" {
		return "", "", "", errors.New("share origin needs a host name")
	}
	if p := u.Port(); p != "" {
		n, aerr := strconv.Atoi(p)
		if aerr != nil || n < 1 || n > 65535 {
			return "", "", "", errors.New("share origin has an invalid port")
		}
	}
	host = normalizeHostPort(u.Host, scheme)
	return scheme + "://" + host, scheme, host, nil
}

// normalizeHostPort lower-cases a `host[:port]` and drops the default port for
// scheme (:80 for http, :443 for https), so `Host: share.example.com:443` and
// `Host: share.example.com` compare equal.
func normalizeHostPort(rawHost, scheme string) string {
	rawHost = strings.ToLower(strings.TrimSpace(rawHost))
	if rawHost == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(rawHost)
	if err != nil {
		return rawHost
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, port)
}

// shareOriginRead is the configured share origin, or the reason it could not
// be read. shareHostOnly is the caller that needs the difference.
func (s *Server) shareOriginRead(ctx context.Context) (origin, scheme, host string, err error) {
	return s.pb.origin.get(ctx, func(ctx context.Context) (string, error) {
		if s.DB == nil {
			return "", nil
		}
		return s.DB.GetSetting(ctx, shareOriginKey, "")
	})
}

// shareOrigin is shareOriginRead for callers that build an address or answer
// a settings read, where "could not read" and "not set" lead to the same
// thing: the panel's own address. Nothing that decides what a host may reach
// calls this.
func (s *Server) shareOrigin(ctx context.Context) (origin, scheme, host string) {
	origin, scheme, host, _ = s.shareOriginRead(ctx)
	return origin, scheme, host
}

// hostnameOnly reduces a Host header or a `host[:port]` to the name alone:
// lower case, no port, no brackets, no trailing dot.
//
// The share host is recognised by this and not by `host:port`, because every
// difference it erases is one a stranger chooses. `share.example.com.` is the
// same name to DNS and to a certificate, and `share.example.com:444` reaches
// the same proxy; compared exactly, both were "not the share host" and got
// the panel's sign-in page on the public domain.
func hostnameOnly(rawHost string) string {
	h := strings.ToLower(strings.TrimSpace(rawHost))
	if name, _, err := net.SplitHostPort(h); err == nil {
		h = name
	}
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	return strings.TrimRight(h, ".")
}

// panelConfiguredHosts returns every normalized `host[:port]` known to belong
// to the panel itself from configuration and setup-approved origins.
func (s *Server) panelConfiguredHosts() map[string]bool {
	hosts := map[string]bool{}
	add := func(h string) {
		if h != "" {
			hosts[strings.ToLower(h)] = true
		}
	}
	if dom := strings.ToLower(strings.TrimSpace(s.Cfg.Domain)); dom != "" {
		add(dom)
		if s.Cfg.PublicPort > 0 && s.Cfg.PublicPort != 80 && s.Cfg.PublicPort != 443 {
			add(net.JoinHostPort(dom, strconv.Itoa(s.Cfg.PublicPort)))
		}
		if p := s.Cfg.Port(); p > 0 && p != 80 && p != 443 {
			add(net.JoinHostPort(dom, strconv.Itoa(p)))
		}
	}
	if h, p, err := net.SplitHostPort(s.Cfg.Addr); err == nil && h != "" && h != "0.0.0.0" && h != "::" {
		joined := net.JoinHostPort(h, p)
		add(normalizeHostPort(joined, "http"))
		add(normalizeHostPort(joined, "https"))
	}
	for _, o := range append(append([]string(nil), s.Cfg.PublicOrigins...), s.approvedOrigins()...) {
		if _, _, h, err := parseShareOrigin(o); err == nil && h != "" {
			add(h)
		}
	}
	return hosts
}

// panelHostnames is panelConfiguredHosts without the ports. The share host is
// a name on any port, so the panel's side of the comparison has to be too.
func (s *Server) panelHostnames() map[string]bool {
	names := map[string]bool{}
	for h := range s.panelConfiguredHosts() {
		if n := hostnameOnly(h); n != "" {
			names[n] = true
		}
	}
	return names
}

// classifyShareHost reports whether r arrived on the configured dedicated
// share origin, returning that origin.
//
// It fails closed on the name: once a share origin is set, a request whose
// Host is that hostname -- on any port, with or without a trailing dot -- is a
// share-host request.
//
// A Host that is neither the share name nor a name the panel knows as its own
// is deliberately NOT confined. The panel is reached by addresses that are in
// no configuration: a LAN or tailnet address, `localhost` through an SSH
// tunnel, a second name on the proxy. Confining those would lock out exactly
// the owner who is trying to sign in to turn this off, and nothing recorded
// anywhere can tell that owner from a stranger before they have signed in.
// What closes that side instead is the proxy: the vhost this file's sibling
// writes sends the literal share hostname upstream, whatever the client said.
//
// Never true when the stored share name is also a configured panel name: if
// the panel's domain is later changed to match it (or the setting was edited
// directly in SQLite), the panel must not lock itself out on its own address.
func (s *Server) classifyShareHost(ctx context.Context, r *http.Request) (string, bool, error) {
	origin, _, host, err := s.shareOriginRead(ctx)
	if err != nil {
		return "", false, err
	}
	name := hostnameOnly(host)
	if name == "" || hostnameOnly(r.Host) != name {
		return "", false, nil
	}
	if s.panelHostnames()[name] {
		return "", false, nil
	}
	return origin, true, nil
}

type shareHostCtxKey struct{}

// isShareHost is what shareHostOnly decided about this request, and the
// origin it decided for.
//
// Read from the request rather than worked out again: a second read of the
// setting further down could disagree with the first (the setting changed, or
// the read failed), and the two handlers that ask are the ones refusing
// preview and peek links on the public host.
func (s *Server) isShareHost(r *http.Request) (string, bool) {
	origin, ok := r.Context().Value(shareHostCtxKey{}).(string)
	return origin, ok
}

// shareHostOnly restricts requests arriving on the configured dedicated share
// host to the share surface (`/share/{token}*` and `/api/share/{token}/*`).
//
// Without this, pointing a second public domain at the panel for sharing also
// puts the panel's sign-in screen, `/api/health`, `/api/auth/*` and `/ws` on
// that public domain. Everything outside the share surface answers 404 — a
// static sandboxed page for navigations, JSON 404 for `/api/*` and `/ws` —
// so the panel bundle is never served on the share host.
func (s *Server) shareHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, onShareHost, err := s.classifyShareHost(r.Context(), r)
		if err != nil {
			// The setting could not be read, so this may be the share host and
			// there is no way to know. Answering as the panel is the one wrong
			// answer: it is the sign-in page on the public domain. Refusing
			// costs the panel's own host nothing it had, because every route
			// that matters reads the same database to find out who is asking,
			// and the next request reads the setting again. A caller that hung
			// up mid-read is not the database failing, so it raises no banner.
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				s.noteStale(err)
			}
			if isAPIPath(r.URL) {
				writeErr(w, http.StatusServiceUnavailable, "the panel cannot reach its own database")
				return
			}
			writeGonePage(w, true)
			return
		}
		if !onShareHost {
			next.ServeHTTP(w, r)
			return
		}
		if isShareHostPath(r.Method, r.URL) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), shareHostCtxKey{}, origin)))
			return
		}
		if isAPIPath(r.URL) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeGonePage(w, false)
	})
}

func isAPIPath(u *url.URL) bool {
	clean := path.Clean(u.Path)
	return strings.HasPrefix(clean, "/api/") || clean == "/ws"
}

// isShareHostPath is whether a request on the share host is for the share
// surface.
//
// Two spellings of the path have to agree. The router matches on the escaped
// path when the request had one (chi reads URL.RawPath), and this used to
// look only at the decoded one: `/share%2Fx` decodes to `/share/x`, passed,
// matched no share route in the router and fell through to the catch-all,
// which is the panel's bundle (red line 8: an unknown share path never gets
// the SPA). So the prefix must be there as the router will see it, and the
// cleaned decoded path must still be under it, which is what refuses
// `/share/../api/health`.
func isShareHostPath(method string, u *url.URL) bool {
	routed := u.Path
	if u.RawPath != "" {
		routed = u.RawPath
	}
	clean := path.Clean(u.Path)
	under := func(prefix string) bool {
		return strings.HasPrefix(routed, prefix) && strings.HasPrefix(clean, prefix) && len(clean) > len(prefix)
	}
	if under("/share/") {
		return method == http.MethodGet || method == http.MethodHead
	}
	if under("/api/share/") {
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions:
			return true
		}
	}
	if under("/.well-known/vibepanel-probe/") {
		return method == http.MethodGet || method == http.MethodHead
	}
	return false
}

// ShareOriginHostname returns the configured dedicated share origin's DNS
// hostname when it uses https:// and is a valid public domain, or "" otherwise.
// Used at startup under --tls acme so certmagic keeps the share domain's
// certificate managed across restarts.
func (s *Server) ShareOriginHostname(ctx context.Context) string {
	origin, scheme, host := s.shareOrigin(ctx)
	if origin == "" || scheme != "https" {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	hn := strings.ToLower(u.Hostname())
	if !validPublicDomain(hn) || hn != host {
		return ""
	}
	return hn
}

// visitorWritesAllowed is read on every visitor action, from the database, so
// turning it off stops the next action rather than the next restart.
func (s *Server) visitorWritesAllowed(ctx context.Context) bool {
	v, err := s.DB.GetSetting(ctx, visitorWritesKey, "1")
	// A database that cannot say is a no: the write is the thing that would
	// have to be undone.
	return err == nil && v == "1"
}

func (s *Server) handleGetSharing(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	origin, _, _ := s.shareOrigin(ctx)
	writeJSON(w, http.StatusOK, sharingSettingsResponse{
		VisitorWrites: s.visitorWritesAllowed(ctx),
		ShareOrigin:   origin,
	})
}

func (s *Server) handlePutSharing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VisitorWrites *bool   `json:"visitorWrites"`
		ShareOrigin   *string `json:"shareOrigin"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.VisitorWrites == nil && req.ShareOrigin == nil {
		writeErr(w, http.StatusBadRequest, "visitorWrites or shareOrigin is required")
		return
	}

	// Validate shareOrigin before writing anything, so a request carrying both
	// fields never applies half of itself.
	var nextOrigin string
	if req.ShareOrigin != nil {
		origin, _, host, err := parseShareOrigin(*req.ShareOrigin)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if why := s.collidesWithPanelHost(r, host); why != "" {
			writeErr(w, http.StatusBadRequest, why)
			return
		}
		nextOrigin = origin
	}

	ctx := r.Context()
	if req.VisitorWrites != nil {
		v := "0"
		if *req.VisitorWrites {
			v = "1"
		}
		if err := s.DB.SetSetting(ctx, visitorWritesKey, v); err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if u, ok := currentUserFrom(r); ok {
			state := "off"
			if *req.VisitorWrites {
				state = "on"
			}
			s.audit(ctx, "sharing.visitor_writes", u.Username, s.clientIP(r), state)
		}
	}
	if req.ShareOrigin != nil {
		if err := s.DB.SetSetting(ctx, shareOriginKey, nextOrigin); err != nil {
			s.writeStoreErr(w, err)
			return
		}
		s.pb.origin.forget()
		if u, ok := currentUserFrom(r); ok {
			detail := nextOrigin
			if detail == "" {
				detail = "(default)"
			}
			s.audit(ctx, "sharing.origin_changed", u.Username, s.clientIP(r), detail)
		}
	}
	origin, _, _ := s.shareOrigin(ctx)
	writeJSON(w, http.StatusOK, sharingSettingsResponse{
		VisitorWrites: s.visitorWritesAllowed(ctx),
		ShareOrigin:   origin,
	})
}

// ─── counting actions ─────────────────────────────────────────────────────

// actionDay counts visitor actions per link for today, in memory: a number the
// settings row shows, which a restart is allowed to reset. The day is the
// panel's (Server.today), passed in, so the count turns over when the
// spend figures beside it do.
type actionDay struct {
	mu     sync.Mutex
	day    string
	counts map[string]int
}

func (a *actionDay) add(linkID, day string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roll(day)
	a.counts[linkID]++
}

func (a *actionDay) today(linkID, day string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roll(day)
	return a.counts[linkID]
}

func (a *actionDay) roll(day string) {
	if a.day != day || a.counts == nil {
		a.day, a.counts = day, map[string]int{}
	}
}

// fillLinkCounts adds the in-memory figures a listed link carries.
func (s *Server) fillLinkCounts(ctx context.Context, links []store.ShareLink, now time.Time) {
	day := s.today(ctx)
	for i := range links {
		links[i].Viewers, links[i].ViewportWidth, links[i].ViewportHeight = s.viewers.count(links[i].ID, now)
		links[i].ActionsToday = s.pb.actions.today(links[i].ID, day)
	}
}
