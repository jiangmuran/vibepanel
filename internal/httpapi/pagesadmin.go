package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/git"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Managing share pages: making one, publishing it, trying it on a screen,
// pointing a link at it. All of it behind the ordinary session, like every
// other settings route -- a share token answers 401 to every one of these, and
// none of them is under /api/share.
//
// The part of this file worth reading first is what the Preview pane needs,
// because that is where the design is: a preview is a real share link, minted
// here for fifteen minutes, drawn through exactly the route a wall uses. The
// alternative -- a signed-in preview endpoint that renders the page with the
// owner's cookie -- cannot work (a sandboxed document sends no cookie; see
// preview.go) and would be a second path to the same bytes if it could.

// previewLinkTTL is how long a preview link lives without being renewed.
//
// Short, because it is a real capability in a URL that is sitting in the
// settings page's DOM; long enough that a pane left open while somebody reads
// the agent's output does not go dark. The pane renews it while it is open.
const previewLinkTTL = 15 * time.Minute

// maxTrialMinutes bounds a trial on a real screen. An hour is already lunch.
const maxTrialMinutes = 60

// defaultTrialMinutes is what "Try on a screen" does when not told.
const defaultTrialMinutes = 10

// maxReportedErrors bounds what the Preview pane writes into errors.json.
const maxReportedErrors = 50

func (s *Server) registerPageRoutes(r chi.Router) {
	r.Get("/settings/pages", s.handleListPages)
	r.Post("/settings/pages", s.handleCreatePage)
	r.Get("/settings/pages/catalogue", s.handlePageCatalogue)
	r.Get("/settings/pages/{pageID}", s.handleGetPage)
	r.Patch("/settings/pages/{pageID}", s.handleUpdatePage)
	r.Delete("/settings/pages/{pageID}", s.handleDeletePage)
	r.Get("/settings/pages/{pageID}/draft", s.handlePageDraft)
	r.Get("/settings/pages/{pageID}/draft/fingerprint", s.handlePageFingerprint)
	r.Post("/settings/pages/{pageID}/publish", s.handlePublishPage)
	r.Post("/settings/pages/{pageID}/rollback", s.handleRollbackPage)
	r.Post("/settings/pages/{pageID}/preview", s.handlePreviewPage)
	r.Post("/settings/pages/{pageID}/preview/{linkID}/renew", s.handleRenewPreview)
	r.Put("/settings/pages/{pageID}/errors", s.handlePageErrors)
	r.Post("/settings/pages/{pageID}/trial", s.handleStartTrial)
	r.Post("/settings/pages/{pageID}/trial/{linkID}/keep", s.handleKeepTrial)
	r.Delete("/settings/pages/{pageID}/trial/{linkID}", s.handleEndTrial)
	r.Post("/settings/pages/{pageID}/fork", s.handleForkPage)
	r.Post("/settings/pages/{pageID}/open", s.handleOpenPage)
	r.Get("/settings/pages/{pageID}/export", s.handleExportPage)
	r.Post("/settings/pages/import", s.handleImportPage)
	r.Put("/settings/pages/root", s.handlePutPagesRoot)
	r.Put("/settings/shares/{shareID}/page", s.handleSetSharePage)
}

// ─── list, read, catalogue ────────────────────────────────────────────────

// pageRow is a page as the settings list shows it.
type pageRow struct {
	store.SharePage
	// Links is how many handed-out links draw this page.
	Links int `json:"links"`
	// SourceExists is false when the draft directory has gone, which is what
	// the list says instead of offering a Preview that cannot work.
	SourceExists bool `json:"sourceExists"`
}

