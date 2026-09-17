package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// A share page is HTML the owner wrote, served on a share link, drawing the
// redacted snapshot through the SDK. docs/share-pages.md
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
// catch-all, which must never answer `/share/<token>`: the panel's own bundle
// on an address a stranger holds is the sign-in page, one click from the door
// this link was made so nobody would need.
func (s *Server) registerSharePageRoutes(r chi.Router) {
	r.Get("/share/{token}", s.handleSharePage)
	r.Get("/share/{token}/*", s.handleSharePage)
}

// goneHTML is what `/share/<token>` answers for a link that no longer draws
// anything. Static, no script, no stylesheet from anywhere, and nothing that
// came from the database: whoever holds a dead address learns that it is dead
// and nothing about the panel it pointed at. Both languages, because the
// person reading it did not choose the panel's.
const goneHTML = `<!doctype html><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow"><title>%s</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;
font:16px/1.5 system-ui,sans-serif;background:#111;color:#ddd;padding:1rem;box-sizing:border-box}
main{max-width:28rem;text-align:center}h1{font-size:1.25rem;margin:0 0 .5rem}p{margin:.25rem 0;color:#999}</style>
<main><h1>%s</h1><p>%s</p><p>%s</p></main>
`

func writeGonePage(w http.ResponseWriter, down bool) {
	title, en, zh := "This link no longer works",
		"It was revoked, it expired, or it never existed. Ask whoever sent it for a new one.",
		"链接已失效：已被吊销、已过期或不存在。请向发给你的人要一个新链接。"
	status := http.StatusNotFound
	if down {
		title, en, zh = "This screen is unavailable",
			"The panel cannot read its own database right now. The link may be fine; try again shortly.",
			"面板暂时读不到自己的数据库，链接本身可能没问题，请稍后再试。"
		status = http.StatusServiceUnavailable
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, goneHTML, title, title, en, zh)
}

