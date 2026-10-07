package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// Rung 1, a panel: docs/plugins.md §5. Three routes, two credentials, and the
// shape is the admin page's:
//
//	POST /api/settings/plugins/{id}/grant   the panel session  -> mints a grant
//	/plugin/{grant}/...                      the grant          -> the plugin's files, sandboxed
//	/api/plugin/{cred}/v1/...                the grant          -> what the capability table opens
//
// The frame never has the cookie: it is the plugin's HTML, served with an
// opaque origin like a share page, and the grant in its path is the only
// credential it holds. Which routes under v1 answer is decided by
// plugins.RouteTable and nowhere else (red line 10), and a route named by no
// capability is open to every credential: the plugin's own data and settings.
//
// Handles. A plugin never sees the panel's ids: every session, project and
// todo is named by HMAC(salt + plugin id, real id), stable for one plugin and
// different between plugins, so two plugins cannot be joined on what they
// saw. Resolving a handle back is a scan of the lists the plugin may see,
// which is dozens of rows and is the whole reason the handle can be a hash.

// pluginGrantTTL is how long a grant lives; it also dies with its session.
const pluginGrantTTL = 8 * time.Hour

// pluginViewCoalesce is how long after a change the stream waits before
// building the next view, so a burst is one message.
const pluginViewCoalesce = 150 * time.Millisecond

const pluginHandleSaltKey = "plugins.handle_salt"

type pluginCredKey struct{}

// pluginCred is what every v1 handler reads from the request: the plugin,
// the manifest it runs on, which namespace its data is in, and the
// capabilities granted today.
type pluginCred struct {
	grant    store.PluginGrant
	plugin   store.Plugin
	manifest plugins.Manifest
	ns       string
	caps     map[string]bool
	user     string
}

func (c pluginCred) has(cap string) bool { return c.caps[cap] }

func pluginCredFrom(r *http.Request) (pluginCred, bool) {
	c, ok := r.Context().Value(pluginCredKey{}).(pluginCred)
	return c, ok
}

// pluginRuntime is the server's in-memory state for frames.
type pluginRuntime struct {
	saltMu sync.Mutex
	salt   []byte
	bus    pluginBus
	// The panel state the views are cut from, built once per change for
	// every open stream and poll rather than once per stream: see
	// pluginState.
	stateMu  sync.Mutex
	stateGen uint64
	stateAt  time.Time
	state    stateResponse
	// stateBuilds counts the slow path, for the test that says it is one.
	stateBuilds int
	// pluginsRev is stateResponse.PluginsRev: bumped by pluginsChanged.
	pluginsRev atomic.Uint64
}

// pluginStateTTL bounds the memo in time as well as by generation: a change
// nothing bumped for is seen within this long by a frame that polls.
const pluginStateTTL = time.Second

// pluginState is buildState memoised on the bus generation. Every change the
// panel notices bumps the bus, so one build per bump is exact; a dozen open
// frames on a change are a dozen cuts of one state rather than a dozen
// reads of every table.
func (s *Server) pluginState(ctx context.Context) (stateResponse, error) {
	gen := s.prt.bus.generation()
	s.prt.stateMu.Lock()
	defer s.prt.stateMu.Unlock()
	if s.prt.stateAt.IsZero() || s.prt.stateGen != gen || time.Since(s.prt.stateAt) > pluginStateTTL {
		st, err := s.buildState(ctx)
		if err != nil {
			return stateResponse{}, err
		}
		s.prt.state, s.prt.stateGen, s.prt.stateAt = st, gen, time.Now()
		s.prt.stateBuilds++
	}
	return s.prt.state, nil
}

// pluginBus wakes every open event stream when anything changed. Closing a
// channel is the broadcast; the next one is made for the next change.
type pluginBus struct {
	mu  sync.Mutex
	ch  chan struct{}
	gen uint64
}

// generation counts bumps; the state memo is keyed on it.
func (b *pluginBus) generation() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen
}

func (b *pluginBus) wait() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch == nil {
		b.ch = make(chan struct{})
	}
	return b.ch
}

func (b *pluginBus) bump() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch != nil {
		close(b.ch)
	}
	b.gen++
	b.ch = make(chan struct{})
}

// bumpPluginWatchers is called wherever the panel's state or a project's
// notes changed; every open event stream sends its plugin a fresh view.
func (s *Server) bumpPluginWatchers() {
	s.prt.bus.bump()
}

// ─── routes ───────────────────────────────────────────────────────────────

func (s *Server) registerPluginFrameRoutes(r chi.Router) {
	r.Get("/plugin/{grant}", s.handlePluginFile)
	r.Get("/plugin/{grant}/*", s.handlePluginFile)
}

// registerPluginAPIRoutes mounts v1 under /api, outside the session group,
// below its own middleware -- the share routes' placement, for the share
// routes' reason. Every route comes from plugins.RouteTable; a route in the
// table with no handler here is a panic at startup rather than a 404 at
// runtime, and TestEveryCapabilityOpensOnlyItsRoutes walks the result.
func (s *Server) registerPluginAPIRoutes(r chi.Router) {
	handlers := s.pluginHandlers()
	r.Route("/plugin/{cred}/v1", func(r chi.Router) {
		r.Use(s.requirePluginCred)
		table := plugins.RouteTable()
		keys := make([]string, 0, len(table))
		for k := range table {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			method, pattern, _ := strings.Cut(key, " ")
			h, ok := handlers[key]
			if !ok {
				panic("plugins: route " + key + " is in the capability table and has no handler")
			}
			r.With(s.requirePluginCaps(table[key])).Method(method, pattern, h)
		}
	})
}

