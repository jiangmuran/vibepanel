package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/store"
	version_ "github.com/jiangmuran/vibepanel/internal/version"
)

// Installing, granting, enabling and removing plugins: docs/plugins.md §6.
//
// Every route here is behind the ordinary session, like every other settings
// route: a share token, an admin grant, a chat tools token and -- once they
// exist -- a plugin's own grant or token answer 401 to all of them. Nothing
// that installs, grants or enables a plugin is reachable with any credential
// but the owner's, which is the property that keeps a plugin from installing
// plugins.
//
// The install screen is plugins.Describe, a pure function over the manifest.
// This file reads it and never adds to it: a line the screen does not say is
// a permission the person was not told about.

func (s *Server) registerPluginRoutes(r chi.Router) {
	r.Get("/settings/plugins", s.handleListPlugins)
	// One route for both ways a plugin arrives, decided by the body's type: a
	// zip, or JSON naming a directory on this machine. Not two routes under
	// /settings/plugins/<word>, because every word is a valid plugin id and
	// would shadow a plugin called that.
	r.Post("/settings/plugins", s.handleAddPlugin)
	r.Get("/settings/plugin-themes", s.handlePluginThemes)
	r.Get("/settings/plugins/{pluginID}", s.handleGetPlugin)
	r.Delete("/settings/plugins/{pluginID}", s.handleDeletePlugin)
	r.Post("/settings/plugins/{pluginID}/install", s.handleInstallPlugin)
	r.Post("/settings/plugins/{pluginID}/enable", s.handleEnablePlugin)
	r.Post("/settings/plugins/{pluginID}/disable", s.handleDisablePlugin)
	r.Put("/settings/plugins/{pluginID}/caps", s.handlePutPluginCaps)
	r.Get("/settings/plugins/{pluginID}/settings", s.handleGetPluginSettings)
	r.Put("/settings/plugins/{pluginID}/settings/{key}", s.handlePutPluginSetting)
	r.Delete("/settings/plugins/{pluginID}/settings/{key}", s.handleDeletePluginSetting)
	r.Put("/settings/plugins/{pluginID}/secrets/{name}", s.handlePutPluginSecret)
	r.Delete("/settings/plugins/{pluginID}/secrets/{name}", s.handleDeletePluginSecret)
	r.Get("/settings/plugins/{pluginID}/export", s.handleExportPlugin)
}

// registerPluginThemeRoute serves the enabled themes as one stylesheet,
// outside /api, because index.html links it: a plugin theme has to be in the
// document before first paint for the same reason the pre-paint script is.
func (s *Server) registerPluginThemeRoute(r chi.Router) {
	r.With(s.RequireAuth).Get("/plugin-themes.css", s.handlePluginThemesCSS)
}

// pluginThemeRow is a theme as the picker lists it.
type pluginThemeRow struct {
	Plugin string       `json:"plugin"`
	Attr   string       `json:"attr"`
	Name   plugins.Text `json:"name"`
	Scheme string       `json:"scheme"`
}

// PluginRow is a plugin as the list shows it. Exported, like PluginDetail,
// only so the embedding flattens for TestTypeScriptRowsMatchWhatIsSent,
// which skips unexported embedded fields.
type PluginRow struct {
	store.Plugin
	Name        plugins.Text  `json:"name"`
	Version     string        `json:"version"`
	Description *plugins.Text `json:"description,omitempty"`
	Author      string        `json:"author,omitempty"`
	Rungs       plugins.Rungs `json:"rungs"`
	// Granted is the owner's decision; Wanted is what the manifest asks for.
	// The difference is drawn in amber on the card.
	Granted []string `json:"granted"`
	Wanted  []string `json:"wanted"`
	// Secrets is each secret the plugin needs and whether it is set.
	Secrets []pluginSecretRow `json:"secrets"`
	// LatestVersion is the newest stored version, which is the installed
	// one unless an upgrade arrived and has not been confirmed.
	LatestVersion int `json:"latestVersion"`
	// Problems are sentences for the card: a dropped section, a missing
	// secret, a panel too old. Empty is a plugin with nothing to say.
	Problems []plugins.Text  `json:"problems"`
	Theme    *pluginThemeRow `json:"theme,omitempty"`
	// Panels is where the plugin's frames mount, for the panel to draw its
	// tabs and sections from the list rather than from every detail.
	Panels []plugins.PanelSpec `json:"panels"`
}

