package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/chat"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The chat bridge's HTTP surface: the settings page's API, the two doors an
// outside party may knock on, and what the hook handler does with what an
// agent said.
//
// Three kinds of route, and which middleware each sits under is the design:
//
//   - Everything under /api/chat that a signed-in owner uses is behind
//     RequireAuth like any other setting.
//   - POST /api/chat/hooks/{kind} is where an IM that calls back (飞书)
//     delivers events. It is unauthenticated at the panel's door because the
//     IM cannot sign in; the adapter verifies every request itself, and the
//     handler here does nothing but hand the request over. Nothing about the
//     panel is readable through it.
//   - GET /api/chat/tools/* is what the advanced mode's agent may read,
//     through the MCP server, with a token that exists only in this
//     process's memory and reaches exactly these GETs. Same shape as red
//     line 8: the capability is narrowed by the route list, not by a flag on
//     the account, so an agent holding the token cannot reach a session's
//     input, the settings, or anything else. TestAChatToolsTokenReachesOnlyTheseRoutes
//     is the list.

// AssistantKey is the settings row for the advanced mode's configuration.
const AssistantKey = "chat.assistant"

// AssistantConfig is how the advanced mode is set up, as stored.
type AssistantConfig struct {
	Enabled bool `json:"enabled"`
	// Harness is "claude" or "codex".
	Harness string `json:"harness"`
	Model   string `json:"model"`
	// ProfileID names the launch profile whose environment the harness
	// runs with: an API key, a gateway URL. The existing secret handling
	// covers it; nothing is stored twice.
	ProfileID      string  `json:"profileId"`
	MaxTurns       int     `json:"maxTurns"`
	BudgetUSD      float64 `json:"budgetUsd"`
	TimeoutSeconds int     `json:"timeoutSeconds"`
}

