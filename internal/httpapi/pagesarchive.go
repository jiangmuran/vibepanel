package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Moving a page between panels, or keeping one somewhere that is not this
// machine: a zip out, a zip in. See pages.ReadArchive for the rules an
// incoming archive is read by, which are the publish rules.

// PageArchive is one version of a page as a zip: version 0 is the published
// one, and a page never published is exported from its directory as it is.
// It returns the bytes and the version they are (0 for a directory).
func PageArchive(ctx context.Context, db *store.DB, page store.SharePage, version int) ([]byte, int, error) {
	if version == 0 {
		version = page.PublishedVersion
	}
	var buf bytes.Buffer
	if version == 0 {
		b, err := pages.ReadDir(page.SourceDir)
		if err != nil {
			return nil, 0, err
		}
		raw, err := b.Manifest.Encode()
		if err != nil {
			return nil, 0, err
		}
		if err := pages.WriteArchive(&buf, raw, b.Files); err != nil {
			return nil, 0, err
		}
		return buf.Bytes(), 0, nil
	}
	row, err := db.SharePageVersionByNumber(ctx, page.ID, version)
	if err != nil {
		return nil, 0, err
	}
	files, err := db.SharePageFiles(ctx, page.ID, version)
	if err != nil {
		return nil, 0, err
	}
	out := make([]pages.File, 0, len(files))
	for _, f := range files {
		_, data, ferr := db.SharePageFileData(ctx, page.ID, version, f.Path)
		if ferr != nil {
			return nil, 0, ferr
		}
		out = append(out, pages.File{Path: f.Path, ContentType: f.ContentType, Data: data})
	}
	if err := pages.WriteArchive(&buf, row.Manifest, out); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), version, nil
}

func (s *Server) handleExportPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		if version, err = strconv.Atoi(v); err != nil || version < 0 {
			writeErr(w, http.StatusBadRequest, "version is a number")
			return
		}
	}
	data, got, err := PageArchive(ctx, s.DB, page, version)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such version")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := PageArchiveName(page.Name, got)
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="page.zip"; filename*=UTF-8''`+rfc5987(name))
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// PageArchiveName is what an exported page is called: page-<name>-v<N>.zip,
// without the version for a directory that was never published.
func PageArchiveName(name string, version int) string {
	out := PageDirPrefix + dirName(name)
	if version > 0 {
		out += "-v" + strconv.Itoa(version)
	}
	return out + ".zip"
}

// importedPage is what an import answers: the page, and what the archive had
// that a page does not keep.
type importedPage struct {
	Page    store.SharePage `json:"page"`
	Ignored []pages.Ignored `json:"ignored"`
}

// ImportPage makes a new page from an archive, in a new directory under root,
// named name or the archive's own name. Unpublished: an archive is somebody
// else's page until its new owner has looked at it.
func ImportPage(ctx context.Context, db *store.DB, root, userID, name string, archive []byte) (importedPage, error) {
	b, err := pages.ReadArchive(archive)
	if err != nil {
		return importedPage{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = b.Manifest.Name
	}
	if utf8.RuneCountInString(name) > pages.MaxName {
		name = string([]rune(name)[:pages.MaxName])
	}
	dir := NewPageDir(root, name)
	if err := pages.ScaffoldFrom(dir, name, b, PageFixtures()); err != nil {
		return importedPage{}, err
	}
	pages.GitInit(ctx, dir)
	page, err := db.CreateSharePage(ctx, id.New(), userID, name, dir)
	if err != nil {
		return importedPage{}, err
	}
	return importedPage{Page: page, Ignored: b.Ignored}, nil
}

func (s *Server) handleImportPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pages.MaxArchiveBytes))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("an archive is at most %d MiB", pages.MaxArchiveBytes>>20))
		return
	}
	root := s.pagesRoot(ctx)
	if root.Dir == "" {
		writeErr(w, http.StatusInternalServerError, "nowhere to put the page: "+root.Problem)
		return
	}
	out, err := ImportPage(ctx, s.DB, root.Dir, u.ID, r.URL.Query().Get("name"), data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(ctx, "page.imported", u.Username, s.clientIP(r), out.Page.Name+" ("+out.Page.SourceDir+")")
	writeJSON(w, http.StatusCreated, out)
}