type pluginSecretRow struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// PluginDetail is one plugin with everything the page draws.
type PluginDetail struct {
	PluginRow
	Manifest plugins.Manifest      `json:"manifest"`
	Versions []store.PluginVersion `json:"versions"`
	Screen   plugins.Screen        `json:"screen"`
	Dropped  []string              `json:"dropped"`
	Settings pluginSettingsBody    `json:"settings"`
}

// pluginSettingsBody is the schema and the values, for the form the panel
// draws. A secret's value is never here: `set` says whether one is stored.
type pluginSettingsBody struct {
	Fields []plugins.FieldSpec `json:"fields"`
	Values map[string]any      `json:"values"`
}

// pluginManifest is the manifest a plugin runs on: the installed version's,
// or the newest stored version's for one not yet installed.
func (s *Server) pluginManifest(ctx context.Context, p store.Plugin) (plugins.Manifest, int, []string, error) {
	v := p.InstalledVersion
	if v == 0 {
		versions, err := s.DB.ListPluginVersions(ctx, p.ID)
		if err != nil {
			return plugins.Manifest{}, 0, nil, err
		}
		if len(versions) == 0 {
			return plugins.Manifest{}, 0, nil, store.ErrNotFound
		}
		m, dropped := plugins.DecodeStored(versions[0].Manifest)
		return m, versions[0].Version, dropped, nil
	}
	row, err := s.DB.PluginVersionByNumber(ctx, p.ID, v)
	if err != nil {
		return plugins.Manifest{}, 0, nil, err
	}
	m, dropped := plugins.DecodeStored(row.Manifest)
	return m, v, dropped, nil
}

