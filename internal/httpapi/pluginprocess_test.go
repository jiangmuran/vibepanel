package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// A process plugin whose command is whatever the test puts in cmd.sh.
// The process declares the first capability only; the plugin as a whole
// asks for all of them, so a token's narrowing can be seen.
func processManifest(caps []string) string {
	raw, _ := json.Marshal(caps)
	own := "[]"
	if len(caps) > 0 {
		b, _ := json.Marshal(caps[:1])
		own = string(b)
	}
	return `{
  "plugin": 1, "id": "proc", "name": {"en": "Proc"}, "version": "1.0.0",
  "process": {"command": ["sh", "cmd.sh"], "capabilities": ` + own + `, "env": ["GREETING"]},
  "capabilities": ` + string(raw) + `
}`
}

func installProcess(t *testing.T, ts *httptest.Server, srv *Server, caps []string, script string) {
	t.Helper()
	status, body := postZip(t, ts, pluginZip(t, processManifest(caps), map[string]string{"cmd.sh": script}))
	if status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	raw, _ := json.Marshal(caps)
	if status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/proc/install", `{"caps": `+string(raw)+`}`); status != http.StatusOK {
		t.Fatalf("install: %d %s", status, body)
	}
	if status, _ := doJSON(t, ts, http.MethodPut, "/api/settings/plugins/proc/secrets/GREETING", `{"value": "hello-from-the-owner"}`); status != http.StatusNoContent {
		t.Fatalf("secret: %d", status)
	}
	if status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/proc/enable", `{}`); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, body)
	}
	srv.serviceCtx = t.Context()
	srv.ensurePluginProcesses(t.Context())
}

func processStatus(t *testing.T, ts *httptest.Server) pluginProcessStatus {
	t.Helper()
	_, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/proc/process", "")
	var out pluginProcessStatus
	_ = json.Unmarshal(body, &out)
	return out
}

func waitFor(t *testing.T, what string, ms int, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s did not happen in %dms", what, ms)
}

