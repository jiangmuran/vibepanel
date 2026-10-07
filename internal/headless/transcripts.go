package headless

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Claude Code keeps a conversation as
// <config dir>/projects/<cwd, every byte outside [A-Za-z0-9] as '-'>/<session id>.jsonl.
// Checked against a real ~/.claude/projects: /home/jmr/.local/share/x is
// -home-jmr--local-share-x (the dot is a dash too, hence the double dash).

// ProjectKey is the directory name Claude Code files a cwd's transcripts under.
//
// Claude Code does this with a JavaScript regex over a UTF-16 string, so a
// character outside ASCII is one dash per UTF-16 unit, not one per byte: 助理
// is two dashes, and a character beyond the BMP is two.
func ProjectKey(cwd string) string {
	var b strings.Builder
	for _, c := range cwd {
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			b.WriteRune(c)
		case c > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// TranscriptDir is where cwd's transcripts are under configDir.
func TranscriptDir(configDir, cwd string) string {
	return filepath.Join(configDir, "projects", ProjectKey(cwd))
}

// SessionInfo is one transcript in the list.
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
	Turns     int    `json:"turns"`
	Running   bool   `json:"running"`
	Live      bool   `json:"live"`
	// modified is the file's mtime, for the caller's "live" judgement.
	modified time.Time
}

// Modified is the transcript file's modification time.
func (s SessionInfo) Modified() time.Time { return s.modified }

// Tool is a tool call as the history shows it.
type Tool struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// Message is one turn in the history.
type Message struct {
	Role  string `json:"role"`
	Text  string `json:"text"`
	Tools []Tool `json:"tools"`
	At    int64  `json:"at"`
}

// transcriptLine is the subset of a transcript line read here.
type transcriptLine struct {
	Type        string `json:"type"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Message     *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// timePrefix is the "[2026-10-07 14:03]" the glasses put in front of what
// was said; a title is about what was said.
var timePrefix = regexp.MustCompile(`^\s*\[[^\]\n]{1,40}\]\s*`)

// userText is the person's words on a user line, or "" when the line is not
// something a person said: a tool result, a command echo, a reminder.
func userText(l transcriptLine) string {
	if l.Type != "user" || l.IsMeta || l.IsSidechain || l.Message == nil || len(l.Message.Content) == 0 {
		return ""
	}
	var text string
	switch l.Message.Content[0] {
	case '"':
		_ = json.Unmarshal(l.Message.Content, &text)
	case '[':
		var bs []contentBlock
		_ = json.Unmarshal(l.Message.Content, &bs)
		var parts []string
		for _, b := range bs {
			switch b.Type {
			case "tool_result":
				return ""
			case "text":
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(text)
	// Slash commands and their output are recorded as user lines wrapped in
	// tags (<command-name>, <local-command-stdout>), and so are system
	// reminders. None is something the person said.
	if text == "" || strings.HasPrefix(text, "<") || strings.HasPrefix(text, "Caveat:") {
		return ""
	}
	return text
}

func lineTime(ts string) int64 {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// summary is what the list needs from one file.
type summary struct {
	title string
	turns int
}

// summaryCache remembers each file's title and turn count by path, size and
// mtime: the list is polled, and a transcript is megabytes.
type summaryCache struct {
	mu sync.Mutex
	m  map[string]cachedSummary
}

type cachedSummary struct {
	size int64
	mod  time.Time
	s    summary
}

var summaries = &summaryCache{m: map[string]cachedSummary{}}

func (c *summaryCache) get(path string, info os.FileInfo) summary {
	c.mu.Lock()
	if v, ok := c.m[path]; ok && v.size == info.Size() && v.mod.Equal(info.ModTime()) {
		c.mu.Unlock()
		return v.s
	}
	c.mu.Unlock()
	s := summarize(path)
	c.mu.Lock()
	// Bounded crudely: the list is at most 50 per project, and a panel has a
	// handful of projects anyone talks to from glasses.
	if len(c.m) > 2000 {
		c.m = map[string]cachedSummary{}
	}
	c.m[path] = cachedSummary{size: info.Size(), mod: info.ModTime(), s: s}
	c.mu.Unlock()
	return s
}

func summarize(path string) summary {
	f, err := os.Open(path)
	if err != nil {
		return summary{}
	}
	defer f.Close()
	var s summary
	eachLine(f, func(l transcriptLine) {
		if text := userText(l); text != "" {
			s.turns++
			if s.title == "" {
				s.title = clip(oneLine(timePrefix.ReplaceAllString(text, "")), 80)
			}
		}
	})
	return s
}

// eachLine decodes a transcript line by line, skipping what does not parse.
// A reader rather than a scanner: one line can be a whole file a tool read,
// far past bufio.Scanner's default limit.
func eachLine(r io.Reader, fn func(transcriptLine)) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var l transcriptLine
			if json.Unmarshal(line, &l) == nil {
				fn(l)
			}
		}
		if err != nil {
			return
		}
	}
}

// ListSessions is the transcripts of cwd, newest first, at most limit. A
// directory that does not exist is an empty list: nobody has talked to
// Claude there yet.
func ListSessions(configDir, cwd string, limit int) ([]SessionInfo, error) {
	dir := TranscriptDir(configDir, cwd)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []SessionInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	type file struct {
		id   string
		info os.FileInfo
	}
	var files []file
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sid := strings.TrimSuffix(name, ".jsonl")
		if !ValidSessionID(sid) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{sid, info})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].info.ModTime().After(files[j].info.ModTime()) })
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	out := make([]SessionInfo, 0, len(files))
	for _, f := range files {
		s := summaries.get(filepath.Join(dir, f.id+".jsonl"), f.info)
		if s.turns == 0 {
			// A transcript with nothing a person said in it is a resumed
			// stub or a summary file; nothing to continue.
			continue
		}
		out = append(out, SessionInfo{
			ID: f.id, Title: s.title, UpdatedAt: f.info.ModTime().Unix(), Turns: s.turns,
			modified: f.info.ModTime(),
		})
	}
	return out, nil
}

// ReadSession is the last limit messages of one transcript. os.ErrNotExist
// when there is no such session in that directory.
func ReadSession(configDir, cwd, sessionID string, limit int) ([]Message, error) {
	if !ValidSessionID(sessionID) {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(TranscriptDir(configDir, cwd), sessionID+".jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Message
	eachLine(f, func(l transcriptLine) {
		if l.IsSidechain || l.IsMeta || l.Message == nil {
			return
		}
		switch l.Type {
		case "user":
			if text := userText(l); text != "" {
				out = append(out, Message{Role: "user", Text: text, Tools: []Tool{}, At: lineTime(l.Timestamp)})
			}
		case "assistant":
			var bs []contentBlock
			if len(l.Message.Content) > 0 && l.Message.Content[0] == '[' {
				_ = json.Unmarshal(l.Message.Content, &bs)
			}
			var text []string
			var tools []Tool
			for _, b := range bs {
				switch b.Type {
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						text = append(text, b.Text)
					}
				case "tool_use":
					tools = append(tools, Tool{Name: b.Name, Summary: ToolSummary(b.Input)})
				}
			}
			if len(text) == 0 && len(tools) == 0 {
				return
			}
			// One turn is several assistant lines (a line per content
			// block, a line per tool round). They are one message to a
			// person reading it back.
			if n := len(out); n > 0 && out[n-1].Role == "assistant" {
				last := &out[n-1]
				if len(text) > 0 {
					if last.Text != "" {
						last.Text += "\n\n"
					}
					last.Text += strings.Join(text, "\n\n")
				}
				last.Tools = append(last.Tools, tools...)
				if at := lineTime(l.Timestamp); at > 0 {
					last.At = at
				}
				return
			}
			if tools == nil {
				tools = []Tool{}
			}
			out = append(out, Message{Role: "assistant", Text: strings.Join(text, "\n\n"), Tools: tools, At: lineTime(l.Timestamp)})
		}
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	if out == nil {
		out = []Message{}
	}
	return out, nil
}
