package httpapi

import (
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
)

/*
A directory served as a page.

「在文件管理里可以将任何一个目录作为 Python 的 simple server ... 打开这个链接可
以看到这个目录里的所有文件，可以正常渲染 HTML ... 这样方便我去预览，点一个链接
就进去了」.

The point is that the assets resolve. A single-file download cannot render a
page an agent wrote, because the page asks for its stylesheet and its script by
relative path; a directory served under one prefix can.

Three things had to be got right, and the person asking named all three.

── The cookie ──────────────────────────────────────────────────────────────

Serving somebody's HTML from the panel's own origin means that HTML runs *as*
the panel. It could `fetch('/api/sessions', ...)` with the signed-in session
cookie attached, or read `document.cookie`, and the capability at the end of
that is a terminal. A file you previewed would own the account.

Only one port is open, so a second origin is not available: no separate port, no
separate hostname, no separate scheme.

`Content-Security-Policy: sandbox` is what makes it safe on one origin. It
applies the iframe sandbox to a *top-level* document, so the page is loaded into
an opaque origin: it cannot read `document.cookie`, cannot reach `localStorage`,
and every request it makes is cross-origin to the panel and lands without
credentials. `allow-scripts` is kept because a preview of a page that cannot run
its own script is not a preview -- and crucially, `allow-same-origin` is *not*,
which is the token that would give all of it back.

The browser still sends the session cookie on the way *in*, which is what
authenticates the request. The document simply cannot read or use it. Those are
different things and this depends on the difference.

── The path ────────────────────────────────────────────────────────────────

`browse.Resolve` is the containment primitive the file panel already uses: it
resolves symlinks on both the root and the target and refuses anything that
lands outside. A directory of build output is exactly where a symlink to
somewhere else is plausible, so resolving rather than string-prefixing is the
whole of it.

── What the answer discloses ───────────────────────────────────────────────

The index lists names relative to the root and nothing else. Not the absolute
path, not the root, not the parent -- a preview link handed to somebody is a
capability over one directory and should not also tell them where it lives on
the machine. A 404 is returned for "outside the root" as well as for "not
there", so probing cannot tell one from the other.

── Why the link, and not the session, is the authority ─────────────────────

The first version required a signed-in session as well as the token, which is
what was asked for --「只在应用登录态下才可以」-- and it does not work. Measured,
in a browser, against a real link:

    rendered        PREVIEW_INDEX_OK
    css applied     rgb(0, 0, 0)          the stylesheet did not load
    app.js          (script did not run)
    blocked         style.css :: net::ERR_BLOCKED_BY_ORB

The sandbox is what causes it. An opaque origin makes the document's *own*
asset requests cross-origin, `SameSite` keeps the session cookie off a
cross-origin request, `RequireAuth` answers 401 with a JSON body, and the
browser reports that as ORB rather than as the 401 it is. The two requirements
are in direct conflict: the sandbox is what stops the page stealing the session,
and it is also what stops the page proving it has one.

So the token is the authority, exactly as a share link's is: 32 random bytes,
stored as a SHA-256, revocable, and expiring. Creating one still requires being
signed in, which is the half of 「登录态」 that can be kept. Using one requires
holding it.

What that costs, said plainly: a preview URL that leaks is readable by whoever
holds it until it is revoked or expires. It is scoped to one directory, it
cannot reach the API or the session, and `Referrer-Policy: no-referrer` and
`X-Robots-Tag` are there so it does not leak on its own.

Red line 8 is untouched: this is its own route with its own table, and
`currentUser` consults neither. A share token presented here resolves nothing.
*/

// previewMaxAge is how long a browser may reuse a preview response.
//
// Zero. The directory being previewed is one an agent is still writing to, and
// a cached stylesheet is the difference between "my change did not work" and
// "my change is not on screen yet". Correctness over a round trip, on a link
// that exists to watch something change.
const dirPreviewMaxAge = 0

