// Package webui serves the frontend, from the binary or from disk.
package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// dist holds the built frontend.
//
// Embedding rather than shipping a directory alongside the binary is the whole
// reason this project is in Go: "download one file and run it" stops being true
// the moment the binary needs a sibling folder to be useful.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the frontend. When staticDir is non-empty the files come from
// disk instead, which is what `npm run dev` output is served through — an
// embedded build would need a Go rebuild for every CSS change.
func Handler(staticDir string) http.Handler {
	if staticDir != "" {
		// Absolute, once, here: the containment check below compares against a
		// path that filepath.Abs has already resolved, and a relative --static-dir
		// (which is what anyone types — "--static-dir web/dist") would never
		// match it. Every request answered 404 while the files sat right there.
		root, err := filepath.Abs(staticDir)
		if err != nil {
			root = filepath.Clean(staticDir)
		}
		return spaHandler{fsys: os.DirFS(staticDir), root: root, gz: &gzCache{}}
	}
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Only reachable if the embed directive is broken, which is a build-
		// time mistake rather than something a deployment can hit.
		panic("webui: embedded dist missing: " + err.Error())
	}
	return spaHandler{fsys: sub, gz: &gzCache{}}
}

// Built reports whether a frontend build is actually embedded.
//
// An empty dist still compiles, so without this check the first sign of a
// forgotten `npm run build` is a blank page rather than a startup warning.
func Built() bool {
	f, err := dist.Open("dist/index.html")
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	return err == nil && st.Size() > 0
}

type spaHandler struct {
	fsys fs.FS
	// root is the absolute, cleaned directory being served, and is empty when
	// serving the embedded build. Absolute because the containment check
	// compares it against a resolved path; anything else silently rejects
	// everything.
	root string
	// gz holds each compressible file's gzip form, made on first request.
	gz *gzCache
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}

	// Reject anything that escapes the root. path.Clean above already collapses
	// "..", but a disk-backed root deserves the belt as well as the braces:
	// this handler is one URL away from reading the user's home directory.
	//
	// Textual, and that is the limit of it. Symlinks are not resolved here, and
	// os.DirFS does not resolve them either — its documentation says it only
	// guarantees the Open calls begin with the prefix, not that the file is
	// within it, and that it is no substitute for chroot. A symlink inside the
	// served directory pointing outside is followed, and these assets are
	// served before authentication because the login page has to load.
	//
	// handleFiles states the principle this stops short of: browse.Resolve
	// refuses anything leaving the root "including through a symlink, which a
	// textual prefix check would happily follow".
	//
	// Left as it is, deliberately. --static-dir is a development flag aimed at
	// a build directory the operator chose, and vite emits no symlinks; the
	// project directory, where an agent can create one, is served by browse,
	// which resolves them.
	//
	// When it is changed, the fix is not EvalSymlinks — that fails on paths
	// which do not exist, and the single-page fallback is the commonest branch
	// here. os.DirFS's own documentation names the replacement: "Use Root.FS to
	// obtain a fs.FS that prevents escapes from the tree via symbolic links."
	// So `os.OpenRoot(staticDir)` and its `.FS()` in place of `os.DirFS`, which
	// also makes the textual check below redundant rather than merely
	// insufficient. OpenRoot returns an error and the Root wants closing, so it
	// is not a one-line swap, and the single-page fallback has to be re-driven
	// in a browser afterwards.
	if h.root != "" {
		abs, err := filepath.Abs(filepath.Join(h.root, filepath.FromSlash(name)))
		if err != nil || (abs != h.root && !strings.HasPrefix(abs, h.root+string(os.PathSeparator))) {
			http.NotFound(w, r)
			return
		}
	}

	f, err := h.fsys.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		// Single-page app: unknown paths are client-side routes, so they get
		// index.html rather than a 404. API and WebSocket routes are mounted
		// before this handler and never reach here.
		h.serveIndex(w, r)
		return
	}
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		h.serveIndex(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "not seekable", http.StatusInternalServerError)
		return
	}
	setCacheHeaders(w, name)
	// `.webmanifest` is not in Go's MIME table, so ServeContent falls through
	// to content sniffing and answers `text/plain; charset=utf-8`. Measured
	// against the real binary, which is the only way this shows up at all: the
	// file is served, it is valid JSON, and every check that fetches it gets a
	// 200.
	//
	// What it costs is the whole of the manifest. A browser that will not read
	// it does not install a web app -- "add to home screen" makes a bookmark
	// instead -- and a bookmark gets none of `theme_color`, `background_color`
	// or `display: standalone`. That is 「上下仍然有白边」 reported three times
	// against a panel whose own surfaces all measure dark in every theme
	// state, because the bars in question are drawn by the browser from a file
	// it declined to parse.
	if strings.HasSuffix(name, ".webmanifest") {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	h.serveContent(w, r, name, st, rs)
}

