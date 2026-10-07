package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/plugins"
)

// New plugin (docs/plugins.md §9): a name and a template become a directory
// an agent can start in, registered as a project and already running in dev
// mode under this panel -- the page workflow with the panel itself as the
// preview.
//
// Two routes beside /settings/plugins rather than under it: every word is a
// valid plugin id, so /settings/plugins/new would shadow a plugin called new.

// PluginDirPrefix names a scaffolded plugin's directory and its project:
// plugin-<id>. The sidebar then says what the project is.
const PluginDirPrefix = "plugin-"

func (s *Server) registerPluginNewRoutes(r chi.Router) {
	r.Get("/settings/plugin-templates", s.handlePluginTemplates)
	r.Post("/settings/plugin-new", s.handleNewPlugin)
}

func (s *Server) handlePluginTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, plugins.Templates())
}

// newPluginResult is what the plugins page hands to the panel: the plugin,
// and the project the launch picker opens.
type newPluginResult struct {
	Plugin    PluginDetail `json:"plugin"`
	ProjectID string       `json:"projectId"`
	Dir       string       `json:"dir"`
}

func (s *Server) handleNewPlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	var req struct {
		Name     string `json:"name"`
		Template string `json:"template"`
		Path     string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "a name is required")
		return
	}
	pid := plugins.Slug(name)
	dir := strings.TrimSpace(expandHome(req.Path))
	if dir != "" && !filepath.IsAbs(dir) {
		writeErr(w, http.StatusBadRequest, "path must be an absolute directory")
		return
	}
	if dir == "" {
		dir = freeDir(s.pluginDevRoot(), PluginDirPrefix+pid)
	}
	dir = filepath.Clean(dir)
	if _, err := s.DB.PluginByID(ctx, pid); err == nil {
		writeErr(w, http.StatusConflict, "a plugin called "+pid+" already exists; pick another name")
		return
	}
	if err := plugins.Scaffold(dir, req.Template, pid, name); err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, plugins.ErrNotEmpty) && !strings.HasPrefix(err.Error(), "no template") {
			status = http.StatusInternalServerError
		}
		writeErr(w, status, err.Error())
		return
	}
	pages.GitInit(ctx, dir)
	b, err := plugins.ReadDir(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "the scaffold did not read back: "+err.Error())
		return
	}
	p, _, err := s.storePluginBundle(ctx, u.ID, b, dir, "scaffold")
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// Dev mode from the first second: the directory is what runs, so the
	// agent's first edit is already on the screen.
	if err := s.DB.UpdatePluginSource(ctx, p.ID, dir, true); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	projectID, err := s.projectForDir(ctx, PluginDirPrefix+pid, dir)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.created", u.Username, s.clientIP(r), pid+" from "+req.Template+" ("+dir+")")
	s.pluginsChanged()
	p, _ = s.DB.PluginByID(ctx, p.ID)
	d, err := s.pluginDetailFor(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newPluginResult{Plugin: d, ProjectID: projectID, Dir: dir})
}

// pluginDevRoot is where scaffolded plugins go: beside the checked-out
// versions, under the data directory, so a backup of the panel carries them.
func (s *Server) pluginDevRoot() string {
	return filepath.Join(s.Cfg.DataDir, "plugins", "dev")
}

// projectForDir is the project at dir: the one already there, an archived
// one brought back, or a new one called name. The same rule as opening a
// page: a directory is one project, however many times it is opened.
func (s *Server) projectForDir(ctx context.Context, name, dir string) (string, error) {
	dir = filepath.Clean(dir)
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if filepath.Clean(p.Path) == dir {
			return p.ID, nil
		}
	}
	if old, aerr := s.DB.ArchivedProjectAt(ctx, dir); aerr == nil {
		if rerr := s.DB.RestoreProject(ctx, old.ID); rerr != nil {
			return "", rerr
		}
		s.notifyState()
		return old.ID, nil
	}
	p, err := s.DB.CreateProject(ctx, id.New(), name, dir)
	if err != nil {
		return "", err
	}
	s.notifyState()
	return p.ID, nil
}
