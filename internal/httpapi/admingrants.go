package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A page's admin page: docs/page-backend.md §3.
//
// Three routes, three credentials, and the shape is the point:
//
//	/pages/{id}/admin/        the panel session  -> mints a grant, 303s away
//	/page-admin/{grant}/...   the grant          -> the admin page's files, sandboxed
//	/api/page-admin/{grant}/v1/...  the grant    -> one page's admin API
//
// The first is the only one that reads the cookie, and it reads it to decide
// whether to mint. The admin page itself never has the cookie: it is the
// owner's HTML, served with an opaque origin like a share page, and the grant
// in its path is the only credential it holds. Run on the panel's origin with
// the cookie, a mistake in it -- or a malicious import -- would be the owner.

// adminGrantTTL is how long a grant lives; it also dies with its session.
const adminGrantTTL = 8 * time.Hour

type adminContextKey struct{}

type adminContext struct {
	grant store.PageAdminGrant
	hash  []byte
}

func adminFrom(r *http.Request) (adminContext, bool) {
	a, ok := r.Context().Value(adminContextKey{}).(adminContext)
	return a, ok
}

func (s *Server) registerAdminPageRoutes(r chi.Router) {
	r.Get("/pages/{pageID}/admin", s.handleOpenAdminPage)
	r.Get("/pages/{pageID}/admin/", s.handleOpenAdminPage)
	r.Get("/page-admin/{grant}", s.handleAdminPageFile)
	r.Get("/page-admin/{grant}/*", s.handleAdminPageFile)
}

// registerAdminAPIRoutes is the whole admin API. Mounted under /api, outside
// the session group, below its own middleware -- the same placement as the
// share routes, for the same reason. TestAnAdminGrantReachesOnlyTheseRoutes
// is the list.
func (s *Server) registerAdminAPIRoutes(r chi.Router) {
	r.Route("/page-admin/{grant}/v1", func(r chi.Router) {
		r.Use(s.requireAdminGrant)
		r.Get("/snapshot", s.handleAdminSnapshot)
		r.Get("/data", s.handleAdminData)
		r.Put("/data/{key}", s.handleAdminDataOp("set"))
		r.Post("/data/{key}/increment", s.handleAdminDataOp("increment"))
		r.Post("/data/{key}/append", s.handleAdminDataOp("append"))
		r.Delete("/data/{key}", s.handleAdminDataOp("reset"))
		r.Post("/actions/{name}", s.handleAdminAction)
		r.Get("/sources", s.handleAdminSources)
		r.Get("/links", s.handleAdminLinks)
	})
}

// handleOpenAdminPage mints a grant for a signed-in session and sends the
// browser to the admin page.
//
// The session cookie only, not an API token: a grant is bound to the session
// that minted it so that signing out ends it, and a bearer token has no
// session to end. A request with no session goes to the sign-in page.
func (s *Server) handleOpenAdminPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)
	if s.Auth != nil && !auth.Allowed(ip, s.Auth.Allow) {
		s.auditFromOutside(ctx, "blocked", "", ip, "address not in the allowlist")
		writeErr(w, http.StatusForbidden, "not allowed from this address")
		return
	}
	token := auth.TokenFromRequest(r)
	user, ok, err := s.currentUserBySessionOnly(r)
	if err != nil {
		s.noteStale(err)
		writeErr(w, http.StatusServiceUnavailable, "the panel cannot reach its own database")
		return
	}
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	draft := r.URL.Query().Get("draft") == "1"
	m, err := s.pageManifestFor(ctx, page, map[bool]string{true: store.PageDataDraft, false: store.PageDataLive}[draft])
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if m.Admin == nil {
		writeErr(w, http.StatusNotFound, "this page has no admin page; its data is edited from settings")
		return
	}
	grant, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.DB.SweepPageAdminGrants(ctx)
	if err := s.DB.CreatePageAdminGrant(ctx, auth.HashToken(grant), page.ID, user.ID,
		auth.HashToken(token), draft, adminGrantTTL); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/page-admin/"+grant+"/"+m.Admin.Entry, http.StatusSeeOther)
}

// currentUserBySessionOnly is currentUser with a cookie and never a bearer
// token.
func (s *Server) currentUserBySessionOnly(r *http.Request) (store.User, bool, error) {
	if bearerToken(r) != "" || auth.TokenFromRequest(r) == "" {
		return store.User{}, false, nil
	}
	return s.currentUser(r)
}

