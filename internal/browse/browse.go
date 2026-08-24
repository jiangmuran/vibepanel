// Package browse lists directories under a root, and refuses to leave it.
//
// Separate from the HTTP layer because containment is the whole point and it
// wants testing on its own. Every path here arrives from a URL.
package browse

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrOutsideRoot means the requested path resolves outside the project.
var ErrOutsideRoot = errors.New("browse: path is outside the project")

// Entry is one item in a directory.
type Entry struct {
	Name string `json:"name"`
	// Path is relative to the root, with forward slashes. Relative because an
	// absolute path is the server's business, and handing the browser one
	// invites it to send back a different one.
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	ModTime  int64  `json:"modTime"`
	Symlink  bool   `json:"symlink"`
	Readable bool   `json:"readable"`
}

// Listing is a directory and what is in it.
type Listing struct {
	Path    string  `json:"path"`
	Parent  *string `json:"parent"`
	Entries []Entry `json:"entries"`
}

// maxEntries bounds a single listing. A directory with a hundred thousand
// files should render slowly, not hang the browser and the server together.
const maxEntries = 2000

// Resolve turns a root and a relative request into an absolute path, refusing
// anything that escapes.
//
// Both sides are resolved through EvalSymlinks before comparison: a symlink
// inside the project pointing at /etc would otherwise pass a purely textual
// prefix check, and comparing unresolved paths is the classic way this goes
// wrong.
func Resolve(root, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("browse: root: %w", err)
	}

	// Clean against "/" first so that "../../etc" collapses to "/etc" and then
	// joins as "etc" under the root, rather than climbing out of it.
	cleaned := filepath.Clean("/" + filepath.FromSlash(rel))
	joined := filepath.Join(realRoot, cleaned)

	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// A path that does not exist cannot be listed, but it also must not
		// leak whether something exists outside the root, so containment is
		// still checked against the unresolved form.
		if !within(realRoot, joined) {
			return "", ErrOutsideRoot
		}
		return "", err
	}
	if !within(realRoot, resolved) {
		return "", ErrOutsideRoot
	}
	return resolved, nil
}

func within(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

// List returns the contents of a directory under root.
func List(root, rel string) (Listing, error) {
	abs, err := Resolve(root, rel)
	if err != nil {
		return Listing{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Listing{}, err
	}
	if !info.IsDir() {
		return Listing{}, fmt.Errorf("browse: %s is not a directory", rel)
	}

	items, err := os.ReadDir(abs)
	if err != nil {
		return Listing{}, err
	}

	realRoot, _ := filepath.EvalSymlinks(root)
	relDir, _ := filepath.Rel(realRoot, abs)
	if relDir == "." {
		relDir = ""
	}

	out := Listing{Path: filepath.ToSlash(relDir)}
	if relDir != "" {
		parent := filepath.ToSlash(filepath.Dir(relDir))
		if parent == "." {
			parent = ""
		}
		out.Parent = &parent
	}

	for _, item := range items {
		if len(out.Entries) >= maxEntries {
			break
		}
		e := Entry{
			Name:  item.Name(),
			Path:  filepath.ToSlash(filepath.Join(relDir, item.Name())),
			IsDir: item.IsDir(),
		}
		if fi, err := item.Info(); err == nil {
			e.Size = fi.Size()
			e.ModTime = fi.ModTime().Unix()
			e.Symlink = fi.Mode()&os.ModeSymlink != 0
			// A symlink's target decides whether it behaves as a directory,
			// and ReadDir reports the link itself.
			if e.Symlink {
				if target, serr := os.Stat(filepath.Join(abs, item.Name())); serr == nil {
					e.IsDir = target.IsDir()
				}
			}
		}
		e.Readable = true
		out.Entries = append(out.Entries, e)
	}

	// Directories first, then by name, case-insensitively. This is the order
	// every file browser uses, and matching it means nobody has to think.
	sort.SliceStable(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}
