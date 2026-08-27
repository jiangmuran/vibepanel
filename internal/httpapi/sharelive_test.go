package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// The half of a share link that is about a screen on a wall.
//
// Everything here exists because of one sentence: nobody is standing at the
// television, so the board has to be changeable from somewhere else and the
// change has to arrive without anybody touching the screen. The tests are
// arranged around what that must not cost — the share surface is still one GET,
// the payload is still what the redaction says it is, and nothing a viewer
// sends decides anything.

// patchShare edits a link as the signed-in owner and returns the status.
func patchShare(t *testing.T, ts *httptest.Server, id, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/settings/shares/"+id,
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// shareGETAs fetches the dashboard the way one particular screen does.
func shareGETAs(t *testing.T, ts *httptest.Server, token, viewer string, w, h int) []byte {
	t.Helper()
	url := ts.URL + "/api/share/" + token + "/dashboard?v=" + viewer
	if w > 0 || h > 0 {
		url += "&w=" + itoa(w) + "&h=" + itoa(h)
	}
	res, err := anonymousClient(t).Get(url)
	if err != nil {
		t.Fatalf("GET dashboard: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET dashboard = %d: %s", res.StatusCode, body)
	}
	return body
}

func itoa(n int) string { return strings.TrimSpace(strings.Trim(jsonNumber(n), `"`)) }

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// listShares reads the settings listing as the owner.
func listShares(t *testing.T, ts *httptest.Server) []store.ShareLink {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + "/api/settings/shares")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out []store.ShareLink
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	return out
}

// The whole feature, from the owner's side to the wall's, in one pass.
//
// This is the test the work exists for: an owner edits a board from a
// signed-in client and the screen shows the new one without anybody touching
// it. Break the live path -- cache the row in the middleware, serve the board
// from a snapshot taken at startup, move the read out of the poll -- and this
// is what says so.
func TestAnOwnersEditReachesAnOpenScreenOnItsNextPoll(t *testing.T) {
	ts, _ := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall","detail":"counts","preset":"single"}`)

	first := decodeDashboard(t, shareGETAs(t, ts, link.Token, "aa11", 3840, 2160))
	if len(first.Board.Widgets) != 1 || first.Board.Widgets[0].Kind != "bignumber" {
		t.Fatalf("the link did not open its preset: %+v", first.Board.Widgets)
	}
	if first.Remark != "" || first.Locked {
		t.Fatalf("a new link is not blank: remark %q locked %v", first.Remark, first.Locked)
	}

	if code := patchShare(t, ts, link.ID,
		`{"name":"wall","remark":"meeting room three",`+
			`"board":{"grid":12,"fill":true,"widgets":[{"kind":"statebar","span":12}]}}`); code !=
		http.StatusNoContent {
		t.Fatalf("PATCH = %d, want 204", code)
	}

	// No reload, no socket, no second request shape: the same poll the screen
	// was already making.
	after := decodeDashboard(t, shareGETAs(t, ts, link.Token, "aa11", 3840, 2160))
	if after.Remark != "meeting room three" {
		t.Errorf("remark = %q; the owner's label did not reach the screen", after.Remark)
	}
	if len(after.Board.Widgets) != 1 || after.Board.Widgets[0].Kind != "statebar" {
		t.Errorf("board = %+v; the owner's edit did not reach the screen", after.Board.Widgets)
	}
	if !after.Board.Fill {
		t.Error("fill did not reach the screen; a board drawn for a wall arrived as a page")
	}
}

