package httpapi

import (
	"context"
	"encoding/json"
	"github.com/jiangmuran/vibepanel/internal/tz"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/tmux"
	"github.com/jiangmuran/vibepanel/internal/version"
)

// started is when this process came up, for the uptime the settings page shows.
var started = time.Now()

func (s *Server) registerSettingsRoutes(r chi.Router) {
	r.Get("/settings", s.handleSettings)
	r.Get("/settings/audit", s.handleAudit)
	r.Get("/settings/hooks", s.handleHooksStatus)
	r.Get("/settings/tokens", s.handleListTokens)
	r.Post("/settings/tokens", s.handleCreateToken)
	r.Delete("/settings/tokens/{tokenID}", s.handleDeleteToken)
	r.Post("/settings/hooks", s.handleHooksInstall)
	r.Put("/settings/hooks/agents", s.handlePutHookAgents)
	r.Post("/settings/restart", s.handleRestart)
	r.Post("/settings/tour", s.handleTourDone)
	r.Put("/settings/paste", s.handlePutPaste)
	r.Put("/settings/timezone", s.handlePutTimeZone)
	r.Get("/settings/env", s.handleGetEnv)
	r.Put("/settings/env", s.handlePutEnv)
	r.Get("/settings/tune", s.handleTuneStatus)
	r.Post("/settings/tune", s.handleTuneApply)
	r.Delete("/settings/hooks", s.handleHooksUninstall)
}

type settingsResponse struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
	Go      string `json:"go"`

	Uptime int64 `json:"uptime"`

	TmuxVersion string `json:"tmuxVersion"`
	TmuxSocket  string `json:"tmuxSocket"`
	// TmuxConfigStale means the running tmux server was started with a
	// different config from the one this binary carries.
	//
	// This is the half of an upgrade that nothing else can see. tmux reads its
	// `-f` file once, at start-server, and the panel never kills its server --
	// that is the premise of the project. So a new binary writes a new config
	// and the running server goes on using the old one, and both look
	// installed. `vibepanel doctor` says so, but nobody runs doctor after a
	// `systemctl restart`; the settings page is where a person looks.
	//
	// Not an error, and deliberately not phrased as one: applying it costs
	// every session on the socket, which is a decision for whoever reads it.
	TmuxConfigStale bool `json:"tmuxConfigStale"`
	// TmuxConfigUnknown means the running server predates the stamp, so the
	// question cannot be answered either way. Different from "it is current",
	// and a page that shows the two the same way is guessing.
	TmuxConfigUnknown bool `json:"tmuxConfigUnknown"`
	Sessions          int  `json:"sessions"`
	Attached          int  `json:"attached"`
	Viewers           int  `json:"viewers"`

	DataDir string `json:"dataDir"`
	DBBytes int64  `json:"dbBytes"`
	Addr    string `json:"addr"`
	URL     string `json:"url"`
	TLSMode string `json:"tlsMode"`
	// CertExpiry is unix seconds, absent when nothing is serving a certificate
	// or the mode does not have one.
	CertExpiry int64  `json:"certExpiry,omitempty"`
	Domain     string `json:"domain"`
	AllowAll   bool   `json:"allowAll"`

	PasskeysUsable bool   `json:"passkeysUsable"`
	PasskeyReason  string `json:"passkeyReason,omitempty"`
	Username       string `json:"username"`

	// TimeZone is what the panel calls a day. Empty means the machine's own.
	//
	// The offset goes with it because a page that wants to show the panel's
	// day rather than the viewer's needs one, and a name is a location fact
	// about the owner -- the same reason red line 8 refuses the hostname.
	TimeZone       string `json:"timezone"`
	TimeZoneOffset int    `json:"timezoneOffset"`
	// PanelDay is today on the panel's clock, which is the string every usage
	// query is keyed by. Shown so somebody choosing a zone can see the answer
	// change rather than trusting that it did.
	PanelDay string `json:"panelDay"`

	// Paste is where a screenshot pasted into a terminal lands, and what
	// happens to its path afterwards. "panel" | "session", and
	// "type" | "buffer" | "both".
	PasteDir  string `json:"pasteDir"`
	PasteThen string `json:"pasteThen"`

	// TourDone is whether the first-run tour has been dismissed.
	//
	// On this payload rather than a route of its own, because the frontend
	// already fetches it before it draws anything -- and a tour that arrives
	// one request after the panel is a tour that appears over a panel somebody
	// has started reading.
	TourDone bool `json:"tourDone"`
}

// tourKey is the settings row the first-run tour is remembered in.
const tourKey = "tour.done"

