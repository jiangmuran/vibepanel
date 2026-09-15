package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/pages"
)

// A kiosk page: data of every kind a visitor or an admin writes, an admin
// page, server code, and actions. Shared by the backend tests.
const kioskManifest = `{"sdk":1,"name":"Kiosk","sections":["sessions"],
	"data":{
		"announcement":{"type":"text","max":40,"default":"hello"},
		"goals":{"type":"list","item":{"type":"text","max":20},"max":3},
		"votes":{"type":"counter"},
		"guestbook":{"type":"log","item":{"type":"object","fields":{"name":{"type":"text","max":10},"msg":{"type":"text","max":20}}},"max":2},
		"notes":{"type":"text","visibility":"admin"},
		"blob":{"type":"json","maxBytes":65536}
	},
	"admin":{"entry":"admin/index.html"},
	"server":{"entry":"server.js"},
	"actions":{
		"vote":{"who":"visitor","effect":{"increment":"votes"},"rate":"3/min"},
		"sign":{"who":"both","effect":{"append":"guestbook"}},
		"announce":{"who":"admin","effect":{"set":"announcement"}},
		"tally":{"who":"visitor","effect":"server","writes":["announcement"]}
	}}`

var kioskFiles = map[string]string{
	"admin/index.html": `<!doctype html><script src="../vibepanel.js"></script><p id="admin">admin</p>`,
	"server.js":        `function onVisitorAction(name, payload, ctx) { ctx.data.increment('votes'); return { total: ctx.data.get('votes') } }`,
}

func dataOp(t *testing.T, ts interface {
	Client() *http.Client
}, url, method, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestPageDataIsDeclaredCheckedAndOnTheNextPoll(t *testing.T) {
	ts, srv := newTestServer(t)
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	base := ts.URL + "/api/settings/pages/" + p.page.ID + "/data"
	link := pageLink(t, ts, p.page.ID, "")

	got := getJSON[pageDataResponse](t, ts, "/api/settings/pages/"+p.page.ID+"/data")
	if got.Values["announcement"] != "hello" || got.Values["votes"] != float64(0) || got.Limit != 256<<10 {
		t.Fatalf("defaults = %+v", got.Values)
	}
	if _, ok := got.Values["notes"]; !ok {
		t.Error("settings does not see an admin-visibility key")
	}

	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPut, "/announcement", `{"value":"lunch at noon"}`, http.StatusOK},
		{http.MethodPost, "/votes/increment", `{"by":2}`, http.StatusOK},
		{http.MethodPost, "/guestbook/append", `{"item":{"name":"ann","msg":"hi"}}`, http.StatusOK},
		{http.MethodPut, "/notes", `{"value":"for the owner"}`, http.StatusOK},
		// Refused, and each for its own reason.
		{http.MethodPut, "/votes", `{"value":9}`, http.StatusBadRequest},
		{http.MethodPut, "/guestbook", `{"value":[]}`, http.StatusBadRequest},
		{http.MethodPut, "/announcement", `{"value":"` + strings.Repeat("x", 41) + `"}`, http.StatusBadRequest},
		{http.MethodPut, "/goals", `{"value":["a","b","c","d"]}`, http.StatusBadRequest},
		{http.MethodPost, "/guestbook/append", `{"item":{"name":"ann","extra":1}}`, http.StatusBadRequest},
		{http.MethodPut, "/undeclared", `{"value":1}`, http.StatusBadRequest},
		{http.MethodPut, "/blob", `{"value":"` + strings.Repeat("x", 60000) + `"}`, http.StatusOK},
	} {
		if code, body := dataOp(t, ts, base+tc.path, tc.method, tc.body); code != tc.want {
			t.Errorf("%s %s = %d %v, want %d", tc.method, tc.path, code, body, tc.want)
		}
	}
	for i := 0; i < 3; i++ {
		dataOp(t, ts, base+"/guestbook/append", http.MethodPost, `{"item":{"name":"n","msg":"m"}}`)
	}

	code, snap, _ := snapshotGET(t, ts, link.Token)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if snap.Data["announcement"] != "lunch at noon" || snap.Data["votes"] != float64(2) {
		t.Errorf("the snapshot did not carry the writes: %v", snap.Data)
	}
	if _, leaked := snap.Data["notes"]; leaked {
		t.Error("an admin-visibility key is in a share snapshot")
	}
	if entries, _ := snap.Data["guestbook"].([]any); len(entries) != 2 {
		t.Errorf("guestbook kept %d entries, want its max of 2", len(entries))
	}
	if snap.Interactive || snap.Actions["vote"].Enabled {
		t.Error("a link that is not interactive says its actions work")
	}
	if _, ok := snap.Actions["announce"]; ok {
		t.Error("an admin action is listed on a share link")
	}

	// A write shows on the very next poll, memo or not.
	dataOp(t, ts, base+"/announcement", http.MethodPut, `{"value":"changed"}`)
	if _, again, _ := snapshotGET(t, ts, link.Token); again.Data["announcement"] != "changed" {
		t.Errorf("the next poll still says %v", again.Data["announcement"])
	}

	// Reset goes back to the default; a size over the cap is refused whole.
	if code, _ := dataOp(t, ts, base+"/announcement", http.MethodDelete, ""); code != http.StatusNoContent {
		t.Errorf("reset = %d", code)
	}
	if v := getJSON[pageDataResponse](t, ts, "/api/settings/pages/"+p.page.ID+"/data").Values["announcement"]; v != "hello" {
		t.Errorf("after reset = %v", v)
	}
	for i := 0; i < 5; i++ {
		dataOp(t, ts, base+"/blob", http.MethodPut, `{"value":"`+strings.Repeat("y", 60000)+`"}`)
	}
	// Draft is its own namespace.
	if code, _ := dataOp(t, ts, base+"/votes/increment?ns=draft", http.MethodPost, `{}`); code != http.StatusOK {
		t.Errorf("draft increment = %d", code)
	}
	if v := getJSON[pageDataResponse](t, ts, "/api/settings/pages/"+p.page.ID+"/data").Values["votes"]; v != float64(2) {
		t.Errorf("a draft write changed live: %v", v)
	}
	if !auditHas(t, srv, "page.data_changed", "live: set announcement") {
		t.Error("an owner's write left no audit row")
	}

	detail := getJSON[map[string]any](t, ts, "/api/settings/pages/"+p.page.ID)
	caps, _ := detail["capabilities"].(map[string]any)
	if caps["data"] != true || caps["visitorActions"] != true || caps["admin"] != true {
		t.Errorf("capabilities = %v", caps)
	}
}