func (s *Server) pluginRowFor(ctx context.Context, p store.Plugin) (PluginRow, plugins.Manifest, []string, error) {
	m, _, dropped, err := s.pluginManifest(ctx, p)
	if err != nil {
		return PluginRow{}, plugins.Manifest{}, nil, err
	}
	// In dev mode the draft directory's manifest is what runs, so it is what
	// the card and the slots are drawn from; a draft that does not parse
	// leaves the stored manifest on screen with a problem line.
	if p.Dev && p.SourceDir != "" {
		if draft, _, derr := s.pluginRunningManifest(ctx, p); derr == nil {
			m = draft
		} else {
			dropped = append(dropped, "draft: "+strings.TrimPrefix(derr.Error(), store.ErrNotFound.Error()+": "))
		}
	}
	caps, err := s.DB.PluginCaps(ctx, p.ID)
	if err != nil {
		return PluginRow{}, plugins.Manifest{}, nil, err
	}
	granted := make([]string, 0, len(caps))
	for _, c := range caps {
		granted = append(granted, c.Cap)
	}
	set, err := s.DB.PluginSecretNames(ctx, p.ID)
	if err != nil {
		return PluginRow{}, plugins.Manifest{}, nil, err
	}
	versions, err := s.DB.ListPluginVersions(ctx, p.ID)
	if err != nil {
		return PluginRow{}, plugins.Manifest{}, nil, err
	}
	latest := 0
	if len(versions) > 0 {
		latest = versions[0].Version
	}
	row := PluginRow{Plugin: p, Name: m.Name, Version: m.Version, Description: m.Description, Author: m.Author,
		Rungs: m.Rungs(), Granted: granted, Wanted: m.AllCapabilities(), Secrets: []pluginSecretRow{},
		LatestVersion: latest, Problems: []plugins.Text{}, Panels: append([]plugins.PanelSpec{}, m.Panels...)}
	for _, name := range m.SecretNames() {
		_, ok := set[name]
		row.Secrets = append(row.Secrets, pluginSecretRow{Name: name, Set: ok})
		if !ok {
			row.Problems = append(row.Problems, plugins.Text{
				EN: "secret " + name + " is not set", ZH: "secret " + name + " 未填写"})
		}
	}
	for _, d := range dropped {
		row.Problems = append(row.Problems, plugins.Text{
			EN: "this panel no longer reads the plugin's " + d + " section; it runs without it",
			ZH: "这个面板不再读取插件的 " + d + " 部分；已略去运行"})
	}
	if m.Panel != "" {
		if c, err := plugins.ParseConstraint(m.Panel); err == nil && !c.Satisfied(version_.Version) {
			row.Problems = append(row.Problems, plugins.Text{
				EN: "needs panel " + m.Panel + "; this panel is " + plugins.PanelVersion(version_.Version),
				ZH: "需要面板 " + m.Panel + "，当前 " + plugins.PanelVersion(version_.Version)})
		}
	}
	if m.Unsandboxed != nil {
		if rg, err := plugins.ParseRange(m.Unsandboxed.Tested); err == nil && !rg.Within(version_.Version) {
			row.Problems = append(row.Problems, plugins.Text{
				EN: "tested on panel " + m.Unsandboxed.Tested + "; this panel is " + plugins.PanelVersion(version_.Version),
				ZH: "在面板 " + m.Unsandboxed.Tested + " 上测试过，当前 " + plugins.PanelVersion(version_.Version)})
		}
	}
	if m.Theme != nil {
		row.Theme = &pluginThemeRow{Plugin: p.ID, Attr: plugins.ThemeAttr(p.ID), Name: m.Theme.Name, Scheme: m.Theme.Scheme}
	}
	return row, m, dropped, nil
}

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.DB.ListPlugins(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// ?detail=1 is the plugins page's poll: every card's detail in the one
	// request, rather than the list and then one request per card every
	// five seconds.
	if r.URL.Query().Get("detail") == "1" {
		out := make([]PluginDetail, 0, len(list))
		for _, p := range list {
			d, err := s.pluginDetailFor(ctx, p)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				s.writeStoreErr(w, err)
				return
			}
			out = append(out, d)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out := make([]PluginRow, 0, len(list))
	for _, p := range list {
		row, _, _, err := s.pluginRowFor(ctx, p)
		if errors.Is(err, store.ErrNotFound) {
			// A row with no version: made and never given files. Shown so it
			// can be removed, rather than hidden.
			row = PluginRow{Plugin: p, Name: plugins.Text{EN: p.ID}, Granted: []string{}, Wanted: []string{},
				Secrets: []pluginSecretRow{}, Problems: []plugins.Text{{EN: "no version stored", ZH: "没有存储的版本"}},
				Panels: []plugins.PanelSpec{}}
			err = nil
		}
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	d, err := s.pluginDetailFor(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) pluginDetailFor(ctx context.Context, p store.Plugin) (PluginDetail, error) {
	row, m, dropped, err := s.pluginRowFor(ctx, p)
	if err != nil {
		return PluginDetail{}, err
	}
	versions, err := s.DB.ListPluginVersions(ctx, p.ID)
	if err != nil {
		return PluginDetail{}, err
	}
	// The screen for the newest version, against what is granted today: on
	// a first install nothing is, and every box is ticked; on an upgrade the
	// boxes are what the owner decided, and a new line is drawn unticked.
	screen := plugins.Describe(m, row.Granted, version_.Version)
	if p.InstalledVersion == 0 {
		screen = plugins.Describe(m, nil, version_.Version)
	}
	if len(versions) > 0 && versions[0].Version != p.InstalledVersion && p.InstalledVersion != 0 {
		// An upgrade waiting: describe the newest, with the current grants.
		newest, _ := plugins.DecodeStored(versions[0].Manifest)
		screen = plugins.Describe(newest, row.Granted, version_.Version)
	}
	settings, err := s.pluginSettingsBody(ctx, p.ID, m)
	if err != nil {
		return PluginDetail{}, err
	}
	if dropped == nil {
		dropped = []string{}
	}
	return PluginDetail{PluginRow: row, Manifest: m, Versions: versions, Screen: screen, Dropped: dropped,
		Settings: settings}, nil
}

// handleAddPlugin takes a zip, or JSON naming a directory, and stores it as
// a version: the first version of a new plugin, or the next version of one
// that exists. Nothing is installed or enabled here; the answer carries the
// screen, and the install route is the confirmation.
func (s *Server) handleAddPlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	var b plugins.Bundle
	var sourceDir string
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		var req struct {
			Path string `json:"path"`
		}
		if !decode(w, r, &req) {
			return
		}
		dir, err := filepath.Abs(strings.TrimSpace(req.Path))
		if err != nil || req.Path == "" {
			writeErr(w, http.StatusBadRequest, "path is a directory on this machine")
			return
		}
		b, err = plugins.ReadDir(dir)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		sourceDir = dir
	default:
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, plugins.MaxArchiveBytes))
		if err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("an archive is at most %d MiB", plugins.MaxArchiveBytes>>20))
			return
		}
		b, err = plugins.ReadArchive(data)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	p, created, err := s.storePluginBundle(ctx, u.ID, b, sourceDir, "")
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	d, err := s.pluginDetailFor(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.imported", u.Username, s.clientIP(r), b.Manifest.ID+" "+b.Manifest.Version+" "+b.Hash()[:12])
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"plugin": d, "ignored": b.Ignored, "created": created})
}