func (s *Server) pluginHandlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /me":                    s.handlePluginMe,
		"GET /settings":              s.handlePluginSettingsValues,
		"GET /data":                  s.handlePluginData,
		"PUT /data/{key}":            s.handlePluginDataOp("set"),
		"POST /data/{key}/increment": s.handlePluginDataOp("increment"),
		"POST /data/{key}/append":    s.handlePluginDataOp("append"),
		"DELETE /data/{key}":         s.handlePluginDataOp("reset"),
		"GET /view":                  s.handlePluginView,
		"GET /events":                s.handlePluginEvents,
		"GET /sessions/{h}/screen":   s.handlePluginScreen,
		"GET /projects/{h}/notes":    s.handlePluginGetNote,
		"PUT /projects/{h}/notes":    s.handlePluginPutNote,
		"GET /projects/{h}/todos":    s.handlePluginListTodos,
		"POST /projects/{h}/todos":   s.handlePluginCreateTodo,
		"PATCH /todos/{h}":           s.handlePluginPatchTodo,
		"DELETE /todos/{h}":          s.handlePluginDeleteTodo,
		"PATCH /sessions/{h}/state":  s.handlePluginSessionState,
		"GET /resources":             s.handlePluginResources,
		"GET /usage":                 s.handlePluginUsage,
		"GET /projects/{h}/git":      s.handlePluginGit,
		"POST /sessions/{h}/restart": s.handlePluginSessionRestart,
		"DELETE /sessions/{h}":       s.handlePluginSessionDelete,
		"POST /sessions":             s.handlePluginSessionCreate,
		"POST /sessions/{h}/input":   s.handlePluginSessionInput,
		"GET /x/*":                   s.handlePluginFrameRoute,
		"POST /x/*":                  s.handlePluginFrameRoute,
		"PUT /x/*":                   s.handlePluginFrameRoute,
		"PATCH /x/*":                 s.handlePluginFrameRoute,
		"DELETE /x/*":                s.handlePluginFrameRoute,
	}
}

// requirePluginCred resolves the credential in the path: a grant today, a
// process's token later. Also the CORS preflight a frame's writes need: its
// origin is opaque, so every request it makes is cross-origin, and the
// credential in the path -- never a cookie -- is what authorises it.
func (s *Server) requirePluginCred(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Cache-Control", "no-store")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, PUT, POST, PATCH, DELETE")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		ctx := r.Context()
		ip := s.clientIP(r)
		if s.Auth != nil && !auth.Allowed(ip, s.Auth.Allow) {
			s.auditFromOutside(ctx, "blocked", "", ip, "address not in the allowlist")
			writeErr(w, http.StatusForbidden, "not allowed from this address")
			return
		}
		cred, ok, err := s.resolvePluginCred(ctx, chi.URLParam(r, "cred"))
		if err != nil {
			s.noteStale(err)
			writeErr(w, http.StatusServiceUnavailable, "the panel cannot reach its own database")
			return
		}
		if !ok {
			s.auditFromOutside(ctx, "plugin.rejected", "", ip, "unknown, expired or signed-out plugin credential")
			writeErr(w, http.StatusUnauthorized, "this plugin credential has expired; reload the panel")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, pluginCredKey{}, cred)))
	})
}

// resolvePluginCred turns a presented credential into what a handler needs,
// or (false, nil) for one that resolves to nothing.
func (s *Server) resolvePluginCred(ctx context.Context, token string) (pluginCred, bool, error) {
	hash := auth.HashToken(token)
	grant, err := s.DB.PluginGrantByToken(ctx, hash)
	process := false
	if errors.Is(err, store.ErrNotFound) {
		// Not a grant: a process's token, which has the plugin's grants
		// narrowed to the list its process declared.
		id, terr := s.DB.PluginTokenByHash(ctx, hash)
		if errors.Is(terr, store.ErrNotFound) {
			return pluginCred{}, false, nil
		}
		if terr != nil {
			return pluginCred{}, false, terr
		}
		grant, err, process = store.PluginGrant{PluginID: id}, nil, true
	}
	if err != nil {
		return pluginCred{}, false, err
	}
	p, err := s.DB.PluginByID(ctx, grant.PluginID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return pluginCred{}, false, nil
		}
		return pluginCred{}, false, err
	}
	m, ns, err := s.pluginRunningManifest(ctx, p)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return pluginCred{}, false, nil
		}
		return pluginCred{}, false, err
	}
	caps, err := s.DB.PluginCaps(ctx, p.ID)
	if err != nil {
		return pluginCred{}, false, err
	}
	granted := map[string]bool{}
	wanted := m.AllCapabilities()
	for _, c := range caps {
		// A grant is a decision about the manifest that was shown; a
		// capability the running manifest no longer asks for is not held.
		if !contains(wanted, c.Cap) {
			continue
		}
		// A process holds only what its own list declares, of what the owner
		// granted: the frame's ui:* never, and nothing the process did not
		// say it would use.
		if process && (m.Process == nil || !contains(m.Process.Capabilities, c.Cap)) {
			continue
		}
		granted[c.Cap] = true
	}
	user := "plugin:" + p.ID
	if !process {
		user = grant.UserID
		if u, err := s.DB.UserByID(ctx, grant.UserID); err == nil {
			user = u.Username
		}
	}
	return pluginCred{grant: grant, plugin: p, manifest: m, ns: ns, caps: granted, user: user}, true, nil
}

