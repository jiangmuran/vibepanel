package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/store"
	version_ "github.com/jiangmuran/vibepanel/internal/version"
)

// Rung 4, an unsandboxed module: docs/plugins.md §7. A plugin's `main.mjs`
// loaded as an ES module on the panel's own origin, after the SPA, as the
// owner. The panel cannot limit it, and the install screen says so in red;
// what this file does is bound the blast radius without pretending to bound
// the capability:
//
//   - nothing is served unless the panel-wide switch is on (off by default,
//     audited `plugins.unsandboxed`), the plugin is installed and enabled,
//     and the request carries the owner's session;
//   - the module is served from the installed version's files only, never
//     from a draft, because a draft is a directory an agent is writing into
//     and this is code that runs as the owner;
//   - the SPA loads nothing under `?safe=1`, and `vibepanel plugin disable
//     --all` turns everything off from a shell, so a module that breaks the
//     page has two ways back that need no working page.

const unsandboxedKey = "plugins.unsandboxed"

// unsandboxedAllowed is read on every request for a module, from the
// database: the switch is the off switch and must not be cached past it.
func (s *Server) unsandboxedAllowed(ctx context.Context) bool {
	v, err := s.DB.GetSetting(ctx, unsandboxedKey, "0")
	return err == nil && v == "1"
}

func (s *Server) registerPluginModuleRoutes(r chi.Router) {
	r.Get("/settings/plugin-unsandboxed", s.handleGetUnsandboxed)
	r.Put("/settings/plugin-unsandboxed", s.handlePutUnsandboxed)
	r.Get("/settings/plugin-modules", s.handlePluginModules)
}

// registerPluginCodeRoute serves a module's files, outside /api, under the
// session: a <script type=module> on the panel's origin carries the cookie.
func (s *Server) registerPluginCodeRoute(r chi.Router) {
	r.With(s.RequireAuth).Get("/plugin-code/{pluginID}/*", s.handlePluginCode)
}

func (s *Server) handleGetUnsandboxed(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": s.unsandboxedAllowed(r.Context())})
}

func (s *Server) handlePutUnsandboxed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "enabled is required")
		return
	}
	v, state := "0", "off"
	if *req.Enabled {
		v, state = "1", "on"
	}
	if err := s.DB.SetSetting(r.Context(), unsandboxedKey, v); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "plugins.unsandboxed", u.Username, s.clientIP(r), state)
	}
	s.pluginsChanged()
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *req.Enabled})
}

// pluginModule is one module the SPA loads.
type pluginModule struct {
	Plugin string       `json:"plugin"`
	URL    string       `json:"url"`
	Name   plugins.Text `json:"name"`
	// Tested is the panel range the module names, and Within whether this
	// panel is inside it: the card goes amber outside, and the SPA says so
	// when it loads the module anyway.
	Tested string `json:"tested"`
	Within bool   `json:"within"`
}

// handlePluginModules lists the modules the SPA should load: none with the
// switch off, so a page that never asks about the switch still loads
// nothing.
func (s *Server) handlePluginModules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []pluginModule{}
	if !s.unsandboxedAllowed(ctx) {
		writeJSON(w, http.StatusOK, out)
		return
	}
	list, err := s.DB.ListPlugins(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	for _, p := range list {
		m, ok := s.pluginModuleFor(ctx, p)
		if !ok {
			continue
		}
		within := true
		if rg, err := plugins.ParseRange(m.Unsandboxed.Tested); err == nil {
			within = rg.Within(version_.Version)
		}
		out = append(out, pluginModule{Plugin: p.ID, URL: "/plugin-code/" + p.ID + "/" + m.Unsandboxed.Entry,
			Name: m.Name, Tested: m.Unsandboxed.Tested, Within: within})
	}
	writeJSON(w, http.StatusOK, out)
}

// pluginModuleFor is the installed manifest of an enabled plugin with a
// module, or false. Never the draft: see the file comment.
func (s *Server) pluginModuleFor(ctx context.Context, p store.Plugin) (plugins.Manifest, bool) {
	if !p.Enabled || p.InstalledVersion == 0 {
		return plugins.Manifest{}, false
	}
	row, err := s.DB.PluginVersionByNumber(ctx, p.ID, p.InstalledVersion)
	if err != nil {
		return plugins.Manifest{}, false
	}
	m, _ := plugins.DecodeStored(row.Manifest)
	if m.Unsandboxed == nil {
		return plugins.Manifest{}, false
	}
	return m, true
}

// handlePluginCode serves one file of the installed version to the module:
// the entry, and whatever it imports from beside itself. Scripts and their
// maps only; a module's stylesheet is a string it injects.
func (s *Server) handlePluginCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.unsandboxedAllowed(ctx) {
		writeErr(w, http.StatusForbidden, "unsandboxed plugins are off; the switch is on the plugins page")
		return
	}
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if _, ok := s.pluginModuleFor(ctx, p); !ok {
		writeErr(w, http.StatusNotFound, "this plugin has no module, or is not enabled")
		return
	}
	rel := chi.URLParam(r, "*")
	if !plugins.ValidPath(rel) {
		http.NotFound(w, r)
		return
	}
	ct, data, err := s.DB.PluginFileData(ctx, p.ID, p.InstalledVersion, rel)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	switch ct {
	case "text/javascript; charset=utf-8", "application/json":
	default:
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
