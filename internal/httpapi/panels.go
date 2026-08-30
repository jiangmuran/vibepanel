package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// registerPanelRoutes mounts the side-panel endpoints: system stats, the file
// browser, notes, and the todo routes the wall boards and API clients still
// use after the panel's own checklist was removed.
func (s *Server) registerPanelRoutes(r chi.Router) {
	r.Get("/system", s.handleSystem)
	r.Get("/usage", s.handleUsage)
	r.Get("/browse", s.handleBrowse)
	r.Post("/browse/mkdir", s.handleBrowseMkdir)
	r.Get("/projects/{id}/files", s.handleFiles)
	r.Get("/projects/{id}/download", s.handleDownload)
	r.Get("/projects/{id}/preview", s.handlePreview)
	// A second route rather than a mode on the first, and that placement is the
	// design. See internal/httpapi/preview_render.go: /preview can only ever
	// answer with an attachment, and exactly one handler in this codebase can
	// produce an inline text/html response out of a project directory.
	r.Get("/projects/{id}/preview/render", s.handleRenderPreview)
	r.Post("/projects/{id}/upload", s.handleUpload)
	r.Post("/projects/{id}/mkdir", s.handleProjectMkdir)
	r.Post("/clipboard", s.handleClipboard)
	r.Get("/projects/{id}/notes", s.handleGetNote)
	r.Put("/projects/{id}/notes", s.handlePutNote)
	// The note that belongs to no project. Its own pair of routes rather than
	// a magic id under /projects/: `{id}` is looked up in projects and has to
	// stay that way, and a handler that special-cases one value of a path
	// parameter is a handler somebody adds a second special case to.
	r.Get("/notes", s.handleGetGlobalNote)
	r.Put("/notes", s.handlePutGlobalNote)
	// The four below have no caller in this repository's frontend any more.
	// That is deliberate and they are deliberately still here.
	//
	// The side panel's checklist was removed — 「也不要留下 todo」 — and the
	// obvious next step is to delete the handlers, the store methods and the
	// table. Two things call them that are not the side panel. The read-only
	// wall boards count todos: `todos` is a widget kind, `todoPercent` is a
	// gauge metric, and four of the shipped presets place one, so deleting the
	// routes means rewriting somebody's wall. And an API token is a documented
	// way in — an agent that finishes a task can tick it off, which is the one
	// use of this that never needed a panel at all.
	//
	// So: no UI, and an API. If the wall widgets ever go, these go with them,
	// and `TodoProgressByProject` in internal/store is the thread to pull.
	r.Get("/projects/{id}/todos", s.handleListTodos)
	r.Post("/projects/{id}/todos", s.handleCreateTodo)
	r.Patch("/todos/{todoID}", s.handlePatchTodo)
	r.Delete("/todos/{todoID}", s.handleDeleteTodo)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Sampler.Sample())
}

// usageResponse is what each session's process tree is costing right now.
type usageResponse struct {
	// Readable is false where there is no /proc to read, which is a different
	// thing from every session reading zero.
	Readable bool `json:"readable"`
	Cores    int  `json:"cores"`

	// Sessions is keyed by session id, and a session is absent rather than
	// zero when its pane has gone -- zero is a real reading.
	Sessions map[string]sysmon.Usage `json:"sessions"`
}

