package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/chat/assistant"
	"github.com/jiangmuran/vibepanel/internal/headless"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/version"
)

// The headless assistant: `claude -p` runs for the G2 glasses, started with an
// API token from a page on another origin. internal/headless is the logic;
// this file is the routes, the CORS answer and the settings rows.
//
// Security, in one place:
//
//   - Every route but the preflight is behind RequireAuth, and from another
//     origin only a Bearer token authenticates: the cookie is removed from a
//     cross-origin request before RequireAuth reads it, so an allowed origin
//     that happens to be same-site with the panel (another port on this host)
//     cannot ride the owner's browser session.
//   - The token is full authority over the machine, because runs default to
//     bypassPermissions (the owner's explicit decision; see the package
//     comment in internal/headless). The settings page says so beside the
//     switch.
//   - CORS is an exact allowlist from the settings, reflected, never `*` and
//     never with credentials. The same list lets those origins past the
//     cross-origin-write check, for /api/headless/* only.
//   - The prompt is never audited or logged; the run's project, model and
//     permission mode are.

const (
	headlessSettingsKey = "headless.settings"
	headlessASRKeyKey   = "headless.asr_key_sealed"
	headlessPrefix      = "/api/headless/"
	maxHeadlessPrompt   = 8 << 10
	maxHeadlessContext  = 2 << 10
	// liveWindow is how recently a vibepanel session in the project must have
	// printed, and its transcript been written, for a session to count as
	// being worked on at a terminal right now.
	liveWindow = 2 * time.Minute
)

// headlessState is the runs and the harness lookup, made on first use so a
// Server built by hand works.
type headlessState struct {
	once     sync.Once
	registry *headless.Registry

	harnessMu   sync.Mutex
	harnessBin  string
	harnessPath string
}

func (s *Server) headlessRuns() *headless.Registry {
	s.hl.once.Do(func() { s.hl.registry = headless.NewRegistry() })
	return s.hl.registry
}

// headlessHarness is the claude binary and the PATH its child needs. A test
// sets HeadlessBinary to a fake.
func (s *Server) headlessHarness() (string, string, error) {
	if s.HeadlessBinary != "" {
		return s.HeadlessBinary, "", nil
	}
	s.hl.harnessMu.Lock()
	defer s.hl.harnessMu.Unlock()
	if s.hl.harnessBin != "" {
		return s.hl.harnessBin, s.hl.harnessPath, nil
	}
	// A failure is not remembered: installing claude should not need a
	// panel restart to be noticed.
	bin, path, err := assistant.FindHarness("claude")
	if err != nil {
		return "", "", err
	}
	s.hl.harnessBin, s.hl.harnessPath = bin, path
	return bin, path, nil
}

// headlessSettings is the stored settings over the defaults.
func (s *Server) headlessSettings(ctx context.Context) (headless.Settings, error) {
	set := headless.Defaults()
	raw, err := s.DB.GetSetting(ctx, headlessSettingsKey, "")
	if err != nil {
		return set, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &set); err != nil {
			return headless.Defaults(), fmt.Errorf("the stored headless settings do not parse: %w", err)
		}
	}
	return set, nil
}

func headlessASRContext() string { return "headless:asr-key" }

// headlessASRKey is the speech-to-text key, or "" when none is set.
func (s *Server) headlessASRKey(ctx context.Context) (string, error) {
	enc, err := s.DB.GetSetting(ctx, headlessASRKeyKey, "")
	if err != nil || enc == "" {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	box, err := s.secretBox()
	if err != nil {
		return "", err
	}
	plain, err := box.Unseal(sealed, headlessASRContext())
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Server) headlessHasASRKey(ctx context.Context) bool {
	enc, err := s.DB.GetSetting(ctx, headlessASRKeyKey, "")
	return err == nil && enc != ""
}

// headlessOriginAllowed reports whether this is a /api/headless/* request
// from an origin the settings list.
func (s *Server) headlessOriginAllowed(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, headlessPrefix) {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	set, err := s.headlessSettings(r.Context())
	if err != nil {
		return false
	}
	return set.OriginAllowed(origin)
}

func setHeadlessCORS(h http.Header, origin string) {
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Last-Event-ID")
	h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	h.Set("Access-Control-Max-Age", "600")
}

// handleHeadlessPreflight answers OPTIONS outside auth: a preflight carries
// no credential by design. 204 either way; an origin not on the list gets no
// Access-Control headers, which is the browser's refusal.
func (s *Server) handleHeadlessPreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Origin")
	if s.headlessOriginAllowed(r) {
		setHeadlessCORS(w.Header(), r.Header.Get("Origin"))
	}
	w.WriteHeader(http.StatusNoContent)
}

