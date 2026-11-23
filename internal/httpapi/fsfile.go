package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Server-wide file preview routes for paths an agent prints in a terminal.
//
// Agents routinely write artifacts outside the project root (~/.gemini,
// ~/.claude, /tmp), so clicking a file link in the terminal needs to open the
// viewer wherever that regular file sits on the server. Folders stay confined
// to the project's working directory: there is no directory-listing route under
// /fs, and handleResolveFS answers "outside_dir" for any directory outside the
// project so the side panel's FileTree never leaves the workspace.
func (s *Server) registerFSFileRoutes(r chi.Router) {
	r.Get("/fs/resolve", s.handleResolveFS)
	r.Get("/fs/preview", s.handleFSPreview)
	r.Get("/fs/preview/render", s.handleFSRenderPreview)
	r.Get("/fs/inline", s.handleFSInline)
	r.Get("/fs/download", s.handleFSDownload)
	r.Get("/fs/archive-entries", s.handleFSArchiveEntries)
	r.Get("/fs/sqlite", s.handleFSSQLite)
	r.Put("/fs/file", s.handleFSWriteFile)
}

type resolvedPathResponse struct {
	Kind        string `json:"kind"`
	InProject   bool   `json:"inProject"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ModTime     int64  `json:"modTime"`
	Line        int    `json:"line"`
}

var (
	hashLineSuffix  = regexp.MustCompile(`#L?(\d+)(?:C\d+)?(?:-L?\d+(?:C\d+)?)?$`)
	colonLineSuffix = regexp.MustCompile(`(?::(\d+)(?::\d+)?|\((\d+)(?:,\d+)?\))$`)
)

func (s *Server) handleResolveFS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	target := strings.TrimSpace(q.Get("target"))
	if target == "" {
		writeErr(w, http.StatusBadRequest, "target is required")
		return
	}
	fallback := strings.TrimSpace(q.Get("fallback"))
	sessionID := strings.TrimSpace(q.Get("sessionId"))
	projectID := strings.TrimSpace(q.Get("projectId"))

	ctx := r.Context()
	cwd := ""
	if sessionID != "" {
		if sess, err := s.DB.GetSession(ctx, sessionID); err == nil {
			cwd = sess.CWD
			if projectID == "" {
				projectID = sess.ProjectID
			}
		}
	}
	var proj store.Project
	haveProject := false
	if projectID != "" {
		if p, err := s.DB.GetProject(ctx, projectID); err == nil {
			proj = p
			haveProject = true
		}
	}

	home := browseHome()
	res := resolveTerminalTarget(target, home, cwd, proj, haveProject)
	if res.Kind == "missing" && fallback != "" && fallback != target {
		res = resolveTerminalTarget(fallback, home, cwd, proj, haveProject)
	}
	writeJSON(w, http.StatusOK, res)
}

func resolveTerminalTarget(raw, home, cwd string, proj store.Project, haveProject bool) resolvedPathResponse {
	cleaned, uriLine, ok := normalizeTerminalTarget(raw)
	if !ok || cleaned == "" {
		return resolvedPathResponse{Kind: "missing"}
	}

	type variant struct {
		path string
		line int
	}
	variants := []variant{{path: cleaned, line: uriLine}}
	if m := hashLineSuffix.FindStringSubmatchIndex(cleaned); m != nil {
		if n, err := strconv.Atoi(cleaned[m[2]:m[3]]); err == nil && n > 0 {
			line := n
			if uriLine > 0 {
				line = uriLine
			}
			base := cleaned[:m[0]]
			if base != "" {
				variants = append(variants, variant{path: base, line: line})
			}
		}
	} else if m := colonLineSuffix.FindStringSubmatchIndex(cleaned); m != nil {
		numStr := ""
		if m[2] >= 0 {
			numStr = cleaned[m[2]:m[3]]
		} else if m[4] >= 0 {
			numStr = cleaned[m[4]:m[5]]
		}
		if n, err := strconv.Atoi(numStr); err == nil && n > 0 {
			line := n
			if uriLine > 0 {
				line = uriLine
			}
			base := cleaned[:m[0]]
			if base != "" {
				variants = append(variants, variant{path: base, line: line})
			}
		}
	}

	var realProj string
	if haveProject && proj.Path != "" {
		if rp, err := filepath.EvalSymlinks(proj.Path); err == nil {
			realProj = rp
		}
	}

	for _, v := range variants {
		for _, cand := range expandCandidatePaths(v.path, home, cwd, proj.Path, haveProject) {
			resolved, err := filepath.EvalSymlinks(cand)
			if err != nil {
				continue
			}
			info, err := os.Stat(resolved)
			if err != nil {
				continue
			}
			inProject := false
			relInProject := ""
			if realProj != "" && withinDir(realProj, resolved) {
				inProject = true
				if resolved != realProj {
					if rel, rerr := filepath.Rel(realProj, resolved); rerr == nil {
						relInProject = filepath.ToSlash(rel)
					} else {
						inProject = false
					}
				}
			}
			name := filepath.Base(resolved)
			if info.IsDir() {
				if inProject {
					return resolvedPathResponse{
						Kind:        "dir",
						InProject:   true,
						ProjectID:   proj.ID,
						ProjectName: proj.Name,
						Path:        relInProject,
						Name:        name,
					}
				}
				return resolvedPathResponse{
					Kind: "outside_dir",
					Name: name,
				}
			}
			if !info.Mode().IsRegular() {
				return resolvedPathResponse{
					Kind: "not_regular",
					Name: name,
				}
			}
			if inProject {
				return resolvedPathResponse{
					Kind:        "file",
					InProject:   true,
					ProjectID:   proj.ID,
					ProjectName: proj.Name,
					Path:        relInProject,
					Name:        name,
					Size:        info.Size(),
					ModTime:     info.ModTime().Unix(),
					Line:        v.line,
				}
			}
			return resolvedPathResponse{
				Kind:    "file",
				Path:    resolved,
				Name:    name,
				Size:    info.Size(),
				ModTime: info.ModTime().Unix(),
				Line:    v.line,
			}
		}
	}
	return resolvedPathResponse{Kind: "missing"}
}