func TestAPageDataCapHoldsAcrossKeys(t *testing.T) {
	ts, _ := newTestServer(t)
	manifest := `{"sdk":1,"name":"Big","sections":[],"data":{` +
		`"a":{"type":"json","maxBytes":65536},"b":{"type":"json","maxBytes":65536},"c":{"type":"json","maxBytes":65536},` +
		`"d":{"type":"json","maxBytes":65536},"e":{"type":"json","maxBytes":65536}}}`
	p := newPublishedPage(t, ts, manifest, nil)
	base := ts.URL + "/api/settings/pages/" + p.page.ID + "/data/"
	big := `{"value":"` + strings.Repeat("z", 60000) + `"}`
	for _, k := range []string{"a", "b", "c", "d"} {
		if code, _ := dataOp(t, ts, base+k, http.MethodPut, big); code != http.StatusOK {
			t.Fatalf("%s = %d", k, code)
		}
	}
	if code, body := dataOp(t, ts, base+"e", http.MethodPut, big); code != http.StatusBadRequest {
		t.Errorf("a write past 256 KiB = %d %v, want 400", code, body)
	}
}

// The hostile fixture fills a page's public data with what a visitor could
// write, and only that: markup and long text, never a character the checks
// refuse, never an admin key.
func TestTheHostileFixtureCarriesVisitorText(t *testing.T) {
	m, err := pages.ParseManifest([]byte(kioskManifest))
	if err != nil {
		t.Fatal(err)
	}
	var f pageFixture
	if err := json.Unmarshal(shapeFixture(PageFixtures()["hostile"], m, nil, true), &f); err != nil {
		t.Fatal(err)
	}
	d := f.Snapshot.Data
	raw, _ := json.Marshal(d)
	if !strings.Contains(string(raw), "onerror") || len(d["guestbook"].([]any)) != 2 || len(d["goals"].([]any)) != 3 {
		t.Errorf("hostile data = %s", raw)
	}
	if _, ok := d["notes"]; ok {
		t.Error("the hostile fixture carries an admin key")
	}
	for key, v := range d {
		if _, err := m.Data[key].Check(v, true); err != nil {
			t.Errorf("%s = %v, which a visitor could not have written: %v", key, v, err)
		}
	}
	if err := json.Unmarshal(shapeFixture(PageFixtures()["busy"], m, nil, false), &f); err != nil {
		t.Fatal(err)
	}
	if f.Snapshot.Data["announcement"] != "hello" {
		t.Errorf("busy data = %v, want the defaults", f.Snapshot.Data)
	}
}

func TestExportedDataTravelsWithThePage(t *testing.T) {
	ts, _ := newTestServer(t)
	p := newPublishedPage(t, ts, kioskManifest, kioskFiles)
	dataOp(t, ts, ts.URL+"/api/settings/pages/"+p.page.ID+"/data/announcement", http.MethodPut, `{"value":"moved"}`)

	code, zipped, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/pages/"+p.page.ID+"/export?data=1", "", nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	zr, _ := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	found := false
	for _, f := range zr.File {
		found = found || f.Name == "vibepanel-data.json"
	}
	if !found {
		t.Fatal("export with data=1 carries no vibepanel-data.json")
	}
	code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/pages/import?name=Copy", "application/zip", zipped)
	if code != http.StatusCreated {
		t.Fatalf("import = %d %s", code, body)
	}
	var got importedPage
	_ = json.Unmarshal(body, &got)
	postJSON[map[string]int](t, ts, "/api/settings/pages/"+got.Page.ID+"/publish", `{}`)
	if v := getJSON[pageDataResponse](t, ts, "/api/settings/pages/"+got.Page.ID+"/data").Values["announcement"]; v != "moved" {
		t.Errorf("imported data = %v", v)
	}
}
