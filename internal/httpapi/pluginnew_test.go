package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// New plugin (docs/plugins.md §9): a name and a template become a directory
// that is a plugin already running in dev mode, with a project at it. Every
// template is taken through the real route, and the service one is then
// installed and called, so the template an author starts from is one the
// panel has actually run.
func TestNewPluginScaffoldsRegistersAndRunsInDev(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := t.Context()
	for _, tpl := range []string{"theme", "pane", "service", "process", "full"} {
		t.Run(tpl, func(t *testing.T) {
			status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugin-new",
				`{"name":"Try `+tpl+`","template":"`+tpl+`"}`)
			if status != http.StatusCreated {
				t.Fatalf("new: %d %s", status, body)
			}
			var out struct {
				Plugin    PluginDetail `json:"plugin"`
				ProjectID string       `json:"projectId"`
				Dir       string       `json:"dir"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if out.Plugin.ID != "try-"+tpl || !out.Plugin.Dev || out.Plugin.SourceDir != out.Dir {
				t.Errorf("plugin = id %q dev %v source %q, dir %q", out.Plugin.ID, out.Plugin.Dev, out.Plugin.SourceDir, out.Dir)
			}
			want := filepath.Join(srv.Cfg.DataDir, "plugins", "dev", "plugin-try-"+tpl)
			if out.Dir != want {
				t.Errorf("dir = %q, want %q", out.Dir, want)
			}
			for _, f := range []string{"AGENTS.md", "plugin.json", "vibepanel-plugin.d.ts"} {
				if _, err := os.Stat(filepath.Join(out.Dir, f)); err != nil {
					t.Errorf("missing %s", f)
				}
			}
			projects, _ := srv.DB.ListProjects(ctx)
			found := false
			for _, p := range projects {
				if p.ID == out.ProjectID && p.Path == out.Dir && p.Name == "plugin-try-"+tpl {
					found = true
				}
			}
			if !found {
				t.Errorf("no project %q at %s", out.ProjectID, out.Dir)
			}
		})
	}

	// The service template, installed with what it asks for, answers its
	// own route from the draft directory.
	status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/try-service/install", `{"caps":["read:panel"]}`)
	if status != http.StatusOK {
		t.Fatalf("install: %d %s", status, body)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/ext/try-service/summary", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"sessions":0`) {
		t.Fatalf("summary: %d %s", status, body)
	}

	// The same name again is a second plugin, not a clobbered one.
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugin-new", `{"name":"Try pane","template":"pane"}`)
	if status != http.StatusConflict {
		t.Fatalf("the same name twice: %d %s", status, body)
	}

	// Pointed at a directory with somebody's files in it: refused, nothing written.
	mine := t.TempDir()
	if err := os.WriteFile(filepath.Join(mine, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugin-new",
		`{"name":"Other","template":"pane","path":"`+mine+`"}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "not empty") {
		t.Fatalf("non-empty dir: %d %s", status, body)
	}
	if _, err := os.Stat(filepath.Join(mine, "plugin.json")); err == nil {
		t.Error("plugin.json was written beside the person's file")
	}
	if status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugin-new", `{"name":"Other","template":"nope"}`); status != http.StatusBadRequest {
		t.Fatalf("unknown template: %d %s", status, body)
	}
	if status, _ = doJSON(t, ts, http.MethodPost, "/api/settings/plugin-new", `{"name":"  ","template":"pane"}`); status != http.StatusBadRequest {
		t.Fatalf("empty name: %d", status)
	}
}

// The gallery is the templates the build carries, with their rungs.
func TestPluginTemplatesAreListed(t *testing.T) {
	ts, _ := newTestServer(t)
	status, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugin-templates", "")
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, body)
	}
	var list []struct {
		ID    string          `json:"id"`
		Rungs map[string]bool `json:"rungs"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 || list[0].ID != "theme" || !list[0].Rungs["theme"] {
		t.Fatalf("templates: %s", body)
	}
}
