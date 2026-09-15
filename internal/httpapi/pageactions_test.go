package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func visitorAction(t *testing.T, ts *httptest.Server, token, name, body string) (int, actionAnswer, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/share/"+token+"/v1/actions/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := anonymousClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out actionAnswer
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out, res.Header
}

// Every condition of docs/page-backend.md §6, each shown refusing on its own.
func TestAVisitorActionRunsOnlyWhenEveryConditionHolds(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	plain := pageLink(t, ts, p.page.ID, "")
	kiosk := pageLink(t, ts, p.page.ID, `,"interactive":true`)

	// 2. Not interactive.
	if code, out, _ := visitorAction(t, ts, plain.Token, "vote", ""); code != http.StatusForbidden || out.OK {
		t.Errorf("vote on a plain link = %d %+v", code, out)
	}
	// It runs on an interactive one, and the next poll carries it.
	code, out, h := visitorAction(t, ts, kiosk.Token, "vote", "")
	if code != http.StatusOK || !out.OK || out.Result != float64(1) || h.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("vote on a kiosk link = %d %+v %v", code, out, h)
	}
	if _, snap, _ := snapshotGET(t, ts, kiosk.Token); snap.Data["votes"] != float64(1) || !snap.Interactive || !snap.Actions["vote"].Enabled {
		t.Errorf("after a vote the snapshot says %v, interactive %v", snap.Data["votes"], snap.Interactive)
	}

	// 4. Undeclared, admin-only, and declared only in a draft nobody published.
	for _, name := range []string{"nope", "announce"} {
		if code, _, _ := visitorAction(t, ts, kiosk.Token, name, ""); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, code)
		}
	}

	// 6. The payload.
	for _, body := range []string{
		`{"name":"ann","msg":"hi","extra":1}`,
		`{"name":"ann","msg":"a\u202eb"}`,
		`{"name":"` + strings.Repeat("x", 11) + `","msg":"hi"}`,
		`{"name":"ann","msg":"` + strings.Repeat("x", 5000) + `"}`,
		`[1,2]`,
	} {
		if code, out, _ := visitorAction(t, ts, kiosk.Token, "sign", body); code != http.StatusBadRequest || out.OK {
			t.Errorf("sign %q = %d %+v, want 400", body[:min(len(body), 40)], code, out)
		}
	}
	if code, out, _ := visitorAction(t, ts, kiosk.Token, "vote", `{"by":100}`); code != http.StatusBadRequest {
		t.Errorf("a vote with input = %d %+v", code, out)
	}
	if code, out, _ := visitorAction(t, ts, kiosk.Token, "sign", `{"name":"ann","msg":"hi"}`); code != http.StatusOK {
		t.Errorf("a good sign = %d %+v", code, out)
	}

	// 5. The action's own rate: 3/min per address, counting refused calls --
	// vote has been asked for twice already, once with a payload it refused.
	for i := 0; i < 2; i++ {
		visitorAction(t, ts, kiosk.Token, "vote", "")
	}
	code, out, h = visitorAction(t, ts, kiosk.Token, "vote", "")
	if code != http.StatusTooManyRequests || out.RetryAfter < 1 || h.Get("Retry-After") == "" {
		t.Errorf("the fourth vote in a minute = %d %+v", code, out)
	}

	// 3. The panel-wide switch.
	sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json", []byte(`{"visitorWrites":false}`))
	if code, _, _ := visitorAction(t, ts, kiosk.Token, "sign", `{"name":"b","msg":"c"}`); code != http.StatusForbidden {
		t.Errorf("an action with visitor writes off = %d", code)
	}
	if _, snap, _ := snapshotGET(t, ts, kiosk.Token); snap.Interactive || snap.Actions["sign"].Enabled {
		t.Error("with visitor writes off the snapshot still says actions work")
	}
	sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json", []byte(`{"visitorWrites":true}`))

	// Counted and audited, once per link and action per minute, not per call.
	if !auditHas(t, srv, "share.action", "wall: vote") {
		t.Error("no share.action row")
	}
	for _, l := range getJSON[[]map[string]any](t, ts, "/api/settings/shares") {
		// Two votes and one signature ran.
		if l["id"] == kiosk.ID && l["actionsToday"] != float64(3) {
			t.Errorf("actionsToday = %v, want 3", l["actionsToday"])
		}
	}
	var rows int
	_ = srv.DB.SQL().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE event = 'share.action'`).Scan(&rows)
	if rows > 2 {
		t.Errorf("%d share.action rows for a handful of calls; they are coalesced per minute", rows)
	}
}

// A View copy never writes; a preview link writes draft data only.
func TestViewAndPreviewLinksWriteWhereTheyShould(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	kiosk := pageLink(t, ts, p.page.ID, `,"interactive":true`)

	code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/shares/"+kiosk.ID+"/view", "application/json", nil)
	if code != http.StatusCreated {
		t.Fatal(code)
	}
	var peek struct{ Token string }
	_ = json.Unmarshal(body, &peek)
	if code, _, _ := visitorAction(t, ts, peek.Token, "vote", ""); code != http.StatusForbidden {
		t.Errorf("a vote through a View copy = %d", code)
	}

	prev := postJSON[map[string]any](t, ts, "/api/settings/pages/"+p.page.ID+"/preview", `{"detail":"counts"}`)
	if code, out, _ := visitorAction(t, ts, prev["token"].(string), "vote", ""); code != http.StatusOK {
		t.Fatalf("a vote in the Preview = %d %+v", code, out)
	}
	live := getJSON[pageDataResponse](t, ts, "/api/settings/pages/"+p.page.ID+"/data")
	draft := getJSON[pageDataResponse](t, ts, "/api/settings/pages/"+p.page.ID+"/data?ns=draft")
	if live.Values["votes"] != float64(0) || draft.Values["votes"] != float64(1) {
		t.Errorf("a Preview vote landed live=%v draft=%v", live.Values["votes"], draft.Values["votes"])
	}
	_ = srv
}

// An action declared only in the draft does not run on a link drawing the
// published version.
func TestAnUnpublishedActionDoesNotRun(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, sessionsManifest, nil)
	kiosk := pageLink(t, ts, p.page.ID, `,"interactive":true`)
	writeTestFile(t, filepath.Join(p.dir, "vibepanel.json"), `{"sdk":1,"name":"Lobby","sections":["sessions"],
		"data":{"votes":{"type":"counter"}},"actions":{"vote":{"who":"visitor","effect":{"increment":"votes"}}}}`)
	if code, _, _ := visitorAction(t, ts, kiosk.Token, "vote", ""); code != http.StatusNotFound {
		t.Errorf("a draft-only action = %d, want 404", code)
	}
}

func TestActionPreflightIsAnsweredWithoutCredentials(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	kiosk := pageLink(t, ts, p.page.ID, `,"interactive":true`)
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/api/share/"+kiosk.Token+"/v1/actions/vote", nil)
	res, err := anonymousClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), "POST") ||
		res.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("preflight = %d %v", res.StatusCode, res.Header)
	}
}

func TestTheRateBookForgetsNothingUnderAFlood(t *testing.T) {
	var b rateBook
	now := time.Now()
	for i := 0; i < rateBookCap; i++ {
		b.allow("k"+string(rune(i)), 1, time.Minute, now)
	}
	if ok, _ := b.allow("a-fresh-key", 1, time.Minute, now); ok {
		t.Error("a full book let a key it had never seen through")
	}
	if ok, _ := b.allow("a-fresh-key", 1, time.Minute, now.Add(2*time.Minute)); !ok {
		t.Error("expired windows were not cleared to make room")
	}
	_ = context.Background()
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
