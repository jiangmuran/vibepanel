package plugins

import (
	"archive/zip"
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
	"time"

	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/pages"
)

// A plugin's files, read from a directory or a zip.
//
// The same rules as a share page's bundle (internal/pages/bundle.go), with a
// different manifest name, different reserved names, larger limits and no
// index.html requirement, and that is why it is a second reader rather than a
// parameter on the first: a plugin and a page agree on what a path may be and
// what a file's bytes must match, and disagree on everything a reader is
// configured by. The byte checks are shared through pages.SniffOK and
// pages.ReadBounded so there is one definition of "a PNG is a PNG".

// servable is what a plugin may contain, by extension. The page's list plus
// the module forms a process or a rung-4 module is written in, and the
// scripts and configuration a process is made of -- all text, all sniffed as
// text. Still an
// allowlist: a file a frame could be served must be a type the sandbox knows
// how to refuse, and a file a process reads is still stored in SQLite and
// checked out, so a binary is a thing to install with a package manager.
var servable = map[string]string{
	".cjs":  "text/javascript; charset=utf-8",
	".map":  "application/json",
	".md":   "text/markdown; charset=utf-8",
	".sh":   "text/x-shellscript; charset=utf-8",
	".py":   "text/x-python; charset=utf-8",
	".toml": "text/plain; charset=utf-8",
	".yaml": "text/plain; charset=utf-8",
	".yml":  "text/plain; charset=utf-8",
}

// ContentTypeFor reports the type a path would be served as, or "".
func ContentTypeFor(p string) string {
	if ct := pages.ContentTypeFor(p); ct != "" {
		return ct
	}
	return servable[strings.ToLower(path.Ext(p))]
}

var pathSegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

const maxPathLen = 200

// reserved are the names at a plugin's root that are never published: the
// manifest travels as a column, the SDK and its types are the panel's, and
// the scaffold's instructions are for the directory.
var reserved = map[string]bool{ManifestFile: true, SDKFile: true, TypesFile: true, UIFile: true,
	"AGENTS.md": true, "CLAUDE.md": true, "README.md": true}

