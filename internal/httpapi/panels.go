package httpapi

import (
	"errors"
	"net/http"
	"os"
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
	note, err := s.DB.SetNote(r.Context(), pid, req.Content)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	writeJSON(w, http.StatusOK, todo)
}

func (s *Server) handleDeleteTodo(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.DeleteTodo(r.Context(), chi.URLParam(r, "todoID")); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// emptyTodos keeps the compiler honest about the generic helper's use here.
var _ = func() []store.Todo { return emptyIfNil[store.Todo](nil) }