// Where a pasted screenshot goes, and what happens next.
//
// Defaults chosen from the complaint: the panel's own directory, not the
// project, because a picture pasted at an agent used to dirty a git tree and
// stay there. And typed at the prompt, which is what the panel did before and
// is what most agents can act on -- the buffer is the other half of the same
// answer for anybody whose agent reads it.
const (
	pasteDirKey  = "paste.dir"
	pasteThenKey = "paste.then"
	pasteDirDef  = "panel"
	pasteThenDef = "type"
)

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// handleTourDone puts the first-run tour away for good.
//
// A write with no body and no "undone": somebody who wants it back can be
// shown it again from the settings page, and an endpoint that toggles is an
// endpoint that gets called with the wrong value.
func (s *Server) handleTourDone(w http.ResponseWriter, r *http.Request) {
	// `?again=1` puts it back. Not a toggle: an endpoint that flips gets called
	// with the wrong value by whoever calls it twice, and these are two
	// deliberate actions -- closing it, and asking for it again from the
	// settings page.
	value := "1"
	if r.URL.Query().Get("again") == "1" {
		value = ""
	}
	if err := s.DB.SetSetting(r.Context(), tourKey, value); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "done": value == "1"})
}

// handlePutPaste sets where a pasted screenshot goes.
//
// The values are checked here rather than trusted, because they decide a
// directory to write into: an unknown one would fall through to whatever the
// reader's default happens to be, which is a setting that silently does
// something other than what the page shows.
func (s *Server) handlePutPaste(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir  string `json:"dir"`
		Then string `json:"then"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !oneOf(req.Dir, "panel", "session") || !oneOf(req.Then, "type", "buffer", "both") {
		writeErr(w, http.StatusBadRequest, "unknown paste setting")
		return
	}
	if err := s.DB.SetSetting(r.Context(), pasteDirKey, req.Dir); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.SetSetting(r.Context(), pasteThenKey, req.Then); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": req.Dir, "then": req.Then})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tv, _ := s.Tmux.Version(ctx)
	infos, _ := s.Tmux.List(ctx)

	stamp := s.Tmux.RunningConfigStamp(ctx)
	configUnknown := stamp == ""
	configStale := !configUnknown && stamp != tmux.ConfigStamp()

	// The whole database, not just the main file.
	//
	// The panel runs in journal_mode=WAL, so recent writes live in
	// `vibepanel.db-wal` until a checkpoint, and a checkpoint can be held off by
	// a long-lived read -- which this panel has, with four pooled connections
	// and a poller reading every two seconds. So `os.Stat(DBPath())` alone can
	// report well under what is on disk, at the moment somebody is reading it to
	// answer "why is this growing", and it disagreed with the runbook's own
	// `du -sh ~/.local/share/vibepanel` for a reason neither screen explained.
	//
	// -shm is small and included anyway: it is part of what `du` counts, and the
	// point of this number is to agree with `du`.
	var dbBytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if st, err := os.Stat(s.Cfg.DBPath() + suffix); err == nil {
			dbBytes += st.Size()
		}
	}

	// Read before the struct rather than inside it: a failure here is not a
	// reason to fail the whole page, and an unreadable settings row simply
	// means the tour has not been dismissed.
	tour, _ := s.DB.GetSetting(r.Context(), tourKey, "")
	pdir, _ := s.DB.GetSetting(r.Context(), pasteDirKey, pasteDirDef)
	pthen, _ := s.DB.GetSetting(r.Context(), pasteThenKey, pasteThenDef)

	out := settingsResponse{
		TourDone:  tour == "1",
		PasteDir:  pdir,
		PasteThen: pthen,

		Version: version.Version, Commit: version.Commit, Built: version.Date,
		Go:     runtime.Version(),
		Uptime: int64(time.Since(started).Seconds()),

		TmuxVersion: tv, TmuxSocket: s.Cfg.TmuxSocket,
		TmuxConfigStale: configStale, TmuxConfigUnknown: configUnknown,
		Sessions: len(infos), Attached: len(s.Manager.LiveIDs()),

		DataDir: s.Cfg.DataDir, DBBytes: dbBytes,
		Addr: s.Cfg.Addr, URL: s.Cfg.PublicURL(),
		TLSMode: string(s.Cfg.TLSMode), Domain: s.Cfg.Domain,

		PasskeysUsable: s.Cfg.PasskeysUsable(),
	}
	if s.CertExpiry != nil {
		if at := s.CertExpiry(); !at.IsZero() {
			out.CertExpiry = at.Unix()
		}
	}
	if s.Hub != nil {
		out.Viewers = s.Hub.Connections()
	}
	if s.Auth != nil {
		out.AllowAll = len(s.Auth.Allow) == 0
	}
	// Same code the login page gets, and the settings page now shows the
	// passkey section either way: hiding it when it cannot work is why
	// somebody went looking for a feature that was on screen the whole time
	// on a machine configured differently.
	out.PasskeyReason = s.Cfg.PasskeyBlocker()
	out.TimeZone, _ = s.DB.GetSetting(r.Context(), TimeZoneKey, "")
	out.TimeZoneOffset = zoneOffsetMinutes(s.loc(r.Context()))
	out.PanelDay = s.today(r.Context())
	if u, ok := currentUserFrom(r); ok {
		out.Username = u.Username
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.DB.RecentAudit(r.Context(), 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(entries))
}

// scriptPath installs the reporter if needed and returns where it lives.
func (s *Server) scriptPath() (string, error) {
	return hooks.InstallScript(filepath.Join(s.Cfg.DataDir, "hooks"))
}

// hookStatusResponse is the hooks package's answer plus the one thing it
// cannot know: which agents this panel has been asked to show.
//
// Embedded, so every field of hooks.Status is still sent at the top level and
// wire.ts does not have to learn a new shape.
type hookStatusResponse struct {
	hooks.Status
	// AgentsShown is the setting, not what ends up on screen: an agent with
	// hooks installed is drawn whether or not it is in here, and the page is
	// where those two are put together.
	AgentsShown []string `json:"agentsShown"`
}

// agentsShown reads the setting, falling back to the default on anything it
// does not recognise.
//
// An unreadable row is not a reason to draw nothing: the reporting section
// with no rows in it reads as "this panel cannot install hooks", which is a
// worse answer than the default list.
func (s *Server) agentsShown(ctx context.Context) []string {
	raw, err := s.DB.GetSetting(ctx, reportingAgentsKey, "")
	if err != nil || raw == "" {
		return hookAgentsShownDefault
	}
	// A JSON list rather than a comma-separated one, because "" has to mean
	// "never set" and "[]" has to mean "none of them", and a comma list spells
	// both of those the same way -- so turning every row off would have come
	// back as the default list on the next page load.
	var stored []string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return hookAgentsShownDefault
	}
	// Order from hookAgents rather than from the stored list, so the rows do
	// not reshuffle when somebody ticks one back on.
	out := []string{}
	for _, agent := range hookAgents {
		if slices.Contains(stored, agent) {
			out = append(out, agent)
		}
	}
	return out
}

// hookStatus answers every route that reports the state of the hooks.
//
// The install and uninstall handlers return a status too, and the page replaces
// everything it has with it -- so a field only the GET carried disappeared from
// the page the moment somebody pressed a button.
func (s *Server) hookStatus(ctx context.Context, st hooks.Status) hookStatusResponse {
	return hookStatusResponse{
		Status:      s.withCodexReports(ctx, st),
		AgentsShown: s.agentsShown(ctx),
	}
}

func (s *Server) handleHooksStatus(w http.ResponseWriter, r *http.Request) {
	script, err := s.scriptPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, err := hooks.Inspect(script)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.hookStatus(r.Context(), st))
}

// handlePutHookAgents sets which agents the reporting section lists.
//
// Names are checked against the same list the install route takes, because an
// id nobody recognises would sit in the setting for good and hide a row that
// the page has no other way to bring back.
func (s *Server) handlePutHookAgents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agents []string `json:"agents"`
	}
	if !decode(w, r, &req) {
		return
	}
	for _, agent := range req.Agents {
		if !isHookAgent(agent) {
			writeErr(w, http.StatusBadRequest,
				"unknown agent "+agent+"; want "+strings.Join(hookAgents, ", "))
			return
		}
	}
	// Stored even when it is empty, and empty is a real answer: somebody who
	// runs one agent and installed its hooks from the CLI wants none of these
	// rows. The installed ones are still drawn.
	stored, err := json.Marshal(agentsInOrder(req.Agents))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.SetSetting(r.Context(), reportingAgentsKey, string(stored)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agentsShown": s.agentsShown(r.Context())})
}

// withCodexReports counts the running Codex sessions and how many of them a
// hook has reported for.
//
// Here because it is the one thing the settings page can show that settles
// whether Codex's hooks work: Codex runs them only once trusted with /hooks,
// and a hooks.json that is installed, trusted by config.toml's record and
// still silent -- a definition changed since it was trusted, a Codex too old
// for hooks -- looks fine in every file the panel can read.
func (s *Server) withCodexReports(ctx context.Context, st hooks.Status) hooks.Status {
	rows, err := s.DB.ListSessions(ctx)
	if err != nil || s.Detector == nil {
		return st
	}
	for _, row := range rows {
		if row.Exited || row.Command != "codex" {
			continue
		}
		st.CodexSessions++
		if !s.Detector.LastHook(row.ID).IsZero() {
			st.CodexReporting++
		}
	}
	return st
}

// hookAgents is every agent the panel can install hooks for, in the order the
// settings page offers them.
//
// One list. The install switch, the uninstall switch, the file a status row
// names, the error message and the "which of these do I want to see" setting
// were each going to grow their own copy of it, and the fifth one added in a
// hurry is how a panel ends up offering an agent it cannot install.
var hookAgents = []string{"claude", "codex", "kimi", "zcode", "opencode"}

// hookAgentsShownDefault is what the reporting section lists on a panel nobody
// has configured.
//
// Not every agent the panel knows. Five rows of install buttons on a machine
// running one agent is a settings page somebody stops reading, and the two
// newest are the ones most people do not have: 「设置里面可以隐藏/配置」. An
// agent whose hooks are actually installed is shown whatever this says -- the
// panel does not hide a file it has written.
var hookAgentsShownDefault = []string{"claude", "codex", "opencode"}

const reportingAgentsKey = "reporting.agents"

func isHookAgent(name string) bool {
	return slices.Contains(hookAgents, name)
}

// agentsInOrder is the list in hookAgents order with the duplicates gone, so
// what is stored is what is read back.
func agentsInOrder(names []string) []string {
	out := []string{}
	for _, agent := range hookAgents {
		if slices.Contains(names, agent) {
			out = append(out, agent)
		}
	}
	return out
}

// hookAgent reads which agent a hook request is about.
//
// Defaults to Claude, which is what the parameter-less request meant before
// Codex had a button of its own, and refuses anything else rather than quietly
// installing for whichever one is first in the code. This decides which file in
// somebody's home directory gets edited, so a value nobody recognises has to be
// an error and not a guess.
func hookAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		return "claude", true
	}
	if !isHookAgent(agent) {
		writeErr(w, http.StatusBadRequest,
			"unknown agent "+agent+"; want "+strings.Join(hookAgents, ", "))
		return "", false
	}
	return agent, true
}

func (s *Server) handleHooksInstall(w http.ResponseWriter, r *http.Request) {
	agent, ok := hookAgent(w, r)
	if !ok {
		return
	}
	script, err := s.scriptPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The user's own configuration file, either way. It is backed up first,
	// merged rather than replaced, and what the panel wrote stays recognisable
	// so that removing it later cannot take anybody else's hook with it.
	// opencode is the exception and the easy one: it auto-discovers every file
	// in its plugin directory, so installing writes a file that did not exist
	// rather than editing a document full of somebody's own settings.
	var st hooks.Status
	switch agent {
	case "codex":
		st, err = hooks.InstallCodex(script)
	case "kimi":
		st, err = hooks.InstallKimi(script)
	case "zcode":
		st, err = hooks.InstallZcode(script)
	case "opencode":
		if err = hooks.InstallOpencode(); err == nil {
			st, err = hooks.Inspect(script)
		}
	default:
		st, err = hooks.InstallClaude(script)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The snapshot's "states are being guessed" notice reads a cached answer;
	// without this it would keep telling the user to install hooks for up to a
	// TTL after they just did.
	s.forgetHookStatus()
	s.notifyState()
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "hooks.installed", u.Username, s.clientIP(r), hookTarget(agent, st))
	}
	writeJSON(w, http.StatusOK, s.hookStatus(r.Context(), st))
}

func (s *Server) handleHooksUninstall(w http.ResponseWriter, r *http.Request) {
	agent, ok := hookAgent(w, r)
	if !ok {
		return
	}
	script, err := s.scriptPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var st hooks.Status
	switch agent {
	case "codex":
		st, err = hooks.UninstallCodex(script)
	case "kimi":
		st, err = hooks.UninstallKimi(script)
	case "zcode":
		st, err = hooks.UninstallZcode(script)
	case "opencode":
		if err = hooks.UninstallOpencode(); err == nil {
			st, err = hooks.Inspect(script)
		}
	default:
		st, err = hooks.UninstallClaude(script)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.forgetHookStatus()
	s.notifyState()
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "hooks.removed", u.Username, s.clientIP(r), hookTarget(agent, st))
	}
	writeJSON(w, http.StatusOK, s.hookStatus(r.Context(), st))
}

// ─── API tokens ───────────────────────────────────────────────────────────

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.DB.ListAPITokens(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tokens))
}

type createTokenRequest struct {
	Name string `json:"name"`
}

// handleCreateToken mints one, and is the only time the token is ever readable.
//
// Stored as a hash, exactly like a session: a database that leaks must not hand
// over live credentials. Which means there is no "show it again" — the response
// to this request is the only copy, and the settings page says so before you
// press the button rather than after.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "api"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	u, ok := currentUserFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return
	}
	token, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A prefix that names the token without being a head start on guessing it:
	// 8 characters of a 43-character random string.
	prefix := token
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	rec, err := s.DB.CreateAPIToken(r.Context(), id.New(), auth.HashToken(token), prefix, name, u.ID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(r.Context(), "token.created", u.Username, s.clientIP(r), name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     token,
		"id":        rec.ID,
		"name":      rec.Name,
		"prefix":    rec.Prefix,
		"createdAt": rec.CreatedAt,
	})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	tokenID := chi.URLParam(r, "tokenID")
	if err := s.DB.DeleteAPIToken(r.Context(), tokenID); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "token.revoked", u.Username, s.clientIP(r), tokenID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// hookTarget names the file an audit entry is about.
//
// The audit log is read after the fact by somebody asking what this panel
// changed on their machine. Recording ~/.claude/settings.json for an edit to
// ~/.codex/config.toml sends them to the wrong file.
func hookTarget(agent string, st hooks.Status) string {
	switch agent {
	case "codex":
		return st.CodexPath
	case "kimi":
		return st.KimiPath
	case "zcode":
		return st.ZcodePath
	case "opencode":
		return st.OpencodePath
	default:
		return st.SettingsPath
	}
}

// handlePutTimeZone sets the zone every day boundary in the panel is measured
// in, and rebuilds the usage history in it.
//
// The rebuild is the whole reason this is not a two-line setter. A record's day
// is decided when the transcript is read and written into `usage_daily.day` as
// a `YYYY-MM-DD` string; every query afterwards is a string comparison. So
// changing the zone does not re-label anything on its own -- it leaves a table
// bucketed by yesterday's rule and a "today" computed by today's, which is a
// heatmap missing its last square and a spend tile reading zero on a day with
// numbers in it, with no error anywhere.
//
// Dropping the scan cursor is what fixes that: the next pass re-reads every
// transcript. It costs seconds of disk on a machine with a year of history,
// once, at a moment somebody chose.
func (s *Server) handlePutTimeZone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Zone string `json:"zone"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Zone)
	// Refused rather than stored and quietly ignored. A setting that shows one
	// thing and does another is the failure this whole file avoids.
	if !tz.Valid(name) {
		writeErr(w, http.StatusBadRequest, "not a time zone this build knows: "+name)
		return
	}

	ctx := r.Context()
	before, _ := s.DB.GetSetting(ctx, TimeZoneKey, "")
	if err := s.DB.SetSetting(ctx, TimeZoneKey, name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.zone.forget()

	rebuilt := int64(0)
	if before != name {
		// The scanner writes the labels, so it has to be told too. This is the
		// pair that was already wrong before anybody changed a zone:
		// Scanner.Loc existed and was never set, while the query side used the
		// process clock, so the two could disagree with nothing to say so.
		//
		// Through the ingester rather than by hand: the zone and the cursor
		// are one change, and setting either from this goroutine while a pass
		// is walking races it and leaves the transcripts that pass happens to
		// touch labelled in the old zone for good. Ingester.Rezone says why in
		// full. It waits for a running pass, which is a request that takes as
		// long as the pass does -- rare, deliberate, and honest.
		var (
			n   int64
			err error
		)
		if loc, lerr := tz.Load(name); lerr == nil && s.Tokens != nil {
			n, err = s.Tokens.Rezone(ctx, loc)
		} else {
			n, err = s.DB.ForgetEveryUsageFile(ctx)
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		rebuilt = n
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "timezone.changed", u.Username, s.clientIP(r), name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"zone":     name,
		"rebuilt":  rebuilt,
		"offset":   zoneOffsetMinutes(s.loc(ctx)),
		"nowLabel": s.today(ctx),
	})
}

// zoneOffsetMinutes is how far the panel's clock is from UTC right now.
//
// Minutes rather than a name, and this is the only zone fact that goes to a
// browser. Red line 8 refuses the hostname on the share surface because it
// says where the owner is; an IANA name says the same thing more precisely. An
// offset is what a page needs to render the panel's day rather than its own.
func zoneOffsetMinutes(loc *time.Location) int {
	_, off := time.Now().In(loc).Zone()
	return off / 60
}
