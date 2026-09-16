// Package feishu is the 飞书 (Feishu / Lark) adapter: a custom app's bot in a
// private chat, reached by webhook.
//
// 飞书 calls the panel rather than being polled. The bridge mounts
// WebhookHandler at
//
//	<PublicURL>/api/chat/hooks/feishu
//
// and that one URL is pasted into the app's console twice: under 事件与回调
// → 事件配置 (subscription mode 将事件发送至开发者服务器, then add the event
// im.message.receive_v1) and again under the 回调配置 tab (add
// card.action.trigger, not card.action.trigger_v1, or every click arrives
// twice). Both kinds of POST share one envelope and one signing scheme, so
// the handler dispatches on header.event_type. The Verification Token and
// the Encrypt Key are on the 加密策略 tab of the same page; the scopes the
// bot needs are im:message.p2p_msg:readonly, im:message:send_as_bot,
// im:message:readonly (downloading a picture somebody sent) and
// im:resource (uploading a screenshot). The bot capability must be on and a
// version published, or nothing ever arrives.
//
// The Encrypt Key is optional in the console and recommended here: without
// it the only thing that says a request came from 飞书 is the Verification
// Token, in the clear, in the body. With it every body is encrypted and
// every request signed, and this handler then refuses an unsigned or a
// plaintext one outright, because "encrypted when 飞书 feels like it" would
// let anyone bypass the signature by not encrypting.
//
// Private chats only, as the bridge demands: a message whose chat_type is
// not p2p is dropped before it is parsed.
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// Kind is the registered adapter kind and the last path segment of the
// webhook URL.
const Kind = "feishu"

const defaultAPIBase = "https://open.feishu.cn"

// tombstoneTTL is how long a delivered message_id or event_id is remembered.
// 飞书 redelivers on anything but a 200 within 3s, at 15s, 5m, 1h and 6h,
// and says delivery is at-least-once even on success; a day covers the
// retry schedule with room, and the map it costs is a few thousand strings.
const tombstoneTTL = 24 * time.Hour

func init() {
	chat.Register(chat.Factory{
		Kind:    Kind,
		Label:   "飞书",
		Webhook: true,
		Fields: []chat.Field{
			{Name: "app_id", Label: "App ID", Hint: "console → app → credentials", HintZh: "开发者后台 → 应用 → 凭证与基础信息"},
			{Name: "app_secret", Label: "App Secret", Secret: true},
			{Name: "verification_token", Label: "Verification Token", Secret: true, Hint: "events & callbacks → encryption", HintZh: "事件与回调 → 加密策略"},
			{Name: "encrypt_key", Label: "Encrypt Key", Secret: true, Hint: "optional", HintZh: "可选"},
		},
		New: New,
	})
}

// Adapter is one 飞书 app talking to one tenant's people.
type Adapter struct {
	appID       string
	appSecret   string
	verifyToken string
	encryptKey  string
	apiBase     string
	http        *http.Client
	logf        func(string, ...any)

	// now and sleep are the clock, replaceable so a test can age a
	// tombstone by a day and see a 429 back-off without waiting through it.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	mu   sync.Mutex
	sink chat.Sink
	// ctx is Run's context. Inbound is handed over on it rather than on
	// the HTTP request's, because the bridge queues the work on a goroutine
	// that outlives the request, and a picture is downloaded after the
	// response has gone.
	ctx  context.Context
	seen map[string]time.Time
	// lastRefused is when the door last reported a refusal; see refuse.
	lastRefused time.Time
	pruned      time.Time

	tokMu  sync.Mutex
	token  string
	tokExp time.Time
}

