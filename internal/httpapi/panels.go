package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// registerPanelRoutes mounts the side-panel endpoints: system stats, the file
// browser, notes and todos.
func (s *Server) registerPanelRoutes(r chi.Router) {
	r.Get("/system", s.handleSystem)
	r.Get("/projects/{id}/files", s.handleFiles)
	r.Get("/projects/{id}/download", s.handleDownload)
	r.Post("/projects/{id}/upload", s.handleUpload)
	r.Get("/projects/{id}/notes", s.handleGetNote)
	r.Put("/projects/{id}/notes", s.handlePutNote)
	r.Get("/projects/{id}/todos", s.handleListTodos)
	r.Post("/projects/{id}/todos", s.handleCreateTodo)
	r.Patch("/todos/{todoID}", s.handlePatchTodo)
	r.Delete("/todos/{todoID}", s.handleDeleteTodo)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Sampler.Sample())
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// The project directory is the root, and browse.Resolve refuses anything
	// that leaves it — including through a symlink, which a textual prefix
	// check would happily follow.
	listing, err := browse.List(p.Path, r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, browse.ErrOutsideRoot):
		writeErr(w, http.StatusForbidden, "outside the project")
		return
	case errors.Is(err, os.ErrNotExist):
		writeErr(w, http.StatusNotFound, "no such directory")
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

// maxUploadBytes bounds one request. Generous enough for the screenshots and
// logs this exists for, small enough that a mistyped path cannot fill the disk
// before anyone notices.
const maxUploadBytes = 256 << 20

// handleDownload streams one file out of a project.
//
// Deliberately not http.FileServer over the project root: that would serve
// directory indexes, follow symlinks by its own rules and answer ranged
// requests for paths this code never validated. One file, resolved through the
// same containment as the listing.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	abs, err := browse.Resolve(p.Path, r.URL.Query().Get("path"))
	if err != nil {
		writeBrowseErr(w, err)
		return
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
	// Regular files only, and this is not fussiness.
	//
	// Opening a FIFO for reading blocks until somebody opens the other end.
	// There is no writer, so os.Open never returns: the request goroutine and
	// its descriptor are gone for the lifetime of the process, and graceful
	// shutdown — which waits for requests in flight — never completes either.
	// Measured: one `mkfifo` in a project directory and a single click wedged
	// the test server so hard that Close() hung for the full five-minute test
	// timeout.
	//
	// `mkfifo` needs no privileges and shell scripts create them routinely, so
	// this is a thing a project directory contains, not a thing an attacker
	// must arrange. Device nodes and sockets have their own versions of the
	// same problem, and none of the four is a file anyone meant to download.
	if !info.Mode().IsRegular() {
		writeErr(w, http.StatusBadRequest, "not a regular file")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, http.StatusForbidden, "cannot read that file")
		return
	}
	defer f.Close()

	// Both forms of the filename, per RFC 6266.
	//
	// `filename` is ISO-8859-1 by specification, so putting raw UTF-8 in it
	// leaves the result to the browser: Chromium usually guesses UTF-8 and
	// Firefox has historically read it as Latin-1, which turns 报告.pdf into
	// æŠ¥å'Š.pdf on the way to the disk. `filename*` says the encoding out loud.
	// Old clients that do not understand it fall back to the quoted one, which
	// is why both are sent rather than only the correct one.
	//
	// A filename is attacker-controlled the moment an agent writes one, so the
	// fallback drops everything outside printable ASCII — a raw CR in a header
	// is a response-splitting bug — and the encoder escapes everything that is
	// not an RFC 5987 attr-char. url.PathEscape is not a substitute: it leaves
	// ';' and ',' alone, and ';' is the parameter separator.
	name := filepath.Base(abs)
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+asciiFilename(name)+`"; filename*=UTF-8''`+rfc5987(name))
	// Never let a browser decide it knows better and render the thing inline;
	// this endpoint serves whatever an agent happened to write, HTML included.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// handleUpload writes files into a directory inside a project.