// storePluginBundle is AddPluginBundle on the server's database.
func (s *Server) storePluginBundle(ctx context.Context, userID string, b plugins.Bundle, sourceDir, note string) (store.Plugin, bool, error) {
	return AddPluginBundle(ctx, s.DB, userID, b, sourceDir, note)
}

// AddPluginBundle stores a bundle as the next version of its plugin,
// creating the row when the id is new. A bundle whose hash equals the newest
// version's stores nothing and returns that version. Shared with the CLI,
// which opens the database directly.
func AddPluginBundle(ctx context.Context, db *store.DB, userID string, b plugins.Bundle, sourceDir, note string) (store.Plugin, bool, error) {
	p, err := db.PluginByID(ctx, b.Manifest.ID)
	created := false
	if errors.Is(err, store.ErrNotFound) {
		if p, err = db.CreatePlugin(ctx, b.Manifest.ID, userID, sourceDir); err != nil {
			return store.Plugin{}, false, err
		}
		created = true
	} else if err != nil {
		return store.Plugin{}, false, err
	} else if sourceDir != "" && p.SourceDir != sourceDir {
		if err := db.UpdatePluginSource(ctx, p.ID, sourceDir, p.Dev); err != nil {
			return store.Plugin{}, false, err
		}
	}
	versions, err := db.ListPluginVersions(ctx, p.ID)
	if err != nil {
		return store.Plugin{}, false, err
	}
	if len(versions) > 0 && versions[0].Hash == b.Hash() {
		p, err = db.PluginByID(ctx, p.ID)
		return p, created, err
	}
	files := make([]store.NewPluginFile, 0, len(b.Files))
	for _, f := range b.Files {
		files = append(files, store.NewPluginFile{Path: f.Path, ContentType: f.ContentType, Data: f.Data})
	}
	if _, err := db.AddPluginVersion(ctx, p.ID, store.NewPluginVersion{Manifest: b.Raw, Note: note, Hash: b.Hash(), Files: files}); err != nil {
		return store.Plugin{}, false, err
	}
	p, err = db.PluginByID(ctx, p.ID)
	return p, created, err
}

// ErrPluginNeedsNewerPanel is an install refused because the manifest asks
// for a panel newer than this one.
var ErrPluginNeedsNewerPanel = errors.New("this plugin needs a newer panel")

