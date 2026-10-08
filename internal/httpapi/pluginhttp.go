package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A process on the panel's port: docs/plugins.md §5, rung 3. A plugin's
// process listens on a unix socket the panel names, and the panel serves it
// at /api/plugin-http/{id}/*. The panel guards the door; the process does
// business. Everything a credential could leak through is handled here and
// never reaches the process: the cookie and the Authorization header are
// stripped, the caller is named in a header the process can trust, and the
// response is cleaned before it is sent on the panel's own origin.
//
// Three things make the trust real rather than stated:
//
//   - the socket is in a directory only this user can enter, so another
//     user cannot connect; and because every process of *this* user can
//     (a coding agent in a session is this user), every proxied request
//     carries a per-start secret in X-Vibepanel-Proxy that the process
//     checks, so a request that did not come through the panel is one the
//     process refuses;
//   - the response's content type decides whether the browser may render it
//     on the panel's origin. JSON, an event stream, text, audio, a raster
//     image and a byte stream may; anything else -- HTML above all, and SVG,
//     which carries script -- is served under `Content-Security-Policy:
//     sandbox` as an attachment. An HTML page a process returned, rendered
//     here, would be rung 4 without the switch;
//   - which origins may call across is the owner's list on the card, never
//     the plugin's declaration, exact matches only, and a cross-origin call
//     is Bearer only: the cookie is never a credential across origins, so
//     there is no CSRF to defend.
//
// There is no anonymous mode. "owner" is the panel's own session or API
// token; "token" is a plugin access token the owner mints on the card;
// "hmac" is a shared secret, as the inbound door.

const (
	pluginHTTPRatePlugin   = 600 // requests per minute, per plugin
	pluginHTTPRateCaller   = 120 // per minute, per token or owner
	pluginHTTPStreams      = 8   // open streaming responses per plugin
	pluginHTTPInflight     = 32  // in-flight requests per plugin
	pluginHTTPBudget       = 60 * time.Second
	pluginHTTPMaxOrigins   = 20
	pluginHTTPCallerHeader = "X-Vibepanel-Caller"
	pluginHTTPProxyHeader  = "X-Vibepanel-Proxy"
	pluginHTTPMountPrefix  = "/api/plugin-http/"
)

type pluginHTTPState struct {
	mu       sync.Mutex
	rate     map[string][]time.Time
	inflight map[string]int
	streams  map[string]int
}

// pluginHTTPMethods is what the door forwards: the methods an HTTP API is
// made of. Not CONNECT, TRACE or QUERY, which no plugin needs and which
// would be two more things to think about at a door.
var pluginHTTPMethods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions}

func (s *Server) registerPluginHTTPRoute(r chi.Router) {
	for _, m := range pluginHTTPMethods {
		r.Method(m, "/plugin-http/{pluginID}", http.HandlerFunc(s.handlePluginHTTP))
		r.Method(m, "/plugin-http/{pluginID}/*", http.HandlerFunc(s.handlePluginHTTP))
	}
}

func (s *Server) registerPluginHTTPOwnerRoutes(r chi.Router) {
	r.Get("/settings/plugins/{pluginID}/tokens", s.handleListPluginAccessTokens)
	r.Post("/settings/plugins/{pluginID}/tokens", s.handleCreatePluginAccessToken)
	r.Delete("/settings/plugins/{pluginID}/tokens/{tokenID}", s.handleRevokePluginAccessToken)
	r.Get("/settings/plugins/{pluginID}/origins", s.handleGetPluginOrigins)
	r.Put("/settings/plugins/{pluginID}/origins", s.handlePutPluginOrigins)
}

// pluginMountPath is where a plugin's process answers.
func pluginMountPath(id string) string { return pluginHTTPMountPrefix + id + "/" }

// pluginSocketDir is where the sockets live: the user's runtime directory
// when there is one, else a directory of this user's under /tmp. Not the
// data directory: a unix socket path is at most 108 bytes on Linux and 104
// on macOS, and <data>/plugins/<40-character id>/state/http.sock under a
// long HOME is over it.
func pluginSocketDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "vibepanel")
	}
	return filepath.Join(os.TempDir(), "vibepanel-"+strconv.Itoa(os.Getuid()))
}

// pluginSocketPath is one plugin's socket for this panel instance: the data
// directory is in the name so two panels of one user (a test server, a
// second instance) do not share a socket.
func (s *Server) pluginSocketPath(pluginID string) string {
	sum := sha256.Sum256([]byte(s.Cfg.DataDir))
	return filepath.Join(pluginSocketDir(), "p-"+pluginID+"-"+hex.EncodeToString(sum[:4])+".sock")
}

