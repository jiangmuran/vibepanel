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
// television, so what it shows has to be changeable from somewhere else and the
// change has to arrive without anybody touching the screen. The tests are
// arranged around what that must not cost — the share surface is still GETs,
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

// shareGETAs fetches the snapshot the way one particular screen does.
func shareGETAs(t *testing.T, ts *httptest.Server, token, viewer string, w, h int) []byte {
	t.Helper()
	url := ts.URL + "/api/share/" + token + "/v1/snapshot?v=" + viewer
	if w > 0 || h > 0 {
		url += "&w=" + itoa(w) + "&h=" + itoa(h)
	}
	res, err := anonymousClient(t).Get(url)
	if err != nil {
		t.Fatalf("GET snapshot: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET snapshot = %d: %s", res.StatusCode, body)
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

// lockedOf reads whether a link is locked from the owner's listing, which is
// the only place it is said: the screen has nothing to do with it.
func lockedOf(t *testing.T, ts *httptest.Server, id string) bool {
	t.Helper()
	for _, l := range listShares(t, ts) {
		if l.ID == id {
			return l.Locked
		}
	}
	t.Fatalf("link %s is not listed", id)
	return false
}

// The whole feature, from the owner's side to the wall's, in one pass.
//
// This is the test the work exists for: an owner changes a screen from a
// signed-in client and the screen shows it without anybody touching it. Break
// the live path -- cache the row in the middleware, serve the link's words from
// the snapshot memo, move the read out of the poll -- and this is what says so.
func TestAnOwnersEditReachesAnOpenScreenOnItsNextPoll(t *testing.T) {
	ts, _ := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall","detail":"counts"}`)

	first := decodeSnapshot(t, shareGETAs(t, ts, link.Token, "aa11", 3840, 2160))
	if first.Remark != "" || lockedOf(t, ts, link.ID) {
		t.Fatalf("a new link is not blank: remark %q", first.Remark)
	}
	if first.Page == nil {
		t.Fatal("the link draws no page")
	}

	if code := patchShare(t, ts, link.ID, `{"name":"lobby","remark":"meeting room three"}`); code !=
		http.StatusNoContent {
		t.Fatalf("PATCH = %d, want 204", code)
	}

	// No reload, no socket, no second request shape: the same poll the screen
	// was already making, inside the snapshot memo's second.
	after := decodeSnapshot(t, shareGETAs(t, ts, link.Token, "aa11", 3840, 2160))
	if after.Remark != "meeting room three" || after.Name != "lobby" {
		t.Errorf("name %q remark %q; the owner's edit did not reach the screen", after.Name, after.Remark)
	}

	// And what it draws: pointed at another page, the next poll names it, which
	// is the SDK's cue to reload.
	other := newPublishedPage(t, ts, sessionsManifest, nil)
	if status, out := callJSON(t, ts, http.MethodPut, "/api/settings/shares/"+link.ID+"/page",
		`{"pageId":"`+other.page.ID+`"}`); status != http.StatusNoContent {
		t.Fatalf("point at another page = %d %s", status, out)
	}
	moved := decodeSnapshot(t, shareGETAs(t, ts, link.Token, "aa11", 3840, 2160))
	if moved.Page == nil || moved.Page.ID == first.Page.ID {
		t.Errorf("page %+v; the screen was not told it now draws another page", moved.Page)
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
			`{"name":"wall","detail":"`+detail+`","remark":"for the customer"}`)
		_, body := shareGET(t, ts, link.Token)
		got := decodeSnapshot(t, body)
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
	got := decodeSnapshot(t, body)
	if n := len([]rune(got.Remark)); n != store.MaxRemark {
		t.Errorf("remark kept %d runes, want %d", n, store.MaxRemark)
	}
	if strings.ContainsRune(got.Remark, '�') {
		t.Error("the remark was cut through a character; a byte bound reached a wall")
	}

	// The same on the way through an edit, which is the path with its own
	// call site and therefore its own chance to forget.
	if code := patchShare(t, ts, link.ID,
		`{"name":"wall","remark":"`+long+`"}`); code !=
		http.StatusNoContent {
		t.Fatalf("PATCH = %d, want 204", code)
	}
	_, body = shareGET(t, ts, link.Token)
	if n := len([]rune(decodeSnapshot(t, body).Remark)); n != store.MaxRemark {
		t.Errorf("an edited remark kept %d runes, want %d", n, store.MaxRemark)
	}
}

// A locked link is a guard and not a message.
//
// What it prevents is not an attacker: it is the screen a customer is sitting
// in front of being changed from a settings page left open on the wrong row,
// with several links in a list. So it is enforced on the server, and the only
// edit a locked link accepts is the one that unlocks it -- applying nothing
// else, because a request that could unlock *and* rename makes the lock one
// step instead of two. What it draws is locked too: its page, its pin, its
// parameters and a trial.
func TestALockedLinkRefusesEveryEditExceptUnlocking(t *testing.T) {
	ts, _ := newTestServer(t)
	page := newPublishedPage(t, ts, sessionsManifest, nil)
	other := newPublishedPage(t, ts, sessionsManifest, nil)
	link := newShare(t, ts, `{"name":"wall","locked":true,"pageId":"`+page.page.ID+`"}`)

	if !lockedOf(t, ts, link.ID) {
		t.Fatal("a link created locked did not arrive locked")
	}
	before := decodeSnapshot(t, shareGETAs(t, ts, link.Token, "aa11", 1920, 1080))

	edit := `{"name":"changed","remark":"changed"}`
	if code := patchShare(t, ts, link.ID, edit); code != http.StatusConflict {
		t.Fatalf("editing a locked link = %d, want 409", code)
	}
	// Including one that says "locked": true alongside the edit, which is the
	// shape a client that had not refetched would send.
	if code := patchShare(t, ts, link.ID,
		strings.TrimSuffix(edit, "}")+`,"locked":true}`); code != http.StatusConflict {
		t.Fatalf("editing a locked link while re-asserting the lock = %d, want 409", code)
	}
	for _, refused := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/settings/shares/" + link.ID + "/page", `{"pageId":"` + other.page.ID + `"}`},
		{http.MethodPut, "/api/settings/shares/" + link.ID + "/page",
			`{"pageId":"` + page.page.ID + `","params":{"title":"changed"}}`},
		{http.MethodPost, "/api/settings/pages/" + page.page.ID + "/trial", `{"linkId":"` + link.ID + `"}`},
	} {
		status, out := callJSON(t, ts, refused.method, refused.path, refused.body)
		if status != http.StatusConflict || !strings.Contains(string(out), "this link is locked") {
			t.Errorf("%s %s on a locked link = %d %s, want 409 saying it is locked",
				refused.method, refused.path, status, out)
		}
	}
	still := decodeSnapshot(t, shareGETAs(t, ts, link.Token, "aa11", 1920, 1080))
	if still.Name != "wall" || still.Page == nil || still.Page.ID != before.Page.ID ||
		still.Page.Version != before.Page.Version || still.Params["title"] != before.Params["title"] {
		t.Errorf("a refused edit changed the screen anyway: %q %+v %v", still.Name, still.Page, still.Params)
	}

	// The unlocking request applies the unlock and nothing else.
	if code := patchShare(t, ts, link.ID,
		`{"name":"sneaky","remark":"sneaky","locked":false}`); code != http.StatusNoContent {
		t.Fatalf("unlocking = %d, want 204", code)
	}
	if lockedOf(t, ts, link.ID) {
		t.Error("the link is still locked after an unlock")
	}
	opened := decodeSnapshot(t, shareGETAs(t, ts, link.Token, "aa11", 1920, 1080))
	if opened.Name != "wall" || opened.Remark != "" {
		t.Errorf("unlocking carried an edit with it: %q %q", opened.Name, opened.Remark)
	}

	// And now the ordinary edit lands.
	if code := patchShare(t, ts, link.ID, edit); code != http.StatusNoContent {
		t.Fatalf("editing an unlocked link = %d, want 204", code)
	}
	if got := decodeSnapshot(t, shareGETAs(t, ts, link.Token, "aa11", 1920, 1080)); got.Name !=
		"changed" {
		t.Errorf("the edit after unlocking did not land: %q", got.Name)
	}
}

// Locking and unlocking are their own audit lines.
func TestLockingAndUnlockingAShareLinkIsAudited(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)
	patchShare(t, ts, link.ID, `{"name":"wall","locked":true}`)
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
func TestWhatAViewerSaysAboutItselfCannotChangeTheSnapshot(t *testing.T) {
	ts, _ := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"p"}`)
	postJSON[store.Session](t, ts, "/api/sessions",
		`{"projectId":"`+project.ID+`","title":"work","command":[]}`)
	link := newShare(t, ts, `{"name":"wall","detail":"names"}`)

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
			t.Errorf("the snapshot carries %q; one holder of a URL is being told about "+
				"another", field)
		}
	}
}

// A moving line is a section like any other: it arrives only if the page asked.
//
// The point of the whole "a page can only subtract" rule. Delete the `needs`
// check around the trend and every link starts carrying fifteen minutes of
// machine readings whether or not anything draws them.
func TestTheTrendArrivesOnlyForAPageThatDrawsOne(t *testing.T) {
	ts, _ := newTestServer(t)

	plain := shareOn(t, ts, `{"sdk":1,"name":"A","sections":[]}`, `{"name":"a"}`)
	if got := decodeSnapshot(t, shareGETAs(t, ts, plain.Token, "aa11", 800, 600)); got.Trend !=
		nil {
		t.Errorf("a page with no line carries %d trend points", len(got.Trend.Points))
	}

	drawn := shareOn(t, ts, `{"sdk":1,"name":"B","sections":["trend"]}`, `{"name":"b"}`)
	got := decodeSnapshot(t, shareGETAs(t, ts, drawn.Token, "aa11", 800, 600))
	if got.Trend == nil {
		t.Fatal("a page with a machine line carries no trend")
	}
	if got.Trend.Every != int(trendSampleEvery/time.Second) {
		t.Errorf("trend.every = %d, want %d seconds", got.Trend.Every,
			int(trendSampleEvery/time.Second))
	}
	if len(got.Trend.Points) == 0 {
		t.Error("the first poll of a page with a line drew nothing at all")
	}
}

// One ring per token series, not one ring per directory.
//
// The token half of a point is the spend section's total, and a page with no
// spend section contributes a zero because there is nothing to ask. Two
// whole-panel links both keyed on the empty directory therefore shared a ring:
// the machine page stamped zeros into the one the burn page reads, and
// TokenBurn -- which draws differences between consecutive points -- rendered
// the day's whole running total as a fresh burst every other sample and a rate
// per minute larger than the day.
func TestAPageWithNoSpendCannotZeroAnothersTokenLine(t *testing.T) {
	ts, srv := newTestServer(t)
	project := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"Acme"}`)
	seedUsage(t, srv, project.Path, 6000000)

	machine := shareOn(t, ts, `{"sdk":1,"name":"M","sections":["trend"]}`, `{"name":"m"}`)
	burn := shareOn(t, ts, burnManifest, `{"name":"b"}`)

	// The machine page first, which is the whole trigger: its poll lands the
	// only point trendSampleEvery will allow for the next ten seconds, so the
	// burn page's own reading is dropped and it reads back the zero.
	shareGET(t, ts, machine.Token)

	_, body := shareGET(t, ts, burn.Token)
	got := decodeSnapshot(t, body)
	if got.Spend == nil || got.Spend.Today.Total == 0 {
		t.Fatal("nothing was counted, so this test would pass on any server")
	}
	if got.Trend == nil || len(got.Trend.Points) == 0 {
		t.Fatal("a page with spend and a trend carries no trend at all")
	}
	for i, p := range got.Trend.Points {
		if p.Tokens != got.Spend.Today.Total {
			t.Fatalf("trend point %d carries %d tokens while the same response's spend "+
				"section says %d were spent today; the point was written by another "+
				"page that has no spend section", i, p.Tokens, got.Spend.Today.Total)
		}
	}
}

