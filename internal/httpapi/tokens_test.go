package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// seedTranscript writes one Claude Code transcript into the test server's
// throwaway Claude root and ingests it synchronously.
func seedTranscript(t *testing.T, srv *Server, name, cwd string, day string, out int64) {
	t.Helper()
	root := srv.Tokens.Scanner.ClaudeRoot
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":%q,"cwd":%q,`+
		`"requestId":"r1","message":{"id":"m1","model":"opus","usage":`+
		`{"input_tokens":1,"output_tokens":%d,"cache_creation_input_tokens":0,`+
		`"cache_read_input_tokens":0}}}`, day+"T12:00:00.000Z", name, cwd, out)
	if err := os.WriteFile(filepath.Join(root, name+".jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	srv.Tokens.RunNow(context.Background())
}

// An agent that is not installed reports as unread, not as zero.
//
// This is the failure the whole feature is arranged around. A panel that says
// "Codex: 0 tokens" when Codex has never been run on this machine has told the
// reader something false and made it look like a measurement.
func TestAToolWithNoTranscriptsIsUnknownRatherThanZero(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Tokens.RunNow(context.Background())

	res, err := ts.Client().Get(ts.URL + "/api/token-usage")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	var out tokenUsageResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(out.Sources) == 0 {
		t.Fatal("the response says nothing about where the numbers came from, " +
			"so a zero on screen is unfalsifiable")
	}
	for _, src := range out.Sources {
		if src.Found {
			t.Errorf("%s reported Found for a directory that does not exist", src.Tool)
		}
		if src.Problem == "" {
			t.Errorf("%s contributed nothing and did not say why", src.Tool)
		}
	}
	if out.ScannedAt == 0 {
		t.Error("a completed pass did not stamp scannedAt, so the panel cannot tell " +
			"'read and empty' from 'never read'")
	}
}

// Every known tool appears in byTool whether or not it spent anything, so an
// agent with no rows sits next to its reason instead of being absent.
func TestEveryToolIsListedEvenWithNothingToShow(t *testing.T) {
	ts, srv := newTestServer(t)
	seedTranscript(t, srv, "s1", "/somewhere", time.Now().Format("2006-01-02"), 100)

	res, err := ts.Client().Get(ts.URL + "/api/token-usage")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	var out tokenUsageResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	seen := map[string]bool{}
	for _, tool := range out.ByTool {
		seen[tool.Tool] = true
	}
	for _, want := range []string{"claude", "codex"} {
		if !seen[want] {
			t.Errorf("byTool omits %q; an agent that spent nothing must still be on screen "+
				"next to why", want)
		}
	}
}

// Work done in a directory the panel has never heard of still has to appear in
// the project breakdown, or that table disagrees with the total above it by an
// amount nothing explains.
func TestSpendOutsideEveryProjectIsStillCounted(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	if _, err := srv.DB.CreateProject(ctx, "p_known", "Known", "/known"); err != nil {
		t.Fatalf("create project: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	seedTranscript(t, srv, "inside", "/known/sub", today, 10)
	seedTranscript(t, srv, "outside", "/elsewhere", today, 7)

	res, err := ts.Client().Get(ts.URL + "/api/token-usage")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	var out tokenUsageResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var named, unnamed int64
	for _, p := range out.Projects {
		if p.ID == "" {
			unnamed += p.Output
			continue
		}
		named += p.Output
	}
	if named != 10 {
		t.Errorf("the known project shows %d output tokens, want 10", named)
	}
	if unnamed != 7 {
		t.Errorf("work outside every project shows %d output tokens, want 7; it was dropped, "+
			"so the project table no longer adds up to the total", unnamed)
	}
	if out.Total.Output != 17 {
		t.Errorf("total output %d, want 17", out.Total.Output)
	}
}

// The project filter takes an id and resolves it here. A path from the browser
// would let a caller ask about any directory on the machine and learn from the
// answer whether an agent had ever run in it.
func TestTheProjectFilterRefusesAnythingButAKnownID(t *testing.T) {
	ts, _ := newTestServer(t)

	for _, q := range []string{"?project=/etc", "?project=p_nope", "?tool=gpt", "?days=x"} {
		res, err := ts.Client().Get(ts.URL + "/api/token-usage" + q)
		if err != nil {
			t.Fatalf("GET %s: %v", q, err)
		}
		res.Body.Close() //nolint:errcheck // test
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /api/token-usage%s answered %s, want 400; an unrecognised filter "+
				"that quietly matches nothing looks exactly like a quiet week", q, res.Status)
		}
	}
}

// The heatmap is not narrowed by the range control: a 53-week grid holding
// seven days of data is not a smaller version of a year, it is a broken one.
func TestTheHeatmapKeepsItsYearWhateverTheRangeIs(t *testing.T) {
	ts, srv := newTestServer(t)
	old := time.Now().AddDate(0, 0, -100).Format("2006-01-02")
	seedTranscript(t, srv, "s_old", "/p", old, 42)

	res, err := ts.Client().Get(ts.URL + "/api/token-usage?days=7")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	var out tokenUsageResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(out.ByDay) != 0 {
		t.Errorf("a 7-day range returned %d days, and the only data is 100 days old",
			len(out.ByDay))
	}
	if len(out.Heatmap) != 1 {
		t.Errorf("the heatmap returned %d days; it followed the range control instead of "+
			"keeping its year", len(out.Heatmap))
	}
}

// The response says which local day it is, because the buckets are local days.
// A browser in another timezone deciding for itself would highlight the wrong
// square on the grid.
func TestTheResponseCarriesTheServersLocalToday(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := ts.Client().Get(ts.URL + "/api/token-usage")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	var out tokenUsageResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Today != time.Now().Format("2006-01-02") {
		t.Errorf("today is %q, want the server's local date %q",
			out.Today, time.Now().Format("2006-01-02"))
	}
}

// Refresh does not block, and says whether it started one.
func TestRefreshAnswersImmediately(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := ts.Client().Post(ts.URL+"/api/token-usage/refresh", "application/json",
		strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST refresh: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("refresh answered %s, want 202; a synchronous refresh over a year of "+
			"history looks like a hung panel", res.Status)
	}
	var body map[string]bool
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["started"]; !ok {
		t.Error("refresh did not say whether it started a pass")
	}
}

// Nothing from inside a transcript may reach a browser. The panel reads
// people's private conversations to count tokens in them; the counts leave and
// the words do not, and that is the whole difference between this and a remote
// reader for somebody's history.
func TestNoTranscriptContentIsEverServed(t *testing.T) {
	ts, srv := newTestServer(t)
	root := srv.Tokens.Scanner.ClaudeRoot
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const secret = "the-passphrase-is-hunter2"
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":"s","cwd":"/p",`+
		`"requestId":"r","message":{"id":"m","model":"opus","text":%q,"usage":`+
		`{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,`+
		`"cache_read_input_tokens":0}}}`,
		time.Now().Format("2006-01-02")+"T12:00:00.000Z", secret)
	if err := os.WriteFile(filepath.Join(root, "s.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	srv.Tokens.RunNow(context.Background())

	res, err := ts.Client().Get(ts.URL + "/api/token-usage")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatal("the response carries text out of a transcript; the panel counts tokens, " +
			"it does not serve conversations")
	}
	var out tokenUsageResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total.Output != 1 {
		t.Errorf("output %d, want 1 — the transcript was not counted at all", out.Total.Output)
	}
}

// A panel built without an ingester answers 503, not a zero. Nil is a working
// state for every other endpoint, and this one must not pretend otherwise.
func TestWithoutAnIngesterTheEndpointRefusesRatherThanReportingZero(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.Tokens = nil

	res, err := ts.Client().Get(ts.URL + "/api/token-usage")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("answered %s, want 503", res.Status)
	}
}