// pluginSocketFor is the running process's socket and per-start secret.
func (s *Server) pluginSocketFor(pluginID string) (sock, secret string, ok bool) {
	st := &s.ppr
	st.mu.Lock()
	pr := st.procs[pluginID]
	st.mu.Unlock()
	if pr == nil {
		return "", "", false
	}
	pr.stateMu.Lock()
	defer pr.stateMu.Unlock()
	if pr.pid == 0 || pr.socket == "" {
		return "", "", false
	}
	return pr.socket, pr.proxySecret, true
}

// pluginOrigins is the owner's list of origins that may call a plugin's
// mount across origins. A setting, not a column: it is the owner's and not
// the plugin's, and it survives the plugin's versions.
func (s *Server) pluginOrigins(ctx context.Context, pluginID string) []string {
	raw, err := s.DB.GetSetting(ctx, "plugins."+pluginID+".origins", "[]")
	if err != nil {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// cleanOrigin is one origin as the owner typed it, or an error: scheme and
// host, lower-case, no path. Exact matches only, so what is stored is what
// is compared.
func cleanOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%q is not an origin: https://host[:port], nothing after it", raw)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

// ─── the door ─────────────────────────────────────────────────────────────

func (s *Server) handlePluginHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.Auth != nil && !auth.Allowed(s.clientIP(r), s.Auth.Allow) {
		writeErr(w, http.StatusForbidden, "not allowed from this address")
		return
	}
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such plugin")
		return
	}
	c, ok, err := s.pluginServiceCred(ctx, p)
	if err != nil || !ok || c.manifest.Process == nil || c.manifest.Process.HTTP == nil {
		writeErr(w, http.StatusNotFound, "this plugin has no door on the panel's port")
		return
	}
	spec := c.manifest.Process.HTTP

	// Cross-origin first, before any credential is looked at: a preflight
	// carries none, and a request from an origin the owner did not list is
	// refused whatever it carries.
	origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
	cross := origin != "" && origin != "null" && !sameOrigin(hostOfOrigin(origin), r.Host)
	if cross {
		if !contains(s.pluginOrigins(ctx, p.ID), strings.ToLower(origin)) {
			writeErr(w, http.StatusForbidden, "this origin is not on the plugin's list; the owner adds it on the plugin's card")
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Signature-256")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	// Who is calling. Across origins only a Bearer counts: the cookie is a
	// browser's and a browser on another origin is exactly what CSRF is.
	var caller, rateKey string
	var hmacBody []byte
	switch spec.Auth {
	case "owner":
		if cross && bearerToken(r) == "" {
			writeErr(w, http.StatusUnauthorized, "across origins this door takes a Bearer token, not a cookie")
			return
		}
		u, ok, uerr := s.currentUser(r)
		if uerr != nil {
			s.writeStoreErr(w, uerr)
			return
		}
		if !ok {
			writeErr(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if !cross && unsafeMethod(r.Method) && crossOriginWrite(r, s.publicOrigins(r)) {
			writeErr(w, http.StatusForbidden, "a write from another origin")
			return
		}
		caller, rateKey = "owner:"+u.Username, "owner"
	case "token":
		bearer := bearerToken(r)
		if bearer == "" {
			writeErr(w, http.StatusUnauthorized, "this door takes a plugin token: Authorization: Bearer …")
			return
		}
		hash := auth.HashToken(bearer)
		tok, terr := s.DB.PluginAccessTokenByHash(ctx, hash)
		if errors.Is(terr, store.ErrNotFound) || (terr == nil && tok.PluginID != p.ID) {
			s.pluginLog(p.ID, "warn", "a request with a token this plugin does not know, from "+s.clientIP(r))
			writeErr(w, http.StatusUnauthorized, "that token is not one of this plugin's")
			return
		}
		if terr != nil {
			s.writeStoreErr(w, terr)
			return
		}
		if s.touchCooldowns().Allow("plugin-access", tok.ID, time.Now()) {
			_ = s.DB.TouchPluginAccessToken(ctx, tok.ID)
		}
		caller, rateKey = "token:"+tok.Name, "token:"+tok.ID
	case "hmac":
		body, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, spec.MaxBodyBytes()))
		if rerr != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a body is at most %d bytes", spec.MaxBodyBytes()))
			return
		}
		secret, serr := s.pluginSecretValue(ctx, p.ID, spec.Secret)
		if serr != nil || !inboundVerified(r, body, secret) {
			s.pluginLog(p.ID, "warn", "a request that failed the secret check, from "+s.clientIP(r))
			writeErr(w, http.StatusUnauthorized, "the request did not verify against the plugin's secret")
			return
		}
		hmacBody = body
		caller, rateKey = "hmac", "hmac"
	default:
		writeErr(w, http.StatusNotFound, "this plugin has no door on the panel's port")
		return
	}

	// The limits: per plugin and per caller, then in-flight, then streams.
	stream := spec.Stream && wantsStream(r)
	if retry, over := s.pluginHTTPAdmit(p.ID, rateKey, stream); over != "" {
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		writeErr(w, http.StatusTooManyRequests, over)
		return
	}
	defer s.pluginHTTPRelease(p.ID, stream)

	sock, proxySecret, up := s.pluginSocketFor(p.ID)
	if !up {
		writeErr(w, http.StatusServiceUnavailable, "the plugin's process is not running")
		return
	}
	if _, err := os.Stat(sock); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "the plugin's process has not opened its socket yet")
		return
	}

	// The path under the mount, cleaned and kept under it.
	rel := "/" + chi.URLParam(r, "*")
	if strings.Contains(rel, "\\") {
		writeErr(w, http.StatusBadRequest, "a path under the mount")
		return
	}
	clean := path.Clean(rel)
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			writeErr(w, http.StatusBadRequest, "a path under the mount")
			return
		}
	}
	if strings.HasSuffix(rel, "/") && clean != "/" {
		clean += "/"
	}

	if hmacBody != nil {
		r.Body = io.NopCloser(strings.NewReader(string(hmacBody)))
		r.ContentLength = int64(len(hmacBody))
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, spec.MaxBodyBytes())
	}
	deadline := pluginHTTPBudget
	if stream {
		deadline = spec.IdleTimeoutOrDefault()
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(deadline, cancel)
	defer idle.Stop()

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "plugin"
			pr.Out.URL.Path = clean
			pr.Out.URL.RawPath = ""
			pr.Out.Host = "plugin"
			// Nothing of the owner's reaches the process: not the cookie, not
			// the token, and nothing that claims to be from the panel.
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			for k := range pr.Out.Header {
				if strings.HasPrefix(strings.ToLower(k), "x-vibepanel-") {
					pr.Out.Header.Del(k)
				}
			}
			pr.Out.Header.Set(pluginHTTPCallerHeader, caller)
			pr.Out.Header.Set(pluginHTTPProxyHeader, proxySecret)
			pr.Out.Header.Set("X-Forwarded-Prefix", strings.TrimSuffix(pluginMountPath(p.ID), "/"))
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
			DisableCompression:    true,
			ResponseHeaderTimeout: pluginHTTPBudget,
			MaxIdleConnsPerHost:   4,
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			sanitisePluginResponse(resp)
			if stream {
				resp.Body = &idleBody{ReadCloser: resp.Body, idle: idle, every: deadline}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			s.pluginLog(p.ID, "error", "proxy: "+firstLine(err.Error()))
			writeErr(w, http.StatusBadGateway, "the plugin's process did not answer: "+firstLine(err.Error()))
		},
	}
	proxy.ServeHTTP(w, r.WithContext(pctx))
}

