package headless

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// RunArgs is one run's argv after the binary, in the contract's order:
//
//	-p <prompt> --output-format stream-json --verbose --include-partial-messages
//	(--session-id|--resume) <id> --model <m> --permission-mode <pm>
//	--append-system-prompt <sp>
//
// stream-json requires --verbose under -p; --include-partial-messages is what
// produces the text deltas the glasses draw as they arrive.
func RunArgs(prompt, sessionID string, resume bool, model, permissionMode, systemPrompt string) []string {
	// A prompt that starts with a dash would be read as an option -- `-p` is
	// a switch and the prompt is a positional argument. A leading space is
	// invisible to the model and keeps it a positional.
	if strings.HasPrefix(prompt, "-") {
		prompt = " " + prompt
	}
	sessionFlag := "--session-id"
	if resume {
		sessionFlag = "--resume"
	}
	return []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		sessionFlag, sessionID,
		"--model", model,
		"--permission-mode", permissionMode,
		"--append-system-prompt", systemPrompt,
	}
}

// NewSessionID is a random (v4) UUID, which is what --session-id requires.
func NewSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("headless: crypto/rand failed: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ValidSessionID reports whether s is a UUID. It is the only shape accepted
// for a session id anywhere here, because the id becomes a file name under
// the transcripts directory and a path segment must not be able to climb.
func ValidSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// Event is one normalised event: its type and its fields, "t" excluded.
// Seq is assigned by the run that buffers it.
type Event struct {
	Seq    int64
	Type   string
	Fields map[string]any
}

// JSON is the event as the SSE data line carries it.
func (e Event) JSON() []byte {
	m := make(map[string]any, len(e.Fields)+1)
	for k, v := range e.Fields {
		m[k] = v
	}
	m["t"] = e.Type
	b, _ := json.Marshal(m)
	return b
}

// Terminal reports whether the event ends a run.
func (e Event) Terminal() bool {
	return e.Type == "result" || e.Type == "error" || e.Type == "stopped"
}

// streamLine is the subset of Claude Code's stream-json lines this reads.
type streamLine struct {
	Type            string  `json:"type"`
	Subtype         string  `json:"subtype"`
	SessionID       string  `json:"session_id"`
	Model           string  `json:"model"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Event           *struct {
		Type  string `json:"type"`
		Delta *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	// result
	Result       string  `json:"result"`
	IsError      bool    `json:"is_error"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	DurationMS   int64   `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Text      string          `json:"text"`
}

// Normalize turns one stream-json line into the events the glasses get.
// Lines it does not recognise -- and Claude Code adds new kinds -- produce
// nothing rather than an error: a run must not fail because the harness
// grew a field.
//
// Only the top level is reported (parent_tool_use_id null). A subagent's
// text and tool calls are the subagent's working, and on a 576×288 screen
// they would bury the answer.
func Normalize(line []byte) []Event {
	var l streamLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil
	}
	if l.ParentToolUseID != nil && *l.ParentToolUseID != "" {
		return nil
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			return []Event{{Type: "init", Fields: map[string]any{"sessionId": l.SessionID, "model": l.Model}}}
		}
	case "stream_event":
		if l.Event != nil && l.Event.Type == "content_block_delta" && l.Event.Delta != nil &&
			l.Event.Delta.Type == "text_delta" && l.Event.Delta.Text != "" {
			return []Event{{Type: "text", Fields: map[string]any{"d": l.Event.Delta.Text}}}
		}
	case "assistant":
		var out []Event
		for _, b := range blocks(l.Message) {
			if b.Type == "tool_use" {
				out = append(out, Event{Type: "tool", Fields: map[string]any{
					"id": b.ID, "name": b.Name, "summary": ToolSummary(b.Input),
				}})
			}
		}
		return out
	case "user":
		var out []Event
		for _, b := range blocks(l.Message) {
			if b.Type == "tool_result" {
				out = append(out, Event{Type: "tool_done", Fields: map[string]any{
					"id": b.ToolUseID, "ok": !b.IsError,
				}})
			}
		}
		return out
	case "result":
		return []Event{{Type: "result", Fields: map[string]any{
			"text": l.Result, "isError": l.IsError, "costUsd": l.TotalCostUSD,
			"ms": l.DurationMS, "turns": l.NumTurns,
		}}}
	}
	return nil
}

// blocks reads message.content, which is a string or a list of blocks.
func blocks(m *struct {
	Content json.RawMessage `json:"content"`
}) []contentBlock {
	if m == nil || len(m.Content) == 0 || m.Content[0] != '[' {
		return nil
	}
	var out []contentBlock
	_ = json.Unmarshal(m.Content, &out)
	return out
}

// summaryKeys are the tool-input fields that say what a call is about, in
// the order they are tried: a path, a command, a search, an address.
var summaryKeys = []string{"file_path", "notebook_path", "path", "command", "query", "url", "pattern", "description", "prompt", "skill"}

// ToolSummary is the one line a tool call is shown as: its most telling
// input field, at most 80 characters.
func ToolSummary(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range summaryKeys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return clip(oneLine(v), 80)
		}
	}
	return ""
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clip cuts s to n runes, with an ellipsis when it cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