// pluginRunningManifest is the manifest a plugin runs on right now and the
// data namespace that goes with it: the draft directory's in dev mode, the
// installed version's otherwise.
func (s *Server) pluginRunningManifest(ctx context.Context, p store.Plugin) (plugins.Manifest, string, error) {
	if p.Dev && p.SourceDir != "" {
		raw, err := plugins.ReadManifestFile(p.SourceDir)
		if err != nil {
			return plugins.Manifest{}, "", fmt.Errorf("%w: %v", store.ErrNotFound, err)
		}
		m, err := plugins.ParseManifest(raw)
		if err != nil {
			return plugins.Manifest{}, "", fmt.Errorf("%w: %v", store.ErrNotFound, err)
		}
		if m.ID != p.ID {
			return plugins.Manifest{}, "", fmt.Errorf("%w: the draft's id is %s, not %s", store.ErrNotFound, m.ID, p.ID)
		}
		return m, store.PageDataDraft, nil
	}
	if p.InstalledVersion == 0 {
		return plugins.Manifest{}, "", store.ErrNotFound
	}
	row, err := s.DB.PluginVersionByNumber(ctx, p.ID, p.InstalledVersion)
	if err != nil {
		return plugins.Manifest{}, "", err
	}
	m, _ := plugins.DecodeStored(row.Manifest)
	return m, store.PageDataLive, nil
}