// A remark is the owner's sentence to whoever is looking, so it is disclosed at
// both settings -- and that is a decision rather than an oversight.
//
// `detail` governs whether the *panel's* words leave the machine. `name` has
// always been sent in both modes for exactly this reason. Suppress a remark
// under `counts` and the owner labels a wall with a label the wall never shows,
// which they work around by putting it in `name`, which is disclosed anyway.
func TestTheRemarkIsShownUnderBothDetailModes(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"secret project"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"secret work","command":[]}`)

	for _, detail := range []string{"counts", "names"} {
		link := newShare(t, ts,
			`{"name":"wall","detail":"`+detail+`","remark":"for the customer",`+
				`"preset":"attention"}`)
		_, body := shareGET(t, ts, link.Token)
		got := decodeDashboard(t, body)
		if got.Remark != "for the customer" {
			t.Errorf("detail %q: remark = %q, want the owner's own words", detail, got.Remark)
		}
		// And the mode still does its own job.
		named := strings.Contains(string(body), "secret work")
		if (detail == "names") != named {
			t.Errorf("detail %q disclosed names = %v", detail, named)
		}
	}
}

// The remark is cut by runes and not by bytes.
//
// Remove store.TruncateRemark, or spell it as remark[:MaxRemark], and this
// fails: a byte slice through a multi-byte character renders the last one as
// U+FFFD on a screen behind somebody's desk.
func TestARemarkIsBoundedInRunesAndNotInBytes(t *testing.T) {
	ts, _ := newTestServer(t)
	long := strings.Repeat("会", store.MaxRemark+40)
	link := newShare(t, ts, `{"name":"wall","remark":"`+long+`"}`)

	_, body := shareGET(t, ts, link.Token)
	got := decodeDashboard(t, body)
	if n := len([]rune(got.Remark)); n != store.MaxRemark {
		t.Errorf("remark kept %d runes, want %d", n, store.MaxRemark)
	}
	if strings.ContainsRune(got.Remark, '�') {
		t.Error("the remark was cut through a character; a byte bound reached a wall")
	}

	// The same on the way through an edit, which is the path with its own
	// call site and therefore its own chance to forget.
	if code := patchShare(t, ts, link.ID,
		`{"name":"wall","remark":"`+long+`","board":{"widgets":[{"kind":"states"}]}}`); code !=
		http.StatusNoContent {
		t.Fatalf("PATCH = %d, want 204", code)
	}
	_, body = shareGET(t, ts, link.Token)
	if n := len([]rune(decodeDashboard(t, body).Remark)); n != store.MaxRemark {
		t.Errorf("an edited remark kept %d runes, want %d", n, store.MaxRemark)
	}
}

// A locked board is a guard and not a message.
//
// What it prevents is not an attacker: it is the wall a customer is sitting in
// front of being rearranged from an editor left open on the wrong row, with
// several links in a list. So it is enforced on the server, and the only edit a
// locked link accepts is the one that unlocks it -- applying nothing else,
// because a request that could unlock *and* apply a board makes the lock one
// step instead of two.
func TestALockedBoardRefusesEveryEditExceptUnlocking(t *testing.T) {
	ts, _ := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall","preset":"single","locked":true}`)

	before := decodeDashboard(t, shareGETAs(t, ts, link.Token, "aa11", 1920, 1080))
	if !before.Locked {
		t.Fatal("a link created locked did not arrive locked")
	}

	edit := `{"name":"changed","remark":"changed",` +
		`"board":{"grid":12,"widgets":[{"kind":"statebar","span":12}]}}`
	if code := patchShare(t, ts, link.ID, edit); code != http.StatusConflict {
		t.Fatalf("editing a locked board = %d, want 409", code)
	}
	// Including one that says "locked": true alongside the edit, which is the
	// shape a client that had not refetched would send.
	if code := patchShare(t, ts, link.ID,
		strings.TrimSuffix(edit, "}")+`,"locked":true}`); code != http.StatusConflict {
		t.Fatalf("editing a locked board while re-asserting the lock = %d, want 409", code)
	}
	still := decodeDashboard(t, shareGETAs(t, ts, link.Token, "aa11", 1920, 1080))
	if still.Name != "wall" || still.Board.Widgets[0].Kind != "bignumber" {
		t.Errorf("a refused edit changed the screen anyway: %q %+v",
			still.Name, still.Board.Widgets)
	}

	// The unlocking request applies the unlock and nothing else, even carrying
	// a board.
	if code := patchShare(t, ts, link.ID,
		`{"name":"sneaky","remark":"sneaky","locked":false,`+
			`"board":{"grid":12,"widgets":[{"kind":"statebar","span":12}]}}`); code !=
		http.StatusNoContent {
		t.Fatalf("unlocking = %d, want 204", code)
	}
	opened := decodeDashboard(t, shareGETAs(t, ts, link.Token, "aa11", 1920, 1080))
	if opened.Locked {
		t.Error("the link is still locked after an unlock")
	}
	if opened.Name != "wall" || opened.Board.Widgets[0].Kind != "bignumber" {
		t.Errorf("unlocking carried an edit with it: %q %+v", opened.Name, opened.Board.Widgets)
	}

	// And now the ordinary edit lands.
	if code := patchShare(t, ts, link.ID, edit); code != http.StatusNoContent {
		t.Fatalf("editing an unlocked board = %d, want 204", code)
	}
	if got := decodeDashboard(t, shareGETAs(t, ts, link.Token, "aa11", 1920, 1080)); got.Name !=
		"changed" {
		t.Errorf("the edit after unlocking did not land: %q", got.Name)
	}
}