func normalizeTerminalTarget(raw string) (string, int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", 0, false
	}
	lower := strings.ToLower(s)
	isURI := strings.HasPrefix(lower, "file://") ||
		strings.HasPrefix(lower, "vscode://file/") ||
		strings.HasPrefix(lower, "vscode-insiders://file/") ||
		strings.HasPrefix(lower, "cursor://file/")
	if !isURI {
		return s, 0, true
	}

	line := 0
	if hash := strings.IndexByte(s, '#'); hash >= 0 {
		frag := s[hash:]
		s = s[:hash]
		if m := hashLineSuffix.FindStringSubmatch(frag); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				line = n
			}
		}
	}
	if q := strings.IndexByte(s, '?'); q >= 0 {
		s = s[:q]
	}

	var pathPart string
	switch {
	case strings.HasPrefix(strings.ToLower(s), "file://"):
		rest := s[len("file://"):]
		if strings.HasPrefix(rest, "/") {
			pathPart = rest
		} else if strings.HasPrefix(strings.ToLower(rest), "localhost/") {
			pathPart = rest[len("localhost"):]
		} else {
			return "", 0, false
		}
	default:
		idx := strings.Index(strings.ToLower(s), "://file/")
		if idx < 0 {
			return "", 0, false
		}
		pathPart = s[idx+len("://file"):]
	}

	decoded, err := url.PathUnescape(pathPart)
	if err != nil || decoded == "" {
		return "", 0, false
	}
	return decoded, line, true
}

func expandCandidatePaths(p, home, cwd, projectPath string, haveProject bool) []string {
	if p == "~" {
		if home == "" {
			return nil
		}
		return []string{home}
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, filepath.FromSlash(p[2:]))}
	}
	if filepath.IsAbs(p) {
		return []string{filepath.Clean(p)}
	}
	rel := filepath.FromSlash(p)
	var out []string
	if cwd != "" {
		out = append(out, filepath.Join(cwd, rel))
	}
	if haveProject && projectPath != "" && projectPath != cwd {
		out = append(out, filepath.Join(projectPath, rel))
	}
	return out
}

func withinDir(root, path string) bool {
	if path == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(os.PathSeparator)) {
		prefix += string(os.PathSeparator)
	}
	return strings.HasPrefix(path, prefix)
}

func resolveServerFilePath(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := r.URL.Query().Get("path")
	if !filepath.IsAbs(raw) {
		writeErr(w, http.StatusBadRequest, "expected an absolute file path")
		return "", false
	}
	abs, err := browse.Resolve(browseRoot(), raw)
	if err != nil {
		writeBrowseErr(w, err)
		return "", false
	}
	return abs, true
}

func openServerFile(w http.ResponseWriter, r *http.Request) (*os.File, os.FileInfo, bool) {
	abs, ok := resolveServerFilePath(w, r)
	if !ok {
		return nil, nil, false
	}
	return openRegularFile(w, abs)
}

func (s *Server) handleFSPreview(w http.ResponseWriter, r *http.Request) {
	f, info, ok := openServerFile(w, r)
	if !ok {
		return
	}
	defer f.Close()
	servePreview(w, r, f.Name(), f, info)
}

func (s *Server) handleFSRenderPreview(w http.ResponseWriter, r *http.Request) {
	abs, ok := resolveServerFilePath(w, r)
	if !ok {
		return
	}
	serveRenderPreviewAt(w, r, abs)
}

func (s *Server) handleFSInline(w http.ResponseWriter, r *http.Request) {
	f, info, ok := openServerFile(w, r)
	if !ok {
		return
	}
	defer f.Close()
	serveInline(w, r, f, info)
}

func (s *Server) handleFSDownload(w http.ResponseWriter, r *http.Request) {
	f, info, ok := openServerFile(w, r)
	if !ok {
		return
	}
	defer f.Close()
	serveDownload(w, r, f, info)
}

func (s *Server) handleFSArchiveEntries(w http.ResponseWriter, r *http.Request) {
	f, info, ok := openServerFile(w, r)
	if !ok {
		return
	}
	defer f.Close()
	serveArchiveEntries(w, f, info)
}

func (s *Server) handleFSSQLite(w http.ResponseWriter, r *http.Request) {
	f, _, ok := openServerFile(w, r)
	if !ok {
		return
	}
	serveSQLite(w, r, f)
}

func (s *Server) handleFSWriteFile(w http.ResponseWriter, r *http.Request) {
	abs, ok := resolveServerFilePath(w, r)
	if !ok {
		return
	}
	s.serveWriteFile(w, r, abs, func(n int) string {
		return fmt.Sprintf("%s, %d bytes", abs, n)
	})
}