func (s *Server) handleListPages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.DB.ListSharePages(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	links, err := s.DB.ListShareLinks(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	counts := map[string]int{}
	for _, l := range links {
		if l.PageID != "" {
			counts[l.PageID]++
		}
	}
	out := make([]pageRow, 0, len(list))
	for _, p := range list {
		out = append(out, pageRow{SharePage: p, Links: counts[p.ID], SourceExists: dirExists(p.SourceDir)})
	}
	writeJSON(w, http.StatusOK, out)
}

// pageDetail is one page with its history and the screens drawing it.
type pageDetail struct {
	Page         store.SharePage          `json:"page"`
	SourceExists bool                     `json:"sourceExists"`
	Versions     []store.SharePageVersion `json:"versions"`
	Links        []store.ShareLink        `json:"links"`
}

func (s *Server) handleGetPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	versions, err := s.DB.ListSharePageVersions(ctx, page.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	links, err := s.DB.ShareLinksForPage(ctx, page.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	now := time.Now()
	for i := range links {
		links[i].Viewers, links[i].ViewportWidth, links[i].ViewportHeight = s.viewers.count(links[i].ID, now)
	}
	writeJSON(w, http.StatusOK, pageDetail{Page: page, SourceExists: dirExists(page.SourceDir),
		Versions: versions, Links: links})
}

// pageCatalogue is the vocabulary the New page dialog and the Preview pane are
// built from. Served rather than mirrored, for the reason shareCatalogue is.
type pageCatalogue struct {
	Templates   []pages.Template `json:"templates"`
	Sections    []string         `json:"sections"`
	Viewports   []pages.Viewport `json:"viewports"`
	Fixtures    []string         `json:"fixtures"`
	ScriptHosts []string         `json:"scriptHosts"`
	// PagesRoot is where a new page's directory goes when none is given, and
	// PagesRootInfo why: the owner's setting, the default, or a fallback.
	PagesRoot     string    `json:"pagesRoot"`
	PagesRootInfo PagesRoot `json:"pagesRootInfo"`
	SDK           int       `json:"sdk"`
	MaxFiles      int       `json:"maxFiles"`
	MaxBytes      int       `json:"maxBytes"`
}

func (s *Server) handlePageCatalogue(w http.ResponseWriter, r *http.Request) {
	fixtures := []string{}
	for name := range PageFixtures() {
		fixtures = append(fixtures, name)
	}
	sort.Strings(fixtures)
	root := s.pagesRoot(r.Context())
	writeJSON(w, http.StatusOK, pageCatalogue{
		Templates: pages.Templates(), Sections: pages.Sections(), Viewports: pages.Viewports,
		Fixtures: fixtures, ScriptHosts: pages.ScriptHosts, PagesRoot: root.Dir, PagesRootInfo: root,
		SDK: pages.SDKVersion, MaxFiles: pages.MaxFiles, MaxBytes: pages.MaxBytes,
	})
}

// PageDirPrefix starts the name of every directory, and every project, the
// panel makes for a page. So `page-lobby` in the sidebar is recognisably a
// share page's project and not somebody's repository called lobby.
const PageDirPrefix = "page-"

// NewPageDir is where a page called name goes when nobody said where:
// <root>/page-<name>, or -2, -3… for the first that is free.
func NewPageDir(root, name string) string {
	return freeDir(root, PageDirPrefix+dirName(name))
}

// dirName is a page name as a directory name: letters and digits of any
// script, lower-cased, everything else a single dash.
//
// Not pages.Slug, which keeps ASCII only and is right for what it names. A
// directory is what the sidebar calls the page's project, and most pages here
// are named in Chinese: 「走廊电视墙」 slugged to "page" made every one of them
// page-page, page-page-2, page-page-3 -- a list nobody can tell apart.
func dirName(name string) string {
	var b strings.Builder
	dash := false
	n := 0
	for _, r := range strings.ToLower(name) {
		if n >= 40 {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			n++
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "page"
	}
	return out
}

// ─── create, update, delete ───────────────────────────────────────────────

type createPageRequest struct {
	Name string `json:"name"`
	// Template scaffolds a new directory. Empty means SourceDir already holds
	// a page, which is adopted as it is.
	Template string `json:"template"`
	// SourceDir is an absolute directory. Empty with a template means a new
	// directory under the data directory's pages/, named page-<slug>.
	SourceDir string `json:"sourceDir"`
}

func (s *Server) handleCreatePage(w http.ResponseWriter, r *http.Request) {
	var req createPageRequest
	if !decode(w, r, &req) {
		return
	}
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > pages.MaxName {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a page needs a name of at most %d characters", pages.MaxName))
		return
	}
	dir := strings.TrimSpace(expandHome(req.SourceDir))
	if dir != "" && !filepath.IsAbs(dir) {
		writeErr(w, http.StatusBadRequest, "sourceDir must be an absolute path")
		return
	}
	if dir != "" {
		dir = filepath.Clean(dir)
	}

	if req.Template == "" {
		if dir == "" {
			writeErr(w, http.StatusBadRequest, "a template, or the directory of an existing page")
			return
		}
		if _, err := pages.ReadManifestFile(dir); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		if dir == "" {
			var derr error
			if dir, derr = s.newPageDir(r.Context(), name); derr != nil {
				writeErr(w, http.StatusInternalServerError, derr.Error())
				return
			}
		}
		if err := pages.Scaffold(dir, req.Template, name, PageFixtures()); err != nil {
			status := http.StatusBadRequest
			if !errors.Is(err, pages.ErrNotEmpty) && !strings.HasPrefix(err.Error(), "no template") {
				status = http.StatusInternalServerError
			}
			writeErr(w, status, err.Error())
			return
		}
		pages.GitInit(r.Context(), dir)
	}

	page, err := s.DB.CreateSharePage(r.Context(), id.New(), u.ID, name, dir)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(r.Context(), "page.created", u.Username, s.clientIP(r), name+" ("+dir+")")
	writeJSON(w, http.StatusCreated, page)
}

// freeDir is root/slug, or root/slug-2, -3… for the first that does not exist.
func freeDir(root, slug string) string {
	candidate := filepath.Join(root, slug)
	for n := 2; dirExists(candidate) || fileExists(candidate); n++ {
		candidate = filepath.Join(root, slug+"-"+strconv.Itoa(n))
	}
	return candidate
}

type updatePageRequest struct {
	Name      string `json:"name"`
	SourceDir string `json:"sourceDir"`
}

func (s *Server) handleUpdatePage(w http.ResponseWriter, r *http.Request) {
	var req updatePageRequest
	if !decode(w, r, &req) {
		return
	}
	page, err := s.DB.SharePageByID(r.Context(), chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = page.Name
	}
	if utf8.RuneCountInString(name) > pages.MaxName {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a name is at most %d characters", pages.MaxName))
		return
	}
	dir := page.SourceDir
	if strings.TrimSpace(req.SourceDir) != "" {
		dir = filepath.Clean(expandHome(strings.TrimSpace(req.SourceDir)))
		if !filepath.IsAbs(dir) {
			writeErr(w, http.StatusBadRequest, "sourceDir must be an absolute path")
			return
		}
	}
	if err := s.DB.UpdateSharePage(r.Context(), page.ID, name, dir); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok && dir != page.SourceDir {
		// Where a publish reads from is worth a line; a rename is not.
		s.audit(r.Context(), "page.moved", u.Username, s.clientIP(r), name+" ("+dir+")")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeletePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	links, err := s.DB.ShareLinksForPage(ctx, page.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if len(links) > 0 {
		// Refused rather than cascaded. The cascade would be a wall silently
		// drawing something else, or nothing, with nobody standing at it.
		names := make([]string, 0, len(links))
		for _, l := range links {
			names = append(names, l.Name)
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "links still draw this page: " + strings.Join(names, ", "),
			"links": names,
		})
		return
	}
	if err := s.DB.DeleteSharePage(ctx, page.ID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "page.deleted", u.Username, s.clientIP(r), page.Name)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── the draft ────────────────────────────────────────────────────────────

// pageDraft is what the Preview pane knows about the directory being edited.
type pageDraft struct {
	Fingerprint string `json:"fingerprint"`
	// OK is false when the directory cannot be read as a page at all; Error
	// says why, in words the person or the agent can act on.
	OK       bool            `json:"ok"`
	Error    string          `json:"error"`
	Manifest *pages.Manifest `json:"manifest"`
	Files    []pages.File    `json:"files"`
	Ignored  []pages.Ignored `json:"ignored"`
	Problems []pages.Problem `json:"problems"`
	Bytes    int64           `json:"bytes"`
	// Changes is the draft against the published version, by file hash.
	Changes pageChanges `json:"changes"`
	// SDKCurrent is false when the directory's copy of the SDK is older than
	// the panel's. The page itself always loads the panel's.
	SDKCurrent bool   `json:"sdkCurrent"`
	Commit     string `json:"commit"`
	Dirty      bool   `json:"dirty"`
}

type pageChanges struct {
	Against  int      `json:"against"`
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Modified []string `json:"modified"`
	// Manifest says vibepanel.json differs from the published one.
	Manifest bool `json:"manifest"`
}

func (s *Server) handlePageFingerprint(w http.ResponseWriter, r *http.Request) {
	page, err := s.DB.SharePageByID(r.Context(), chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	fp, ferr := pages.Fingerprint(page.SourceDir)
	if ferr != nil {
		writeErr(w, http.StatusNotFound, "the draft directory cannot be read")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fp})
}

func (s *Server) handlePageDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out := pageDraft{Files: []pages.File{}, Ignored: []pages.Ignored{}, Problems: []pages.Problem{},
		Changes: pageChanges{Added: []string{}, Removed: []string{}, Modified: []string{}}}
	out.Fingerprint, _ = pages.Fingerprint(page.SourceDir)
	b, problems := pages.LintDir(page.SourceDir)
	if b.Files == nil {
		out.Error = problems[0].Message
	} else {
		out.OK = true
		m := b.Manifest
		out.Manifest = &m
		out.Files, out.Ignored, out.Problems, out.Bytes = b.Files, b.Ignored, emptyIfNil(problems), b.Bytes
		out.Changes = s.draftChanges(r, page, b)
	}
	out.SDKCurrent = pages.SDKCurrent(page.SourceDir)
	if st, gerr := git.ReadStatus(ctx, page.SourceDir); gerr == nil {
		out.Commit, out.Dirty = st.Head, st.Dirty()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) draftChanges(r *http.Request, page store.SharePage, b pages.Bundle) pageChanges {
	out := pageChanges{Against: page.PublishedVersion, Added: []string{}, Removed: []string{},
		Modified: []string{}}
	if page.PublishedVersion == 0 {
		for _, f := range b.Files {
			out.Added = append(out.Added, f.Path)
		}
		out.Manifest = true
		return out
	}
	published, err := s.DB.SharePageFiles(r.Context(), page.ID, page.PublishedVersion)
	if err != nil {
		return out
	}
	was := map[string]string{}
	for _, f := range published {
		was[f.Path] = fmt.Sprintf("%x", f.SHA256)
	}
	now := map[string]bool{}
	for _, f := range b.Files {
		now[f.Path] = true
		sum, had := was[f.Path]
		switch {
		case !had:
			out.Added = append(out.Added, f.Path)
		case sum != f.SHA256:
			out.Modified = append(out.Modified, f.Path)
		}
	}
	for _, f := range published {
		if !now[f.Path] {
			out.Removed = append(out.Removed, f.Path)
		}
	}
	if v, err := s.DB.SharePageVersionByNumber(r.Context(), page.ID, page.PublishedVersion); err == nil {
		draft, _ := b.Manifest.Encode()
		stored, _ := pages.DecodeStored(v.Manifest).Encode()
		out.Manifest = string(draft) != string(stored)
	}
	return out
}

// ─── publish, roll back ───────────────────────────────────────────────────

type publishRequest struct {
	Note string `json:"note"`
}

// maxNote bounds a publish note, in runes.
const maxNote = 200

func (s *Server) handlePublishPage(w http.ResponseWriter, r *http.Request) {
	var req publishRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	version, err := s.freezeDraft(r, page, req.Note, false)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "page.published", u.Username, s.clientIP(r),
			page.Name+" v"+strconv.Itoa(version))
	}
	writeJSON(w, http.StatusOK, map[string]int{"version": version})
}

// freezeDraft reads the draft directory whole and stores it as the next
// version: published, or a candidate for a trial.
//
// The same reader the Preview pane used, so what was previewed is what is
// stored. Lint problems do not block it -- they are the page's author's to
// weigh -- but anything ReadDir refuses does.
func (s *Server) freezeDraft(r *http.Request, page store.SharePage, note string, candidate bool) (int, error) {
	return FreezeDraft(r.Context(), s.DB, page, note, candidate)
}

// FreezeDraft is freezeDraft without a request, for `vibepanel page publish`.
// One function so the CLI and the Publish button cannot store different
// things from the same directory.
func FreezeDraft(ctx context.Context, db *store.DB, page store.SharePage, note string, candidate bool) (int, error) {
	if page.SourceDir == "" {
		return 0, errors.New("this page has no draft directory")
	}
	b, err := pages.ReadDir(page.SourceDir)
	if err != nil {
		return 0, err
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > maxNote {
		note = string([]rune(note)[:maxNote])
	}
	manifest, err := b.Manifest.Encode()
	if err != nil {
		return 0, err
	}
	in := store.NewSharePageVersion{Manifest: manifest, Note: note, Publish: !candidate, Candidate: candidate}
	if st, gerr := git.ReadStatus(ctx, page.SourceDir); gerr == nil {
		in.CommitSHA, in.Dirty = st.Head, st.Dirty()
	}
	for _, f := range b.Files {
		in.Files = append(in.Files, store.NewSharePageFile{Path: f.Path, ContentType: f.ContentType, Data: f.Data})
	}
	version, err := db.AddSharePageVersion(ctx, page.ID, in)
	if err != nil {
		return 0, err
	}
	if !candidate {
		_ = RecordHistory(page, version, note, in.CommitSHA, in.Dirty)
	}
	return version, nil
}

// recordHistory appends to the draft's .vibepanel/HISTORY.md, best effort: a
// directory the panel cannot write to has still published.
func (s *Server) recordHistory(page store.SharePage, version int, note, commit string, dirty bool) {
	if err := RecordHistory(page, version, note, commit, dirty); err != nil {
		s.Log.Debug("page history", "page", page.ID, "err", err)
	}
}

// RecordHistory writes one line of a page's publish history.
func RecordHistory(page store.SharePage, version int, note, commit string, dirty bool) error {
	line := fmt.Sprintf("v%d · %s", version, time.Now().Format("2006-01-02 15:04"))
	if commit != "" {
		short := commit
		if len(short) > 8 {
			short = short[:8]
		}
		line += " · " + short
		if dirty {
			line += " (uncommitted changes)"
		}
	}
	if note != "" {
		line += " · " + note
	}
	return pages.AppendHistory(page.SourceDir, line)
}

type rollbackRequest struct {
	Version int `json:"version"`
}

func (s *Server) handleRollbackPage(w http.ResponseWriter, r *http.Request) {
	var req rollbackRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	v, err := s.DB.SharePageVersionByNumber(ctx, page.ID, req.Version)
	if err != nil || v.Candidate {
		// A candidate is kept through its trial, not rolled back to: it was
		// never published, so "back" is not a direction it can be in.
		writeErr(w, http.StatusBadRequest, "no published version "+strconv.Itoa(req.Version))
		return
	}
	if err := s.DB.PublishSharePageVersion(ctx, page.ID, req.Version); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "page.rolled_back", u.Username, s.clientIP(r),
			page.Name+" to v"+strconv.Itoa(req.Version))
	}
	s.recordHistory(page, req.Version, "rolled back", "", false)
	w.WriteHeader(http.StatusNoContent)
}

// ─── preview links ────────────────────────────────────────────────────────

type previewPageRequest struct {
	Detail  string `json:"detail"`
	Scope   string `json:"scope"`
	ScopeID string `json:"scopeId"`
}

func (s *Server) handlePreviewPage(w http.ResponseWriter, r *http.Request) {
	var req previewPageRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if !dirExists(page.SourceDir) {
		writeErr(w, http.StatusBadRequest, "this page's draft directory is not there")
		return
	}
	detail := store.ShareDetail(req.Detail)
	if detail == "" {
		detail = store.ShareCounts
	}
	if !store.ValidShareDetail(detail) {
		writeErr(w, http.StatusBadRequest, "detail must be counts or names")
		return
	}
	scope, scopeID, err := s.scopeFor(ctx, req.Scope, req.ScopeID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	link, token, err := MintPreviewLink(ctx, s.DB, page, detail, scope, scopeID, u.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// Not audited. A preview link is minted every time a pane opens and says
	// nothing a link the owner could make in the same click would not; a row
	// per pane would bury the share.created rows that matter.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": link.ID, "token": token, "expiresAt": link.ExpiresAt,
	})
}

// MintPreviewLink makes a fifteen-minute share link that draws a page's draft.
//
// Exported for `vibepanel page shot`, which needs exactly what the Preview
// pane needs: a real link through the real route, so a screenshot cannot show
// something a wall would not.
func MintPreviewLink(ctx context.Context, db *store.DB, page store.SharePage, detail store.ShareDetail,
	scope store.ShareScope, scopeID, userID string) (store.ShareLink, string, error) {
	_ = db.SweepPreviewLinks(ctx)
	token, err := auth.NewToken()
	if err != nil {
		return store.ShareLink{}, "", err
	}
	link, err := db.CreateShareLink(ctx, store.NewShareLink{
		ID: id.New(), TokenHash: auth.HashToken(token), Prefix: token[:8], Name: page.Name,
		Detail: detail, Scope: scope, ScopeID: scopeID, UserID: userID,
		ExpiresAt: time.Now().Add(previewLinkTTL).Unix(), PageID: page.ID, Purpose: store.SharePurposePreview,
	})
	return link, token, err
}

func (s *Server) handleRenewPreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	link, err := s.DB.ShareLinkByID(ctx, chi.URLParam(r, "linkID"))
	if err != nil || link.Purpose != store.SharePurposePreview || link.PageID != chi.URLParam(r, "pageID") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if link.ExpiresAt <= time.Now().Unix() {
		// Expired is expired. The pane mints a fresh one, which is the same
		// click, and a renewal that could revive a dead token is a way to keep
		// one alive that leaked.
		writeErr(w, http.StatusGone, "that preview link has expired")
		return
	}
	expires := time.Now().Add(previewLinkTTL).Unix()
	if err := s.DB.RenewShareLink(ctx, link.ID, expires); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"expiresAt": expires})
}