// ValidPath reports whether rel may name a file in a plugin.
func ValidPath(rel string) bool {
	if rel == "" || len(rel) > maxPathLen || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if !pathSegment.MatchString(seg) || seg == "node_modules" {
			return false
		}
	}
	if reserved[rel] {
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

// Bundle is a directory or archive read as a plugin.
type Bundle struct {
	Manifest Manifest `json:"manifest"`
	// Raw is the manifest's bytes as read, stored with the version.
	Raw   []byte `json:"-"`
	Files []File `json:"files"`
	// Ignored lists what was there and is not part of the plugin, with the
	// reason, so "my font does not load" is a line on screen.
	Ignored []Ignored `json:"ignored"`
	Bytes   int64     `json:"bytes"`
}

// Ignored is one path left out of a bundle.
type Ignored struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
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

// Get finds a file by path.
func (b Bundle) Get(rel string) (File, bool) {
	for _, f := range b.Files {
		if f.Path == rel {
			return f, true
		}
	}
	return File{}, false
}

// ReadDir reads a plugin's directory whole, for a publish or an install from
// a path. Every limit is an error, not a truncation.
func ReadDir(root string) (Bundle, error) {
	real, err := browse.Resolve(root, "")
	if err != nil {
		return Bundle{}, fmt.Errorf("cannot read the plugin directory: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return Bundle{}, fmt.Errorf("%s is not a directory", root)
	}
	raw, err := ReadManifestFile(real)
	if err != nil {
		return Bundle{}, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return Bundle{}, err
	}
	b := Bundle{Manifest: m, Raw: raw, Files: []File{}, Ignored: []Ignored{}}
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
			return fmt.Errorf("%s is a symlink; a plugin is published from real files only", rel)
		}
		if !d.Type().IsRegular() {
			b.Ignored = append(b.Ignored, Ignored{rel, "not a regular file"})
			return nil
		}
		switch {
		case rel == ManifestFile:
			return nil
		case reserved[rel]:
			b.Ignored = append(b.Ignored, Ignored{rel, "the panel provides its own"})
			return nil
		case ContentTypeFor(rel) == "":
			b.Ignored = append(b.Ignored, Ignored{rel, "not a type a plugin can carry"})
			return nil
		case !ValidPath(rel):
			return fmt.Errorf("%s: a path in a plugin is letters, digits, dots, dashes and "+
				"underscores, and no segment starts with a dot", rel)
		}
		if len(b.Files) >= MaxFiles {
			return fmt.Errorf("a plugin holds at most %d files", MaxFiles)
		}
		f, rerr := readFile(p, rel)
		if rerr != nil {
			return rerr
		}
		b.Bytes += f.Size
		if b.Bytes > MaxBytes {
			return fmt.Errorf("a plugin is at most %d MiB in total", MaxBytes>>20)
		}
		b.Files = append(b.Files, f)
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	sort.Slice(b.Files, func(i, j int) bool { return b.Files[i].Path < b.Files[j].Path })
	if err := b.checkEntries(); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// checkEntries is the one check that needs the files and the manifest
// together: every entry the manifest names exists, and the theme passes its
// lint. Run by both readers, after the files are in.
func (b Bundle) checkEntries() error {
	m := b.Manifest
	need := func(what, p string) error {
		if !b.Has(p) {
			return fmt.Errorf("%s names %s, which is not in the plugin", what, p)
		}
		return nil
	}
	if m.Theme != nil {
		if err := need("theme.file", m.Theme.File); err != nil {
			return err
		}
		f, _ := b.Get(m.Theme.File)
		if _, err := LintTheme(f.Data, m.ID); err != nil {
			return fmt.Errorf("%s: %w", m.Theme.File, err)
		}
	}
	for i, p := range m.Panels {
		if err := need(fmt.Sprintf("panels[%d].entry", i), p.Entry); err != nil {
			return err
		}
	}
	if m.Server != nil {
		if err := need("server.entry", m.Server.Entry); err != nil {
			return err
		}
	}
	if m.Unsandboxed != nil {
		if err := need("unsandboxed.entry", m.Unsandboxed.Entry); err != nil {
			return err
		}
	}
	return nil
}

// ReadManifestFile reads a directory's plugin.json without parsing it.
func ReadManifestFile(root string) ([]byte, error) {
	real, err := browse.Resolve(root, "")
	if err != nil {
		return nil, fmt.Errorf("cannot read the plugin directory: %w", err)
	}
	p := filepath.Join(real, ManifestFile)
	info, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("a plugin needs a %s", ManifestFile)
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", ManifestFile)
	}
	return pages.ReadBounded(p, MaxFileBytes)
}

// ReadFile reads one file of a plugin's draft, for dev mode. The same rules
// as ReadDir applied to one path; anything refused is fs.ErrNotExist, so a
// probe cannot tell a refused path from an absent one.
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
		return File{}, fs.ErrNotExist
	}
	return readFile(joined, rel)
}

func readFile(abs, rel string) (File, error) {
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
	data, err := pages.ReadBounded(abs, MaxFileBytes)
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", rel, err)
	}
	ct := ContentTypeFor(rel)
	if !pages.SniffOK(ct, data) {
		return File{}, fmt.Errorf("%s does not contain what its name says", rel)
	}
	sum := sha256.Sum256(data)
	return File{Path: rel, ContentType: ct, Size: int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]), Data: data}, nil
}

