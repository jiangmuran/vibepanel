package headless

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunArgsAreTheContractsOrder(t *testing.T) {
	got := RunArgs("天气", "11111111-2222-4333-8444-555555555555", false, "opus", "bypassPermissions", "SP")
	want := []string{
		"-p", "天气", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--session-id", "11111111-2222-4333-8444-555555555555",
		"--model", "opus", "--permission-mode", "bypassPermissions", "--append-system-prompt", "SP",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	resumed := RunArgs("x", "11111111-2222-4333-8444-555555555555", true, "haiku", "plan", "SP")
	if resumed[6] != "--resume" {
		t.Fatalf("a continued session must use --resume: %q", resumed)
	}
	// A prompt that starts with a dash stays a positional argument.
	if dash := RunArgs("-rf /", "id", false, "m", "plan", ""); dash[1] != " -rf /" {
		t.Fatalf("dash prompt: %q", dash[1])
	}
}

func TestSessionIDs(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := NewSessionID()
		if !ValidSessionID(id) || id[14] != '4' {
			t.Fatalf("NewSessionID %q is not a v4 UUID", id)
		}
	}
	for _, bad := range []string{"", "../../etc/passwd", "11111111-2222-4333-8444-55555555555g", "11111111x2222-4333-8444-555555555555", strings.Repeat("a", 36)} {
		if ValidSessionID(bad) {
			t.Errorf("%q accepted as a session id", bad)
		}
	}
}