// The process starts from the version's checked-out directory with a
// cleared environment, its token, its state directory and the secret it
// declared; its output lands in the ring; the token reaches the v1 API
// narrowed to the process's own list.
func TestAProcessStartsWithItsTokenAndSecret(t *testing.T) {
	ts, srv := newTestServer(t)
	installProcess(t, ts, srv, []string{"read:panel", "read:notes"},
		"echo greeting=$GREETING; echo state=$VIBEPANEL_PLUGIN_STATE; echo url=$VIBEPANEL_PLUGIN_URL > $VIBEPANEL_PLUGIN_STATE/url; echo pwd=$PWD; env | grep -c VIBEPANEL; sleep 60")
	// Wait for what the script prints, not for Running: the status says
	// running the instant the process is spawned, and its first lines arrive
	// through the output pump a moment later. On one CPU that moment is
	// most of the time (29 of 30 runs under taskset -c 0 read an empty ring).
	waitFor(t, "the process to print", 5000, func() bool {
		st := processStatus(t, ts)
		return st.Running && strings.Contains(st.Output, "pwd=")
	})
	st := processStatus(t, ts)
	if !strings.Contains(st.Output, "greeting=hello-from-the-owner") {
		t.Errorf("the secret did not reach the environment:\n%s", st.Output)
	}
	stateDir := filepath.Join(srv.Cfg.DataDir, "plugins", "proc", "state")
	if !strings.Contains(st.Output, "state="+stateDir) {
		t.Errorf("state dir:\n%s", st.Output)
	}
	if !strings.Contains(st.Output, "pwd="+filepath.Join(srv.Cfg.DataDir, "plugins", "proc", "v1")) {
		t.Errorf("not started from the checked-out version:\n%s", st.Output)
	}
	if _, err := os.Stat(filepath.Join(srv.Cfg.DataDir, "plugins", "proc", "v1", "cmd.sh")); err != nil {
		t.Errorf("the version was not checked out: %v", err)
	}
	// The token in the environment is a credential on the plugin API and
	// nowhere else, holding read:panel (declared by the process) and not
	// read:notes (granted, but not in the process's list).
	raw, err := os.ReadFile(filepath.Join(stateDir, "url"))
	if err != nil {
		t.Fatal(err)
	}
	url := strings.TrimSpace(strings.TrimPrefix(string(raw), "url="))
	api := url[strings.Index(url, "/api/"):]
	status, body := anonJSON(t, ts, http.MethodGet, api+"view", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"caps":["read:panel"]`) {
		t.Errorf("the token's view: %d %s", status, body)
	}
	if status, _ := anonJSON(t, ts, http.MethodGet, api+"projects/0123456789abcdef/notes", ""); status != http.StatusForbidden {
		t.Errorf("read:notes through a process token that did not declare it: %d", status)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(api, "/api/plugin/"), "/v1/")
	client := anonymousClient(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, _ := client.Do(req)
	if res != nil {
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("a process token as a bearer on the panel: %d", res.StatusCode)
		}
	}
	// Disabling stops it and ends the token.
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/proc/disable", `{}`)
	waitFor(t, "the process to stop", 8000, func() bool { return !processStatus(t, ts).Running })
	if status, _ := anonJSON(t, ts, http.MethodGet, api+"me", ""); status != http.StatusUnauthorized {
		t.Errorf("the token after disabling: %d", status)
	}
}

// A process that crashes in a loop is restarted with backoff and stopped at
// the cap, audited; the owner's restart clears the stop.
func TestACrashLoopIsStoppedAndReported(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.ppr.restartMin, srv.ppr.restartMax, srv.ppr.failureCap = 5*time.Millisecond, 20*time.Millisecond, 4
	installProcess(t, ts, srv, nil, "echo failing; exit 3")
	waitFor(t, "the cap", 5000, func() bool { return processStatus(t, ts).Stopped })
	st := processStatus(t, ts)
	if st.Running || !strings.Contains(st.StopWhy, "crashed 4 times") || st.LastExit != "exited 3" {
		t.Errorf("after the cap: %+v", st)
	}
	_, body := doJSON(t, ts, http.MethodGet, "/api/settings/audit", "")
	if !strings.Contains(string(body), `"plugin.crashed"`) {
		t.Errorf("not audited: %s", body)
	}
	// Fix the script (a new version), restart: it runs again.
	status, body := postZip(t, ts, pluginZip(t, processManifest(nil), map[string]string{"cmd.sh": "echo fixed; sleep 60"}))
	if status != http.StatusOK {
		t.Fatalf("new version: %d %s", status, body)
	}
	doJSON(t, ts, http.MethodPost, "/api/settings/plugins/proc/install", `{"caps": []}`)
	status, body = doJSON(t, ts, http.MethodPost, "/api/settings/plugins/proc/process/restart", `{}`)
	if status != http.StatusOK {
		t.Fatalf("restart: %d %s", status, body)
	}
	waitFor(t, "the fixed process", 5000, func() bool {
		st := processStatus(t, ts)
		return st.Running && strings.Contains(st.Output, "fixed")
	})
}

// A process that never exits is ended at shutdown, and one that prints in a
// loop fills a ring and not the panel.
func TestAProcessIsEndedAtShutdownAndItsOutputIsBounded(t *testing.T) {
	ts, srv := newTestServer(t)
	installProcess(t, ts, srv, nil, "trap '' TERM; i=0; while :; do i=$((i+1)); echo line $i xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; done")
	waitFor(t, "output", 5000, func() bool { return len(processStatus(t, ts).Output) > 10000 })
	time.Sleep(200 * time.Millisecond)
	st := processStatus(t, ts)
	// Bounded, and the newest bytes: the ring holds the tail, whose last
	// line is whatever arrived last and may be cut mid-word.
	if len(st.Output) > pluginOutputRing+1 || !strings.Contains(st.Output, "line ") {
		t.Errorf("ring is %d bytes", len(st.Output))
	}
	pid := st.PID
	start := time.Now()
	srv.StopPluginProcesses(t.Context())
	if time.Since(start) > pluginStopGrace+3*time.Second {
		t.Errorf("shutdown took %v", time.Since(start))
	}
	// The process ignored SIGTERM; the SIGKILL after the grace ended it.
	waitFor(t, "the process to be gone", 3000, func() bool {
		return syscallKillProbe(pid) != nil
	})
	if _, err := os.Stat(filepath.Join(srv.Cfg.DataDir, "plugins", "proc", "state")); err != nil {
		t.Errorf("the state directory was not made: %v", err)
	}
}

// The three red capabilities: a plugin with them restarts, ends, starts and
// types into sessions through the panel's own handlers, by handle, and one
// without them is refused.
func TestTheRedCapabilitiesReachSessionsByHandle(t *testing.T) {
	ts, srv := newTestServer(t)
	dir := t.TempDir()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+dir+`","name":"controlled"}`)
	g := installPane(t, ts, []string{"read:panel", "sessions:create", "sessions:input", "sessions:control"})
	ph := srv.pluginHandle(t.Context(), "pane", project.ID)
	status, body := anonJSON(t, ts, http.MethodPost, g.API+"sessions", `{"project": "`+ph+`", "title": "from a plugin", "command": ["sleep", "60"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var made pluginViewSession
	_ = json.Unmarshal(body, &made)
	if len(made.ID) != 16 || made.ProjectID != ph || made.Name != "from a plugin" {
		t.Errorf("made = %+v", made)
	}
	if strings.Contains(string(body), project.ID) {
		t.Error("the answer carried the real project id")
	}
	sessions, _ := srv.DB.ListSessions(t.Context())
	if len(sessions) != 1 || sessions[0].Title != "from a plugin" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if status, _ := anonJSON(t, ts, http.MethodPost, g.API+"sessions/"+made.ID+"/input", `{"text": "echo hi", "submit": true}`); status != http.StatusNoContent {
		t.Errorf("input: %d", status)
	}
	if status, _ := anonJSON(t, ts, http.MethodPost, g.API+"sessions/"+made.ID+"/restart", ""); status == http.StatusForbidden || status == http.StatusUnauthorized {
		t.Errorf("restart with sessions:control: %d", status)
	}
	if status, _ := anonJSON(t, ts, http.MethodDelete, g.API+"sessions/"+made.ID, ""); status != http.StatusNoContent {
		t.Errorf("delete: %d", status)
	}
	if sessions, _ := srv.DB.ListSessions(t.Context()); len(sessions) != 0 {
		t.Errorf("the session was not ended: %+v", sessions)
	}
	_, body = doJSON(t, ts, http.MethodGet, "/api/settings/audit", "")
	if !strings.Contains(string(body), `"plugin.input"`) {
		t.Errorf("input not audited: %s", body)
	}
	// Untick the red ones: refused.
	doJSON(t, ts, http.MethodPut, "/api/settings/plugins/pane/caps", `{"caps": ["read:panel"]}`)
	if status, _ := anonJSON(t, ts, http.MethodPost, g.API+"sessions", `{"project": "`+ph+`", "command": ["sleep", "60"]}`); status != http.StatusForbidden {
		t.Errorf("create without sessions:create: %d", status)
	}
}

// syscallKillProbe is kill -0: nil while the process exists.
func syscallKillProbe(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.Signal(0))
}