// Fingerprint is a cheap summary of a draft directory: every path's size and
// modification time, hashed. For dev mode's change detection, twice a second.
func Fingerprint(root string) (string, error) {
	real, err := browse.Resolve(root, "")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	count := 0
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil //nolint:nilerr // a file vanishing mid-walk is an edit
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

// MaxArchiveBytes bounds an uploaded archive: the files plus slack for headers.
const MaxArchiveBytes = MaxBytes + 1<<20

// WriteArchive writes a manifest and files as a zip, plugin.json at the top.
func WriteArchive(w io.Writer, manifest []byte, files []File) error {
	zw := zip.NewWriter(w)
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	put := func(name string, data []byte) error {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at})
		if err != nil {
			return err
		}
		_, err = fw.Write(data)
		return err
	}
	if err := put(ManifestFile, manifest); err != nil {
		return err
	}
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	for _, f := range sorted {
		if !ValidPath(f.Path) {
			return fmt.Errorf("bad path %q", f.Path)
		}
		if err := put(f.Path, f.Data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// ReadArchive reads a plugin out of a zip, by the publish rules: one wrapping
// folder is looked through, what a plugin cannot carry is listed as ignored,
// and a path that leaves the plugin, a file that is not what its name says or
// a limit exceeded is an error.
func ReadArchive(data []byte) (Bundle, error) {
	if len(data) > MaxArchiveBytes {
		return Bundle{}, fmt.Errorf("an archive is at most %d MiB", MaxArchiveBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Bundle{}, fmt.Errorf("not a zip archive: %w", err)
	}
	prefix := archivePrefix(zr.File)
	b := Bundle{Files: []File{}, Ignored: []Ignored{}}
	var manifest []byte
	seen := map[string]bool{}
	for _, zf := range zr.File {
		name := strings.TrimPrefix(zf.Name, prefix)
		if name == "" || strings.HasSuffix(zf.Name, "/") {
			continue
		}
		if !zf.Mode().IsRegular() {
			b.Ignored = append(b.Ignored, Ignored{name, "not a regular file"})
			continue
		}
		hidden := false
		for _, seg := range strings.Split(name, "/") {
			if strings.HasPrefix(seg, ".") || seg == "node_modules" || seg == "__MACOSX" {
				hidden = true
			}
		}
		switch {
		case strings.HasPrefix(name, "/") || strings.Contains("/"+name+"/", "/../") || strings.Contains(name, "\\"):
			return Bundle{}, fmt.Errorf("%s: a path in a plugin never leaves the plugin", name)
		case hidden:
			continue
		case name == ManifestFile:
			if manifest, err = readZipFile(zf, MaxFileBytes); err != nil {
				return Bundle{}, fmt.Errorf("%s: %w", name, err)
			}
			continue
		case reserved[name]:
			b.Ignored = append(b.Ignored, Ignored{name, "the panel provides its own"})
			continue
		case ContentTypeFor(name) == "":
			b.Ignored = append(b.Ignored, Ignored{name, "not a type a plugin can carry"})
			continue
		case !ValidPath(path.Clean(name)) || path.Clean(name) != name:
			return Bundle{}, fmt.Errorf("%s: a path in a plugin is letters, digits, dots, dashes and "+
				"underscores, and no segment starts with a dot", name)
		case seen[name]:
			return Bundle{}, fmt.Errorf("%s is in the archive twice", name)
		}
		if len(b.Files) >= MaxFiles {
			return Bundle{}, fmt.Errorf("a plugin holds at most %d files", MaxFiles)
		}
		body, rerr := readZipFile(zf, MaxFileBytes)
		if rerr != nil {
			return Bundle{}, fmt.Errorf("%s: %w", name, rerr)
		}
		ct := ContentTypeFor(name)
		if !pages.SniffOK(ct, body) {
			return Bundle{}, fmt.Errorf("%s does not contain what its name says", name)
		}
		b.Bytes += int64(len(body))
		if b.Bytes > MaxBytes {
			return Bundle{}, fmt.Errorf("a plugin is at most %d MiB in total", MaxBytes>>20)
		}
		sum := sha256.Sum256(body)
		b.Files = append(b.Files, File{Path: name, ContentType: ct, Size: int64(len(body)),
			SHA256: hex.EncodeToString(sum[:]), Data: body})
		seen[name] = true
	}
	if manifest == nil {
		return Bundle{}, fmt.Errorf("the archive has no %s at its top", ManifestFile)
	}
	m, err := ParseManifest(manifest)
	if err != nil {
		return Bundle{}, err
	}
	b.Manifest, b.Raw = m, manifest
	sort.Slice(b.Files, func(i, j int) bool { return b.Files[i].Path < b.Files[j].Path })
	if err := b.checkEntries(); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

func archivePrefix(files []*zip.File) string {
	for _, f := range files {
		if f.Name == ManifestFile {
			return ""
		}
	}
	prefix := ""
	for _, f := range files {
		if strings.HasPrefix(f.Name, "__MACOSX/") {
			continue
		}
		top, _, ok := strings.Cut(f.Name, "/")
		if !ok {
			return ""
		}
		if prefix == "" {
			prefix = top + "/"
		} else if prefix != top+"/" {
			return ""
		}
	}
	return prefix
}

func readZipFile(zf *zip.File, limit int64) ([]byte, error) {
	if zf.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("larger than %d MiB", limit>>20)
	}
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("larger than it says")
	}
	return data, nil
}

// Hash is the bundle's identity for the install screen: the manifest and
// every file, in path order. Two archives of the same plugin hash the same
// whatever order the zip listed them in.
func (b Bundle) Hash() string {
	h := sha256.New()
	h.Write(b.Raw)
	h.Write([]byte{0})
	for _, f := range b.Files {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write([]byte(f.SHA256))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