// New builds the adapter from the settings form. The hidden apiBase value
// exists for tests to point the adapter at a fake; nothing on the form sets
// it.
func New(config json.RawMessage, env chat.Env) (chat.Adapter, error) {
	var v map[string]string
	if err := json.Unmarshal(config, &v); err != nil {
		return nil, fmt.Errorf("feishu: config: %w", err)
	}
	get := func(k string) string { return strings.TrimSpace(v[k]) }
	a := &Adapter{
		appID:       get("app_id"),
		appSecret:   get("app_secret"),
		verifyToken: get("verification_token"),
		encryptKey:  get("encrypt_key"),
		apiBase:     strings.TrimRight(get("apiBase"), "/"),
		http:        env.HTTP,
		logf:        env.Logf,
		now:         time.Now,
		sleep:       sleepCtx,
		seen:        map[string]time.Time{},
	}
	switch {
	case a.appID == "":
		return nil, errors.New("feishu: app_id is required")
	case a.appSecret == "":
		return nil, errors.New("feishu: app_secret is required")
	case a.verifyToken == "":
		// Without it an event has nothing to be checked against; a handler
		// that accepts anything at an unauthenticated URL would type
		// whatever arrived into a person's tmux pane.
		return nil, errors.New("feishu: verification_token is required")
	}
	if a.apiBase == "" {
		a.apiBase = defaultAPIBase
	}
	if a.http == nil {
		a.http = &http.Client{Timeout: 60 * time.Second}
	}
	if a.logf == nil {
		a.logf = func(string, ...any) {}
	}
	return a, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Kind implements chat.Adapter.
func (a *Adapter) Kind() string { return Kind }

// Capabilities implements chat.Adapter.
//
// Typing is false because 飞书 has no typing indicator for bots; VoiceText is
// false because a voice note arrives as a file key and a duration and
// nothing transcribes it.
func (a *Adapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{
		Edit: true, Buttons: true, QuoteRefs: true, Proactive: true,
		MaxText: 4000, Images: true, Typing: false,
	}
}

// Run holds the sink for the webhook handler until ctx ends. It fetches a
// token once on the way in, which is the only moment a wrong app_secret is
// reported anywhere: after that the panel only speaks when a session does.
func (a *Adapter) Run(ctx context.Context, sink chat.Sink) error {
	a.mu.Lock()
	a.sink = sink
	a.ctx = ctx
	a.mu.Unlock()
	if _, err := a.accessToken(ctx); err != nil {
		sink.Health(false, err)
	} else {
		sink.Health(true, nil)
	}
	<-ctx.Done()
	a.mu.Lock()
	a.sink = nil
	a.ctx = nil
	a.mu.Unlock()
	return ctx.Err()
}

func (a *Adapter) running() (chat.Sink, context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sink, a.ctx
}

// Send implements chat.Adapter. A card, and a screen capture (which only a
// card can show in monospace), go as an interactive message; anything else
// is a text message. ReplyTo turns the create into a reply, which is how
// 飞书 draws a quote.
func (a *Adapter) Send(ctx context.Context, to chat.Peer, m chat.Outbound) (string, error) {
	msgType, content, err := render(m)
	if err != nil {
		return "", err
	}
	return a.sendMessage(ctx, to, m.ReplyTo, msgType, content)
}

func (a *Adapter) sendMessage(ctx context.Context, to chat.Peer, replyTo, msgType, content string) (string, error) {
	var out struct {
		MessageID string `json:"message_id"`
	}
	if replyTo != "" {
		err := a.callJSON(ctx, http.MethodPost, "/open-apis/im/v1/messages/"+pathSeg(replyTo)+"/reply",
			map[string]any{"msg_type": msgType, "content": content}, &out)
		return out.MessageID, err
	}
	err := a.callJSON(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=open_id",
		map[string]any{"receive_id": to.ID, "msg_type": msgType, "content": content}, &out)
	return out.MessageID, err
}

// Edit implements chat.Adapter. Only an interactive message can be patched;
// asking to edit a text message is an error on purpose, so the bridge
// clears the status ref and sends a fresh card next time instead of
// retrying an edit that can never work.
func (a *Adapter) Edit(ctx context.Context, to chat.Peer, ref string, m chat.Outbound) error {
	msgType, content, err := render(m)
	if err != nil {
		return err
	}
	if msgType != "interactive" {
		return errors.New("feishu: only a card can be edited")
	}
	return a.callJSON(ctx, http.MethodPatch, "/open-apis/im/v1/messages/"+pathSeg(ref),
		map[string]any{"content": content}, nil)
}

// SendImage implements chat.Adapter: upload, then an image message. The
// caption goes ahead of it as its own text message when there is one,
// because an image message carries nothing but the key.
func (a *Adapter) SendImage(ctx context.Context, to chat.Peer, png []byte, caption string) (string, error) {
	key, err := a.uploadImage(ctx, png)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(caption) != "" {
		if _, err := a.sendMessage(ctx, to, "", "text", jsonString(map[string]string{"text": caption})); err != nil {
			return "", err
		}
	}
	return a.sendMessage(ctx, to, "", "image", jsonString(map[string]string{"image_key": key}))
}

// Typing implements chat.Adapter and does nothing: Capabilities says so and
// the bridge never calls it.
func (a *Adapter) Typing(ctx context.Context, to chat.Peer, on bool) error { return nil }

// Ack implements chat.Adapter and does nothing, deliberately. A card
// callback is answered synchronously by the webhook handler -- the 200 with
// `{}` has already gone by the time the bridge decides what the press
// meant -- and the only channel for a toast is that response. The bridge's
// text reaches the person as the reply Send that follows.
func (a *Adapter) Ack(ctx context.Context, act chat.Action, text string) error { return nil }
