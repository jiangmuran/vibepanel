package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// Claude Code as the harness: `claude -p`, verified against 2.1.273.

// claudeDisallowed is every built-in tool that reads, writes, runs or
// browses. Listed by name rather than relying on the working directory
// being empty, because Read and Glob take absolute paths, and Bash takes
// anything. Ask adds the panel's MCP tools on top; Translate adds nothing.
const claudeDisallowed = "Bash,Edit,Write,Read,Glob,Grep,WebFetch,WebSearch,Task,NotebookEdit,TodoWrite"

// claudeSettings is passed as --settings. An empty hooks block on top of the
// person's own settings, in which the panel's reporter hook is installed:
// the stripped environment already makes that hook exit at its first line,
// and this is the second reason it does not fire.
const claudeSettings = `{"hooks":{}}`

// claudeResult is the shape of --output-format json.
type claudeResult struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	SessionID        string          `json:"session_id"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// claudeCommon is what both calls pass: no interactive permission prompt
// (dontAsk refuses rather than waits; bypassPermissions is never used), the
// built-in tools off, the person's hooks off, our system prompt on.
func (r *Runner) claudeCommon(promptFile string) []string {
	args := []string{
		"--output-format", "json",
		"--permission-mode", "dontAsk",
		"--disallowedTools", claudeDisallowed,
		"--settings", claudeSettings,
	}
	// 2.1.273 lists it as "--append-system-prompt[-file]", so both
	// spellings count.
	if r.helpMentions("--append-system-prompt-file") || r.helpMentions("--append-system-prompt[-file]") {
		args = append(args, "--append-system-prompt-file", r.promptPath(promptFile))
	} else {
		// Older builds take the text only. Same file, read now, so an edit
		// still lands on the next call.
		body, err := os.ReadFile(r.promptPath(promptFile))
		if err == nil {
			args = append(args, "--append-system-prompt", string(body))
		}
	}
	if r.cfg.Model != "" {
		args = append(args, "--model", r.cfg.Model)
	}
	return args
}

func (r *Runner) claudeTranslate(ctx context.Context, prompt string) (chat.Intent, float64, error) {
	args := []string{"-p", prompt, "--json-schema", intentSchema, "--max-turns", "1"}
	args = append(args, r.claudeCommon(translateFile)...)
	if r.helpMentions("--tools ") {
		// "" is the documented way to remove every built-in tool. The
		// disallowed list stays for a build that predates the flag.
		args = append(args, "--tools", "")
	}
	if r.helpMentions("--strict-mcp-config") {
		// With no --mcp-config of our own, strict means none at all: the
		// person's own MCP servers from ~/.claude.json are tools too.
		args = append(args, "--strict-mcp-config")
	}
	out, err := r.run(ctx, args)
	if err != nil {
		return chat.Intent{}, 0, err
	}
	res, err := parseClaudeResult(out)
	if err != nil {
		return chat.Intent{}, 0, err
	}
	raw := res.StructuredOutput
	if len(raw) == 0 || string(raw) == "null" {
		// A build without structured output puts the JSON in the text.
		raw = json.RawMessage(strings.TrimSpace(res.Result))
	}
	in, err := parseIntent(raw)
	if err != nil {
		return chat.Intent{}, res.TotalCostUSD, err
	}
	return in, res.TotalCostUSD, nil
}

func (r *Runner) claudeAsk(ctx context.Context, prompt, resume string) (chat.AssistantAnswer, string, error) {
	args := []string{
		"-p", prompt,
		"--max-turns", strconv.Itoa(r.cfg.MaxTurns),
		"--allowedTools", "mcp__vibepanel__*",
		"--mcp-config", r.mcpConfig(),
	}
	if r.helpMentions("--strict-mcp-config") {
		args = append(args, "--strict-mcp-config")
	}
	args = append(args, r.claudeCommon(askFile)...)
	if resume != "" {
		args = append(args, "--resume", resume)
	}
	out, err := r.run(ctx, args)
	if err != nil {
		return chat.AssistantAnswer{}, "", err
	}
	res, err := parseClaudeResult(out)
	if err != nil {
		return chat.AssistantAnswer{}, "", err
	}
	text := strings.TrimSpace(res.Result)
	if text == "" {
		return chat.AssistantAnswer{}, "", errors.New("claude answered nothing")
	}
	return chat.AssistantAnswer{Text: text, CostUSD: res.TotalCostUSD}, res.SessionID, nil
}

// mcpConfig is the inline --mcp-config: this binary, as `vibepanel mcp`,
// told where the panel is and what to say to it. Inline rather than a file
// so the token is never on disk.
func (r *Runner) mcpConfig() string {
	return mustJSON(map[string]any{
		"mcpServers": map[string]any{
			"vibepanel": map[string]any{
				"type":    "stdio",
				"command": r.cfg.SelfBinary,
				"args":    []string{"mcp"},
				"env": map[string]string{
					"VIBEPANEL_URL":         r.cfg.PanelURL,
					"VIBEPANEL_TOOLS_TOKEN": r.cfg.ToolsToken,
				},
			},
		},
	})
}

func parseClaudeResult(out []byte) (claudeResult, error) {
	var res claudeResult
	if err := json.Unmarshal(out, &res); err != nil {
		return res, fmt.Errorf("claude's output was not the JSON result: %w", err)
	}
	if res.IsError {
		msg := strings.TrimSpace(res.Result)
		if msg == "" {
			msg = res.Subtype
		}
		if len(msg) > stderrLimit {
			msg = msg[:stderrLimit]
		}
		return res, fmt.Errorf("claude: %s", msg)
	}
	return res, nil
}