// pageError is one thing a preview frame reported.
type pageError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Source  string `json:"source"`
	Line    int    `json:"line"`
}

type pageErrorsRequest struct {
	Errors []pageError `json:"errors"`
}

// handlePageErrors writes what the Preview pane saw into the draft directory,
// for the agent to read.
//
// The pane is signed in and relays what a sandboxed frame posted to it; the
// frame cannot reach this route itself. Bounded in count and length, because
// the content started life as a message from a page.
func (s *Server) handlePageErrors(w http.ResponseWriter, r *http.Request) {
	var req pageErrorsRequest
	if !decode(w, r, &req) {
		return
	}
	page, err := s.DB.SharePageByID(r.Context(), chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if len(req.Errors) > maxReportedErrors {
		req.Errors = req.Errors[:maxReportedErrors]
	}
	for i := range req.Errors {
		e := &req.Errors[i]
		e.Kind = clip(e.Kind, 20)
		e.Message = clip(e.Message, 500)
		e.Source = clip(e.Source, 200)
	}
	raw, err := json.MarshalIndent(map[string]any{
		"at": time.Now().Unix(), "errors": emptyIfNil(req.Errors),
	}, "", "  ")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := pages.WriteMeta(page.SourceDir, "errors.json", append(raw, '\n')); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ─── trials ───────────────────────────────────────────────────────────────

type trialRequest struct {
	LinkID  string `json:"linkId"`
	Minutes int    `json:"minutes"`
}

// handleStartTrial freezes the draft and puts it on one screen for a while.
//
// Only a link already drawing this page. A trial that could take over a
// screen drawing something else would have to remember what that was to give
// it back, and a screen that forgets is the failure a trial ends by itself to
// prevent.
func (s *Server) handleStartTrial(w http.ResponseWriter, r *http.Request) {
	var req trialRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	link, err := s.DB.ShareLinkByID(ctx, req.LinkID)
	if err != nil || link.Purpose != "" || link.PageID != page.ID {
		writeErr(w, http.StatusBadRequest, "a trial goes on a link that already draws this page")
		return
	}
	if link.Locked {
		writeErr(w, http.StatusConflict, "this link is locked")
		return
	}
	minutes := req.Minutes
	if minutes == 0 {
		minutes = defaultTrialMinutes
	}
	if minutes < 1 || minutes > maxTrialMinutes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a trial is 1 to %d minutes", maxTrialMinutes))
		return
	}
	version, err := s.freezeDraft(r, page, "", true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	until := time.Now().Add(time.Duration(minutes) * time.Minute).Unix()
	if err := s.DB.SetShareLinkPage(ctx, link.ID, page.ID, version, until, link.Params); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "share.page_trial", u.Username, s.clientIP(r),
			fmt.Sprintf("%s on %s, v%d for %dm", page.Name, link.Name, version, minutes))
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "pinUntil": until})
}