//
// The response carries the absolute paths back, because the one thing the user
// wants immediately afterwards is to name the file at a prompt.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	dir, err := browse.Resolve(p.Path, r.URL.Query().Get("path"))
	if err != nil {
		writeBrowseErr(w, err)
		return
	}
	if info, serr := os.Stat(dir); serr != nil || !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "upload target is not a directory")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected a multipart upload")
		return
	}
	// Streamed part by part rather than ParseMultipartForm, which buffers the
	// whole request in memory and on disk before the handler sees any of it.
	var written []string
	for {
		part, perr := reader.NextPart()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			writeErr(w, http.StatusBadRequest, "malformed upload: "+perr.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		// The browser sends whatever the file was called, which on some
		// platforms includes a path. Only the last element is ours to use, and
		// it still goes through Resolve so that a crafted "..%2f" cannot land
		// outside the directory that was validated above.
		base := filepath.Base(filepath.FromSlash(part.FileName()))
		if base == "." || base == ".." || strings.ContainsRune(base, filepath.Separator) {
			part.Close()
			writeErr(w, http.StatusBadRequest, "unusable filename")
			return
		}
		// Joined rather than passed through browse.Resolve, which resolves
		// symlinks and so requires the path to exist — which an upload target
		// by definition does not. Containment still holds: dir came back from
		// Resolve, and filepath.Base cannot produce a separator, so the result
		// is one element inside an already-validated directory. A symlink
		// sitting at that name does not help an attacker either, because
		// O_EXCL refuses to open anything that already exists.
		target := filepath.Join(dir, base)
		// O_EXCL: an upload must never quietly replace a file an agent is
		// working on. The caller renames and retries instead.
		f, oerr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if oerr != nil {
			part.Close()
			if os.IsExist(oerr) {
				writeErr(w, http.StatusConflict, base+" already exists")
				return
			}
			writeErr(w, http.StatusInternalServerError, oerr.Error())
			return
		}
		_, cerr := io.Copy(f, part)
		part.Close()
		if closeErr := f.Close(); cerr == nil {
			cerr = closeErr
		}
		if cerr != nil {
			// A partial file is worse than no file: the agent would read it.
			_ = os.Remove(target)
			writeErr(w, http.StatusInternalServerError, "writing "+base+": "+cerr.Error())
			return
		}
		written = append(written, target)
	}
	if len(written) == 0 {
		writeErr(w, http.StatusBadRequest, "no files in the upload")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"paths": written})
}

// asciiFilename is the legacy half of Content-Disposition: printable ASCII
// only, with anything else replaced rather than dropped.
//
// Replaced so that a name which is entirely non-ASCII does not collapse to
// nothing — a browser old enough to need this parameter still has to be given
// something to call the file. Byte-wise, so one CJK character becomes three
// underscores; that only ever reaches clients that cannot read filename*.
func asciiFilename(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			out = append(out, '_')
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return "download"
	}
	return string(out)
}

// rfc5987 percent-encodes a filename for the `filename*` parameter.
//
// Everything outside the specification's attr-char set is escaped, on the
// UTF-8 bytes, which is what the encoding is defined over.
func rfc5987(name string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte(attrChars, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func writeBrowseErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, browse.ErrOutsideRoot):
		writeErr(w, http.StatusForbidden, "outside the project")
	case errors.Is(err, os.ErrNotExist):
		writeErr(w, http.StatusNotFound, "no such path")
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) handleGetNote(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "id")
	if _, err := s.DB.GetProject(r.Context(), pid); err != nil {
		writeStoreErr(w, err)
		return
	}
	note, err := s.DB.GetNote(r.Context(), pid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, note)
}

type putNoteRequest struct {
	Content string `json:"content"`
	// BaseRev is the revision the client's text was built on. Present means
	// "only write if nothing has changed under me". Absent is an unconditional
	// write, which is what the CLI and the tests want and what a client that
	// cannot merge should not be sending.
	BaseRev *int64 `json:"baseRev"`
}

