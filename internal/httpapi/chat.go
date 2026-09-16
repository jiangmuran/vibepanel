package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/chat"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
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
		Monitor:   s.chatMonitor,
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
	r.Post("/chat/consent", s.handleChatConsent)
	r.Post("/chat/pair", s.handleChatPair)
	r.Patch("/chat/peers/{channel}/{peer}", s.handlePatchChatPeer)
	r.Delete("/chat/peers/{channel}/{peer}", s.handleDeleteChatPeer)
	r.Put("/chat/routes", s.handlePutChatRoutes)
	r.Post("/chat/routes/preview", s.handleChatRoutePreview)
	r.Put("/chat/keys", s.handlePutChatTools)
	r.Put("/chat/assistant", s.handlePutChatAssistant)
	r.Put("/chat/lang", s.handlePutChatLang)
	r.Put("/chat/alerts", s.handlePutChatAlerts)
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
		r.Get("/system", s.handleChatToolSystem)
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
	// ConsentAt is when the owner accepted that configuring chat sends
	// session content to services outside this machine; zero until then.
	ConsentAt int64 `json:"consentAt"`
	// Alerts is when the machine is worth a message, and MonitorAvailable
	// whether this panel can read the machine at all.
	Alerts           chat.Alerts `json:"alerts"`
	MonitorAvailable bool        `json:"monitorAvailable"`
}

// ChatConsentKey is the settings row recording that acceptance.
const ChatConsentKey = "chat.consentAt"

// chatConsented reports whether the owner has accepted what configuring chat
// means. Asked before anything that makes the panel talk to an IM or a model
// provider for the first time: switching a channel on, a 微信 sign-in, the
// advanced mode. Not before reading, saving a channel switched off, or
// anything a channel already running keeps doing.
//
// On the server, not only in the page: an API token can configure a channel
// too, and a consent the page asks for and the API skips is a checkbox, not a
// gate. Until a channel is configured the chat code connects to nothing, which
// is what this acceptance is about losing.
func (s *Server) chatConsented(ctx context.Context) bool {
	raw, err := s.DB.GetSetting(ctx, ChatConsentKey, "")
	return err == nil && raw != "" && raw != "0"
}

// refuseWithoutConsent answers 409 when the owner has not accepted yet, and
// reports whether it did.
func (s *Server) refuseWithoutConsent(w http.ResponseWriter, r *http.Request) bool {
	if s.chatConsented(r.Context()) {
		return false
	}
	writeErr(w, http.StatusConflict, "chat consent required: accept that session content is sent to outside services first")
	return true
}

