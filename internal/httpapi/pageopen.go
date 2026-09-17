package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Three things a share page needs that are about where it lives rather than
// what it draws: opening it as a project (and writing it back out when its
// directory has gone), looking at what a handed-out link shows, and turning
// the links a build with boards handed out into links that draw pages.

// CheckoutVersion writes one stored version of a page into dir, which must be
// empty or missing. The files are the version's; the SDK, the types, the
// fixtures and the agent instructions are this build's, exactly as a new page
// gets them.
//
// Shared by `vibepanel page checkout`, the settings page's Open on a page whose
// directory is gone, and nothing else -- so a restored directory and a checked
// out one cannot differ.
func CheckoutVersion(ctx context.Context, db *store.DB, page store.SharePage, version int, dir string) error {
	row, err := db.SharePageVersionByNumber(ctx, page.ID, version)
	if err != nil {
		return err
	}
	files, err := db.SharePageFiles(ctx, page.ID, version)
	if err != nil {
		return err
	}
	b := pages.Bundle{Manifest: pages.DecodeStored(row.Manifest)}
	for _, f := range files {
		_, data, ferr := db.SharePageFileData(ctx, page.ID, version, f.Path)
		if ferr != nil {
			return ferr
		}
		b.Files = append(b.Files, pages.File{Path: f.Path, ContentType: f.ContentType, Data: data})
	}
	return pages.ScaffoldFrom(dir, b.Manifest.Name, b, PageFixtures())
}

// openPageResult is what Open answers: the page as it now is, and the project
// its directory is.
type openPageResult struct {
	Page      store.SharePage `json:"page"`
	ProjectID string          `json:"projectId"`
	// Restored is the version written back into a directory that was gone: 0
	// when nothing had to be, and -1 when the page had never been published
	// and a blank page was the only thing there was to start again from.
	Restored int `json:"restored"`
}

// handleOpenPage makes a page something an agent can be started in: its
// directory exists, and a project points at it.
//
// Both halves can have gone missing without the page going anywhere, because
// the page is its versions in the database. A deleted `page-lobby` project, a
// wiped data directory, a page made on another checkout -- each is recovered
// by the same press of Open rather than by a command somebody has to know.
func (s *Server) handleOpenPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out := openPageResult{}
	if !dirExists(page.SourceDir) {
		dir, restored, rerr := s.restorePage(ctx, page)
		if rerr != nil {
			writeErr(w, http.StatusInternalServerError, "cannot write the page back out: "+rerr.Error())
			return
		}
		if dir != page.SourceDir {
			if uerr := s.DB.UpdateSharePage(ctx, page.ID, page.Name, dir); uerr != nil {
				s.writeStoreErr(w, uerr)
				return
			}
			page.SourceDir = dir
		}
		out.Restored = restored
		if u, ok := currentUserFrom(r); ok {
			s.audit(ctx, "page.restored", u.Username, s.clientIP(r), page.Name+" ("+dir+")")
		}
	}

	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	for _, p := range projects {
		if filepath.Clean(p.Path) == filepath.Clean(page.SourceDir) {
			out.ProjectID = p.ID
			break
		}
	}
	if out.ProjectID == "" {
		p, perr := s.DB.CreateProject(ctx, id.New(), pageProjectName(page), page.SourceDir)
		if perr != nil {
			s.writeStoreErr(w, perr)
			return
		}
		out.ProjectID = p.ID
		s.notifyState()
	}
	out.Page = page
	writeJSON(w, http.StatusOK, out)
}

// pageProjectName is the sidebar name of a page's project: its directory's
// name when that already says page-, and page-<slug> when somebody put the
// page somewhere of their own.
func pageProjectName(page store.SharePage) string {
	if base := filepath.Base(page.SourceDir); strings.HasPrefix(base, PageDirPrefix) {
		return base
	}
	return PageDirPrefix + dirName(page.Name)
}

// restorePage writes a page whose directory has gone back to disk, and says
// where and from which version.
//
// Back where it was when that is possible, and under the data directory when
// it is not -- a directory on a disk that is no longer mounted is not one to
// keep insisting on.
func (s *Server) restorePage(ctx context.Context, page store.SharePage) (string, int, error) {
	write := func(dir string) (int, error) {
		if page.PublishedVersion > 0 {
			if err := CheckoutVersion(ctx, s.DB, page, page.PublishedVersion, dir); err != nil {
				return 0, err
			}
			pages.GitInit(ctx, dir)
			return page.PublishedVersion, nil
		}
		if err := pages.Scaffold(dir, "blank", page.Name, PageFixtures()); err != nil {
			return 0, err
		}
		pages.GitInit(ctx, dir)
		return -1, nil
	}
	if page.SourceDir != "" {
		if v, err := write(page.SourceDir); err == nil {
			return page.SourceDir, v, nil
		}
	}
	dir, err := s.newPageDir(ctx, page.Name)
	if err != nil {
		return "", 0, err
	}
	v, err := write(dir)
	return dir, v, err
}

// peekLinkTTL is how long a peek link lives. Long enough to look, short enough
// that a URL left in a tab is not a second copy of the link.
const peekLinkTTL = 15 * time.Minute

