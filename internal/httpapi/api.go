// Package httpapi exposes the REST surface and wires the WebSocket handler.
package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/version"
	"github.com/jiangmuran/vibepanel/internal/webui"
	"github.com/jiangmuran/vibepanel/internal/ws"
)

// Server holds everything the HTTP layer needs.
type Server struct {
	Cfg      config.Config
	DB       *store.DB
	Tmux     *tmux.Client
	Manager  *session.Manager
	Hub      *ws.Hub
	Detector *session.Detector
	Sampler  *sysmon.Sampler
	Auth     *Auth
	// Challenges holds in-flight WebAuthn ceremonies. The challenge stays on
	// the server; the browser only carries an opaque id for it.
	Challenges *challengeStore
	Log        *slog.Logger

	// hookToken authenticates state reports from agent hooks. Cached after the
	// first read so the hot path does not hit the database.
	tokenOnce  sync.Once
	hookToken  string
	tokenError error

	// lastSnapshot is the most recent state payload that was broadcast. The
	// poller compares against it so that a tick where nothing changed sends
	// nothing — otherwise pushing is just polling with extra steps.
	snapMu       sync.Mutex
	lastSnapshot []byte

	// hookInstalled caches whether the reporter is wired into the agent's
	// configuration.
	//
	// The state snapshot needs the answer, and snapshots are built on every
	// broadcast — a session changing state, a note being saved. Asking properly
	// means reading and parsing a file in the user's home directory, so asking
	// every time meant doing that several times a second with a couple of dozen
	// busy sessions. The TTL covers someone editing that file by hand;
	// installing or removing through the panel invalidates it immediately.
	hookMu        sync.Mutex
	hookInstalled bool
	hookCheckedAt time.Time

	// outputSeen debounces last_output_at writes. The pump can call into here
	// hundreds of times a second; the column is read by humans.
	outMu      sync.Mutex
	outputSeen map[string]time.Time
	// CertExpiry, if set, reports when the certificate now being served stops
	// being valid.
	//
	// Surfaced on the settings page because the failure it guards is silent by
	// nature: a certificate nobody renewed does not announce itself, it simply
	// stops working one morning. The panel logs a warning as it approaches, but
	// a log line on a machine nobody reads is not where this should first be
	// noticed.
	CertExpiry func() time.Time
}

// Routes builds the router.
//
// Order matters: /api and /ws are registered before the catch-all that serves
// the single-page app, so an unknown API path returns a JSON 404 instead of
// quietly handing the caller an HTML document.
// securityHeaders sets the four that cost nothing and matter for a terminal
// that is deliberately on the public internet.
//
// Referrer-Policy is the one with teeth. The terminal makes every URL an agent
// prints into a clickable link, and clicking one used to send
// `Referer: https://<the panel>/` to whatever host that URL named — an address
// chosen by whatever the agent read, echoed, or was told to print. Measured
// against a listener standing in for "somewhere else on the internet": it
// received `referer: http://127.0.0.1:38475/`, the panel's exact origin. For a
// panel whose exposure story is a non-standard port and a password, handing
// the address to arbitrary third parties on a link click is a real weakening.
//
// Not the opener: the web-links addon opens a blank window, nulls `opener` and
// then navigates, so reverse tabnabbing was already handled. Worth stating
// because it looks like the same bug and is not.
//
// frame-ancestors rather than a full CSP. A real content policy would have to
// account for the inline styles xterm and Tailwind generate, and a CSP that
// breaks the terminal would be turned off within a day. This one directive
// restricts nothing the panel does.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Routes() http.Handler {
	if s.Challenges == nil {
		s.Challenges = newChallengeStore()
	}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	// Deliberately NOT middleware.RealIP.
	//
	// It overwrites r.RemoteAddr from X-Forwarded-For or X-Real-IP with no
	// trust model at all, and everything downstream that cares about who is
	// calling — the CIDR allowlist, the login throttle, the audit log — then
	// reads a value the caller chose. Measured before removing it: with
	// --allow-from set to a network this machine is not on, a plain request
	// was refused and the same request with "X-Forwarded-For: <an allowed
	// address>" went through; and twelve wrong passwords with a different
	// header value each time were never throttled, while twelve from one
	// address were throttled after the first.
	//
	// auth.ClientIP does this properly: it believes the header only from a
	// proxy the operator listed in --trusted-proxies. Use that, always.

	r.Route("/api", func(r chi.Router) {
		// Open: the health probe says nothing sensitive, the auth endpoints are
		// how you get in, and hooks carry their own token because they run
		// outside the browser as children of an agent.
		r.Get("/health", s.handleHealth)
		s.registerAuthRoutes(r)
		s.registerPasskeyRoutes(r)
		r.Post("/hook/state", s.handleHookState)

		// Everything else needs a session. This panel hands out a writable
		// terminal; there is no such thing as a harmless unauthenticated
		// endpoint here.
		r.Group(func(r chi.Router) {
			r.Use(s.RequireAuth)

			r.Get("/state", s.handleState)

			r.Post("/projects", s.handleCreateProject)
			r.Post("/projects/reorder", s.handleReorderProjects)
			r.Patch("/projects/{id}", s.handlePatchProject)
			r.Delete("/projects/{id}", s.handleDeleteProject)

			r.Post("/sessions", s.handleCreateSession)
			r.Patch("/sessions/{id}", s.handlePatchSession)
			r.Delete("/sessions/{id}", s.handleDeleteSession)
			r.Post("/sessions/{id}/restart", s.handleRestartSession)

			s.registerPanelRoutes(r)
			s.registerSettingsRoutes(r)
		})

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusNotFound, "no such endpoint")
		})
	})

	// The WebSocket is the terminal itself; it needs the same session as
	// everything else.
	r.With(s.RequireAuth).Handle("/ws", &ws.Handler{
		Manager:  s.Manager,
		Resolve:  resolver{db: s.DB},
		Hub:      s.Hub,
		Log:      s.Log,
		Snapshot: s.snapshot,
		// A socket is authorised once, at the handshake, and then lives for
		// hours. Without this, signing out, a session expiring, an
		// administrator revoking one, or changing the password left the
		// terminals those browsers already had open still streaming — and
		// still accepting keystrokes.
		StillAuthorized: s.stillAuthorized,
	})

	r.Handle("/*", webui.Handler(s.Cfg.StaticDir))
	return r
}

