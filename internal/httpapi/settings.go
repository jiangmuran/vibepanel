package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/version"
)

// started is when this process came up, for the uptime the settings page shows.
var started = time.Now()

func (s *Server) registerSettingsRoutes(r chi.Router) {
	r.Get("/settings", s.handleSettings)
	r.Get("/settings/audit", s.handleAudit)
	r.Get("/settings/hooks", s.handleHooksStatus)
	r.Get("/settings/tokens", s.handleListTokens)
	r.Post("/settings/tokens", s.handleCreateToken)
	r.Delete("/settings/tokens/{tokenID}", s.handleDeleteToken)
	r.Post("/settings/hooks", s.handleHooksInstall)
	r.Delete("/settings/hooks", s.handleHooksUninstall)
}

type settingsResponse struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
	Go      string `json:"go"`

	Uptime int64 `json:"uptime"`

	TmuxVersion string `json:"tmuxVersion"`
	TmuxSocket  string `json:"tmuxSocket"`
	// TmuxConfigStale means the running tmux server was started with a
	// different config from the one this binary carries.
	//
	// This is the half of an upgrade that nothing else can see. tmux reads its
	// `-f` file once, at start-server, and the panel never kills its server --
	// that is the premise of the project. So a new binary writes a new config
	// and the running server goes on using the old one, and both look
	// installed. `vibepanel doctor` says so, but nobody runs doctor after a
	// `systemctl restart`; the settings page is where a person looks.
	//
	// Not an error, and deliberately not phrased as one: applying it costs
	// every session on the socket, which is a decision for whoever reads it.
	TmuxConfigStale bool `json:"tmuxConfigStale"`
	// TmuxConfigUnknown means the running server predates the stamp, so the
	// question cannot be answered either way. Different from "it is current",
	// and a page that shows the two the same way is guessing.
	TmuxConfigUnknown bool `json:"tmuxConfigUnknown"`
	Sessions          int  `json:"sessions"`
	Attached          int  `json:"attached"`
	Viewers           int  `json:"viewers"`

	DataDir string `json:"dataDir"`
	DBBytes int64  `json:"dbBytes"`
	Addr    string `json:"addr"`
	URL     string `json:"url"`
	TLSMode string `json:"tlsMode"`
	// CertExpiry is unix seconds, absent when nothing is serving a certificate
	// or the mode does not have one.
	CertExpiry int64  `json:"certExpiry,omitempty"`
	Domain     string `json:"domain"`
	AllowAll   bool   `json:"allowAll"`

	PasskeysUsable bool   `json:"passkeysUsable"`
	PasskeyReason  string `json:"passkeyReason,omitempty"`
	Username       string `json:"username"`

	// VNCEnabled says whether the VNC routes exist at all. The settings
	// section is hidden when they do not -- not because hiding it protects
	// anything, the routes are simply absent, but because a form that posts
	// into a 404 is worse than no form.
	VNCEnabled bool `json:"vncEnabled"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tv, _ := s.Tmux.Version(ctx)
	infos, _ := s.Tmux.List(ctx)

	stamp := s.Tmux.RunningConfigStamp(ctx)
	configUnknown := stamp == ""
	configStale := !configUnknown && stamp != tmux.ConfigStamp()

	// The whole database, not just the main file.
	//
	// The panel runs in journal_mode=WAL, so recent writes live in
	// `vibepanel.db-wal` until a checkpoint, and a checkpoint can be held off by
	// a long-lived read -- which this panel has, with four pooled connections
	// and a poller reading every two seconds. So `os.Stat(DBPath())` alone can
	// report well under what is on disk, at the moment somebody is reading it to
	// answer "why is this growing", and it disagreed with the runbook's own
	// `du -sh ~/.local/share/vibepanel` for a reason neither screen explained.
	//
	// -shm is small and included anyway: it is part of what `du` counts, and the
	// point of this number is to agree with `du`.
	var dbBytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if st, err := os.Stat(s.Cfg.DBPath() + suffix); err == nil {
			dbBytes += st.Size()
		}
	}

	out := settingsResponse{
		Version: version.Version, Commit: version.Commit, Built: version.Date,
		Go:     runtime.Version(),
		Uptime: int64(time.Since(started).Seconds()),

		TmuxVersion: tv, TmuxSocket: s.Cfg.TmuxSocket,
		TmuxConfigStale: configStale, TmuxConfigUnknown: configUnknown,
		Sessions: len(infos), Attached: len(s.Manager.LiveIDs()),

		DataDir: s.Cfg.DataDir, DBBytes: dbBytes,
		Addr: s.Cfg.Addr, URL: s.Cfg.PublicURL(),
		TLSMode: string(s.Cfg.TLSMode), Domain: s.Cfg.Domain,

		PasskeysUsable: s.Cfg.PasskeysUsable(),
		VNCEnabled:     s.VNCEnabled,
	}
	if s.CertExpiry != nil {
		if at := s.CertExpiry(); !at.IsZero() {
			out.CertExpiry = at.Unix()
		}
	}
	if s.Hub != nil {
		out.Viewers = s.Hub.Connections()
	}
	if s.Auth != nil {
		out.AllowAll = len(s.Auth.Allow) == 0
	}
	if !out.PasskeysUsable {
		out.PasskeyReason = "needs a hostname in --domain plus TLS, or localhost"
	}
	if u, ok := currentUserFrom(r); ok {
		out.Username = u.Username
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.DB.RecentAudit(r.Context(), 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(entries))
}

// scriptPath installs the reporter if needed and returns where it lives.
func (s *Server) scriptPath() (string, error) {
	return hooks.InstallScript(filepath.Join(s.Cfg.DataDir, "hooks"))
}

func (s *Server) handleHooksStatus(w http.ResponseWriter, r *http.Request) {
	script, err := s.scriptPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, err := hooks.Inspect(script)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// hookAgent reads which agent a hook request is about.
//
// Defaults to Claude, which is what the parameter-less request meant before
// Codex had a button of its own, and refuses anything else rather than quietly
// installing for whichever one is first in the code. This decides which file in
// somebody's home directory gets edited, so a value nobody recognises has to be
// an error and not a guess.
func hookAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	switch agent := r.URL.Query().Get("agent"); agent {
	case "", "claude":
		return "claude", true
	case "codex":
		return "codex", true
	case "opencode":
		return "opencode", true
	default:
		writeErr(w, http.StatusBadRequest, "unknown agent "+agent+"; want claude, codex or opencode")
		return "", false
	}
}

func (s *Server) handleHooksInstall(w http.ResponseWriter, r *http.Request) {
	agent, ok := hookAgent(w, r)
	if !ok {
		return
	}
	script, err := s.scriptPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The user's own configuration file, either way. It is backed up first,
	// merged rather than replaced, and what the panel wrote stays recognisable
	// so that removing it later cannot take anybody else's hook with it.
	// opencode is the exception and the easy one: it auto-discovers every file
	// in its plugin directory, so installing writes a file that did not exist
	// rather than editing a document full of somebody's own settings.
	var st hooks.Status
	switch agent {
	case "codex":
		st, err = hooks.InstallCodex(script)
	case "opencode":
		if err = hooks.InstallOpencode(); err == nil {
			st, err = hooks.Inspect(script)
		}
	default:
		st, err = hooks.InstallClaude(script)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The snapshot's "states are being guessed" notice reads a cached answer;
	// without this it would keep telling the user to install hooks for up to a
	// TTL after they just did.
	s.forgetHookStatus()
	s.notifyState()
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "hooks.installed", u.Username, s.clientIP(r), hookTarget(agent, st))
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleHooksUninstall(w http.ResponseWriter, r *http.Request) {
	agent, ok := hookAgent(w, r)
	if !ok {
		return
	}
	script, err := s.scriptPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var st hooks.Status
	switch agent {
	case "codex":
		st, err = hooks.UninstallCodex(script)
	case "opencode":
		if err = hooks.UninstallOpencode(); err == nil {
			st, err = hooks.Inspect(script)
		}
	default:
		st, err = hooks.UninstallClaude(script)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.forgetHookStatus()
	s.notifyState()
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "hooks.removed", u.Username, s.clientIP(r), hookTarget(agent, st))
	}
	writeJSON(w, http.StatusOK, st)
}

// ─── API tokens ───────────────────────────────────────────────────────────

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.DB.ListAPITokens(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tokens))
}

type createTokenRequest struct {
	Name string `json:"name"`
}

// handleCreateToken mints one, and is the only time the token is ever readable.
//
// Stored as a hash, exactly like a session: a database that leaks must not hand
// over live credentials. Which means there is no "show it again" — the response
// to this request is the only copy, and the settings page says so before you
// press the button rather than after.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "api"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	token, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A prefix that names the token without being a head start on guessing it:
	// 8 characters of a 43-character random string.
	prefix := token
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	rec, err := s.DB.CreateAPIToken(r.Context(), id.New(), auth.HashToken(token), prefix, name, u.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(r.Context(), "token.created", u.Username, s.clientIP(r), name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     token,
		"id":        rec.ID,
		"name":      rec.Name,
		"prefix":    rec.Prefix,
		"createdAt": rec.CreatedAt,
	})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	tokenID := chi.URLParam(r, "tokenID")
	if err := s.DB.DeleteAPIToken(r.Context(), tokenID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "token.revoked", u.Username, s.clientIP(r), tokenID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// hookTarget names the file an audit entry is about.
//
// The audit log is read after the fact by somebody asking what this panel
// changed on their machine. Recording ~/.claude/settings.json for an edit to
// ~/.codex/config.toml sends them to the wrong file.
func hookTarget(agent string, st hooks.Status) string {
	switch agent {
	case "codex":
		return st.CodexPath
	case "opencode":
		return st.OpencodePath
	default:
		return st.SettingsPath
	}
}