// handleUsage samples per-session CPU and memory.
//
// Deliberately not part of the state broadcast. That snapshot goes to every
// viewer whenever it differs from the last one, and a number that moves every
// tick would make every tick a broadcast -- the same reasoning that keeps
// LastOutputAt off the wire. This is polled by whoever is looking at it, and
// by nobody when the panel is in the background.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	out := usageResponse{
		Readable: sysmon.ProcReadable(),
		Cores:    runtime.NumCPU(),
		Sessions: map[string]sysmon.Usage{},
	}
	if !out.Readable {
		writeJSON(w, http.StatusOK, out)
		return
	}

	sessions, err := s.DB.ListSessions(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	infos, err := s.Tmux.List(r.Context())
	if err != nil {
		// tmux being unreachable is not a reason to fail the page. The meters
		// go blank; everything else on screen is still true.
		writeJSON(w, http.StatusOK, out)
		return
	}
	pidOf := make(map[string]int, len(infos))
	for _, i := range infos {
		pidOf[i.Name] = i.PID
	}
	panes := make(map[string]int, len(sessions))
	for _, sess := range sessions {
		if pid, ok := pidOf[sess.TmuxName]; ok && pid > 0 {
			panes[sess.ID] = pid
		}
	}
	out.Sessions = s.TreeSampler.Sample(panes)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
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

// previewMaxBytes bounds what one click can pull off the disk.
//
// A preview is not asked for by name the way a download is -- it is what
// happens when somebody taps a row in a list of files an agent wrote, and one
// of those rows is a core dump. Without a ceiling, clicking it is a denial of
// service against the person who clicked: the server streams it, the browser
// holds it, and neither of them was told how big it was first.
//
// Eight mebibytes is chosen against what a preview is for. A screenshot off a
// 5K display is two to four megabytes of PNG, a scanned page is a few, and
// source files are kilobytes; nothing this feature exists to show is near the
// limit, and everything that would hurt is well past it. Far below
// maxUploadBytes on purpose -- an upload is a file you chose, a preview is a
// file you brushed against.
//
// The browser holds the same number, in web/src/components/panels/preview.ts,
// so it can answer instantly from the size it already has instead of starting
// a transfer to be told no. TestThePreviewBoundIsTheSameOnBothSides is what
// keeps the two from drifting.
const previewMaxBytes = 8 << 20

// previewTextBytes and previewTextLines bound the *rendering* rather than the
// transfer, and they are much smaller for a reason the byte limit does not
// cover: a browser will happily stream eight megabytes and then spend a minute
// laying it out as wrapped monospace in a 280px column. See browse.ClipText,
// which is where both bites are decided and where the line bound is argued.
const (
	previewTextBytes = 256 << 10
	previewTextLines = 4000
)

// handleDownload streams one file out of a project.
//
// Deliberately not http.FileServer over the project root: that would serve
// directory indexes, follow symlinks by its own rules and answer ranged
// requests for paths this code never validated. One file, resolved through the
// same containment as the listing.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
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

	name := filepath.Base(abs)
	setAttachmentHeaders(w, name)
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// setAttachmentHeaders names a file for the browser and forbids it from
// deciding the bytes are a page.
//
// Both forms of the filename, per RFC 6266.
//
// `filename` is ISO-8859-1 by specification, so putting raw UTF-8 in it leaves
// the result to the browser: Chromium usually guesses UTF-8 and Firefox has
// historically read it as Latin-1, which turns a CJK name into mojibake on the
// way to the disk. `filename*` says the encoding out loud. Old clients that do
// not understand it fall back to the quoted one, which is why both are sent
// rather than only the correct one.
//
// A filename is attacker-controlled the moment an agent writes one, so the
// fallback drops everything outside printable ASCII -- a raw CR in a header is
// a response-splitting bug -- and the encoder escapes everything that is not
// an RFC 5987 attr-char. url.PathEscape is not a substitute: it leaves ';' and
// ',' alone, and ';' is the parameter separator.
//
// The content type is the other half, and it is why the preview endpoint
// shares this rather than sending something a browser would render. Both
// endpoints serve whatever an agent happened to write, HTML included, on the
// panel's own origin. Nothing here is ever offered inline: the preview hands
// the bytes to fetch(), and what they are is decided from a type this server
// picked off a whitelist.
func setAttachmentHeaders(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+asciiFilename(name)+`"; filename*=UTF-8''`+rfc5987(name))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// handlePreview answers "what is in this file" without committing to reading
// all of it.
//
// One endpoint rather than two, and bytes rather than JSON. The kind has to be
// decided from the content (browse.SniffMagic), which means the server is
// already holding the head of the file at the moment it knows the answer -- so
// it says what it found in a header and sends what it read, and the browser
// makes one request instead of asking what the file is and then asking for it.
// Base64 in a JSON envelope was the alternative, and it is a third larger for
// the only two kinds that need it.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
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
	// Regular files only, for the reason spelled out on handleDownload: opening
	// a FIFO with no writer never returns, and it takes the request goroutine
	// and graceful shutdown with it. A preview is reached by clicking a row
	// rather than a download button, so it is the easier of the two to trip
	// over by accident.
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

	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	head = head[:n]
	name := filepath.Base(abs)

	if kind, mime := browse.SniffMagic(head); kind != browse.KindBinary {
		// Half a picture draws nothing, so these are the kinds the ceiling
		// refuses outright rather than truncating.
		if info.Size() > previewMaxBytes {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("%d bytes is past the %d-byte preview limit; download it instead",
					info.Size(), previewMaxBytes))
			return
		}
		setAttachmentHeaders(w, name)
		w.Header().Set("X-Preview-Kind", string(kind))
		w.Header().Set("X-Preview-Type", mime)
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// LimitReader as well as the Stat above. An agent may be writing this
		// file right now, so the size that was checked is not the size that
		// arrives: the check is a courtesy, the limit is the enforcement.
		_, _ = io.Copy(w, io.LimitReader(f, previewMaxBytes))
		return
	}

	// Text is truncated rather than refused, which is what makes a
	// two-gigabyte log worth clicking at all: the part anybody wants is the
	// top, and the budget below is the only part of it ever read.
	rest, err := io.ReadAll(io.LimitReader(f, previewTextBytes+1-int64(len(head))))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	buf := append(head, rest...)
	more := len(buf) > previewTextBytes
	if more {
		buf = buf[:previewTextBytes]
	}
	text, truncated := browse.ClipText(buf, more, previewTextLines)
	if !browse.IsText(text) {
		writeErr(w, http.StatusUnsupportedMediaType, "no preview for this kind of file")
		return
	}
	setAttachmentHeaders(w, name)
	w.Header().Set("X-Preview-Kind", string(browse.KindText))
	// Still text, still an attachment, still nothing a browser will render --
	// this header only says that a *second* endpoint would draw this file, so
	// the panel can offer the choice. The bytes in this response are unchanged
	// by it, which is why it can be added to the safe endpoint at all.
	if markup, _ := browse.SniffMarkup(name, head); markup != browse.MarkupNone {
		w.Header().Set("X-Preview-Markup", string(markup))
	}
	if truncated {
		// The panel says so on screen. A preview that silently stops is the
		// same defect as a directory listing that silently stops, which this
		// panel already refuses to do.
		w.Header().Set("X-Preview-Truncated", "true")
	}
	_, _ = w.Write(text)
}

