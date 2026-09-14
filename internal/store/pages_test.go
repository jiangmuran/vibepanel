package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newPageFixture(t *testing.T) (*DB, SharePage) {
	t.Helper()
	db := openTest(t)
	ctx := context.Background()
	if _, err := db.CreateUser(ctx, "u1", "owner", "hash"); err != nil {
		t.Fatal(err)
	}
	p, err := db.CreateSharePage(ctx, "page-1", "u1", "Lobby", "/tmp/lobby")
	if err != nil {
		t.Fatal(err)
	}
	return db, p
}

func version(files map[string]string, opts NewSharePageVersion) NewSharePageVersion {
	opts.Manifest = []byte(`{"sdk":1,"name":"Lobby"}`)
	for p, body := range files {
		opts.Files = append(opts.Files, NewSharePageFile{Path: p, ContentType: "text/plain", Data: []byte(body)})
	}
	return opts
}

func TestPageVersionsAreImmutableAndShareTheirBytes(t *testing.T) {
	db, p := newPageFixture(t)
	ctx := context.Background()

	v1, err := db.AddSharePageVersion(ctx, p.ID, version(map[string]string{
		"index.html": "one", "style.css": "same"}, NewSharePageVersion{Publish: true}))
	if err != nil || v1 != 1 {
		t.Fatalf("v1 = %d, %v", v1, err)
	}
	v2, err := db.AddSharePageVersion(ctx, p.ID, version(map[string]string{
		"index.html": "two", "style.css": "same"}, NewSharePageVersion{Publish: true}))
	if err != nil || v2 != 2 {
		t.Fatalf("v2 = %d, %v", v2, err)
	}
	var blobs int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM share_page_blobs`).Scan(&blobs); err != nil {
		t.Fatal(err)
	}
	if blobs != 3 {
		t.Errorf("%d blobs for three distinct contents; the unchanged file was stored twice", blobs)
	}
	_, data, err := db.SharePageFileData(ctx, p.ID, 1, "index.html")
	if err != nil || string(data) != "one" {
		t.Errorf("v1's index.html = %q, %v; publishing v2 changed v1", data, err)
	}
	got, _ := db.SharePageByID(ctx, p.ID)
	if got.PublishedVersion != 2 {
		t.Errorf("published = %d", got.PublishedVersion)
	}

	if err := db.PublishSharePageVersion(ctx, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.SharePageByID(ctx, p.ID); got.PublishedVersion != 1 {
		t.Errorf("rollback did not move published: %d", got.PublishedVersion)
	}
	if err := db.PublishSharePageVersion(ctx, p.ID, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("publishing a version that does not exist: %v", err)
	}
}

// A version number is what an open page compares to decide whether to reload.
// Handed out twice, a wall showing the first would never notice the second.
func TestAVersionNumberIsNeverHandedOutTwice(t *testing.T) {
	db, p := newPageFixture(t)
	ctx := context.Background()
	seen := map[int]bool{}
	add := func(opts NewSharePageVersion) int {
		t.Helper()
		v, err := db.AddSharePageVersion(ctx, p.ID, version(map[string]string{"index.html": time.Now().String()}, opts))
		if err != nil {
			t.Fatal(err)
		}
		if seen[v] {
			t.Fatalf("version %d was handed out twice", v)
		}
		seen[v] = true
		return v
	}
	add(NewSharePageVersion{Publish: true})
	for i := 0; i < 4; i++ {
		add(NewSharePageVersion{Candidate: true})
	}
	add(NewSharePageVersion{Publish: true})
	add(NewSharePageVersion{Candidate: true})

	versions, err := db.ListSharePageVersions(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidates := 0
	for _, v := range versions {
		if v.Candidate {
			candidates++
		}
	}
	// Twenty presses of "Try on a screen" must not leave twenty versions.
	if candidates != 1 {
		t.Errorf("%d candidates kept; an unpinned candidate is swept by the next", candidates)
	}
	if _, err := db.AddSharePageVersion(ctx, p.ID, NewSharePageVersion{Candidate: true, Publish: true}); err == nil {
		t.Error("a version that is both a candidate and published was accepted")
	}
}

func TestAPinnedCandidateSurvivesTheSweep(t *testing.T) {
	db, p := newPageFixture(t)
	ctx := context.Background()
	if _, err := db.AddSharePageVersion(ctx, p.ID, version(map[string]string{"index.html": "a"},
		NewSharePageVersion{Publish: true})); err != nil {
		t.Fatal(err)
	}
	cand, err := db.AddSharePageVersion(ctx, p.ID, version(map[string]string{"index.html": "b"},
		NewSharePageVersion{Candidate: true}))
	if err != nil {
		t.Fatal(err)
	}
	link, err := db.CreateShareLink(ctx, NewShareLink{ID: "l1", TokenHash: []byte("h1"), Prefix: "p",
		Name: "wall", Detail: ShareCounts, Board: DefaultBoard(), UserID: "u1", PageID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetShareLinkPage(ctx, link.ID, p.ID, cand, now()+600, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddSharePageVersion(ctx, p.ID, version(map[string]string{"index.html": "c"},
		NewSharePageVersion{Candidate: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SharePageVersionByNumber(ctx, p.ID, cand); err != nil {
		t.Errorf("the candidate a wall is showing was swept: %v", err)
	}
}

func TestATrialResolvesBackToThePublishedVersionWithoutAWrite(t *testing.T) {
	now := int64(1_000_000)
	for _, tc := range []struct {
		name       string
		pin        int
		until      int64
		published  int
		wantResult int
	}{
		{"no pin", 0, 0, 3, 3},
		{"permanent pin", 2, 0, 3, 2},
		{"trial running", 4, now + 60, 3, 4},
		{"trial over", 4, now - 1, 3, 3},
		{"trial ends this second", 4, now, 3, 3},
	} {
		link := ShareLink{PinVersion: tc.pin, PinUntil: tc.until}
		if got := link.ResolvePageVersion(tc.published, now); got != tc.wantResult {
			t.Errorf("%s: resolved %d, want %d", tc.name, got, tc.wantResult)
		}
	}
}

func TestPreviewLinksAreNotListedNotEditableAndSwept(t *testing.T) {
	db, p := newPageFixture(t)
	ctx := context.Background()
	past := now() - 10
	for i, purpose := range []string{"", SharePurposePreview} {
		if _, err := db.CreateShareLink(ctx, NewShareLink{ID: []string{"real", "prev"}[i],
			TokenHash: []byte{byte(i)}, Prefix: "p", Name: "x", Detail: ShareCounts, Board: DefaultBoard(),
			UserID: "u1", PageID: p.ID, Purpose: purpose, ExpiresAt: past + int64(i)*0}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := db.ListShareLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "real" {
		t.Errorf("list = %+v, want only the real link", list)
	}
	if err := db.UpdateShareLink(ctx, "prev", "renamed", "", DefaultBoard(), false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a preview link was editable: %v", err)
	}
	if err := db.SetShareLinkPage(ctx, "prev", "other", 0, 0, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("a preview link was re-pointable: %v", err)
	}
	if err := db.RenewShareLink(ctx, "real", now()+600); !errors.Is(err, ErrNotFound) {
		t.Errorf("a real link's expiry was moved: %v", err)
	}
	if err := db.SweepPreviewLinks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ShareLinkByID(ctx, "prev"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired preview link survived the sweep: %v", err)
	}
	if _, err := db.ShareLinkByID(ctx, "real"); err != nil {
		t.Errorf("the sweep took a real link: %v", err)
	}
}

func TestDeletingAPageTakesItsPreviewLinksAndOrphanBlobs(t *testing.T) {
	db, p := newPageFixture(t)
	ctx := context.Background()
	if _, err := db.AddSharePageVersion(ctx, p.ID, version(map[string]string{"index.html": "gone soon"},
		NewSharePageVersion{Publish: true})); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateShareLink(ctx, NewShareLink{ID: "prev", TokenHash: []byte("t"), Prefix: "p",
		Name: "x", Detail: ShareCounts, Board: DefaultBoard(), UserID: "u1", PageID: p.ID,
		Purpose: SharePurposePreview}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSharePage(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ShareLinkByID(ctx, "prev"); !errors.Is(err, ErrNotFound) {
		t.Error("a preview link outlived its page")
	}
	var blobs, files int
	_ = db.sql.QueryRow(`SELECT COUNT(*) FROM share_page_blobs`).Scan(&blobs)
	_ = db.sql.QueryRow(`SELECT COUNT(*) FROM share_page_files`).Scan(&files)
	if blobs != 0 || files != 0 {
		t.Errorf("%d blobs and %d files left behind", blobs, files)
	}
}

func TestLinkParamsSurviveAGarbageColumn(t *testing.T) {
	db, _ := newPageFixture(t)
	ctx := context.Background()
	if _, err := db.CreateShareLink(ctx, NewShareLink{ID: "l", TokenHash: []byte("x"), Prefix: "p",
		Name: "x", Detail: ShareCounts, Board: DefaultBoard(), UserID: "u1",
		Params: map[string]any{"title": "Kitchen"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := db.ShareLinkByID(ctx, "l")
	if got.Params["title"] != "Kitchen" {
		t.Errorf("params = %v", got.Params)
	}
	if _, err := db.sql.Exec(`UPDATE share_links SET params = 'not json' WHERE id = 'l'`); err != nil {
		t.Fatal(err)
	}
	got, err := db.ShareLinkByID(ctx, "l")
	if err != nil || got.Params == nil || len(got.Params) != 0 {
		t.Errorf("a broken params column = %v, %v; want an empty map and no error", got.Params, err)
	}
}