func TestDefaultsAndValidation(t *testing.T) {
	d := Defaults()
	if d.DefaultPermissionMode != "bypassPermissions" {
		t.Fatalf("default permission mode %q; the owner chose bypassPermissions", d.DefaultPermissionMode)
	}
	if d.Enabled {
		t.Fatal("headless must start switched off")
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
	cases := map[string]func(*Settings){
		"wildcard origin":       func(s *Settings) { s.AllowedOrigins = []string{"*"} },
		"origin with path":      func(s *Settings) { s.AllowedOrigins = []string{"http://x.test/app"} },
		"origin not a url":      func(s *Settings) { s.AllowedOrigins = []string{"x.test"} },
		"default not listed":    func(s *Settings) { s.DefaultModel = "gpt" },
		"model is a flag":       func(s *Settings) { s.Models = append(s.Models, Model{ID: "--evil"}) },
		"duplicate model":       func(s *Settings) { s.Models = append(s.Models, Model{ID: "opus"}) },
		"no models":             func(s *Settings) { s.Models = nil },
		"unknown mode":          func(s *Settings) { s.DefaultPermissionMode = "yolo" },
		"relative dir":          func(s *Settings) { s.AssistantDir = "assistant" },
		"concurrency":           func(s *Settings) { s.MaxConcurrent = 0 },
		"timeout":               func(s *Settings) { s.TimeoutMinutes = 1000 },
		"asr url not http":      func(s *Settings) { s.ASR.BaseURL = "file:///etc" },
		"persona far too large": func(s *Settings) { s.Persona = strings.Repeat("x", 20<<10) },
	}
	for name, mutate := range cases {
		s := Defaults()
		mutate(&s)
		s.Normalize()
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := Defaults()
	ok.AllowedOrigins = []string{" http://192.168.1.20:5173/ ", "null", "http://192.168.1.20:5173"}
	ok.Models = append(ok.Models, Model{ID: "claude-opus-4-1[1m]"})
	ok.Normalize()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(ok.AllowedOrigins) != 2 || !ok.OriginAllowed("http://192.168.1.20:5173") || !ok.OriginAllowed("null") {
		t.Fatalf("origins normalised to %q", ok.AllowedOrigins)
	}
	if ok.OriginAllowed("http://192.168.1.20:5174") || ok.OriginAllowed("") {
		t.Fatal("an origin matched that is not on the list")
	}
	if ok.Models[len(ok.Models)-1].Label != "claude-opus-4-1[1m]" {
		t.Fatal("an empty label is not filled from the id")
	}
}

func TestNormalizeStreamJSON(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5-5","tools":[]}`,
		`{"type":"stream_event","event":{"type":"message_start"},"parent_tool_use_id":null}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"好的，"}},"parent_tool_use_id":null}`,
		// A subagent's delta: not the answer.
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"inner"}},"parent_tool_use_id":"toolu_task"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"好的，"},{"type":"tool_use","id":"toolu_1","name":"WebSearch","input":{"query":"上海 明天 天气"}}]},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":false,"content":"..."}]},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","is_error":true}]},"parent_tool_use_id":null}`,
		`{"type":"rate_limit_event","whatever":1}`,
		`not json`,
		`{"type":"result","subtype":"success","result":"明天多云","session_id":"s1","total_cost_usd":0.11,"duration_ms":28330,"num_turns":3,"is_error":false}`,
	}
	var got []Event
	for _, l := range lines {
		got = append(got, Normalize([]byte(l))...)
	}
	var types []string
	for _, e := range got {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "init,text,tool,tool_done,tool_done,result" {
		t.Fatalf("event types %v", types)
	}
	if got[0].Fields["sessionId"] != "s1" || got[0].Fields["model"] != "claude-opus-5-5" {
		t.Errorf("init %v", got[0].Fields)
	}
	if got[1].Fields["d"] != "好的，" {
		t.Errorf("text %v", got[1].Fields)
	}
	if got[2].Fields["name"] != "WebSearch" || got[2].Fields["summary"] != "上海 明天 天气" || got[2].Fields["id"] != "toolu_1" {
		t.Errorf("tool %v", got[2].Fields)
	}
	if got[3].Fields["ok"] != true || got[4].Fields["ok"] != false {
		t.Errorf("tool_done ok flags %v %v", got[3].Fields, got[4].Fields)
	}
	r := got[5]
	if r.Fields["text"] != "明天多云" || r.Fields["costUsd"] != 0.11 || r.Fields["ms"] != int64(28330) || r.Fields["turns"] != 3 || !r.Terminal() {
		t.Errorf("result %v", r.Fields)
	}
	var wire map[string]any
	if err := json.Unmarshal(r.JSON(), &wire); err != nil || wire["t"] != "result" || wire["isError"] != false {
		t.Errorf("wire form %s", r.JSON())
	}
}

func TestToolSummary(t *testing.T) {
	cases := map[string]string{
		`{"file_path":"/a/b.go","old_string":"x"}`:       "/a/b.go",
		`{"command":"ls -la\n  /tmp","description":"x"}`: "ls -la /tmp",
		`{"url":"https://x.test","prompt":"p"}`:          "https://x.test",
		`{"pattern":"TODO","path":""}`:                   "TODO",
		`{"todos":[]}`:                                   "",
		`[1,2]`:                                          "",
	}
	for in, want := range cases {
		if got := ToolSummary(json.RawMessage(in)); got != want {
			t.Errorf("ToolSummary(%s) = %q, want %q", in, got, want)
		}
	}
	long := ToolSummary(json.RawMessage(`{"command":"` + strings.Repeat("长", 100) + `"}`))
	if n := len([]rune(long)); n != 80 || !strings.HasSuffix(long, "…") {
		t.Errorf("long summary is %d runes: %q", n, long)
	}
}

func TestCompactMergesOldTextAndKeepsTheTail(t *testing.T) {
	var evs []Event
	for i := 1; i <= 100; i++ {
		evs = append(evs, Event{Seq: int64(i), Type: "text", Fields: map[string]any{"d": "x"}})
	}
	out := compact(evs, 60)
	if len(out) != 51 {
		t.Fatalf("compacted to %d events", len(out))
	}
	if out[0].Fields["d"] != strings.Repeat("x", 50) || out[0].Seq != 50 {
		t.Fatalf("merged head %v seq %d", out[0].Fields, out[0].Seq)
	}
	if out[len(out)-1].Seq != 100 {
		t.Fatal("the newest event was lost")
	}
}

func TestProjectKeyMatchesClaudeCode(t *testing.T) {
	cases := map[string]string{
		"/home/jmr/.local/share/vibepanel/assistant": "-home-jmr--local-share-vibepanel-assistant",
		"/home/jmr/projects/even_vibeagent":          "-home-jmr-projects-even-vibeagent",
		"/home/jmr/助理":                               "-home-jmr---",
	}
	for in, want := range cases {
		if got := ProjectKey(in); got != want {
			t.Errorf("ProjectKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWAVHeader(t *testing.T) {
	pcm := make([]byte, 32000) // one second
	w := WAV(pcm, 16000, 1)
	if len(w) != 44+len(pcm) {
		t.Fatalf("length %d", len(w))
	}
	le := binary.LittleEndian
	checks := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"riff size", le.Uint32(w[4:8]), uint32(36 + len(pcm))},
		{"fmt size", le.Uint32(w[16:20]), 16},
		{"format", uint32(le.Uint16(w[20:22])), 1},
		{"channels", uint32(le.Uint16(w[22:24])), 1},
		{"rate", le.Uint32(w[24:28]), 16000},
		{"byte rate", le.Uint32(w[28:32]), 32000},
		{"block align", uint32(le.Uint16(w[32:34])), 2},
		{"bits", uint32(le.Uint16(w[34:36])), 16},
		{"data size", le.Uint32(w[40:44]), uint32(len(pcm))},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if string(w[0:4]) != "RIFF" || string(w[8:16]) != "WAVEfmt " || string(w[36:40]) != "data" {
		t.Errorf("chunk ids wrong: %q", w[:44])
	}
}

func TestAssistantDirIsCreatedAndNeverOverwritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "assistant")
	if err := EnsureAssistantDir(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"CLAUDE.md", "memories/USER.md", "memories/MEMORY.md", ".claude/skills", "notes"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	user, _ := os.ReadFile(filepath.Join(dir, "memories", "USER.md"))
	if string(user) != "# 用户档案\n\n（助理会在这里记录你的偏好）\n" {
		t.Errorf("USER.md template %q", user)
	}
	learned := "# 用户档案\n\n住在上海\n"
	if err := os.WriteFile(filepath.Join(dir, "memories", "USER.md"), []byte(learned), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAssistantDir(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "memories", "USER.md")); string(got) != learned {
		t.Fatalf("a second ensure overwrote what the assistant learned: %q", got)
	}

	sp := SystemPrompt(PromptInput{AssistantDir: dir, Memory: true, ProjectName: "助理", ProjectPath: dir, Host: "box", Context: "设备：G2"})
	for _, want := range []string{
		"你是用户的私人助理", dir + "/memories/USER.md", "## 记忆快照（会话开始时的内容）", "### USER.md\n# 用户档案\n\n住在上海",
		"### MEMORY.md\n# 长期记忆", "当前工作目录：助理 (" + dir + ")；服务器：box", "设备：G2",
	} {
		if !strings.Contains(sp, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, sp)
		}
	}
	if strings.Contains(sp, "{assistantDir}") {
		t.Error("{assistantDir} not substituted")
	}
	noMem := SystemPrompt(PromptInput{Persona: "P {assistantDir}", AssistantDir: "/x", ProjectName: "n", ProjectPath: "/p", Host: "h"})
	if noMem != "P /x\n\n当前工作目录：n (/p)；服务器：h" {
		t.Errorf("custom persona without memory: %q", noMem)
	}
}

func TestExpandDir(t *testing.T) {
	if got, _ := ExpandDir("~/vibeagent-assistant", "/home/u"); got != "/home/u/vibeagent-assistant" {
		t.Errorf("got %q", got)
	}
	if _, err := ExpandDir("rel", "/home/u"); err == nil {
		t.Error("relative accepted")
	}
}
