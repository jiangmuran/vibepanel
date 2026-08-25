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
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
	Symlink bool   `json:"symlink"`
	// Readable means "a regular file the panel can offer for download". Not a
	// permission check: the point is to keep FIFOs, sockets and device nodes
	// out of the download path, where opening one blocks forever. This field
	// used to be set to true unconditionally, which made the button that reads
	// it decorative.
	Readable bool `json:"readable"`

	// Escapes marks a symlink that points outside the project. The panel shows
	// it, because pretending a file is not there is its own kind of lie, but
	// it offers nothing to do with it.
	Escapes bool `json:"escapes"`
}

// Listing is a directory and what is in it.
type Listing struct {
	Path    string  `json:"path"`
	Parent  *string `json:"parent"`
	Entries []Entry `json:"entries"`
	// Total is how many items the directory actually holds, which is not
	// len(Entries) once the cap bites. Reported so the panel can say that it
	// is showing part of a directory instead of quietly implying it is all of
	// it.
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

// maxEntries bounds a single listing. A directory with a hundred thousand
// files should render slowly, not hang the browser and the server together.
//
// What matters as much as the number is *when* it is applied. Truncating the
// ReadDir order — which is sorted by filename — and sorting afterwards means a
// directory of 2500 files plus one subdirectory named "zzz" returns 2000
// files, no directories at all, and no indication of either. Measured exactly
// that. The tree simply ended, and nothing said why. So the sort comes first
// and the cap comes second, which keeps directories reachable and makes the
// truncation reportable.
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

	// Entries starts as an empty slice, not nil.
	//
	// A nil slice marshals to `null`, and the frontend's very next move is
	// listing.entries.length — so an empty project directory took the whole
	// console down with "Cannot read properties of null", React unmounted the
	// tree, and the page went blank with no error visible anywhere. The line
	// that crashed was the one rendering the "nothing here" message: the code
	// for the empty case was the code that could not survive it.
	out := Listing{Path: filepath.ToSlash(relDir), Entries: []Entry{}}
	if relDir != "" {
		parent := filepath.ToSlash(filepath.Dir(relDir))
		if parent == "." {
			parent = ""
		}
		out.Parent = &parent
	}

	out.Total = len(items)

	// Directories first, then by name, case-insensitively. This is the order
	// every file browser uses, and matching it means nobody has to think.
	//
	// Sorted here rather than after the entries are built, so that the cap
	// below removes the tail of the directory rather than an arbitrary
	// alphabetical slice of it. DirEntry.IsDir comes from the dirent the
	// kernel already returned, so ordering a hundred thousand of them costs no
	// syscalls; only the ones that survive the cap are stat'd.
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.IsDir() != b.IsDir() {
			return a.IsDir()
		}
		return strings.ToLower(a.Name()) < strings.ToLower(b.Name())
	})
	if len(items) > maxEntries {
		items = items[:maxEntries]
		out.Truncated = true
	}

	for _, item := range items {
		e := Entry{
			Name:  item.Name(),
			Path:  filepath.ToSlash(filepath.Join(relDir, item.Name())),
			IsDir: item.IsDir(),
		}
		if fi, err := item.Info(); err == nil {
			e.Size = fi.Size()
			e.ModTime = fi.ModTime().Unix()
			e.Symlink = fi.Mode()&os.ModeSymlink != 0
			e.Readable = fi.Mode().IsRegular()
			// A symlink's target decides whether it behaves as a directory,
			// and ReadDir reports the link itself. This runs after the sort,
			// so a symlink pointing at a directory sorts among the files —
			// the alternative is a stat for every entry in the directory
			// before anything can be ordered, which is what the cap exists to
			// avoid.
			if e.Symlink {
				if target, serr := os.Stat(filepath.Join(abs, item.Name())); serr == nil {
					e.IsDir = target.IsDir()
					// Where it points, not only what it points at.
					//
					// Readable is what makes the panel offer a download, and
					// the download resolves symlinks and refuses anything that
					// leaves the project. A link to /etc/passwd sitting in a
					// project — one `ln -s`, or anything under node_modules —
					// was a regular file, so the button appeared, and clicking
					// it answered `403 outside the project`.
					//
					// A control that cannot do what it offers teaches people
					// the panel is unreliable rather than that the file is out
					// of bounds.
					resolved, rerr := filepath.EvalSymlinks(filepath.Join(abs, item.Name()))
					inside := rerr == nil && (resolved == realRoot || within(realRoot, resolved))
					e.Readable = target.Mode().IsRegular() && inside
					e.Escapes = !inside
				}
			}
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}
