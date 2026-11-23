package httpapi

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What the whole-host file routes (/api/fs/*, fsfile.go) leave in the audit log.
//
// Those routes read and write any file the panel's user can, because a path
// printed in a terminal can be anywhere and anyone signed in already has a
// shell (docs/design.md, "The viewer outside a project"). That is accepted and
// not narrowed; what is owed in exchange is a record. A click that opens
// ~/.ssh/id_ed25519 in a browser tab is not the same event as a click that
// opens a file in the project, and without a row nothing afterwards says it
// happened.
//
// Reads inside a registered project are not audited: they never were, and one
// preview is several requests. Writes always are (filewrite.go).

// fsReadWindow is how long one (user, path, kind of access) is one row. A
// click is a preview, then a render, then every subresource the page asks for
// through /fs/inline; a row each would bury the one fact -- this file was
// opened -- under its own plumbing.
const fsReadWindow = time.Minute

// fsReadCap bounds the memory the window map may hold. A person clicking
// paths cannot reach it; a script walking the disk can, and it is cleared
// rather than evicted because a full map means the rows are already
// being written one per path.
const fsReadCap = 4096

// fsReadAudit is the coalescing state. The zero value is ready.
type fsReadAudit struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// first reports whether key has not been recorded within the window, and
// records it when it has not.
func (a *fsReadAudit) first(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.seen[key]; ok && now.Sub(t) < fsReadWindow {
		return false
	}
	if a.seen == nil || len(a.seen) >= fsReadCap {
		a.seen = map[string]time.Time{}
	}
	a.seen[key] = now
	return true
}

// fsAccess is which kind of access a route is. Reading in the viewer and
// downloading are different acts -- the second leaves the browser's sandbox
// as a file on somebody's disk -- so one does not hide the other inside a
// window.
type fsAccess struct {
	download bool
	route    string
}

var (
	fsPreview  = fsAccess{false, "preview"}
	fsRender   = fsAccess{false, "render"}
	fsInline   = fsAccess{false, "inline"}
	fsArchive  = fsAccess{false, "archive-entries"}
	fsSQLite   = fsAccess{false, "sqlite"}
	fsDownload = fsAccess{true, "download"}
)

// secretMark opens the audit detail of a file that holds the panel's own
// secrets or somebody's credentials, so a scan of the log for them is one
// prefix.
const secretMark = "[credentials] "

// sensitiveRoots is every place a click should leave a louder row. Small on
// purpose: a long list is one nobody keeps right, and a path outside it is
// still audited, only not marked.
func (s *Server) sensitiveRoots() []string {
	roots := []string{
		// The database (sessions, share and API tokens, the audit log itself)
		// and secrets.key, which opens the sealed share links and plugin
		// secrets. Whoever reads both holds live links.
		s.Cfg.DataDir,
	}
	if home := browseHome(); home != browseRoot() {
		for _, rel := range []string{
			".ssh",                                      // private keys
			".gnupg",                                    // private keyring
			".claude", ".codex", ".kimi-code", ".zcode", // agent configs: API keys, OAuth tokens, and the hooks the panel itself installs
		} {
			roots = append(roots, filepath.Join(home, rel))
		}
	}
	return roots
}

// isSensitivePath says whether abs (already symlink-resolved) is under one of
// sensitiveRoots. A root that is itself a symlink is compared resolved too,
// since abs is.
func (s *Server) isSensitivePath(abs string) bool {
	for _, root := range s.sensitiveRoots() {
		if root == "" {
			continue
		}
		if withinDir(filepath.Clean(root), abs) {
			return true
		}
		if real, err := filepath.EvalSymlinks(root); err == nil && withinDir(real, abs) {
			return true
		}
	}
	return false
}

// insideAProject says whether abs is in any registered project, archived or
// not: hiding a project does not make reading it unusual.
func (s *Server) insideAProject(r *http.Request, abs string) bool {
	projects, err := s.DB.ListAllProjects(r.Context())
	if err != nil {
		// Unknown is audited: the failure that costs a row is cheaper than
		// the one that costs the record.
		return false
	}
	for _, p := range projects {
		root, err := filepath.EvalSymlinks(p.Path)
		if err != nil {
			continue
		}
		if withinDir(root, abs) {
			return true
		}
	}
	return false
}

// pathDetail is what a row says about a path: the credentials mark when it
// applies, and that the file is outside every project when it is.
func (s *Server) pathDetail(abs string, outside bool) string {
	var b strings.Builder
	if s.isSensitivePath(abs) {
		b.WriteString(secretMark)
	}
	b.WriteString(abs)
	if outside {
		b.WriteString(" (outside any project)")
	}
	return b.String()
}

// auditFSRead records a read of abs through a /api/fs route, when abs is
// outside every project and this user has not just read it the same way.
func (s *Server) auditFSRead(r *http.Request, abs string, how fsAccess) {
	u, ok := currentUserFrom(r)
	if !ok {
		return
	}
	// The window is consulted before the project lookup so a page's
	// subresources cost a map read rather than a query -- but it is only
	// *written* once the file is known to be outside, so an in-project read
	// does not take a slot from one that matters.
	key := u.Username + "\x00" + strconv.FormatBool(how.download) + "\x00" + abs
	s.fsReads.mu.Lock()
	t, seen := s.fsReads.seen[key]
	s.fsReads.mu.Unlock()
	if seen && time.Since(t) < fsReadWindow {
		return
	}
	if s.insideAProject(r, abs) {
		return
	}
	if !s.fsReads.first(key, time.Now()) {
		return
	}
	// Literal event names at the call, which is where the audit-event list's
	// test looks for them.
	detail := s.pathDetail(abs, false) + " via " + how.route
	if how.download {
		s.audit(r.Context(), "files.download", u.Username, s.clientIP(r), detail)
		return
	}
	s.audit(r.Context(), "files.read", u.Username, s.clientIP(r), detail)
}
