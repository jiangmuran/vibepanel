package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// A page whose server.js does, per action, one of the things §5 says it may
// or may not do.
const serverManifest = `{"sdk":1,"name":"Counter","sections":["sessions"],
	"data":{
		"announcement":{"type":"text","max":40},
		"notes":{"type":"text","visibility":"admin"},
		"votes":{"type":"counter"}
	},
	"admin":{"entry":"admin/index.html"},
	"server":{"entry":"server.js","every":"1m"},
	"actions":{
		"tally":{"who":"visitor","effect":"server","writes":["announcement"]},
		"sneak":{"who":"visitor","effect":"server","writes":["announcement"]},
		"swallow":{"who":"visitor","effect":"server"},
		"halfway":{"who":"visitor","effect":"server"},
		"spin":{"who":"visitor","effect":"server"},
		"huge":{"who":"visitor","effect":"server"},
		"reach":{"who":"visitor","effect":"server"},
		"again":{"who":"visitor","effect":"server"},
		"echo":{"who":"visitor","effect":"server","input":{"msg":{"type":"text","max":20}}},
		"reset":{"who":"admin","effect":"server"}
	}}`

var serverFiles = map[string]string{
	"admin/index.html": `<!doctype html><p>admin</p>`,
	"server.js": `
var calls = 0
function transform(input) {
  return { votes: input.data.votes, seesNotes: 'notes' in input.data, ctx: typeof arguments[1], sessions: Array.isArray(input.snapshot.sessions) }
}
function onSchedule(ctx) { ctx.data.increment('votes', 10) }
function onAdminAction(name, payload, ctx) {
  ctx.data.reset('votes')
  ctx.data.set('notes', 'by admin')
  ctx.log('reset', { n: 1 })
  return { ok: true, notes: ctx.data.get('notes') }
}
function onVisitorAction(name, payload, ctx) {
  switch (name) {
  case 'tally':
    ctx.data.increment('votes')
    ctx.data.set('announcement', 'tallied')
    return { total: ctx.data.get('votes'), visitor: ctx.visitor }
  case 'sneak':
    ctx.data.increment('votes')
    ctx.data.set('notes', 'pwned')
    return 1
  case 'swallow':
    try { ctx.data.set('notes', 'pwned') } catch (e) {}
    return 'swallowed'
  case 'halfway':
    ctx.data.increment('votes')
    throw new Error('changed my mind')
  case 'spin':
    ctx.data.increment('votes')
    for (;;) {}
  case 'huge':
    ctx.data.increment('votes')
    return 'x'.repeat(70000)
  case 'reach':
    return [typeof require, typeof fetch, typeof setTimeout, typeof process, typeof XMLHttpRequest, typeof module].join(',')
  case 'echo':
    return payload.msg
  case 'again':
    calls++
    return calls
  }
}`,
}

func pageValues(t *testing.T, ts interface{ Client() *http.Client }, url string) map[string]any {
	t.Helper()
	code, out := dataOp(t, ts, url, http.MethodGet, "")
	if code != http.StatusOK {
		t.Fatalf("GET data = %d %v", code, out)
	}
	values, _ := out["values"].(map[string]any)
	return values
}

