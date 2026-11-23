package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/shareskill"
)

// Optional agent skill/plugin management for one-prompt share page creation
// across Claude Code, Codex, Antigravity CLI (agy), and DeepSeek Harness (dsh).
//
// Registered strictly under /api/settings/share-skill behind RequireAuth:
// share tokens never reach these routes, and installing or removing never
// touches any agent file outside the panel's dedicated vibepanel-share folder.

func (s *Server) handleShareSkillStatus(w http.ResponseWriter, r *http.Request) {
	st, err := shareskill.Inspect()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleShareSkillInstall(w http.ResponseWriter, r *http.Request) {
	agents, ok := parseShareSkillAgents(w, r)
	if !ok {
		return
	}
	st, err := shareskill.InstallMany(agents)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "shareskill.installed", u.Username, s.clientIP(r), joinShareSkillAgents(agents))
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleShareSkillUninstall(w http.ResponseWriter, r *http.Request) {
	agents, ok := parseShareSkillAgents(w, r)
	if !ok {
		return
	}
	st, err := shareskill.UninstallMany(agents)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "shareskill.removed", u.Username, s.clientIP(r), joinShareSkillAgents(agents))
	}
	writeJSON(w, http.StatusOK, st)
}

// parseShareSkillAgents reads target agents from either ?agent=... or a JSON
// body ({"agent":"..."} or {"agents":[...]}). Refuses empty or unrecognised
// names rather than guessing which home directory to write into.
func parseShareSkillAgents(w http.ResponseWriter, r *http.Request) ([]shareskill.Agent, bool) {
	var raw []string
	if q := strings.TrimSpace(r.URL.Query().Get("agent")); q != "" {
		raw = append(raw, q)
	}
	if r.Body != nil {
		var body struct {
			Agent  string   `json:"agent"`
			Agents []string `json:"agents"`
		}
		// Allow an empty body when ?agent= was already given on the query string.
		if err := decodeOptionalJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return nil, false
		}
		if strings.TrimSpace(body.Agent) != "" {
			raw = append(raw, body.Agent)
		}
		raw = append(raw, body.Agents...)
	}
	if len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, "agent is required; want "+validShareSkillAgentList())
		return nil, false
	}

	seen := map[shareskill.Agent]bool{}
	var out []shareskill.Agent
	add := func(a shareskill.Agent) {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, token := range raw {
		for _, part := range strings.Split(token, ",") {
			p := strings.ToLower(strings.TrimSpace(part))
			if p == "" {
				continue
			}
			if p == "all" {
				for _, a := range shareskill.Agents {
					add(a)
				}
				continue
			}
			a, ok := shareskill.ParseAgent(p)
			if !ok {
				writeErr(w, http.StatusBadRequest, "unknown agent "+p+"; want "+validShareSkillAgentList())
				return nil, false
			}
			add(a)
		}
	}
	if len(out) == 0 {
		writeErr(w, http.StatusBadRequest, "agent is required; want "+validShareSkillAgentList())
		return nil, false
	}
	return out, true
}

func decodeOptionalJSON(r *http.Request, dst any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	if err := decodeStrictJSON(b, dst); err != nil {
		return err
	}
	return nil
}

func decodeStrictJSON(b []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}

func validShareSkillAgentList() string {
	names := make([]string, len(shareskill.Agents))
	for i, a := range shareskill.Agents {
		names[i] = string(a)
	}
	return strings.Join(names, ", ")
}

func joinShareSkillAgents(agents []shareskill.Agent) string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = string(a)
	}
	return strings.Join(names, ",")
}