// Locking and unlocking are their own audit lines.
func TestLockingAndUnlockingAShareLinkIsAudited(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)
	patchShare(t, ts, link.ID,
		`{"name":"wall","locked":true,"board":{"widgets":[{"kind":"states"}]}}`)
	patchShare(t, ts, link.ID, `{"locked":false}`)

	entries, err := srv.DB.RecentAudit(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Event] = true
	}
	for _, want := range []string{"share.locked", "share.unlocked"} {
		if !seen[want] {
			t.Errorf("nothing recorded %s", want)
		}
	}
}

// What a viewer says about itself changes nothing about what it is told.
//
// The three query parameters are the only thing a share token's holder can
// vary, and the property that has to hold is that they are recorded and never
// read back. Make one of them select a scope, a range or a field, and this
// fails.
func TestWhatAViewerSaysAboutItselfCannotChangeTheDashboard(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"p"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"work","command":[]}`)
	link := newShare(t, ts, `{"name":"wall","detail":"names","preset":"overview"}`)

	strip := func(raw []byte) string {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		// Three things move on their own between two calls a millisecond
		// apart: the clock, the ring that grows by a point, and the machine,
		// whose free memory and free disk are a live reading. Everything else
		// is what the link discloses, and none of it may depend on what the
		// caller said about its own screen.
		delete(m, "at")
		delete(m, "trend")
		delete(m, "machine")
		out, _ := json.Marshal(m)
		return string(out)
	}

	base := strip(shareGETAs(t, ts, link.Token, "aa11", 1280, 720))
	for _, probe := range []struct {
		viewer string
		w, h   int
	}{
		{"bb22", 3840, 2160},
		{"cc33", 1, 1},
		{"", 0, 0},
		{"../../etc/passwd", 99999, 99999},
		{strings.Repeat("f", 512), -5, -5},
	} {
		got := strip(shareGETAs(t, ts, link.Token, probe.viewer, probe.w, probe.h))
		if got != base {
			t.Errorf("a viewer saying v=%q w=%d h=%d was told something different:\n%s\n%s",
				probe.viewer, probe.w, probe.h, base, got)
		}
	}
}

// How many screens have this open, and what happens when one is unplugged.
//
// The count is derived from the polls the screens were already making, so a
// viewer that dies silently needs no cleanup: nothing is held open to notice
// dying, and the entry ages out. Remove the TTL prune and this test hangs a
// phantom viewer on the row forever.
func TestTheOwnerSeesHowManyScreensHaveALinkOpen(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)

	if got := listShares(t, ts); len(got) != 1 || got[0].Viewers != 0 {
		t.Fatalf("a link nobody has opened reports %d viewers", got[0].Viewers)
	}

	shareGETAs(t, ts, link.Token, "aa11", 3840, 2160)
	shareGETAs(t, ts, link.Token, "aa11", 3840, 2160) // the same screen, twice
	shareGETAs(t, ts, link.Token, "bb22", 390, 844)   // a phone as well

	rows := listShares(t, ts)
	if rows[0].Viewers != 2 {
		t.Errorf("viewers = %d, want 2: one screen polling twice is one screen",
			rows[0].Viewers)
	}
	// The largest, not the most recent: the owner is composing for the
	// television, not for the phone somebody checked it from.
	if rows[0].ViewportWidth != 3840 || rows[0].ViewportHeight != 2160 {
		t.Errorf("viewport = %dx%d, want the largest screen on the link",
			rows[0].ViewportWidth, rows[0].ViewportHeight)
	}

	// Unplugged. Nothing tells the panel; the entries simply stop being
	// refreshed. Driven by moving the clock rather than by sleeping.
	n, _, _ := srv.viewers.count(link.ID, time.Now().Add(shareViewerTTL+time.Second))
	if n != 0 {
		t.Errorf("%d viewers still counted after the TTL; a screen that was switched off "+
			"is on the owner's row forever", n)
	}

	// And a revoked link forgets its screens at once rather than for the next
	// fifteen seconds, which is the moment somebody is looking hardest.
	shareGETAs(t, ts, link.Token, "aa11", 3840, 2160)
	if code := revokeShare(t, ts, link.ID); code != http.StatusNoContent {
		t.Fatalf("revoke = %d", code)
	}
	if n, _, _ := srv.viewers.count(link.ID, time.Now()); n != 0 {
		t.Errorf("a revoked link still reports %d viewers", n)
	}
}

// Viewers are not told the count, and that is deliberate.
//
// It is a fact about other people holding the same URL. A link that discloses
// nothing about who holds it should not start telling one holder that another
// exists -- and under `counts` the entire point is that the screen says nothing
// about people.
func TestAViewerIsNotToldWhoElseIsWatching(t *testing.T) {
	ts, _ := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)
	shareGETAs(t, ts, link.Token, "aa11", 1920, 1080)
	shareGETAs(t, ts, link.Token, "bb22", 1920, 1080)

	body := shareGETAs(t, ts, link.Token, "cc33", 1920, 1080)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"viewers", "viewportWidth", "viewportHeight"} {
		if _, found := m[field]; found {
			t.Errorf("the dashboard carries %q; one holder of a URL is being told about "+
				"another", field)
		}
	}
}

// The preview is the same builder, behind a session, and off the share surface.
//
// Two failures it exists to prevent, and they pull in opposite directions. A
// preview written on the frontend from /api/state would diverge from the real
// redaction on the first field either side gained, in the direction "the
// preview shows something the real screen does not". A preview mounted under
// the share token would be the second route below requireShareToken.
func TestThePreviewIsTheSameRedactionAndNeedsASession(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"secret project"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"secret work","command":[]}`)
	link := newShare(t, ts, `{"name":"wall","detail":"counts","preset":"overview"}`)

	res, err := ts.Client().Get(ts.URL + "/api/settings/shares/" + link.ID + "/preview")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d: %s", res.StatusCode, body)
	}
	got := decodeDashboard(t, body)
	if got.Detail != "counts" || got.Name != "wall" {
		t.Errorf("the preview is not this link: %+v", got)
	}
	// The redaction, not the panel's own state. A preview that leaked what the
	// dashboard hides would be a settings page quietly holding the answer the
	// owner is trying to check they are not disclosing.
	if strings.Contains(string(body), "secret work") ||
		strings.Contains(string(body), "secret project") ||
		strings.Contains(string(body), project.ID) {
		t.Errorf("the preview disclosed what the link does not:\n%s", body)
	}

	// A share token is not a session, here as everywhere.
	req, err := http.NewRequest(http.MethodGet,
		ts.URL+"/api/settings/shares/"+link.ID+"/preview", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+link.Token)
	r, err := anonymousClient(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("the preview answered %d to a share token, want 401", r.StatusCode)
	}

	// And it is not reachable under the share token's own prefix either.
	for _, path := range []string{
		"/api/share/" + link.Token + "/preview",
		"/api/share/" + link.Token + "/settings/shares",
	} {
		p, err := anonymousClient(t).Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		p.Body.Close()
		if p.StatusCode == http.StatusOK {
			t.Errorf("GET %s succeeded; the share surface is one GET", path)
		}
	}
}