// requireAdminGrant resolves the grant in the path. Also answers the CORS
// preflight an admin page's writes need: its origin is opaque, so every
// request it makes is cross-origin, and the grant in the path -- never a
// cookie -- is what authorises it.
func (s *Server) requireAdminGrant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Cache-Control", "no-store")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		ctx := r.Context()
		ip := s.clientIP(r)
		if s.Auth != nil && !auth.Allowed(ip, s.Auth.Allow) {
			s.auditFromOutside(ctx, "blocked", "", ip, "address not in the allowlist")
			writeErr(w, http.StatusForbidden, "not allowed from this address")
			return
		}
		hash := auth.HashToken(chi.URLParam(r, "grant"))
		grant, err := s.DB.PageAdminGrantByToken(ctx, hash)
		if errors.Is(err, store.ErrNotFound) {
			s.auditFromOutside(ctx, "page.admin_rejected", "", ip, "unknown, expired or signed-out admin grant")
			writeErr(w, http.StatusUnauthorized, "this admin page has expired; open it again from the panel")
			return
		}
		if err != nil {
			s.noteStale(err)
			writeErr(w, http.StatusServiceUnavailable, "the panel cannot reach its own database")
			return
		}
		s.markWatched(grant.PageID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, adminContextKey{}, adminContext{grant: grant, hash: hash})))
	})
}

func adminPageCSP(origin, grant string, scriptHosts []string) string {
	self, api := "'none'", "'none'"
	if origin != "" {
		self = origin + "/page-admin/" + grant + "/"
		api = origin + "/api/page-admin/" + grant + "/v1/"
	}
	scripts := self
	for _, h := range scriptHosts {
		scripts += " https://" + h + "/"
	}
	return "default-src 'none'; " +
		"script-src 'unsafe-inline' " + scripts + "; " +
		"style-src 'unsafe-inline' " + self + "; " +
		"img-src " + self + " data: blob:; " +
		"font-src " + self + " data:; " +
		"connect-src " + api + " " + self + "; " +
		"worker-src 'none'; manifest-src 'none'; frame-src 'none'; " +
		"form-action 'none'; base-uri 'none'; " +
		"frame-ancestors 'self'; " +
		// allow-forms so a form's submit event fires for the page's own script
		// to handle; form-action 'none' above means it submits nowhere.
		"sandbox allow-scripts allow-forms"
}

// handleAdminPageFile serves one file of the page to its admin page.
func (s *Server) handleAdminPageFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)
	if s.Auth != nil && !auth.Allowed(ip, s.Auth.Allow) {
		writeErr(w, http.StatusForbidden, "not allowed from this address")
		return
	}
	grantToken := chi.URLParam(r, "grant")
	grant, err := s.DB.PageAdminGrantByToken(ctx, auth.HashToken(grantToken))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.noteStale(err)
		}
		writeGonePage(w, err != nil && !errors.Is(err, store.ErrNotFound))
		return
	}
	page, err := s.DB.SharePageByID(ctx, grant.PageID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	ns := store.PageDataLive
	if grant.Draft {
		ns = store.PageDataDraft
	}
	m, err := s.pageManifestFor(ctx, page, ns)
	if err != nil || m.Admin == nil {
		http.NotFound(w, r)
		return
	}
	rel := chi.URLParam(r, "*")
	if rel == "" {
		http.Redirect(w, r, "/page-admin/"+grantToken+"/"+m.Admin.Entry, http.StatusSeeOther)
		return
	}

	h := w.Header()
	h.Set("Content-Security-Policy", adminPageCSP(s.requestOrigin(r), grantToken, m.ScriptHosts))
	h.Set("Permissions-Policy", sharePagePermissions)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Access-Control-Allow-Origin", "*")

	// Server code is never served, to anybody; the owner reads it in the
	// directory.
	if m.Server != nil && rel == m.Server.Entry {
		http.NotFound(w, r)
		return
	}
	if rel == pages.SDKFile {
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(pages.SDK)
		return
	}
	var ct string
	var data []byte
	if grant.Draft {
		f, ferr := pages.ReadFile(page.SourceDir, rel)
		if ferr != nil {
			if errors.Is(ferr, fs.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, ferr.Error(), http.StatusUnprocessableEntity)
			return
		}
		ct, data = f.ContentType, f.Data
	} else {
		if !pages.ValidPath(rel) {
			http.NotFound(w, r)
			return
		}
		ct, data, err = s.DB.SharePageFileData(ctx, page.ID, page.PublishedVersion, rel)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// adminTarget is the page, namespace and manifest a grant is for.
func (s *Server) adminTarget(w http.ResponseWriter, r *http.Request) (adminContext, store.SharePage, string, pages.Manifest, bool) {
	a, ok := adminFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "this admin page has expired")
		return adminContext{}, store.SharePage{}, "", pages.Manifest{}, false
	}
	page, err := s.DB.SharePageByID(r.Context(), a.grant.PageID)
	if err != nil {
		s.writeStoreErr(w, err)
		return adminContext{}, store.SharePage{}, "", pages.Manifest{}, false
	}
	ns := store.PageDataLive
	if a.grant.Draft {
		ns = store.PageDataDraft
	}
	m, err := s.pageManifestFor(r.Context(), page, ns)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return adminContext{}, store.SharePage{}, "", pages.Manifest{}, false
	}
	return a, page, ns, m, true
}