// ─── resolver ─────────────────────────────────────────────────────────────

// resolver lets the ws package look sessions up without importing the store.
type resolver struct{ db *store.DB }

func (r resolver) Resolve(ctx context.Context, sessionID string) (string, int, int, error) {
	s, err := r.db.GetSession(ctx, sessionID)
	if err != nil {
		return "", 0, 0, err
	}
	return s.TmuxName, s.Cols, s.Rows, nil
}

func (r resolver) RecordSize(ctx context.Context, sessionID string, cols, rows int) error {
	return r.db.SetSessionSize(ctx, sessionID, cols, rows)
}

// ─── handlers ─────────────────────────────────────────────────────────────

// snapshot builds the full state payload. Returns nil if it cannot be read,
// in which case nothing is broadcast rather than something wrong.
// RestoreState seeds the detector from the database, once, at startup.
//
// Must run before Reconcile, which re-derives every session from live facts
// and writes the answer back. Without this the answer for anything that is not
// a shell is "working", so a restart turned every session that was waiting for
// a human into one that looked busy — permanently, because nothing was ever
// going to ring a second time.
func (s *Server) RestoreState(ctx context.Context) error {
	if s.Detector == nil {
		return nil
	}
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("restore state: %w", err)
	}
	restored := 0
	for _, row := range rows {
		if row.StateChangedAt == 0 {
			continue
		}
		before := row.State
		s.Detector.Restore(row.ID, row.State, row.StateSource, time.Unix(row.StateChangedAt, 0))
		if before == session.StateWaiting || row.StateSource != session.SourceHeuristic {
			restored++
		}
	}
	if restored > 0 {
		s.Log.Info("restored session states", "sessions", restored)
	}
	return nil
}

func (s *Server) snapshot(ctx context.Context) []byte {
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		s.Log.Warn("snapshot projects", "err", err)
		return nil
	}
	sessions, err := s.DB.ListSessions(ctx)
	if err != nil {
		s.Log.Warn("snapshot sessions", "err", err)
		return nil
	}
	manual, err := s.DB.ProjectOrderIsManual(ctx)
	if err != nil {
		s.Log.Warn("snapshot order mode", "err", err)
		return nil
	}
	hasOrder, err := s.DB.HasProjectOrder(ctx)
	if err != nil {
		s.Log.Warn("snapshot stored order", "err", err)
		return nil
	}
	payload, err := json.Marshal(struct {
		Type string `json:"t"`
		stateResponse
	}{
		Type: ws.MsgState,
		stateResponse: stateResponse{
			Projects:        emptyIfNil(projects),
			Sessions:        emptyIfNil(sessions),
			Live:            emptyIfNil(s.Manager.LiveIDs()),
			ProjectOrder:    orderMode(manual),
			HasProjectOrder: hasOrder,
			StateGuessed:    s.stateIsGuessed(sessions),
			HooksInstalled:  s.hooksAreInstalled(),
		},
	})
	if err != nil {
		s.Log.Warn("snapshot marshal", "err", err)
		return nil
	}
	return payload
}

// notifyState pushes the current state to every viewer, coalesced.
// notifyPanel tells every viewer that a project's note or todo list changed.
//
// Deliberately not part of the state snapshot: notes and todo lists are per
// project and can be long, and pushing them to every viewer on every keystroke
// would make the socket carry a document nobody asked for. This says what
// changed and lets the panel that cares refetch — which is also what makes the
// panels work at all in a second window, where before they simply never
// updated and the second save overwrote the first.
func (s *Server) notifyPanel(projectID, kind string) {
	if s.Hub == nil {
		return
	}
	// "t" is the discriminator every other control message uses; "type" here
	// would be silently ignored by the client's switch.
	payload, err := json.Marshal(map[string]string{
		"t":         ws.MsgPanel,
		"projectId": projectID,
		"kind":      kind,
	})
	if err != nil {
		return
	}
	s.Hub.Broadcast(payload)
}

func (s *Server) notifyState() {
	if s.Hub == nil {
		return
	}
	s.Hub.Notify(func() []byte {
		// A short independent timeout: this runs on the hub's timer, detached
		// from whatever request caused it, and must not hang on a locked
		// database while holding up every other viewer's update.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		payload := s.snapshot(ctx)
		if payload == nil {
			return nil
		}
		s.snapMu.Lock()
		s.lastSnapshot = payload
		s.snapMu.Unlock()
		return payload
	})
}