// requirePluginCaps refuses a route unless the credential holds one of the
// capabilities that open it. An empty list is a route open to every
// credential. The refusal names the capability, so the SDK can say which
// box was unticked.
func (s *Server) requirePluginCaps(caps []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(caps) > 0 {
				c, ok := pluginCredFrom(r)
				if !ok {
					writeErr(w, http.StatusUnauthorized, "no plugin credential")
					return
				}
				held := false
				for _, cap := range caps {
					if c.has(cap) {
						held = true
					}
				}
				if !held {
					writeJSON(w, http.StatusForbidden, map[string]any{
						"error": "this plugin was not granted " + caps[0], "cap": caps[0]})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ─── the frame's files ────────────────────────────────────────────────────

func pluginFrameCSP(origin, grant string) string {
	self, api := "'none'", "'none'"
	if origin != "" {
		self = origin + "/plugin/" + grant + "/"
		api = origin + "/api/plugin/" + grant + "/v1/"
	}
	return "default-src 'none'; " +
		"script-src 'unsafe-inline' " + self + "; " +
		"style-src 'unsafe-inline' " + self + "; " +
		"img-src " + self + " data: blob:; " +
		"font-src " + self + " data:; " +
		"connect-src " + api + " " + self + "; " +
		"worker-src 'none'; manifest-src 'none'; frame-src 'none'; " +
		"form-action 'none'; base-uri 'none'; " +
		// Framed by the panel and nothing else: a page on another site that
		// framed a plugin would be able to postMessage into it.
		"frame-ancestors 'self'; " +
		"sandbox allow-scripts allow-forms"
}

func (s *Server) handlePluginFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)
	if s.Auth != nil && !auth.Allowed(ip, s.Auth.Allow) {
		writeErr(w, http.StatusForbidden, "not allowed from this address")
		return
	}
	grantToken := chi.URLParam(r, "grant")
	cred, ok, err := s.resolvePluginCred(ctx, grantToken)
	if err != nil {
		s.noteStale(err)
		writeErr(w, http.StatusServiceUnavailable, "the panel cannot reach its own database")
		return
	}
	if !ok {
		writeErr(w, http.StatusUnauthorized, "this plugin credential has expired; reload the panel")
		return
	}
	rel := chi.URLParam(r, "*")
	if rel == "" {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", pluginFrameCSP(s.requestOrigin(r), grantToken))
	h.Set("Permissions-Policy", sharePagePermissions)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Access-Control-Allow-Origin", "*")

	m := cred.manifest
	switch rel {
	case plugins.SDKFile:
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(plugins.SDK)
		return
	case plugins.UIFile:
		// The panel's tokens, with the enabled plugin themes appended, so a
		// frame under a plugin theme recolours with the page around it.
		_, themes, _ := s.enabledThemes(ctx)
		h.Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write([]byte(strings.Replace(string(plugins.UI), "/* @vibepanel-themes */", themes, 1)))
		return
	}
	// Server code and the rung-4 module are never served to a frame: the
	// owner reads them in the directory.
	if (m.Server != nil && rel == m.Server.Entry) || (m.Unsandboxed != nil && rel == m.Unsandboxed.Entry) {
		http.NotFound(w, r)
		return
	}
	var ct string
	var data []byte
	if cred.ns == store.PageDataDraft {
		f, ferr := plugins.ReadFile(cred.plugin.SourceDir, rel)
		if ferr != nil {
			if errors.Is(ferr, fs.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, ferr.Error(), http.StatusUnprocessableEntity)
			return
		}
		ct, data = f.ContentType, f.Data
	} else {
		if !plugins.ValidPath(rel) {
			http.NotFound(w, r)
			return
		}
		ct, data, err = s.DB.PluginFileData(ctx, cred.plugin.ID, cred.plugin.InstalledVersion, rel)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// ─── handles ──────────────────────────────────────────────────────────────

func (s *Server) pluginHandleSalt(ctx context.Context) ([]byte, error) {
	s.prt.saltMu.Lock()
	defer s.prt.saltMu.Unlock()
	if s.prt.salt != nil {
		return s.prt.salt, nil
	}
	v, err := s.DB.GetSetting(ctx, pluginHandleSaltKey, "")
	if err != nil {
		return nil, err
	}
	if v == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		v = hex.EncodeToString(raw)
		if err := s.DB.SetSetting(ctx, pluginHandleSaltKey, v); err != nil {
			return nil, err
		}
	}
	s.prt.salt = []byte(v)
	return s.prt.salt, nil
}

// pluginHandle is a plugin's name for one of the panel's ids.
func (s *Server) pluginHandle(ctx context.Context, pluginID, realID string) string {
	salt, err := s.pluginHandleSalt(ctx)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, append(append([]byte{}, salt...), []byte(":"+pluginID)...))
	mac.Write([]byte(realID))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

func (s *Server) pluginSessionByHandle(ctx context.Context, pluginID, h string) (store.Session, bool, error) {
	list, err := s.DB.ListSessions(ctx)
	if err != nil {
		return store.Session{}, false, err
	}
	for _, sess := range list {
		if s.pluginHandle(ctx, pluginID, sess.ID) == h {
			return sess, true, nil
		}
	}
	return store.Session{}, false, nil
}

func (s *Server) pluginProjectByHandle(ctx context.Context, pluginID, h string) (store.Project, bool, error) {
	list, err := s.DB.ListProjects(ctx)
	if err != nil {
		return store.Project{}, false, err
	}
	for _, p := range list {
		if s.pluginHandle(ctx, pluginID, p.ID) == h {
			return p, true, nil
		}
	}
	return store.Project{}, false, nil
}

func (s *Server) pluginTodoByHandle(ctx context.Context, pluginID, h string) (store.Todo, bool, error) {
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		return store.Todo{}, false, err
	}
	for _, p := range projects {
		todos, err := s.DB.ListTodos(ctx, p.ID)
		if err != nil {
			return store.Todo{}, false, err
		}
		for _, t := range todos {
			if s.pluginHandle(ctx, pluginID, t.ID) == h {
				return t, true, nil
			}
		}
	}
	return store.Todo{}, false, nil
}

// ─── the view ─────────────────────────────────────────────────────────────

// pluginView is the panel as a plugin sees it: a restated struct, built the
// way buildShareReading is, so a field added to store.Session is not
// disclosed by default. Paths are filled only with read:paths.
type pluginView struct {
	V        int                 `json:"v"`
	At       int64               `json:"at"`
	Plugin   pluginIdentity      `json:"plugin"`
	Caps     []string            `json:"caps"`
	Projects []pluginViewProject `json:"projects"`
	Sessions []pluginViewSession `json:"sessions"`
}

type pluginIdentity struct {
	ID      string       `json:"id"`
	Name    plugins.Text `json:"name"`
	Version string       `json:"version"`
}

type pluginViewProject struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Pinned   bool   `json:"pinned"`
	Path     string `json:"path"`
	Sessions int    `json:"sessions"`
	Waiting  int    `json:"waiting"`
}

type pluginViewSession struct {
	ID             string         `json:"id"`
	ProjectID      string         `json:"projectId"`
	Name           string         `json:"name"`
	State          session.State  `json:"state"`
	StateSource    session.Source `json:"stateSource"`
	StateChangedAt int64          `json:"stateChangedAt"`
	Kind           string         `json:"kind"`
	Exited         bool           `json:"exited"`
	ExitStatus     int            `json:"exitStatus"`
	Pinned         bool           `json:"pinned"`
	Live           bool           `json:"live"`
	Restored       bool           `json:"restored"`
	CWD            string         `json:"cwd"`
	Command        string         `json:"command"`
}

func (s *Server) pluginIdentityFor(c pluginCred) pluginIdentity {
	return pluginIdentity{ID: c.plugin.ID, Name: c.manifest.Name, Version: c.manifest.Version}
}

func (s *Server) pluginCapsList(c pluginCred) []string {
	out := []string{}
	for _, name := range c.manifest.AllCapabilities() {
		if c.has(name) {
			out = append(out, name)
		}
	}
	return out
}

func (s *Server) buildPluginView(ctx context.Context, c pluginCred) (pluginView, error) {
	st, err := s.pluginState(ctx)
	if err != nil {
		return pluginView{}, err
	}
	paths := c.has(plugins.CapReadPaths)
	live := map[string]bool{}
	for _, id := range st.Live {
		live[id] = true
	}
	v := pluginView{V: 1, At: time.Now().Unix(), Plugin: s.pluginIdentityFor(c), Caps: s.pluginCapsList(c),
		Projects: []pluginViewProject{}, Sessions: []pluginViewSession{}}
	counts := map[string][2]int{}
	for _, sess := range st.Sessions {
		if sess.Scratch {
			continue
		}
		n := counts[sess.ProjectID]
		n[0]++
		if sess.State == session.StateWaiting {
			n[1]++
		}
		counts[sess.ProjectID] = n
		row := pluginViewSession{
			ID: s.pluginHandle(ctx, c.plugin.ID, sess.ID), ProjectID: s.pluginHandle(ctx, c.plugin.ID, sess.ProjectID),
			Name: sess.Title, State: sess.State, StateSource: sess.StateSource, StateChangedAt: sess.StateChangedAt,
			Kind: shareKind(sess.Command), Exited: sess.Exited, ExitStatus: sess.ExitStatus, Pinned: sess.Pinned,
			Live: live[sess.ID], Restored: sess.RestoredAt > 0,
		}
		if paths {
			row.CWD, row.Command = sess.CWD, sess.Command
		}
		v.Sessions = append(v.Sessions, row)
	}
	for _, p := range st.Projects {
		row := pluginViewProject{ID: s.pluginHandle(ctx, c.plugin.ID, p.ID), Name: p.Name, Pinned: p.Pinned,
			Sessions: counts[p.ID][0], Waiting: counts[p.ID][1]}
		if paths {
			row.Path = p.Path
		}
		v.Projects = append(v.Projects, row)
	}
	return v, nil
}

func (s *Server) handlePluginView(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	v, err := s.buildPluginView(r.Context(), c)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handlePluginMe is what every credential may know about itself: the plugin,
// the capabilities granted, its settings. The view without the panel.
func (s *Server) handlePluginMe(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	settings, err := s.pluginSettingsBody(r.Context(), c.plugin.ID, c.manifest)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"v": 1, "at": time.Now().Unix(), "plugin": s.pluginIdentityFor(c), "caps": s.pluginCapsList(c),
		"settings": settings.Values,
	})
}