// projectFor picks the innermost project, not the first or the outermost.
// Nested project directories are an arrangement somebody makes on purpose, and
// attributing the inner one's spend to its parent is silently wrong in both.
func TestTheInnermostProjectWinsADirectory(t *testing.T) {
	projects := []store.Project{
		{ID: "outer", Name: "Outer", Path: "/work"},
		{ID: "inner", Name: "Inner", Path: "/work/api"},
		{ID: "sibling", Name: "Sibling", Path: "/work/api-v2"},
	}
	for _, tc := range []struct{ cwd, want string }{
		{"/work/api/web", "inner"},
		{"/work/api", "inner"},
		{"/work/other", "outer"},
		{"/work/api-v2/x", "sibling"},
		{"/elsewhere", ""},
	} {
		got, ok := projectFor(tc.cwd, projects)
		if tc.want == "" {
			if ok {
				t.Errorf("%s matched %s, want no project", tc.cwd, got.ID)
			}
			continue
		}
		if !ok || got.ID != tc.want {
			t.Errorf("%s matched %q, want %q", tc.cwd, got.ID, tc.want)
		}
	}
}

// The sources list is an array before anything has been read, never null.
//
// Every other list in this response is initialised, so this was the one field
// that could arrive as JSON null -- and the browser parses the body straight
// into an interface that declares it non-optional, so nothing type-checks the
// difference: `data.sources.filter(...)` runs *above* the "still reading"
// early return in both consumers, and the TypeError it throws is caught by the
// boundary around the whole app. The window is the seconds after a restart,
// which is exactly when somebody is looking.
func TestTheSourcesListIsNeverNull(t *testing.T) {
	ts, srv := newTestServer(t)

	// A pass that bails before it walks anything, which is the state the very
	// first request after a restart sees. Reachable here without racing the
	// background pass, because a completed-but-empty pass leaves Sources nil
	// the same way an unstarted one does.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if p := srv.Tokens.RunNow(cancelled); p.Err == "" {
		t.Fatalf("the pass was supposed to fail before it walked anything: %+v", p)
	}

	res, err := ts.Client().Get(ts.URL + "/api/token-usage")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck // test
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(fields["sources"]) == "null" {
		t.Fatalf("sources came back as null; the browser calls .filter() on it: %s", body)
	}
}
