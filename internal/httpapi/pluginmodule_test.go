package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const moduleManifest = `{
  "plugin": 1, "id": "mod", "name": {"en": "Mod"}, "version": "1.0.0",
  "unsandboxed": {"entry": "main.mjs", "tested": "0.0.0 - 99.0.x"}
}`

// A module is served only behind the switch, only when installed and
// enabled, only from the installed version, only under the session.
func TestAModuleIsServedOnlyBehindTheSwitch(t *testing.T) {
	ts, _ := newTestServer(t)
	status, body := postZip(t, ts, pluginZip(t, moduleManifest, map[string]string{"main.mjs": "export default function (h) { h.v }"}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	// The screen says it in red and the button says run.
	if !strings.Contains(string(body), `"confirm":"run"`) || !strings.Contains(string(body), `"kind":"danger"`) {
		t.Errorf("the screen for a module: %s", body)
	}
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/mod/install", `{"caps": []}`)

	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugin-modules", "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("modules with the switch off: %d %s", status, body)
	}
	if status, _ := doJSON(t, ts, http.MethodGet, "/plugin-code/mod/main.mjs", ""); status != http.StatusForbidden {
		t.Errorf("code with the switch off: %d", status)
	}
	doJSON(t, ts, http.MethodPut, "/api/settings/plugin-unsandboxed", `{"enabled": true}`)
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugin-modules", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"url":"/plugin-code/mod/main.mjs"`) || !strings.Contains(string(body), `"within":true`) {
		t.Errorf("modules with the switch on: %d %s", status, body)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/plugin-code/mod/main.mjs", "")
	if status != http.StatusOK || !strings.Contains(string(body), "export default") {
		t.Errorf("code: %d %s", status, body)
	}
	// A stranger: 401, switch or no switch.
	if status, _ := anonJSON(t, ts, http.MethodGet, "/plugin-code/mod/main.mjs", ""); status != http.StatusUnauthorized {
		t.Errorf("anonymous code: %d", status)
	}
	// Disabled: gone from the list and the route.
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/mod/disable", `{}`)
	if status, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugin-modules", ""); strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("modules of a disabled plugin: %d %s", status, body)
	}
	if status, _ := doJSON(t, ts, http.MethodGet, "/plugin-code/mod/main.mjs", ""); status != http.StatusNotFound {
		t.Errorf("code of a disabled plugin: %d", status)
	}
	// The audit says who flipped the switch.
	_, body = doJSON(t, ts, http.MethodGet, "/api/settings/audit", "")
	if !strings.Contains(string(body), `"plugins.unsandboxed"`) {
		t.Errorf("the switch is not audited: %s", body)
	}
}

// Dev mode never serves a draft's module: code that runs as the owner comes
// from an installed version only.
func TestAModuleIsNeverServedFromADraft(t *testing.T) {
	ts, _ := newTestServer(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(moduleManifest), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "main.mjs"), []byte("export default function () { /* draft */ }"), 0o644)
	status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins", `{"path": "`+dir+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	doJSON(t, ts, http.MethodPut, "/api/settings/plugin-unsandboxed", `{"enabled": true}`)
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/mod/dev", `{"dev": true}`)
	// Dev on, nothing installed: no module.
	if status, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugin-modules", ""); strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("a draft listed as a module: %d %s", status, body)
	}
	if status, _ := doJSON(t, ts, http.MethodGet, "/plugin-code/mod/main.mjs", ""); status != http.StatusNotFound {
		t.Errorf("a draft's code served: %d", status)
	}
	// Installed and enabled, still in dev mode: the installed file, not the
	// draft's, even after the draft changes.
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/mod/install", `{"caps": []}`)
	_ = os.WriteFile(filepath.Join(dir, "main.mjs"), []byte("export default function () { /* changed */ }"), 0o644)
	status, body = doJSON(t, ts, http.MethodGet, "/plugin-code/mod/main.mjs", "")
	if status != http.StatusOK || !strings.Contains(string(body), "draft") || strings.Contains(string(body), "changed") {
		t.Errorf("code in dev mode: %d %s", status, body)
	}
}