// headlessCORS runs ahead of RequireAuth on the headless routes, so a 401 or
// 403 reaches the page as a readable answer rather than an opaque CORS
// failure. It also drops the cookie from any request that names another
// origin: from there, only the token counts.
func (s *Server) headlessCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !sameOrigin(strings.TrimSuffix(origin, "/"), s.requestOrigin(r)) {
			r.Header.Del("Cookie")
			w.Header().Add("Vary", "Origin")
			if s.headlessOriginAllowed(r) {
				setHeadlessCORS(w.Header(), origin)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// registerHeadlessPublicRoutes is the preflight, registered outside auth.
func (s *Server) registerHeadlessPublicRoutes(r chi.Router) {
	r.Options("/headless/*", s.handleHeadlessPreflight)
}

// registerHeadlessRoutes is the client surface, in a group of its own so the
// CORS middleware runs before RequireAuth.
func (s *Server) registerHeadlessRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.headlessCORS)
		r.Use(s.RequireAuth)
		r.Get("/headless/config", s.handleHeadlessConfig)
		r.Group(func(r chi.Router) {
			r.Use(s.requireHeadlessEnabled)
			r.Get("/headless/projects", s.handleHeadlessProjects)
			r.Get("/headless/sessions", s.handleHeadlessSessions)
			r.Get("/headless/sessions/{sid}", s.handleHeadlessSession)
			r.Post("/headless/runs", s.handleHeadlessCreateRun)
			r.Get("/headless/runs", s.handleHeadlessListRuns)
			r.Get("/headless/runs/{runId}/events", s.handleHeadlessRunEvents)
			r.Delete("/headless/runs/{runId}", s.handleHeadlessStopRun)
			r.Post("/headless/transcribe", s.handleHeadlessTranscribe)
		})
	})
}

// registerHeadlessSettingsRoutes is the settings page's pair, with the rest
// of the settings.
func (s *Server) registerHeadlessSettingsRoutes(r chi.Router) {
	r.Get("/settings/headless", s.handleGetHeadlessSettings)
	r.Put("/settings/headless", s.handlePutHeadlessSettings)
}

func (s *Server) requireHeadlessEnabled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		set, err := s.headlessSettings(r.Context())
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if !set.Enabled {
			writeErr(w, http.StatusServiceUnavailable, "headless is disabled in settings")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- settings ---------------------------------------------------------------

type headlessASRView struct {
	headless.ASR
	HasKey bool `json:"hasKey"`
}

type headlessSettingsView struct {
	headless.Settings
	ASR headlessASRView `json:"asr"`
	// DefaultPersona is what an empty persona means, so the settings page can
	// show it as the placeholder without a copy of its own.
	DefaultPersona string `json:"defaultPersona"`
}

func (s *Server) headlessView(ctx context.Context, set headless.Settings) headlessSettingsView {
	if set.AllowedOrigins == nil {
		set.AllowedOrigins = []string{}
	}
	return headlessSettingsView{
		Settings:       set,
		ASR:            headlessASRView{ASR: set.ASR, HasKey: s.headlessHasASRKey(ctx)},
		DefaultPersona: headless.DefaultPersona,
	}
}

func (s *Server) handleGetHeadlessSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.headlessSettings(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.headlessView(r.Context(), set))
}

// headlessASRBody is the asr object a PUT takes: the settings, plus the
// write-only key and its clear switch. hasKey is accepted and ignored, so a
// client can send back what it was given.
type headlessASRBody struct {
	headless.ASR
	APIKey   *string `json:"apiKey"`
	ClearKey bool    `json:"clearKey"`
	HasKey   *bool   `json:"hasKey"`
}

type headlessSettingsBody struct {
	headless.Settings
	ASR            headlessASRBody `json:"asr"`
	DefaultPersona *string         `json:"defaultPersona"`
}