// A moving line is a section like any other: it arrives only if the board asked.
//
// The point of the whole "a board can only subtract" rule. Delete the `needs`
// check around the trend and every link starts carrying fifteen minutes of
// machine readings whether or not anything draws them.
func TestTheTrendArrivesOnlyForABoardThatDrawsOne(t *testing.T) {
	ts, _ := newTestServer(t)

	plain := newShare(t, ts, `{"name":"a","board":{"widgets":[{"kind":"states"}]}}`)
	if got := decodeDashboard(t, shareGETAs(t, ts, plain.Token, "aa11", 800, 600)); got.Trend !=
		nil {
		t.Errorf("a board with no line carries %d trend points", len(got.Trend.Points))
	}

	drawn := newShare(t, ts,
		`{"name":"b","board":{"widgets":[{"kind":"machinearea","by":"cpu"}]}}`)
	got := decodeDashboard(t, shareGETAs(t, ts, drawn.Token, "aa11", 800, 600))
	if got.Trend == nil {
		t.Fatal("a board with a machine line carries no trend")
	}
	if got.Trend.Every != int(trendSampleEvery/time.Second) {
		t.Errorf("trend.every = %d, want %d seconds", got.Trend.Every,
			int(trendSampleEvery/time.Second))
	}
	if len(got.Trend.Points) == 0 {
		t.Error("the first poll of a board with a line drew nothing at all")
	}
}