// HookToken returns the shared secret that authenticates state reports,
// generating it on first use.
func (s *Server) HookToken(ctx context.Context) (string, error) {
	// The memo stays here — every hook report checks the token, so this is on
	// a hot path — but the value itself is the store's, because the admin CLI
	// needs the same one when it creates a session.
	s.tokenOnce.Do(func() {
		s.hookToken, s.tokenError = s.DB.HookToken(ctx)
	})
	return s.hookToken, s.tokenError
}

type hookStateRequest struct {
	SessionID string `json:"sessionId"`
	State     string `json:"state"`
}

// handleHookState accepts a state report from an agent hook.
//
// This is the only precise source of session state: the agent saying what it
// is doing rather than the panel inferring it from bytes. It is optional by
// design — a session whose agent has no hook installed falls back to the
// output heuristic, which is why the hook script can be installed globally
// without affecting anything outside the panel.
func (s *Server) handleHookState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token, err := s.HookToken(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hook token unavailable")
		return
	}
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Constant time: this endpoint is unauthenticated apart from the token, so
	// a timing oracle on it is a timing oracle on the whole thing.
	if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
		writeErr(w, http.StatusUnauthorized, "bad token")
		return
	}

	var req hookStateRequest
	if !decode(w, r, &req) {
		return
	}
	st := session.State(req.State)
	if !st.Valid() {
		writeErr(w, http.StatusBadRequest, "unknown state "+req.State)
		return
	}
	if _, err := s.DB.GetSession(ctx, req.SessionID); err != nil {
		// An unknown session id is the normal case for an agent started
		// outside the panel that happens to have the hook installed. Say so
		// plainly rather than treating it as an error to be alarmed by.
		writeStoreErr(w, err)
		return
	}
	s.Detector.Report(req.SessionID, st, time.Now())
	if err := s.DB.SetSessionState(ctx, req.SessionID, st, session.SourceHook); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

// HandleSignals records what the PTY pump observed. Wired to
// session.Manager.OnSignals; runs on the pump goroutine, so it must be quick.
func (s *Server) HandleSignals(sig session.Signals) {
	now := time.Now()
	if s.Detector != nil {
		s.Detector.Observe(sig.SessionID, sig, now)
	}
	if !sig.Visible {
		return
	}

	s.outMu.Lock()
	if s.outputSeen == nil {
		s.outputSeen = map[string]time.Time{}
	}
	last := s.outputSeen[sig.SessionID]
	if now.Sub(last) < time.Second {
		s.outMu.Unlock()
		return
	}
	s.outputSeen[sig.SessionID] = now
	s.outMu.Unlock()

	// Off the pump goroutine: a database write must never sit between the PTY
	// and the viewers watching it.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.DB.TouchSessionOutput(ctx, sig.SessionID, now.Unix()); err != nil {
			s.Log.Debug("touch output", "session", sig.SessionID, "err", err)
		}
	}()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	tv, _ := s.Tmux.Version(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"version":     version.Version,
		"commit":      version.Commit,
		"tmuxVersion": tv,
		"live":        len(s.Manager.LiveIDs()),
		"passkeys":    s.Cfg.PasskeysUsable(),
	})
}