func (s *Server) handleChatConsent(w http.ResponseWriter, r *http.Request) {
	if !s.requireChat(w) {
		return
	}
	ctx := r.Context()
	at := time.Now().Unix()
	if raw, err := s.DB.GetSetting(ctx, ChatConsentKey, ""); err == nil && raw != "" && raw != "0" {
		at, _ = strconv.ParseInt(raw, 10, 64)
	} else {
		if err := s.DB.SetSetting(ctx, ChatConsentKey, strconv.FormatInt(at, 10)); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		user, _, _ := s.currentUser(r)
		s.audit(ctx, "chat.consent", user.Username, s.clientIP(r), "accepted that chat sends session content to outside services")
	}
	writeJSON(w, http.StatusOK, map[string]int64{"consentAt": at})
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
	raw, _ = s.DB.GetSetting(ctx, chat.AlertsKey, "")
	view.Alerts = chat.ParseAlerts(raw)
	view.MonitorAvailable = s.Sampler != nil
	if raw, err := s.DB.GetSetting(ctx, ChatConsentKey, ""); err == nil {
		view.ConsentAt, _ = strconv.ParseInt(raw, 10, 64)
	}
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
	if req.Enabled && s.refuseWithoutConsent(w, r) {
		return
	}
	if err := s.Chat.WriteChannel(ctx, kind, req.Enabled, cfg); err != nil {
		if errors.Is(err, chat.ErrChannelConfig) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
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
	// A sign-in is the first request to the IM's servers.
	if s.refuseWithoutConsent(w, r) {
		return
	}
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
	Mode    *string `json:"mode"`
	Status  *string `json:"status"`
	Display *string `json:"display"`
}

// cleanName drops what a name must not carry: control characters, which
// break a line in the log and the chat, and format characters such as the
// right-to-left override, which reorder what is around the name.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// peerParams reads a peer's channel and id from the path, decoded. A 微信 id
// is "someone@im.wechat", which the page escapes to %40 and chi hands over
// still escaped, so every row with an @ in it answered "not found" to block,
// mode and remove alike.
func peerParams(r *http.Request) (string, string, bool) {
	channel, err1 := url.PathUnescape(chi.URLParam(r, "channel"))
	peer, err2 := url.PathUnescape(chi.URLParam(r, "peer"))
	return channel, peer, err1 == nil && err2 == nil && channel != "" && peer != ""
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
	channel, peer, ok := peerParams(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad peer")
		return
	}
	if req.Mode != nil {
		if *req.Mode != store.ModeNormal && *req.Mode != store.ModeAdvanced {
			writeErr(w, http.StatusBadRequest, "mode is normal or advanced")
			return
		}
		if err := s.Chat.SetPeerMode(ctx, channel, peer, *req.Mode); err != nil {
			if errors.Is(err, chat.ErrPairByCode) {
				writeErr(w, http.StatusConflict, "pair them with their code first")
				return
			}
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
			if errors.Is(err, chat.ErrPairByCode) {
				writeErr(w, http.StatusConflict, "enter the code they were sent")
				return
			}
			s.writeStoreErr(w, err)
			return
		}
	}
	// The name last, so a request refused above changes nothing.
	if req.Display != nil {
		name := cleanName(*req.Display)
		if utf8.RuneCountInString(name) > 40 {
			writeErr(w, http.StatusBadRequest, "a name is at most 40 characters")
			return
		}
		if err := s.Chat.SetPeerDisplay(ctx, channel, peer, name); err != nil {
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
	channel, peer, ok := peerParams(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad peer")
		return
	}
	name := peer
	if p, err := s.DB.GetChatPeer(r.Context(), channel, peer); err == nil && p.Display != "" {
		name = p.Display
	}
	if err := s.DB.DeleteChatPeer(r.Context(), channel, peer); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	user, _, _ := s.currentUser(r)
	s.audit(r.Context(), "chat.peer", user.Username, s.clientIP(r), name+" ("+channel+"): removed")
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
	// Request and RequestPeers are the same for a permission request from
	// this session, whatever it is doing now.
	Request      chat.Decision `json:"request"`
	RequestPeers []string      `json:"requestPeers"`
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
	rd, rwho, _ := s.Chat.PreviewRequest(r.Context(), req.SessionID)
	writeJSON(w, http.StatusOK, routePreviewResponse{Decision: d, Change: c, Peers: who, Request: rd, RequestPeers: rwho})
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
		if len(name) == 0 || len(name) > 32 {
			writeErr(w, http.StatusBadRequest, "a profile needs a name")
			return
		}
		if why := chat.CheckProfile(name, p); why != "" {
			writeErr(w, http.StatusBadRequest, name+": "+why)
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
	// The advanced mode hands a person's words and the session table to a
	// model provider, which is outside this machine as much as an IM is.
	if cfg.Enabled && s.refuseWithoutConsent(w, r) {
		return
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

func (s *Server) handlePutChatAlerts(w http.ResponseWriter, r *http.Request) {
	var a chat.Alerts
	if !decode(w, r, &a) {
		return
	}
	if a.To == nil {
		a.To = []string{}
	}
	if err := a.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, _ := json.Marshal(a)
	if err := s.DB.SetSetting(r.Context(), chat.AlertsKey, string(raw)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, _, _ := s.currentUser(r)
	s.audit(r.Context(), "chat.alerts", user.Username, s.clientIP(r),
		fmt.Sprintf("enabled=%v cpu=%d%%/%dm mem=%d%% disk=%d%%", a.Enabled, a.CPUPercent, a.CPUMinutes, a.MemPercent, a.DiskPercent))
	writeJSON(w, http.StatusOK, a)
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

// toolSystemView is the machine for the advanced mode: the numbers, and the
// sessions using the most by handle and title. Restated rather than the
// sample itself, for the reason the session views are: the disk path and a
// field added to Sample later are not disclosed by default.
type toolSystemView struct {
	CPUPercent  *float64          `json:"cpuPercent"`
	Cores       int               `json:"cores"`
	Load        [3]float64        `json:"load"`
	MemUsed     uint64            `json:"memUsedBytes"`
	MemTotal    uint64            `json:"memTotalBytes"`
	SwapUsed    uint64            `json:"swapUsedBytes"`
	SwapTotal   uint64            `json:"swapTotalBytes"`
	DiskUsed    uint64            `json:"diskUsedBytes"`
	DiskTotal   uint64            `json:"diskTotalBytes"`
	UptimeHours float64           `json:"uptimeHours"`
	Sessions    []toolSessionLoad `json:"sessions"`
}

type toolSessionLoad struct {
	Handle     int     `json:"handle"`
	Title      string  `json:"title"`
	CPUPercent float64 `json:"cpuPercent"`
	RSSBytes   uint64  `json:"rssBytes"`
}

func (s *Server) handleChatToolSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sample, usage := s.chatMonitor(ctx)
	view := toolSystemView{
		CPUPercent: sample.CPUPercent, Cores: sample.Cores, Load: [3]float64{sample.Load1, sample.Load5, sample.Load15},
		MemUsed: sample.MemTotal - sample.MemAvailable, MemTotal: sample.MemTotal,
		SwapUsed: sample.SwapTotal - sample.SwapFree, SwapTotal: sample.SwapTotal,
		DiskUsed: sample.DiskTotal - sample.DiskFree, DiskTotal: sample.DiskTotal,
		UptimeHours: float64(sample.Uptime) / 3600, Sessions: []toolSessionLoad{},
	}
	for id, u := range usage {
		row, err := s.DB.GetSession(ctx, id)
		if err != nil || !chat.Addressable(row) || s.Chat == nil {
			continue
		}
		h, err := s.Chat.Handle(ctx, id)
		if err != nil {
			continue
		}
		view.Sessions = append(view.Sessions, toolSessionLoad{Handle: h, Title: row.Title, CPUPercent: u.CPUPercent, RSSBytes: u.RSS})
	}
	sort.Slice(view.Sessions, func(i, j int) bool { return view.Sessions[i].CPUPercent > view.Sessions[j].CPUPercent })
	writeJSON(w, http.StatusOK, view)
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
// It reports whether the document put a menu on the screen, which is a
// session waiting on its person whatever state the hook was configured to
// send for that event.
func (s *Server) recordHookMessage(ctx context.Context, row store.Session, body []byte) (menu bool) {
	rep, ok := hooks.Extract(body)
	if !ok {
		return false
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
		return false
	}
	// A menu on the screen is announced a few seconds later by a
	// Notification ("Claude needs your permission", "Claude is waiting for
	// your input") that says nothing about it. Stored, that sentence became
	// the latest message and turned the menu into a permission prompt. While
	// the menu is the latest message it is still what is being asked, so the
	// announcement only moves its time forward.
	if rep.Event == "Notification" {
		if last, ok, _ := s.DB.LatestSessionMessage(ctx, row.ID); ok && last.Menu != "" {
			_ = s.DB.TouchSessionMessage(ctx, last.ID, time.Now().Unix())
			return false
		}
	}
	// Claude Code fires PermissionRequest (with the command) and then
	// Notification (with a sentence about it) for one prompt. The first is
	// the one worth keeping; the second, arriving within promptEcho of a
	// prompt already stored, is the same event said twice.
	if rep.Event == "Notification" && rep.Kind == hooks.KindPrompt {
		if last, ok, _ := s.DB.LatestSessionMessage(ctx, row.ID); ok && last.Kind == store.MessagePrompt && time.Now().Unix()-last.At <= promptEcho {
			return false
		}
	}
	msg := store.SessionMessage{SessionID: row.ID, Kind: rep.Kind, Text: text, Tool: tool}
	if rep.Menu != nil {
		raw, _ := json.Marshal(rep.Menu)
		msg.Menu = string(raw)
	}
	m, err := s.DB.AddSessionMessage(ctx, msg)
	if err != nil {
		s.Log.Warn("session message", "err", err)
		return false
	}
	if s.Chat != nil {
		s.Chat.SessionSaid(row, m)
	}
	return rep.Menu != nil
}

// chatMonitor is the machine as the chat reads it: the panel's own sample,
// and each session's process tree where /proc can be read. Nil-safe for a
// server built without a sampler, which tests do.
func (s *Server) chatMonitor(ctx context.Context) (sysmon.Sample, map[string]sysmon.Usage) {
	if s.Sampler == nil {
		return sysmon.Sample{}, nil
	}
	sample := s.Sampler.Sample()
	if !sysmon.ProcReadable() {
		return sample, nil
	}
	usage, _ := s.sessionUsage(ctx)
	return sample, usage
}