// wantsStream is a request that asked for an event stream, or any request
// when the process is allowed to stream and the client did not say; the
// idle timeout then bounds it rather than the request budget.
func wantsStream(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/event-stream") || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		r.Header.Get("X-Vibepanel-Stream") == "1"
}

// idleBody resets the idle timer on every read: a stream that keeps sending
// stays open; one that goes quiet for the idle timeout is ended.
type idleBody struct {
	io.ReadCloser
	idle  *time.Timer
	every time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.idle.Reset(b.every)
	}
	return n, err
}

// pluginHTTPAdmit applies the rate limits and the in-flight caps. It returns
// a Retry-After in seconds and a reason, or "" when admitted; an admitted
// request is released by pluginHTTPRelease.
func (s *Server) pluginHTTPAdmit(pluginID, caller string, stream bool) (int, string) {
	st := &s.phx
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.rate == nil {
		st.rate, st.inflight, st.streams = map[string][]time.Time{}, map[string]int{}, map[string]int{}
	}
	now := time.Now()
	check := func(key string, limit int) bool {
		recent := st.rate[key][:0]
		for _, t := range st.rate[key] {
			if now.Sub(t) < time.Minute {
				recent = append(recent, t)
			}
		}
		st.rate[key] = recent
		return len(recent) >= limit
	}
	if check(pluginID, pluginHTTPRatePlugin) {
		return 60, "this plugin's door is busy: " + strconv.Itoa(pluginHTTPRatePlugin) + " requests a minute"
	}
	if check(pluginID+"\x00"+caller, pluginHTTPRateCaller) {
		return 60, "too many requests from this caller: " + strconv.Itoa(pluginHTTPRateCaller) + " a minute"
	}
	if st.inflight[pluginID] >= pluginHTTPInflight {
		return 1, "this plugin has too many requests in flight"
	}
	if stream && st.streams[pluginID] >= pluginHTTPStreams {
		return 5, "this plugin has too many streams open"
	}
	st.rate[pluginID] = append(st.rate[pluginID], now)
	st.rate[pluginID+"\x00"+caller] = append(st.rate[pluginID+"\x00"+caller], now)
	st.inflight[pluginID]++
	if stream {
		st.streams[pluginID]++
	}
	return 0, ""
}