// InstallPlugin is the confirmation, shared with the CLI: records the grants
// exactly as given, makes the version the one that runs, and enables the
// plugin when every secret it needs is set. Returns which secrets are
// missing, and whether this was a first install.
func InstallPlugin(ctx context.Context, db *store.DB, id string, version int, caps []string, by string) (
	missing []string, first bool, err error) {
	p, err := db.PluginByID(ctx, id)
	if err != nil {
		return nil, false, err
	}
	versions, err := db.ListPluginVersions(ctx, p.ID)
	if err != nil {
		return nil, false, err
	}
	if len(versions) == 0 {
		return nil, false, fmt.Errorf("%w: this plugin has no version to install", store.ErrNotFound)
	}
	target := versions[0]
	if version != 0 {
		found := false
		for _, v := range versions {
			if v.Version == version {
				target, found = v, true
			}
		}
		if !found {
			return nil, false, fmt.Errorf("%w: no such version", store.ErrNotFound)
		}
	}
	m, _ := plugins.DecodeStored(target.Manifest)
	if m.Panel != "" {
		if c, err := plugins.ParseConstraint(m.Panel); err == nil && !c.Satisfied(version_.Version) {
			return nil, false, fmt.Errorf("%w: needs %s, this panel is %s", ErrPluginNeedsNewerPanel, m.Panel, plugins.PanelVersion(version_.Version))
		}
	}
	granted, err := checkGrantedCaps(m, caps)
	if err != nil {
		return nil, false, err
	}
	if err := db.SetPluginCaps(ctx, p.ID, granted, by); err != nil {
		return nil, false, err
	}
	missing, err = missingPluginSecrets(ctx, db, p.ID, m)
	if err != nil {
		return nil, false, err
	}
	if err := db.InstallPluginVersion(ctx, p.ID, target.Version, len(missing) == 0); err != nil {
		return nil, false, err
	}
	return missing, p.InstalledVersion == 0, nil
}

// handleInstallPlugin is the confirmation: the owner has read the screen and
// pressed the button. It records the grants exactly as ticked, makes the
// version the one that runs, and enables it when every secret is set.
func (s *Server) handleInstallPlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	id := chi.URLParam(r, "pluginID")
	var req struct {
		Version int      `json:"version"`
		Caps    []string `json:"caps"`
	}
	if !decode(w, r, &req) {
		return
	}
	missing, first, err := InstallPlugin(ctx, s.DB, id, req.Version, req.Caps, u.Username)
	switch {
	case errors.Is(err, ErrPluginNeedsNewerPanel):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
		return
	case err != nil && strings.Contains(err.Error(), "is not a capability"):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		s.writeStoreErr(w, err)
		return
	}
	p, err := s.DB.PluginByID(ctx, id)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	caps, _ := s.DB.PluginCaps(ctx, id)
	names := make([]string, 0, len(caps))
	for _, c := range caps {
		names = append(names, c.Cap)
	}
	detail := fmt.Sprintf("%s v%d granted %s", p.ID, p.InstalledVersion, strings.Join(names, ","))
	if first {
		s.audit(ctx, "plugin.installed", u.Username, s.clientIP(r), detail)
	} else {
		s.audit(ctx, "plugin.updated", u.Username, s.clientIP(r), detail)
	}
	s.pluginsChanged()
	d, err := s.pluginDetailFor(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugin": d, "needsSecrets": missing})
}

