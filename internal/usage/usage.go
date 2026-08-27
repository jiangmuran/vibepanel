// Package usage reads what the coding agents recorded about their own token
// spend, and normalises it into something the panel can add up.
//
// Every number in here was written by the agent that spent it. Nothing is
// estimated: there is no character count divided by four anywhere in this
// package, and there must never be one. A figure that looks like a measurement
// and is not one is worse than an empty panel, because nobody checks a number
// that already looks right.
//
// The consequence has to be said out loud wherever these numbers are shown:
// they are the *agent's*, not the panel's. A `claude` run in a terminal this
// panel never started is counted; a session the panel did start that ran
// something with no transcript is not. The panel has no way to bridge that —
// it knows a session's argv and cwd, and neither Claude Code nor Codex tells
// anybody the id of the transcript it is writing — so the honest unit is the
// agent's own session, and that is what this reports.
//
// Where a transcript directory is missing or unreadable the answer is
// "unknown", carried as a Source with Found false and a Problem saying why.
// It is never zero. A dashboard that shows a confident zero for "I could not
// find the file" is the exact failure this package exists to avoid.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Tool names an agent. These strings reach the database, the API and the
// browser's filter, so they are the wire format and not a display label.
type Tool string

const (
	ToolClaude Tool = "claude"
	ToolCodex  Tool = "codex"
)

// Tools is every source this package knows how to read, in display order.
var Tools = []Tool{ToolClaude, ToolCodex}

// Counts is one normalised reading, in tokens.
//
// The two agents count input differently and the difference is not cosmetic:
// Claude reports `input_tokens` *excluding* what was served from cache, while
// Codex reports `input_tokens` *including* it and gives the cached part
// separately. Adding the two raw fields into one column makes Codex look like
// it re-sent its entire context uncached on every turn — on this machine's
// largest Codex thread that is 52.4M "input" tokens where 50.7M of them were
// cache reads.
//
// So everything here is normalised to Claude's split — Input is what was
// actually sent fresh — and the subtraction happens once, in readCodex.
type Counts struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	// Requests is how many API calls produced the totals above, after
	// duplicates were dropped. It is what distinguishes "a day with no
	// traffic" from "a day whose transcripts said zero".
	Requests int64 `json:"requests"`
}

// Add accumulates another reading.
func (c *Counts) Add(o Counts) {
	c.Input += o.Input
	c.Output += o.Output
	c.CacheRead += o.CacheRead
	c.CacheWrite += o.CacheWrite
	c.Requests += o.Requests
}

// Total is every token the request was billed for, cache included.
func (c Counts) Total() int64 { return c.Input + c.Output + c.CacheRead + c.CacheWrite }

// Bucket is one file's contribution to one day, one agent session and one
// model.
//
// Aggregated this coarsely at read time on purpose. A row per API request is
// the obvious shape and it is the wrong one: this machine holds 67,339
// requests over 37 days of history, so a year is on the order of 650,000 rows
// and 40 MB — larger than everything else in the database put together, to
// answer questions that are all "per day" anyway.
type Bucket struct {
	// Day is YYYY-MM-DD in the server's local zone. Not UTC: "how many tokens
	// did I spend today" is a question about the day the person lived through,
	// and at UTC+8 a UTC boundary cuts the working evening in half.
	Day string
	// Session is the agent's own session id, not a vibepanel session id.
	Session string
	// CWD is the directory the agent was working in, which is the only thing
	// that can be matched against a panel project. Resolution to a project
	// happens at query time, not here: projects are created and renamed after
	// the transcripts were written.
	CWD   string
	Model string
	Counts
}

// key identifies the bucket a record belongs in.
type key struct{ day, session, model string }

