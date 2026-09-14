package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A share page is HTML the owner wrote, served on a share link in place of the
// board, drawing the same redacted snapshot through the SDK. docs/share-pages.md
// is the design; this file is the part a share token can reach.
//
// It adds two things below the token, and both are GETs. Red line 8 counts them:
// TestAShareTokenReachesOnlyTheseRoutes walks the router and fails on anything
// under either prefix that is not in its list.
//
//	GET /share/{token}/*             the page's files, and the SDK
//	GET /api/share/{token}/v1/snapshot   the data
//
// # The boundary
//
// The page is somebody's HTML on the panel's own origin, and the panel's origin
// holds a session cookie that is a writable terminal. What keeps them apart is
// the one preview.go already relies on and measured: `sandbox` in the
// Content-Security-Policy *response header*, without allow-same-origin. The
// document lands in an opaque origin, so it cannot read a cookie, cannot reach
// storage, and every request it makes to the panel is cross-origin and carries
// no credentials. The header rather than an iframe attribute, because a page is
// opened top-level on a wall and there is no frame to put an attribute on.
//
// # What a page's policy names
//
// Its own files, the v1 snapshot for its own token, and -- if its manifest
// asks -- scripts from one of pages.ScriptHosts. Written out as absolute URLs
// rather than 'self', because an opaque origin matches no source expression and
// 'self' would refuse the very requests the page is made of. connect-src never
// names another host, whatever the manifest says.
//
// What it does not buy, in the words preview.go uses for the same header: CSP
// is not an exfiltration boundary. A page can still be clicked away from, and a
// determined one has roads CSP does not cover. The data a page could send is
// the snapshot its link already discloses to whoever holds the URL; the policy
// keeps an honest page from phoning home, and the redaction is what bounds a
// dishonest one.

// sharePageCSP is the policy every byte of a page is served under.
//
// framed is true for a preview link, which the settings page draws in an
// iframe; a link somebody was handed is never framed by anything, so it keeps
// the panel's own frame-ancestors 'none'.
func sharePageCSP(origin, token string, scriptHosts []string, framed bool) string {
	self, api := "'none'", "'none'"
	if origin != "" {
		self = origin + "/share/" + token + "/"
		api = origin + "/api/share/" + token + "/v1/"
	}
	scripts := self
	for _, h := range scriptHosts {
		scripts += " https://" + h + "/"
	}
	ancestors := "'none'"
	if framed {
		ancestors = "'self'"
	}
	return "default-src 'none'; " +
		"script-src 'unsafe-inline' " + scripts + "; " +
		"style-src 'unsafe-inline' " + self + "; " +
		"img-src " + self + " data: blob:; " +
		"font-src " + self + " data:; " +
		"media-src " + self + " data: blob:; " +
		// The page's own directory is here so the SDK can read a fixture out
		// of fixtures/ in the Preview pane. It is the same set of files the
		// page can already load as scripts.
		"connect-src " + api + " " + self + "; " +
		"worker-src 'none'; manifest-src 'none'; frame-src 'none'; " +
		"form-action 'none'; base-uri 'none'; " +
		"frame-ancestors " + ancestors + "; " +
		// allow-scripts and nothing else. See previewSandbox for the tokens that
		// are absent and why; allow-same-origin above all.
		"sandbox allow-scripts"
}

// sharePagePermissions turns off the device APIs a page has no use for. A wall
// asking for the microphone of the television it is on is not a feature.
const sharePagePermissions = "camera=(), microphone=(), geolocation=(), usb=(), " +
	"payment=(), display-capture=(), serial=(), hid=(), bluetooth=()"

// registerSharePageRoutes mounts the page files, outside /api.
//
// Outside /api because this is what a browser opens, and before the SPA's
// catch-all because `/share/<token>` used to be answered by the catch-all and a
// board link still is: spa is that catch-all, and a link that draws a board,
// or a token that resolves to nothing, is handed to it exactly as before.
func (s *Server) registerSharePageRoutes(r chi.Router, spa http.Handler) {
	h := func(w http.ResponseWriter, r *http.Request) { s.handleSharePage(w, r, spa) }
	r.Get("/share/{token}", h)
	r.Get("/share/{token}/*", h)
}