// maxNoteBytes bounds a note. Generous for prose, small enough that the whole
// thing can be sent on every save without thinking about it.
const maxNoteBytes = 256 << 10

func (s *Server) handlePutNote(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "id")
	var req putNoteRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Content) > maxNoteBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "note is too long")
		return
	}
	if _, err := s.DB.GetProject(r.Context(), pid); err != nil {
		writeStoreErr(w, err)
		return
	}
	var note store.Note
	var err error
	if req.BaseRev != nil {
		note, err = s.DB.SetNoteIfUnchanged(r.Context(), pid, req.Content, *req.BaseRev)
		if errors.Is(err, store.ErrNoteStale) {
			// The current note goes back with the rejection so the client can
			// show both versions without a second round trip.
			current, gerr := s.DB.GetNote(r.Context(), pid)
			if gerr != nil {
				writeErr(w, http.StatusInternalServerError, gerr.Error())
				return
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "the note changed elsewhere",
				"current": current,
			})
			return
		}
	} else {
		note, err = s.DB.SetNote(r.Context(), pid, req.Content)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyPanel(pid, "note")
	writeJSON(w, http.StatusOK, note)
}

func (s *Server) handleListTodos(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "id")
	if _, err := s.DB.GetProject(r.Context(), pid); err != nil {
		writeStoreErr(w, err)
		return
	}
	todos, err := s.DB.ListTodos(r.Context(), pid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(todos))
}

type createTodoRequest struct {
	Text string `json:"text"`
}

// maxTodoBytes bounds one item. A todo is a line, not a document.
const maxTodoBytes = 2000

func (s *Server) handleCreateTodo(w http.ResponseWriter, r *http.Request) {
	pid := chi.URLParam(r, "id")
	var req createTodoRequest
	if !decode(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeErr(w, http.StatusBadRequest, "text is required")
		return
	}
	if len(text) > maxTodoBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "item is too long")
		return
	}
	if _, err := s.DB.GetProject(r.Context(), pid); err != nil {
		writeStoreErr(w, err)
		return
	}
	todo, err := s.DB.CreateTodo(r.Context(), id.New(), pid, text)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyPanel(pid, "todos")
	writeJSON(w, http.StatusCreated, todo)
}

type patchTodoRequest struct {
	Text *string `json:"text"`
	Done *bool   `json:"done"`
}

func (s *Server) handlePatchTodo(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "todoID")
	var req patchTodoRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Text != nil {
		text := strings.TrimSpace(*req.Text)
		if text == "" {
			writeErr(w, http.StatusBadRequest, "text must not be empty; delete the item instead")
			return
		}
		if len(text) > maxTodoBytes {
			writeErr(w, http.StatusRequestEntityTooLarge, "item is too long")
			return
		}
		if err := s.DB.SetTodoText(ctx, tid, text); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if req.Done != nil {
		if err := s.DB.SetTodoDone(ctx, tid, *req.Done); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	todo, err := s.DB.GetTodo(ctx, tid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	s.notifyPanel(todo.ProjectID, "todos")
	writeJSON(w, http.StatusOK, todo)
}

func (s *Server) handleDeleteTodo(w http.ResponseWriter, r *http.Request) {
	tid := chi.URLParam(r, "todoID")
	// Read it first: after the delete there is nothing left to say which
	// project's list changed, and a broadcast without that makes every viewer
	// refetch every panel.
	todo, err := s.DB.GetTodo(r.Context(), tid)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if err := s.DB.DeleteTodo(r.Context(), tid); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.notifyPanel(todo.ProjectID, "todos")
	w.WriteHeader(http.StatusNoContent)
}

// emptyTodos keeps the compiler honest about the generic helper's use here.
var _ = func() []store.Todo { return emptyIfNil[store.Todo](nil) }
