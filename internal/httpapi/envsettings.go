package httpapi

import (
	"net/http"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/config"
)

// envResponse is the file, as the settings page shows it.
type envResponse struct {
	Path string `json:"path"`
	// Keys is the editable list, in the order the page draws them. From the
	// server so the two cannot disagree about what a PUT will accept.
	Keys   []string          `json:"keys"`
	Values map[string]string `json:"values"`
	// Live is what this process is actually running with, which is not the
	// same thing and is the whole reason the page needs a restart button. A
	// file edited an hour ago and never applied looks identical to one that is
	// in force.
	Live map[string]string `json:"live"`
	// Socket is shown and not editable. Red line 1: a panel pointed at another
	// tmux socket cannot see its own sessions, and the ones it was managing
	// keep running with nothing attached to them.
	Socket string `json:"socket"`
}

func (s *Server) handleGetEnv(w http.ResponseWriter, r *http.Request) {
	path := config.EnvFilePath()
	values, err := config.ReadEnvFile(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Only the editable ones go out. The file may also hold
	// CLOUDFLARE_API_TOKEN, and a response that carries it puts an ACME
	// credential into every browser cache and every screenshot of this page.
	out := envResponse{
		Path:   path,
		Keys:   config.EditableEnv,
		Values: map[string]string{},
		Live: map[string]string{
			"VIBEPANEL_ADDR":            s.Cfg.Addr,
			"VIBEPANEL_DOMAIN":          s.Cfg.Domain,
			"VIBEPANEL_TLS_MODE":        string(s.Cfg.TLSMode),
			"VIBEPANEL_CERT_FILE":       s.Cfg.CertFile,
			"VIBEPANEL_KEY_FILE":        s.Cfg.KeyFile,
			"VIBEPANEL_ALLOW_FROM":      strings.Join(s.Cfg.AllowFrom, ","),
			"VIBEPANEL_TRUSTED_PROXIES": strings.Join(s.Cfg.TrustedProxies, ","),
		},
		Socket: s.Cfg.TmuxSocket,
	}
	for _, k := range config.EditableEnv {
		out.Values[k] = values[k]
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePutEnv writes the editable keys and nothing else.
//
// It does not restart. Applying these costs every connection and is a separate
// decision -- and one somebody may want to make after changing three of them
// rather than after each.
func (s *Server) handlePutEnv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Values map[string]string `json:"values"`
	}
	if !decode(w, r, &req) {
		return
	}
	path := config.EnvFilePath()
	if err := config.PatchEnvFile(path, req.Values); err != nil {
		// A key that is not editable is the caller's mistake, not the server's.
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "env.changed", u.Username, s.clientIP(r), path)
	}
	s.handleGetEnv(w, r)
}
