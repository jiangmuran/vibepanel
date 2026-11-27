package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/browse"
)

// The viewer's editor writes a text file back (docs/file-preview.md,
// "Editing"). The file is in somebody's project and an agent may be writing it
// at the same moment, so a write names the content it replaces -- the sha256
// the preview route sent as its ETag -- and is refused when that is not what is
// on disk. What it may replace is what the viewer shows as text and nothing
// else: an existing file, inside the project, readable UTF-8, whole.

// fileWriteMaxBytes bounds the new content, and it is the text preview's
// bound on purpose: a file past it was shown truncated, so nobody holds all of
// it to send back, and the preview gives such a file no ETag.
const fileWriteMaxBytes = previewTextBytes

// fileWriteMu makes reading the hash, comparing it and renaming one step for
// writes through this panel. Two tabs saving the same file at once is the
// case the precondition exists for; without this both read the old hash, both
// match, and the second rename silently discards the first save. One lock for
// every file rather than one each: a write is a person pressing save, and a
// map of locks is a map somebody has to empty.
//
// It cannot cover a process outside the panel writing between the compare and
// the rename. That window is the length of one rename, and the file the
// person saved over would have been overwritten by their save a moment later
// anyway; what the precondition catches is the edit made while they were
// typing, which is minutes, not microseconds.
var fileWriteMu sync.Mutex

// fileWriteChecked runs between the compare and the write. A test widens the
// window there to show two saves cannot both pass it.
var fileWriteChecked = func() {}

// underDir says whether abs is dir itself or inside it.
func underDir(abs, dir string) bool {
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// contentETag is the validator for a text file's bytes, strong and quoted as
// RFC 9110 has it.
func contentETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// handleWriteFile replaces a text file's content.
func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.GetProject(ctx, chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	rel := r.URL.Query().Get("path")
	// Resolve follows symlinks and refuses a target outside the project, so
	// a link inside it that points at ~/.bashrc is refused here and a link to
	// another file in the project writes that file, leaving the link a link.
	abs, err := browse.Resolve(p.Path, rel)
	if err != nil {
		writeBrowseErr(w, err)
		return
	}
	s.serveWriteFile(w, r, abs, func(n int) string {
		written := rel
		if root, err := filepath.EvalSymlinks(p.Path); err == nil {
			if got, err := filepath.Rel(root, abs); err == nil {
				written = filepath.ToSlash(got)
			}
		}
		return fmt.Sprintf("%s: %s, %d bytes", p.Name, written, n)
	})
}

func (s *Server) serveWriteFile(w http.ResponseWriter, r *http.Request, abs string, auditDetail func(int) string) {
	ctx := r.Context()
	// The panel's own data directory is the one tree the editor must not be
	// able to replace with text: the live database and secrets.key live there,
	// and a save over either is one wrong click away from deleting the
	// panel's memory. Reads stay open (docs/design.md); this bounds writes.
	// Symlinks are already resolved into abs by the callers, so a link
	// pointing in is caught here as the file it points at.
	if s.Cfg.DataDir != "" {
		if root, derr := filepath.EvalSymlinks(s.Cfg.DataDir); derr == nil && underDir(abs, root) {
			writeErr(w, http.StatusForbidden, "the panel's own data directory is not writable from here")
			return
		}
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such file")
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "that is a directory")
		return
	}
	// Regular files only, for openProjectFile's reason: opening a FIFO with
	// no writer never returns. And there is nothing to replace a device with.
	if !info.Mode().IsRegular() {
		writeErr(w, http.StatusBadRequest, "not a regular file")
		return
	}
	// Required, not optional. A write with no precondition is a write that
	// overwrites whatever an agent put there since the page was loaded, which
	// is the one thing this route must not do quietly. `*` is not accepted
	// either: it means "anything at all", the same overwrite spelled
	// differently.
	match := strings.TrimSpace(r.Header.Get("If-Match"))
	if match == "" || match == "*" {
		writeErr(w, http.StatusPreconditionRequired,
			"say which version of the file this replaces: If-Match with the ETag the preview sent")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, fileWriteMaxBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("the file can be at most %d bytes", fileWriteMaxBytes))
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// What the viewer would show as text, for the new content as for the old:
	// valid UTF-8 and no NUL. Anything else is a file the editor never had.
	if !browse.IsText(body) {
		writeErr(w, http.StatusBadRequest, "the new content is not UTF-8 text")
		return
	}

	fileWriteMu.Lock()
	defer fileWriteMu.Unlock()

	// The current content, read whole and judged again here rather than
	// trusted from the page: the file may have become something else since.
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, http.StatusForbidden, "cannot read that file")
		return
	}
	old, err := io.ReadAll(io.LimitReader(f, fileWriteMaxBytes+1))
	f.Close()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	head := old
	if len(head) > 512 {
		head = head[:512]
	}
	if kind, _ := browse.SniffMedia(head, int64(len(old))); kind != browse.KindBinary || !browse.Readable(old) {
		writeErr(w, http.StatusUnsupportedMediaType, "only a text file can be edited here")
		return
	}
	if len(old) > fileWriteMaxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("the file is past %d bytes and was shown cut short, so it cannot be edited here", fileWriteMaxBytes))
		return
	}
	if match != contentETag(old) {
		writeErr(w, http.StatusPreconditionFailed,
			"the file was changed after you opened it; open it again to see what is there now")
		return
	}

	fileWriteChecked()

	// A temporary file beside it and a rename over it: a reader -- an agent,
	// a compiler watching the tree -- sees the old file or the new one, never
	// half of either, and a full disk leaves the old one where it was.
	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".vp-*")
	if err != nil {
		writeErr(w, http.StatusForbidden, "cannot write in that directory")
		return
	}
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := io.Copy(tmp, bytes.NewReader(body)); err != nil {
		tmp.Close()
		writeErr(w, http.StatusInternalServerError, "writing the file failed: "+err.Error())
		return
	}
	// CreateTemp makes 0600. A script saved from the viewer that stopped
	// being executable, or a config that stopped being readable by the
	// service that reads it, is a save that broke something.
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		writeErr(w, http.StatusInternalServerError, "writing the file failed: "+err.Error())
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		writeErr(w, http.StatusInternalServerError, "writing the file failed: "+err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		writeErr(w, http.StatusInternalServerError, "writing the file failed: "+err.Error())
		return
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		writeErr(w, http.StatusInternalServerError, "writing the file failed: "+err.Error())
		return
	}
	done = true

	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "files.write", u.Username, s.clientIP(r), auditDetail(len(body)))
	}
	etag := contentETag(body)
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, map[string]any{"etag": etag, "size": len(body)})
}