// dirPreviewCSP is the policy served with every byte of a directory preview.
//
// It diverges from previewCSP, which serves *one* file, in exactly one way and
// for exactly one reason: this has to let the page load its own assets. A
// preview of a built site whose stylesheet is blocked is not a preview.
//
// `'self'` cannot express that. `sandbox` puts the document in an opaque
// origin, and an opaque origin matches no source expression -- so `'self'`
// would forbid the very requests this exists to allow. The panel's own origin
// is therefore named explicitly, which is the same set of requests written a
// different way, and still forbids every other host: a preview containing
// `<img src="https://someone/?leak">` makes no request, which is the promise
// preview_render.go makes with `default-src 'none'` and this keeps in the only
// form available to it.
//
// What this costs relative to the single-file door, written down because a
// list of protections with no residue is one nobody checked:
//
//   - The page can request other files *inside the same preview*. That is the
//     feature. It cannot reach the panel's API, because those paths are not
//     under the token and the opaque origin sends no credentials anyway.
//   - `allow-forms` is granted, so a form inside a preview can submit -- to the
//     preview's own origin only, by `form-action`. A built site with a search
//     box behaves; a page pretending to be a sign-in cannot post anywhere.
//   - `allow-same-origin` is never emitted, in any combination. With
//     `allow-scripts` beside it the sandbox would be theatre, and the whole of
//     the cookie defence is that those two never appear together.
//
// dirPreviewCSP is the policy a previewed directory is served under.
//
// `external` widens the fetch directives to https:, and is off unless the
// person who made the link asked for it. What it is for: a page an agent has
// just written almost always pulls three.js off a CDN and a font off Google,
// and under the default policy it renders blank with a console full of
// refusals -- 「预览的时候好像会报错好多」.
//
// What it costs, stated rather than implied: a script fetched from anywhere
// can carry anything in its own URL, so a page that may load one is a page
// that may send the directory somewhere. `connect-src` stays 'none' either
// way, which stops the obvious fetch/XHR/WebSocket road and not the clever
// ones -- it is not the thing keeping this safe, and pretending otherwise
// would be worse than the widening.
//
// The sandbox, the opaque origin, `base-uri 'none'` and `frame-ancestors` do
// not move. Those are what stop a preview reaching the *panel*, and no link
// setting touches them.
func dirPreviewCSP(origin string, external bool) string {
	self := origin
	if self == "" {
		// No origin to name means nothing may load rather than everything: a
		// preview that renders without its stylesheet is a worse preview and a
		// safe one.
		self = "'none'"
	}
	out := self
	if external {
		out = self + " https:"
	}
	return "default-src " + out + "; " +
		"style-src " + out + " 'unsafe-inline'; " +
		"script-src " + out + " 'unsafe-inline'; " +
		"img-src " + out + " data: blob:; " +
		"media-src " + out + " data: blob:; " +
		"font-src " + out + " data:; " +
		"connect-src 'none'; " +
		"base-uri 'none'; " +
		"form-action " + self + "; " +
		"frame-ancestors 'self'; " +
		"sandbox allow-scripts allow-forms allow-popups allow-modals"
}

func (s *Server) registerPreviewRoutes(r chi.Router) {
	r.Route("/preview/{token}", func(r chi.Router) {
		// No RequireAuth: see the note above. The sandbox that protects the
		// session is the same thing that prevents the page proving it has one,
		// so the token in the path is the capability.
		r.Get("/", s.handleDirPreview)
		r.Get("/*", s.handleDirPreview)
	})
}