// adminUsername is the user a grant was minted for, for the audit trail.
func (s *Server) adminUsername(ctx context.Context, a adminContext) string {
	u, err := s.DB.UserByID(ctx, a.grant.UserID)
	if err != nil {
		return a.grant.UserID
	}
	return u.Username
}

func (s *Server) handleAdminSnapshot(w http.ResponseWriter, r *http.Request) {
	a, page, _, _, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	purpose := ""
	if a.grant.Draft {
		purpose = store.SharePurposePreview
	}
	// A link that exists nowhere but here: the owner's view of the page, with
	// names, over the whole panel. Pseudonyms are derived from the grant, so
	// they join to nothing a screen shows.
	link := store.ShareLink{ID: "admin:" + page.ID + ":" + strconv.FormatBool(a.grant.Draft), PageID: page.ID,
		Name: page.Name, Detail: string(store.ShareNames), Purpose: purpose}
	out, status, msg := s.buildShareSnapshot(r.Context(), shareContext{link: link, secret: a.hash, admin: true})
	if status != http.StatusOK {
		writeErr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminData(w http.ResponseWriter, r *http.Request) {
	_, page, ns, m, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	body, err := s.pageDataBody(r.Context(), page, ns, m)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleAdminDataOp(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, page, ns, m, ok := s.adminTarget(w, r)
		if !ok {
			return
		}
		op := pageDataOp{Kind: kind, Key: chi.URLParam(r, "key")}
		if kind != "reset" {
			var req struct {
				Value any            `json:"value"`
				By    *float64       `json:"by"`
				Item  map[string]any `json:"item"`
			}
			if !decode(w, r, &req) {
				return
			}
			op.Value, op.Item = req.Value, req.Item
			op.By = 1
			if req.By != nil {
				op.By = *req.By
			}
			if kind == "append" && req.Item == nil {
				writeErr(w, http.StatusBadRequest, `append takes {"item": {...}}`)
				return
			}
		}
		name := s.adminUsername(r.Context(), a)
		s.writePageDataOp(w, r, page, ns, m, op, name, name)
	}
}

func (s *Server) handleAdminSources(w http.ResponseWriter, r *http.Request) {
	_, page, _, m, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": s.sourceResults(page.ID, m)})
}

// adminLink is a link as an admin page sees it: no token, no address.
type adminLink struct {
	Name        string `json:"name"`
	Remark      string `json:"remark"`
	Interactive bool   `json:"interactive"`
	Viewers     int    `json:"viewers"`
}

func (s *Server) handleAdminLinks(w http.ResponseWriter, r *http.Request) {
	_, page, _, _, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	links, err := s.DB.ShareLinksForPage(r.Context(), page.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	now := time.Now()
	out := make([]adminLink, 0, len(links))
	for _, l := range links {
		n, _, _ := s.viewers.count(l.ID, now)
		out = append(out, adminLink{Name: l.Name, Remark: l.Remark, Interactive: l.Interactive, Viewers: n})
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}
