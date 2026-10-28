package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"time"
)

// ─── omp ──────────────────────────────────────────────────────────────────

// ompRecord is the part of one omp session line this cares about.
//
// omp (oh-my-pi) began as a fork of pi and still writes pi's session format:
// a header, then a tree of entries, one JSON object per line, under
// ~/.omp/agent/sessions/<cwd-slug>/<timestamp>_<session-uuid>.jsonl. What it
// spends is in two places, and they are the two omp's own usage summary adds
// up (`ZAn` in omp 18.6.3):
//
//   - an assistant `message` carries `message.usage`, the call that produced
//     it;
//   - a `model_usage` entry carries a top-level `usage` and its own `model`,
//     for the calls made on the side of the conversation -- the auto-thinking
//     judge, and whatever else omp files under a `purpose`.
//
// omp's summary also adds `details.usage` from a `task` tool result, which is
// a subagent's spend as reported to its parent. That is deliberately not read
// here: the subagent writes its own session file, and that file is read. One
// of the two has to go, and the file is the one that exists whether or not
// the task finished.
//
// Unlike pi, omp's `compaction` and `branch_summary` carry no `usage`; the
// summary call is a `model_usage` entry. Both are still decoded the way readPi
// decodes them, so an omp that starts writing usage there is counted rather
// than silently dropped.
type ompRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	// Entry id, in every record but the header. Kept when omp copies an entry
	// into a fork, which is what makes it part of the request's identity.
	ID string `json:"id"`
	// Header fields.
	CWD string `json:"cwd"`
	// model_usage: the side call's own model and usage.
	Model string   `json:"model"`
	Usage *piUsage `json:"usage"`
	// A message: only an assistant's usage is a call.
	Message struct {
		Role       string          `json:"role"`
		Model      string          `json:"model"`
		Usage      *piUsage        `json:"usage"`
		ResponseID string          `json:"responseId"`
		Timestamp  json.RawMessage `json:"timestamp"`
	} `json:"message"`
}

var ompHeaderMarker = []byte(`"type":"session"`)

// A session file's name, and so the name of the directory its subagents are
// written into: <timestamp>_<session-uuid>.
var ompSessionDir = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[0-9-]+Z_([0-9a-f-]{36})$`)

// ompOwner is the session a file's spend belongs to.
//
// A subagent writes its own file inside a directory named after its parent's
// file (and names that file in `parentSession`):
//
//	<slug>/2026-10-06T13-36-48-755Z_01a1116e-….jsonl              the session
//	<slug>/2026-10-06T13-36-48-755Z_01a1116e-…/AdminUiTask.jsonl  its subagent
//
// That spend is the session's, the way a Claude Code subagent's is: it was
// spent on the conversation somebody is looking at, and a per-session table
// that listed "AdminUiTask" beside it would show the session costing less than
// it did. The outermost session directory on the path wins, so a subagent's
// own subagents land on the same session.
//
// A fork names a parent too, by id or by path, and is a session of its own
// that a person resumes. It is written beside its parent, never inside it, so
// where the file is answers the question and `parentSession` is not consulted.
func ompOwner(path, id string) string {
	owner := id
	for dir := filepath.Dir(path); ; {
		if m := ompSessionDir.FindStringSubmatch(filepath.Base(dir)); m != nil {
			owner = m[1]
		}
		next := filepath.Dir(dir)
		if next == dir {
			return owner
		}
		dir = next
	}
}

// ompKey is a call's identity across files.
//
// A fork copies the branch it was taken from into the new file entry for
// entry -- `createBranchedSession` and `forkFrom` both keep the entry ids and
// the messages as they were -- so every call before the fork point is in two
// files. Counted per file, a session forked twice reported its first hour
// three times. The ingest already counts a keyed request once however many
// files carry it (Claude's resumed sessions needed the same), so omp's calls
// carry a key: the entry id, the call's own timestamp and the provider's
// response id, all three of which a copy keeps. 33 of the 5,250 calls here
// have no response id, which is why it is not the key on its own.
func ompKey(entryID, ts string, responseID string) int64 {
	return requestKey("omp\x00"+entryID+"\x00"+ts, responseID)
}

// readOmp accumulates one omp session file. path is where it was found, which
// decides whose session a subagent's spend is (see ompOwner).
//
// omp's `reasoningTokens` is not read. It is a breakdown of `output`, as pi's
// `reasoning` is: omp's `totalTokens` is input+output+cacheRead+cacheWrite in
// all 5,250 calls on the machine this was written against, and
// reasoningTokens <= output in every one. Adding it would report more than omp
// says it spent.
func readOmp(r io.Reader, loc *time.Location, path string) (readResult, error) {
	var out readResult
	var session, cwd, model string

	addCall := func(rec *ompRecord, m string, u *piUsage, ts string, responseID string) {
		c := Counts{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
		if c.Total() == 0 {
			// An aborted or refused turn spent nothing and was not a call.
			return
		}
		day, ok := localDay(rec.Timestamp, loc)
		if !ok {
			out.skipped++
			return
		}
		out.requests = append(out.requests, Request{
			Key: ompKey(rec.ID, ts, responseID), Day: day, Session: session, Model: m, CWD: cwd, Counts: c,
		})
	}

	err := eachLine(r, &out.skipped, func(line []byte) {
		if !bytes.Contains(line, usageMarker) && !bytes.Contains(line, ompHeaderMarker) {
			return
		}
		var rec ompRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			out.skipped++
			return
		}
		switch rec.Type {
		case "session":
			session, cwd = ompOwner(path, rec.ID), rec.CWD
		case "message":
			if rec.Message.Role != "assistant" || rec.Message.Usage == nil {
				return
			}
			if rec.Message.Model != "" {
				model = rec.Message.Model
			}
			addCall(&rec, model, rec.Message.Usage, string(rec.Message.Timestamp), rec.Message.ResponseID)
		case "model_usage":
			if rec.Usage == nil {
				return
			}
			// Its own model, not the conversation's: the judge runs on
			// whatever omp's smol role points at.
			m := rec.Model
			if m == "" {
				m = model
			}
			addCall(&rec, m, rec.Usage, rec.Timestamp, "")
		case "compaction", "branch_summary":
			if rec.Usage == nil {
				return
			}
			addCall(&rec, model, rec.Usage, rec.Timestamp, "")
		}
	})
	if err != nil {
		return out, err
	}
	out.buckets = Aggregate(out.requests)
	return out, nil
}
