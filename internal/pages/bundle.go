package pages

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/browse"
)

// A page's files, read from the directory an agent is editing.
//
// The same reader serves the Preview pane one file at a time and reads the
// whole directory for a publish, and that sharing is the design: what the
// preview shows and what publish stores are decided by one set of rules, so a
// page cannot look right in the pane and then be refused -- or worse, be
// accepted with a file the pane never showed.

// Limits on a bundle. A page is a screen, not a site: five megabytes is
// several fonts, a sprite sheet and a dozen scripts, and anything much larger
// is a video, which a wall polling every two seconds should not be serving
// out of a SQLite file.
const (
	MaxFiles     = 64
	MaxFileBytes = 2 << 20
	MaxBytes     = 5 << 20
	maxPathLen   = 200
)

// SDKFile is the SDK's name inside a page. The panel serves it from the
// binary at that path in every page, whatever the directory holds, so a copy
// left in the directory for offline work is ignored rather than published.
const SDKFile = "vibepanel.js"

// TypesFile is the SDK's TypeScript declarations, for editors and agents.
// Never served.
const TypesFile = "vibepanel.d.ts"

// IndexFile is what a page opens on.
const IndexFile = "index.html"

// servable is the whole list of what a page may contain, by extension.
//
// An allowlist, and by extension rather than sniffed, for the reason
// preview.go gives: sniffing is how a file somebody thought was data becomes a
// document. The bytes are then checked against the claim (see sniffOK), so an
// extension is necessary and not sufficient.
var servable = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

// ContentTypeFor reports the type a path would be served as, or "".
func ContentTypeFor(p string) string {
	return servable[strings.ToLower(path.Ext(p))]
}

var pathSegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// ValidPath reports whether rel may name a file in a page.
//
// Refused: anything with a dot-segment (".git", ".vibepanel", "..", a
// dotfile), an empty segment, a character outside a conservative set, and the
// two reserved names at the root. The set is conservative because this string
// becomes a URL path segment and a key in a table, and every character it
// refuses is one that has a second meaning in one of those.
func ValidPath(rel string) bool {
	if rel == "" || len(rel) > maxPathLen || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if !pathSegment.MatchString(seg) {
			return false
		}
	}
	if rel == SDKFile || rel == TypesFile || rel == ManifestFile {
		return false
	}
	return ContentTypeFor(rel) != ""
}

// File is one file of a bundle.
type File struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Data        []byte `json:"-"`
}

// Bundle is a directory read as a page.
type Bundle struct {
	Manifest Manifest `json:"manifest"`
	Files    []File   `json:"files"`
	// Ignored lists what was in the directory and is not part of the page,
	// with the reason. Said rather than silently dropped: "my font does not
	// load" is a much shorter conversation when the answer is on screen.
	Ignored []Ignored `json:"ignored"`
	Bytes   int64     `json:"bytes"`
}

// Ignored is one path left out of a bundle.
type Ignored struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ErrNoIndex is a directory with no index.html.
var ErrNoIndex = errors.New("a page needs an index.html")

// ReadDir reads a page's directory whole, for a publish.
//
// Every limit is an error, not a truncation. A page published with its 65th
// file missing is a page whose author does not know which one.
func ReadDir(root string) (Bundle, error) {
	real, err := browse.Resolve(root, "")
	if err != nil {
		return Bundle{}, fmt.Errorf("cannot read the page directory: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return Bundle{}, fmt.Errorf("%s is not a directory", root)
	}

	manifestRaw, err := ReadManifestFile(real)
	if err != nil {
		return Bundle{}, err
	}
	m, err := ParseManifest(manifestRaw)
	if err != nil {
		return Bundle{}, err
	}

	b := Bundle{Manifest: m, Files: []File{}, Ignored: []Ignored{}}
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == real {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, real+string(filepath.Separator)))
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// Refused outright rather than followed and contained. A symlink in
			// a page is either a mistake or a way to publish a file from
			// outside the directory, and in both cases the person should be
			// told rather than have it resolved for them.
			return fmt.Errorf("%s is a symlink; a page is published from real files only", rel)
		}
		if !d.Type().IsRegular() {
			b.Ignored = append(b.Ignored, Ignored{rel, "not a regular file"})
			return nil
		}
		switch {
		case rel == ManifestFile:
			return nil
		case rel == SDKFile:
			b.Ignored = append(b.Ignored, Ignored{rel, "served by the panel"})
			return nil
		case rel == TypesFile:
			b.Ignored = append(b.Ignored, Ignored{rel, "for editors only"})
			return nil
		case ContentTypeFor(rel) == "":
			b.Ignored = append(b.Ignored, Ignored{rel, "not a type a page can serve"})
			return nil
		case !ValidPath(rel):
			return fmt.Errorf("%s: a path in a page is letters, digits, dots, dashes and "+
				"underscores, and no segment starts with a dot", rel)
		}
		if len(b.Files) >= MaxFiles {
			return fmt.Errorf("a page holds at most %d files", MaxFiles)
		}
		f, rerr := readPageFile(p, rel)
		if rerr != nil {
			return rerr
		}
		b.Bytes += f.Size
		if b.Bytes > MaxBytes {
			return fmt.Errorf("a page is at most %d MiB in total", MaxBytes>>20)
		}
		b.Files = append(b.Files, f)
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	if !b.Has(IndexFile) {
		return Bundle{}, ErrNoIndex
	}
	sort.Slice(b.Files, func(i, j int) bool { return b.Files[i].Path < b.Files[j].Path })
	return b, nil
}

