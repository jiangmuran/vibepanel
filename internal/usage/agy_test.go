package usage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const agySessionID = "6a85de34-7622-47b8-91d8-0f6dc632f500"

func agyTestPath(root string) string {
	return filepath.Join(root, "brain", agySessionID, ".system_generated", "logs", agyTranscript)
}

func TestAgyModelFromASettingsChange(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"hi\n<USER_SETTINGS_CHANGE>\nThe user changed setting `Model Selection` from None to Gemini 3.8 Flash (Low). No need to comment.\n</USER_SETTINGS_CHANGE>", "Gemini 3.8 Flash (Low)"},
		{"changed setting `Model Selection` from Gemini 4 Argon (Medium) to Claude Sonnet 5.5 (Medium). No need", "Claude Sonnet 5.5 (Medium)"},
		// Two switches in one message: the later one is what is active.
		{"`Model Selection` from None to A B. x\n`Model Selection` from A B to C D. y", "C D"},
		{"changed setting `Model Selection` from X to None. No need", ""},
		{"nothing about models here", ""},
	} {
		if got := agyModelFrom(tc.in); got != tc.want {
			t.Errorf("agyModelFrom(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAgyModelID(t *testing.T) {
	for in, want := range map[string]string{
		"Gemini 3.8 Flash (High)":    "gemini-3.8-flash-high",
		"Claude Sonnet 5.5 (Medium)": "claude-sonnet-5.5-medium",
		"gemini-3.8-flash-high":      "gemini-3.8-flash-high",
	} {
		if got := agyModelID(in); got != want {
			t.Errorf("agyModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func agyUser(step int, at, content string) string {
	return `{"step_index":` + itoaT(step) + `,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"` + at + `","content":"` + content + `"}` + "\n"
}

func agyResp(step int, at, status string, in, out, cr, cw int) string {
	return `{"step_index":` + itoaT(step) + `,"source":"MODEL","type":"PLANNER_RESPONSE","status":"` + status + `","created_at":"` + at +
		`","input_tokens":` + itoaT(in) + `,"output_tokens":` + itoaT(out) + `,"cache_read_tokens":` + itoaT(cr) + `,"cache_write_tokens":` + itoaT(cw) +
		`,"content":"x","thinking":"y","tool_calls":[]}` + "\n"
}

func itoaT(n int) string { return strconv.Itoa(n) }

func TestReadAgyCountsResponsesAndAttributesTheModelInStepOrder(t *testing.T) {
	root := t.TempDir()
	body := agyUser(0, "2026-11-09T13:42:47Z", "go\\n`Model Selection` from None to Gemini 3.8 Flash (Low). No need") +
		agyResp(1, "2026-11-09T13:43:00Z", "DONE", 100, 20, 500, 0) +
		agyResp(2, "2026-11-09T13:43:10Z", "DONE", 50, 10, 600, 0) +
		agyUser(3, "2026-11-09T13:50:00Z", "again\\n`Model Selection` from Gemini 3.8 Flash (Low) to Claude Sonnet 5.5 (Medium). No need") +
		agyResp(4, "2026-11-09T13:51:00Z", "DONE", 7, 3, 1000, 5)
	res, err := readAgy(strings.NewReader(body), time.UTC, agyTestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Counts{}
	for _, b := range res.buckets {
		if b.Session != agySessionID {
			t.Errorf("session = %q, want the conversation id", b.Session)
		}
		got[b.Model] = b.Counts
	}
	if c := got["gemini-3.8-flash-low"]; c.Input != 150 || c.Output != 30 || c.CacheRead != 1100 || c.Requests != 2 {
		t.Errorf("flash = %+v", c)
	}
	if c := got["claude-sonnet-5.5-medium"]; c.Input != 7 || c.Output != 3 || c.CacheRead != 1000 || c.CacheWrite != 5 || c.Requests != 1 {
		t.Errorf("sonnet = %+v", c)
	}
	if len(got) != 2 {
		t.Errorf("buckets = %v, want exactly the two models", got)
	}
}

// Input is already fresh input and Output already includes thinking: nothing
// is subtracted or added, so the four counters come through as they were.
func TestReadAgyDoesNotAdjustTheCounters(t *testing.T) {
	body := agyResp(1, "2026-11-09T13:43:00Z", "DONE", 10, 20, 30, 40)
	res, err := readAgy(strings.NewReader(body), time.UTC, agyTestPath(t.TempDir()))
	if err != nil || len(res.buckets) != 1 {
		t.Fatalf("buckets = %d, err = %v", len(res.buckets), err)
	}
	c := res.buckets[0].Counts
	if c.Input != 10 || c.Output != 20 || c.CacheRead != 30 || c.CacheWrite != 40 {
		t.Errorf("counts = %+v", c)
	}
	if res.buckets[0].Model != "unknown" {
		t.Errorf("a response before any model switch is recorded as %q, want unknown", res.buckets[0].Model)
	}
}

// A step written again when it completes is one request, not two, and the
// later copy's numbers are the ones that stand.
func TestReadAgyFoldsARewrittenStep(t *testing.T) {
	body := agyResp(1, "2026-11-09T13:43:00Z", "DONE", 10, 1, 0, 0) +
		agyResp(1, "2026-11-09T13:43:00Z", "DONE", 12, 2, 0, 0)
	res, _ := readAgy(strings.NewReader(body), time.UTC, agyTestPath(t.TempDir()))
	if len(res.requests) != 1 || res.requests[0].Input != 12 || res.requests[0].Output != 2 {
		t.Errorf("requests = %+v, want one with the later copy's counts", res.requests)
	}
}

func TestReadAgySkipsUnfinishedEmptyAndTornSteps(t *testing.T) {
	body := agyResp(1, "2026-11-09T13:43:00Z", "RUNNING", 10, 1, 0, 0) +
		agyResp(2, "2026-11-09T13:43:01Z", "DONE", 0, 0, 0, 0) +
		agyResp(3, "2026-11-09T13:43:02Z", "DONE", 5, 5, 0, 0) +
		`{"step_index":4,"type":"PLANNER_RESPONSE","status":"DONE","input_tok`
	res, err := readAgy(strings.NewReader(body), time.UTC, agyTestPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.requests) != 1 || res.requests[0].Input != 5 {
		t.Errorf("requests = %+v, want only the finished, non-empty step", res.requests)
	}
	if res.skipped != 1 {
		t.Errorf("skipped = %d, want the torn tail counted", res.skipped)
	}
}

// The day is the panel's local day, as for every other agent.
func TestReadAgyBucketsByTheLocalDay(t *testing.T) {
	body := agyResp(1, "2026-11-09T20:00:00Z", "DONE", 1, 1, 0, 0)
	res, _ := readAgy(strings.NewReader(body), time.FixedZone("x", 8*3600), agyTestPath(t.TempDir()))
	if len(res.buckets) != 1 || res.buckets[0].Day != "2026-11-10" {
		t.Errorf("day = %+v, want 2026-11-10 at UTC+8", res.buckets)
	}
}

func TestAgyWorkspaceComesFromThePromptHistory(t *testing.T) {
	root := t.TempDir()
	hist := `{"display":"x","workspace":"/other","conversationId":"someone-else"}` + "\n" +
		`{"display":"y","workspace":"/mnt/pi-data","conversationId":"` + agySessionID + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "history.jsonl"), []byte(hist), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := agyWorkspace(agyTestPath(root), agySessionID); got != "/mnt/pi-data" {
		t.Errorf("workspace = %q", got)
	}
	if got := agyWorkspace(agyTestPath(t.TempDir()), agySessionID); got != "" {
		t.Errorf("no history should mean no workspace, got %q", got)
	}
}

// Both transcript files sit in the same directory; only the full one carries
// token counts.
func TestAgyWalkReadsOnlyTheFullTranscript(t *testing.T) {
	home := t.TempDir()
	s := DefaultScanner(home)
	dir := filepath.Dir(agyTestPath(s.AgyRoot[:len(s.AgyRoot)-len("brain")]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{agyTranscript, "transcript.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	refs, src, err := s.Walk(ToolAgy)
	if err != nil {
		t.Fatal(err)
	}
	if !src.Found || len(refs) != 1 || filepath.Base(refs[0].Path) != agyTranscript {
		t.Errorf("refs = %+v, src = %+v", refs, src)
	}
}