func (s *Server) handlePluginSettingsValues(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	settings, err := s.pluginSettingsBody(r.Context(), c.plugin.ID, c.manifest)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": settings.Values})
}

// handlePluginEvents is the push: a server-sent stream that carries the
// view whenever the panel's state or a project's notes changed, coalesced,
// with a comment every 25 seconds so a proxy keeps the connection.
func (s *Server) handlePluginEvents(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "streaming is not supported here")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	send := func() bool {
		// The credential is resolved again on every send: a grant signed out
		// or a box unticked ends the stream or narrows the view at the next
		// change rather than at the next page load.
		cred, ok, err := s.resolvePluginCred(r.Context(), chi.URLParam(r, "cred"))
		if err != nil || !ok {
			_, _ = fmt.Fprint(w, "event: revoked\ndata: {}\n\n")
			flusher.Flush()
			return false
		}
		var payload []byte
		if cred.has(plugins.CapReadPanel) || cred.has(plugins.CapReadPaths) {
			v, err := s.buildPluginView(r.Context(), cred)
			if err != nil {
				return true
			}
			payload, _ = json.Marshal(v)
		} else {
			payload, _ = json.Marshal(map[string]any{"v": 1, "at": time.Now().Unix(),
				"plugin": s.pluginIdentityFor(cred), "caps": s.pluginCapsList(cred)})
		}
		_, _ = fmt.Fprintf(w, "event: view\ndata: %s\n\n", payload)
		flusher.Flush()
		return true
	}
	_ = c
	if !send() {
		return
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		wake := s.prt.bus.wait()
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keep\n\n")
			flusher.Flush()
		case <-wake:
			time.Sleep(pluginViewCoalesce)
			if !send() {
				return
			}
		}
	}
}

// ─── data ─────────────────────────────────────────────────────────────────

func (s *Server) pluginDataBody(ctx context.Context, c pluginCred) (map[string]any, error) {
	rows, err := s.DB.PluginData(ctx, c.plugin.ID, c.ns)
	if err != nil {
		return nil, err
	}
	m := pages.Manifest{Data: c.manifest.Data}
	values, _ := resolvePageData(m, rows, true)
	return values, nil
}

func (s *Server) handlePluginData(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	values, err := s.pluginDataBody(r.Context(), c)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}

func (s *Server) handlePluginDataOp(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, _ := pluginCredFrom(r)
		op := pageDataOp{Kind: kind, Key: chi.URLParam(r, "key")}
		if kind != "reset" {
			var req struct {
				Value any            `json:"value"`
				By    *float64       `json:"by"`
				Item  map[string]any `json:"item"`
			}
			if !decode(w, r, &req) {
				return
			}
			op.Value, op.Item = req.Value, req.Item
			op.By = 1
			if req.By != nil {
				op.By = *req.By
			}
			if kind == "append" && req.Item == nil {
				writeErr(w, http.StatusBadRequest, `append takes {"item": {...}}`)
				return
			}
		}
		ctx := r.Context()
		rows, err := s.DB.PluginData(ctx, c.plugin.ID, c.ns)
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		m := pages.Manifest{Data: c.manifest.Data}
		changed, batch, err := applyDataOps(m, rows, []pageDataOp{op}, false, true)
		var de dataError
		if errors.As(err, &de) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if len(batch) > 0 {
			err = s.DB.WritePluginData(ctx, c.plugin.ID, c.ns, batch, "plugin:"+c.plugin.ID, pages.MaxDataBytes, pages.MaxDataKeys)
			switch {
			case errors.Is(err, store.ErrPageDataTooLarge):
				writeErr(w, http.StatusBadRequest, fmt.Sprintf("this plugin's data would be larger than %d KiB", pages.MaxDataBytes>>10))
				return
			case errors.Is(err, store.ErrPageDataTooManyKeys):
				writeErr(w, http.StatusBadRequest, fmt.Sprintf("this plugin's data would have more than %d keys", pages.MaxDataKeys))
				return
			case err != nil:
				s.writeStoreErr(w, err)
				return
			}
		}
		s.bumpPluginWatchers()
		if kind == "reset" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"value": changed[op.Key]})
	}
}

// ─── what the capabilities open ───────────────────────────────────────────

func (s *Server) pluginProjectArg(w http.ResponseWriter, r *http.Request) (pluginCred, store.Project, bool) {
	c, _ := pluginCredFrom(r)
	p, ok, err := s.pluginProjectByHandle(r.Context(), c.plugin.ID, chi.URLParam(r, "h"))
	if err != nil {
		s.writeStoreErr(w, err)
		return c, store.Project{}, false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such project")
		return c, store.Project{}, false
	}
	return c, p, true
}

func (s *Server) pluginSessionArg(w http.ResponseWriter, r *http.Request) (pluginCred, store.Session, bool) {
	c, _ := pluginCredFrom(r)
	sess, ok, err := s.pluginSessionByHandle(r.Context(), c.plugin.ID, chi.URLParam(r, "h"))
	if err != nil {
		s.writeStoreErr(w, err)
		return c, store.Session{}, false
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return c, store.Session{}, false
	}
	return c, sess, true
}

// pluginNote restates a note with the project's handle for its id.
type pluginNote struct {
	ProjectID string `json:"projectId"`
	Content   string `json:"content"`
	Rev       int64  `json:"rev"`
	UpdatedAt int64  `json:"updatedAt"`
}