// handleSharePage serves one file of the page a link draws.
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
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
		// Unknown, expired, revoked, or a database hiccup. A page of its own,
		// with no script and nothing of the panel's in it: the address is held
		// by somebody who is not signed in, and the one useful thing to tell
		// them is that it no longer works. A database that is down says so,
		// because "ask for a new link" is the wrong advice for that one.
		down := err != nil && !errors.Is(err, store.ErrNotFound)
		if down {
			s.noteStale(err)
		} else {
			s.auditFromOutside(ctx, "share.rejected", "", ip, "unknown or expired share link")
		}
		writeGonePage(w, down)
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

	// The admin page and server code are the page's, and never a screen's:
	// refused the way a file that does not exist is, so a probe cannot tell
	// which pages have them.
	if manifest.Private(rel) {
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
		data = shapeFixture(data, manifest, link.Params, rel == "fixtures/hostile.json")
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
// Restated field by field from shareReading rather than embedding it, for
// the reason every struct in share.go is restated: a field added to the
// reading must not become part of a published API without somebody writing
// the line here.
//
// v1 is additive only. A field may be added; none may be renamed, retyped or
// removed while a published page asks for v1. vibepanel.d.ts declares every
// field here and TestTheSDKTypesMatchTheSnapshot holds the two together.
type shareSnapshot struct {
	V int `json:"v"`
	// Page is which page and version this is. The SDK reloads when it changes.
	Page *shareSnapshotPage `json:"page"`
	// Sections is what the page's manifest asked for, in pages.Sections order.
	Sections []string `json:"sections"`
	// Params is every declared parameter with this link's value, or its
	// default.
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

	// A page with a backend, additive like everything above
	// (docs/page-backend.md). Data is every public key the version declares;
	// Interactive is whether this link may run visitor actions right now;
	// Actions the visitor actions it declares; Sources the last fetch of each
	// source; Server what server.js's transform returned, or null.
	Data        map[string]any            `json:"data"`
	Interactive bool                      `json:"interactive"`
	Actions     map[string]snapshotAction `json:"actions"`
	Sources     map[string]*sourceResult  `json:"sources"`
	Server      any                       `json:"server"`
}

// snapshotAction is one action as a page sees it: enough to draw a button and
// a form for it, and whether pressing it can work.
type snapshotAction struct {
	Who     string                     `json:"who"`
	Label   string                     `json:"label"`
	Input   map[string]*pages.DataSpec `json:"input"`
	Enabled bool                       `json:"enabled"`
}

// snapshotActions lists the actions a caller may run; visitor for a share
// link, admin for an admin page.
func snapshotActions(m pages.Manifest, forAdmin, enabled bool) map[string]snapshotAction {
	out := map[string]snapshotAction{}
	for name, a := range m.Actions {
		if forAdmin && !a.AdminMay() || !forAdmin && !a.VisitorMay() {
			continue
		}
		out[name] = snapshotAction{Who: a.Who, Label: a.Label, Input: m.InputFields(a), Enabled: enabled}
	}
	return out
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
	dash shareReading
}

func (m *snapshotMemo) get(key string, now time.Time) (shareReading, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || now.Sub(e.at) >= snapshotMemoTTL {
		return shareReading{}, false
	}
	return e.dash, true
}

func (m *snapshotMemo) put(key string, now time.Time, d shareReading) {
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
	var needs pages.Needs

	if link.PageID == "" {
		// Not converted yet (ConvertBoardLinks runs at startup, before the
		// listener), which only a failed conversion leaves behind.
		return out, http.StatusGone, "this link draws no page"
	}
	var memoKey string
	page, err := s.DB.SharePageByID(ctx, link.PageID)
	if errors.Is(err, store.ErrNotFound) {
		return out, http.StatusGone, "this page no longer exists"
	}
	if err != nil {
		s.noteStale(err)
		return out, http.StatusServiceUnavailable, "the panel cannot reach its own database"
	}
	var manifest pages.Manifest
	// The published version this link draws, 0 for a draft preview: the code
	// transform runs comes from it, as the manifest above does. See
	// serverProgram.
	drawnVersion := 0
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
		drawnVersion = version
		ref.Version = version
		memoKey = link.ID + "|v" + strconv.Itoa(version)
	}
	out.Page = ref
	out.Sections = manifest.SectionNames()
	out.Params = pages.ResolveParams(manifest.Params, link.Params)
	needs = manifest.Needs()

	ns := store.PageDataLive
	if link.Purpose == store.SharePurposePreview {
		ns = store.PageDataDraft
	}
	rows, derr := s.pageDataRows(ctx, page.ID, ns)
	if derr != nil {
		s.noteStale(derr)
		return out, http.StatusServiceUnavailable, "the panel cannot reach its own database"
	}
	out.Data, _ = resolvePageData(manifest, rows, sc.admin)
	if sc.admin {
		out.Interactive = true
		out.Actions = snapshotActions(manifest, true, true)
	} else {
		out.Interactive = manifest.Capabilities().VisitorActions && s.linkMayAct(ctx, link)
		out.Actions = snapshotActions(manifest, false, out.Interactive)
	}
	s.markWatched(page.ID, ns, manifest)
	out.Sources = s.sourceResults(page.ID, ns, manifest)

	dash, hit := s.snapshots.get(memoKey, now)
	if !hit {
		s.snapshots.builds.Add(1)
		var err error
		dash, err = s.buildShareReading(ctx, link, sc.secret, needs)
		if err != nil {
			s.noteStale(err)
			return out, http.StatusServiceUnavailable, "the panel cannot reach its own database"
		}
		s.snapshots.put(memoKey, now, dash)
	}

	// The link's own words from the row this request just read, never from the
	// memo: an owner renaming a screen from a laptop expects the next poll to
	// say so, and a memo keyed by version does not change when a remark does.
	out.Name, out.Remark, out.Detail, out.ExpiresAt = link.Name, link.Remark, link.Detail, link.ExpiresAt
	out.At, out.UsageReadable, out.Stale = dash.At, dash.UsageReadable, dash.Stale
	out.Machine, out.Counts, out.Projects, out.Sessions = dash.Machine, dash.Counts, dash.Projects, dash.Sessions
	out.Spend, out.Todos, out.Trend, out.Flow, out.Feed, out.Repo =
		dash.Spend, dash.Todos, dash.Trend, dash.Flow, dash.Feed, dash.Repo
	out.Scope, out.ScopeName = dash.Scope, dash.ScopeName
	out.ScopeRepoOwner, out.ScopeRepoName = dash.ScopeRepoOwner, dash.ScopeRepoName
	// Last, so transform sees the snapshot it is transforming.
	out.Server = s.serverTransform(ctx, page, ns, manifest, out, drawnVersion)
	return out, http.StatusOK, ""
}

// readDraftManifest reads vibepanel.json out of a draft directory, confined
// the way every other read of it is.
func readDraftManifest(dir string) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("this page has no draft directory")
	}
	return pages.ReadManifestFile(dir)
}