// handleUpload writes files into a directory inside a project.
//
// The response carries the absolute paths back, because the one thing the user
// wants immediately afterwards is to name the file at a prompt.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// `?dest=panel` writes outside the project.
	//
	// A screenshot pasted into a terminal used to land in the session's working
	// directory, which for an agent session is a git repository -- so pasting a
	// picture at somebody dirtied their tree, and the file was still there
	// afterwards. 「粘贴图片不要直接粘贴到项目根目录啊」. The panel keeps its
	// own directory for these; the project stays the *other* option, because
	// dragging a file into the tree still means "put it here".
	dir := ""
	if r.URL.Query().Get("dest") == "panel" {
		dir = filepath.Join(s.Cfg.DataDir, "pasted")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		dir, err = browse.Resolve(p.Path, r.URL.Query().Get("path"))
		if err != nil {
			writeBrowseErr(w, err)
			return
		}
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
		f, target, oerr := createUnique(dir, base)
		if oerr != nil {
			part.Close()
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
	// emptyIfNil: `written` is nil when no part of the multipart body was
	// named "file", so the response was `{"paths": null}` and the frontend
	// would map over it. Nothing sends such a request today, which is the only
	// reason this has not been seen.
	writeJSON(w, http.StatusOK, map[string]any{"paths": emptyIfNil(written)})
}

// createUnique opens a new file in dir, adding -1, -2, ... until the name is
// free.
//
// O_EXCL every time, which is the part that must not be lost: an upload may
// never quietly replace a file an agent is working on, and the check-then-open
// version of this has a window between the two where it can. Each attempt is
// atomic; a collision is a retry rather than a race.
//
// Refusing was the old answer -- 409 `screenshot.png already exists` -- and it
// is the wrong one for what this is actually for. Pasting a screenshot at an
// agent produces `image.png` every single time, from every operating system,
// so the second paste of a session always failed and the fix offered to the
// person was to go and rename a file they never chose the name of.
//
// The suffix goes before the extension, because `image-1.png` is a picture and
// `image.png-1` is not. A name with no extension gets the suffix at the end.
func createUnique(dir, base string) (*os.File, string, error) {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		target := filepath.Join(dir, name)
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, target, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
		// A bound, because this loop is driven by what is on disk and the disk
		// is not this package's to trust. A directory holding a thousand
		// `image-N.png` is somebody's mistake, not a reason to sit in a loop
		// stat-ing forever.
		if i >= 999 {
			return nil, "", fmt.Errorf("%s: too many files with this name already", base)
		}
	}
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
		s.writeStoreErr(w, err)
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

// handleGetGlobalNote is handleGetNote without a project to look up.
func (s *Server) handleGetGlobalNote(w http.ResponseWriter, r *http.Request) {
	note, err := s.DB.GetGlobalNote(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, note)
}

// handlePutGlobalNote is handlePutNote for it, including the same conflict
// answer: the editor is the same component and cannot have two shapes of 409.
func (s *Server) handlePutGlobalNote(w http.ResponseWriter, r *http.Request) {
	var req putNoteRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Content) > maxNoteBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "note is too long")
		return
	}
	var note store.Note
	var err error
	if req.BaseRev != nil {
		note, err = s.DB.SetGlobalNoteIfUnchanged(r.Context(), req.Content, *req.BaseRev)
		if errors.Is(err, store.ErrNoteStale) {
			current, gerr := s.DB.GetGlobalNote(r.Context())
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
		note, err = s.DB.SetGlobalNote(r.Context(), req.Content)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Every project's panel socket, because this note is reachable from all of
	// them and a second tab showing a stale copy is the thing the revision
	// check exists to make visible rather than silent.
	s.notifyPanel(store.GlobalNoteID, "note")
	writeJSON(w, http.StatusOK, note)
}

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
		s.writeStoreErr(w, err)
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
		s.writeStoreErr(w, err)
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
		s.writeStoreErr(w, err)
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
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.Done != nil {
		if err := s.DB.SetTodoDone(ctx, tid, *req.Done); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	todo, err := s.DB.GetTodo(ctx, tid)
	if err != nil {
		s.writeStoreErr(w, err)
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
		s.writeStoreErr(w, err)
		return
	}
	if err := s.DB.DeleteTodo(r.Context(), tid); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.notifyPanel(todo.ProjectID, "todos")
	w.WriteHeader(http.StatusNoContent)
}

// browseRoot is where the directory picker starts.
//
// The home directory, not "/". Everything the panel is for lives under it, a
// picker rooted at the filesystem makes the first screen a list of /boot and
// /proc, and Resolve's containment then means something a reader can hold in
// their head: nothing this endpoint returns is outside your own home.
//
// It is not a security boundary and is not offered as one -- this endpoint is
// behind the same session as a writable terminal, and anyone through that door
// can read the disk anyway. It is a boundary on *noise*. Paths outside it are
// reached with the text field beside the picker.
func browseRoot() (string, error) { return os.UserHomeDir() }

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	root, err := browseRoot()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "no home directory to browse")
		return
	}
	listing, err := browse.Dirs(root, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root":      root,
		"path":      listing.Path,
		"parent":    listing.Parent,
		"entries":   emptyIfNil(listing.Entries),
		"total":     listing.Total,
		"truncated": listing.Truncated,
	})
}

type mkdirRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// handleClipboard puts text in the tmux paste buffer.
//
// One buffer for the socket, which is what tmux has: this is the panel filling
// the clipboard so whatever is in a pane can take it -- prefix-] in tmux, or an
// agent that reads the buffer -- rather than the panel typing for you. Typing
// is `Paste`, and it is a different thing that already exists.
//
// Bounded, because it goes on a command's standard input and the buffer is
// shared with everything else on the socket. A path is a few hundred bytes.
func (s *Server) handleClipboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Text) > 64*1024 {
		writeErr(w, http.StatusRequestEntityTooLarge, "too much for a paste buffer")
		return
	}
	if err := s.Tmux.LoadBuffer(r.Context(), req.Text); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProjectMkdir makes a directory inside a project.
//
// The directory picker has been able to do this since it existed, against the
// home directory; the file tree could not, against the project it is showing.
// Same helper, same refusals -- `browse.Mkdir` is where the name is checked and
// the root is enforced -- so the only new thing here is which root.
func (s *Server) handleProjectMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := s.DB.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	created, err := browse.Mkdir(p.Path, req.Path, strings.TrimSpace(req.Name))
	if err != nil {
		if os.IsExist(err) {
			writeErr(w, http.StatusConflict, req.Name+" already exists")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": created})
}

func (s *Server) handleBrowseMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirRequest
	if !decode(w, r, &req) {
		return
	}
	root, err := browseRoot()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "no home directory to write in")
		return
	}
	created, err := browse.Mkdir(root, req.Path, strings.TrimSpace(req.Name))
	if err != nil {
		// The two that a person can act on, told apart. "already exists" is a
		// different instruction from "that is not a name".
		if os.IsExist(err) {
			writeErr(w, http.StatusConflict, req.Name+" already exists")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": created, "abs": filepath.Join(root, created)})
}