type pluginTodo struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Text      string `json:"text"`
	Done      bool   `json:"done"`
	CreatedAt int64  `json:"createdAt"`
}

func (s *Server) pluginTodoRow(ctx context.Context, pluginID string, t store.Todo) pluginTodo {
	return pluginTodo{ID: s.pluginHandle(ctx, pluginID, t.ID), ProjectID: s.pluginHandle(ctx, pluginID, t.ProjectID),
		Text: t.Text, Done: t.Done, CreatedAt: t.CreatedAt}
}

func (s *Server) handlePluginGetNote(w http.ResponseWriter, r *http.Request) {
	c, p, ok := s.pluginProjectArg(w, r)
	if !ok {
		return
	}
	note, err := s.DB.GetNote(r.Context(), p.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pluginNote{ProjectID: s.pluginHandle(r.Context(), c.plugin.ID, p.ID),
		Content: note.Content, Rev: note.Rev, UpdatedAt: note.UpdatedAt})
}

func (s *Server) handlePluginPutNote(w http.ResponseWriter, r *http.Request) {
	c, p, ok := s.pluginProjectArg(w, r)
	if !ok {
		return
	}
	var req putNoteRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Content) > maxNoteBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "note is too long")
		return
	}
	ctx := r.Context()
	var note store.Note
	var err error
	if req.BaseRev != nil {
		note, err = s.DB.SetNoteIfUnchanged(ctx, p.ID, req.Content, *req.BaseRev)
		if errors.Is(err, store.ErrNoteStale) {
			writeErr(w, http.StatusConflict, "the note changed elsewhere")
			return
		}
	} else {
		note, err = s.DB.SetNote(ctx, p.ID, req.Content)
	}
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyPanel(p.ID, "note")
	writeJSON(w, http.StatusOK, pluginNote{ProjectID: s.pluginHandle(ctx, c.plugin.ID, p.ID),
		Content: note.Content, Rev: note.Rev, UpdatedAt: note.UpdatedAt})
}

