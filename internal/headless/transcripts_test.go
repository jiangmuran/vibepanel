package headless

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	sidA = "aaaaaaaa-0000-4000-8000-000000000001"
	sidB = "bbbbbbbb-0000-4000-8000-000000000002"
)

func writeTranscript(t *testing.T, configDir, cwd, sid string, lines []string, mod time.Time) {
	t.Helper()
	dir := TranscriptDir(configDir, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

var conversation = []string{
	`{"type":"summary","summary":"x"}`,
	`{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>Caveat: ...</local-command-caveat>"},"timestamp":"2026-10-07T10:00:00Z"}`,
	`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"},"timestamp":"2026-10-07T10:00:01Z"}`,
	`{"type":"user","message":{"role":"user","content":"[10-07 18:02] 帮我查一下明天上海的天气"},"timestamp":"2026-10-07T10:00:02Z"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"我查一下。"}]},"timestamp":"2026-10-07T10:00:03Z"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"WebSearch","input":{"query":"上海 天气"}}]},"timestamp":"2026-10-07T10:00:04Z"}`,
	`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"..."}]},"timestamp":"2026-10-07T10:00:05Z"}`,
	`{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent noise"}]},"timestamp":"2026-10-07T10:00:05Z"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"明天多云，18 到 24 度。"}]},"timestamp":"2026-10-07T10:00:06Z"}`,
	`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"谢谢"}]},"timestamp":"2026-10-07T10:01:00Z"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"不客气"}]},"timestamp":"2026-10-07T10:01:01Z"}`,
	`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"不客气"}]},` + `"timestamp":"bad"`, // truncated last line
}

func TestTranscriptsAreListedNewestFirstWithTitles(t *testing.T) {
	cfg := t.TempDir()
	cwd := "/home/u/vibeagent-assistant"
	now := time.Now()
	writeTranscript(t, cfg, cwd, sidA, conversation, now.Add(-time.Hour))
	writeTranscript(t, cfg, cwd, sidB, []string{
		`{"type":"user","message":{"role":"user","content":"` + strings.Repeat("长", 100) + `"},"timestamp":"2026-10-07T11:00:00Z"}`,
	}, now)
	// Not sessions: a stub with nothing said, and a file that is not a UUID.
	writeTranscript(t, cfg, cwd, "cccccccc-0000-4000-8000-000000000003", []string{`{"type":"summary"}`}, now)
	writeTranscript(t, cfg, cwd, "notes", conversation, now)

	list, err := ListSessions(cfg, cwd, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != sidB || list[1].ID != sidA {
		t.Fatalf("list %+v", list)
	}
	if list[1].Title != "帮我查一下明天上海的天气" || list[1].Turns != 2 {
		t.Fatalf("title %q turns %d", list[1].Title, list[1].Turns)
	}
	if n := len([]rune(list[0].Title)); n != 80 {
		t.Fatalf("long title is %d runes", n)
	}
	if one, _ := ListSessions(cfg, cwd, 1); len(one) != 1 {
		t.Fatalf("limit ignored: %d", len(one))
	}
	if none, err := ListSessions(cfg, "/never/used", 50); err != nil || len(none) != 0 || none == nil {
		t.Fatalf("missing directory: %v %v", none, err)
	}
}

func TestATranscriptReadsBackAsTurns(t *testing.T) {
	cfg := t.TempDir()
	cwd := "/p"
	writeTranscript(t, cfg, cwd, sidA, conversation, time.Now())
	msgs, err := ReadSession(cfg, cwd, sidA, 40)
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,user,assistant" {
		t.Fatalf("roles %v: %+v", roles, msgs)
	}
	a := msgs[1]
	if a.Text != "我查一下。\n\n明天多云，18 到 24 度。" {
		t.Errorf("assistant turn text %q", a.Text)
	}
	if len(a.Tools) != 1 || a.Tools[0].Name != "WebSearch" || a.Tools[0].Summary != "上海 天气" {
		t.Errorf("tools %+v", a.Tools)
	}
	if strings.Contains(a.Text, "subagent") {
		t.Error("a sidechain line was shown")
	}
	if msgs[0].At != time.Date(2026, 10, 7, 10, 0, 2, 0, time.UTC).Unix() {
		t.Errorf("at %d", msgs[0].At)
	}
	last2, _ := ReadSession(cfg, cwd, sidA, 2)
	if len(last2) != 2 || last2[0].Text != "谢谢" {
		t.Fatalf("limit 2: %+v", last2)
	}
	if _, err := ReadSession(cfg, cwd, sidB, 40); !os.IsNotExist(err) {
		t.Fatalf("missing session: %v", err)
	}
	if _, err := ReadSession(cfg, cwd, "../../../etc/passwd", 40); !os.IsNotExist(err) {
		t.Fatalf("traversal: %v", err)
	}
}
