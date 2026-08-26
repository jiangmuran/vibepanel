package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/version"
)

// started is when this process came up, for the uptime the settings page shows.
var started = time.Now()

func (s *Server) registerSettingsRoutes(r chi.Router) {
	r.Get("/settings", s.handleSettings)
	r.Get("/settings/audit", s.handleAudit)
	r.Get("/settings/hooks", s.handleHooksStatus)
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
	Sessions    int    `json:"sessions"`
	Attached    int    `json:"attached"`
	Viewers     int    `json:"viewers"`

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
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tv, _ := s.Tmux.Version(ctx)
	infos, _ := s.Tmux.List(ctx)

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
		Sessions: len(infos), Attached: len(s.Manager.LiveIDs()),

		DataDir: s.Cfg.DataDir, DBBytes: dbBytes,
		Addr: s.Cfg.Addr, URL: s.Cfg.PublicURL(),
		TLSMode: string(s.Cfg.TLSMode), Domain: s.Cfg.Domain,

		PasskeysUsable: s.Cfg.PasskeysUsable(),
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

func (s *Server) handleHooksInstall(w http.ResponseWriter, r *http.Request) {
	script, err := s.scriptPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The user's own settings file. It is backed up first, merged rather than
	// replaced, and every entry is tagged so removing them later cannot take
	// anybody else's with it.
	st, err := hooks.InstallClaude(script)
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
		s.audit(r.Context(), "hooks.installed", u.Username, s.clientIP(r), st.SettingsPath)
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleHooksUninstall(w http.ResponseWriter, r *http.Request) {
	script, err := s.scriptPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, err := hooks.UninstallClaude(script)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.forgetHookStatus()
	s.notifyState()
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "hooks.removed", u.Username, s.clientIP(r), st.SettingsPath)
	}
	writeJSON(w, http.StatusOK, st)
}
