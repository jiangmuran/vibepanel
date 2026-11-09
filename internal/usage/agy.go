package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ─── agy (Antigravity CLI) ────────────────────────────────────────────────

// agyTranscript is the one file per conversation this reads:
//
//	~/.gemini/antigravity-cli/brain/<conversation-id>/.system_generated/logs/transcript_full.jsonl
//
// (transcript.jsonl beside it is the same steps, shortened, with no token
// counts.) One JSON object per step. What it spends is on the model's own
// steps: a `PLANNER_RESPONSE` with `status: DONE` carries `input_tokens`,
// `output_tokens`, `cache_read_tokens` and -- when there is any -- `cache_write_tokens`.
//
// The design, including how the model is recovered, follows cc-switch's reader
// (src-tauri/src/services/session_usage_agy.rs), checked against this
// machine's own transcripts.
const agyTranscript = "transcript_full.jsonl"

// agyRecord is the part of one step this cares about.
//
// Content is a RawMessage because only a USER_INPUT's is read, and it is
// the largest field in most steps: a step that is not one never pays for
// decoding it.
type agyRecord struct {
	StepIndex  *int64          `json:"step_index"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	CreatedAt  string          `json:"created_at"`
	Content    json.RawMessage `json:"content"`
	Input      int64           `json:"input_tokens"`
	Output     int64           `json:"output_tokens"`
	CacheRead  int64           `json:"cache_read_tokens"`
	CacheWrite int64           `json:"cache_write_tokens"`
}

var (
	agyResponseMarker  = []byte(`"PLANNER_RESPONSE"`)
	agyUserInputMarker = []byte(`"USER_INPUT"`)

	// agyModelChange matches the sentence agy writes into a conversation when
	// the model is switched:
	//
	//	The user changed setting `Model Selection` from X to Y.
	//
	// A response carries no model of its own, so this is how a step gets one.
	// The names have dots in them ("Gemini 3.8"), so the sentence ends at a
	// full stop followed by whitespace and not at the first one.
	agyModelChange = regexp.MustCompile("`?Model Selection`? from (?:.+?) to (.+?)(?:\\.\\s|\\n|$)")
)

// agyModelFrom returns the model a USER_INPUT switched to, or "" if it did not
// switch one. The last switch in a message wins; "None" is the model being
// unset, which names nothing.
func agyModelFrom(content string) string {
	all := agyModelChange.FindAllStringSubmatch(content, -1)
	if len(all) == 0 {
		return ""
	}
	m := strings.Trim(strings.TrimSpace(all[len(all)-1][1]), ".`")
	m = strings.TrimSpace(m)
	if m == "" || strings.EqualFold(m, "none") {
		return ""
	}
	return m
}

// agyModelID turns the display name agy records ("Gemini 3.8 Flash (High)")
// into the id `agy models` lists ("gemini-3.8-flash-high"), which is what the
// same model is called everywhere else. A name with no space is already an id.
func agyModelID(display string) string {
	if !strings.Contains(display, " ") {
		return display
	}
	s := strings.ToLower(display)
	s = strings.NewReplacer(" (", "-", "(", "", ")", "").Replace(s)
	return strings.Join(strings.Fields(s), "-")
}

// agySession is the conversation a transcript belongs to: the directory above
// `.system_generated`.
func agySession(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := len(parts) - 1; i > 0; i-- {
		if parts[i] == ".system_generated" {
			return parts[i-1]
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// agyWorkspace is the directory a conversation was started in, read from
// agy's prompt history, which is the only place that records it -- the
// transcript does not.
//
// A project is matched by directory, so a conversation with no answer here
// still counts toward the totals and simply belongs to no project. The last
// entry wins, as a Codex thread that changed directory does.
func agyWorkspace(path, session string) string {
	// <root>/brain/<id>/.system_generated/logs/<file> -> <root>/history.jsonl
	root := path
	for i := 0; i < 5; i++ {
		root = filepath.Dir(root)
	}
	b, err := os.ReadFile(filepath.Join(root, "history.jsonl"))
	if err != nil {
		return ""
	}
	var cwd string
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(session)) {
			continue
		}
		var h struct {
			Workspace      string `json:"workspace"`
			ConversationID string `json:"conversationId"`
		}
		if json.Unmarshal(line, &h) == nil && h.ConversationID == session && h.Workspace != "" {
			cwd = h.Workspace
		}
	}
	return cwd
}

// agyStep is one step after later copies of it have been folded in.
type agyStep struct {
	switchedTo string // USER_INPUT: the model it switched to
	response   bool   // PLANNER_RESPONSE, DONE
	counts     Counts
	at         string
}

// readAgy accumulates one agy transcript.
//
// What is and is not counted, each from reading agy's output rather than from
// a guess:
//
//   - Input is fresh input, excluding the cache, and Output already includes
//     thinking -- so this adds nothing and subtracts nothing, which is the one
//     thing about agy that could be got wrong without anything failing. (The
//     other direction is Codex, where input includes the cache.)
//   - A step can be written again when it completes. They are folded by
//     step_index, the last copy winning, so a rewritten step is one request.
//   - A response whose four counters are all zero spent nothing.
//   - A line that does not parse is counted as skipped. The tail of a file
//     being written is the usual one.
//   - A response before any model switch has no model, and is recorded under
//     "unknown" rather than under whatever model is active today.
//
// agy also keeps the model, and timings, per generation in the conversation's
// own database. That is not read: it is a protobuf blob, and a model that is
// unknown is a smaller error than one decoded from a guessed schema.
func readAgy(r io.Reader, loc *time.Location, path string) (readResult, error) {
	var out readResult
	session := agySession(path)
	cwd := agyWorkspace(path, session)

	steps := map[int64]*agyStep{}
	err := eachLine(r, &out.skipped, func(line []byte) {
		resp := bytes.Contains(line, agyResponseMarker)
		if !resp && !bytes.Contains(line, agyUserInputMarker) {
			return
		}
		var rec agyRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			out.skipped++
			return
		}
		if rec.StepIndex == nil {
			return
		}
		switch {
		case rec.Type == "PLANNER_RESPONSE":
			steps[*rec.StepIndex] = &agyStep{
				response: rec.Status == "DONE",
				counts:   Counts{Input: rec.Input, Output: rec.Output, CacheRead: rec.CacheRead, CacheWrite: rec.CacheWrite},
				at:       rec.CreatedAt,
			}
		case rec.Type == "USER_INPUT":
			var content string
			if json.Unmarshal(rec.Content, &content) != nil {
				return
			}
			if m := agyModelFrom(content); m != "" {
				steps[*rec.StepIndex] = &agyStep{switchedTo: m}
			}
		}
	})
	if err != nil {
		return out, err
	}

	order := make([]int64, 0, len(steps))
	for i := range steps {
		order = append(order, i)
	}
	sort.Slice(order, func(a, b int) bool { return order[a] < order[b] })

	model := ""
	for _, i := range order {
		s := steps[i]
		if s.switchedTo != "" {
			model = agyModelID(s.switchedTo)
			continue
		}
		if !s.response || s.counts.Total() == 0 {
			continue
		}
		day, ok := localDay(s.at, loc)
		if !ok {
			out.skipped++
			continue
		}
		m := model
		if m == "" {
			m = "unknown"
		}
		out.requests = append(out.requests, Request{
			Key: requestKey("agy\x00"+session+"\x00"+strconv.FormatInt(i, 10), ""), Day: day,
			Session: session, Model: m, CWD: cwd, Counts: s.counts,
		})
	}
	out.buckets = Aggregate(out.requests)
	return out, nil
}