// A scoped link whose project is gone does not read the whole panel's line.
//
// The same defect wearing the other face, and this one is a disclosure rather
// than a wrong shape. `cwd` is empty for a whole-panel link *and* for a scoped
// link whose target has been deleted, so keying the ring on the directory alone
// put the panel's token history on a link that was deliberately narrowed to one
// project -- while its own spend section, which does check `kind`, correctly
// reported nothing.
func TestALinkWhoseProjectIsGoneDoesNotReadThePanelsTokenLine(t *testing.T) {
	ts, srv := newTestServer(t)
	doomed := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"doomed"}`)
	other := postJSON[store.Project](t, ts, "/api/projects",
		`{"path":"`+t.TempDir()+`","name":"still here"}`)
	seedUsage(t, srv, other.Path, 6000000)

	whole := shareOn(t, ts, burnManifest, `{"name":"w"}`)
	scoped := shareOn(t, ts, burnManifest, `{"name":"s","scope":"project","scopeId":"`+doomed.ID+`"}`)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/projects/"+doomed.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	// The whole-panel link polls first and puts the panel's real total in its
	// ring.
	first := decodeSnapshot(t, shareGETAs(t, ts, whole.Token, "aa11", 800, 600))
	if first.Spend == nil || first.Spend.Today.Total == 0 {
		t.Fatal("nothing was counted, so this test would pass on any server")
	}

	_, body := shareGET(t, ts, scoped.Token)
	got := decodeSnapshot(t, body)
	if got.Trend == nil {
		t.Fatal("a page with spend and a trend carries no trend at all")
	}
	for i, p := range got.Trend.Points {
		if p.Tokens != 0 {
			t.Fatalf("a link scoped to a deleted project drew %d tokens at point %d; the "+
				"whole panel's line has been read into a link narrowed to one project",
				p.Tokens, i)
		}
	}
}

// burnManifest is a token-burn page: spend, and the line it is drawn on.
const burnManifest = `{"sdk":1,"name":"Burn","sections":["spend","trend"]}`
