package httpapi

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/headless"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// fakeHeadlessClaude is a `claude` that records its argv and environment in
// its working directory and prints canned stream-json; a prompt of "sleep"
// keeps it running until it is stopped.
func fakeHeadlessClaude(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
printf '%s\n' "$@" > "$PWD/argv.txt"
env > "$PWD/env.txt"
echo '{"type":"system","subtype":"init","session_id":"x","model":"fake-model"}'
case "$2" in
sleep)
  sleep 300 &
  wait
  ;;
*)
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"晴"}},"parent_tool_use_id":null}'
  echo '{"type":"result","subtype":"success","result":"晴","is_error":false,"total_cost_usd":0.02,"duration_ms":7,"num_turns":1}'
  ;;
esac
`
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

type headlessRig struct {
	t       *testing.T
	ts      *httptest.Server
	srv     *Server
	token   string
	project store.Project
	dir     string // the assistant directory
}

// newHeadlessRig is a signed-in server with headless enabled, an API token
// and a fake claude.
func newHeadlessRig(t *testing.T, extra string) *headlessRig {
	t.Helper()
	ts, srv := newTestServer(t)
	srv.HeadlessBinary = fakeHeadlessClaude(t)
	dir := filepath.Join(t.TempDir(), "assistant")
	body := `{"enabled":true,"assistantDir":` + strconvQuote(dir) + `,"allowedOrigins":["http://glasses.test:5173"]` + extra + `}`
	put(t, ts, "/api/settings/headless", body, http.StatusOK)
	tok := postJSON[map[string]any](t, ts, "/api/settings/tokens", `{"name":"g2"}`)
	set, err := srv.headlessSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := srv.DB.GetProject(context.Background(), set.AssistantProjectID)
	if err != nil {
		t.Fatalf("the assistant project was not made: %v", err)
	}
	return &headlessRig{t: t, ts: ts, srv: srv, token: tok["token"].(string), project: p, dir: dir}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func put(t *testing.T, ts *httptest.Server, path, body string, want int) []byte {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("PUT %s = %d, want %d: %s", path, res.StatusCode, want, b)
	}
	return b
}

// do sends a request with the API token and no cookie, from origin if given.
func (h *headlessRig) do(method, path, origin, ctype string, body io.Reader) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.ts.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+h.token)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func readAll(t *testing.T, res *http.Response) []byte {
	t.Helper()
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return b
}

// sseEvents reads a run's stream to its end.
func (h *headlessRig) sseEvents(runID, after string) []map[string]any {
	h.t.Helper()
	res := h.do(http.MethodGet, "/api/headless/runs/"+runID+"/events?after="+after, "", "", nil)
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		b, _ := io.ReadAll(res.Body)
		h.t.Fatalf("events: %d %s %s", res.StatusCode, ct, b)
	}
	var out []map[string]any
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if d, ok := strings.CutPrefix(line, "data: "); ok {
			var m map[string]any
			if err := json.Unmarshal([]byte(d), &m); err != nil {
				h.t.Fatalf("data line %q: %v", d, err)
			}
			out = append(out, m)
		}
	}
	return out
}

func TestHeadlessSettingsNeverReturnTheKey(t *testing.T) {
	ts, srv := newTestServer(t)
	var view map[string]any
	if err := json.Unmarshal(put(t, ts, "/api/settings/headless", `{"asr":{"apiKey":"sk-very-secret"}}`, http.StatusOK), &view); err != nil {
		t.Fatal(err)
	}
	if view["asr"].(map[string]any)["hasKey"] != true {
		t.Fatalf("hasKey not set: %v", view["asr"])
	}
	res, _ := ts.Client().Get(ts.URL + "/api/settings/headless")
	got := readAll(t, res)
	if strings.Contains(string(got), "sk-very-secret") {
		t.Fatalf("GET leaked the key: %s", got)
	}
	if !strings.Contains(string(got), `"defaultPermissionMode":"bypassPermissions"`) || !strings.Contains(string(got), `"enabled":false`) {
		t.Fatalf("defaults: %s", got)
	}
	raw, _ := srv.DB.GetSetting(context.Background(), headlessASRKeyKey, "")
	if raw == "" || strings.Contains(raw, "sk-very-secret") {
		t.Fatalf("the key is not sealed at rest: %q", raw)
	}
	if k, err := srv.headlessASRKey(context.Background()); err != nil || k != "sk-very-secret" {
		t.Fatalf("unseal: %q %v", k, err)
	}
	// Sending back what GET gave, plus nothing about the key: kept.
	var back map[string]any
	_ = json.Unmarshal(got, &back)
	b, _ := json.Marshal(back)
	put(t, ts, "/api/settings/headless", string(b), http.StatusOK)
	if k, _ := srv.headlessASRKey(context.Background()); k != "sk-very-secret" {
		t.Fatal("a round trip of the GET body lost the key")
	}
	out := put(t, ts, "/api/settings/headless", `{"asr":{"clearKey":true}}`, http.StatusOK)
	if !strings.Contains(string(out), `"hasKey":false`) {
		t.Fatalf("clearKey: %s", out)
	}
	put(t, ts, "/api/settings/headless", `{"defaultPermissionMode":"yolo"}`, http.StatusBadRequest)
	put(t, ts, "/api/settings/headless", `{"allowedOrigins":["*"]}`, http.StatusBadRequest)
	put(t, ts, "/api/settings/headless", `{"launchProfileId":"nope"}`, http.StatusBadRequest)
	put(t, ts, "/api/settings/headless", `{"bogus":1}`, http.StatusBadRequest)
}

func TestEnablingMakesTheWorkspaceAndOneProject(t *testing.T) {
	h := newHeadlessRig(t, "")
	if h.project.Name != "助理" || h.project.Path != h.dir {
		t.Fatalf("project %+v", h.project)
	}
	learned := []byte("# 用户档案\n\n喜欢简短回答\n")
	if err := os.WriteFile(filepath.Join(h.dir, "memories", "USER.md"), learned, 0o644); err != nil {
		t.Fatal(err)
	}
	put(t, h.ts, "/api/settings/headless", `{"enabled":true}`, http.StatusOK)
	if got, _ := os.ReadFile(filepath.Join(h.dir, "memories", "USER.md")); string(got) != string(learned) {
		t.Fatal("enabling again overwrote the memory")
	}
	list, _ := h.srv.DB.ListProjects(context.Background())
	n := 0
	for _, p := range list {
		if p.Path == h.dir {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d projects on the assistant directory", n)
	}

	res := h.do(http.MethodGet, "/api/headless/config", "", "", nil)
	var cfg map[string]any
	_ = json.Unmarshal(readAll(t, res), &cfg)
	if cfg["enabled"] != true || cfg["assistantProjectId"] != h.project.ID || cfg["asr"] != false ||
		cfg["defaultPermissionMode"] != "bypassPermissions" || len(cfg["permissionModes"].([]any)) != 5 {
		t.Fatalf("config %v", cfg)
	}

	other, _ := h.srv.DB.CreateProject(context.Background(), "other1", "other", t.TempDir())
	_ = other
	res = h.do(http.MethodGet, "/api/headless/projects", "", "", nil)
	var pl struct {
		Projects []headlessProject `json:"projects"`
	}
	_ = json.Unmarshal(readAll(t, res), &pl)
	if len(pl.Projects) != 2 || !pl.Projects[0].Assistant || pl.Projects[0].ID != h.project.ID {
		t.Fatalf("projects %+v", pl.Projects)
	}
}

func TestHeadlessIsOffUntilSwitchedOn(t *testing.T) {
	h := newHeadlessRig(t, "")
	put(t, h.ts, "/api/settings/headless", `{"enabled":false}`, http.StatusOK)
	res := h.do(http.MethodGet, "/api/headless/projects", "", "", nil)
	body := readAll(t, res)
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "headless is disabled in settings") {
		t.Fatalf("disabled: %d %s", res.StatusCode, body)
	}
	res = h.do(http.MethodGet, "/api/headless/config", "", "", nil)
	if b := readAll(t, res); res.StatusCode != 200 || !strings.Contains(string(b), `"enabled":false`) {
		t.Fatalf("config while off: %d %s", res.StatusCode, b)
	}
}

func TestAHeadlessPreflightAnswersOnlyHeaders(t *testing.T) {
	h := newHeadlessRig(t, "")
	pre := func(origin string) *http.Response {
		req, _ := http.NewRequest(http.MethodOptions, h.ts.URL+"/api/headless/runs", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	res := pre("http://glasses.test:5173")
	if res.StatusCode != http.StatusNoContent ||
		res.Header.Get("Access-Control-Allow-Origin") != "http://glasses.test:5173" ||
		!strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "Last-Event-ID") ||
		res.Header.Get("Access-Control-Allow-Methods") != "GET, POST, DELETE, OPTIONS" ||
		res.Header.Get("Access-Control-Max-Age") != "600" ||
		len(res.Header.Values("Vary")) != 1 ||
		res.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("allowed preflight: %d %v", res.StatusCode, res.Header)
	}
	res = pre("http://evil.test")
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("denied preflight: %d %v", res.StatusCode, res.Header)
	}

	// The real request from the allowed origin: through the write check, and
	// answered with the CORS header. A 401 carries it too.
	res = h.do(http.MethodPost, "/api/headless/runs", "http://glasses.test:5173", "application/json",
		strings.NewReader(`{"projectId":"`+h.project.ID+`","prompt":"hi"}`))
	body := readAll(t, res)
	if res.StatusCode != http.StatusCreated || res.Header.Get("Access-Control-Allow-Origin") != "http://glasses.test:5173" {
		t.Fatalf("allowed POST: %d %v %s", res.StatusCode, res.Header, body)
	}
	var made map[string]any
	_ = json.Unmarshal(body, &made)
	h.sseEvents(made["runId"].(string), "0")

	res = h.do(http.MethodPost, "/api/headless/runs", "http://evil.test", "application/json",
		strings.NewReader(`{"projectId":"`+h.project.ID+`","prompt":"hi"}`))
	if b := readAll(t, res); res.StatusCode != http.StatusForbidden || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("denied POST: %d %s", res.StatusCode, b)
	}
	// The allowlist reaches /api/headless only.
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/projects", strings.NewReader(`{"path":"/tmp"}`))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Origin", "http://glasses.test:5173")
	r2, _ := http.DefaultClient.Do(req)
	if readAll(t, r2); r2.StatusCode != http.StatusForbidden {
		t.Fatalf("an allowed headless origin wrote outside /api/headless: %d", r2.StatusCode)
	}
	// From another origin the cookie does not count, only the token.
	req, _ = http.NewRequest(http.MethodGet, h.ts.URL+"/api/headless/config", nil)
	req.Header.Set("Origin", "http://glasses.test:5173")
	r3, _ := h.ts.Client().Do(req)
	if readAll(t, r3); r3.StatusCode != http.StatusUnauthorized || r3.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("a cross-origin cookie authenticated, or the 401 lacked CORS: %d %v", r3.StatusCode, r3.Header)
	}
}

func TestAHeadlessRunEndToEnd(t *testing.T) {
	h := newHeadlessRig(t, `,"persona":"PERSONA {assistantDir}"`)
	res := h.do(http.MethodPost, "/api/headless/runs", "", "application/json",
		strings.NewReader(`{"projectId":"`+h.project.ID+`","prompt":"上海天气","context":"设备：G2"}`))
	body := readAll(t, res)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST: %d %s", res.StatusCode, body)
	}
	var made struct {
		RunID     string `json:"runId"`
		SessionID string `json:"sessionId"`
		New       bool   `json:"new"`
	}
	_ = json.Unmarshal(body, &made)
	if !strings.HasPrefix(made.RunID, "r_") || !headless.ValidSessionID(made.SessionID) || !made.New {
		t.Fatalf("made %+v", made)
	}
	evs := h.sseEvents(made.RunID, "0")
	var ts []string
	for _, e := range evs {
		ts = append(ts, e["t"].(string))
	}
	if strings.Join(ts, ",") != "init,text,result" || evs[2]["text"] != "晴" {
		t.Fatalf("events %v", evs)
	}
	if again := h.sseEvents(made.RunID, "2"); len(again) != 1 || again[0]["t"] != "result" {
		t.Fatalf("replay after 2: %v", again)
	}

	argv, _ := os.ReadFile(filepath.Join(h.dir, "argv.txt"))
	args := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n")
	want := map[string]string{"--session-id": made.SessionID, "--model": "opus", "--permission-mode": "bypassPermissions", "--output-format": "stream-json"}
	for flag, v := range want {
		found := false
		for i, a := range args {
			if a == flag && i+1 < len(args) && args[i+1] == v {
				found = true
			}
		}
		if !found {
			t.Errorf("argv lacks %s %s: %q", flag, v, args)
		}
	}
	if !strings.Contains(string(argv), "PERSONA "+h.dir) || !strings.Contains(string(argv), "## 记忆快照") ||
		!strings.Contains(string(argv), "当前工作目录：助理 ("+h.dir+")") || !strings.Contains(string(argv), "设备：G2") {
		t.Errorf("system prompt: %s", argv)
	}
	env, _ := os.ReadFile(filepath.Join(h.dir, "env.txt"))
	if strings.Contains(string(env), "VIBEPANEL_") {
		t.Errorf("a hook variable reached the run: %s", env)
	}

	// Continuing it: --resume, new false.
	res = h.do(http.MethodPost, "/api/headless/runs", "", "application/json",
		strings.NewReader(`{"projectId":"`+h.project.ID+`","prompt":"明天呢","sessionId":"`+made.SessionID+`","model":"haiku","permissionMode":"plan"}`))
	body = readAll(t, res)
	_ = json.Unmarshal(body, &made)
	if res.StatusCode != http.StatusCreated || made.New {
		t.Fatalf("resume: %d %s", res.StatusCode, body)
	}
	h.sseEvents(made.RunID, "0")
	argv, _ = os.ReadFile(filepath.Join(h.dir, "argv.txt"))
	if !strings.Contains(string(argv), "--resume\n"+made.SessionID+"\n") || !strings.Contains(string(argv), "--model\nhaiku\n") ||
		!strings.Contains(string(argv), "--permission-mode\nplan\n") {
		t.Errorf("resume argv: %s", argv)
	}

	res = h.do(http.MethodGet, "/api/headless/runs?projectId="+h.project.ID, "", "", nil)
	var runs struct {
		Runs []headless.Info `json:"runs"`
	}
	_ = json.Unmarshal(readAll(t, res), &runs)
	if len(runs.Runs) != 2 || runs.Runs[0].State != "done" {
		t.Fatalf("runs %+v", runs.Runs)
	}

	// The audit has the run without the prompt.
	entries, _ := h.srv.DB.RecentAudit(context.Background(), 50)
	var details []string
	for _, e := range entries {
		if e.Event == "headless.run" {
			details = append(details, e.Detail)
			if strings.Contains(e.Detail, "天气") || strings.Contains(e.Detail, "明天") {
				t.Errorf("the prompt reached the audit: %q", e.Detail)
			}
		}
	}
	if strings.Join(details, "|") != "助理 · haiku · plan|助理 · opus · bypassPermissions" {
		t.Errorf("headless.run audit entries %q", details)
	}

	for _, bad := range []string{
		`{"projectId":"` + h.project.ID + `","prompt":"x","model":"gpt-9"}`,
		`{"projectId":"` + h.project.ID + `","prompt":"x","permissionMode":"yolo"}`,
		`{"projectId":"` + h.project.ID + `","prompt":"  "}`,
		`{"projectId":"` + h.project.ID + `","prompt":"x","sessionId":"../x"}`,
		`{"projectId":"` + h.project.ID + `","prompt":"` + strings.Repeat("x", 9000) + `"}`,
	} {
		res = h.do(http.MethodPost, "/api/headless/runs", "", "application/json", strings.NewReader(bad))
		if b := readAll(t, res); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%.80s = %d %s", bad, res.StatusCode, b)
		}
	}
	res = h.do(http.MethodPost, "/api/headless/runs", "", "application/json", strings.NewReader(`{"projectId":"nope","prompt":"x"}`))
	if readAll(t, res); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d", res.StatusCode)
	}
}

func TestHeadlessRunsAreCappedAndStoppable(t *testing.T) {
	h := newHeadlessRig(t, `,"maxConcurrent":1`)
	start := func(sid string) *http.Response {
		b := `{"projectId":"` + h.project.ID + `","prompt":"sleep"`
		if sid != "" {
			b += `,"sessionId":"` + sid + `"`
		}
		return h.do(http.MethodPost, "/api/headless/runs", "", "application/json", strings.NewReader(b+"}"))
	}
	res := start("")
	var made map[string]any
	_ = json.Unmarshal(readAll(t, res), &made)
	runID, sid := made["runId"].(string), made["sessionId"].(string)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("first: %d", res.StatusCode)
	}
	if r := start(sid); readAll(t, r) != nil && r.StatusCode != http.StatusConflict {
		t.Fatalf("same session: %d", r.StatusCode)
	}
	if r := start(""); readAll(t, r) != nil && r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over the cap: %d", r.StatusCode)
	}
	// running shows on the session list once the transcript exists.
	set, _ := h.srv.headlessSettings(context.Background())
	_, cfgDir, err := h.srv.headlessLaunch(context.Background(), set)
	if err != nil {
		t.Fatal(err)
	}
	tdir := headless.TranscriptDir(cfgDir, h.project.Path)
	_ = os.MkdirAll(tdir, 0o755)
	_ = os.WriteFile(filepath.Join(tdir, sid+".jsonl"), []byte(`{"type":"user","message":{"role":"user","content":"sleep"},"timestamp":"2026-10-07T10:00:00Z"}`+"\n"), 0o600)
	res = h.do(http.MethodGet, "/api/headless/sessions?projectId="+h.project.ID, "", "", nil)
	var sl struct {
		Sessions []headless.SessionInfo `json:"sessions"`
	}
	_ = json.Unmarshal(readAll(t, res), &sl)
	if len(sl.Sessions) != 1 || !sl.Sessions[0].Running || sl.Sessions[0].Title != "sleep" {
		t.Fatalf("sessions %+v", sl.Sessions)
	}
	res = h.do(http.MethodGet, "/api/headless/sessions/"+sid+"?projectId="+h.project.ID, "", "", nil)
	if b := readAll(t, res); res.StatusCode != 200 || !strings.Contains(string(b), `"role":"user"`) {
		t.Fatalf("session read: %d %s", res.StatusCode, b)
	}

	res = h.do(http.MethodDelete, "/api/headless/runs/"+runID, "", "", nil)
	if readAll(t, res); res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d", res.StatusCode)
	}
	evs := h.sseEvents(runID, "0")
	if last := evs[len(evs)-1]; last["t"] != "stopped" {
		t.Fatalf("last event %v", last)
	}
	res = h.do(http.MethodDelete, "/api/headless/runs/r_nope", "", "", nil)
	if readAll(t, res); res.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE unknown = %d", res.StatusCode)
	}
	res = h.do(http.MethodGet, "/api/headless/runs/r_nope/events", "", "", nil)
	if readAll(t, res); res.StatusCode != http.StatusNotFound {
		t.Fatalf("events of unknown = %d", res.StatusCode)
	}
}

func TestTranscribeProxiesToTheUpstream(t *testing.T) {
	var got struct {
		auth, model, language, prompt, fileType string
		wav                                     []byte
	}
	fail := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.Error(w, "wrong path "+r.URL.Path, 404)
			return
		}
		if fail {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		got.auth = r.Header.Get("Authorization")
		got.model, got.language, got.prompt = r.FormValue("model"), r.FormValue("language"), r.FormValue("prompt")
		f, fh, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		got.fileType = fh.Header.Get("Content-Type")
		got.wav, _ = io.ReadAll(f)
		_, _ = io.WriteString(w, `{"text":" 明天上海天气 "}`)
	}))
	defer up.Close()

	h := newHeadlessRig(t, "")
	res := h.do(http.MethodPost, "/api/headless/transcribe", "", "audio/L16; rate=16000", strings.NewReader("ab"))
	if b := readAll(t, res); res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured: %d %s", res.StatusCode, b)
	}
	put(t, h.ts, "/api/settings/headless", `{"asr":{"baseUrl":"`+up.URL+`/v1","model":"m1","language":"zh","prompt":"人名：江","apiKey":"k1"}}`, http.StatusOK)

	pcm := make([]byte, 3200)
	for i := range pcm {
		pcm[i] = byte(i)
	}
	res = h.do(http.MethodPost, "/api/headless/transcribe?prompt=天气", "", "audio/L16; rate=16000", strings.NewReader(string(pcm)))
	body := readAll(t, res)
	if res.StatusCode != 200 || !strings.Contains(string(body), `"text":"明天上海天气"`) || !strings.Contains(string(body), `"ms":`) {
		t.Fatalf("transcribe: %d %s", res.StatusCode, body)
	}
	if got.auth != "Bearer k1" || got.model != "m1" || got.language != "zh" || got.prompt != "人名：江 天气" || got.fileType != "audio/wav" {
		t.Fatalf("upstream got %+v", got)
	}
	if len(got.wav) != 44+len(pcm) || string(got.wav[:4]) != "RIFF" ||
		binary.LittleEndian.Uint32(got.wav[24:28]) != 16000 || binary.LittleEndian.Uint32(got.wav[40:44]) != uint32(len(pcm)) ||
		string(got.wav[44:]) != string(pcm) {
		t.Fatalf("the WAV the upstream got is wrong: header %v", got.wav[:44])
	}

	// A WAV goes through untouched.
	wav := headless.WAV(pcm, 8000, 1)
	res = h.do(http.MethodPost, "/api/headless/transcribe", "", "audio/wav", strings.NewReader(string(wav)))
	if readAll(t, res); res.StatusCode != 200 || string(got.wav) != string(wav) {
		t.Fatalf("wav passthrough: %d", res.StatusCode)
	}
	res = h.do(http.MethodPost, "/api/headless/transcribe", "", "audio/mpeg", strings.NewReader("x"))
	if readAll(t, res); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("mp3 = %d", res.StatusCode)
	}
	res = h.do(http.MethodPost, "/api/headless/transcribe", "", "audio/L16; rate=16000", strings.NewReader(strings.Repeat("x", headless.MaxAudioBytes+10)))
	if readAll(t, res); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large = %d", res.StatusCode)
	}
	fail = true
	res = h.do(http.MethodPost, "/api/headless/transcribe", "", "audio/L16; rate=16000", strings.NewReader("abcd"))
	if b := readAll(t, res); res.StatusCode != http.StatusBadGateway || !strings.Contains(string(b), "invalid api key") {
		t.Fatalf("upstream failure: %d %s", res.StatusCode, b)
	}
}