func (s *Server) handleKeepTrial(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, link, ok := s.trialOf(w, r)
	if !ok {
		return
	}
	if err := s.DB.PublishSharePageVersion(ctx, page.ID, link.PinVersion); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if err := s.DB.SetShareLinkPage(ctx, link.ID, page.ID, 0, 0, link.Params); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "page.published", u.Username, s.clientIP(r),
			page.Name+" v"+strconv.Itoa(link.PinVersion)+" (kept from a trial)")
	}
	s.recordHistory(page, link.PinVersion, "kept after a trial on "+link.Name, "", false)
	writeJSON(w, http.StatusOK, map[string]int{"version": link.PinVersion})
}

func (s *Server) handleEndTrial(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, link, ok := s.trialOf(w, r)
	if !ok {
		return
	}
	if err := s.DB.SetShareLinkPage(ctx, link.ID, page.ID, 0, 0, link.Params); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "share.page_trial", u.Username, s.clientIP(r),
			fmt.Sprintf("%s on %s ended", page.Name, link.Name))
	}
	w.WriteHeader(http.StatusNoContent)
}

// trialOf resolves a running trial from the path, answering when there is none.
func (s *Server) trialOf(w http.ResponseWriter, r *http.Request) (store.SharePage, store.ShareLink, bool) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return store.SharePage{}, store.ShareLink{}, false
	}
	link, err := s.DB.ShareLinkByID(ctx, chi.URLParam(r, "linkID"))
	if err != nil || link.Purpose != "" || link.PageID != page.ID || link.PinVersion == 0 ||
		link.PinUntil == 0 || link.PinUntil <= time.Now().Unix() {
		writeErr(w, http.StatusNotFound, "no trial is running on that link")
		return store.SharePage{}, store.ShareLink{}, false
	}
	return page, link, true
}

