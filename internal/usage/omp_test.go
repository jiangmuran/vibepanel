package usage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

const (
	ompParentID = "01a1116e-6073-739e-958c-0c7384c50d15"
	ompParent   = "2026-10-06T13-36-48-755Z_" + ompParentID
)

func ompHeader(id, cwd, parent string) string {
	p := ""
	if parent != "" {
		p = fmt.Sprintf(`,"parentSession":%q`, parent)
	}
	return fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":"2026-10-17T09:44:40.088Z","cwd":%q%s}`, id, cwd, p)
}

// ompAssistant is an assistant entry the way omp 18.6.3 writes it, the fields
// this does not read included.
func ompAssistant(entry, ts, model, resp string, in, out, cr, reasoning int64) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":"p","timestamp":%q,`+
		`"message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"api":"openai-completions",`+
		`"provider":"zhipu-coding-plan","model":%q,"usage":{"input":%d,"output":%d,"cacheRead":%d,"cacheWrite":0,`+
		`"totalTokens":%d,"reasoningTokens":%d,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},`+
		`"stopReason":"toolUse","timestamp":1793159956653,"responseId":%q}}`,
		entry, ts, model, in, out, cr, in+out+cr, reasoning, resp)
}

func ompFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A title line first, as omp writes it, before the header.
	body := `{"type":"title","v":1,"title":"","updatedAt":"2026-10-17T09:44:40.088Z"}` + "\n" +
		strings.Join(lines, "\n") + "\n"
	writeFile(t, path, body)
}