// maxLine bounds one JSON record.
//
// A transcript line is a whole message, and a message can carry the output of
// a command that printed a lot. Measured over the 4.4 GB of transcripts on the
// machine this was written on: the longest Claude line is 1,247,169 bytes and
// the longest Codex line is 5,756,466. So the cap is generous, and lines that
// approach it are tool output rather than anything carrying a usage object.
//
// It exists at all because the alternative is worse than skipping a line.
// bufio.Scanner stops the whole file at its token limit, which would mean one
// enormous command output silently truncating a year of history to whatever
// came before it; a bufio.Reader with no cap means one line deciding how much
// memory the panel uses. Overlong lines are skipped and counted, and the count
// travels to the UI as a problem rather than being swallowed.
const maxLine = 16 << 20

// readResult is what one transcript file yielded.
type readResult struct {
	buckets []Bucket
	// skipped counts records that could not be read: overlong lines and
	// malformed JSON. Reported rather than ignored, because "some of this
	// file was unreadable" and "this file says zero" must not look alike.
	skipped int
}

// ─── Claude Code ──────────────────────────────────────────────────────────

// claudeRecord is the part of a Claude Code transcript line this cares about.
//
// Deliberately a small struct rather than map[string]any: these files run to
// 89 MB and decoding every field of every message would spend most of a pass
// building maps that are thrown away. Unknown fields are ignored by
// encoding/json, which is what makes this survive the format growing.
type claudeRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// readClaude accumulates one Claude Code transcript.
//
// The whole file, from the start, every time — see Scan for why a byte offset
// would be wrong here.
//
// Deduplication on (message.id, requestId) is the load-bearing part. One API
// response with several content blocks is written as one line per block, each
// carrying the *same* usage object, so counting lines counts a thinking block
// and the text after it as two separate responses. Measured on one real
// 89 MB transcript: 13,869 usage-bearing lines for 6,563 actual requests, and
// 14.1M "output tokens" where the truth is 5.95M — inflation of 2.37x, in the
// direction that flatters. Across every transcript on that machine, 125,102
// lines for 67,339 requests.
//
// The duplicates come in two shapes and only one of them is obvious. 57,296 of
// them are the adjacent kind above. The other 466 sit exactly 1,787
// usage-lines apart, all in one file: a resumed session, where Claude Code
// replays the entire history into the same transcript. That second shape is
// why the seen-set covers the whole file and not a sliding window — a window
// of any affordable size passes the adjacent case and double-counts the
// replayed prefix, which is the failure that looks correct.
func readClaude(r io.Reader, loc *time.Location) (readResult, error) {
	var out readResult
	agg := map[key]*Bucket{}
	seen := map[string]struct{}{}

	err := eachLine(r, &out.skipped, func(line []byte) {
		// The cheap gate first. Most lines in a transcript are user messages
		// and tool results with no usage object at all, and unmarshalling them
		// is the difference between a pass that costs seconds and one that
		// costs minutes.
		if !containsUsage(line) {
			return
		}
		var rec claudeRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			out.skipped++
			return
		}
		if rec.Type != "assistant" || rec.Message.Usage == nil {
			return
		}
		dedupe := rec.Message.ID + "\x00" + rec.RequestID
		if _, dup := seen[dedupe]; dup {
			return
		}
		seen[dedupe] = struct{}{}

		day, ok := localDay(rec.Timestamp, loc)
		if !ok {
			out.skipped++
			return
		}
		u := rec.Message.Usage
		add(agg, key{day, rec.SessionID, rec.Message.Model}, rec.CWD, Counts{
			Input:      u.InputTokens,
			Output:     u.OutputTokens,
			CacheRead:  u.CacheReadInputTokens,
			CacheWrite: u.CacheCreationInputTokens,
			Requests:   1,
		})
	})
	if err != nil {
		return out, err
	}
	out.buckets = flatten(agg)
	return out, nil
}

// ─── Codex ────────────────────────────────────────────────────────────────

type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
}

type codexRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
		Model     string `json:"model"`
		Info      *struct {
			TotalTokenUsage codexUsage `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// readCodex accumulates one Codex rollout file.
//
// Codex does not report per-request usage that can simply be summed. Each
// `token_count` event carries both `last_token_usage` and `total_token_usage`,
// and the obvious reading — add up every `last_token_usage` — is wrong,
// because the event is re-emitted with an unchanged `last` when only the rate
// limits moved. Measured on this machine's largest rollout: summing `last`
// gives 53,309,297 total tokens where Codex's own final `total_token_usage`
// says 52,519,697, an over-count of 1.5%.
//
// Differencing `total_token_usage` reproduces that final figure to the token,
// which is the check that settled it: what this reports for a whole thread is
// exactly what Codex itself last wrote down. A duplicate event contributes a
// delta of zero and needs no dedupe table.
//
// A decreasing total is treated as a fresh baseline rather than a negative
// delta. Not observed on 138 rollouts — compaction does not reset it — but the
// alternative if it ever happens is a negative token count on somebody's
// dashboard, and there is no reading of "minus four million tokens" that is
// less wrong than counting the new total once.
func readCodex(r io.Reader, loc *time.Location) (readResult, error) {
	var out readResult
	agg := map[key]*Bucket{}

	var session, cwd, model string
	var prev codexUsage

	err := eachLine(r, &out.skipped, func(line []byte) {
		if !containsCodexMarker(line) {
			return
		}
		var rec codexRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			out.skipped++
			return
		}
		switch {
		case rec.Type == "session_meta":
			session = rec.Payload.SessionID
			cwd = rec.Payload.CWD
		case rec.Type == "turn_context":
			// Later than session_meta and more specific: a turn can run in a
			// subdirectory of where the thread started, and the model can be
			// switched mid-thread. session_meta carries no model at all, so
			// without this every Codex row would be attributed to "".
			if rec.Payload.CWD != "" {
				cwd = rec.Payload.CWD
			}
			if rec.Payload.Model != "" {
				model = rec.Payload.Model
			}
		case rec.Payload.Type == "token_count" && rec.Payload.Info != nil:
			day, ok := localDay(rec.Timestamp, loc)
			if !ok {
				out.skipped++
				return
			}
			total := rec.Payload.Info.TotalTokenUsage
			d := delta(prev, total)
			prev = total
			if d.Total() == 0 {
				// A repeated event. Not counted as a request either: it is the
				// same request being described a second time.
				return
			}
			add(agg, key{day, session, model}, cwd, d)
		}
	})
	if err != nil {
		return out, err
	}
	out.buckets = flatten(agg)
	return out, nil
}

// delta turns two cumulative readings into one request's worth, and does the
// input normalisation described on Counts.
func delta(prev, now codexUsage) Counts {
	sub := func(a, b int64) int64 {
		if b < a {
			// A reset: take the new total as the whole of it.
			return b
		}
		return b - a
	}
	input := sub(prev.InputTokens, now.InputTokens)
	cached := sub(prev.CachedInputTokens, now.CachedInputTokens)
	// Codex's input_tokens includes the cached part; Claude's does not. Floor
	// at zero rather than trusting the arithmetic: the two counters are
	// reported independently and a rounding difference between them would
	// otherwise show as a negative column.
	fresh := input - cached
	if fresh < 0 {
		fresh = 0
	}
	c := Counts{
		Input:      fresh,
		Output:     sub(prev.OutputTokens, now.OutputTokens),
		CacheRead:  cached,
		CacheWrite: sub(prev.CacheWriteInputTokens, now.CacheWriteInputTokens),
	}
	if c.Total() > 0 {
		c.Requests = 1
	}
	return c
}

// ─── shared reading ───────────────────────────────────────────────────────

// The prefilters. A transcript is mostly user turns and tool results with no
// usage object anywhere in them, and json.Unmarshal on those is where a pass
// would spend nearly all of its time. bytes.Contains never produces a false
// negative here — the markers are the JSON key names themselves — so the only
// cost of a false positive is one wasted decode.
var (
	usageMarker   = []byte(`"usage"`)
	tokenMarker   = []byte(`"token_count"`)
	metaMarker    = []byte(`"session_meta"`)
	contextMarker = []byte(`"turn_context"`)
)

func containsUsage(line []byte) bool { return bytes.Contains(line, usageMarker) }

func containsCodexMarker(line []byte) bool {
	return bytes.Contains(line, tokenMarker) ||
		bytes.Contains(line, metaMarker) ||
		bytes.Contains(line, contextMarker)
}

// eachLine calls fn for every line, skipping any that exceeds maxLine.
//
// Not bufio.Scanner: its token limit ends the file rather than the line, so a
// single oversized record would silently discard everything after it. Here an
// overlong line is drained and counted, and the pass continues.
func eachLine(r io.Reader, skipped *int, fn func([]byte)) error {
	br := bufio.NewReaderSize(r, 256<<10)
	var buf []byte
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			if !over {
				if len(buf)+len(chunk) > maxLine {
					over = true
					buf = buf[:0]
				} else {
					buf = append(buf, chunk...)
				}
			}
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		line := chunk
		if len(buf) > 0 {
			// The final piece counts toward the cap as well, and this check
			// used to be missing. A record that filled the buffer an exact
			// number of times passed every check made while it was filling and
			// then grew past the cap on its last chunk — so the cap did not
			// hold for the very records it exists to bound, and a
			// mutation that made an overlong line abort the file was not
			// noticed by the test that was supposed to catch it.
			if len(buf)+len(chunk) > maxLine {
				over = true
			} else {
				buf = append(buf, chunk...)
				line = buf
			}
		}
		if over {
			*skipped++
		} else if len(bytes.TrimSpace(line)) > 0 {
			fn(line)
		}
		buf = buf[:0]
		over = false
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

// localDay turns an RFC 3339 timestamp into a YYYY-MM-DD in loc.
func localDay(ts string, loc *time.Location) (string, bool) {
	if ts == "" {
		return "", false
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", false
	}
	return t.In(loc).Format("2006-01-02"), true
}

func add(agg map[key]*Bucket, k key, cwd string, c Counts) {
	b := agg[k]
	if b == nil {
		b = &Bucket{Day: k.day, Session: k.session, Model: k.model, CWD: cwd}
		agg[k] = b
	}
	// The last cwd seen wins. A Codex thread that changed directory mid-way
	// has one answer per bucket and this is the more recent one; a Claude
	// session cannot change it at all.
	if cwd != "" {
		b.CWD = cwd
	}
	b.Counts.Add(c)
}

func flatten(agg map[key]*Bucket) []Bucket {
	out := make([]Bucket, 0, len(agg))
	for _, b := range agg {
		out = append(out, *b)
	}
	// Sorted so a file's rows go into the database in a stable order, which is
	// what makes two passes over an unchanged file produce byte-identical
	// results and keeps a test able to compare them.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		if out[i].Session != out[j].Session {
			return out[i].Session < out[j].Session
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// ─── walking the roots ────────────────────────────────────────────────────

// Source is one agent's transcript directory as the last pass found it.
//
// Found is the field the UI must read before it renders a number. False means
// this agent contributed nothing *because it could not be read*, which is a
// different statement from "this agent spent nothing", and the two must not
// look alike on screen.
type Source struct {
	Tool    Tool   `json:"tool"`
	Root    string `json:"root"`
	Found   bool   `json:"found"`
	Problem string `json:"problem"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	// Skipped is records the reader could not use. Non-zero means the totals
	// below it are a lower bound.
	Skipped int `json:"skipped"`
}