// handlePutHeadlessSettings stores the settings. Fields left out keep their
// current values; assistantProjectId is the server's and is ignored.
func (s *Server) handlePutHeadlessSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, err := s.headlessSettings(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	body := headlessSettingsBody{Settings: cur, ASR: headlessASRBody{ASR: cur.ASR}}
	if !decode(w, r, &body) {
		return
	}
	next := body.Settings
	next.ASR = body.ASR.ASR
	next.AssistantProjectID = cur.AssistantProjectID
	next.Normalize()
	if err := next.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ASR.APIKey != nil && len(*body.ASR.APIKey) > 512 {
		writeErr(w, http.StatusBadRequest, "the speech-to-text key is too long")
		return
	}
	if next.LaunchProfileID != "" {
		if _, err := s.launchProfileFor(ctx, next.LaunchProfileID); errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, "no launch profile "+next.LaunchProfileID)
			return
		} else if err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if next.Enabled {
		if err := s.ensureAssistant(ctx, &next); err != nil {
			writeErr(w, http.StatusBadRequest, "preparing the assistant workspace: "+err.Error())
			return
		}
	}

	switch {
	case body.ASR.ClearKey:
		if err := s.DB.SetSetting(ctx, headlessASRKeyKey, ""); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	case body.ASR.APIKey != nil && strings.TrimSpace(*body.ASR.APIKey) != "":
		box, err := s.secretBox()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "cannot open the panel's secrets key: "+err.Error())
			return
		}
		sealed := box.Seal([]byte(strings.TrimSpace(*body.ASR.APIKey)), headlessASRContext())
		if err := s.DB.SetSetting(ctx, headlessASRKeyKey, base64.StdEncoding.EncodeToString(sealed)); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}

	raw, err := json.Marshal(next)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.SetSetting(ctx, headlessSettingsKey, string(raw)); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "headless.settings", u.Username, s.clientIP(r),
			fmt.Sprintf("enabled=%t mode=%s origins=%s", next.Enabled, next.DefaultPermissionMode, strings.Join(next.AllowedOrigins, " ")))
	}
	writeJSON(w, http.StatusOK, s.headlessView(ctx, next))
}