// handleViewShare mints a short-lived link that shows what a handed-out link
// shows.
//
// Not the link's own address, even where a sealed copy of it is kept
// (handleShareURL): an older link has none, and a peek left open in a tab
// should not be a second copy of the credential somebody was handed. So it
// makes another link with the same page, pin, trial, parameters, detail and
// scope, which draws the same screen through the same route. Not listed, not
// editable, and swept when it expires, like a preview link.
func (s *Server) handleViewShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	link, err := s.DB.ShareLinkByID(ctx, chi.URLParam(r, "shareID"))
	if err != nil || link.Purpose != "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if link.PageID == "" {
		writeErr(w, http.StatusConflict, "this link draws no page")
		return
	}
	token, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.DB.SweepPreviewLinks(ctx)
	peek, err := s.DB.CreateShareLink(ctx, store.NewShareLink{
		ID: id.New(), TokenHash: auth.HashToken(token), Prefix: token[:8], Name: link.Name,
		Detail: store.ShareDetail(link.Detail), Scope: store.ShareScope(link.Scope), ScopeID: link.ScopeID,
		UserID: u.ID, Remark: link.Remark, ExpiresAt: time.Now().Add(peekLinkTTL).Unix(),
		PageID: link.PageID, PinVersion: link.PinVersion, PinUntil: link.PinUntil,
		Params: link.Params, Purpose: store.SharePurposePeek,
	})
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// Not audited, for the preview link's reason: it discloses nothing the
	// owner's own session does not already show them.
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "expiresAt": peek.ExpiresAt})
}

// ─── converting the links boards handed out ───────────────────────────────

// ConvertBoardLinks turns every link a build with boards handed out into a link
// that draws a page, at the same address.
//
// Boards were removed; the URLs on walls and in emails were not, and a wall
// that went dark on an upgrade is the failure nobody at the wall can fix. Each
// link gets the built-in template closest to what its board showed -- one page
// per owner and template, published once and shared by every link that maps to
// it -- with its detail, scope, remark and expiry untouched, because those are
// the disclosure and none of them is the drawing.
//
// Called at startup, before the listener. Idempotent: a converted link is no
// longer listed by LegacyBoardLinks, so a second run finds nothing. A link that
// cannot be converted is logged and left; it answers "this link no longer
// works" until the next start tries again.
func (s *Server) ConvertBoardLinks(ctx context.Context) error {
	legacy, err := s.DB.LegacyBoardLinks(ctx)
	if err != nil || len(legacy) == 0 {
		return err
	}
	made := map[string]string{}
	converted := 0
	for _, l := range legacy {
		tpl := templateForBoard(l.Board)
		key := l.UserID + "\x00" + tpl
		pageID, ok := made[key]
		if !ok {
			page, perr := s.pageFromTemplate(ctx, l.UserID, tpl)
			if perr != nil {
				s.Log.Warn("convert a board link", "link", l.ID, "template", tpl, "err", perr)
				continue
			}
			pageID = page.ID
			made[key] = pageID
		}
		if cerr := s.DB.ConvertBoardLink(ctx, l.ID, pageID); cerr != nil && !errors.Is(cerr, store.ErrNotFound) {
			s.Log.Warn("convert a board link", "link", l.ID, "err", cerr)
			continue
		}
		converted++
		s.audit(ctx, "share.converted", "", "", l.Name+" → "+tpl)
	}
	s.Log.Info("converted board links to pages", "links", converted, "pages", len(made))
	return nil
}

// pageFromTemplate scaffolds a template under the pages directory, registers it
// for userID and publishes it as version 1.
func (s *Server) pageFromTemplate(ctx context.Context, userID, tpl string) (store.SharePage, error) {
	name, err := templateName(tpl)
	if err != nil {
		return store.SharePage{}, err
	}
	dir, err := s.newPageDir(ctx, name)
	if err != nil {
		return store.SharePage{}, err
	}
	if err := pages.Scaffold(dir, tpl, name, PageFixtures()); err != nil {
		return store.SharePage{}, err
	}
	pages.GitInit(ctx, dir)
	page, err := s.DB.CreateSharePage(ctx, id.New(), userID, name, dir)
	if err != nil {
		return store.SharePage{}, err
	}
	if _, err := FreezeDraft(ctx, s.DB, page, "converted from a board", false); err != nil {
		_ = s.DB.DeleteSharePage(ctx, page.ID)
		return store.SharePage{}, err
	}
	return s.DB.SharePageByID(ctx, page.ID)
}

// templateName is the name in a template's own manifest.
func templateName(tpl string) (string, error) {
	fsys, err := pages.TemplateFS(tpl)
	if err != nil {
		return "", err
	}
	raw, err := fs.ReadFile(fsys, pages.ManifestFile)
	if err != nil {
		return "", err
	}
	m, err := pages.ParseManifest(raw)
	if err != nil {
		return "", err
	}
	return m.Name, nil
}

// templateForBoard picks the built-in template closest to a stored board.
//
// Read from the raw column and never decoded into anything that renders: the
// board vocabulary is gone from this build, and all this needs is which kinds
// of thing a board mostly showed. What got built, then what it cost, then a
// phone's glance, and a session wall for everything else -- which is what the
// default board was.
func templateForBoard(raw string) string {
	var b struct {
		Preset  string `json:"preset"`
		Widgets []struct {
			Kind string `json:"kind"`
		} `json:"widgets"`
	}
	_ = json.Unmarshal([]byte(raw), &b)
	repo, spend := 0, 0
	for _, w := range b.Widgets {
		switch w.Kind {
		case "output", "codechurn", "spentmade", "repoprojects", "prs":
			repo++
		case "tokenburn", "odometer", "sparkline":
			spend++
		default:
			if strings.HasPrefix(w.Kind, "spend") {
				spend++
			}
		}
	}
	switch {
	case repo > 0 && repo >= spend:
		return "built"
	case spend > 0:
		return "spend"
	case b.Preset == "phone":
		return "glance"
	default:
		return "wall"
	}
}
