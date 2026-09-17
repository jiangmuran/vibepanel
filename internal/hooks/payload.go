package hooks

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a hook says besides its state.
//
// Every agent the panel wires up hands its hook a JSON document on stdin in
// Claude Code's shape: `hook_event_name`, `session_id`, `transcript_path`, and
// per event a `message`, a `last_assistant_message`, a `prompt` or a
// `tool_name` with its `tool_input`. Codex's hooks.json, Kimi Code and zcode
// all copied the shape, which is why one reader serves four agents. report.sh
// forwards that document untouched; this file is where it is read, and it is
// the only place in the panel that knows the agents' vocabulary. What comes
// out is one of four kinds and a bounded string.
//
// Everything here is untrusted (red line 6). The document was produced by a
// program on the user's machine and shaped by whatever their hook
// configuration says, and the text inside it was written by a model reading
// the internet. It is read with a size cap, decoded as UTF-8 with the
// invalid bytes replaced, stripped of control characters, and never
// interpreted. A hook that carries nothing usable yields ok == false and the
// state report goes through exactly as it did before this file existed.

// Report is what one hook document said, in the panel's terms.
type Report struct {
	// Event is the agent's own event name, kept for the log; the panel's
	// decisions are made on Kind.
	Event string
	// Kind is one of the four store kinds, named here as literals for the
	// same reason the states are (red line 3): this package does not import
	// the store, and TestPayloadKindsAreTheStoreKinds pins the spelling.
	Kind string
	// Text is what to show a person: the agent's last message, the prompt it
	// is waiting on, the question it asked, the line the person typed.
	Text string
	// TranscriptPath is where the agent says it writes its transcript, if the
	// document carried one.
	TranscriptPath string
	// Interrupted marks an Interrupt event, which carries no text but is worth
	// a line in the chat.
	Interrupted bool
	// Menu is set when the agent is about to show a menu rather than ask for
	// permission: Claude Code's AskUserQuestion (questions with options) or
	// ExitPlanMode (approve a plan). Kind is then question.
	Menu *Menu
}

// Menu is a choice an agent puts on the screen, read from the tool call that
// draws it.
//
// Claude Code announces both with the same Notification it uses for a
// permission prompt, "Claude needs your permission", which says nothing
// about what is being asked; answered with allow and deny, Enter picked the
// first question's highlighted option and Escape threw the whole set away.
// The tool call's input, which PreToolUse carries, is the only place the
// questions and options exist.
type Menu struct {
	// Tool is AskUserQuestion or ExitPlanMode.
	Tool      string         `json:"tool"`
	Questions []MenuQuestion `json:"questions"`
	// Plan is the plan text, for ExitPlanMode.
	Plan string `json:"plan,omitempty"`
}

// MenuQuestion is one question of a menu.
type MenuQuestion struct {
	Header   string       `json:"header,omitempty"`
	Question string       `json:"question"`
	Options  []MenuOption `json:"options"`
	Multi    bool         `json:"multi,omitempty"`
	// Preview is whether the options carry previews, which changes the
	// screen: no "Type something" row, and notes on an option instead.
	Preview bool `json:"preview,omitempty"`
}

// MenuOption is one choice.
type MenuOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Menu tools.
const (
	ToolAskUserQuestion = "AskUserQuestion"
	ToolExitPlanMode    = "ExitPlanMode"
)

// maxMenuQuestions and maxMenuOptions bound what a hook document can make the
// panel store and draw; Claude Code asks at most four of each.
const (
	maxMenuQuestions = 8
	maxMenuOptions   = 9
)