func (c AssistantConfig) validate() error {
	switch c.Harness {
	case "", "claude", "codex":
	default:
		return fmt.Errorf("harness %q is not claude or codex", c.Harness)
	}
	if c.MaxTurns < 0 || c.MaxTurns > 50 {
		return fmt.Errorf("maxTurns %d is not 0..50", c.MaxTurns)
	}
	if c.BudgetUSD < 0 || c.BudgetUSD > 1000 {
		return fmt.Errorf("budget %v is not 0..1000", c.BudgetUSD)
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 900 {
		return fmt.Errorf("timeout %d is not 0..900 seconds", c.TimeoutSeconds)
	}
	if len(c.Model) > 100 {
		return fmt.Errorf("model name too long")
	}
	return nil
}

// AssistantBuilder makes the advanced mode's brain from its configuration,
// the launch profile's environment and the tools token. Set by the
// entrypoint; nil means the advanced mode is unavailable and the page says
// so.
type AssistantBuilder func(cfg AssistantConfig, env []string, toolsToken string) (chat.Assistant, error)

// chatTools holds the per-process token the MCP server presents.
type chatTools struct {
	mu    sync.Mutex
	token string
}

// ChatToolsToken returns the token, making it on first use. Never stored:
// it is minted for this process and dies with it, so a backup of the
// database holds nothing that reaches these routes.
func (s *Server) ChatToolsToken() (string, error) {
	s.chatTools.mu.Lock()
	defer s.chatTools.mu.Unlock()
	if s.chatTools.token == "" {
		t, err := auth.NewToken()
		if err != nil {
			return "", err
		}
		s.chatTools.token = t
	}
	return s.chatTools.token, nil
}

// StartChat builds the bridge from what the server already has and runs it
// until ctx ends. Called once by serve, after the adapters' packages have
// registered themselves and Shooter / NewAssistant are set. A panel whose
// secret key cannot be opened runs without a bridge and says so on the page.
func (s *Server) StartChat(ctx context.Context) error {
	box, err := s.secretBox()
	if err != nil {
		return err
	}
	s.Chat = chat.New(chat.Deps{
		DB: s.DB, Term: ChatTerminal(s.Tmux), Box: box, Log: s.Log,
		PublicURL: s.Cfg.PublicURL,
		Zone:      func() *time.Location { return s.loc(ctx) },
		Shot:      s.Shooter,
		Audit:     func(ctx context.Context, event, detail string) { s.audit(ctx, event, "chat", "", detail) },
		PastedDir: filepath.Join(s.Cfg.DataDir, "pasted"),
	})
	s.Chat.Start(ctx)
	if err := s.RebuildAssistant(ctx); err != nil {
		s.Log.Warn("chat assistant", "err", err)
	}
	return nil
}

func (s *Server) registerChatRoutes(r chi.Router) {
	r.Get("/chat", s.handleChatSettings)
	r.Put("/chat/channels/{kind}", s.handlePutChatChannel)
	r.Delete("/chat/channels/{kind}", s.handleDeleteChatChannel)
	r.Post("/chat/channels/{kind}/test", s.handleTestChatChannel)
	r.Post("/chat/channels/{kind}/login", s.handleChatLoginStart)
	r.Get("/chat/channels/{kind}/login/{id}", s.handleChatLoginStatus)
	r.Post("/chat/channels/{kind}/login/{id}/code", s.handleChatLoginCode)
	r.Post("/chat/pair", s.handleChatPair)
	r.Patch("/chat/peers/{channel}/{peer}", s.handlePatchChatPeer)
	r.Delete("/chat/peers/{channel}/{peer}", s.handleDeleteChatPeer)
	r.Put("/chat/routes", s.handlePutChatRoutes)
	r.Post("/chat/routes/preview", s.handleChatRoutePreview)
	r.Put("/chat/keys", s.handlePutChatTools)
	r.Put("/chat/assistant", s.handlePutChatAssistant)
	r.Put("/chat/lang", s.handlePutChatLang)
	r.Get("/chat/log", s.handleChatLog)
}

// registerChatPublicRoutes mounts the two doors described at the top of the
// file. Outside RequireAuth on purpose; see there.
func (s *Server) registerChatPublicRoutes(r chi.Router) {
	// GET as well as POST: an IM that verifies a callback URL with a GET
	// (企业微信's echostr) would otherwise be told 405 by the router before
	// its adapter could answer. The adapter refuses what it does not expect.
	r.Post("/chat/hooks/{kind}", s.handleChatWebhook)
	r.Get("/chat/hooks/{kind}", s.handleChatWebhook)
	r.Route("/chat/tools", func(r chi.Router) {
		r.Use(s.requireChatToolsToken)
		r.Get("/sessions", s.handleChatToolSessions)
		r.Get("/sessions/{handle}/messages", s.handleChatToolMessages)
		r.Get("/sessions/{handle}/screen", s.handleChatToolScreen)
		r.Get("/usage", s.handleChatToolUsage)
		r.Get("/projects", s.handleChatToolProjects)
	})
}

// ─── settings page ─────────────────────────────────────────────────────────

type chatFactoryView struct {
	Kind    string       `json:"kind"`
	Label   string       `json:"label"`
	Fields  []chat.Field `json:"fields"`
	Login   bool         `json:"login"`
	Webhook bool         `json:"webhook"`
}

type chatChannelView struct {
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	// Configured means a row exists; for a QR channel it means signed in.
	Configured bool `json:"configured"`
	// Values carries the non-secret fields; SecretSet says which secret
	// fields hold something, never what.
	Values     map[string]string `json:"values"`
	SecretSet  map[string]bool   `json:"secretSet"`
	Health     chat.Health       `json:"health"`
	WebhookURL string            `json:"webhookUrl,omitempty"`
	UpdatedAt  int64             `json:"updatedAt"`
}

type chatSessionView struct {
	ID      string `json:"id"`
	Handle  int    `json:"handle"`
	Title   string `json:"title"`
	Project string `json:"project"`
	State   string `json:"state"`
	Tool    string `json:"tool"`
}

type chatSettingsView struct {
	Available bool                        `json:"available"`
	Factories []chatFactoryView           `json:"factories"`
	Channels  []chatChannelView           `json:"channels"`
	Peers     []store.ChatPeer            `json:"peers"`
	Routes    chat.Routes                 `json:"routes"`
	Tools     map[string]chat.ToolProfile `json:"tools"`
	Assistant AssistantConfig             `json:"assistant"`
	// AssistantAvailable is whether this build can run one at all.
	AssistantAvailable bool              `json:"assistantAvailable"`
	Lang               string            `json:"lang"`
	Dropped            int64             `json:"dropped"`
	Sessions           []chatSessionView `json:"sessions"`
	Projects           []store.Project   `json:"projects"`
	SpendToday         float64           `json:"spendToday"`
	CallsToday         int               `json:"callsToday"`
}

func (s *Server) assistantConfig(ctx context.Context) AssistantConfig {
	cfg := AssistantConfig{Harness: "claude", MaxTurns: 6, BudgetUSD: 2, TimeoutSeconds: 120}
	raw, err := s.DB.GetSetting(ctx, AssistantKey, "")
	if err != nil || raw == "" {
		return cfg
	}
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg
}

func (s *Server) handleChatSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := chatSettingsView{Available: s.Chat != nil, Factories: []chatFactoryView{}, Channels: []chatChannelView{},
		Peers: []store.ChatPeer{}, Sessions: []chatSessionView{}, Projects: []store.Project{},
		AssistantAvailable: s.NewAssistant != nil, Lang: "zh"}
	if s.Chat == nil {
		writeJSON(w, http.StatusOK, view)
		return
	}
	factories := chat.Factories()
	byKind := map[string]chat.Factory{}
	for _, f := range factories {
		byKind[f.Kind] = f
		view.Factories = append(view.Factories, chatFactoryView{Kind: f.Kind, Label: f.Label, Fields: emptyIfNil(f.Fields), Login: f.Login, Webhook: f.Webhook})
	}
	healths := map[string]chat.Health{}
	for _, h := range s.Chat.Healths(ctx) {
		healths[h.Kind] = h
	}
	rows, err := s.DB.ListChatChannels(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, row := range rows {
		f, known := byKind[row.Kind]
		cv := chatChannelView{Kind: row.Kind, Enabled: row.Enabled, Configured: true, Values: map[string]string{},
			SecretSet: map[string]bool{}, Health: healths[row.Kind], UpdatedAt: row.UpdatedAt}
		if _, cfg, err := s.Chat.ReadChannel(ctx, row.Kind); err == nil && known {
			anySecret := false
			for _, fld := range f.Fields {
				v := cfg.Values[fld.Name]
				if fld.Secret {
					cv.SecretSet[fld.Name] = v != ""
					anySecret = anySecret || v != ""
				} else {
					cv.Values[fld.Name] = v
				}
			}
			if f.Login {
				// A QR channel is configured once its sign-in stored a
				// credential; the factory says which field that is.
				cv.Configured = anySecret
			}
		}
		if known && f.Webhook {
			cv.WebhookURL = strings.TrimRight(s.Cfg.PublicURL(), "/") + "/api/chat/hooks/" + row.Kind
		}
		view.Channels = append(view.Channels, cv)
	}
	if peers, err := s.DB.ListChatPeers(ctx); err == nil {
		view.Peers = emptyIfNil(peers)
	}
	raw, _ := s.DB.GetSetting(ctx, chat.RoutesKey, "")
	view.Routes = chat.ParseRoutes(raw)
	if view.Routes.Rules == nil {
		view.Routes.Rules = []chat.Rule{}
	}
	raw, _ = s.DB.GetSetting(ctx, chat.ToolsKey, "")
	view.Tools = chat.ParseTools(raw)
	view.Assistant = s.assistantConfig(ctx)
	if l, err := s.DB.GetSetting(ctx, chat.LangKey, ""); err == nil && (l == "en" || l == "zh") {
		view.Lang = l
	}
	view.Dropped = s.Chat.Dropped()
	if usd, calls, err := s.DB.ChatSpend(ctx, dayIn(s.loc(ctx), time.Now())); err == nil {
		view.SpendToday, view.CallsToday = usd, calls
	}
	if projects, err := s.DB.ListProjects(ctx); err == nil {
		view.Projects = emptyIfNil(projects)
		names := map[string]string{}
		for _, p := range projects {
			names[p.ID] = p.Name
		}
		if sessions, err := s.DB.ListSessions(ctx); err == nil {
			for _, row := range sessions {
				if !chat.Addressable(row) {
					continue
				}
				h, _ := s.Chat.Handle(ctx, row.ID)
				view.Sessions = append(view.Sessions, chatSessionView{
					ID: row.ID, Handle: h, Title: row.Title, Project: names[row.ProjectID],
					State: string(row.State), Tool: chat.AgentFor(row.LaunchCommand, row.Command),
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, view)
}

type putChannelRequest struct {
	Enabled bool              `json:"enabled"`
	Values  map[string]string `json:"values"`
}

func (s *Server) requireChat(w http.ResponseWriter) bool {
	if s.Chat == nil {
		writeErr(w, http.StatusServiceUnavailable, "the chat bridge is not running")
		return false
	}
	return true
}

func (s *Server) handlePutChatChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	f, ok := chat.FactoryFor(kind)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such chat adapter")
		return
	}
	var req putChannelRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	// Merge: a secret sent empty keeps what is stored, the same rule launch
	// profiles follow, because the page never receives secrets to send back.
	cfg := chat.ChannelConfig{Values: map[string]string{}}
	if _, prev, err := s.Chat.ReadChannel(ctx, kind); err == nil {
		cfg = prev
	}
	for _, fld := range f.Fields {
		v, sent := req.Values[fld.Name]
		v = strings.TrimSpace(v)
		if len(v) > 4096 {
			writeErr(w, http.StatusBadRequest, fld.Name+" is too long")
			return
		}
		if fld.Secret && sent && v == "" {
			continue
		}
		if sent {
			cfg.Values[fld.Name] = v
		}
	}
	if f.Login && req.Enabled && !anySecretSet(f, cfg) {
		writeErr(w, http.StatusBadRequest, "sign in first")
		return
	}
	if err := s.Chat.WriteChannel(ctx, kind, req.Enabled, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, _, _ := s.currentUser(r)
	s.audit(ctx, "chat.channel", user.Username, s.clientIP(r), fmt.Sprintf("%s enabled=%v", kind, req.Enabled))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteChatChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	if err := s.Chat.RemoveChannel(r.Context(), kind); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	user, _, _ := s.currentUser(r)
	s.audit(r.Context(), "chat.channel", user.Username, s.clientIP(r), kind+" removed")
	w.WriteHeader(http.StatusNoContent)
}

// anySecretSet says whether a channel holds any of its factory's secret
// fields, which for a QR channel is whether the sign-in happened.
func anySecretSet(f chat.Factory, cfg chat.ChannelConfig) bool {
	for _, fld := range f.Fields {
		if fld.Secret && cfg.Values[fld.Name] != "" {
			return true
		}
	}
	return false
}

type testChannelRequest struct {
	PeerID string `json:"peerId"`
}

type testChannelResponse struct {
	Sent  int    `json:"sent"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleTestChatChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	var req testChannelRequest
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	lang, _ := s.DB.GetSetting(r.Context(), chat.LangKey, "")
	text := "vibepanel：这是一条测试消息。"
	if lang == "en" {
		text = "vibepanel: this is a test message."
	}
	sent, err := s.Chat.TestSend(r.Context(), chi.URLParam(r, "kind"), req.PeerID, text)
	resp := testChannelResponse{Sent: sent}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleChatLoginStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	la, err := s.Chat.LoginAdapter(kind)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	login, err := la.StartLogin(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, login)
}

func (s *Server) handleChatLoginStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	la, err := s.Chat.LoginAdapter(kind)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	login, err := la.LoginStatus(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if login.Status == "done" && len(login.Credentials) > 0 {
		var creds map[string]string
		if err := json.Unmarshal(login.Credentials, &creds); err != nil {
			writeErr(w, http.StatusBadGateway, "credentials did not parse")
			return
		}
		// Signing in replaces the channel's configuration but keeps the
		// adapter's state, so a re-scan does not replay the poll cursor.
		cfg := chat.ChannelConfig{Values: creds}
		if _, prev, err := s.Chat.ReadChannel(r.Context(), kind); err == nil {
			cfg.State = prev.State
		}
		if err := s.Chat.WriteChannel(r.Context(), kind, true, cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		user, _, _ := s.currentUser(r)
		s.audit(r.Context(), "chat.channel", user.Username, s.clientIP(r), kind+" signed in")
	}
	writeJSON(w, http.StatusOK, login)
}

func (s *Server) handleChatLoginCode(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	la, err := s.Chat.LoginAdapter(chi.URLParam(r, "kind"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := la.SubmitCode(r.Context(), chi.URLParam(r, "id"), strings.TrimSpace(req.Code)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChatPair(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, err := s.Chat.Pair(r.Context(), req.Code)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no pending person has that code")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type patchPeerRequest struct {
	Mode   *string `json:"mode"`
	Status *string `json:"status"`
}

func (s *Server) handlePatchChatPeer(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	var req patchPeerRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	channel, peer := chi.URLParam(r, "channel"), chi.URLParam(r, "peer")
	if req.Mode != nil {
		if *req.Mode != store.ModeNormal && *req.Mode != store.ModeAdvanced {
			writeErr(w, http.StatusBadRequest, "mode is normal or advanced")
			return
		}
		if err := s.Chat.SetPeerMode(ctx, channel, peer, *req.Mode); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	if req.Status != nil {
		if *req.Status != store.PeerPaired && *req.Status != store.PeerBlocked {
			writeErr(w, http.StatusBadRequest, "status is paired or blocked")
			return
		}
		if err := s.Chat.SetPeerStatus(ctx, channel, peer, *req.Status); err != nil {
			s.writeStoreErr(w, err)
			return
		}
	}
	p, err := s.DB.GetChatPeer(ctx, channel, peer)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteChatPeer(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	if err := s.DB.DeleteChatPeer(r.Context(), chi.URLParam(r, "channel"), chi.URLParam(r, "peer")); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	user, _, _ := s.currentUser(r)
	s.audit(r.Context(), "chat.peer", user.Username, s.clientIP(r), chi.URLParam(r, "channel")+":"+chi.URLParam(r, "peer")+" removed")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePutChatRoutes(w http.ResponseWriter, r *http.Request) {
	var routes chat.Routes
	if !decode(w, r, &routes) {
		return
	}
	if err := routes.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := json.Marshal(routes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.DB.SetSetting(r.Context(), chat.RoutesKey, string(raw)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, chat.ParseRoutes(string(raw)))
}

type routePreviewRequest struct {
	SessionID string `json:"sessionId"`
}

type routePreviewResponse struct {
	Decision chat.Decision `json:"decision"`
	Change   chat.Change   `json:"change"`
	// Peers is who would be told, by "channel:peer", after the rule's
	// destinations are intersected with who is paired and not muted.
	Peers []string `json:"peers"`
}

func (s *Server) handleChatRoutePreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	var req routePreviewRequest
	if !decode(w, r, &req) {
		return
	}
	d, c, who, ok := s.Chat.Preview(r.Context(), req.SessionID)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	writeJSON(w, http.StatusOK, routePreviewResponse{Decision: d, Change: c, Peers: who})
}

func (s *Server) handlePutChatTools(w http.ResponseWriter, r *http.Request) {
	var tools map[string]chat.ToolProfile
	if !decode(w, r, &tools) {
		return
	}
	if len(tools) > 50 {
		writeErr(w, http.StatusBadRequest, "too many profiles")
		return
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Refused out loud here, where a person is typing: a key with a space in
	// it is text send-keys would type, and "rm -rf /" is a valid argument.
	// ParseTools would drop such a profile silently and fall back to the
	// default, which is what made an earlier version of this accept it.
	for name, p := range tools {
		if len(name) == 0 || len(name) > 32 || !chat.ValidProfile(p) {
			writeErr(w, http.StatusBadRequest, "profile "+name+" has a key that is not a tmux key name")
			return
		}
	}
	parsed := chat.ParseTools(string(raw))
	raw, _ = json.Marshal(parsed)
	if err := s.DB.SetSetting(r.Context(), chat.ToolsKey, string(raw)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, parsed)
}

func (s *Server) handlePutChatAssistant(w http.ResponseWriter, r *http.Request) {
	var cfg AssistantConfig
	if !decode(w, r, &cfg) {
		return
	}
	if err := cfg.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if cfg.Harness == "" {
		cfg.Harness = "claude"
	}
	// Built before it is stored: a configuration the harness refuses is
	// answered 400 and nothing changes, rather than saved and off.
	if cfg.Enabled && s.NewAssistant != nil {
		if _, err := s.buildAssistant(r.Context(), cfg); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	raw, _ := json.Marshal(cfg)
	if err := s.DB.SetSetting(r.Context(), AssistantKey, string(raw)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.RebuildAssistant(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, _, _ := s.currentUser(r)
	s.audit(r.Context(), "chat.assistant", user.Username, s.clientIP(r), fmt.Sprintf("enabled=%v harness=%s", cfg.Enabled, cfg.Harness))
	writeJSON(w, http.StatusOK, cfg)
}

// RebuildAssistant makes the advanced mode's brain from the stored
// configuration and installs it in the bridge, or removes it.
func (s *Server) RebuildAssistant(ctx context.Context) error {
	if s.Chat == nil {
		return nil
	}
	cfg := s.assistantConfig(ctx)
	if !cfg.Enabled || s.NewAssistant == nil {
		s.Chat.SetAssistant(nil)
		return nil
	}
	a, err := s.buildAssistant(ctx, cfg)
	if err != nil {
		s.Chat.SetAssistant(nil)
		return err
	}
	s.Chat.SetAssistant(a)
	return nil
}

func (s *Server) buildAssistant(ctx context.Context, cfg AssistantConfig) (chat.Assistant, error) {
	var env []string
	if cfg.ProfileID != "" {
		profile, err := s.DB.GetLaunchProfile(ctx, cfg.ProfileID)
		if err != nil {
			return nil, fmt.Errorf("launch profile: %w", err)
		}
		env = store.LaunchEnv(&profile, nil)
	}
	token, err := s.ChatToolsToken()
	if err != nil {
		return nil, err
	}
	return s.NewAssistant(cfg, env, token)
}

func (s *Server) handlePutChatLang(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lang string `json:"lang"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Lang != "zh" && req.Lang != "en" {
		writeErr(w, http.StatusBadRequest, "lang is zh or en")
		return
	}
	if err := s.DB.SetSetting(r.Context(), chat.LangKey, req.Lang); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.Chat != nil {
		s.Chat.SetLang(req.Lang)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChatLog(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	entries, err := s.DB.RecentAuditPrefix(r.Context(), "chat.", n)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(entries))
}

// ─── the two doors ─────────────────────────────────────────────────────────

func (s *Server) handleChatWebhook(w http.ResponseWriter, r *http.Request) {
	if s.Chat == nil {
		http.NotFound(w, r)
		return
	}
	h := s.Chat.Webhook(chi.URLParam(r, "kind"))
	if h == nil {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}

func (s *Server) requireChatToolsToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want, err := s.ChatToolsToken()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "token unavailable")
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			s.auditFromOutside(r.Context(), "chat.tools.rejected", "", s.clientIP(r), "bad tools token")
			writeErr(w, http.StatusUnauthorized, "bad token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type toolSessionView struct {
	Handle    int    `json:"handle"`
	Title     string `json:"title"`
	Project   string `json:"project"`
	State     string `json:"state"`
	Kind      string `json:"kind,omitempty"`
	Tool      string `json:"tool"`
	ChangedAt int64  `json:"changedAt"`
}

// The tool views restate their fields, as the share snapshot does, so that a
// field added to store.Session is not handed to an agent by default. Paths,
// commands, tmux names and ids are not here on purpose: the agent addresses
// sessions by handle and nothing else.

func (s *Server) handleChatToolSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	ctx := r.Context()
	names := map[string]string{}
	if projects, err := s.DB.ListProjects(ctx); err == nil {
		for _, p := range projects {
			names[p.ID] = p.Name
		}
	}
	rows, err := s.DB.ListSessions(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []toolSessionView{}
	for _, row := range rows {
		if row.ArchivedAt != nil || row.Scratch || row.Exited {
			continue
		}
		h, err := s.Chat.Handle(ctx, row.ID)
		if err != nil {
			continue
		}
		v := toolSessionView{Handle: h, Title: row.Title, Project: names[row.ProjectID], State: string(row.State),
			Tool: chat.AgentFor(row.LaunchCommand, row.Command), ChangedAt: row.StateChangedAt}
		if last, ok, _ := s.DB.LatestSessionMessage(ctx, row.ID); ok && last.At >= row.StateChangedAt-1 {
			v.Kind = last.Kind
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) toolSession(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	h, err := strconv.Atoi(chi.URLParam(r, "handle"))
	if err != nil || h <= 0 {
		writeErr(w, http.StatusBadRequest, "handle is a number")
		return store.Session{}, false
	}
	id, ok, err := s.DB.SessionByHandle(r.Context(), h)
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "no such handle")
		return store.Session{}, false
	}
	row, err := s.DB.GetSession(r.Context(), id)
	if err != nil {
		s.writeStoreErr(w, err)
		return store.Session{}, false
	}
	return row, true
}

type toolMessageView struct {
	At   int64  `json:"at"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func (s *Server) handleChatToolMessages(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	row, ok := s.toolSession(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 {
		n = 20
	}
	msgs, err := s.DB.ListSessionMessages(r.Context(), row.ID, n)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []toolMessageView{}
	for _, m := range msgs {
		out = append(out, toolMessageView{At: m.At, Kind: m.Kind, Text: m.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleChatToolScreen(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	row, ok := s.toolSession(w, r)
	if !ok {
		return
	}
	text, err := s.Tmux.Screen(r.Context(), row.TmuxName, false)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "the pane could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": strings.TrimRight(text, "\n ")})
}

type toolUsageView struct {
	Days           int     `json:"days"`
	Started        int     `json:"started"`
	Waited         int     `json:"waited"`
	Finished       int     `json:"finished"`
	AssistantCalls int     `json:"assistantCalls"`
	AssistantUSD   float64 `json:"assistantUsd"`
}

func (s *Server) handleChatToolUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 1
	}
	if days > 31 {
		days = 31
	}
	loc := s.loc(ctx)
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	view := toolUsageView{Days: days}
	if bk, err := s.DB.CountSessionEvents(ctx, start.Unix(), store.EventScope{}); err == nil {
		view.Started, view.Waited, view.Finished = bk.Started, bk.Waited, bk.Finished
	}
	for i := 0; i < days; i++ {
		usd, calls, _ := s.DB.ChatSpend(ctx, dayShift(loc, now, -i))
		view.AssistantUSD += usd
		view.AssistantCalls += calls
	}
	writeJSON(w, http.StatusOK, view)
}

type toolProjectView struct {
	Name     string `json:"name"`
	Sessions int    `json:"sessions"`
	Waiting  int    `json:"waiting"`
	Working  int    `json:"working"`
}

func (s *Server) handleChatToolProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projects, err := s.DB.ListProjects(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessions, _ := s.DB.ListSessions(ctx)
	out := []toolProjectView{}
	for _, p := range projects {
		v := toolProjectView{Name: p.Name}
		for _, row := range sessions {
			if row.ProjectID != p.ID || row.ArchivedAt != nil || row.Scratch {
				continue
			}
			v.Sessions++
			switch row.State {
			case session.StateWaiting:
				v.Waiting++
			case session.StateWorking:
				v.Working++
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// ─── what the hook said ────────────────────────────────────────────────────

// promptEcho is how many seconds after a PermissionRequest its Notification
// twin may arrive and still be the same prompt. Measured at under a second
// on this machine; five leaves room for a hook runner under load without
// swallowing a second, real prompt a minute later.
const promptEcho = 5

// recordHookMessage keeps what an agent's hook document carried and tells
// the bridge. Called before the state is written, so the message is never
// newer than the state change it belongs to (the bridge compares the two).
func (s *Server) recordHookMessage(ctx context.Context, row store.Session, body []byte) {
	rep, ok := hooks.Extract(body)
	if !ok {
		return
	}
	tool := chat.AgentFor(row.LaunchCommand, row.Command)
	if rep.TranscriptPath != "" {
		if err := s.DB.SetSessionTranscript(ctx, row.ID, tool, rep.TranscriptPath); err != nil {
			s.Log.Warn("transcript path", "err", err)
		}
	}
	text := rep.Text
	if rep.Interrupted && text == "" {
		text = "(interrupted)"
	}
	if text == "" {
		return
	}
	// Claude Code fires PermissionRequest (with the command) and then
	// Notification (with a sentence about it) for one prompt. The first is
	// the one worth keeping; the second, arriving within promptEcho of a
	// prompt already stored, is the same event said twice.
	if rep.Event == "Notification" && rep.Kind == hooks.KindPrompt {
		if last, ok, _ := s.DB.LatestSessionMessage(ctx, row.ID); ok && last.Kind == store.MessagePrompt && time.Now().Unix()-last.At <= promptEcho {
			return
		}
	}
	m, err := s.DB.AddSessionMessage(ctx, store.SessionMessage{SessionID: row.ID, Kind: rep.Kind, Text: text, Tool: tool})
	if err != nil {
		s.Log.Warn("session message", "err", err)
		return
	}
	if s.Chat != nil {
		s.Chat.SessionSaid(row, m)
	}
}
