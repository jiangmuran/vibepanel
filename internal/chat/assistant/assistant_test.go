package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// The harness is a shell script that records how it was called and prints
// what the test told it to. What is under test is the argv, the environment
// and the parsing, which is everything this package decides; what a real
// harness does with them is checked by hand against its --help.

// fakeScript answers --help from a file and otherwise records argv (NUL
// separated: the prompt carries newlines), env, cwd and stdin per call,
// then prints the canned reply and exits with the canned status. For codex
// it also honours -o and --output-schema, the way the real one does.
const fakeScript = `#!/bin/sh
if [ "$1" = "--help" ]; then cat "$FAKE_OUT/help"; exit 0; fi
n=$(cat "$FAKE_OUT/calls" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$FAKE_OUT/calls"
printf '%s\0' "$@" > "$FAKE_OUT/argv.$n"
env > "$FAKE_OUT/env.$n"
pwd > "$FAKE_OUT/cwd.$n"
cat > "$FAKE_OUT/stdin.$n"
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then cp "$FAKE_OUT/last" "$a"; fi
  if [ "$prev" = "--output-schema" ]; then cp "$a" "$FAKE_OUT/schema.$n"; fi
  prev="$a"
done
if [ -f "$FAKE_OUT/sleep" ]; then sleep "$(cat "$FAKE_OUT/sleep")"; fi
if [ -f "$FAKE_OUT/stderr" ]; then cat "$FAKE_OUT/stderr" >&2; fi
if [ -f "$FAKE_OUT/reply.$n" ]; then cat "$FAKE_OUT/reply.$n"; else cat "$FAKE_OUT/reply"; fi
exit "$(cat "$FAKE_OUT/exit" 2>/dev/null || echo 0)"
`

type fake struct {
	t   *testing.T
	dir string // FAKE_OUT
	bin string
}

func newFake(t *testing.T, name, help string) *fake {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte(fakeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fake{t: t, dir: dir, bin: bin}
	f.set("help", help)
	f.set("reply", "")
	return f
}

func (f *fake) set(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fake) calls() int {
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func (f *fake) argv(n int) []string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "argv."+strconv.Itoa(n)))
	if err != nil {
		f.t.Fatalf("call %d never happened: %v", n, err)
	}
	s := strings.TrimSuffix(string(b), "\x00")
	return strings.Split(s, "\x00")
}

