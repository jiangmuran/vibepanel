// Package httpapi exposes the REST surface and wires the WebSocket handler.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/version"
	"github.com/jiangmuran/vibepanel/internal/webui"
	"github.com/jiangmuran/vibepanel/internal/ws"
)

// Server holds everything the HTTP layer needs.
type Server struct {
	Cfg     config.Config
	DB      *store.DB
	Tmux    *tmux.Client
	Manager *session.Manager
	Log     *slog.Logger
}

// Routes builds the router.
//
// Order matters: /api and /ws are registered before the catch-all that serves
// the single-page app, so an unknown API path returns a JSON 404 instead of
// quietly handing the caller an HTML document.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.Get("/state", s.handleState)

		r.Post("/projects", s.handleCreateProject)
		r.Patch("/projects/{id}", s.handlePatchProject)
		r.Delete("/projects/{id}", s.handleDeleteProject)

		r.Post("/sessions", s.handleCreateSession)
		r.Patch("/sessions/{id}", s.handlePatchSession)
		r.Delete("/sessions/{id}", s.handleDeleteSession)

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusNotFound, "no such endpoint")
		})
	})

	r.Handle("/ws", &ws.Handler{
		Manager: s.Manager,
		Resolve: resolver{db: s.DB},
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
	writeJSON(w, http.StatusOK, stateResponse{
		Projects: emptyIfNil(projects),
		Sessions: emptyIfNil(sessions),
		Live:     emptyIfNil(s.Manager.LiveIDs()),
	})
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
	w.WriteHeader(http.StatusNoContent)
}

type createSessionRequest struct {
	ProjectID string   `json:"projectId"`
	Title     string   `json:"title"`
	Command   []string `json:"command"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
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

	sid := id.New()
	tmuxName := id.TmuxName(sid)
	err = s.Tmux.Create(ctx, tmux.CreateOptions{
		Name:    tmuxName,
		Dir:     p.Path,
		Command: req.Command,
		// Hooks identify their session from this. A session created without it
		// simply falls back to the output heuristic, which is why the hook
		// script is safe to install globally.
		Env: []string{
			"VIBEPANEL_SESSION_ID=" + sid,
			"VIBEPANEL_PROJECT_ID=" + p.ID,
		},
		Width:  req.Cols,
		Height: req.Rows,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	rec, err := s.DB.CreateSession(ctx, store.Session{
		ID: sid, ProjectID: p.ID, TmuxName: tmuxName,
		Title: req.Title, CWD: p.Path, Cols: req.Cols, Rows: req.Rows,
		State: session.StateWorking,
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
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sid := chi.URLParam(r, "id")
	rec, err := s.DB.GetSession(ctx, sid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.Manager.Detach(sid)
	if err := s.Tmux.Kill(ctx, rec.TmuxName); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.DeleteSession(ctx, sid); err != nil {
		writeStoreErr(w, err)
		return
	}
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
		if err := s.DB.UpdateSessionRuntime(ctx, row.ID, info.Path, info.Command, time.Now().Unix()); err != nil {
			s.Log.Warn("reconcile runtime", "session", row.ID, "err", err)
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
	for _, row := range rows {
		info, alive := byName[row.TmuxName]
		if !alive {
			continue
		}
		if err := s.DB.UpdateSessionRuntime(ctx, row.ID, info.Path, info.Command, time.Now().Unix()); err != nil {
			return err
		}
		if title := deriveTitle(info); title != "" {
			// SetSessionTitle with TitleAuto is a no-op once the user has
			// renamed the tab, so this cannot stomp a manual name.
			if err := s.DB.SetSessionTitle(ctx, row.ID, title, store.TitleAuto); err != nil {
				return err
			}
		}
	}
	return nil
}

// deriveTitle picks the best automatic name for a session.
//
// #{pane_title} defaults to the hostname for a plain shell, which is the same
// string for every session on the box and therefore useless as a tab name. When
// the application has not set a real title, the running command is far more
// informative — "claude" and "codex" are exactly what the user is looking for.
func deriveTitle(info tmux.Info) string {
	if info.Title != "" && info.Title != hostname() && info.Title != info.Command {
		return info.Title
	}
	if info.Command != "" && !isShell(info.Command) {
		return info.Command
	}
	return ""
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