// Ref is a transcript the walk found, with the two numbers that decide whether
// it needs reading. Returned instead of a bare path so that the cursor
// comparison costs nothing: WalkDir has already stat'd every entry, and asking
// again per file would be a second syscall each to answer a question already
// answered.
type Ref struct {
	Path       string
	Size       int64
	ModifiedAt int64
}

// File is one transcript as the scanner read it.
type File struct {
	Path       string
	Tool       Tool
	Size       int64
	ModifiedAt int64
	Buckets    []Bucket
	Skipped    int
	Problem    string
}

// Scanner walks the agents' transcript directories.
//
// The roots are fields rather than constants so the tests can point at a
// corpus they built, and so an operator whose agents write somewhere else can
// be pointed at it later without this package changing.
type Scanner struct {
	ClaudeRoot string
	CodexRoot  string
	// Loc decides which day a timestamp belongs to. Nil means the server's
	// local zone, which is the right default: the machine is the user's.
	Loc *time.Location
}

// DefaultScanner reads the two well-known locations under a home directory.
func DefaultScanner(home string) *Scanner {
	return &Scanner{
		ClaudeRoot: filepath.Join(home, ".claude", "projects"),
		CodexRoot:  filepath.Join(home, ".codex", "sessions"),
	}
}

func (s *Scanner) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