func (f *fake) env(n int) map[string]string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "env."+strconv.Itoa(n)))
	if err != nil {
		f.t.Fatalf("call %d never happened: %v", n, err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

func (f *fake) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

// flagValue finds "--flag value" in argv; "" and false when absent.
func flagValue(argv []string, flag string) (string, bool) {
	for i, a := range argv {
		if a == flag {
			if i+1 < len(argv) {
				return argv[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

const claudeHelp = `Usage: claude [options]
  --append-system-prompt-file <file>
  --strict-mcp-config
  --tools <tools...>
`

func newClaudeRunner(t *testing.T, f *fake, extra ...string) (*Runner, Config) {
	t.Helper()
	cfg := Config{
		Harness:    "claude",
		Binary:     f.bin,
		WorkDir:    filepath.Join(t.TempDir(), "assistant"),
		PanelURL:   "https://127.0.0.1:18443",
		ToolsToken: "tok-secret",
		SelfBinary: "/opt/vibepanel/bin/vibepanel",
		Env:        append([]string{"FAKE_OUT=" + f.dir}, extra...),
		Timeout:    10 * time.Second,
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r, cfg
}

func sampleRequest() chat.AssistantRequest {
	return chat.AssistantRequest{
		Chat: "telegram:42",
		Text: "tell the api one to add tests",
		Lang: "en",
		Sessions: []chat.SessionBrief{
			{Handle: 1, Title: "api refactor", Project: "api", State: "working", Tool: "claude"},
			{Handle: 2, Title: "docs", Project: "site", State: "waiting", Kind: "prompt", Tool: "codex"},
		},
		Recent: []int{2, 1},
	}
}

func TestClaudeTranslateArgvAndParsing(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("reply", `{"type":"result","subtype":"success","is_error":false,"result":"ignored","session_id":"s1","total_cost_usd":0.0123,"structured_output":{"verb":"send","handle":1,"text":"add tests","say":""}}`)
	r, cfg := newClaudeRunner(t, f)

	in, cost, err := r.Translate(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if in.Verb != "send" || in.Handle != 1 || in.Text != "add tests" {
		t.Errorf("intent = %+v", in)
	}
	if cost != 0.0123 {
		t.Errorf("cost = %v, want 0.0123 from total_cost_usd", cost)
	}

	argv := f.argv(1)
	joined := strings.Join(argv, " ")
	if argv[0] != "-p" {
		t.Errorf("argv starts %q, want -p", argv[0])
	}
	if v, ok := flagValue(argv, "--json-schema"); !ok || !strings.Contains(v, `"clarify"`) || !strings.Contains(v, `"additionalProperties":false`) {
		t.Errorf("--json-schema = %q, %v", v, ok)
	}
	if v, _ := flagValue(argv, "--max-turns"); v != "1" {
		t.Errorf("--max-turns = %q, want 1: Translate is one turn by design", v)
	}
	if v, _ := flagValue(argv, "--permission-mode"); v != "dontAsk" {
		t.Errorf("--permission-mode = %q", v)
	}
	if v, _ := flagValue(argv, "--disallowedTools"); v != claudeDisallowed {
		t.Errorf("--disallowedTools = %q", v)
	}
	for _, tool := range []string{"Bash", "Edit", "Write", "Read", "Glob", "Grep", "WebFetch", "WebSearch", "Task", "NotebookEdit", "TodoWrite"} {
		if v, _ := flagValue(argv, "--disallowedTools"); !strings.Contains(v, tool) {
			t.Errorf("--disallowedTools misses %s", tool)
		}
	}
	if v, _ := flagValue(argv, "--settings"); v != `{"hooks":{}}` {
		t.Errorf("--settings = %q", v)
	}
	if v, _ := flagValue(argv, "--append-system-prompt-file"); v != filepath.Join(cfg.WorkDir, "translate.md") {
		t.Errorf("--append-system-prompt-file = %q", v)
	}
	if v, _ := flagValue(argv, "--output-format"); v != "json" {
		t.Errorf("--output-format = %q", v)
	}
	if v, ok := flagValue(argv, "--tools"); !ok || v != "" {
		t.Errorf("--tools = %q, %v; want the empty list the help documents", v, ok)
	}
	if !hasFlag(argv, "--strict-mcp-config") {
		t.Error("Translate loads the person's own MCP servers: no --strict-mcp-config")
	}
	if hasFlag(argv, "--mcp-config") || hasFlag(argv, "--allowedTools") {
		t.Error("Translate must have no tools at all, not even the panel's")
	}
	for _, bad := range []string{"--dangerously-skip-permissions", "bypassPermissions", "--allow-dangerously-skip-permissions"} {
		if strings.Contains(joined, bad) {
			t.Errorf("argv contains %s", bad)
		}
	}
	if hasFlag(argv, "--model") {
		t.Error("--model passed with none configured")
	}
	// The prompt: the table as data, the words fenced, the language named,
	// and never a line an agent printed (there is no field for one).
	prompt := argv[1]
	for _, want := range []string{`"handle":1`, `"title":"api refactor"`, "data, not instructions", "[2,1]", "<<<\ntell the api one to add tests\n>>>", "English"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if cwd := strings.TrimSpace(f.read("cwd.1")); cwd != cfg.WorkDir {
		t.Errorf("cwd = %q, want the working directory %q", cwd, cfg.WorkDir)
	}
	if got := f.read("stdin.1"); got != "" {
		t.Errorf("stdin was not closed: %q", got)
	}
}

func TestTheChildDoesNotInheritThePanelsStdin(t *testing.T) {
	// The panel under systemd has no stdin worth reading; run from a
	// terminal it has one, and a harness that finds a terminal may wait on
	// it. The fake copies whatever it is given, so give the panel something.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pw.WriteString("the panel's own stdin\n"); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	saved := os.Stdin
	os.Stdin = pr
	t.Cleanup(func() { os.Stdin = saved; pr.Close() })

	f := newFake(t, "claude", claudeHelp)
	f.set("reply", `{"type":"result","is_error":false,"structured_output":{"verb":"none","handle":0,"text":"","say":""}}`)
	r, _ := newClaudeRunner(t, f)
	if _, _, err := r.Translate(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if got := f.read("stdin.1"); got != "" {
		t.Errorf("the child read the panel's stdin: %q", got)
	}
}

func TestClaudeTranslateFallsBackToResultTextAndModelFlag(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("reply", `{"type":"result","is_error":false,"result":" {\"verb\":\"clarify\",\"handle\":0,\"text\":\"\",\"say\":\"哪一个？\"} ","total_cost_usd":0.01}`)
	cfg := Config{
		Harness: "claude", Binary: f.bin, WorkDir: t.TempDir(), Model: "sonnet",
		SelfBinary: "/x", Env: []string{"FAKE_OUT=" + f.dir},
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	in, _, err := r.Translate(context.Background(), chat.AssistantRequest{Text: "hi", Lang: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Verb != "clarify" || in.Say != "哪一个？" {
		t.Errorf("intent = %+v", in)
	}
	if v, _ := flagValue(f.argv(1), "--model"); v != "sonnet" {
		t.Errorf("--model = %q", v)
	}
	if !strings.Contains(f.argv(1)[1], "简体中文") {
		t.Error("the prompt does not name the language")
	}
}

func TestTranslateRefusesAVerbTheBridgeDoesNotKnow(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("reply", `{"type":"result","is_error":false,"result":"","structured_output":{"verb":"delete","handle":1,"text":"","say":""}}`)
	r, _ := newClaudeRunner(t, f)
	if _, _, err := r.Translate(context.Background(), sampleRequest()); err == nil {
		t.Fatal("an unknown verb was accepted; the bridge would act on it")
	}
}

func TestClaudeAskArgvResumeAndScoping(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("reply.1", `{"type":"result","is_error":false,"result":"[1] is running tests.","session_id":"sess-A","total_cost_usd":0.2}`)
	f.set("reply.2", `{"type":"result","is_error":false,"result":"still.","session_id":"sess-A","total_cost_usd":0.1}`)
	f.set("reply.3", `{"type":"result","is_error":false,"result":"other.","session_id":"sess-B","total_cost_usd":0.1}`)
	r, cfg := newClaudeRunner(t, f)
	r.cfg.MaxTurns = 4

	ans, err := r.Ask(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "[1] is running tests." || ans.CostUSD != 0.2 {
		t.Errorf("answer = %+v", ans)
	}
	argv := f.argv(1)
	if v, _ := flagValue(argv, "--allowedTools"); v != "mcp__vibepanel__*" {
		t.Errorf("--allowedTools = %q", v)
	}
	if v, _ := flagValue(argv, "--max-turns"); v != "4" {
		t.Errorf("--max-turns = %q", v)
	}
	if v, _ := flagValue(argv, "--disallowedTools"); v != claudeDisallowed {
		t.Errorf("--disallowedTools = %q", v)
	}
	if v, _ := flagValue(argv, "--permission-mode"); v != "dontAsk" {
		t.Errorf("--permission-mode = %q", v)
	}
	if v, _ := flagValue(argv, "--append-system-prompt-file"); v != filepath.Join(cfg.WorkDir, "ask.md") {
		t.Errorf("--append-system-prompt-file = %q", v)
	}
	if !hasFlag(argv, "--strict-mcp-config") {
		t.Error("no --strict-mcp-config although the help mentions it")
	}
	mcp, ok := flagValue(argv, "--mcp-config")
	if !ok {
		t.Fatal("no --mcp-config")
	}
	var conf struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	// A path to a 0600 file, not inline JSON: argv is world-readable through
	// /proc and the token would be in it.
	if strings.HasPrefix(mcp, "{") {
		t.Fatalf("--mcp-config is inline JSON on argv: %s", mcp)
	}
	if st, err := os.Stat(mcp); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mcp config file: %v %v", st, err)
	}
	raw, err := os.ReadFile(mcp)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatalf("--mcp-config file is not JSON: %v", err)
	}
	srv, ok := conf.Servers["vibepanel"]
	if !ok {
		t.Fatalf("--mcp-config names no vibepanel server: %s", mcp)
	}
	if srv.Type != "stdio" || srv.Command != cfg.SelfBinary || len(srv.Args) != 1 || srv.Args[0] != "mcp" {
		t.Errorf("server = %+v", srv)
	}
	if srv.Env["VIBEPANEL_URL"] != cfg.PanelURL {
		t.Errorf("server env = %v", srv.Env)
	}
	if _, ok := srv.Env["VIBEPANEL_TOOLS_TOKEN"]; ok {
		t.Error("the tools token is in the MCP config")
	}
	tokenPath := srv.Env["VIBEPANEL_TOOLS_TOKEN_FILE"]
	if st, err := os.Stat(tokenPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", st, err)
	}
	if b, _ := os.ReadFile(tokenPath); string(b) != cfg.ToolsToken {
		t.Errorf("token file holds %q", b)
	}
	if hasFlag(argv, "--resume") {
		t.Error("first call for a chat carries --resume")
	}
	if hasFlag(argv, "--json-schema") {
		t.Error("Ask is prose; a schema would make it JSON")
	}
	if strings.Contains(strings.Join(argv, " "), "dangerously") {
		t.Error("argv mentions dangerously")
	}

	// The same chat continues; a different one does not.
	if _, err := r.Ask(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if v, _ := flagValue(f.argv(2), "--resume"); v != "sess-A" {
		t.Errorf("second call --resume = %q, want sess-A", v)
	}
	other := sampleRequest()
	other.Chat = "wecom:7"
	if _, err := r.Ask(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if hasFlag(f.argv(3), "--resume") {
		t.Error("another chat was resumed into the first one's conversation")
	}
}

func TestClaudeAskRetriesOnceWithoutAStaleResume(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("reply", `{"type":"result","is_error":false,"result":"fine","session_id":"sess-new","total_cost_usd":0}`)
	r, _ := newClaudeRunner(t, f)
	r.remember("telegram:42", "sess-old")
	// Every reply file is the same, so make the resumed call fail through
	// the JSON instead: is_error on call 1 only.
	f.set("reply.1", `{"type":"result","is_error":true,"result":"No conversation found with session ID: sess-old"}`)

	ans, err := r.Ask(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "fine" {
		t.Errorf("answer = %+v", ans)
	}
	if v, _ := flagValue(f.argv(1), "--resume"); v != "sess-old" {
		t.Errorf("first attempt --resume = %q", v)
	}
	if hasFlag(f.argv(2), "--resume") {
		t.Error("the retry still carried the resume that just failed")
	}
	if f.calls() != 2 {
		t.Errorf("%d calls, want 2", f.calls())
	}
	if got := r.threadFor("telegram:42"); got != "sess-new" {
		t.Errorf("thread after retry = %q, want the new session", got)
	}
}

func TestAThreadExpiresAfterADay(t *testing.T) {
	r := &Runner{threads: map[string]thread{
		"old": {id: "a", at: time.Now().Add(-25 * time.Hour)},
		"new": {id: "b", at: time.Now().Add(-23 * time.Hour)},
	}}
	if got := r.threadFor("old"); got != "" {
		t.Errorf("a day-old thread was resumed: %q", got)
	}
	if got := r.threadFor("new"); got != "b" {
		t.Errorf("a thread from this day was dropped: %q", got)
	}
}

func TestTheChildEnvironmentIsStrippedOfHookVariablesAndCarriesTheProfile(t *testing.T) {
	// The panel process itself might be running inside a session (a
	// developer's), so these are set here to make sure they are removed
	// rather than merely absent.
	t.Setenv("VIBEPANEL_SESSION_ID", "vp_leak")
	t.Setenv("VIBEPANEL_TOKEN", "leak")
	t.Setenv("VIBEPANEL_URL", "https://leak")
	t.Setenv("VIBEPANEL_PROJECT_ID", "pr_leak")
	t.Setenv("VIBEPANEL_DATA_DIR", "/kept")
	t.Setenv("CLOUDFLARE_API_TOKEN", "cf-secret")
	f := newFake(t, "claude", claudeHelp)
	f.set("reply", `{"type":"result","is_error":false,"structured_output":{"verb":"none","handle":0,"text":"","say":""}}`)
	r, _ := newClaudeRunner(t, f, "ANTHROPIC_BASE_URL=https://proxy.example", "ANTHROPIC_API_KEY=sk-profile")

	if _, _, err := r.Translate(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	env := f.env(1)
	for _, k := range hookVars {
		if v, ok := env[k]; ok {
			t.Errorf("%s=%q reached the child; the hook script would report the assistant as a session", k, v)
		}
	}
	if env["ANTHROPIC_BASE_URL"] != "https://proxy.example" || env["ANTHROPIC_API_KEY"] != "sk-profile" {
		t.Errorf("the profile's env did not reach the child: %v", env)
	}
	// An allowlist: the panel's own variables, hook or not, and anything
	// else in its environment (an ACME token, say) stay out of a child that
	// can run a shell.
	if _, ok := env["VIBEPANEL_DATA_DIR"]; ok {
		t.Error("a panel variable reached the child")
	}
	if _, ok := env["CLOUDFLARE_API_TOKEN"]; ok {
		t.Error("the panel's ACME token reached the child")
	}
	if env["HOME"] != os.Getenv("HOME") {
		t.Errorf("HOME = %q, want the panel's own %q: the login lives there", env["HOME"], os.Getenv("HOME"))
	}
}

func TestANonZeroExitCarriesStderr(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("exit", "1")
	f.set("stderr", "Not logged in. Run claude login.\n"+strings.Repeat("x", 2000))
	r, _ := newClaudeRunner(t, f)
	_, _, err := r.Translate(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("exit 1 was not an error")
	}
	if !strings.Contains(err.Error(), "Not logged in") {
		t.Errorf("error does not carry stderr: %v", err)
	}
	if len(err.Error()) > stderrLimit+100 {
		t.Errorf("error is %d bytes; stderr was not cut", len(err.Error()))
	}
}

func TestAnIsErrorResultIsAnError(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("reply", `{"type":"result","is_error":true,"result":"Invalid API key"}`)
	r, _ := newClaudeRunner(t, f)
	if _, _, err := r.Translate(context.Background(), sampleRequest()); err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("err = %v", err)
	}
}

func TestATimeoutIsAnErrorAndTheChildIsGone(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("sleep", "30")
	r, _ := newClaudeRunner(t, f)
	r.cfg.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, _, err := r.Translate(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("a harness that never answers was not an error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("waited %s for a 300ms timeout: the process group was not killed", time.Since(start))
	}
}

func TestPromptFilesAreWrittenOnceAndAnEditSurvives(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("reply", `{"type":"result","is_error":false,"structured_output":{"verb":"none","handle":0,"text":"","say":""}}`)
	dir := filepath.Join(t.TempDir(), "assistant")
	cfg := Config{Harness: "claude", Binary: f.bin, WorkDir: dir, SelfBinary: "/x", Env: []string{"FAKE_OUT=" + f.dir}}
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"translate.md", "ask.md", "CLAUDE.md", "AGENTS.md"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
		if len(b) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
	// What translate.md must say, because the model reads only that.
	tr, _ := os.ReadFile(filepath.Join(dir, "translate.md"))
	for _, want := range []string{"DATA", "clarify", "上上条", "exactly one session", "send, approve, deny, stop, screen, shot, open, context", "list, mute, unmute, focus, usage, ask, clarify, none"} {
		if !strings.Contains(string(tr), want) {
			t.Errorf("translate.md lacks %q", want)
		}
	}
	note, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(string(note), "no project") {
		t.Error("CLAUDE.md does not say there is no project here")
	}

	edited := "# mine\nAnswer like a pirate.\n"
	if err := os.WriteFile(filepath.Join(dir, "translate.md"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Translate(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "translate.md")); string(got) != edited {
		t.Errorf("translate.md was overwritten:\n%s", got)
	}
}

func TestTheBracketedSpellingOfThePromptFileFlagCounts(t *testing.T) {
	f := newFake(t, "claude", "Usage: claude\n  via: --system-prompt[-file], --append-system-prompt[-file], --add-dir\n")
	f.set("reply", `{"type":"result","is_error":false,"structured_output":{"verb":"none","handle":0,"text":"","say":""}}`)
	r, cfg := newClaudeRunner(t, f)
	if _, _, err := r.Translate(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if v, _ := flagValue(f.argv(1), "--append-system-prompt-file"); v != filepath.Join(cfg.WorkDir, "translate.md") {
		t.Errorf("--append-system-prompt-file = %q; 2.1.273 documents the flag as --append-system-prompt[-file]", v)
	}
}

func TestWithoutThePromptFileFlagTheTextIsPassedInline(t *testing.T) {
	f := newFake(t, "claude", "Usage: claude\n  --append-system-prompt <prompt>\n")
	f.set("reply", `{"type":"result","is_error":false,"structured_output":{"verb":"none","handle":0,"text":"","say":""}}`)
	r, _ := newClaudeRunner(t, f)
	if _, _, err := r.Translate(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	argv := f.argv(1)
	if hasFlag(argv, "--append-system-prompt-file") || hasFlag(argv, "--strict-mcp-config") || hasFlag(argv, "--tools") {
		t.Errorf("flags the help does not mention were passed: %v", argv)
	}
	if v, _ := flagValue(argv, "--append-system-prompt"); !strings.Contains(v, "# Translate") {
		t.Errorf("--append-system-prompt = %q", v)
	}
}

func TestNewRefusesABadConfig(t *testing.T) {
	if _, err := New(Config{Harness: "gemini", WorkDir: t.TempDir(), SelfBinary: "/x"}); err == nil {
		t.Error("an unknown harness was accepted")
	}
	if _, err := New(Config{Harness: "claude", SelfBinary: "/x"}); err == nil {
		t.Error("no WorkDir was accepted")
	}
	if _, err := New(Config{Harness: "claude", WorkDir: t.TempDir(), SelfBinary: "/x", Env: []string{"NOEQUALS"}}); err == nil {
		t.Error("a malformed env entry was accepted")
	}
	r, err := New(Config{Harness: "claude", WorkDir: t.TempDir(), SelfBinary: "/x", BudgetUSD: 2.5})
	if err != nil {
		t.Fatal(err)
	}
	if r.Budget() != 2.5 || r.cfg.MaxTurns != defaultMaxTurns || r.cfg.Timeout != defaultTimeout || r.cfg.Binary != "claude" {
		t.Errorf("defaults: %+v", r.cfg)
	}
}

// ─── codex ─────────────────────────────────────────────────────────────────

func newCodexRunner(t *testing.T, f *fake) (*Runner, Config) {
	t.Helper()
	cfg := Config{
		Harness:    "codex",
		Binary:     f.bin,
		WorkDir:    filepath.Join(t.TempDir(), "assistant"),
		PanelURL:   "https://127.0.0.1:18443",
		ToolsToken: `tok"quoted`,
		SelfBinary: "/opt/vibepanel/bin/vibepanel",
		Env:        []string{"FAKE_OUT=" + f.dir},
		Model:      "gpt-5-codex",
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r, cfg
}

func TestCodexTranslateArgvAndOutputFile(t *testing.T) {
	f := newFake(t, "codex", "codex exec\n")
	f.set("last", `{"verb":"approve","handle":2,"text":"","say":""}`)
	f.set("reply", `{"type":"thread.started","thread_id":"t-1"}`+"\n"+`{"type":"turn.completed"}`+"\n")
	r, cfg := newCodexRunner(t, f)

	in, cost, err := r.Translate(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if in.Verb != "approve" || in.Handle != 2 {
		t.Errorf("intent = %+v", in)
	}
	if cost != 0 {
		t.Errorf("cost = %v; codex reports none", cost)
	}
	argv := f.argv(1)
	if argv[0] != "exec" || !hasFlag(argv, "--json") || !hasFlag(argv, "--skip-git-repo-check") {
		t.Errorf("argv = %v", argv)
	}
	if v, _ := flagValue(argv, "--sandbox"); v != "read-only" {
		t.Errorf("--sandbox = %q", v)
	}
	if v, _ := flagValue(argv, "-C"); v != cfg.WorkDir {
		t.Errorf("-C = %q", v)
	}
	if v, _ := flagValue(argv, "-m"); v != "gpt-5-codex" {
		t.Errorf("-m = %q", v)
	}
	schema := f.read("schema.1")
	if !strings.Contains(schema, `"clarify"`) || !strings.Contains(schema, `"additionalProperties":false`) {
		t.Errorf("--output-schema file: %q", schema)
	}
	if out, ok := flagValue(argv, "-o"); !ok || !strings.HasPrefix(out, cfg.WorkDir) {
		t.Errorf("-o = %q, want a file under the working directory", out)
	}
	if strings.Contains(strings.Join(argv, " "), "dangerously") {
		t.Error("argv mentions dangerously")
	}
	// The prompt file, prepended: Codex has no system prompt flag.
	last := argv[len(argv)-1]
	if !strings.HasPrefix(last, "# Translate") || !strings.Contains(last, "<<<\ntell the api one to add tests\n>>>") {
		t.Errorf("prompt = %q", last)
	}
	// Nothing of the call is left behind.
	entries, _ := os.ReadDir(cfg.WorkDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "schema-") || strings.HasPrefix(e.Name(), "out-") {
			t.Errorf("temporary file %s was left in the working directory", e.Name())
		}
	}
}

func TestCodexAskArgvAndResume(t *testing.T) {
	f := newFake(t, "codex", "codex exec\n")
	f.set("last", "[2] is waiting for you to approve a command.")
	f.set("reply", `{"type":"thread.started","thread_id":"019a-thread"}`+"\n"+`{"type":"item.completed","item":{"type":"agent_message","text":"..."}}`+"\n")
	r, cfg := newCodexRunner(t, f)

	ans, err := r.Ask(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "[2] is waiting for you to approve a command." || ans.CostUSD != 0 {
		t.Errorf("answer = %+v", ans)
	}
	argv := f.argv(1)
	joined := strings.Join(argv, "\n")
	for _, want := range []string{
		`mcp_servers.vibepanel.command="/opt/vibepanel/bin/vibepanel"`,
		`mcp_servers.vibepanel.args=["mcp"]`,
		`mcp_servers.vibepanel.env.VIBEPANEL_URL="https://127.0.0.1:18443"`,
		`mcp_servers.vibepanel.env.VIBEPANEL_TOOLS_TOKEN_FILE="` + filepath.Join(cfg.WorkDir, "tools.token") + `"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "tok\"quoted") {
		t.Fatal("the tools token is on argv")
	}
	if v, _ := flagValue(argv, "--sandbox"); v != "read-only" {
		t.Errorf("--sandbox = %q", v)
	}
	if hasFlag(argv, "--output-schema") {
		t.Error("Ask is prose; a schema would make it JSON")
	}
	if argv[1] == "resume" {
		t.Error("first call resumed")
	}

	if _, err := r.Ask(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	argv = f.argv(2)
	if argv[0] != "exec" || argv[1] != "resume" || argv[2] != "019a-thread" {
		t.Errorf("second call argv = %v, want exec resume 019a-thread ...", argv[:3])
	}
	if hasFlag(argv, "-C") || hasFlag(argv, "--sandbox") {
		t.Error("exec resume does not accept -C or --sandbox (codex-cli 0.153.4)")
	}
	if !strings.Contains(strings.Join(argv, "\n"), `sandbox_mode="read-only"`) {
		t.Error("the resumed thread is not pinned to the read-only sandbox")
	}
	if !strings.Contains(strings.Join(argv, "\n"), "mcp_servers.vibepanel.command=") {
		t.Error("the resumed thread lost the panel's MCP server")
	}
	if v, _ := flagValue(argv, "-C"); v == cfg.WorkDir {
		t.Error("-C on resume")
	}

	other := sampleRequest()
	other.Chat = "wecom:7"
	if _, err := r.Ask(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if f.argv(3)[1] == "resume" {
		t.Error("another chat resumed the first one's thread")
	}
}

func TestCodexWithoutAThreadIDStartsFreshEachTime(t *testing.T) {
	f := newFake(t, "codex", "codex exec\n")
	f.set("last", "ok")
	f.set("reply", `{"type":"turn.completed"}`+"\n")
	r, _ := newCodexRunner(t, f)
	for i := 0; i < 2; i++ {
		if _, err := r.Ask(context.Background(), sampleRequest()); err != nil {
			t.Fatal(err)
		}
	}
	if f.argv(2)[1] == "resume" {
		t.Error("resumed with no thread id to resume")
	}
}

func TestCodexEmptyLastMessageIsAnError(t *testing.T) {
	f := newFake(t, "codex", "codex exec\n")
	f.set("last", "")
	f.set("reply", "")
	r, _ := newCodexRunner(t, f)
	if _, err := r.Ask(context.Background(), sampleRequest()); err == nil {
		t.Error("an empty answer was returned as prose")
	}
	if _, _, err := r.Translate(context.Background(), sampleRequest()); err == nil {
		t.Error("an empty intent was accepted")
	}
}

func TestCodexThreadID(t *testing.T) {
	if got := codexThreadID([]byte("garbage\n{\"type\":\"thread.started\",\"thread_id\":\"x\"}\n")); got != "x" {
		t.Errorf("got %q", got)
	}
	if got := codexThreadID([]byte(`{"type":"item.completed","thread_id":"y"}`)); got != "" {
		t.Errorf("a thread_id on another event was taken: %q", got)
	}
}

func TestContextCancellationIsNotRetried(t *testing.T) {
	f := newFake(t, "claude", claudeHelp)
	f.set("sleep", "30")
	r, _ := newClaudeRunner(t, f)
	r.remember("telegram:42", "sess")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := r.Ask(ctx, sampleRequest())
	if err == nil {
		t.Fatal("no error")
	}
	if f.calls() != 1 {
		t.Errorf("%d calls: a cancelled call was retried", f.calls())
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("test setup")
	}
	// The conversation is kept: the person went away, the harness did not
	// forget anything, and the next question should still follow this one.
	if got := r.threadFor("telegram:42"); got != "sess" {
		t.Errorf("thread after a cancelled call = %q; a cancel is not a failed resume", got)
	}
}
