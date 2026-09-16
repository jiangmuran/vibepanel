package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// Codex as the harness: `codex exec`, verified against codex-cli 0.153.4.
//
// Differences from Claude Code that shape this file:
//
//   - There is no system-prompt flag, so the prompt file is prepended to the
//     user turn; AGENTS.md in the working directory is the other thing Codex
//     reads on its own, and it says there is no project here.
//   - The answer is read from the file -o names, not from stdout: stdout is
//     the event stream, and the only thing taken from it is the thread id.
//   - Codex reports no cost, so every call costs 0 against the budget.
//   - `codex exec resume` accepts neither -C nor --sandbox: the thread keeps
//     its directory, and the sandbox is pinned with -c sandbox_mode instead.
//   - Its read-only sandbox still has a shell. See the package comment.

// codexThreadEvent is the first event of `codex exec --json`.
type codexThreadEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
}

func (r *Runner) codexTranslate(ctx context.Context, prompt string) (chat.Intent, float64, error) {
	schema, err := r.tempFile("schema-*.json", []byte(intentSchema))
	if err != nil {
		return chat.Intent{}, 0, err
	}
	defer os.Remove(schema)
	out, err := r.tempFile("out-*.txt", nil)
	if err != nil {
		return chat.Intent{}, 0, err
	}
	defer os.Remove(out)

	args := []string{
		"exec", "--json",
		"--output-schema", schema,
		"-C", r.cfg.WorkDir,
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"-o", out,
	}
	if r.cfg.Model != "" {
		args = append(args, "-m", r.cfg.Model)
	}
	args = append(args, r.codexPrompt(translateFile, prompt))
	if _, err := r.run(ctx, args); err != nil {
		return chat.Intent{}, 0, err
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return chat.Intent{}, 0, fmt.Errorf("codex left no last message: %w", err)
	}
	in, err := parseIntent(bytes.TrimSpace(raw))
	if err != nil {
		return chat.Intent{}, 0, err
	}
	return in, 0, nil
}

func (r *Runner) codexAsk(ctx context.Context, prompt, resume string) (chat.AssistantAnswer, string, error) {
	out, err := r.tempFile("out-*.txt", nil)
	if err != nil {
		return chat.AssistantAnswer{}, "", err
	}
	defer os.Remove(out)

	var args []string
	if resume != "" {
		args = []string{"exec", "resume", resume, "--json", "-c", `sandbox_mode="read-only"`}
	} else {
		args = []string{"exec", "--json", "-C", r.cfg.WorkDir, "--sandbox", "read-only"}
	}
	args = append(args, "--skip-git-repo-check")
	args = append(args, r.codexMCP()...)
	args = append(args, "-o", out)
	if r.cfg.Model != "" {
		args = append(args, "-m", r.cfg.Model)
	}
	args = append(args, r.codexPrompt(askFile, prompt))
	stdout, err := r.run(ctx, args)
	if err != nil {
		return chat.AssistantAnswer{}, "", err
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return chat.AssistantAnswer{}, "", fmt.Errorf("codex left no last message: %w", err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return chat.AssistantAnswer{}, "", errors.New("codex answered nothing")
	}
	return chat.AssistantAnswer{Text: text}, codexThreadID(stdout), nil
}

// codexMCP is the panel's MCP server as -c overrides. TOML values, quoted
// with the JSON encoder because a TOML basic string escapes the same way
// and the token is opaque bytes we did not choose.
func (r *Runner) codexMCP() []string {
	q := func(s string) string { return mustJSON(s) }
	return []string{
		"-c", "mcp_servers.vibepanel.command=" + q(r.cfg.SelfBinary),
		"-c", `mcp_servers.vibepanel.args=["mcp"]`,
		"-c", "mcp_servers.vibepanel.env.VIBEPANEL_URL=" + q(r.cfg.PanelURL),
		"-c", "mcp_servers.vibepanel.env.VIBEPANEL_TOOLS_TOKEN_FILE=" + q(r.tokenPathOrEmpty()),
	}
}

// codexPrompt is the prompt file followed by the turn, because Codex has no
// system-prompt flag. Read per call so an edit lands on the next one.
func (r *Runner) codexPrompt(promptFile, prompt string) string {
	body, err := os.ReadFile(r.promptPath(promptFile))
	if err != nil {
		return prompt
	}
	return strings.TrimSpace(string(body)) + "\n\n---\n\n" + prompt
}

// codexThreadID finds the thread in the event stream, for `exec resume`.
// Empty when there is none, in which case the next question starts fresh.
func codexThreadID(stdout []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev codexThreadEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type == "thread.started" && ev.ThreadID != "" {
			return ev.ThreadID
		}
	}
	return ""
}

// tokenPathOrEmpty is the tools token's file for the MCP server's
// environment; an error leaves it empty and the MCP server refuses to
// start, which the answer then says.
func (r *Runner) tokenPathOrEmpty() string {
	p, err := r.tokenFile()
	if err != nil {
		return ""
	}
	return p
}