// Roots pairs each tool with the directory it would be read from, whether or
// not that directory exists.
func (s *Scanner) Roots() map[Tool]string {
	return map[Tool]string{ToolClaude: s.ClaudeRoot, ToolCodex: s.CodexRoot}
}

// Walk lists every transcript under one tool's root.
//
// This is the only place in the panel that reads files outside its own data
// directory, so containment is explicit rather than assumed:
//
//   - The root is resolved through EvalSymlinks first, so the boundary being
//     compared against is a real path and not one that changes meaning when a
//     link in it is replaced.
//   - filepath.WalkDir does not follow symlinks, and anything that is not a
//     regular file is skipped, so no link inside the tree can lead out of it.
//   - Every path is checked against the resolved root anyway. That is belt and
//     braces, and it is cheap; the failure it guards against — a future caller
//     joining a name from somewhere else — is silent.
//
// Nothing read here is ever sent to a browser. The counts leave, the file
// contents do not, and that is the difference between a usage panel and a
// remote reader for somebody's private conversations.
func (s *Scanner) Walk(tool Tool) ([]Ref, Source, error) {
	root := s.Roots()[tool]
	src := Source{Tool: tool, Root: root}
	if root == "" {
		src.Problem = "no directory configured"
		return nil, src, nil
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		// The ordinary case: the agent is not installed on this machine, or
		// has never been run. Not an error — a Source that says so.
		if errors.Is(err, fs.ErrNotExist) {
			src.Problem = "not found"
			return nil, src, nil
		}
		src.Problem = err.Error()
		return nil, src, nil
	}
	src.Root = resolved
	src.Found = true

	var out []Ref
	err = filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory the panel's user cannot read is a gap in the
			// numbers, not a reason to abandon the rest of the tree.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		rel, rerr := filepath.Rel(resolved, path)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		src.Files++
		src.Bytes += info.Size()
		out = append(out, Ref{Path: path, Size: info.Size(), ModifiedAt: info.ModTime().Unix()})
		return nil
	})
	if err != nil {
		src.Problem = err.Error()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, src, nil
}

// ReadFile reads one transcript and returns what it contributes.
func (s *Scanner) ReadFile(tool Tool, path string) File {
	f := File{Path: path, Tool: tool}
	// Stat before reading, not after. An agent appending while this runs would
	// otherwise have its new bytes recorded under the size and mtime they
	// arrived with, and the next pass would see nothing to do. Taking the
	// stamp first means a concurrent append leaves the recorded stamp stale,
	// and the file is read again.
	info, err := os.Stat(path)
	if err != nil {
		f.Problem = err.Error()
		return f
	}
	f.Size = info.Size()
	f.ModifiedAt = info.ModTime().Unix()

	fh, err := os.Open(path)
	if err != nil {
		f.Problem = err.Error()
		return f
	}
	defer fh.Close() //nolint:errcheck // read-only

	var res readResult
	switch tool {
	case ToolClaude:
		res, err = readClaude(fh, s.loc())
	case ToolCodex:
		res, err = readCodex(fh, s.loc())
	default:
		f.Problem = fmt.Sprintf("unknown tool %q", tool)
		return f
	}
	if err != nil {
		f.Problem = err.Error()
		return f
	}
	f.Buckets = res.buckets
	f.Skipped = res.skipped
	return f
}