// ─── fork ─────────────────────────────────────────────────────────────────

type forkPageRequest struct {
	Name      string `json:"name"`
	SourceDir string `json:"sourceDir"`
}

func (s *Server) handleForkPage(w http.ResponseWriter, r *http.Request) {
	var req forkPageRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	src, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = src.Name + " (fork)"
	}
	if utf8.RuneCountInString(name) > pages.MaxName {
		name = string([]rune(name)[:pages.MaxName])
	}
	b, err := pages.ReadDir(src.SourceDir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir := strings.TrimSpace(expandHome(req.SourceDir))
	switch {
	case dir == "":
		var derr error
		if dir, derr = s.newPageDir(ctx, name); derr != nil {
			writeErr(w, http.StatusInternalServerError, derr.Error())
			return
		}
	case !filepath.IsAbs(dir):
		writeErr(w, http.StatusBadRequest, "sourceDir must be an absolute path")
		return
	default:
		dir = filepath.Clean(dir)
	}
	if err := pages.ScaffoldFrom(dir, name, b, PageFixtures()); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pages.GitInit(ctx, dir)
	page, err := s.DB.CreateSharePage(ctx, id.New(), u.ID, name, dir)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "page.created", u.Username, s.clientIP(r), name+" ("+dir+", forked from "+src.Name+")")
	writeJSON(w, http.StatusCreated, page)
}

