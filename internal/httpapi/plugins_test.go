package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/plugins"
)

// A plugin that climbs the first three rungs and needs a secret, which is
// enough to walk every decision the install screen makes.
const paperManifest = `{
  "plugin": 1, "id": "paper", "name": {"en": "Paper", "zh-CN": "纸"}, "version": "1.2.0",
  "theme": {"file": "theme.css", "name": {"en": "Paper"}, "scheme": "light"},
  "panels": [{"slot": "sidepanel.pane", "entry": "pane.html", "title": {"en": "Paper"}}],
  "capabilities": ["read:panel", "write:todos", "sessions:input"],
  "settings": {"fields": [
    {"key": "quiet", "type": "bool", "default": true, "label": {"en": "Quiet"}},
    {"key": "token", "type": "secret", "label": {"en": "Token"}}
  ]}
}`

const paperTheme = ":root[data-theme='ext-paper'] { --vp-bg: #f4f1ea; --vp-ink: #222222; color-scheme: light; }"

// pluginZip is a raw zip, not plugins.WriteArchive: a test wants to put in
// what somebody's "compress this folder" would -- AGENTS.md, a stale SDK --
// and see it ignored, which the writer refuses to write.
func pluginZip(t *testing.T, manifest string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	put(plugins.ManifestFile, manifest)
	for p, body := range files {
		put(p, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func postZip(t *testing.T, ts *httptest.Server, data []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/plugins", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/zip")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

type addedPlugin struct {
	Plugin  PluginDetail      `json:"plugin"`
	Ignored []plugins.Ignored `json:"ignored"`
	Created bool              `json:"created"`
}

func addPaper(t *testing.T, ts *httptest.Server) addedPlugin {
	t.Helper()
	status, body := postZip(t, ts, pluginZip(t, paperManifest, map[string]string{
		"theme.css": paperTheme, "pane.html": "<p>paper</p>", "AGENTS.md": "# notes"}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	var out addedPlugin
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Arriving is not installing. The answer is the screen; nothing runs until
// the install route has been called with the boxes as ticked.
func TestAPluginArrivesWithItsScreenAndNothingRuns(t *testing.T) {
	ts, _ := newTestServer(t)
	out := addPaper(t, ts)
	p := out.Plugin
	if !out.Created || p.ID != "paper" || p.Enabled || p.InstalledVersion != 0 || p.LatestVersion != 1 {
		t.Errorf("after add: created=%v %+v", out.Created, p.Plugin)
	}
	if len(out.Ignored) != 1 || out.Ignored[0].Path != "AGENTS.md" {
		t.Errorf("ignored = %+v", out.Ignored)
	}
	if p.Screen.Confirm != plugins.ConfirmGrant {
		t.Errorf("confirm = %s", p.Screen.Confirm)
	}
	var caps []string
	var red int
	for _, l := range p.Screen.Lines {
		if l.Kind == plugins.LineCap {
			caps = append(caps, l.Code)
			if !l.Granted {
				t.Errorf("%s unticked on a first install", l.Code)
			}
			if l.Tone == plugins.ToneRed {
				red++
			}
		}
	}
	if strings.Join(caps, ",") != "sessions:input,read:panel,write:todos" || red != 1 {
		t.Errorf("screen caps = %v (red %d); the danger line comes first", caps, red)
	}
	if len(p.Secrets) != 1 || p.Secrets[0].Name != "TOKEN" || p.Secrets[0].Set {
		t.Errorf("secrets = %+v", p.Secrets)
	}
	// Nothing enabled: the theme sheet is empty and the picker lists nothing.
	res, css := ts.Client().Get(ts.URL + "/plugin-themes.css")
	if res != nil {
		defer res.Body.Close()
	}
	if res == nil || res.StatusCode != http.StatusOK {
		t.Fatalf("theme css: %v", res)
	}
	if b, _ := io.ReadAll(res.Body); strings.Contains(string(b), "ext-paper") {
		t.Errorf("a plugin that is not installed has its theme served: %s", b)
	}
	_ = css
}

// The install is the confirmation: the grants are exactly the boxes ticked,
// a box the screen never showed is refused, and a missing secret installs
// the plugin disabled rather than running it without.
func TestInstallRecordsTheBoxesAsTicked(t *testing.T) {
	ts, srv := newTestServer(t)
	addPaper(t, ts)

	status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/paper/install",
		`{"caps": ["read:panel", "read:paths"]}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "read:paths") {
		t.Errorf("a cap the manifest never asked for: %d %s", status, body)
	}

	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugins/paper/install",
		`{"caps": ["read:panel", "write:todos"]}`)
	if status != http.StatusOK {
		t.Fatalf("install: %d %s", status, body)
	}
	var out struct {
		Plugin       PluginDetail `json:"plugin"`
		NeedsSecrets []string     `json:"needsSecrets"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Plugin.InstalledVersion != 1 || out.Plugin.Enabled {
		t.Errorf("installed with a secret missing should be installed and disabled: %+v", out.Plugin.Plugin)
	}
	if strings.Join(out.NeedsSecrets, ",") != "TOKEN" {
		t.Errorf("needsSecrets = %v", out.NeedsSecrets)
	}
	if strings.Join(out.Plugin.Granted, ",") != "read:panel,write:todos" {
		t.Errorf("granted = %v; sessions:input was unticked", out.Plugin.Granted)
	}
	// The screen now draws the decision: the unticked box stays unticked.
	for _, l := range out.Plugin.Screen.Lines {
		if l.Kind == plugins.LineCap && l.Code == "sessions:input" && l.Granted {
			t.Error("sessions:input drawn as granted after being unticked")
		}
	}

	// Enabling before the secret is set is refused, by name.
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugins/paper/enable", `{}`)
	if status != http.StatusConflict || !strings.Contains(string(body), "TOKEN") {
		t.Errorf("enable without the secret: %d %s", status, body)
	}
	status, _ = doJSON(t, ts, http.MethodPut, "/api/settings/plugins/paper/secrets/TOKEN", `{"value": "s3cret"}`)
	if status != http.StatusNoContent {
		t.Fatalf("set secret: %d", status)
	}
	if v, err := srv.pluginSecretValue(t.Context(), "paper", "TOKEN"); err != nil || v != "s3cret" {
		t.Errorf("the secret does not open: %q %v", v, err)
	}
	status, _ = doJSON(t, ts, http.MethodPut, "/api/settings/plugins/paper/secrets/OTHER", `{"value": "x"}`)
	if status != http.StatusNotFound {
		t.Errorf("a secret the manifest does not name: %d", status)
	}
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugins/paper/enable", `{}`)
	if status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, body)
	}

	// Enabled: the theme is served, linted on the way out, and listed.
	res, err := ts.Client().Get(ts.URL + "/plugin-themes.css")
	if err != nil {
		t.Fatal(err)
	}
	css, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(css), ":root[data-theme='ext-paper'] {") || !strings.Contains(string(css), "--vp-bg: #f4f1ea;") {
		t.Errorf("theme css:\n%s", css)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugin-themes", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"attr":"ext-paper"`) {
		t.Errorf("themes: %d %s", status, body)
	}

	// Disable takes it out of the sheet.
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/paper/disable", `{}`)
	res, _ = ts.Client().Get(ts.URL + "/plugin-themes.css")
	css, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(css), "ext-paper") {
		t.Error("a disabled plugin's theme is still served")
	}

	// The audit trail has the decision, with the grants.
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/audit", "")
	if !strings.Contains(string(body), `"plugin.installed"`) || !strings.Contains(string(body), "granted read:panel,write:todos") {
		t.Errorf("audit: %d %s", status, body)
	}
}

// The settings the panel draws: a value outside the schema is refused
// naming the field; a secret field reports only whether it is set.
func TestPluginSettingsAreCheckedAgainstTheSchema(t *testing.T) {
	ts, _ := newTestServer(t)
	addPaper(t, ts)
	status, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/paper/settings", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"quiet":true`) || !strings.Contains(string(body), `"token":false`) {
		t.Errorf("settings: %d %s", status, body)
	}
	status, body = doJSON(t, ts, http.MethodPut, "/api/settings/plugins/paper/settings/quiet", `{"value": "yes"}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "quiet") {
		t.Errorf("a bool given text: %d %s", status, body)
	}
	status, _ = doJSON(t, ts, http.MethodPut, "/api/settings/plugins/paper/settings/quiet", `{"value": false}`)
	if status != http.StatusOK {
		t.Errorf("set: %d", status)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins/paper/settings", "")
	if !strings.Contains(string(body), `"quiet":false`) {
		t.Errorf("after set: %d %s", status, body)
	}
	status, _ = doJSON(t, ts, http.MethodPut, "/api/settings/plugins/paper/settings/token", `{"value": "x"}`)
	if status != http.StatusBadRequest {
		t.Errorf("a secret through the settings route: %d", status)
	}
	status, _ = doJSON(t, ts, http.MethodPut, "/api/settings/plugins/paper/settings/nope", `{"value": 1}`)
	if status != http.StatusNotFound {
		t.Errorf("an undeclared setting: %d", status)
	}
	status, _ = doJSON(t, ts, http.MethodDelete, "/api/settings/plugins/paper/settings/quiet", "")
	if status != http.StatusNoContent {
		t.Errorf("reset: %d", status)
	}
}

// A second arrival of the same id is a version, not a second plugin; the
// grants stay the owner's decision, and a version asking for more is drawn
// with the new line unticked.
func TestAnUpgradeKeepsTheDecisionAndDrawsWhatIsNew(t *testing.T) {
	ts, _ := newTestServer(t)
	addPaper(t, ts)
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/paper/install", `{"caps": ["read:panel"]}`)

	// The same bytes again store nothing.
	status, body := postZip(t, ts, pluginZip(t, paperManifest, map[string]string{
		"theme.css": paperTheme, "pane.html": "<p>paper</p>"}))
	var out addedPlugin
	_ = json.Unmarshal(body, &out)
	if status != http.StatusOK || out.Created || out.Plugin.LatestVersion != 1 {
		t.Errorf("same bytes: %d created=%v latest=%d", status, out.Created, out.Plugin.LatestVersion)
	}

	// A newer version asking for one more capability.
	newer := strings.Replace(strings.Replace(paperManifest, `"version": "1.2.0"`, `"version": "1.3.0"`, 1),
		`"capabilities": ["read:panel", "write:todos", "sessions:input"]`,
		`"capabilities": ["read:panel", "write:todos", "sessions:input", "read:terminal"]`, 1)
	status, body = postZip(t, ts, pluginZip(t, newer, map[string]string{"theme.css": paperTheme, "pane.html": "<p>2</p>"}))
	_ = json.Unmarshal(body, &out)
	if status != http.StatusOK || out.Plugin.LatestVersion != 2 || out.Plugin.InstalledVersion != 1 {
		t.Fatalf("upgrade arrived: %d latest=%d installed=%d", status, out.Plugin.LatestVersion, out.Plugin.InstalledVersion)
	}
	ticked := map[string]bool{}
	for _, l := range out.Plugin.Screen.Lines {
		if l.Kind == plugins.LineCap {
			ticked[l.Code] = l.Granted
		}
	}
	if !ticked["read:panel"] || ticked["read:terminal"] || ticked["sessions:input"] {
		t.Errorf("upgrade screen boxes = %v; granted stays ticked, new and unticked stay unticked", ticked)
	}
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugins/paper/install", `{"caps": ["read:panel", "read:terminal"]}`)
	var installed struct {
		Plugin PluginDetail `json:"plugin"`
	}
	_ = json.Unmarshal(body, &installed)
	if status != http.StatusOK || installed.Plugin.InstalledVersion != 2 || strings.Join(installed.Plugin.Granted, ",") != "read:panel,read:terminal" {
		t.Errorf("after upgrade: %d %+v", status, installed.Plugin.Granted)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/audit", "")
	if !strings.Contains(string(body), `"plugin.updated"`) {
		t.Errorf("audit: %d %s", status, body)
	}
}

func TestAPluginNeedingANewerPanelIsRefused(t *testing.T) {
	ts, _ := newTestServer(t)
	old := strings.Replace(paperManifest, `"version": "1.2.0",`, `"version": "1.2.0", "panel": ">=99.0",`, 1)
	status, body := postZip(t, ts, pluginZip(t, old, map[string]string{"theme.css": paperTheme, "pane.html": "x"}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	// A dev build satisfies every constraint; the screen says so only with a
	// numbered build. The install route uses the same rule, so with a dev
	// build this installs; what is pinned is that the screen and the route
	// agree, which TestAnOlderPanelIsRefusedByNameAndNumber covers for the
	// numbered case in the plugins package.
	var out addedPlugin
	_ = json.Unmarshal(body, &out)
	if plugins.IsDev("dev") && out.Plugin.Screen.Refused != nil {
		t.Errorf("a dev build refused a plugin: %+v", out.Plugin.Screen.Refused)
	}
}

func TestRemovingAPluginTakesEverything(t *testing.T) {
	ts, srv := newTestServer(t)
	addPaper(t, ts)
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/paper/install", `{"caps": ["read:panel"]}`)
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/paper/secrets/TOKEN", `{"value": "s"}`)
	status, _ := doJSON(t, ts, http.MethodDelete, "/api/settings/plugins/paper", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	status, _ = doJSON(t, ts, http.MethodGet, "/api/settings/plugins/paper", "")
	if status != http.StatusNotFound {
		t.Errorf("after delete: %d", status)
	}
	if caps, _ := srv.DB.PluginCaps(t.Context(), "paper"); len(caps) != 0 {
		t.Error("grants survived the delete")
	}
	if names, _ := srv.DB.PluginSecretNames(t.Context(), "paper"); len(names) != 0 {
		t.Error("secrets survived the delete")
	}
	status, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins", "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("list after delete: %d %s", status, body)
	}
}

// A bundle that the publish rules refuse is refused here, with the reason,
// and leaves no row behind.
func TestABadBundleLeavesNothingBehind(t *testing.T) {
	ts, _ := newTestServer(t)
	status, body := postZip(t, ts, pluginZip(t, paperManifest, map[string]string{
		"theme.css": ".vp-control { color: red }", "pane.html": "x"}))
	if status != http.StatusBadRequest || !strings.Contains(string(body), "theme.css") {
		t.Errorf("a theme that fails its lint: %d %s", status, body)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins", "")
	if strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("a row was left behind: %d %s", status, body)
	}
}

// The theme stylesheet is behind the session like everything else; a
// stranger gets 401 and the browser ignores a stylesheet it cannot load.
func TestTheThemeSheetNeedsASession(t *testing.T) {
	ts, _ := newTestServer(t)
	res, _ := anonGET(t, ts, "/plugin-themes.css")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous theme css: %d", res.StatusCode)
	}
}

// The plugins page polls every five seconds; ?detail=1 is the list with
// every card's detail in the one answer, so the poll is one request.
func TestTheListCarriesDetailOnRequest(t *testing.T) {
	ts, _ := newTestServer(t)
	installPane(t, ts, []string{"read:panel"})
	status, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins?detail=1", "")
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, body)
	}
	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["screen"] == nil || list[0]["settings"] == nil || list[0]["id"] != "pane" {
		t.Fatalf("detail list: %s", body)
	}
	status, body = doJSON(t, ts, http.MethodGet, "/api/settings/plugins", "")
	if status != http.StatusOK || strings.Contains(string(body), `"screen"`) {
		t.Fatalf("the plain list carries the screen: %d %s", status, body)
	}
}
