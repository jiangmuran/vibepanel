package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Where new share pages go.
//
// Nobody is made to choose. Unset, a page goes under the panel's data
// directory, beside the pasted screenshots -- ~/.local/share/vibepanel/pages on
// most machines -- and the panel does not write that path anywhere as a
// setting: moving the data directory moves the pages with it. The owner can
// name a directory of their own, and that is the only way one gets stored.
//
// And whatever is chosen is checked where it is used, not trusted, because the
// answer changes under the panel: a disk unmounted, a directory made read-only,
// a data directory under a system unit's /var/lib that this user cannot write.
// The order below is the fallback, and the settings page shows which rung was
// used and why the ones above it were not.

// pagesRootKey is the settings row the owner's choice is kept in.
const pagesRootKey = "pages.root"

// PagesRoot is the resolved answer, with its reasons.
type PagesRoot struct {
	// Dir is where a new page goes right now.
	Dir string `json:"dir"`
	// Setting is what the owner chose, "" when they have not.
	Setting string `json:"setting"`
	// Source is which rung Dir came from: "setting", "default" or "fallback".
	Source string `json:"source"`
	// Problem says why a rung above Dir was skipped, "" when none was.
	Problem string `json:"problem"`
}

// ResolvePagesRoot walks the fallback: the owner's directory, the data
// directory's pages/, the same path under the home directory when the data
// directory is somewhere else, and a directory in the temporary directory as
// the last resort -- a page must be makeable, and a temporary one says so.
func ResolvePagesRoot(ctx context.Context, db *store.DB, cfg config.Config) (out PagesRoot) {
	if db != nil {
		out.Setting, _ = db.GetSetting(ctx, pagesRootKey, "")
	}
	var problems []string
	try := func(dir, source string) bool {
		if dir == "" {
			return false
		}
		if err := usableDir(dir); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", dir, err))
			return false
		}
		out.Dir, out.Source = dir, source
		return true
	}
	defer func() { out.Problem = strings.Join(problems, "; ") }()

	if try(out.Setting, "setting") || try(cfg.PagesDir(), "default") {
		return out
	}
	if home, err := os.UserHomeDir(); err == nil {
		alt := filepath.Join(home, ".local", "share", "vibepanel", "pages")
		if alt != cfg.PagesDir() && try(alt, "fallback") {
			return out
		}
	}
	try(filepath.Join(os.TempDir(), "vibepanel-pages-"+strconv.Itoa(os.Getuid())), "fallback")
	return out
}

// newPageDir is a free page-<name> directory under the resolved root.
func (s *Server) newPageDir(ctx context.Context, name string) (string, error) {
	root := s.pagesRoot(ctx)
	if root.Dir == "" {
		return "", errors.New("nowhere to put a page: " + root.Problem)
	}
	return NewPageDir(root.Dir, name), nil
}

// pagesRoot is ResolvePagesRoot for a handler.
func (s *Server) pagesRoot(ctx context.Context) PagesRoot {
	return ResolvePagesRoot(ctx, s.DB, s.Cfg)
}

// usableDir makes dir if it is missing and proves a file can be written in it.
//
// A write rather than a permission check, because permission bits are not the
// only thing that says no: a read-only mount, a full disk and an ACL all pass
// a mode test and fail the first page.
func usableDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("not an absolute path")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if inner := errors.Unwrap(err); inner != nil {
			return inner
		}
		return err
	}
	f, err := os.CreateTemp(dir, ".vibepanel-probe-*")
	if err != nil {
		return errors.New("cannot write here")
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// handlePutPagesRoot sets, or with "" clears, the directory new pages go in.
//
// A directory that cannot be used is refused rather than stored: a setting
// that silently falls back on every use is a setting that says one thing on
// the page and does another. Pages already made stay where they are.
func (s *Server) handlePutPagesRoot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir string `json:"dir"`
	}
	if !decode(w, r, &req) {
		return
	}
	dir := strings.TrimSpace(expandHome(strings.TrimSpace(req.Dir)))
	if dir != "" {
		if !filepath.IsAbs(dir) {
			writeErr(w, http.StatusBadRequest, "the pages directory must be an absolute path")
			return
		}
		dir = filepath.Clean(dir)
		if err := usableDir(dir); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", dir, err))
			return
		}
	}
	if err := s.DB.SetSetting(r.Context(), pagesRootKey, dir); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		detail := dir
		if detail == "" {
			detail = "(default)"
		}
		s.audit(r.Context(), "page.root_changed", u.Username, s.clientIP(r), detail)
	}
	writeJSON(w, http.StatusOK, s.pagesRoot(r.Context()))
}
