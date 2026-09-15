package pages

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// A page as one file: a zip with vibepanel.json at its root and the page's
// files beside it, which is exactly what a page directory holds minus the
// companions every directory gets anyway (the SDK, its types, AGENTS.md).
//
// Export and import go through the same rules as a publish. An archive is a
// file somebody was sent, so it is read the way a directory an agent wrote is
// read -- every path through ValidPath, every file through sniffOK, every limit
// an error -- and not unpacked first and checked afterwards.

// MaxArchiveBytes bounds an uploaded archive. A page is at most MaxBytes of
// files, and zip only ever makes that smaller; the slack is for headers.
const MaxArchiveBytes = MaxBytes + 1<<20

// WriteArchive writes a manifest and files as a zip.
func WriteArchive(w io.Writer, manifest []byte, files []File) error {
	zw := zip.NewWriter(w)
	// A fixed time, so exporting the same version twice gives the same bytes.
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

// ReadArchive reads a page out of a zip.
//
// One folder at the top is looked through, because "compress this folder" is
// how most people make a zip, and it wraps everything in the folder's name.
// Anything a page could not serve is listed in Ignored rather than refused, so
// a zip of a whole page directory -- AGENTS.md, .gitignore, the SDK copy --
// imports; a path that could be served and is malformed, a file that is not
// what its name says, or a limit exceeded is an error.
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
			// Refused rather than skipped with the dotfiles: an archive that
			// reaches out of itself was made to, and saying so is the answer.
			return Bundle{}, fmt.Errorf("%s: a path in a page is letters, digits, dots, dashes and "+
				"underscores, and never leaves the page", name)
		case hidden:
			continue
		case name == ManifestFile:
			if manifest, err = readZipFile(zf, MaxFileBytes); err != nil {
				return Bundle{}, fmt.Errorf("%s: %w", name, err)
			}
			continue
		case name == SDKFile || name == TypesFile:
			b.Ignored = append(b.Ignored, Ignored{name, "the panel provides its own"})
			continue
		case ContentTypeFor(name) == "":
			b.Ignored = append(b.Ignored, Ignored{name, "not a type a page can serve"})
			continue
		case !ValidPath(path.Clean(name)) || path.Clean(name) != name:
			return Bundle{}, fmt.Errorf("%s: a path in a page is letters, digits, dots, dashes and "+
				"underscores, and no segment starts with a dot", name)
		case seen[name]:
			return Bundle{}, fmt.Errorf("%s is in the archive twice", name)
		}
		if len(b.Files) >= MaxFiles {
			return Bundle{}, fmt.Errorf("a page holds at most %d files", MaxFiles)
		}
		// The size in the header is what the archive claims; readZipFile
		// counts what actually arrives, which is the one a zip bomb lies about.
		body, rerr := readZipFile(zf, MaxFileBytes)
		if rerr != nil {
			return Bundle{}, fmt.Errorf("%s: %w", name, rerr)
		}
		ct := ContentTypeFor(name)
		if !sniffOK(ct, body) {
			return Bundle{}, fmt.Errorf("%s does not contain what its name says", name)
		}
		b.Bytes += int64(len(body))
		if b.Bytes > MaxBytes {
			return Bundle{}, fmt.Errorf("a page is at most %d MiB in total", MaxBytes>>20)
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
	b.Manifest = m
	if !b.Has(IndexFile) {
		return Bundle{}, ErrNoIndex
	}
	sort.Slice(b.Files, func(i, j int) bool { return b.Files[i].Path < b.Files[j].Path })
	return b, nil
}

// archivePrefix is the one folder every entry is inside, or "" when the
// manifest is already at the top.
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