func (s *Server) pluginHTTPRelease(pluginID string, stream bool) {
	st := &s.phx
	st.mu.Lock()
	defer st.mu.Unlock()
	st.inflight[pluginID]--
	if stream {
		st.streams[pluginID]--
	}
}

// sanitisePluginResponse is what makes a process's answer safe to send on
// the panel's own origin. See the file comment.
func sanitisePluginResponse(resp *http.Response) {
	h := resp.Header
	for _, k := range []string{"Set-Cookie", "Set-Cookie2", "Content-Security-Policy", "Content-Security-Policy-Report-Only",
		"Clear-Site-Data", "Strict-Transport-Security", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials",
		"Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Expose-Headers", "Public-Key-Pins",
		"Report-To", "Reporting-Endpoints", "Service-Worker-Allowed", "Link"} {
		h.Del(k)
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-store")
	}
	if !renderablePluginType(h.Get("Content-Type")) {
		h.Set("Content-Security-Policy", "sandbox")
		if h.Get("Content-Disposition") == "" {
			h.Set("Content-Disposition", "attachment")
		}
	}
}

// renderablePluginType says whether a content type may be rendered on the
// panel's origin as it is. Image types are allowed except SVG and anything
// XML, which carry script; HTML never is.
func renderablePluginType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	mt = strings.ToLower(mt)
	switch mt {
	case "application/json", "text/event-stream", "text/plain", "application/octet-stream", "text/csv":
		return true
	}
	if strings.HasSuffix(mt, "+json") {
		return true
	}
	if strings.HasPrefix(mt, "audio/") || strings.HasPrefix(mt, "video/") {
		return true
	}
	if strings.HasPrefix(mt, "image/") && !strings.Contains(mt, "svg") && !strings.HasSuffix(mt, "+xml") && !strings.Contains(mt, "xml") {
		return true
	}
	return false
}

// ─── the owner's side: tokens and origins ─────────────────────────────────

func (s *Server) handleListPluginAccessTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListPluginAccessTokens(r.Context(), chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleCreatePluginAccessToken mints a token and shows it once; the row
// keeps the hash and the name.
func (s *Server) handleCreatePluginAccessToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\n\r") {
		writeErr(w, http.StatusBadRequest, "a name is one short line: what holds this token")
		return
	}
	token, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tid := id.New()
	if err := s.DB.CreatePluginAccessToken(ctx, tid, p.ID, name, auth.HashToken(token)); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.token_created", u.Username, s.clientIP(r), p.ID+" "+name)
	row, _ := s.DB.ListPluginAccessTokens(ctx, p.ID)
	var made store.PluginAccessToken
	for _, t := range row {
		if t.ID == tid {
			made = t
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "row": made, "mount": pluginMountPath(p.ID)})
}

func (s *Server) handleRevokePluginAccessToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	pid, tid := chi.URLParam(r, "pluginID"), chi.URLParam(r, "tokenID")
	if err := s.DB.RevokePluginAccessToken(ctx, pid, tid); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.token_revoked", u.Username, s.clientIP(r), pid+" "+tid)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetPluginOrigins(w http.ResponseWriter, r *http.Request) {
	list := s.pluginOrigins(r.Context(), chi.URLParam(r, "pluginID"))
	if list == nil {
		list = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"origins": list})
}

func (s *Server) handlePutPluginOrigins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	var req struct {
		Origins []string `json:"origins"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Origins) > pluginHTTPMaxOrigins {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("at most %d origins", pluginHTTPMaxOrigins))
		return
	}
	clean := []string{}
	for _, o := range req.Origins {
		if strings.TrimSpace(o) == "" {
			continue
		}
		c, err := cleanOrigin(o)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if !contains(clean, c) {
			clean = append(clean, c)
		}
	}
	raw, _ := json.Marshal(clean)
	if err := s.DB.SetSetting(ctx, "plugins."+p.ID+".origins", string(raw)); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.origins_changed", u.Username, s.clientIP(r), p.ID+" "+strings.Join(clean, " "))
	writeJSON(w, http.StatusOK, map[string]any{"origins": clean})
}