func TestServerJSWritesOnlyWhatItsActionAllowsAndOnlyOnSuccess(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, serverManifest, serverFiles)
	kiosk := pageLink(t, ts, p.page.ID, `,"interactive":true`)
	dataURL := ts.URL + "/api/settings/pages/" + p.page.ID + "/data"

	code, out, _ := visitorAction(t, ts, kiosk.Token, "tally", "")
	res, _ := out.Result.(map[string]any)
	if code != http.StatusOK || !out.OK || res["total"] != float64(1) {
		t.Fatalf("tally = %d %+v", code, out)
	}
	if v, _ := res["visitor"].(map[string]any); v["link"] != "wall" || v["id"] == "" {
		t.Errorf("ctx.visitor = %v", res["visitor"])
	}
	if v := pageValues(t, ts, dataURL); v["votes"] != float64(1) || v["announcement"] != "tallied" {
		t.Fatalf("after tally: %v", v)
	}

	// Refused, and each leaves every key as it was -- including the increment
	// each made before it failed.
	for name, want := range map[string]string{
		"sneak":   "writes",
		"halfway": "changed my mind",
		"spin":    "budget",
		"huge":    "KiB",
	} {
		start := time.Now()
		code, out, _ := visitorAction(t, ts, kiosk.Token, name, "")
		if code != http.StatusBadRequest || out.OK || !strings.Contains(out.Error, want) {
			t.Errorf("%s = %d %+v, want 400 mentioning %q", name, code, out, want)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("%s took %v; a visitor's budget is %v", name, took, visitorBudget)
		}
	}
	// A refusal the code catches still refuses.
	if code, out, _ := visitorAction(t, ts, kiosk.Token, "swallow", ""); code != http.StatusOK || out.Result != "swallowed" {
		t.Errorf("swallow = %d %+v", code, out)
	}
	if v := pageValues(t, ts, dataURL); v["votes"] != float64(1) || v["notes"] != "" {
		t.Errorf("after the refused calls: %v", v)
	}

	// What server.js is handed was checked as a visitor's text first: no data
	// write stands behind it to catch a line break later.
	if code, out, _ := visitorAction(t, ts, kiosk.Token, "echo", `{"msg":"a\nb"}`); code != http.StatusBadRequest {
		t.Errorf("echo with a line break = %d %+v", code, out)
	}
	if _, out, _ := visitorAction(t, ts, kiosk.Token, "echo", `{"msg":"hi"}`); out.Result != "hi" {
		t.Errorf("echo = %+v", out)
	}

	// Nothing to reach, and nothing kept between calls.
	if _, out, _ := visitorAction(t, ts, kiosk.Token, "reach", ""); out.Result != "undefined,undefined,undefined,undefined,undefined,undefined" {
		t.Errorf("reach = %+v", out)
	}
	for i := 0; i < 2; i++ {
		if _, out, _ := visitorAction(t, ts, kiosk.Token, "again", ""); out.Result != float64(1) {
			t.Errorf("call %d saw state from an earlier one: %+v", i, out)
		}
	}

	// What went wrong is in the log, in settings and in the draft directory.
	logs := getJSON[struct{ Lines []serverLogLine }](t, ts, "/api/settings/pages/"+p.page.ID+"/server/log")
	joined := ""
	for _, l := range logs.Lines {
		joined += l.Level + " " + l.Text + "\n"
	}
	for _, want := range []string{"error onVisitorAction: ", "budget", "changed my mind"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the server log does not mention %q:\n%s", want, joined)
		}
	}
	if file, err := os.ReadFile(filepath.Join(p.dir, ".vibepanel", "server.log")); err != nil || !strings.Contains(string(file), "changed my mind") {
		t.Errorf(".vibepanel/server.log = %q, %v", file, err)
	}
}