// stateResponse is the whole picture in one request.
//
// The UI needs projects and sessions together on every load, and two requests
// would let it render a session list against a project list from a different
// instant.
type stateResponse struct {
	Projects []store.Project `json:"projects"`
	Sessions []store.Session `json:"sessions"`
	Live     []string        `json:"live"`

	// ProjectOrder is "auto" (most recently active first) or "manual". The UI
	// offers "sort by activity" only when that would change something, rather
	// than showing a control that does nothing.
	ProjectOrder string `json:"projectOrder"`

	// HooksInstalled says which way out to offer when the state is guessed:
	// install the reporter, or reload the sessions that started before it.
	HooksInstalled bool `json:"hooksInstalled"`

	// HasProjectOrder is true when an arrangement is stored, whichever
	// ordering is in use. Without it the sidebar has no way to offer the way
	// back: switching to automatic used to erase the arrangement, so there was
	// never anything to return to and the control that did it removed itself.
	HasProjectOrder bool `json:"hasProjectOrder"`

	// StateGuessed is true when an agent is running and nothing is reporting
	// its state, so "waiting for you" is being inferred rather than known.
	//
	// It exists because the inference does not work for the agent most people
	// run here: Claude Code does not ring the terminal bell when it stops for
	// a decision, and the bell is the only signal the heuristic has. Saying so
	// is better than quietly under-reporting the one state the panel is for.
	StateGuessed bool `json:"stateGuessed"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessions, err := s.DB.ListSessions(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	manual, err := s.DB.ProjectOrderIsManual(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	hasOrder, err := s.DB.HasProjectOrder(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stateResponse{
		Projects:        emptyIfNil(projects),
		Sessions:        emptyIfNil(sessions),
		Live:            emptyIfNil(s.Manager.LiveIDs()),
		ProjectOrder:    orderMode(manual),
		HasProjectOrder: hasOrder,
		StateGuessed:    s.stateIsGuessed(sessions),
		HooksInstalled:  s.hooksAreInstalled(),
	})
}

// agentCommands are the programs whose state the heuristic cannot read well.
//
// Not a general "is this an agent" test — just the ones known to stop and wait
// without ringing the bell, which is the case the notice is about.
var agentCommands = map[string]bool{"claude": true, "codex": true}

// stateIsGuessed reports whether an agent is running with nothing reporting
// its state.
func (s *Server) stateIsGuessed(sessions []store.Session) bool {
	var agent bool
	for _, sess := range sessions {
		if agentCommands[sess.Command] {
			agent = true
			break
		}
	}
	if !agent {
		return false
	}
	for _, sess := range sessions {
		// One hook report anywhere is enough: the script is installed globally
		// or not at all.
		if sess.StateSource == session.SourceHook {
			return false
		}
	}
	// Installing the hooks used to be enough to clear this, and that was the
	// worst possible moment to stop explaining. An agent reads its hooks when
	// it starts, so every session already open keeps guessing after the
	// install — and the notice that said so vanished on the click that was
	// meant to fix it.
	//
	// Guessed now means what it says: an agent is running and nothing has
	// reported. Whether the hooks are installed decides which way out the
	// notice offers, not whether it appears.
	return true
}

// hookCheckTTL bounds how stale the cached answer may be. Short enough that
// editing the agent's configuration by hand is noticed while you are still
// looking at the panel, long enough that a burst of state changes costs one
// read rather than one per change.
const hookCheckTTL = 10 * time.Second

// hooksAreInstalled reports whether the reporter is wired into the agent's
// configuration, from cache when it can.
//
// Errors count as "not installed", which is the same answer the uncached
// version gave: unreadable configuration and absent configuration are
// indistinguishable from here, and both mean the panel is guessing.
func (s *Server) hooksAreInstalled() bool {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	if !s.hookCheckedAt.IsZero() && time.Since(s.hookCheckedAt) < hookCheckTTL {
		return s.hookInstalled
	}
	installed := false
	if script, err := s.scriptPath(); err == nil {
		if st, ierr := hooks.Inspect(script); ierr == nil {
			installed = st.Installed
		}
	}
	s.hookInstalled, s.hookCheckedAt = installed, time.Now()
	return installed
}

// forgetHookStatus drops the cached answer, so a change made through the panel
// shows up in the next snapshot rather than up to a TTL later.
func (s *Server) forgetHookStatus() {
	s.hookMu.Lock()
	s.hookCheckedAt = time.Time{}
	s.hookMu.Unlock()
}

// isDirectory reports whether a path is a directory that exists right now.
//
// "Right now" is the point: a project's directory is checked when the project
// is created and can be gone by the time a session is started in it.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func orderMode(manual bool) string {
	if manual {
		return "manual"
	}
	return "auto"
}

type reorderProjectsRequest struct {
	// Ids is the desired order, top first.
	Ids []string `json:"ids"`
	// Auto discards manual positions and returns to activity ordering.
	Auto bool `json:"auto"`
}

func (s *Server) handleReorderProjects(w http.ResponseWriter, r *http.Request) {
	var req reorderProjectsRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Auto {
		// Switch the ordering, keep the arrangement. `auto` used to erase every
		// position, which made a clock icon with no confirmation the most
		// destructive control in the sidebar — and it removed itself on the way
		// out, since it only renders in manual mode.
		if err := s.DB.SetProjectOrderManual(ctx, false); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		// No ids: go back to the arrangement already stored. That is the way
		// out of automatic ordering, and before the positions survived the
		// switch there was no such thing to ask for.
		if len(req.Ids) == 0 {
			if err := s.DB.SetProjectOrderManual(ctx, true); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			s.notifyState()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := s.DB.ReorderProjects(ctx, req.Ids); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

type createProjectRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := filepath.Abs(expandHome(req.Path))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad path: "+err.Error())
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot open directory: "+err.Error())
		return
	}
	if !info.IsDir() {
		writeErr(w, http.StatusBadRequest, abs+" is not a directory")
		return
	}
	if req.Name == "" {
		req.Name = filepath.Base(abs)
	}
	p, err := s.DB.CreateProject(r.Context(), id.New(), req.Name, abs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyState()
	writeJSON(w, http.StatusCreated, p)
}

type patchProjectRequest struct {
	Name      *string `json:"name"`
	Pinned    *bool   `json:"pinned"`
	SortIndex *int    `json:"sortIndex"`
	// ClearSortIndex returns the project to automatic activity ordering.
	// A separate flag because a null SortIndex is indistinguishable from an
	// absent one after JSON decoding.
	ClearSortIndex bool `json:"clearSortIndex"`
}

func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "id")
	var req patchProjectRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Name != nil {
		if err := s.DB.RenameProject(ctx, pid, *req.Name); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if req.Pinned != nil {
		if err := s.DB.SetProjectPinned(ctx, pid, *req.Pinned); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if req.ClearSortIndex {
		if err := s.DB.SetProjectSortIndex(ctx, pid, nil); err != nil {
			writeStoreErr(w, err)
			return
		}
	} else if req.SortIndex != nil {
		if err := s.DB.SetProjectSortIndex(ctx, pid, req.SortIndex); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	p, err := s.DB.GetProject(ctx, pid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.notifyState()
	writeJSON(w, http.StatusOK, p)
}

// tearDownSession ends one session: its tmux session first, then the panel's
// attachment to it.
//
// That order is the whole point. A tmux client does not exit when its PTY is
// closed, so detaching first leaves close() waiting on the two-second timer
// that kills the client — measured at 2015ms to delete one session, and 10029ms
// for a project with five, because the loops that call this are serial.
//
// Killing the tmux session first takes that wait away rather than hiding it:
// the client's session is gone, so the client exits on its own and the pump
// sees EOF immediately.
//
// Kill failing before anything else has happened is also the better half of
// the bargain. Detaching first and then failing to kill left the panel with no
// attachment to a tmux session that was still running.
func (s *Server) tearDownSession(ctx context.Context, id, tmuxName string) error {
	if err := s.Tmux.Kill(ctx, tmuxName); err != nil {
		return err
	}
	s.Manager.Detach(id)
	// Without this the detector keeps a tracker per session for the life of the
	// process — small, but it is the kind of asymmetry between two paths that
	// doing the same thing eventually turns into a real bug.
	if s.Detector != nil {
		s.Detector.Forget(id)
	}
	return nil
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := chi.URLParam(r, "id")

	// Kill tmux first: the rows cascade away on delete, and a row that vanishes
	// while its tmux session lives on leaves a process nothing can reach.
	sessions, err := s.DB.ListProjectSessions(ctx, pid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	for _, sess := range sessions {
		if err := s.tearDownSession(ctx, sess.ID, sess.TmuxName); err != nil {
			writeErr(w, http.StatusInternalServerError, "kill "+sess.TmuxName+": "+err.Error())
			return
		}
	}
	if err := s.DB.DeleteProject(ctx, pid); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

type createSessionRequest struct {
	ProjectID string   `json:"projectId"`
	Title     string   `json:"title"`
	Command   []string `json:"command"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`

	// ParentSessionID makes this a scratch terminal under a main session,
	// starting in whatever directory that session is currently in.
	ParentSessionID string `json:"parentSessionId"`
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	p, err := s.DB.GetProject(ctx, req.ProjectID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if req.Cols <= 0 {
		req.Cols = 120
	}
	if req.Rows <= 0 {
		req.Rows = 32
	}

	// A scratch terminal starts where its parent currently is, not where the
	// project root is. Opening a shell next to an agent that has cd'd into a
	// worktree, and landing somewhere else, is the kind of small wrongness
	// that makes a panel feel untrustworthy.
	dir := p.Path
	var parent *string
	if req.ParentSessionID != "" {
		parentRec, perr := s.DB.GetSession(ctx, req.ParentSessionID)
		if perr != nil {
			writeStoreErr(w, perr)
			return
		}
		if parentRec.ParentID != nil {
			writeErr(w, http.StatusBadRequest, "a scratch terminal cannot have scratch terminals of its own")
			return
		}
		if parentRec.ProjectID != p.ID {
			writeErr(w, http.StatusBadRequest, "parent session belongs to a different project")
			return
		}
		if parentRec.CWD != "" {
			dir = parentRec.CWD
		}
		parent = &req.ParentSessionID
	}

	// tmux falls back to $HOME when -c names a directory that is not there, and
	// says nothing about it. So a project whose directory has been removed — a
	// git worktree pruned, a mount gone, a rename — silently produced an agent
	// running in the user's home directory, filed in the sidebar under the
	// project it was not in.
	//
	// Measured: directory deleted, POST /api/sessions returns 201, and
	// pane_current_path is /home/jmr. For a panel whose job is running coding
	// agents, "refactor this" starting in somebody's home directory is the
	// wrong kind of surprise.
	//
	// A scratch terminal inherits its parent's working directory, which can
	// have gone while the project root is still there; the root is a useful
	// place to be and not a lie about where you are. Only when there is
	// nowhere left to stand does this refuse.
	if !isDirectory(dir) {
		if dir != p.Path && isDirectory(p.Path) {
			dir = p.Path
		} else {
			writeErr(w, http.StatusBadRequest,
				"the project directory is not there any more: "+p.Path)
			return
		}
	}

	sid := id.New()
	tmuxName := id.TmuxName(sid)

	env := s.hookEnv(ctx, sid, p.ID)

	err = s.Tmux.Create(ctx, tmux.CreateOptions{
		Name:    tmuxName,
		Dir:     dir,
		Command: req.Command,
		// Hooks identify their session and reach the panel through these. A
		// session created without them simply falls back to the output
		// heuristic, which is why the hook script is safe to install globally:
		// outside the panel the variables are absent and it does nothing.
		Env:    env,
		Width:  req.Cols,
		Height: req.Rows,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	rec, err := s.DB.CreateSession(ctx, store.Session{
		ID: sid, ProjectID: p.ID, TmuxName: tmuxName,
		Title: req.Title, CWD: dir, Cols: req.Cols, Rows: req.Rows,
		State: session.StateWorking, ParentID: parent,
	})
	if err != nil {
		// Untracked tmux session: remove it rather than orphan a process the
		// panel can never show again.
		_ = s.Tmux.Kill(context.WithoutCancel(ctx), tmuxName)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Title != "" {
		if err := s.DB.SetSessionTitle(ctx, sid, req.Title, store.TitleManual); err != nil {
			s.Log.Warn("set initial title", "session", sid, "err", err)
		}
		rec.TitleSource = store.TitleManual
	}
	if err := s.DB.TouchProject(ctx, p.ID); err != nil {
		s.Log.Warn("touch project", "project", p.ID, "err", err)
	}

	// Attach now, not at the next poll. A session can ring the bell within a
	// second of starting, and anything that happens before the pump is running
	// is simply not seen.
	if _, aerr := s.Manager.Attach(ctx, sid, tmuxName, req.Cols, req.Rows); aerr != nil {
		s.Log.Warn("attach new session", "session", sid, "err", aerr)
	}
	s.notifyState()
	writeJSON(w, http.StatusCreated, rec)
}

type patchSessionRequest struct {
	Title          *string `json:"title"`
	Pinned         *bool   `json:"pinned"`
	State          *string `json:"state"`
	SortIndex      *int    `json:"sortIndex"`
	ClearSortIndex bool    `json:"clearSortIndex"`
}

func (s *Server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "id")
	var req patchSessionRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Title != nil {
		// A title arriving over the API is a person renaming a tab, so it is
		// recorded as manual and stops automatic updates overwriting it.
		if err := s.DB.SetSessionTitle(ctx, sid, *req.Title, store.TitleManual); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if req.Pinned != nil {
		if err := s.DB.SetSessionPinned(ctx, sid, *req.Pinned); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if req.State != nil {
		st := session.State(*req.State)
		if !st.Valid() {
			writeErr(w, http.StatusBadRequest, "unknown state "+*req.State)
			return
		}
		// The detector has to know as well: it is what the poller consults, so
		// an override recorded only in the database is undone two seconds later.
		if s.Detector != nil {
			s.Detector.SetManual(sid, st, time.Now())
		}
		if err := s.DB.SetSessionState(ctx, sid, st, session.SourceManual); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if req.ClearSortIndex {
		if err := s.DB.SetSessionSortIndex(ctx, sid, nil); err != nil {
			writeStoreErr(w, err)
			return
		}
	} else if req.SortIndex != nil {
		if err := s.DB.SetSessionSortIndex(ctx, sid, req.SortIndex); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	rec, err := s.DB.GetSession(ctx, sid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.notifyState()
	writeJSON(w, http.StatusOK, rec)
}

// handleRestartSession brings a dead session's process back.
//
// In place, reusing the pane and the session id, so the sidebar entry, its
// name, its project and its scrollback all stay put. Deleting the session and
// making a new one would lose every one of those.
//
// What respawn-pane does destroy, measured rather than assumed: the pane's
// visible screen, which is where the crash message and the tail of any stack
// trace are. Everything that had already scrolled into history survives. A
// viewer watching at the time keeps the lost screen in the browser's own
// scrollback; a viewer who reloads afterwards does not, because replay is
// rebuilt from tmux's history.
// hookEnv builds the variables a hook inside a session needs to report back.
//
// Shared by creating a session and by restarting one whose tmux session has
// disappeared, because the second builds a session too and a copy of this that
// drifted would produce sessions that silently cannot report their state.
func (s *Server) hookEnv(ctx context.Context, sessionID, projectID string) []string {
	token, terr := s.HookToken(ctx)
	if terr != nil {
		s.Log.Warn("hook token unavailable; sessions will fall back to the heuristic", "err", terr)
		token = ""
	}
	return hooks.SessionEnv(sessionID, projectID, s.Cfg.LoopbackURL(), token)
}

func (s *Server) handleRestartSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sid := chi.URLParam(r, "id")
	rec, err := s.DB.GetSession(ctx, sid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// Respawn needs a session to respawn into. If the tmux session is gone —
	// killed from a shell, lost with the server, gone in a reboot — there is
	// nothing to restart and this used to answer 500, so the button offered on
	// exactly those rows was the one button that could not work.
	//
	// Build a new tmux session under the same name instead. The row keeps its
	// id, its title, its place in the project and anything written about it;
	// only the process is new, which is what "restart" means here.
	//
	// A login shell rather than the recorded command: `command` holds
	// #{pane_current_command}, the name of whatever was running last — "node"
	// for an agent, "bash" for a shell that had been used — and starting that
	// as an argv would run something the user never asked for. A shell in the
	// right directory is honest and always works.
	exists, cerr := s.Tmux.Has(ctx, rec.TmuxName)
	if cerr != nil {
		writeErr(w, http.StatusInternalServerError, cerr.Error())
		return
	}
	if exists {
		if err := s.Tmux.Respawn(ctx, rec.TmuxName); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		dir := rec.CWD
		if dir == "" {
			if p, perr := s.DB.GetProject(ctx, rec.ProjectID); perr == nil {
				dir = p.Path
			}
		}
		if err := s.Tmux.Create(ctx, tmux.CreateOptions{
			Name:   rec.TmuxName,
			Dir:    dir,
			Env:    s.hookEnv(ctx, rec.ID, rec.ProjectID),
			Width:  rec.Cols,
			Height: rec.Rows,
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// Clear the flag here rather than waiting for the poller: the button the
	// user just pressed has to visibly do something, and a tick of latency
	// reads as "nothing happened" and gets pressed again.
	if err := s.DB.SetSessionExit(ctx, sid, false, 0); err != nil {
		writeStoreErr(w, err)
		return
	}
	// A respawned pane is a new process behind the same session, so the old
	// evidence describes somebody else. Forgetting is what stops a bell rung
	// by the dead process from being attributed to the new one.
	if s.Detector != nil {
		s.Detector.Forget(sid)
	}
	if err := s.DB.SetSessionState(ctx, sid, session.StateWorking, session.SourceHeuristic); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sid := chi.URLParam(r, "id")
	rec, err := s.DB.GetSession(ctx, sid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// Children cascade away in the database, but their tmux sessions do not.
	// Deleting the row first would leave processes nothing in the UI can reach.
	children, err := s.DB.ListChildSessions(ctx, sid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	for _, child := range append(children, rec) {
		if err := s.tearDownSession(ctx, child.ID, child.TmuxName); err != nil {
			writeErr(w, http.StatusInternalServerError, "kill "+child.TmuxName+": "+err.Error())
			return
		}
	}
	if err := s.DB.DeleteSession(ctx, sid); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.notifyState()
	w.WriteHeader(http.StatusNoContent)
}

// ─── helpers ──────────────────────────────────────────────────────────────

// maxBodyBytes caps request bodies. None of these endpoints takes anything
// large, and an unbounded decode is a free denial of service.
const maxBodyBytes = 1 << 20

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

// emptyIfNil makes JSON arrays render as [] rather than null, so the frontend
// never has to guard a map over a missing list.
func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// expandHome resolves a leading ~ so the "new project" dialog accepts the paths
// people actually type.
func expandHome(p string) string {
	if p != "~" && !filepath.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// Reconcile makes the database agree with tmux at startup.
//
// The two can disagree in both directions: a session the panel recorded may
// have been killed while it was down, and the panel may have been killed
// between creating a tmux session and writing its row.
func (s *Server) Reconcile(ctx context.Context) error {
	infos, err := s.Tmux.List(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list tmux: %w", err)
	}
	byName := make(map[string]tmux.Info, len(infos))
	for _, i := range infos {
		byName[i.Name] = i
	}

	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list sessions: %w", err)
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		seen[row.TmuxName] = true
		info, alive := byName[row.TmuxName]
		if !alive {
			if err := s.markVanished(ctx, row); err != nil {
				s.Log.Warn("reconcile vanished", "session", row.ID, "err", err)
			}
			continue
		}
		if err := s.DB.UpdateSessionRuntime(ctx, row.ID, info.Path, info.Command); err != nil {
			s.Log.Warn("reconcile runtime", "session", row.ID, "err", err)
		}
		// A bell that rang while the panel was down is still latched, because
		// tmux only clears the flag when a client views the window and there
		// was no client. Read it before attaching, which clears it: otherwise
		// restarting the panel loses every "this needs you" raised while it
		// was gone — exactly when the user was not watching.
		if info.Bell && s.Detector != nil {
			s.Detector.Observe(row.ID, session.Signals{Bell: true}, time.Now())
		}
		// Attach at startup too, so state is being watched from the moment the
		// panel is up rather than from the first poll.
		if _, aerr := s.Manager.Attach(ctx, row.ID, row.TmuxName, row.Cols, row.Rows); aerr != nil {
			s.Log.Debug("attach at startup", "session", row.ID, "err", aerr)
		}
	}

	orphans := 0
	for _, i := range infos {
		if !seen[i.Name] {
			orphans++
		}
	}
	if orphans > 0 {
		// Adopting them would guess at which project they belong to. Reporting
		// is honest; `tmux -L <socket> kill-session` is one command away.
		s.Log.Warn("tmux sessions on our socket with no database row",
			"count", orphans, "socket", s.Cfg.TmuxSocket)
	}
	return nil
}

// pollInterval is how often tmux is asked what changed.
//
// Two seconds is a compromise: each tick shells out to tmux once, and the
// values it refreshes (title, cwd, current command) are things a human reads
// rather than reacts to. Live output does not come through here — it arrives on
// the PTY the instant it is produced.
const pollInterval = 2 * time.Second

// Poll keeps the database in step with tmux until the context ends.
func (s *Server) Poll(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.pollOnce(ctx); err != nil && ctx.Err() == nil {
				s.Log.Debug("poll", "err", err)
				continue
			}
			// Push only when the picture actually changed. A tick that
			// broadcasts regardless is polling again, just with the cost moved
			// onto every connected viewer.
			payload := s.snapshot(ctx)
			if payload == nil {
				continue
			}
			s.snapMu.Lock()
			changed := !bytes.Equal(payload, s.lastSnapshot)
			if changed {
				s.lastSnapshot = payload
			}
			s.snapMu.Unlock()
			if changed && s.Hub != nil {
				s.Hub.Broadcast(payload)
			}
		}
	}
}

// markVanished records that a session's tmux session is no longer there.
//
// The row is kept rather than deleted: the user should see what happened and
// dismiss it themselves, because losing a session silently is worse than
// showing a dead one. What is not kept is the state, which used to be frozen
// at whatever it last was — so a session that had been waiting for a human
// went on showing an orange triangle for an agent that no longer exists. The
// panel's one job is answering "which of these needs me", and a permanent
// wrong answer is worse than no answer.
//
// Exited with an unknown status, because that is the truth: nothing observed
// how it ended. Done as well, or it keeps sorting above everything running.
//
// Both the startup reconcile and the poller need this. The poller alone would
// do it within a second or two, but the second or two is exactly the moment
// after a reboot when most sessions are gone and the first thing on screen
// would be a list of triangles asking for attention that nothing needs.
func (s *Server) markVanished(ctx context.Context, row store.Session) error {
	if row.Exited && row.ExitStatus == store.ExitStatusVanished {
		return nil
	}
	if err := s.DB.SetSessionExit(ctx, row.ID, true, store.ExitStatusVanished); err != nil {
		return err
	}
	if row.State == session.StateDone {
		return nil
	}
	return s.DB.SetSessionState(ctx, row.ID, session.StateDone, session.SourceHeuristic)
}

func (s *Server) pollOnce(ctx context.Context) error {
	infos, err := s.Tmux.List(ctx)
	if err != nil {
		return err
	}
	// No early return when tmux reports nothing.
	//
	// It was here to skip building an empty map, and it skipped the whole
	// reconciliation pass instead — in precisely the state where reconciliation
	// is the only thing that can help: every session gone. Nothing then pruned
	// the detector, so a panel that had just lost its tmux server kept the
	// history of sessions that no longer existed. The loop below does nothing
	// on an empty list anyway; the work above it is what has to run.
	byName := make(map[string]tmux.Info, len(infos))
	for _, i := range infos {
		byName[i.Name] = i
	}
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		return err
	}
	if s.Detector != nil {
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		s.Detector.Retain(ids)
	}
	now := time.Now()
	for _, row := range rows {
		info, alive := byName[row.TmuxName]
		if !alive {
			if err := s.markVanished(ctx, row); err != nil {
				return err
			}
			continue
		}

		// Attach every live session, not only the one somebody is watching.
		//
		// State is inferred from the PTY byte stream, and the panel exists to
		// tell you about the sessions you are *not* looking at. Attaching only
		// on subscribe meant a session could ring the bell, sit there wanting
		// a human, and show as "done" until you happened to click it — the one
		// failure that makes the whole feature pointless.
		//
		// The cost is one tmux client and one replay buffer per session. For
		// the couple of dozen sessions this is built for, that is a few tens of
		// megabytes and a handful of small processes.
		if _, ok := s.Manager.Get(row.ID); !ok {
			if _, aerr := s.Manager.Attach(ctx, row.ID, row.TmuxName, row.Cols, row.Rows); aerr != nil {
				s.Log.Debug("attach for monitoring", "session", row.ID, "err", aerr)
			}
		}
		if err := s.DB.UpdateSessionRuntime(ctx, row.ID, info.Path, info.Command); err != nil {
			return err
		}
		if title := deriveTitle(info, row.ParentID != nil); title != "" {
			// SetSessionTitle with TitleAuto is a no-op once the user has
			// renamed the tab, so this cannot stomp a manual name.
			if err := s.DB.SetSessionTitle(ctx, row.ID, title, store.TitleAuto); err != nil {
				return err
			}
		}
		if info.Dead != row.Exited || (info.Dead && info.DeadStatus != row.ExitStatus) {
			if err := s.DB.SetSessionExit(ctx, row.ID, info.Dead, info.DeadStatus); err != nil {
				return err
			}
		}
		if s.Detector != nil {
			st, src := s.Detector.Evaluate(row.ID, session.Observation{
				Dead:      info.Dead,
				ShellOnly: session.IsShellCommand(info.Command),
			}, now)
			if st != row.State || src != row.StateSource {
				if err := s.DB.SetSessionState(ctx, row.ID, st, src); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// deriveTitle picks the best automatic name for a session.
//
// Three fallbacks, in descending order of how much they tell the user:
//
//  1. The title the application set over OSC 0/2. Agents set useful ones.
//  2. The running command — "claude", "codex" is exactly what is being looked
//     for. #{pane_title} is skipped when it is just the hostname, which is what
//     a plain shell leaves it as and is identical for every session on the box.
//  3. For a shell, the directory it sits in. Every shell is called "bash", so
//     the command tells you nothing; where it is at least distinguishes the one
//     in a worktree from the one at the repo root.
//
// The directory fallback is skipped for scratch terminals. They live in a tab
// strip that already belongs to one session in one directory, so naming them
// all after it produces a row of identical tabs. The UI numbers those instead.
func deriveTitle(info tmux.Info, isScratch bool) string {
	if info.Title != "" && info.Title != hostname() && info.Title != info.Command {
		return info.Title
	}
	if info.Command != "" && !isShell(info.Command) {
		return info.Command
	}
	if !isScratch && info.Path != "" {
		if base := filepath.Base(info.Path); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	if isShell(info.Command) {
		return ""
	}
	return info.Command
}

// isShell defers to the session package rather than keeping a second list.
//
// There were two, and they had already diverged: this one was missing csh,
// tcsh and the empty string. A session running csh was therefore a shell to
// the state machine — quiet means done rather than working — and not a shell
// to the namer, so it was labelled "csh" instead of falling back to the
// directory it sits in. Two lists that have to agree, in two packages, with
// nothing checking.
//
// session owns the judgement: its version says so in as many words, and the
// state machine is where being a shell has consequences. Naming just wants the
// same answer.
func isShell(cmd string) bool { return session.IsShellCommand(cmd) }

var cachedHostname = func() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}()

func hostname() string { return cachedHostname }
