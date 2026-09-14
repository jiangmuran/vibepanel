package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Where a page lives, how a handed-out link is looked at, and what happened to
// the links boards handed out. Each is about something outliving what it was
// attached to: a directory, a token the panel cannot read back, a feature.

// legacyLink writes a link the way a build with boards left it: a real token,
// a board in its column, and no page.
func legacyLink(t *testing.T, srv *Server, name, detail, remark, board string) string {
	t.Helper()
	ctx := context.Background()
	user, err := srv.DB.FirstUserID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	link, err := srv.DB.CreateShareLink(ctx, store.NewShareLink{
		ID: id.New(), TokenHash: auth.HashToken(token), Prefix: token[:8], Name: name,
		Detail: store.ShareDetail(detail), UserID: user, Remark: remark,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET board = ? WHERE id = ?`, board, link.ID); err != nil {
		t.Fatal(err)
	}
	return token
}

// A wall a build with boards was showing is still showing something after the
// upgrade, at the same address, disclosing exactly what it did.
func TestBoardLinksBecomePagesAtTheSameAddress(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	spendA := legacyLink(t, srv, "costs", "counts", "",
		`{"preset":"cost","widgets":[{"kind":"spendtotals"},{"kind":"spendbars","days":30}]}`)
	spendB := legacyLink(t, srv, "burn", "counts", "",
		`{"widgets":[{"kind":"sparkline"},{"kind":"states"}]}`)
	wall := legacyLink(t, srv, "lobby", "names", "the room by the door",
		`{"preset":"attention","widgets":[{"kind":"attention"}]}`)

	for _, token := range []string{spendA, spendB, wall} {
		if status, _, _ := snapshotGET(t, ts, token); status != http.StatusGone {
			t.Fatalf("before converting, a board link's snapshot = %d, want 410", status)
		}
	}

	if err := srv.ConvertBoardLinks(ctx); err != nil {
		t.Fatal(err)
	}

	pageOf := func(token string) store.ShareLink {
		t.Helper()
		l, err := srv.DB.ShareLinkByToken(ctx, auth.HashToken(token))
		if err != nil {
			t.Fatal(err)
		}
		if l.PageID == "" {
			t.Fatalf("link %s was not converted", l.Name)
		}
		return l
	}
	a, b, w := pageOf(spendA), pageOf(spendB), pageOf(wall)
	if a.PageID != b.PageID {
		t.Error("two spend boards of one owner became two pages; one page per owner and template")
	}
	if a.PageID == w.PageID {
		t.Error("a spend board and a session board became the same page")
	}

	spendPage, err := srv.DB.SharePageByID(ctx, a.PageID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(srv.Cfg.PagesDir(), "page-token-spend"); spendPage.SourceDir != want {
		t.Errorf("the converted page lives in %q, want %q", spendPage.SourceDir, want)
	}
	if _, err := os.Stat(filepath.Join(spendPage.SourceDir, pages.IndexFile)); err != nil {
		t.Errorf("the converted page's directory has no index: %v", err)
	}
	if spendPage.PublishedVersion != 1 {
		t.Errorf("the converted page is at version %d, want 1 published", spendPage.PublishedVersion)
	}

	status, snap, _ := snapshotGET(t, ts, spendA)
	if status != http.StatusOK || snap.Page == nil || snap.Spend == nil {
		t.Errorf("a converted spend link = %d, page %+v, spend %v", status, snap.Page, snap.Spend != nil)
	}
	status, snap, _ = snapshotGET(t, ts, wall)
	if status != http.StatusOK || snap.Detail != "names" || snap.Remark != "the room by the door" ||
		snap.Name != "lobby" {
		t.Errorf("the conversion changed what a link says: %d %q %q %q", status, snap.Detail, snap.Remark, snap.Name)
	}
	if res, _ := anonGET(t, ts, "/share/"+wall+"/"); !strings.Contains(
		res.Header.Get("Content-Security-Policy"), "allow-scripts") {
		t.Error("the converted link's address does not serve its page")
	}

	// Once. A second start finds nothing to convert and makes no more pages.
	before, _ := srv.DB.ListSharePages(ctx)
	if err := srv.ConvertBoardLinks(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := srv.DB.ListSharePages(ctx)
	if len(after) != len(before) || len(before) != 2 {
		t.Errorf("pages: %d after the first run, %d after the second; want 2 and 2", len(before), len(after))
	}

	// A link that already draws a page is not a board link, however its
	// column reads.
	normal := newShare(t, ts, `{"name":"normal"}`)
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET board = '{"widgets":[{"kind":"prs"}]}' WHERE id = ?`,
		normal.ID); err != nil {
		t.Fatal(err)
	}
	was, _ := srv.DB.ShareLinkByID(ctx, normal.ID)
	if err := srv.ConvertBoardLinks(ctx); err != nil {
		t.Fatal(err)
	}
	if now, _ := srv.DB.ShareLinkByID(ctx, normal.ID); now.PageID != was.PageID {
		t.Error("converting board links re-pointed a link that already drew a page")
	}

	entries, err := srv.DB.RecentAudit(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	converted := 0
	for _, e := range entries {
		if e.Event == "share.converted" {
			converted++
		}
	}
	if converted != 3 {
		t.Errorf("%d share.converted rows, want one per converted link (3)", converted)
	}
}

func TestABoardBecomesTheTemplateClosestToIt(t *testing.T) {
	for _, tc := range []struct{ board, want string }{
		{`{"preset":"phone","widgets":[{"kind":"attention"}]}`, "glance"},
		{`{"widgets":[{"kind":"output"},{"kind":"codechurn"}]}`, "built"},
		{`{"widgets":[{"kind":"prs"}]}`, "built"},
		{`{"widgets":[{"kind":"spendtotals"},{"kind":"spendheatmap"}]}`, "spend"},
		{`{"widgets":[{"kind":"tokenburn"}]}`, "spend"},
		{`{"widgets":[{"kind":"output"},{"kind":"spendtotals"},{"kind":"odometer"}]}`, "spend"},
		{`{"widgets":[{"kind":"output"},{"kind":"spendtotals"}]}`, "built"},
		{`{"preset":"phone","widgets":[{"kind":"spendtotals"}]}`, "spend"},
		{`{"widgets":[{"kind":"sessionlist"},{"kind":"feed"}]}`, "wall"},
		{`{{{`, "wall"},
		{``, "wall"},
	} {
		if got := templateForBoard(tc.board); got != tc.want {
			t.Errorf("templateForBoard(%s) = %q, want %q", tc.board, got, tc.want)
		}
	}
}

type openedPage struct {
	Page      store.SharePage `json:"page"`
	ProjectID string          `json:"projectId"`
	Restored  int             `json:"restored"`
}

func openPage(t *testing.T, ts interface {
	Client() *http.Client
}, url, pageID string) openedPage {
	t.Helper()
	res, err := ts.Client().Post(url+"/api/settings/pages/"+pageID+"/open", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("open = %d", res.StatusCode)
	}
	var out openedPage
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A page is its versions in the database, so a lost directory or a deleted
// project is recovered by opening the page, not by knowing a command.
func TestOpeningAPageMakesItSomethingAnAgentCanWorkIn(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()

	page := postJSON[store.SharePage](t, ts, "/api/settings/pages", `{"name":"Lobby","template":"wall"}`)
	if want := filepath.Join(srv.Cfg.PagesDir(), "page-lobby"); page.SourceDir != want {
		t.Fatalf("a new page went to %q, want %q beside the panel's other data", page.SourceDir, want)
	}
	projectAt := func(id string) store.Project {
		t.Helper()
		p, err := srv.DB.GetProject(ctx, id)
		if err != nil {
			t.Fatalf("no project %s: %v", id, err)
		}
		return p
	}

	first := openPage(t, ts, ts.URL, page.ID)
	if first.Restored != 0 || first.ProjectID == "" {
		t.Fatalf("opening a page with its directory = %+v", first)
	}
	if p := projectAt(first.ProjectID); p.Name != "page-lobby" || p.Path != page.SourceDir {
		t.Errorf("the page's project is %q at %q, want page-lobby at its directory", p.Name, p.Path)
	}
	if again := openPage(t, ts, ts.URL, page.ID); again.ProjectID != first.ProjectID {
		t.Error("opening the page twice made a second project on one directory")
	}

	// Published, then the directory is lost.
	postJSON[map[string]int](t, ts, "/api/settings/pages/"+page.ID+"/publish", `{"note":"first"}`)
	if err := os.WriteFile(filepath.Join(page.SourceDir, "index.html"), []byte("unpublished edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(page.SourceDir); err != nil {
		t.Fatal(err)
	}
	restored := openPage(t, ts, ts.URL, page.ID)
	if restored.Restored != 1 || restored.Page.SourceDir != page.SourceDir {
		t.Errorf("a lost directory came back as %+v, want version 1 where it was", restored)
	}
	index, err := os.ReadFile(filepath.Join(page.SourceDir, "index.html"))
	if err != nil || string(index) == "unpublished edit" || len(index) == 0 {
		t.Errorf("the restored index is %q, %v; want the published version", index, err)
	}
	if restored.ProjectID != first.ProjectID {
		t.Error("restoring a directory made a new project beside the one already on it")
	}

	// A project deleted from the sidebar is made again.
	if err := srv.DB.DeleteProject(ctx, first.ProjectID); err != nil {
		t.Fatal(err)
	}
	if reopened := openPage(t, ts, ts.URL, page.ID); reopened.ProjectID == "" || reopened.ProjectID == first.ProjectID {
		t.Errorf("a deleted project was not made again: %+v", reopened)
	}

	// Never published and lost: there is nothing to write back, so a blank page
	// with its name.
	draft := postJSON[store.SharePage](t, ts, "/api/settings/pages", `{"name":"Draft only","template":"spend"}`)
	if err := os.RemoveAll(draft.SourceDir); err != nil {
		t.Fatal(err)
	}
	blank := openPage(t, ts, ts.URL, draft.ID)
	if blank.Restored != -1 {
		t.Errorf("a never-published lost page restored = %d, want -1", blank.Restored)
	}
	if _, err := pages.ReadManifestFile(blank.Page.SourceDir); err != nil {
		t.Errorf("the blank page is not a page: %v", err)
	}

	// A page somebody put in a directory of their own still gets a project that
	// says it is a page.
	adopted := newPublishedPage(t, ts, sessionsManifest, nil)
	if p := projectAt(openPage(t, ts, ts.URL, adopted.page.ID).ProjectID); p.Name != "page-lobby" ||
		p.Path != adopted.dir {
		t.Errorf("an adopted page's project is %q at %q, want page-lobby at %q", p.Name, p.Path, adopted.dir)
	}

	// Where it was cannot be written any more: under the data directory instead,
	// and the page follows it.
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if status, out := callJSON(t, ts, http.MethodPatch, "/api/settings/pages/"+page.ID,
		`{"sourceDir":"`+filepath.Join(blocker, "lobby")+`"}`); status != http.StatusNoContent {
		t.Fatalf("move = %d %s", status, out)
	}
	moved := openPage(t, ts, ts.URL, page.ID)
	if !strings.HasPrefix(moved.Page.SourceDir, srv.Cfg.PagesDir()+string(filepath.Separator)+"page-lobby") {
		t.Errorf("a directory that cannot be written back went to %q", moved.Page.SourceDir)
	}
	if got, _ := srv.DB.SharePageByID(ctx, page.ID); got.SourceDir != moved.Page.SourceDir {
		t.Errorf("the page still says its draft is %q", got.SourceDir)
	}

	entries, err := srv.DB.RecentAudit(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	restoredRows := 0
	for _, e := range entries {
		if e.Event == "page.restored" {
			restoredRows++
		}
	}
	if restoredRows != 3 {
		t.Errorf("%d page.restored rows, want 3", restoredRows)
	}

	if status, _ := callJSON(t, ts, http.MethodPost, "/api/settings/pages/nope/open", ""); status != http.StatusNotFound {
		t.Errorf("opening a page that does not exist = %d, want 404", status)
	}
}

// The owner cannot read a link's token back, so looking at what it shows is a
// copy of it -- and the copy must show what the link shows, and nothing that
// makes it a second link.
func TestViewingALinkDrawsWhatItDrawsAndNothingMore(t *testing.T) {
	ts, srv := newTestServer(t)
	ctx := context.Background()
	project := postJSON[store.Project](t, ts, "/api/projects", `{"path":"`+t.TempDir()+`","name":"ours"}`)

	p := newPublishedPage(t, ts, sessionsManifest, nil)
	postJSON[map[string]int](t, ts, "/api/settings/pages/"+p.page.ID+"/publish", `{"note":"second"}`)
	link := newShare(t, ts, `{"name":"hall","detail":"names","scope":"project","scopeId":"`+project.ID+
		`","pageId":"`+p.page.ID+`"}`)
	if status, out := callJSON(t, ts, http.MethodPut, "/api/settings/shares/"+link.ID+"/page",
		`{"pageId":"`+p.page.ID+`","pinVersion":1,"params":{"title":"Hall"}}`); status != http.StatusNoContent {
		t.Fatalf("pin = %d %s", status, out)
	}

	status, out := callJSON(t, ts, http.MethodPost, "/api/settings/shares/"+link.ID+"/view", "")
	if status != http.StatusCreated {
		t.Fatalf("view = %d %s", status, out)
	}
	var view struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(out, &view); err != nil {
		t.Fatal(err)
	}
	if left := time.Until(time.Unix(view.ExpiresAt, 0)); left <= 0 || left > peekLinkTTL+time.Minute {
		t.Errorf("a view link lives %v, want about %v", left, peekLinkTTL)
	}

	code, snap, _ := snapshotGET(t, ts, view.Token)
	if code != http.StatusOK || snap.Page == nil || snap.Page.Version != 1 || snap.Params["title"] != "Hall" ||
		snap.Detail != "names" || snap.Scope != "project" || snap.ScopeName != "ours" || snap.Page.Draft {
		t.Errorf("the view shows %d %+v %v %q %q %q; want the link's pin, parameters, detail and scope",
			code, snap.Page, snap.Params, snap.Detail, snap.Scope, snap.ScopeName)
	}

	peek, err := srv.DB.ShareLinkByToken(ctx, auth.HashToken(view.Token))
	if err != nil {
		t.Fatal(err)
	}
	if links := listShares(t, ts); len(links) != 1 {
		t.Errorf("%d links listed; a view link is not one somebody handed out", len(links))
	}
	for _, refused := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/settings/shares/" + peek.ID, `{"name":"mine now"}`},
		{http.MethodPut, "/api/settings/shares/" + peek.ID + "/page", `{"pageId":"` + p.page.ID + `"}`},
		{http.MethodPost, "/api/settings/shares/" + peek.ID + "/view", ``},
	} {
		if status, out := callJSON(t, ts, refused.method, refused.path, refused.body); status != http.StatusNotFound {
			t.Errorf("%s %s on a view link = %d %s, want 404", refused.method, refused.path, status, out)
		}
	}

	// A preview link is not a handed-out link either.
	prev := postJSON[struct {
		ID string `json:"id"`
	}](t, ts, "/api/settings/pages/"+p.page.ID+"/preview", `{"detail":"counts"}`)
	if status, _ := callJSON(t, ts, http.MethodPost, "/api/settings/shares/"+prev.ID+"/view", ""); status != http.StatusNotFound {
		t.Errorf("viewing a preview link = %d, want 404", status)
	}

	// And it ends.
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).Unix(), peek.ID); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := snapshotGET(t, ts, view.Token); code != http.StatusUnauthorized {
		t.Errorf("an expired view link = %d, want 401", code)
	}
	if err := srv.DB.SweepPreviewLinks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.ShareLinkByID(ctx, peek.ID); err == nil {
		t.Error("an expired view link survived the sweep")
	}
}

// A page named in Chinese gets a directory a person can tell from the next one.
// Slugging to ASCII made every such page page-page, page-page-2, page-page-3.
func TestAPageDirectoryKeepsTheNameItWasGiven(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, want string }{
		{"走廊电视墙", "page-走廊电视墙"},
		{"Lobby Wall!", "page-lobby-wall"},
		{"本月 token", "page-本月-token"},
		{"  ***  ", "page-page"},
		{strings.Repeat("长", 60), "page-" + strings.Repeat("长", 40)},
	} {
		if got := filepath.Base(NewPageDir(root, tc.name)); got != tc.want {
			t.Errorf("NewPageDir(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