// handleSharePage serves one file of the page a link draws.
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request, spa http.Handler) {
	ctx := r.Context()
	ip := s.clientIP(r)
	if s.Auth != nil && !auth.Allowed(ip, s.Auth.Allow) {
		// The same rule requireShareToken applies, for the same reason: making
		// a link must not be a way past --allow-from.
		s.auditFromOutside(ctx, "blocked", "", ip, "address not in the allowlist")
		writeErr(w, http.StatusForbidden, "not allowed from this address")
		return
	}
	token := chi.URLParam(r, "token")
	link, err := s.DB.ShareLinkByToken(ctx, auth.HashToken(token))
	if err != nil || link.PageID == "" {
		// Unknown, expired, a database hiccup, or a board: the SPA, which is
		// what answered this path before pages existed. It asks the dashboard
		// endpoint next, and that is where a bad token is refused and audited
		// -- once, rather than once here and again there.
		spa.ServeHTTP(w, r)
		return
	}

	rel := chi.URLParam(r, "*")
	if !strings.HasSuffix(r.URL.Path, "/") && rel == "" {
		// Relative URLs in the page resolve against the directory, and
		// `/share/<token>` has no directory: the stylesheet would be asked for
		// at /share/style.css. One redirect, to the same address with a slash.
		http.Redirect(w, r, r.URL.Path+"/", http.StatusPermanentRedirect)
		return
	}
	if rel == "" {
		rel = pages.IndexFile
	}

	page, err := s.DB.SharePageByID(ctx, link.PageID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	preview := link.Purpose == store.SharePurposePreview
	var manifest pages.Manifest
	if preview {
		manifest, _ = draftManifest(page.SourceDir)
	} else {
		v, verr := s.DB.SharePageVersionByNumber(ctx, page.ID,
			link.ResolvePageVersion(page.PublishedVersion, time.Now().Unix()))
		if verr != nil {
			s.writeStoreErr(w, verr)
			return
		}
		manifest = pages.DecodeStored(v.Manifest)
	}

	h := w.Header()
	// Every response, not only the HTML: a stylesheet or a JSON file served
	// without the sandbox is a document somebody can navigate to directly, and
	// it would be the one running on the panel's origin.
	h.Set("Content-Security-Policy",
		sharePageCSP(s.requestOrigin(r), token, manifest.ScriptHosts, preview))
	h.Set("Permissions-Policy", sharePagePermissions)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	// A page reading its own file -- a fixture, a JSON of its own -- is a
	// cross-origin fetch, because the sandbox gave the document an opaque
	// origin. Measured: without this, fetch('fixtures/busy.json') from the
	// page it belongs to fails. `*` and never credentials, for the reason the
	// snapshot gives: the token in the path is the whole capability.
	h.Set("Access-Control-Allow-Origin", "*")

	if rel == pages.SDKFile {
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(pages.SDK)
		return
	}

	var ct string
	var data []byte
	if preview {
		f, ferr := pages.ReadFile(page.SourceDir, rel)
		if ferr != nil {
			if errors.Is(ferr, fs.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			// A draft file that is too large or is not what its name says:
			// said, because the person looking at the Preview pane is the one
			// who can fix it, and a bare 404 would send them looking for a
			// typo.
			http.Error(w, ferr.Error(), http.StatusUnprocessableEntity)
			return
		}
		ct, data = f.ContentType, f.Data
	} else {
		if !pages.ValidPath(rel) {
			http.NotFound(w, r)
			return
		}
		version := link.ResolvePageVersion(page.PublishedVersion, time.Now().Unix())
		ct, data, err = s.DB.SharePageFileData(ctx, page.ID, version, rel)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if strings.HasPrefix(rel, "fixtures/") && strings.HasSuffix(rel, ".json") {
		// Shaped to what this page would really receive, with this link's
		// parameters. See shapeFixture.
		data = shapeFixture(data, manifest, link.Params)
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// draftManifest reads a draft's vibepanel.json. A draft that has none, or one
// that does not validate, has no script hosts and asks for no sections; the
// snapshot route says why.
func draftManifest(dir string) (pages.Manifest, error) {
	if dir == "" {
		return pages.Manifest{}, errors.New("this page has no draft directory")
	}
	b, err := readDraftManifest(dir)
	if err != nil {
		return pages.Manifest{}, err
	}
	return pages.ParseManifest(b)
}

// ─── the snapshot ──────────────────────────────────────────────────────────

// shareSnapshot is the v1 wire contract, and the SDK's Snapshot type.
//
// Restated field by field from shareDashboard rather than embedding it, for
// the reason every struct in share.go is restated: a field added to the
// dashboard -- the board's own `board` and `locked`, for instance -- must not
// become part of a published API without somebody writing the line here.
//
// v1 is additive only. A field may be added; none may be renamed, retyped or
// removed while a published page asks for v1. vibepanel.d.ts declares every
// field here and TestTheSDKTypesMatchTheSnapshot holds the two together.
type shareSnapshot struct {
	V int `json:"v"`
	// Page is which page and version this is, or null for a link that still
	// draws a board. The SDK reloads when it changes.
	Page *shareSnapshotPage `json:"page"`
	// Sections is what the page's manifest asked for, in pages.Sections order.
	Sections []string `json:"sections"`
	// Params is every declared parameter with this link's value, or its
	// default. An empty object on a board link.
	Params map[string]any `json:"params"`

	At            int64  `json:"at"`
	Name          string `json:"name"`
	Remark        string `json:"remark"`
	Detail        string `json:"detail"`
	ExpiresAt     int64  `json:"expiresAt"`
	UsageReadable bool   `json:"usageReadable"`
	Stale         bool   `json:"stale"`

	Machine  shareMachine   `json:"machine"`
	Counts   shareCounts    `json:"counts"`
	Projects []shareProject `json:"projects"`
	Sessions []shareSession `json:"sessions"`
	Spend    *shareSpend    `json:"spend"`
	Todos    *shareTodos    `json:"todos"`
	Trend    *shareTrend    `json:"trend"`
	Flow     *shareFlow     `json:"flow"`
	Feed     *shareFeed     `json:"feed"`
	Repo     *shareRepo     `json:"repo"`

	Scope          string `json:"scope"`
	ScopeName      string `json:"scopeName"`
	ScopeRepoOwner string `json:"scopeRepoOwner"`
	ScopeRepoName  string `json:"scopeRepoName"`
}

// shareSnapshotPage identifies what a link is drawing.
type shareSnapshotPage struct {
	// ID is this link's pseudonym for the page, like every other id here.
	ID      string `json:"id"`
	Version int    `json:"version"`
	// Draft is true on a preview link, which draws the directory an agent is
	// editing. Version is 0 then, and the Preview pane reloads the frame
	// itself when files change.
	Draft bool `json:"draft"`
}

// snapshotMemoTTL is how long one link's snapshot is reused.
//
// A wall polls every two seconds, twenty screens on one link poll twenty times
// in those two seconds, and a page written with setInterval(…, 50) polls forty
// times a second. All of them cost one build a second. The viewer book is
// still fed on every request, in requireShareToken, before this is consulted.
const snapshotMemoTTL = time.Second

// snapshotMemoCap bounds the memo. Past it the expired entries go first and,
// if nothing has expired, the whole map: a memo is a cache, and losing it costs
// one build per link.
const snapshotMemoCap = 256

type snapshotMemo struct {
	mu      sync.Mutex
	entries map[string]snapshotMemoEntry
	// builds counts the misses. The memo's whole claim is a number of builds,
	// and a test that compares timestamps measured in seconds cannot tell one
	// build from twenty inside the same second -- which is how it passed with
	// the memo switched off.
	builds atomic.Int64
}

type snapshotMemoEntry struct {
	at   time.Time
	dash shareDashboard
}

func (m *snapshotMemo) get(key string, now time.Time) (shareDashboard, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || now.Sub(e.at) >= snapshotMemoTTL {
		return shareDashboard{}, false
	}
	return e.dash, true
}

func (m *snapshotMemo) put(key string, now time.Time, d shareDashboard) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]snapshotMemoEntry{}
	}
	if len(m.entries) >= snapshotMemoCap {
		for k, e := range m.entries {
			if now.Sub(e.at) >= snapshotMemoTTL {
				delete(m.entries, k)
			}
		}
		if len(m.entries) >= snapshotMemoCap {
			m.entries = map[string]snapshotMemoEntry{}
		}
	}
	m.entries[key] = snapshotMemoEntry{at: now, dash: d}
}

// shareReadableAnywhere lets any origin read a share API response, and never
// with credentials.
//
// A sandboxed page has the origin "null", so its own fetch is cross-origin.
// The token in the path is the whole capability and nothing under this prefix
// reads a cookie, so `*` grants nothing a curl holding the URL does not
// already have.
//
// Middleware in front of requireShareToken rather than a header in the
// handler, and that ordering is the bug it fixes: the refusals -- 401 for a
// revoked link, 403 for the allowlist, 503 for the database -- are written by
// the middleware before any handler runs. Without the header on those, the
// browser hides the status from the page, the SDK sees a network error, and a
// wall whose link was revoked says "reconnecting" forever instead of saying
// the link is gone. Found by pages-check, not by reasoning about it.
func shareReadableAnywhere(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		next.ServeHTTP(w, r)
	})
}