// ─── pointing a link at a page ────────────────────────────────────────────

type setSharePageRequest struct {
	// PageID is the page to draw.
	PageID string `json:"pageId"`
	// PinVersion holds the link on one published version; 0 follows whatever
	// is published.
	PinVersion int            `json:"pinVersion"`
	Params     map[string]any `json:"params"`
}

// handleSetSharePage changes what a link draws and the page's settings on it.
//
// Not what it discloses: detail and scope stay what they were, for the reason
// handleUpdateShare gives.
func (s *Server) handleSetSharePage(w http.ResponseWriter, r *http.Request) {
	var req setSharePageRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	link, err := s.DB.ShareLinkByID(ctx, chi.URLParam(r, "shareID"))
	if err != nil || link.Purpose != "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if link.Locked {
		writeErr(w, http.StatusConflict, "this link is locked")
		return
	}
	params, err := s.pageLinkSettings(r, req.PageID, req.PinVersion, req.Params)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.SetShareLinkPage(ctx, link.ID, req.PageID, req.PinVersion, 0, params); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		// Two events rather than one with a word in it: "who changed what the
		// lobby screen draws" and "who changed its title" are different
		// questions, and each literal is on TestEveryAuditEventIsAccountedFor's
		// list only if it is written out here.
		switch {
		case req.PageID == link.PageID && req.PinVersion == link.PinVersion:
			s.audit(ctx, "share.params_changed", u.Username, s.clientIP(r), link.Name)
		default:
			detail := link.Name + " → " + req.PageID
			if req.PinVersion > 0 {
				detail += " v" + strconv.Itoa(req.PinVersion)
			}
			s.audit(ctx, "share.page_changed", u.Username, s.clientIP(r), detail)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// pageLinkSettings checks a page, a pin and parameter values for a link, and
// returns the values as they will be stored.
func (s *Server) pageLinkSettings(r *http.Request, pageID string, pin int,
	values map[string]any) (map[string]any, error) {
	if pageID == "" {
		return nil, errors.New("a link draws a page: pageId is required")
	}
	page, err := s.DB.SharePageByID(r.Context(), pageID)
	if err != nil {
		return nil, errors.New("no such page")
	}
	if page.PublishedVersion == 0 {
		return nil, errors.New("that page has not been published yet")
	}
	version := page.PublishedVersion
	if pin != 0 {
		v, verr := s.DB.SharePageVersionByNumber(r.Context(), page.ID, pin)
		if verr != nil || v.Candidate || pin < 0 {
			return nil, fmt.Errorf("that page has no published version %d", pin)
		}
		version = pin
	}
	v, err := s.DB.SharePageVersionByNumber(r.Context(), page.ID, version)
	if err != nil {
		return nil, err
	}
	return pages.ValidateParamValues(pages.DecodeStored(v.Manifest).Params, values)
}

// ─── small helpers ────────────────────────────────────────────────────────

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