// ReadManifestFile reads a directory's vibepanel.json without parsing it,
// refusing one that is a symlink for ReadFile's reason.
func ReadManifestFile(root string) ([]byte, error) {
	real, err := browse.Resolve(root, "")
	if err != nil {
		return nil, fmt.Errorf("cannot read the page directory: %w", err)
	}
	p := filepath.Join(real, ManifestFile)
	info, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("a page needs a %s", ManifestFile)
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", ManifestFile)
	}
	return readBounded(p, MaxFileBytes)
}

// Has reports whether the bundle contains a path.
func (b Bundle) Has(rel string) bool {
	for _, f := range b.Files {
		if f.Path == rel {
			return true
		}
	}
	return false
}

// ReadFile reads one file of a page's draft, for the Preview pane.
//
// The same rules as ReadDir, applied to one path: it must be a path ReadDir
// would publish, it must not pass through a symlink, and its bytes must be
// what its name says. Anything else is fs.ErrNotExist, so a probe cannot tell
// a refused path from an absent one.
func ReadFile(root, rel string) (File, error) {
	if !ValidPath(rel) {
		return File{}, fs.ErrNotExist
	}
	real, err := browse.Resolve(root, "")
	if err != nil {
		return File{}, fs.ErrNotExist
	}
	joined := filepath.Join(real, filepath.FromSlash(rel))
	resolved, err := browse.Resolve(real, rel)
	if err != nil || resolved != joined {
		// Resolve follows symlinks that stay inside the root; ReadDir refuses
		// them outright. A preview that served one would show a file that the
		// publish then refuses, so the two are made to agree here.
		return File{}, fs.ErrNotExist
	}
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		if strings.EqualFold(path.Base(dir), "node_modules") {
			return File{}, fs.ErrNotExist
		}
	}
	return readPageFile(joined, rel)
}

func readPageFile(abs, rel string) (File, error) {
	info, err := os.Lstat(abs)
	if err != nil {
		return File{}, err
	}
	if !info.Mode().IsRegular() {
		return File{}, fs.ErrNotExist
	}
	if info.Size() > MaxFileBytes {
		return File{}, fmt.Errorf("%s is larger than %d MiB", rel, MaxFileBytes>>20)
	}
	data, err := readBounded(abs, MaxFileBytes)
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", rel, err)
	}
	ct := ContentTypeFor(rel)
	if !sniffOK(ct, data) {
		return File{}, fmt.Errorf("%s does not contain what its name says", rel)
	}
	sum := sha256.Sum256(data)
	return File{Path: rel, ContentType: ct, Size: int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]), Data: data}, nil
}

// readBounded reads at most limit bytes and fails on more.
//
// LimitReader as well as the Stat before it: an agent may be writing this file
// right now, so the size that was checked is not the size that arrives.
func readBounded(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p) //nolint:gosec // callers resolved p inside a root
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d MiB", limit>>20)
	}
	return data, nil
}

// sniffOK checks the bytes against the type the extension claims.
//
// Text types must be UTF-8 with no NUL, which is the test browse.IsText already
// uses; images must carry their own magic; fonts theirs. A PNG named .js is
// refused, and so is a script named .png -- the second one is the one that
// matters, because nosniff is what stops a browser second-guessing the type
// and this is what stops the type being wrong in the first place.
func sniffOK(ct string, data []byte) bool {
	switch {
	case strings.HasPrefix(ct, "text/"), ct == "application/json", ct == "image/svg+xml":
		return browse.IsText(data)
	case ct == "font/woff":
		return bytes.HasPrefix(data, []byte("wOFF"))
	case ct == "font/woff2":
		return bytes.HasPrefix(data, []byte("wOF2"))
	case ct == "image/x-icon":
		return bytes.HasPrefix(data, []byte{0, 0, 1, 0})
	case strings.HasPrefix(ct, "image/"):
		head := data
		if len(head) > 512 {
			head = head[:512]
		}
		_, got := browse.SniffMagic(head)
		return got == ct
	}
	return false
}

// Fingerprint is a cheap summary of a draft directory: every publishable
// path's size and modification time, hashed.
//
// For the Preview pane's change detection, which asks twice a second while it
// is open. Reading every byte that often would be reading five megabytes a
// second to learn nothing; a size and an mtime change whenever an editor or an
// agent writes. The manifest is included, so editing sections reloads the
// frame too.
func Fingerprint(root string) (string, error) {
	real, err := browse.Resolve(root, "")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	count := 0
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil //nolint:nilerr // a file vanishing mid-walk is an edit, not a failure
		}
		if p == real {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, real+string(filepath.Separator)))
		if rel != ManifestFile && !ValidPath(rel) {
			return nil
		}
		count++
		if count > MaxFiles*4 {
			return fs.SkipAll
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil //nolint:nilerr // as above
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(info.ModTime().UnixNano(), 10)))
		h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
