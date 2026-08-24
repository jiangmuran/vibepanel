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

	// outputSeen debounces last_output_at writes. The pump can call into here
	// hundreds of times a second; the column is read by humans.
	outMu      sync.Mutex
	outputSeen map[string]time.Time
}

// Routes builds the router.
//
// Order matters: /api and /ws are registered before the catch-all that serves
// the single-page app, so an unknown API path returns a JSON 404 instead of
// quietly handing the caller an HTML document.
func (s *Server) Routes() http.Handler {
	if s.Challenges == nil {
		s.Challenges = newChallengeStore()
	}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

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
		Manager: s.Manager,
		Resolve: resolver{db: s.DB},
		Hub:     s.Hub,
		Log:     s.Log,
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
	payload, err := json.Marshal(struct {
		Type string `json:"t"`
		stateResponse
	}{
		Type: ws.MsgState,
		stateResponse: stateResponse{
			Projects:     emptyIfNil(projects),
			Sessions:     emptyIfNil(sessions),
			Live:         emptyIfNil(s.Manager.LiveIDs()),
			ProjectOrder: orderMode(manual),
			StateGuessed: s.stateIsGuessed(sessions),
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
		"t":         "panel",
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
	s.tokenOnce.Do(func() {
		existing, err := s.DB.GetSetting(ctx, "hook_token", "")
		if err != nil {
			s.tokenError = err
			return
		}
		if existing != "" {
			s.hookToken = existing
			return
		}
		// 32 hex characters from crypto/rand. It travels in an Authorization
		// header on loopback and is written into the user's agent config, so
		// it wants to be unguessable but does not need to be long.
		token := id.New() + id.New()
		if err := s.DB.SetSetting(ctx, "hook_token", token); err != nil {
			s.tokenError = err
			return
		}
		s.hookToken = token
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
	writeJSON(w, http.StatusOK, stateResponse{
		Projects:     emptyIfNil(projects),
		Sessions:     emptyIfNil(sessions),
		Live:         emptyIfNil(s.Manager.LiveIDs()),
		ProjectOrder: orderMode(manual),
		StateGuessed: s.stateIsGuessed(sessions),
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
	script, err := s.scriptPath()
	if err != nil {
		return true
	}
	st, err := hooks.Inspect(script)
	if err != nil {
		return true
	}
	return !st.Installed
}

// loopbackURL is where a hook running on this machine should POST.
//
// Always loopback, never the public URL: the hook runs beside the panel, and
// sending its reports out to the internet and back would put a secret on the
// wire for no reason.
func (s *Server) loopbackURL() string {
	port := s.Cfg.Port()
	if port == 0 {
		port = 8443
	}
	scheme := "http"
	if s.Cfg.TLSMode != config.TLSOff {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", scheme, port)
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
		if err := s.DB.ClearProjectOrder(ctx); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if len(req.Ids) == 0 {
			writeErr(w, http.StatusBadRequest, "ids must not be empty; send auto:true to clear the order")
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
		s.Manager.Detach(sess.ID)
		if err := s.Tmux.Kill(ctx, sess.TmuxName); err != nil {
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

	sid := id.New()
	tmuxName := id.TmuxName(sid)

	env := []string{
		"VIBEPANEL_SESSION_ID=" + sid,
		"VIBEPANEL_PROJECT_ID=" + p.ID,
		"VIBEPANEL_URL=" + s.loopbackURL(),
	}
	if token, terr := s.HookToken(ctx); terr == nil {
		env = append(env, "VIBEPANEL_TOKEN="+token)
	} else {
		s.Log.Warn("hook token unavailable; sessions will fall back to the heuristic", "err", terr)
	}

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
func (s *Server) handleRestartSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sid := chi.URLParam(r, "id")
	rec, err := s.DB.GetSession(ctx, sid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if err := s.Tmux.Respawn(ctx, rec.TmuxName); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
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
		s.Manager.Detach(child.ID)
		if s.Detector != nil {
			s.Detector.Forget(child.ID)
		}
		if err := s.Tmux.Kill(ctx, child.TmuxName); err != nil {
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
			// The tmux session is gone. The row is kept rather than deleted so
			// the user can see what happened and dismiss it themselves; losing
			// a session silently is worse than showing a dead one.
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

func (s *Server) pollOnce(ctx context.Context) error {
	infos, err := s.Tmux.List(ctx)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		return nil
	}
	byName := make(map[string]tmux.Info, len(infos))
	for _, i := range infos {
		byName[i.Name] = i
	}
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, row := range rows {
		info, alive := byName[row.TmuxName]
		if !alive {
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

func isShell(cmd string) bool {
	switch cmd {
	case "bash", "sh", "zsh", "fish", "dash", "ksh", "tmux":
		return true
	}
	return false
}

var cachedHostname = func() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}()

func hostname() string { return cachedHostname }