// handleShareSnapshot is the v1 data a page draws.
func (s *Server) handleShareSnapshot(w http.ResponseWriter, r *http.Request) {
	sc, ok := shareFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "this link is not valid")
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	out, status, msg := s.buildShareSnapshot(r.Context(), sc)
	if status != http.StatusOK {
		writeErr(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// buildShareSnapshot resolves what a link draws and builds its snapshot.
//
// Returns a status and a message rather than an error, because the two
// failures a page can do something about -- a draft whose manifest does not
// validate, a page that is gone -- need different answers from a database
// that is down.
func (s *Server) buildShareSnapshot(ctx context.Context, sc shareContext) (shareSnapshot, int, string) {
	link := sc.link
	now := time.Now()
	out := shareSnapshot{V: pages.SDKVersion, Params: map[string]any{}}

	board := link.Board
	memoKey := link.ID + "|board"
	if link.PageID != "" {
		page, err := s.DB.SharePageByID(ctx, link.PageID)
		if errors.Is(err, store.ErrNotFound) {
			return out, http.StatusGone, "this page no longer exists"
		}
		if err != nil {
			s.noteStale(err)
			return out, http.StatusServiceUnavailable, "the panel cannot reach its own database"
		}
		var manifest pages.Manifest
		ref := &shareSnapshotPage{ID: shareID(sc.secret, page.ID)}
		if link.Purpose == store.SharePurposePreview {
			raw, rerr := readDraftManifest(page.SourceDir)
			if rerr != nil {
				return out, http.StatusUnprocessableEntity, rerr.Error()
			}
			m, perr := pages.ParseManifest(raw)
			if perr != nil {
				return out, http.StatusUnprocessableEntity, perr.Error()
			}
			manifest = m
			ref.Draft = true
			sum := sha256.Sum256(raw)
			memoKey = link.ID + "|draft|" + hex.EncodeToString(sum[:8])
		} else {
			version := link.ResolvePageVersion(page.PublishedVersion, now.Unix())
			v, verr := s.DB.SharePageVersionByNumber(ctx, page.ID, version)
			if errors.Is(verr, store.ErrNotFound) {
				return out, http.StatusGone, "this page has no published version"
			}
			if verr != nil {
				s.noteStale(verr)
				return out, http.StatusServiceUnavailable, "the panel cannot reach its own database"
			}
			manifest = pages.DecodeStored(v.Manifest)
			ref.Version = version
			memoKey = link.ID + "|v" + strconv.Itoa(version)
		}
		out.Page = ref
		out.Sections = manifest.Needs()
		out.Params = pages.ResolveParams(manifest.Params, link.Params)
		board = manifest.Board()
	} else {
		out.Sections = sectionsOfBoard(board)
	}

	dash, hit := s.snapshots.get(memoKey, now)
	if !hit {
		s.snapshots.builds.Add(1)
		built := link
		built.Board = board
		var err error
		dash, err = s.buildShareDashboard(ctx, built, sc.secret)
		if err != nil {
			s.noteStale(err)
			return out, http.StatusServiceUnavailable, "the panel cannot reach its own database"
		}
		s.snapshots.put(memoKey, now, dash)
	}

	out.At, out.Name, out.Remark, out.Detail = dash.At, dash.Name, dash.Remark, dash.Detail
	out.ExpiresAt, out.UsageReadable, out.Stale = dash.ExpiresAt, dash.UsageReadable, dash.Stale
	out.Machine, out.Counts, out.Projects, out.Sessions = dash.Machine, dash.Counts, dash.Projects, dash.Sessions
	out.Spend, out.Todos, out.Trend, out.Flow, out.Feed, out.Repo =
		dash.Spend, dash.Todos, dash.Trend, dash.Flow, dash.Feed, dash.Repo
	out.Scope, out.ScopeName = dash.Scope, dash.ScopeName
	out.ScopeRepoOwner, out.ScopeRepoName = dash.ScopeRepoOwner, dash.ScopeRepoName
	return out, http.StatusOK, ""
}

// sectionsOfBoard names what a board link's snapshot carries, in the
// manifest's vocabulary, so a page hosted elsewhere and pointed at a board link
// can tell what it was given.
func sectionsOfBoard(b store.Board) []string {
	needs := b.Needs()
	have := map[string]bool{
		pages.SectionSessions: needs[store.NeedSessions],
		pages.SectionTodos:    needs[store.NeedTodos],
		pages.SectionSpend:    needs[store.NeedSpend],
		pages.SectionTrend:    needs[store.NeedTrend],
		pages.SectionFlow:     needs[store.NeedFlow],
		pages.SectionFeed:     needs[store.NeedFeed],
		pages.SectionRepo: needs[store.NeedRepo] || needs[store.NeedRepoDays] ||
			needs[store.NeedRepoPRs],
	}
	out := []string{}
	for _, name := range pages.Sections() {
		if have[name] {
			out = append(out, name)
		}
	}
	return out
}

// readDraftManifest reads vibepanel.json out of a draft directory, confined
// the way every other read of it is.
func readDraftManifest(dir string) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("this page has no draft directory")
	}
	return pages.ReadManifestFile(dir)
}