// checkGrantedCaps keeps the request to what the manifest asks for: a grant
// the manifest never mentioned is not a tick on a screen that showed it.
func checkGrantedCaps(m plugins.Manifest, req []string) ([]string, error) {
	wanted := m.AllCapabilities()
	var out []string
	seen := map[string]bool{}
	for _, c := range req {
		if !contains(wanted, c) {
			return nil, fmt.Errorf("%q is not a capability this plugin asks for", c)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Server) missingPluginSecrets(ctx context.Context, id string, m plugins.Manifest) ([]string, error) {
	return missingPluginSecrets(ctx, s.DB, id, m)
}

func missingPluginSecrets(ctx context.Context, db *store.DB, id string, m plugins.Manifest) ([]string, error) {
	set, err := db.PluginSecretNames(ctx, id)
	if err != nil {
		return nil, err
	}
	missing := []string{}
	for _, name := range m.SecretNames() {
		if _, ok := set[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

func (s *Server) handleEnablePlugin(w http.ResponseWriter, r *http.Request) {
	s.setPluginEnabled(w, r, true)
}

func (s *Server) handleDisablePlugin(w http.ResponseWriter, r *http.Request) {
	s.setPluginEnabled(w, r, false)
}

func (s *Server) setPluginEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if enabled {
		if p.InstalledVersion == 0 {
			writeErr(w, http.StatusConflict, "this plugin has not been installed; read the install screen first")
			return
		}
		m, _, _, err := s.pluginManifest(ctx, p)
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		missing, err := s.missingPluginSecrets(ctx, p.ID, m)
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if len(missing) > 0 {
			writeErr(w, http.StatusConflict, "fill in these secrets first: "+strings.Join(missing, ", "))
			return
		}
	}
	if err := s.DB.SetPluginEnabled(ctx, p.ID, enabled); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if enabled {
		s.audit(ctx, "plugin.enabled", u.Username, s.clientIP(r), p.ID)
	} else {
		s.audit(ctx, "plugin.disabled", u.Username, s.clientIP(r), p.ID)
	}
	s.pluginsChanged()
	p, _ = s.DB.PluginByID(ctx, p.ID)
	d, err := s.pluginDetailFor(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeletePlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	id := chi.URLParam(r, "pluginID")
	if err := s.DB.DeletePlugin(ctx, id); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.removed", u.Username, s.clientIP(r), id)
	s.pluginsChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePutPluginCaps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	var req struct {
		Caps []string `json:"caps"`
	}
	if !decode(w, r, &req) {
		return
	}
	m, _, _, err := s.pluginManifest(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	caps, err := checkGrantedCaps(m, req.Caps)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.SetPluginCaps(ctx, p.ID, caps, u.Username); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.permissions_changed", u.Username, s.clientIP(r), p.ID+" "+strings.Join(caps, ","))
	s.pluginsChanged()
	d, err := s.pluginDetailFor(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ─── settings and secrets ─────────────────────────────────────────────────

func (s *Server) pluginSettingsBody(ctx context.Context, id string, m plugins.Manifest) (pluginSettingsBody, error) {
	body := pluginSettingsBody{Fields: []plugins.FieldSpec{}, Values: map[string]any{}}
	if m.Settings == nil {
		return body, nil
	}
	stored, err := s.DB.PluginSettings(ctx, id)
	if err != nil {
		return body, err
	}
	set, err := s.DB.PluginSecretNames(ctx, id)
	if err != nil {
		return body, err
	}
	body.Fields = m.Settings.Fields
	for _, f := range m.Settings.Fields {
		if f.Type == plugins.FieldSecret {
			_, ok := set[strings.ToUpper(f.Key)]
			body.Values[f.Key] = ok
			continue
		}
		body.Values[f.Key] = plugins.DefaultValue(f)
		if raw, ok := stored[f.Key]; ok {
			var v any
			if json.Unmarshal(raw, &v) == nil {
				if checked, err := plugins.CheckFieldValue(f, v); err == nil {
					body.Values[f.Key] = checked
				}
			}
		}
	}
	return body, nil
}

func (s *Server) handleGetPluginSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	m, _, _, err := s.pluginManifest(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	body, err := s.pluginSettingsBody(ctx, p.ID, m)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) pluginField(ctx context.Context, w http.ResponseWriter, r *http.Request) (store.Plugin, plugins.Manifest, plugins.FieldSpec, bool) {
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return store.Plugin{}, plugins.Manifest{}, plugins.FieldSpec{}, false
	}
	m, _, _, err := s.pluginManifest(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return store.Plugin{}, plugins.Manifest{}, plugins.FieldSpec{}, false
	}
	key := chi.URLParam(r, "key")
	if m.Settings != nil {
		for _, f := range m.Settings.Fields {
			if f.Key == key {
				return p, m, f, true
			}
		}
	}
	writeErr(w, http.StatusNotFound, "this plugin has no setting "+key)
	return store.Plugin{}, plugins.Manifest{}, plugins.FieldSpec{}, false
}

func (s *Server) handlePutPluginSetting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, _, f, ok := s.pluginField(ctx, w, r)
	if !ok {
		return
	}
	var req struct {
		Value any `json:"value"`
	}
	if !decode(w, r, &req) {
		return
	}
	if f.Type == plugins.FieldSecret {
		writeErr(w, http.StatusBadRequest, "a secret is set through /secrets/"+strings.ToUpper(f.Key))
		return
	}
	v, err := plugins.CheckFieldValue(f, req.Value)
	if err != nil {
		writeErr(w, http.StatusBadRequest, f.Key+" "+err.Error())
		return
	}
	raw, _ := json.Marshal(v)
	if err := s.DB.SetPluginSetting(ctx, p.ID, f.Key, raw); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.settings_changed", u.Username, s.clientIP(r), p.ID+" "+f.Key)
	s.pluginsChanged()
	writeJSON(w, http.StatusOK, map[string]any{"value": v})
}

func (s *Server) handleDeletePluginSetting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, _, f, ok := s.pluginField(ctx, w, r)
	if !ok {
		return
	}
	if err := s.DB.DeletePluginSetting(ctx, p.ID, f.Key); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.settings_changed", u.Username, s.clientIP(r), p.ID+" "+f.Key+" reset")
	s.pluginsChanged()
	w.WriteHeader(http.StatusNoContent)
}

// pluginSecretContext binds a sealed secret to its plugin and name, so a
// value copied onto another row opens nothing there.
func pluginSecretContext(id, name string) string { return "plugin-secret:" + id + ":" + name }

func (s *Server) handlePutPluginSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	m, _, _, err := s.pluginManifest(ctx, p)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	name := chi.URLParam(r, "name")
	if !contains(m.SecretNames(), name) {
		writeErr(w, http.StatusNotFound, "this plugin needs no secret called "+name)
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Value == "" || len(req.Value) > 4096 {
		writeErr(w, http.StatusBadRequest, "a secret is 1 to 4096 bytes")
		return
	}
	box, err := s.secretBox()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot open the secrets key: "+err.Error())
		return
	}
	if err := s.DB.SetPluginSecret(ctx, p.ID, name, box.Seal([]byte(req.Value), pluginSecretContext(p.ID, name))); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.secret_set", u.Username, s.clientIP(r), p.ID+" "+name)
	s.pluginsChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeletePluginSecret(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := currentUserFrom(r)
	id := chi.URLParam(r, "pluginID")
	name := chi.URLParam(r, "name")
	if err := s.DB.DeletePluginSecret(ctx, id, name); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(ctx, "plugin.secret_deleted", u.Username, s.clientIP(r), id+" "+name)
	s.pluginsChanged()
	w.WriteHeader(http.StatusNoContent)
}

// pluginSecretValue opens one secret, for the runtimes that hand it to a
// process or a source. Never for a frame.
func (s *Server) pluginSecretValue(ctx context.Context, id, name string) (string, error) {
	sealed, err := s.DB.PluginSecret(ctx, id, name)
	if err != nil {
		return "", err
	}
	box, err := s.secretBox()
	if err != nil {
		return "", err
	}
	v, err := box.Unseal(sealed, pluginSecretContext(id, name))
	if err != nil {
		return "", err
	}
	return string(v), nil
}

// ─── export ───────────────────────────────────────────────────────────────

func (s *Server) handleExportPlugin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.DB.PluginByID(ctx, chi.URLParam(r, "pluginID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	v := p.InstalledVersion
	if q := r.URL.Query().Get("version"); q != "" {
		if v, err = strconv.Atoi(q); err != nil || v < 1 {
			writeErr(w, http.StatusBadRequest, "version is a number")
			return
		}
	}
	if v == 0 {
		versions, err := s.DB.ListPluginVersions(ctx, p.ID)
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if len(versions) == 0 {
			writeErr(w, http.StatusNotFound, "no version to export")
			return
		}
		v = versions[0].Version
	}
	data, m, err := PluginArchive(ctx, s.DB, p.ID, v)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	name := p.ID + "-" + m.Version + ".zip"
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="plugin.zip"; filename*=UTF-8''`+rfc5987(name))
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// PluginArchive writes one stored version as a zip. Shared with the CLI.
func PluginArchive(ctx context.Context, db *store.DB, id string, v int) ([]byte, plugins.Manifest, error) {
	row, err := db.PluginVersionByNumber(ctx, id, v)
	if err != nil {
		return nil, plugins.Manifest{}, err
	}
	m, _ := plugins.DecodeStored(row.Manifest)
	list, err := db.PluginFiles(ctx, id, v)
	if err != nil {
		return nil, plugins.Manifest{}, err
	}
	files := make([]plugins.File, 0, len(list))
	for _, f := range list {
		_, data, err := db.PluginFileData(ctx, id, v, f.Path)
		if err != nil {
			return nil, plugins.Manifest{}, err
		}
		files = append(files, plugins.File{Path: f.Path, Data: data})
	}
	var buf bytes.Buffer
	if err := plugins.WriteArchive(&buf, row.Manifest, files); err != nil {
		return nil, plugins.Manifest{}, err
	}
	return buf.Bytes(), m, nil
}

// ─── themes ───────────────────────────────────────────────────────────────

// enabledThemes is every enabled plugin's theme, linted again on the way
// out: what is served is what was checked, from the stored file rather than
// from anything cached.
func (s *Server) enabledThemes(ctx context.Context) ([]pluginThemeRow, string, error) {
	list, err := s.DB.ListPlugins(ctx)
	if err != nil {
		return nil, "", err
	}
	var rows []pluginThemeRow
	var css strings.Builder
	for _, p := range list {
		if !p.Enabled || p.InstalledVersion == 0 {
			continue
		}
		m, _, _, err := s.pluginManifest(ctx, p)
		if err != nil || m.Theme == nil {
			continue
		}
		_, data, err := s.DB.PluginFileData(ctx, p.ID, p.InstalledVersion, m.Theme.File)
		if err != nil {
			continue
		}
		decls, err := plugins.LintTheme(data, p.ID)
		if err != nil {
			s.Log.Warn("plugin theme refused on the way out", "plugin", p.ID, "err", err)
			continue
		}
		rows = append(rows, pluginThemeRow{Plugin: p.ID, Attr: plugins.ThemeAttr(p.ID), Name: m.Theme.Name, Scheme: m.Theme.Scheme})
		css.WriteString(plugins.RenderTheme(p.ID, decls))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Plugin < rows[j].Plugin })
	if rows == nil {
		rows = []pluginThemeRow{}
	}
	return rows, css.String(), nil
}

func (s *Server) handlePluginThemes(w http.ResponseWriter, r *http.Request) {
	rows, _, err := s.enabledThemes(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handlePluginThemesCSS(w http.ResponseWriter, r *http.Request) {
	_, css, err := s.enabledThemes(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "the panel cannot reach its own database")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/css; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, "/* themes from installed plugins; docs/plugins.md §5 */\n"+css)
}

// pluginsChanged is where a change to the installed set is announced. The
// runtimes that exist later -- frames, services, processes -- re-read the
// table on this; today the browser re-reads the list on its next poll.
func (s *Server) pluginsChanged() {
	s.prt.pluginsRev.Add(1)
	s.notifyState()
	s.pluginsChangedServices()
	s.ensurePluginDownloads(s.serviceContext())
}