// What omp spent is what omp's own summary adds up, and nothing else.
//
// The numbers are from real records. `reasoningTokens` is a breakdown of
// `output` (totalTokens is the four columns, not five); the user message and
// the task result carry no call of their own, and the task result's
// `details.usage` is the subagent's spend, which its own file reports.
func TestOmpReadsWhatOmpRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "-p", ompParent+".jsonl")
	ompFile(t, path,
		ompHeader(ompParentID, "/home/agent", ""),
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-17T09:44:41.000Z","message":{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":1793159950000}}`,
		ompAssistant("a1", "2026-10-17T09:44:42.000Z", "glm-5.3", "r1", 7554, 213, 0, 190),
		`{"type":"model_usage","id":"5b93d442","parentId":"a1","timestamp":"2026-10-17T09:44:43.591Z","purpose":"auto-thinking","role":"judge","api":"openai-completions","provider":"zhipu-coding-plan","model":"glm-5.3-air","usage":{"input":274,"output":3,"cacheRead":0,"cacheWrite":0,"totalTokens":277,"cost":{"total":0}},"stopReason":"stop"}`,
		`{"type":"message","id":"t1","parentId":"a1","timestamp":"2026-10-17T09:44:44.000Z","message":{"role":"toolResult","toolName":"task","details":{"usage":{"input":9000,"output":900,"cacheRead":0,"cacheWrite":0,"totalTokens":9900}},"timestamp":1793159960000}}`,
		// Not an assistant, so not a call, whatever it carries: omp's own
		// summary reads message.usage from an assistant and from nothing else.
		`{"type":"message","id":"t2","parentId":"a1","timestamp":"2026-10-17T09:44:44.500Z","message":{"role":"toolResult","toolName":"bash","usage":{"input":5000,"output":500,"cacheRead":0,"cacheWrite":0,"totalTokens":5500},"timestamp":1793159961000}}`,
		// Aborted: a usage object that spent nothing, and not a call.
		ompAssistant("a2", "2026-10-17T09:44:45.000Z", "glm-5.3", "r2", 0, 0, 0, 0),
		ompAssistant("a3", "2026-10-17T09:44:46.000Z", "glm-5.3", "r3", 100, 50, 7000, 0),
	)
	s := &Scanner{OmpRoot: filepath.Dir(filepath.Dir(path)), Loc: time.UTC}
	f := s.ReadFile(ToolOmp, path)
	if f.Problem != "" || f.Skipped != 0 {
		t.Fatalf("problem %q, skipped %d", f.Problem, f.Skipped)
	}
	by := map[string]Counts{}
	for _, b := range f.Buckets {
		if b.Session != ompParentID || b.CWD != "/home/agent" || b.Day != "2026-10-17" {
			t.Errorf("bucket %+v: want session %s in /home/agent on 2026-10-17", b, ompParentID)
		}
		by[b.Model] = b.Counts
	}
	main := Counts{Input: 7554 + 100, Output: 213 + 50, CacheRead: 7000, Requests: 2}
	judge := Counts{Input: 274, Output: 3, Requests: 1}
	if by["glm-5.3"] != main {
		t.Errorf("glm-5.3: %+v, want %+v (reasoning folded in, or the task's usage read)", by["glm-5.3"], main)
	}
	if by["glm-5.3-air"] != judge {
		t.Errorf("the judge: %+v, want %+v under its own model", by["glm-5.3-air"], judge)
	}
	if len(by) != 2 {
		t.Errorf("models %v, want glm-5.3 and glm-5.3-air", by)
	}
}

// A subagent's file is inside its parent's directory and its spend is the
// parent session's. A fork is beside its parent and is a session of its own.
func TestOmpSubagentsBelongToTheirSession(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "-p", ompParent+".jsonl")
	ompFile(t, parent, ompHeader(ompParentID, "/p", ""),
		ompAssistant("a1", "2026-10-17T09:44:42.000Z", "m", "r1", 10, 1, 0, 0))
	sub := filepath.Join(root, "-p", ompParent, "AdminUiTask.jsonl")
	ompFile(t, sub, ompHeader("01a1493f-cb98-75a6-a8a9-8bd1737b80ec", "/p", parent),
		ompAssistant("b1", "2026-10-17T09:45:00.000Z", "m", "r2", 20, 2, 0, 0))
	// A subagent's own subagent, in a directory named the way a session file
	// is: still the session somebody is looking at, so the outermost session
	// directory on the path decides and not the nearest.
	subsub := filepath.Join(root, "-p", ompParent, "2026-10-17T09-45-00-000Z_01a1493f-cb98-75a6-a8a9-8bd1737b80ec", "Scout.jsonl")
	ompFile(t, subsub, ompHeader("01a1493f-0000-75a6-a8a9-8bd1737b80ec", "/p",
		filepath.Join(root, "-p", ompParent, "AdminUiTask.jsonl")),
		ompAssistant("c1", "2026-10-17T09:46:00.000Z", "m", "r3", 30, 3, 0, 0))
	const forkID = "01a1aaaa-0000-7000-8000-000000000000"
	fork := filepath.Join(root, "-p", "2026-10-18T00-00-00-000Z_"+forkID+".jsonl")
	ompFile(t, fork, ompHeader(forkID, "/p", parent),
		ompAssistant("d1", "2026-10-18T00:00:01.000Z", "m", "r4", 40, 4, 0, 0))

	s := &Scanner{OmpRoot: root, Loc: time.UTC}
	refs, src, err := s.Walk(ToolOmp)
	if err != nil || !src.Found || len(refs) != 4 {
		t.Fatalf("walk: %d files, %+v, %v; the nested subagent files must be found", len(refs), src, err)
	}
	want := map[string]string{parent: ompParentID, sub: ompParentID, subsub: ompParentID, fork: forkID}
	for _, r := range refs {
		f := s.ReadFile(ToolOmp, r.Path)
		if len(f.Buckets) != 1 {
			t.Fatalf("%s: %d buckets", r.Path, len(f.Buckets))
		}
		rel, _ := filepath.Rel(root, r.Path)
		if got := f.Buckets[0].Session; got != want[r.Path] {
			t.Errorf("%s is counted under session %s, want %s", rel, got, want[r.Path])
		}
	}
}

// A fork copies its parent's entries, usage and all, into a new file. Each call
// is counted once however many forks carry it -- and the spend after the fork
// point still counts in both.
func TestOmpAForkCountsItsParentsCallsOnce(t *testing.T) {
	root := t.TempDir()
	shared := ompAssistant("a1", "2026-10-17T09:44:42.000Z", "m", "r1", 1000, 100, 0, 0)
	ompFile(t, filepath.Join(root, "-p", ompParent+".jsonl"), ompHeader(ompParentID, "/p", ""),
		shared, ompAssistant("a2", "2026-10-17T10:00:00.000Z", "m", "r2", 10, 1, 0, 0))
	const forkID = "01a1aaaa-0000-7000-8000-000000000000"
	ompFile(t, filepath.Join(root, "-p", "2026-10-17T11-00-00-000Z_"+forkID+".jsonl"),
		ompHeader(forkID, "/p", ompParentID),
		shared, ompAssistant("e1", "2026-10-17T11:00:01.000Z", "m", "r5", 20, 2, 0, 0))

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() }) //nolint:errcheck // temp file
	in := &Ingester{Scanner: &Scanner{OmpRoot: root, Loc: time.UTC}, DB: db}
	ctx := context.Background()
	in.RunNow(ctx)
	days, err := db.UsageByDay(ctx, store.UsageFilter{})
	if err != nil || len(days) != 1 {
		t.Fatalf("by day: %+v %v", days, err)
	}
	if d := days[0]; d.Input != 1030 || d.Output != 103 || d.Requests != 3 {
		t.Errorf("got %d in, %d out in %d requests; want 1030, 103 in 3: the fork's copy counted again",
			d.Input, d.Output, d.Requests)
	}
}