func (h spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	f, err := h.fsys.Open("index.html")
	if err != nil {
		http.Error(w, "frontend not built; run `npm run build` in web/ or pass --static-dir",
			http.StatusServiceUnavailable)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "frontend unavailable", http.StatusServiceUnavailable)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "frontend unavailable", http.StatusServiceUnavailable)
		return
	}
	setCacheHeaders(w, "index.html")
	h.serveContent(w, r, "index.html", st, rs)
}

// setCacheHeaders caches fingerprinted assets hard and index.html not at all.
//
// Vite renames every asset it emits with a content hash, so those are safe to
// cache forever. index.html is the one file that must be re-fetched, because it
// is what points at the new hashes after an upgrade.
func setCacheHeaders(w http.ResponseWriter, name string) {
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
}

// gzCache keeps one compressed copy per file, in memory.
//
// The bundle is ~1.35 MB of JavaScript and was sent as-is: a phone on 3G
// waited ~29 s on a spinner for what gzip makes ~400 KB. Vite is not asked to
// precompress, so it is done here, once per file rather than per request
// (level 9 on the main chunk costs far more than a request should), and a
// rebuilt file under --static-dir replaces its entry because the key carries
// size and mtime.
type gzCache struct {
	mu sync.Mutex
	m  map[string]gzEntry
}

type gzEntry struct {
	sig  string
	hash string // content digest for ETag; embed.FS has zero ModTime and equal-length upgrades collided on size+mtime
	body []byte // nil: compression did not make it smaller, serve as-is
}

func (c *gzCache) get(name, sig string, rs io.ReadSeeker) (gzEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[name]; ok && e.sig == sig {
		return e, true
	}
	var plain bytes.Buffer
	_, _ = rs.Seek(0, io.SeekStart)
	if _, err := io.Copy(&plain, rs); err != nil {
		return gzEntry{}, false
	}
	sum := sha256.Sum256(plain.Bytes())
	hash := hex.EncodeToString(sum[:16])
	var out bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	_, _ = zw.Write(plain.Bytes())
	_ = zw.Close()
	body := out.Bytes()
	if len(body) >= plain.Len() {
		body = nil
	}
	if c.m == nil {
		c.m = map[string]gzEntry{}
	}
	entry := gzEntry{sig: sig, hash: hash, body: body}
	c.m[name] = entry
	return entry, true
}

// compressible is a whitelist: PNG, woff2 and the like are already compressed
// and gzip only burns CPU on them.
func compressible(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs", ".css", ".html", ".svg", ".json", ".webmanifest", ".map", ".txt", ".wasm":
		return true
	}
	return false
}

// acceptsGzip reads Accept-Encoding far enough to honour "gzip;q=0".
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(strings.Join(r.Header.Values("Accept-Encoding"), ","), ",") {
		tok, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(tok), "gzip") {
			continue
		}
		if _, q, ok := strings.Cut(strings.ReplaceAll(params, " ", ""), "q="); ok {
			if v, err := strconv.ParseFloat(q, 64); err == nil && v <= 0 {
				return false
			}
		}
		return true
	}
	return false
}

// serveContent is http.ServeContent with gzip when the browser accepts it.
//
// Vary is set on every compressible answer, identity included, or a shared
// cache could hand the gzip body to a client that never asked for it. The ETag
// differs per encoding for the same reason, and the compressed body goes
// through ServeContent so HEAD, Range and If-None-Match keep working (a range
// then addresses the encoded bytes, which is what Content-Encoding means).
func (h spaHandler) serveContent(w http.ResponseWriter, r *http.Request, name string, st fs.FileInfo, rs io.ReadSeeker) {
	if !compressible(name) || h.gz == nil {
		http.ServeContent(w, r, name, st.ModTime(), rs)
		return
	}
	w.Header().Add("Vary", "Accept-Encoding")
	// ETag must come from the file's bytes, never from (size, mtime) alone:
	// embed.FS reports zero ModTime on every file, and Vite's 8-char asset
	// hashes keep dist/index.html at the exact same byte length across builds.
	// A size+mtime ETag answered 304 Not Modified on reload after an upgrade
	// and pinned browsers to the previous bundle.
	sig := fmt.Sprintf("%d-%d", st.Size(), st.ModTime().UnixNano())
	entry, ok := h.gz.get(name, sig, rs)
	if ok && acceptsGzip(r) && entry.body != nil {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("ETag", `"`+entry.hash+`-gz"`)
		// ServeContent leaves Content-Length out whenever Content-Encoding
		// is set, so a 1.3 MB bundle would go chunked with no progress
		// figure. A Range reply is sized by net/http's own framing instead.
		if r.Header.Get("Range") == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(entry.body)))
		}
		http.ServeContent(w, r, name, st.ModTime(), bytes.NewReader(entry.body))
		return
	}
	if ok {
		w.Header().Set("ETag", `"`+entry.hash+`"`)
	}
	_, _ = rs.Seek(0, io.SeekStart)
	http.ServeContent(w, r, name, st.ModTime(), rs)
}