func TestServerJSForAdminsTransformAndSchedule(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, serverManifest, serverFiles)
	kiosk := pageLink(t, ts, p.page.ID, `,"interactive":true`)
	dataURL := ts.URL + "/api/settings/pages/" + p.page.ID + "/data"
	// The watch loop the first reading starts runs onSchedule at once; hold
	// it off, so this test is the one deciding when it is due.
	setScheduled := func(at time.Time) {
		srv.pb.server.mu.Lock()
		srv.pb.server.schedule = map[string]time.Time{p.page.ID + "|" + store.PageDataLive: at}
		srv.pb.server.mu.Unlock()
	}
	setScheduled(time.Now())
	visitorAction(t, ts, kiosk.Token, "tally", "")

	// An admin action may set any key, and its log reaches settings.
	grant := openAdmin(t, ts, p.page.ID, "")
	code, out := adminCall(t, ts, http.MethodPost, grant, "/actions/reset", "")
	if res, _ := out["result"].(map[string]any); code != http.StatusOK || res["notes"] != "by admin" {
		t.Fatalf("reset = %d %v", code, out)
	}
	if v := pageValues(t, ts, dataURL); v["votes"] != float64(0) || v["notes"] != "by admin" {
		t.Errorf("after reset: %v", v)
	}
	logs := getJSON[struct{ Lines []serverLogLine }](t, ts, "/api/settings/pages/"+p.page.ID+"/server/log")
	if n := len(logs.Lines); n == 0 || logs.Lines[n-1].Text != `reset {"n":1}` || logs.Lines[n-1].Level != "info" {
		t.Errorf("log = %+v", logs.Lines)
	}
	// A visitor cannot run the admin action.
	if code, _, _ := visitorAction(t, ts, kiosk.Token, "reset", ""); code != http.StatusNotFound {
		t.Errorf("a visitor ran reset: %d", code)
	}

	// transform sees what the snapshot it transforms sees -- not admin data
	// on a share link -- and gets no ctx to write with.
	_, snap, _ := snapshotGET(t, ts, kiosk.Token)
	tr, _ := snap.Server.(map[string]any)
	if tr["votes"] != float64(0) || tr["seesNotes"] != false || tr["ctx"] != "undefined" || tr["sessions"] != true {
		t.Errorf("snapshot.server = %v", snap.Server)
	}
	code, admin := adminCall(t, ts, http.MethodGet, grant, "/snapshot", "")
	if tr, _ := admin["server"].(map[string]any); code != http.StatusOK || tr["seesNotes"] != true {
		t.Errorf("the admin snapshot's server = %v", admin["server"])
	}

	// onSchedule runs when due, once per period.
	ctx := context.Background()
	page, _ := srv.DB.SharePageByID(ctx, p.page.ID)
	m, _ := srv.pageManifestFor(ctx, page, store.PageDataLive)
	srv.runSchedule(ctx, page, store.PageDataLive, m)
	if v := pageValues(t, ts, dataURL); v["votes"] != float64(0) {
		t.Errorf("onSchedule ran before it was due: votes = %v", v["votes"])
	}
	setScheduled(time.Now().Add(-2 * time.Minute))
	srv.runSchedule(ctx, page, store.PageDataLive, m)
	srv.runSchedule(ctx, page, store.PageDataLive, m)
	if v := pageValues(t, ts, dataURL); v["votes"] != float64(10) {
		t.Errorf("after two schedule ticks in one period: votes = %v", v["votes"])
	}
}

// A Preview runs the draft's server.js, and a draft that does not compile
// says so rather than running the published one.
func TestAPreviewRunsTheDraftServerJS(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, serverManifest, serverFiles)
	writeTestFile(t, filepath.Join(p.dir, "server.js"), `function onVisitorAction( {`)
	prev := postJSON[map[string]any](t, ts, "/api/settings/pages/"+p.page.ID+"/preview", `{"detail":"counts"}`)
	if code, out, _ := visitorAction(t, ts, prev["token"].(string), "tally", ""); code != http.StatusBadRequest || !strings.Contains(out.Error, "does not compile") {
		t.Errorf("a broken draft = %d %+v", code, out)
	}
	writeTestFile(t, filepath.Join(p.dir, "server.js"), `function onVisitorAction(name, payload, ctx) { return 'draft' }`)
	if _, out, _ := visitorAction(t, ts, prev["token"].(string), "tally", ""); out.Result != "draft" {
		t.Errorf("the Preview ran %+v, want the draft's code", out)
	}
	kiosk := pageLink(t, ts, p.page.ID, `,"interactive":true`)
	if _, out, _ := visitorAction(t, ts, kiosk.Token, "again", ""); out.Result != float64(1) {
		t.Errorf("the live link ran %+v, want the published code", out)
	}
}
