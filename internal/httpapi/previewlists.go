package httpapi

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"github.com/jiangmuran/vibepanel/internal/browse"
)

// handleArchiveEntries answers an archive's table of contents: zip, tar, tar.gz, or
// one gzip-compressed file. The viewer's archive body draws it.
//
// JSON, not the file: nothing in an archive is served by this route, and
// nothing in one is opened -- browse.ListArchive reads a zip's central
// directory and a tar's headers and stops at bounds of its own. Entries an
// extractor would put outside its directory are listed as the archive names
// them and marked `unsafe`.
func (s *Server) handleArchiveEntries(w http.ResponseWriter, r *http.Request) {
	f, info, ok := s.openProjectFile(w, r)
	if !ok {
		return
	}
	defer f.Close()
	serveArchiveEntries(w, f, info)
}

func serveArchiveEntries(w http.ResponseWriter, f interface {
	io.ReaderAt
	Name() string
}, info interface{ Size() int64 }) {
	head := make([]byte, 512)
	n, _ := f.ReadAt(head, 0)
	if !browse.IsArchive(head[:n]) {
		writeErr(w, http.StatusUnsupportedMediaType, "this is not a zip, tar or gzip archive")
		return
	}
	l, err := browse.ListArchive(f, info.Size(), filepath.Base(f.Name()))
	if err != nil {
		var ae *browse.ArchiveError
		if errors.As(err, &ae) {
			writeErr(w, http.StatusUnprocessableEntity, ae.Reason)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if l.Truncated {
		w.Header().Set("X-Preview-Truncated", "true")
	}
	writeJSON(w, http.StatusOK, l)
}

// handleSQLite answers a SQLite database's tables and the first rows of one,
// `?table=` or the first. Opened read-only and immutable, every statement
// under a deadline; the table named is one the database listed, or 400.
func (s *Server) handleSQLite(w http.ResponseWriter, r *http.Request) {
	f, _, ok := s.openProjectFile(w, r)
	if !ok {
		return
	}
	serveSQLite(w, r, f)
}

func serveSQLite(w http.ResponseWriter, r *http.Request, f interface {
	io.ReadCloser
	Name() string
}) {
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	path := f.Name()
	f.Close()
	if !browse.IsSQLite(head[:n]) {
		writeErr(w, http.StatusUnsupportedMediaType, "this is not a SQLite database")
		return
	}
	l, err := browse.ReadDatabase(r.Context(), path, r.URL.Query().Get("table"))
	if err != nil {
		var de *browse.DBError
		if errors.As(err, &de) {
			code := http.StatusUnprocessableEntity
			if de.BadRequest {
				code = http.StatusBadRequest
			}
			writeErr(w, code, de.Reason)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, l)
}
