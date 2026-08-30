package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tz"
	"github.com/jiangmuran/vibepanel/internal/usage"
)

// The label the ingest writes and the label the query asks for are the same
// string.
//
// This is the trap the timezone work was built around, and it predates the
// setting. `usage.Scanner.Loc` decides which day a transcript record lands in
// and has existed since the feature was written; it was never set, so
// production bucketed in the process's zone. The query side computed "today"
// separately, with a bare `time.Now()`. Two independent answers to one
// question, agreeing only because both happened to be `time.Local`.
//
// Set one without the other and nothing reports anything: the query names a
// bucket the ingest never writes, so the heatmap's last square and the "today"
// row are empty, forever, with no error in any log. That is why they are read
// from one setting now, and why this compares them rather than trusting it.
func TestTheIngestAndTheQueryAgreeOnToday(t *testing.T) {
	// Instants chosen so the answer actually differs by zone. At most times of
	// day every zone agrees on the date, so a test taken at `now` passes for
	// sixteen hours out of twenty-four while proving nothing -- which is what
	// the first version of this did, and mutation testing is how that was
	// found: putting the query side back on the process clock changed no
	// result at all.
	instants := []time.Time{
		time.Date(2026, 8, 30, 0, 30, 0, 0, time.UTC),  // Los Angeles is still the 29th
		time.Date(2026, 8, 30, 23, 30, 0, 0, time.UTC), // Shanghai is already the 31st
		time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),  // and one where most agree
		time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC),    // across a year boundary
	}
	zones := []string{"", "UTC", "Asia/Shanghai", "America/Los_Angeles", "Pacific/Kiritimati"}

	for _, zone := range zones {
		loc, err := tz.Load(zone)
		if err != nil {
			t.Fatalf("Load(%q): %v", zone, err)
		}
		scanner := &usage.Scanner{Loc: loc}
		for _, at := range instants {
			queryDay := dayIn(loc, at)
			ingestDay := usage.DayForTest(scanner, at)
			if queryDay != ingestDay {
				t.Errorf("zone %q at %s: the query asks for %q and the ingest writes %q; "+
					"the heatmap's last square and the today row go empty and nothing says why",
					zone, at.Format(time.RFC3339), queryDay, ingestDay)
			}
		}
	}

	// And the instants really do separate the zones, or the loop above proves
	// nothing. This is the assertion the first version was missing.
	la, _ := tz.Load("America/Los_Angeles")
	sh, _ := tz.Load("Asia/Shanghai")
	if dayIn(la, instants[0]) == dayIn(sh, instants[0]) {
		t.Fatal("the chosen instants do not separate any zones; this test cannot fail")
	}
}

// The day-label layout is written in exactly one file.
//
// The pair above can only compare two functions. What it cannot see is a
// handler that never calls either -- which was the actual state of the code
// for as long as the feature existed: `usage.Scanner.Loc` decided the ingest's
// day and was never set, while every query formatted a bare `time.Now()`. Two
// answers to one question, agreeing only by accident.
//
// A regex for `time.Now().Format(...)` does not catch that. The real code
// writes `now := time.Now()` on one line and `now.Format(...)` on another, and
// a pattern that spans statements is a pattern that misses the next spelling.
// Found by mutation: putting the query side back on the process clock left the
// first version of this test green.
//
// So the invariant is structural instead. Every day label comes from dayIn,
// dayShift or dayParse, all of which take the zone as an argument, and the
// layout string exists nowhere else. A literal here is a label built from
// whatever clock was in scope.
func TestOnlyThisFileNamesTheDayLayout(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "zone.go" {
			continue
		}
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatal(rerr)
		}
		checked++
		if n := strings.Count(string(src), `"2006-01-02"`); n > 0 {
			t.Errorf("%s writes the day layout %d time(s). Use dayIn/dayShift/dayParse from "+
				"zone.go, which take the panel's zone: a label built from the process clock "+
				"disagrees with the one the ingest wrote, and nothing reports it.", f, n)
		}
	}
	if checked < 5 {
		t.Fatalf("only read %d files; this test has stopped looking at the package", checked)
	}
	// And zone.go really does still define it, or the sweep above passes by
	// finding nothing anywhere.
	zone, err := os.ReadFile("zone.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zone), `dayLayout = "2006-01-02"`) {
		t.Fatal("zone.go no longer defines dayLayout; this test is checking nothing")
	}
}

// Changing the zone drops the scan cursor, because the day labels already in
// the database were written under the old one and are strings.
func TestChangingTheZoneRebuildsTheHistory(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := t.Context()

	// A file in the cursor, as a completed pass leaves behind.
	if err := srv.DB.ReplaceUsageFile(ctx, store.UsageFile{
		Path: "/tmp/a-transcript.jsonl", Tool: "claude", Size: 1, ModifiedAt: 1,
	}); err != nil {
		t.Fatalf("seeding the cursor: %v", err)
	}

	put := func(zone string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"zone": zone})
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/timezone", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out) //nolint:errcheck
		return out
	}

	if out := put("Asia/Shanghai"); out["rebuilt"] == nil || out["rebuilt"].(float64) < 1 {
		t.Errorf("changing the zone did not drop the cursor: %v", out)
	}
	// Setting the same zone again is not a change and must not throw away a
	// year of reading for nothing.
	if out := put("Asia/Shanghai"); out["rebuilt"].(float64) != 0 {
		t.Errorf("setting the same zone rebuilt anyway: %v", out)
	}
}

// A zone this build cannot load is refused, not stored.
func TestAnUnknownZoneIsNotStored(t *testing.T) {
	ts, srv := newTestServer(t)
	body := `{"zone":"Mars/Olympus"}`
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/timezone", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown zone: %d, want 400", res.StatusCode)
	}
	if got, _ := srv.DB.GetSetting(t.Context(), TimeZoneKey, ""); got != "" {
		t.Errorf("it was stored anyway: %q", got)
	}
}