// ensureAssistant creates the workspace (never overwriting a file) and makes
// sure a project points at it, recording which in the settings.
func (s *Server) ensureAssistant(ctx context.Context, set *headless.Settings) error {
	home, _ := os.UserHomeDir()
	dir, err := headless.ExpandDir(set.AssistantDir, home)
	if err != nil {
		return err
	}
	if err := headless.EnsureAssistantDir(dir); err != nil {
		return err
	}
	if set.AssistantProjectID != "" {
		p, err := s.DB.GetProject(ctx, set.AssistantProjectID)
		if err == nil && filepath.Clean(p.Path) == dir {
			if p.ArchivedAt != nil {
				if err := s.DB.RestoreProject(ctx, p.ID); err != nil {
					return err
				}
				s.notifyState()
			}
			return nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if filepath.Clean(p.Path) == dir {
			set.AssistantProjectID = p.ID
			return nil
		}
	}
	// The same rule POST /api/projects follows: an archived project on this
	// directory comes back rather than being added again beside itself.
	if old, err := s.DB.ArchivedProjectAt(ctx, dir); err == nil {
		if err := s.DB.RestoreProject(ctx, old.ID); err != nil {
			return err
		}
		set.AssistantProjectID = old.ID
		s.notifyState()
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	p, err := s.DB.CreateProject(ctx, id.New(), "助理", dir)
	if err != nil {
		return err
	}
	set.AssistantProjectID = p.ID
	s.notifyState()
	return nil
}

// --- client routes ------------------------------------------------------------

func (s *Server) handleHeadlessConfig(w http.ResponseWriter, r *http.Request) {
	set, err := s.headlessSettings(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	models := set.Models
	if models == nil {
		models = []headless.Model{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":               set.Enabled,
		"version":               strings.TrimPrefix(version.Version, "v"),
		"models":                models,
		"defaultModel":          set.DefaultModel,
		"permissionModes":       headless.PermissionModes,
		"defaultPermissionMode": set.DefaultPermissionMode,
		"asr":                   s.headlessASRConfigured(r.Context(), set),
		"assistantProjectId":    set.AssistantProjectID,
		"host":                  hostname(),
	})
}

func (s *Server) headlessASRConfigured(ctx context.Context, set headless.Settings) bool {
	return set.ASR.BaseURL != "" && set.ASR.Model != "" && s.headlessHasASRKey(ctx)
}

type headlessProject struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	LastActiveAt int64  `json:"lastActiveAt"`
	Assistant    bool   `json:"assistant"`
}

func (s *Server) handleHeadlessProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	set, err := s.headlessSettings(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	list, err := s.DB.ListProjects(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out := make([]headlessProject, 0, len(list))
	for _, p := range list {
		if p.ArchivedAt != nil {
			continue
		}
		out = append(out, headlessProject{
			ID: p.ID, Name: p.Name, Path: p.Path, LastActiveAt: p.LastActiveAt,
			Assistant: p.ID == set.AssistantProjectID,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Assistant != out[j].Assistant {
			return out[i].Assistant
		}
		return out[i].LastActiveAt > out[j].LastActiveAt
	})
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

// headlessLaunch is the environment a run gets from the settings' launch
// profile and its Claude account, and the Claude config directory that
// environment points at -- which is where the transcripts are.
func (s *Server) headlessLaunch(ctx context.Context, set headless.Settings) ([]string, string, error) {
	profile, err := s.launchProfileFor(ctx, set.LaunchProfileID)
	if err != nil {
		return nil, "", err
	}
	var accountID string
	if profile != nil {
		accountID = profile.ClaudeAccountID
	}
	accountEnv, err := s.claudeAccountEnv(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	// No hookEnv: a headless run is not a session, and with the hook
	// variables present the person's own hooks would report its turns as
	// some session's states.
	env := store.LaunchEnv(profile, accountEnv)
	configDir := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok && v != "" {
			configDir = v
		}
	}
	if configDir == "" {
		main, err := s.claudeMain()
		if err != nil {
			return nil, "", err
		}
		configDir = filepath.Join(main.Home, ".claude")
	} else if home, _ := os.UserHomeDir(); home != "" {
		configDir, _ = headless.ExpandDir(configDir, home)
	}
	return env, configDir, nil
}

// writeLaunchErr answers a failed headlessLaunch.
func (s *Server) writeLaunchErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errClaudeAccountGone):
		writeErr(w, http.StatusConflict, "the headless launch profile's Claude account has been removed")
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusConflict, "the headless launch profile has been removed; choose another in settings")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// headlessProjectFor reads ?projectId=, answering the error itself.
func (s *Server) headlessProjectFor(w http.ResponseWriter, r *http.Request, pid string) (store.Project, bool) {
	if pid == "" {
		writeErr(w, http.StatusBadRequest, "projectId is required")
		return store.Project{}, false
	}
	p, err := s.DB.GetProject(r.Context(), pid)
	if err != nil {
		s.writeStoreErr(w, err)
		return store.Project{}, false
	}
	return p, true
}

func (s *Server) handleHeadlessSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.headlessProjectFor(w, r, r.URL.Query().Get("projectId"))
	if !ok {
		return
	}
	set, err := s.headlessSettings(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	_, configDir, err := s.headlessLaunch(ctx, set)
	if err != nil {
		s.writeLaunchErr(w, err)
		return
	}
	list, err := headless.ListSessions(configDir, p.Path, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	terminalBusy := s.projectPrintedSince(ctx, p.ID, time.Now().Add(-liveWindow))
	runs := s.headlessRuns()
	for i := range list {
		list[i].Running = runs.RunningSession(list[i].ID)
		// Best effort, and said so in the doc: a transcript written in the
		// last two minutes, in a project where a panel session printed in
		// the last two minutes, and not by a run of ours.
		list[i].Live = !list[i].Running && terminalBusy && time.Since(list[i].Modified()) < liveWindow
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

// projectPrintedSince reports whether any live panel session in the project
// has printed since t.
func (s *Server) projectPrintedSince(ctx context.Context, projectID string, t time.Time) bool {
	sessions, err := s.DB.ListSessions(ctx)
	if err != nil {
		return false
	}
	for _, se := range sessions {
		if se.ProjectID == projectID && !se.Exited && se.LastOutputAt >= t.Unix() {
			return true
		}
	}
	return false
}

func (s *Server) handleHeadlessSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sid := chi.URLParam(r, "sid")
	if !headless.ValidSessionID(sid) {
		writeErr(w, http.StatusBadRequest, "a session id is a UUID")
		return
	}
	p, ok := s.headlessProjectFor(w, r, r.URL.Query().Get("projectId"))
	if !ok {
		return
	}
	limit := 40
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeErr(w, http.StatusBadRequest, "limit is a number from 1 to 500")
			return
		}
		limit = n
	}
	set, err := s.headlessSettings(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	_, configDir, err := s.headlessLaunch(ctx, set)
	if err != nil {
		s.writeLaunchErr(w, err)
		return
	}
	msgs, err := headless.ReadSession(configDir, p.Path, sid, limit)
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, "no such session in that project")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": sid, "messages": msgs})
}

type headlessRunRequest struct {
	ProjectID      string `json:"projectId"`
	Prompt         string `json:"prompt"`
	SessionID      string `json:"sessionId"`
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`
	Context        string `json:"context"`
}

func (s *Server) handleHeadlessCreateRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req headlessRunRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if len(req.Prompt) > maxHeadlessPrompt {
		writeErr(w, http.StatusBadRequest, "prompt is longer than 8 KiB")
		return
	}
	if len(req.Context) > maxHeadlessContext {
		writeErr(w, http.StatusBadRequest, "context is longer than 2 KiB")
		return
	}
	set, err := s.headlessSettings(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if req.Model == "" {
		req.Model = set.DefaultModel
	}
	if !set.HasModel(req.Model) {
		writeErr(w, http.StatusBadRequest, "model "+req.Model+" is not in the headless model list")
		return
	}
	if req.PermissionMode == "" {
		req.PermissionMode = set.DefaultPermissionMode
	}
	if !headless.ValidPermissionMode(req.PermissionMode) {
		writeErr(w, http.StatusBadRequest, "permission mode "+req.PermissionMode+" is not one of "+strings.Join(headless.PermissionModes, ", "))
		return
	}
	isNew := req.SessionID == ""
	if isNew {
		req.SessionID = headless.NewSessionID()
	} else if !headless.ValidSessionID(req.SessionID) {
		writeErr(w, http.StatusBadRequest, "sessionId is a UUID, or empty for a new session")
		return
	}

	p, ok := s.headlessProjectFor(w, r, req.ProjectID)
	if !ok {
		return
	}
	if p.ArchivedAt != nil {
		writeErr(w, http.StatusConflict, "project "+p.Name+" is archived; restore it first")
		return
	}
	if !isDirectory(p.Path) {
		writeErr(w, http.StatusBadRequest, "the project directory is not there any more: "+p.Path)
		return
	}

	env, _, err := s.headlessLaunch(ctx, set)
	if err != nil {
		s.writeLaunchErr(w, err)
		return
	}
	bin, loginPath, err := s.headlessHarness()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	home, _ := os.UserHomeDir()
	assistantDir, err := headless.ExpandDir(set.AssistantDir, home)
	if err != nil {
		writeErr(w, http.StatusConflict, "the assistant directory in settings: "+err.Error())
		return
	}
	sp := headless.SystemPrompt(headless.PromptInput{
		Persona: set.Persona, AssistantDir: assistantDir, Memory: set.MemoryInjection,
		ProjectName: p.Name, ProjectPath: p.Path, Host: hostname(), Context: req.Context,
	})
	run, err := s.headlessRuns().Start(headless.Spec{
		Binary:        bin,
		Args:          headless.RunArgs(req.Prompt, req.SessionID, !isNew, req.Model, req.PermissionMode, sp),
		Dir:           p.Path,
		Env:           assistant.HarnessEnv(loginPath, env),
		Timeout:       time.Duration(set.TimeoutMinutes) * time.Minute,
		SessionID:     req.SessionID,
		ProjectID:     p.ID,
		Prompt:        req.Prompt,
		MaxConcurrent: set.MaxConcurrent,
	})
	switch {
	case errors.Is(err, headless.ErrSessionBusy):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, headless.ErrTooMany):
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("%s (at most %d at once)", err.Error(), set.MaxConcurrent))
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u, ok := currentUserFrom(r); ok {
		// Never the prompt: it is whatever somebody said to their glasses.
		s.audit(ctx, "headless.run", u.Username, s.clientIP(r),
			fmt.Sprintf("%s · %s · %s", p.Name, req.Model, req.PermissionMode))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"runId": run.ID, "sessionId": req.SessionID, "new": isNew})
}

func (s *Server) handleHeadlessListRuns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"runs": s.headlessRuns().List(r.URL.Query().Get("projectId"))})
}

func (s *Server) handleHeadlessStopRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.headlessRuns().Get(chi.URLParam(r, "runId"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such run")
		return
	}
	run.Stop()
	w.WriteHeader(http.StatusNoContent)
}

// headlessPing is the SSE heartbeat: often enough that a proxy or a phone's
// network stack does not decide the stream is dead during a long tool call.
var headlessPing = 15 * time.Second

// handleHeadlessRunEvents streams a run as server-sent events: the buffered
// events after ?after= (or Last-Event-ID), then live ones, closing after the
// terminal event.
func (s *Server) handleHeadlessRunEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.headlessRuns().Get(chi.URLParam(r, "runId"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such run")
		return
	}
	var after int64
	v := r.URL.Query().Get("after")
	if v == "" {
		v = r.Header.Get("Last-Event-ID")
	}
	if v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "after is an event id")
			return
		}
		after = n
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported here")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	// nginx buffers responses by default, which turns a stream into one
	// answer at the end.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ping := time.NewTicker(headlessPing)
	defer ping.Stop()
	for {
		evs, changed, done := run.Since(after)
		for _, e := range evs {
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, e.JSON()); err != nil {
				return
			}
			after = e.Seq
		}
		if len(evs) > 0 {
			flusher.Flush()
		}
		if done {
			return
		}
		select {
		case <-changed:
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleHeadlessTranscribe turns audio into text through the configured
// OpenAI-compatible upstream. Raw PCM is wrapped in a WAV header here, so
// the glasses can send what their microphone gives them.
func (s *Server) handleHeadlessTranscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	set, err := s.headlessSettings(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	key, err := s.headlessASRKey(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "reading the speech-to-text key: "+err.Error())
		return
	}
	if set.ASR.BaseURL == "" || set.ASR.Model == "" || key == "" {
		writeErr(w, http.StatusServiceUnavailable, "speech-to-text is not configured in the headless settings")
		return
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeErr(w, http.StatusUnsupportedMediaType, "send audio/L16; rate=16000 or audio/wav")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, headless.MaxAudioBytes))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "audio is limited to 4 MiB (about two minutes)")
		return
	}
	if len(body) == 0 {
		writeErr(w, http.StatusBadRequest, "no audio")
		return
	}
	var wav []byte
	switch strings.ToLower(mt) {
	case "audio/l16":
		rate, channels := 16000, 1
		if v := params["rate"]; v != "" {
			if rate, err = strconv.Atoi(v); err != nil || rate < 8000 || rate > 48000 {
				writeErr(w, http.StatusBadRequest, "rate must be between 8000 and 48000")
				return
			}
		}
		if v := params["channels"]; v != "" {
			if channels, err = strconv.Atoi(v); err != nil || channels < 1 || channels > 2 {
				writeErr(w, http.StatusBadRequest, "channels must be 1 or 2")
				return
			}
		}
		// The contract says s16le. RFC 2586's L16 is big-endian, but the
		// glasses' microphone hands over little-endian and that is what is
		// sent; this does not swap.
		if len(body)%2 == 1 {
			body = body[:len(body)-1]
		}
		wav = headless.WAV(body, rate, channels)
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		if len(body) < 12 || string(body[:4]) != "RIFF" || string(body[8:12]) != "WAVE" {
			writeErr(w, http.StatusBadRequest, "that is not a WAV file")
			return
		}
		wav = body
	default:
		writeErr(w, http.StatusUnsupportedMediaType, "send audio/L16; rate=16000 or audio/wav")
		return
	}
	hint := strings.TrimSpace(r.URL.Query().Get("prompt"))
	if len(hint) > 1024 {
		hint = hint[:1024]
	}
	prompt := strings.TrimSpace(set.ASR.Prompt + " " + hint)
	start := time.Now()
	client := s.HeadlessHTTP
	if client == nil {
		client = http.DefaultClient
	}
	text, err := headless.Transcribe(ctx, client, headless.TranscribeRequest{
		BaseURL: set.ASR.BaseURL, APIKey: key, Model: set.ASR.Model,
		Language: set.ASR.Language, Prompt: prompt, WAV: wav,
	})
	var up *headless.UpstreamError
	if errors.As(err, &up) {
		writeErr(w, http.StatusBadGateway, up.Message)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "ms": time.Since(start).Milliseconds()})
}