func (s *Server) handlePluginListTodos(w http.ResponseWriter, r *http.Request) {
	c, p, ok := s.pluginProjectArg(w, r)
	if !ok {
		return
	}
	todos, err := s.DB.ListTodos(r.Context(), p.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out := make([]pluginTodo, 0, len(todos))
	for _, t := range todos {
		out = append(out, s.pluginTodoRow(r.Context(), c.plugin.ID, t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePluginCreateTodo(w http.ResponseWriter, r *http.Request) {
	c, p, ok := s.pluginProjectArg(w, r)
	if !ok {
		return
	}
	var req createTodoRequest
	if !decode(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > maxTodoBytes {
		writeErr(w, http.StatusBadRequest, "text is one line of at most 2000 bytes")
		return
	}
	todo, err := s.DB.CreateTodo(r.Context(), id.New(), p.ID, text)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyPanel(p.ID, "todos")
	writeJSON(w, http.StatusCreated, s.pluginTodoRow(r.Context(), c.plugin.ID, todo))
}

func (s *Server) handlePluginPatchTodo(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	ctx := r.Context()
	todo, ok, err := s.pluginTodoByHandle(ctx, c.plugin.ID, chi.URLParam(r, "h"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such item")
		return
	}
	var req patchTodoRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Text != nil {
		text := strings.TrimSpace(*req.Text)
		if text == "" || len(text) > maxTodoBytes {
			writeErr(w, http.StatusBadRequest, "text is one line of at most 2000 bytes")
			return
		}
		if err := s.DB.SetTodoText(ctx, todo.ID, text); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.Done != nil {
		if err := s.DB.SetTodoDone(ctx, todo.ID, *req.Done); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	todo, err = s.DB.GetTodo(ctx, todo.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyPanel(todo.ProjectID, "todos")
	writeJSON(w, http.StatusOK, s.pluginTodoRow(ctx, c.plugin.ID, todo))
}

func (s *Server) handlePluginDeleteTodo(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	ctx := r.Context()
	todo, ok, err := s.pluginTodoByHandle(ctx, c.plugin.ID, chi.URLParam(r, "h"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such item")
		return
	}
	if err := s.DB.DeleteTodo(ctx, todo.ID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyPanel(todo.ProjectID, "todos")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePluginSessionState(w http.ResponseWriter, r *http.Request) {
	c, sess, ok := s.pluginSessionArg(w, r)
	if !ok {
		return
	}
	var req struct {
		State string `json:"state"`
	}
	if !decode(w, r, &req) {
		return
	}
	st := session.State(req.State)
	if !st.Valid() {
		writeErr(w, http.StatusBadRequest, "unknown state "+req.State)
		return
	}
	ctx := r.Context()
	if s.Detector != nil {
		s.Detector.SetManual(sess.ID, st, time.Now())
	}
	if err := s.setSessionStateByID(ctx, sess.ID, st, session.SourceManual); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyState()
	v, err := s.buildPluginView(ctx, c)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	h := s.pluginHandle(ctx, c.plugin.ID, sess.ID)
	for _, row := range v.Sessions {
		if row.ID == h {
			writeJSON(w, http.StatusOK, row)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": h, "state": st})
}

func (s *Server) handlePluginScreen(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := s.pluginSessionArg(w, r)
	if !ok {
		return
	}
	text, err := s.Tmux.Screen(r.Context(), sess.TmuxName, false)
	if err != nil {
		writeErr(w, http.StatusConflict, "the session's pane cannot be read right now")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

// pluginResources restates the machine reading: what the Resources tab
// shows, without the disk path.
type pluginResources struct {
	At           int64    `json:"at"`
	CPUPercent   *float64 `json:"cpuPercent"`
	Cores        int      `json:"cores"`
	Load1        float64  `json:"load1"`
	MemTotal     uint64   `json:"memTotal"`
	MemAvailable uint64   `json:"memAvailable"`
	SwapTotal    uint64   `json:"swapTotal"`
	SwapFree     uint64   `json:"swapFree"`
	DiskTotal    uint64   `json:"diskTotal"`
	DiskFree     uint64   `json:"diskFree"`
}

func (s *Server) handlePluginResources(w http.ResponseWriter, r *http.Request) {
	var sample sysmon.Sample
	if s.Sampler != nil {
		sample = s.Sampler.Sample()
	}
	writeJSON(w, http.StatusOK, pluginResources{At: sample.At, CPUPercent: sample.CPUPercent, Cores: sample.Cores,
		Load1: sample.Load1, MemTotal: sample.MemTotal, MemAvailable: sample.MemAvailable, SwapTotal: sample.SwapTotal,
		SwapFree: sample.SwapFree, DiskTotal: sample.DiskTotal, DiskFree: sample.DiskFree})
}

// pluginUsage is each session's process tree, by the plugin's handles.
type pluginUsage struct {
	Readable bool                    `json:"readable"`
	Cores    int                     `json:"cores"`
	Sessions map[string]sysmon.Usage `json:"sessions"`
}

func (s *Server) handlePluginUsage(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	out := pluginUsage{Readable: sysmon.ProcReadable(), Cores: runtime.NumCPU(), Sessions: map[string]sysmon.Usage{}}
	if !out.Readable {
		writeJSON(w, http.StatusOK, out)
		return
	}
	usage, err := s.sessionUsage(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	for sid, u := range usage {
		out.Sessions[s.pluginHandle(r.Context(), c.plugin.ID, sid)] = u
	}
	writeJSON(w, http.StatusOK, out)
}

// pluginGit restates the repository summary: counts and the branch, never a
// path or a subject. What a wall gets, through a plugin.
type pluginGit struct {
	Repo      bool   `json:"repo"`
	Branch    string `json:"branch"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	Staged    int    `json:"staged"`
	Unstaged  int    `json:"unstaged"`
	Untracked int    `json:"untracked"`
	Conflicts int    `json:"conflicts"`
	Commits   int    `json:"commits"`
}

func (s *Server) handlePluginGit(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.pluginProjectArg(w, r)
	if !ok {
		return
	}
	out := pluginGit{}
	snap, err := s.Git.Read(r.Context(), p.Path, recentCommits)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Repo = true
	out.Branch = snap.Status.Branch
	out.Ahead, out.Behind = snap.Status.Ahead, snap.Status.Behind
	out.Staged, out.Unstaged, out.Untracked, out.Conflicts =
		snap.Status.Staged, snap.Status.Unstaged, snap.Status.Untracked, snap.Status.Conflicted
	out.Commits = len(snap.Commits)
	writeJSON(w, http.StatusOK, out)
}

// ─── the owner's side: grants, handles, dev mode ──────────────────────────

func (s *Server) registerPluginDevRoutes(r chi.Router) {
	r.Post("/settings/plugins/{pluginID}/grant", s.handleMintPluginGrant)
	r.Get("/settings/plugins/{pluginID}/handles", s.handlePluginHandles)
	r.Put("/settings/plugins/{pluginID}/dev", s.handlePutPluginDev)
	r.Get("/settings/plugins/{pluginID}/draft/fingerprint", s.handlePluginFingerprint)
}

// handleMintPluginGrant mints a grant for the owner's session, for the SPA
// to mount a frame with. The session cookie only: a grant is bound to the
// session that minted it so that signing out ends it, and a bearer token
// has no session to end.
func (s *Server) handleMintPluginGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := auth.TokenFromRequest(r)
	user, ok, err := s.currentUserBySessionOnly(r)
	if err != nil {
		s.noteStale(err)
		writeErr(w, http.StatusServiceUnavailable, "the panel cannot reach its own database")
		return
	}
	if !ok {
		writeErr(w, http.StatusUnauthorized, "a plugin frame needs the panel's own session")
		return
	}
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if !p.Enabled && !p.Dev {
		writeErr(w, http.StatusConflict, "this plugin is not enabled")
		return
	}
	if _, _, err := s.pluginRunningManifest(ctx, p); err != nil {
		writeErr(w, http.StatusConflict, "this plugin has nothing to run: "+err.Error())
		return
	}
	grant, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.DB.SweepPluginGrants(ctx)
	if err := s.DB.CreatePluginGrant(ctx, auth.HashToken(grant), p.ID, user.ID, auth.HashToken(token), pluginGrantTTL); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"grant":     grant,
		"base":      "/plugin/" + grant + "/",
		"api":       "/api/plugin/" + grant + "/v1/",
		"expiresAt": time.Now().Add(pluginGrantTTL).Unix(),
		"dev":       p.Dev,
	})
}

// handlePluginHandles is how the SPA names a session or project to a frame:
// it asks for the plugin's handle rather than posting the real id.
func (s *Server) handlePluginHandles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out := map[string]string{}
	if sid := r.URL.Query().Get("session"); sid != "" {
		out["session"] = s.pluginHandle(ctx, p.ID, sid)
	}
	if pid := r.URL.Query().Get("project"); pid != "" {
		out["project"] = s.pluginHandle(ctx, p.ID, pid)
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePutPluginDev switches dev mode: the draft directory's files and
// manifest are what runs, under the grants already given to this id.
func (s *Server) handlePutPluginDev(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	var req struct {
		Dev       bool   `json:"dev"`
		SourceDir string `json:"sourceDir"`
	}
	if !decode(w, r, &req) {
		return
	}
	dir := p.SourceDir
	if req.SourceDir != "" {
		dir = req.SourceDir
	}
	if req.Dev {
		if dir == "" {
			writeErr(w, http.StatusBadRequest, "dev mode needs a draft directory")
			return
		}
		raw, err := plugins.ReadManifestFile(dir)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		m, err := plugins.ParseManifest(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if m.ID != p.ID {
			writeErr(w, http.StatusBadRequest, "the directory's plugin.json is for "+m.ID+", not "+p.ID)
			return
		}
	}
	if err := s.DB.UpdatePluginSource(ctx, p.ID, dir, req.Dev); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.dev", u.Username, s.clientIP(r), fmt.Sprintf("%s dev=%v %s", p.ID, req.Dev, dir))
	s.pluginsChanged()
	p, _ = s.DB.PluginByID(ctx, p.ID)
	d, err := s.pluginDetailFor(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handlePluginFingerprint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if p.SourceDir == "" {
		writeErr(w, http.StatusConflict, "this plugin has no draft directory")
		return
	}
	fp, err := plugins.Fingerprint(p.SourceDir)
	if err != nil {
		writeErr(w, http.StatusConflict, "the draft directory cannot be read: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fp})
}

// ─── sessions:control, sessions:create, sessions:input ───────────────────
//
// Through the panel's own handlers, by an internal request with the real id
// substituted for the handle: what a plugin does to a session is exactly
// what the sidebar does, audited the same way, refused the same way, and a
// change to the handler is a change here. The response is copied back as
// it came, with the real id replaced by the handle wherever it appears.

func (s *Server) internally(ctx context.Context, c pluginCred, handler http.HandlerFunc, method, path string,
	params map[string]string, body []byte, w http.ResponseWriter, replace map[string]string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	// The handlers that audit read the user from the context; a plugin's
	// writes are audited as the plugin.
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, store.User{ID: c.grant.UserID, Username: c.user}))
	handler(rec, req)
	out := rec.Body.Bytes()
	for real, handle := range replace {
		out = bytes.ReplaceAll(out, []byte(real), []byte(handle))
	}
	for k, v := range rec.Header() {
		if k == "Content-Type" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(out)
}

func (s *Server) handlePluginSessionRestart(w http.ResponseWriter, r *http.Request) {
	c, sess, ok := s.pluginSessionArg(w, r)
	if !ok {
		return
	}
	s.internally(r.Context(), c, s.handleRestartSession, http.MethodPost, "/api/sessions/"+sess.ID+"/restart",
		map[string]string{"id": sess.ID}, nil, w, map[string]string{sess.ID: chi.URLParam(r, "h")})
}

func (s *Server) handlePluginSessionDelete(w http.ResponseWriter, r *http.Request) {
	c, sess, ok := s.pluginSessionArg(w, r)
	if !ok {
		return
	}
	s.internally(r.Context(), c, s.handleDeleteSession, http.MethodDelete, "/api/sessions/"+sess.ID,
		map[string]string{"id": sess.ID}, nil, w, map[string]string{sess.ID: chi.URLParam(r, "h")})
}

// handlePluginSessionCreate starts a program in a project the plugin names
// by handle. The argv is the plugin's; the directory is the project's.
func (s *Server) handlePluginSessionCreate(w http.ResponseWriter, r *http.Request) {
	c, _ := pluginCredFrom(r)
	var req struct {
		Project string   `json:"project"`
		Title   string   `json:"title"`
		Command []string `json:"command"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, ok, err := s.pluginProjectByHandle(r.Context(), c.plugin.ID, req.Project)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such project")
		return
	}
	body, _ := json.Marshal(createSessionRequest{ProjectID: p.ID, Title: req.Title, Command: req.Command})
	rec := httptest.NewRecorder()
	s.internally(r.Context(), c, s.handleCreateSession, http.MethodPost, "/api/sessions", nil, body, rec, nil)
	if rec.Code != http.StatusCreated {
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	var made store.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	v, err := s.buildPluginView(r.Context(), c)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	h := s.pluginHandle(r.Context(), c.plugin.ID, made.ID)
	for _, row := range v.Sessions {
		if row.ID == h {
			writeJSON(w, http.StatusCreated, row)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": h, "projectId": req.Project})
}

// handlePluginSessionInput types into a pane: a paste, and Enter when asked.
// The same two tmux calls the chat bridge makes.
func (s *Server) handlePluginSessionInput(w http.ResponseWriter, r *http.Request) {
	c, sess, ok := s.pluginSessionArg(w, r)
	if !ok {
		return
	}
	var req struct {
		Text   string `json:"text"`
		Submit bool   `json:"submit"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Text) > 64<<10 {
		writeErr(w, http.StatusRequestEntityTooLarge, "input is at most 64 KiB")
		return
	}
	ctx := r.Context()
	if req.Text != "" {
		if err := s.Tmux.Paste(ctx, sess.TmuxName, req.Text); err != nil {
			writeErr(w, http.StatusConflict, "the pane did not take the input: "+err.Error())
			return
		}
	}
	if req.Submit {
		if err := s.Tmux.Keys(ctx, sess.TmuxName, "Enter"); err != nil {
			writeErr(w, http.StatusConflict, "the pane did not take Enter: "+err.Error())
			return
		}
	}
	s.audit(ctx, "plugin.input", c.user, s.clientIP(r), fmt.Sprintf("%s typed %d bytes into %s", c.plugin.ID, len(req.Text), chi.URLParam(r, "h")))
	w.WriteHeader(http.StatusNoContent)
}
