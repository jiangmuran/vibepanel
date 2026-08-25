// Package webui serves the frontend, from the binary or from disk.
package webui

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
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
		return spaHandler{fsys: os.DirFS(staticDir), root: root}
	}
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Only reachable if the embed directive is broken, which is a build-
		// time mistake rather than something a deployment can hit.
		panic("webui: embedded dist missing: " + err.Error())
	}
	return spaHandler{fsys: sub}
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
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}

	// Reject anything that escapes the root. path.Clean above already collapses
	// "..", but a disk-backed root deserves the belt as well as the braces:
	// this handler is one URL away from reading the user's home directory.
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
	http.ServeContent(w, r, name, st.ModTime(), rs)
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
	http.ServeContent(w, r, "index.html", st.ModTime(), rs)
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