// handlePreview serves one file, or an index of one directory.
func (s *Server) handleDirPreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	link, err := s.DB.PreviewLinkByToken(ctx, auth.HashToken(chi.URLParam(r, "token")))
	if err != nil {
		// Not distinguishable from a path that is not there: a wrong token and
		// an expired one and a missing file all answer the same way.
		http.NotFound(w, r)
		return
	}

	rel := chi.URLParam(r, "*")
	abs, rerr := browse.Resolve(link.Root, rel)
	if rerr != nil {
		http.NotFound(w, r)
		return
	}
	info, serr := os.Stat(abs)
	if serr != nil {
		http.NotFound(w, r)
		return
	}

	if err := s.DB.TouchPreviewLink(ctx, link.ID); err != nil {
		s.Log.Warn("touch preview link", "id", link.ID, "err", err)
	}

	// Every response, not only the HTML ones. A stylesheet or a JSON file
	// served without the sandbox is a document somebody can navigate to
	// directly, and it would be the one that runs with the panel's origin.
	w.Header().Set("Content-Security-Policy", dirPreviewCSP(s.requestOrigin(r), link.AllowExternal))
	// `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer` are
	// not set here: `securityHeaders` sets both on every response the panel
	// makes, and this needs exactly what that already gives. Setting them again
	// would be a second place to get them wrong, and a test asserting the
	// property does not care which layer provides it -- which is how the
	// duplication was noticed, by removing these lines and seeing nothing fail.
	//
	// The CSP above *is* set here, because it is an override: the middleware's
	// policy is `frame-ancestors 'none'`, and a preview is a document the panel
	// frames.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	if info.IsDir() {
		s.dirPreviewIndex(w, r, link, abs, rel)
		return
	}
	s.dirPreviewFile(w, r, abs, info)
}

// previewFile writes one file with a type derived from its name.
func (s *Server) dirPreviewFile(w http.ResponseWriter, r *http.Request, abs string, info os.FileInfo) {
	f, err := os.Open(abs) //nolint:gosec // abs came from browse.Resolve
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close() //nolint:errcheck // read-only

	// From the extension, never sniffed. `http.ServeContent` would sniff an
	// unknown type from the first bytes, and sniffing is how a file the person
	// thought was data becomes a document.
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(abs))); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	if dirPreviewMaxAge > 0 {
		w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", dirPreviewMaxAge))
	}
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, f); err != nil {
		// The client went away mid-file, which is ordinary. Nothing to say and
		// nothing to write: the header is already out.
		return
	}
}

// previewIndex renders a directory listing, or its index.html if there is one.
//
// `index.html` first, because that is what makes a link to a built site work
// the way its author expects --「也可以用 index 渲染」-- and because a directory
// that has one has said what it wants shown.
func (s *Server) dirPreviewIndex(
	w http.ResponseWriter, r *http.Request, link store.PreviewLink, abs, rel string,
) {
	if idx := filepath.Join(abs, "index.html"); dirPreviewFileExists(idx) {
		info, err := os.Stat(idx)
		if err == nil {
			s.dirPreviewFile(w, r, idx, info)
			return
		}
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	// The URL prefix this listing links against, which is the request path with
	// the trailing slash guaranteed. Without it a relative link one level down
	// resolves against the parent and every entry 404s.
	base := r.URL.Path
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}

	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	// The name, not the path. See the note at the top: a listing must not say
	// where on the machine this directory is.
	fmt.Fprintf(&b, "<title>%s</title>", html.EscapeString(link.Name))
	b.WriteString(dirPreviewIndexStyle)
	b.WriteString("</head><body><main>")
	shown := rel
	if shown == "" {
		shown = "/"
	} else {
		shown = "/" + strings.TrimPrefix(shown, "/")
	}
	fmt.Fprintf(&b, "<h1>%s</h1><p class=\"where\">%s</p><ul>",
		html.EscapeString(link.Name), html.EscapeString(shown))

	if rel != "" {
		fmt.Fprintf(&b, "<li><a href=\"%s\">../</a></li>",
			html.EscapeString(path.Dir(strings.TrimSuffix(base, "/"))+"/"))
	}
	for _, e := range entries {
		name := e.Name()
		// Escaped as a path segment: a file called "a b&c.html" has to survive
		// becoming a URL, and `html.EscapeString` alone escapes it for HTML
		// while leaving it wrong as a link.
		href := base + url.PathEscape(name)
		label := name
		if e.IsDir() {
			href += "/"
			label += "/"
		}
		fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a></li>",
			html.EscapeString(href), html.EscapeString(label))
	}
	b.WriteString("</ul></main></body></html>")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return
	}
}