// readMenu reads a menu from a tool call, or nil when the tool is not one or
// its input is not the shape that draws a menu.
func readMenu(tool string, input json.RawMessage) *Menu {
	switch tool {
	case ToolAskUserQuestion:
		var in struct {
			Questions []struct {
				Header      string `json:"header"`
				Question    string `json:"question"`
				MultiSelect bool   `json:"multiSelect"`
				Options     []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
					Preview     string `json:"preview"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(input, &in) != nil || len(in.Questions) == 0 || len(in.Questions) > maxMenuQuestions {
			return nil
		}
		m := &Menu{Tool: tool}
		for _, q := range in.Questions {
			if len(q.Options) == 0 || len(q.Options) > maxMenuOptions {
				return nil
			}
			mq := MenuQuestion{Header: Clean(q.Header, 64), Question: Clean(q.Question, 1024), Multi: q.MultiSelect}
			for _, o := range q.Options {
				if o.Preview != "" {
					mq.Preview = true
				}
				mq.Options = append(mq.Options, MenuOption{Label: Clean(o.Label, 256), Description: Clean(o.Description, 512)})
			}
			m.Questions = append(m.Questions, mq)
		}
		return m
	case ToolExitPlanMode:
		var in struct {
			Plan string `json:"plan"`
		}
		_ = json.Unmarshal(input, &in)
		return &Menu{Tool: tool, Plan: Clean(in.Plan, MaxText)}
	}
	return nil
}

// Summary is a menu as plain text: what "context" shows and what the audit
// and a phone without the menu's own rendering read.
func (m *Menu) Summary() string {
	if m.Tool == ToolExitPlanMode {
		return "Plan:\n" + m.Plan
	}
	var b strings.Builder
	for i, q := range m.Questions {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if q.Header != "" {
			b.WriteString(q.Header + ": ")
		}
		b.WriteString(q.Question)
		for j, o := range q.Options {
			fmt.Fprintf(&b, "\n%d. %s", j+1, o.Label)
		}
	}
	return b.String()
}

// The kinds a Report may carry. See Report.Kind.
const (
	KindAssistant = "assistant"
	KindPrompt    = "prompt"
	KindQuestion  = "question"
	KindNotice    = "notice"
	KindUser      = "user"
)

// MaxPayload is the most of a hook document that is read.
//
// A Stop document carries the agent's whole last message, and an agent that
// pasted a file into its answer produces a large one. 256 KiB is far past any
// message worth pushing to a phone, and the point of the bound is that the
// size of a request to the hook endpoint is chosen here rather than by
// whatever a model wrote.
const MaxPayload = 256 << 10

// MaxText is the longest string a Report carries.
//
// What a chat shows of it is shorter still; this bounds what the database
// keeps, so "context" on a phone can ask for the whole of a message that fit.
const MaxText = 16 << 10

// hookDocument is the union of every field any agent's hook is read for.
// Unknown fields are ignored, which is what lets four agents share it.
type hookDocument struct {
	Event            string          `json:"hook_event_name"`
	TranscriptPath   string          `json:"transcript_path"`
	Message          string          `json:"message"`
	Title            string          `json:"title"`
	NotificationType string          `json:"notification_type"`
	LastAssistant    string          `json:"last_assistant_message"`
	Prompt           string          `json:"prompt"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
}

// Extract reads a hook document and says what it carried.
//
// ok is false for anything that is not a JSON object with an event the panel
// has a use for. That includes the empty document, which is what a report from
// a script older than this reader sends.
func Extract(raw []byte) (Report, bool) {
	if len(raw) > MaxPayload {
		raw = raw[:MaxPayload]
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || raw[0] != '{' {
		return Report{}, false
	}
	var doc hookDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		// A truncated document is the likely case: the cap above cut a Stop
		// message in half. Losing the message is the right outcome; losing
		// the state report is not, and the caller makes sure it is not lost.
		return Report{}, false
	}
	r := Report{Event: doc.Event, TranscriptPath: Clean(doc.TranscriptPath, 4096)}
	switch doc.Event {
	case "Stop":
		r.Kind, r.Text = KindAssistant, Clean(doc.LastAssistant, MaxText)
	case "Notification":
		r.Text = Clean(doc.Message, MaxText)
		switch doc.NotificationType {
		case "permission_prompt":
			r.Kind = KindPrompt
		case "idle_prompt":
			// Not a question. Claude Code says it sixty seconds after every
			// turn it finished, and as a question it went to a phone as one:
			// "Claude is waiting for your input", a minute after the answer
			// it is waiting about, with a hint saying how to reply. The answer
			// itself was already sent, from Stop. Like a tool call, it is kept
			// for the transcript path alone.
			if r.TranscriptPath == "" {
				return Report{}, false
			}
			return Report{Event: r.Event, TranscriptPath: r.TranscriptPath}, true
		case "agent_needs_input", "elicitation_dialog", "elicitation_url_dialog":
			r.Kind = KindQuestion
		default:
			r.Kind = KindNotice
		}
	case "Elicitation":
		r.Kind, r.Text = KindQuestion, Clean(doc.Prompt, MaxText)
	case "UserPromptSubmit":
		r.Kind, r.Text = KindUser, Clean(doc.Prompt, MaxText)
	case "Interrupt":
		r.Kind, r.Interrupted = KindNotice, true
	case "PreToolUse", "PermissionRequest":
		// A menu is drawn by a tool call; any other tool before it runs is a
		// state and nothing to show, and a PermissionRequest for any other
		// tool is the permission prompt above.
		if m := readMenu(doc.ToolName, doc.ToolInput); m != nil {
			r.Kind, r.Menu, r.Text = KindQuestion, m, Clean(m.Summary(), MaxText)
			break
		}
		if doc.Event == "PermissionRequest" {
			r.Kind, r.Text = KindPrompt, describeTool(doc.ToolName, doc.ToolInput)
			break
		}
		if r.TranscriptPath == "" {
			return Report{}, false
		}
		return r, true
	default:
		// PreToolUse, PostToolUse, SessionStart and the rest carry a state
		// and nothing a person asked to be told. The transcript path is
		// still worth having from any of them.
		if r.TranscriptPath == "" {
			return Report{}, false
		}
		return r, true
	}
	if r.Text == "" && !r.Interrupted && r.TranscriptPath == "" {
		return Report{}, false
	}
	return r, true
}

// describeTool turns a permission request into the line a person decides on.
//
// A shell command is the command; a file edit is the path; anything else is
// the tool's name with its input compacted after it. What matters is that the
// person on the phone sees *what* they are allowing, not that it is a "Bash".
func describeTool(name string, input json.RawMessage) string {
	name = Clean(name, 64)
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := in[k].(string); ok && v != "" {
				return Clean(v, MaxText)
			}
		}
		return ""
	}
	if v := pick("command", "cmd"); v != "" {
		return name + ": " + v
	}
	if v := pick("file_path", "path", "notebook_path"); v != "" {
		return name + ": " + v
	}
	if v := pick("url", "query", "pattern", "prompt", "description"); v != "" {
		return name + ": " + v
	}
	if len(in) == 0 {
		return name
	}
	compact, err := json.Marshal(in)
	if err != nil {
		return name
	}
	return name + " " + Clean(string(compact), 512)
}

// Clean makes a string safe to store and show: valid UTF-8, no control
// characters except newline and tab, no terminal escape sequences, at most
// max runes, whitespace trimmed.
//
// Escape sequences are dropped whole rather than by their first byte. An
// agent that quoted a coloured command line hands the hook `ESC [ 3 1 m`, and
// taking only the ESC leaves `[31m` in the middle of a sentence on a phone.
//
// Runes rather than bytes, so a cut lands between characters and a Chinese
// message does not end in half a glyph.
func Clean(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	var b strings.Builder
	n := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b:
			i = skipEscape(rs, i)
			continue
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r' || unicode.IsControl(r) || r == 0xFFFE || r == 0xFFFF:
			continue
		default:
			b.WriteRune(r)
		}
		n++
		if n >= max {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// skipEscape returns the index of the last rune of the escape sequence that
// starts at rs[i], which the caller's loop then steps past.
//
// CSI (`ESC [`) ends at the first byte in 0x40..0x7E; OSC (`ESC ]`) ends at
// BEL or `ESC \`; anything else is a two-byte sequence. An unterminated one
// runs to the end of the string, which drops the tail -- the right outcome for
// text that was cut in the middle of an escape.
func skipEscape(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return len(rs)
	}
	switch rs[i+1] {
	case '[':
		for j := i + 2; j < len(rs); j++ {
			if rs[j] >= 0x40 && rs[j] <= 0x7e {
				return j
			}
		}
		return len(rs)
	case ']':
		for j := i + 2; j < len(rs); j++ {
			if rs[j] == 0x07 {
				return j
			}
			if rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
		}
		return len(rs)
	}
	return i + 1
}
