package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/shareskill"
)

func TestShareSkillEndpointsInstallRemoveAndAudit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("DSH_HOME", "")

	ts, _ := newTestServer(t)
	c := ts.Client()

	// GET starts cleanly uninstalled across all four agents.
	resp, err := c.Get(ts.URL + "/api/settings/share-skill")
	if err != nil {
		t.Fatal(err)
	}
	var st shareskill.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	if len(st.Agents) != 4 {
		t.Fatalf("expected 4 agents, got %d", len(st.Agents))
	}
	for _, a := range st.Agents {
		if a.Installed || a.Stale {
			t.Fatalf("agent %s should start uninstalled: %+v", a.ID, a)
		}
	}

	// Bad agent or missing agent is rejected with 400 without writing anything.
	for _, badURL := range []string{
		ts.URL + "/api/settings/share-skill",
		ts.URL + "/api/settings/share-skill?agent=unknown",
	} {
		req, _ := http.NewRequest(http.MethodPost, badURL, nil)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("POST %s status = %d, want 400", badURL, r.StatusCode)
		}
	}

	// Install agy and dsh via JSON body.
	body := bytes.NewBufferString(`{"agents":["agy","dsh"]}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/share-skill", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST install status = %d, want 200", resp.StatusCode)
	}
	byID := map[shareskill.Agent]shareskill.AgentStatus{}
	for _, a := range st.Agents {
		byID[a.ID] = a
	}
	if !byID[shareskill.AgentAgy].Installed || !byID[shareskill.AgentDsh].Installed {
		t.Fatalf("expected agy and dsh installed: %+v", byID)
	}
	if byID[shareskill.AgentClaude].Installed || byID[shareskill.AgentCodex].Installed {
		t.Fatalf("claude and codex should still be uninstalled: %+v", byID)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "config", "plugins", shareskill.SkillName, "plugin.json")); err != nil {
		t.Fatalf("agy plugin.json missing: %v", err)
	}

	// Remove dsh via query param.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/settings/share-skill?agent=dsh", nil)
	resp, err = c.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", resp.StatusCode)
	}
	for _, a := range st.Agents {
		byID[a.ID] = a
	}
	if byID[shareskill.AgentDsh].Installed || !byID[shareskill.AgentAgy].Installed {
		t.Fatalf("after removing dsh, got agy=%v dsh=%v", byID[shareskill.AgentAgy].Installed, byID[shareskill.AgentDsh].Installed)
	}

	// Verify both audit events were recorded.
	auditResp, err := c.Get(ts.URL + "/api/settings/audit")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Event  string `json:"event"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(auditResp.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	auditResp.Body.Close()
	var sawInstall, sawRemove bool
	for _, e := range entries {
		if e.Event == "shareskill.installed" && strings.Contains(e.Detail, "agy") {
			sawInstall = true
		}
		if e.Event == "shareskill.removed" && e.Detail == "dsh" {
			sawRemove = true
		}
	}
	if !sawInstall || !sawRemove {
		t.Fatalf("missing expected audit events: install=%v remove=%v entries=%+v", sawInstall, sawRemove, entries)
	}
}