const dirPreviewIndexStyle = `<style>
:root{color-scheme:light dark}
body{margin:0;font:14px/1.6 system-ui,-apple-system,sans-serif;background:#e8eaed;color:#111}
main{max-width:48rem;margin:0 auto;padding:2rem 1.25rem}
h1{font-size:1.25rem;margin:0 0 .25rem}
.where{margin:0 0 1.25rem;color:#666;font-family:ui-monospace,monospace}
ul{list-style:none;margin:0;padding:0}
li{border-bottom:1px solid rgb(0 0 0/.08)}
a{display:block;padding:.5rem .25rem;color:#0645ad;text-decoration:none}
a:hover{background:rgb(0 0 0/.04)}
@media (prefers-color-scheme:dark){
body{background:#0d0d10;color:#e6e6e6}
.where{color:#999}
li{border-color:rgb(255 255 255/.1)}
a{color:#6ea8fe}
a:hover{background:rgb(255 255 255/.06)}
}
</style>`

func dirPreviewFileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// ─── managing the links ───────────────────────────────────────────────────

type createPreviewRequest struct {
	// Root is an absolute directory on this machine, for a caller that knows
	// one -- the CLI, or curl.
	Root string `json:"root"`
	// ProjectID and Path are how the file panel asks, because a listing knows
	// where it is relative to a project and not where the project is. Resolving
	// it here is also tighter: the directory is checked against the project's
	// own root by the same helper that guards every other file route, so the
	// browser cannot name a directory by absolute path at all.
	ProjectID string `json:"projectId"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	// ExpiresIn is seconds from now; 0 for a link that does not expire.
	ExpiresIn int64 `json:"expiresIn"`
	// AllowExternal lets the page load scripts, styles, fonts and images from
	// other origins. Named on the link rather than settable afterwards: it
	// changes what a token already handed out can do, and a capability that
	// grows after it leaves is one nobody can reason about.
	AllowExternal bool `json:"allowExternal"`
}

func (s *Server) handleCreatePreview(w http.ResponseWriter, r *http.Request) {
	var req createPreviewRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}

	// Resolved here, once, and stored resolved. A root recorded as typed would
	// be re-resolved on every request, so a symlink swapped afterwards would
	// change what the link serves without the link changing.
	var root string
	var err error
	if req.ProjectID != "" {
		p, perr := s.DB.GetProject(ctx, req.ProjectID)
		if perr != nil {
			s.writeStoreErr(w, perr)
			return
		}
		root, err = browse.Resolve(p.Path, req.Path)
	} else {
		root, err = browse.Resolve(req.Root, "")
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot serve that directory: "+err.Error())
		return
	}
	info, serr := os.Stat(root)
	if serr != nil || !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "not a directory")
		return
	}

	if req.ExpiresIn < 0 || req.ExpiresIn > int64((365*24*time.Hour)/time.Second) {
		writeErr(w, http.StatusBadRequest, "expiresIn is 0, or at most a year")
		return
	}
	var expires int64
	if req.ExpiresIn > 0 {
		expires = time.Now().Unix() + req.ExpiresIn
	}

	token, terr := auth.NewToken()
	if terr != nil {
		writeErr(w, http.StatusInternalServerError, terr.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(root)
	}
	link, cerr := s.DB.CreatePreviewLink(ctx, id.New(), auth.HashToken(token),
		token[:8], name, root, u.ID, expires, req.AllowExternal)
	if cerr != nil {
		s.writeStoreErr(w, cerr)
		return
	}

	// The only time the token is readable, exactly as a share link works.
	writeJSON(w, http.StatusOK, map[string]any{
		"id": link.ID, "prefix": link.Prefix, "name": link.Name,
		"root": link.Root, "createdAt": link.CreatedAt, "expiresAt": link.ExpiresAt,
		"allowExternal": link.AllowExternal,
		"token":         token,
	})
}

func (s *Server) handleListPreviews(w http.ResponseWriter, r *http.Request) {
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	links, err := s.DB.ListPreviewLinks(r.Context(), u.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"previews": links})
}

func (s *Server) handleDeletePreview(w http.ResponseWriter, r *http.Request) {
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	err := s.DB.DeletePreviewLink(r.Context(), chi.URLParam(r, "id"), u.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
