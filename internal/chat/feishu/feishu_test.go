package feishu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// The adapter against a fake open.feishu.cn and crafted webhook bodies.
//
// What is pinned is the wire: what a request to 飞书 looks like and which
// requests from it are let in. The bridge's rules are tested in the chat
// package against a fake adapter and not repeated here.

// ─── fakes ────────────────────────────────────────────────────────────────

type apiCall struct {
	Method string
	Path   string
	Query  string
	Auth   string
	CType  string
	Body   []byte
}

// fakeAPI is enough of open.feishu.cn to answer every call the adapter
// makes, recording each, with knobs for the failures worth handling.
type fakeAPI struct {
	srv *httptest.Server
	mu  sync.Mutex

	calls      []apiCall
	tokenCalls int
	tokenN     int
	expire     int
	// rateLimitOnce answers the next messages call with a 429 and this reset.
	rateLimitOnce string
	// staleTokenOnce answers the next messages call as a token error.
	staleTokenOnce bool
	// failMessages answers that many messages calls with fail before
	// succeeding, so a "retries forever" mutant ends rather than hangs.
	failMessages int
	fail         func(w http.ResponseWriter)
	resource     []byte
	// hold, when set, is closed by the test to let a resource download
	// answer; until then the request blocks.
	hold chan struct{}
}

func newAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{expire: 7200, resource: []byte("\x89PNG-bytes")}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, apiCall{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Auth: r.Header.Get("Authorization"), CType: r.Header.Get("Content-Type"), Body: body,
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch {
	case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
		f.tokenCalls++
		f.tokenN++
		fmt.Fprintf(w, `{"code":0,"msg":"ok","tenant_access_token":"t-%d","expire":%d}`, f.tokenN, f.expire)
	case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages") && strings.Contains(r.URL.Path, "/resources/"):
		if f.hold != nil {
			f.mu.Unlock()
			<-f.hold
			f.mu.Lock()
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(f.resource)
	case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages"):
		if f.failMessages > 0 {
			f.failMessages--
			f.fail(w)
			return
		}
		if f.rateLimitOnce != "" {
			w.Header().Set("x-ogw-ratelimit-reset", f.rateLimitOnce)
			f.rateLimitOnce = ""
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"code":99991400,"msg":"request trigger frequency limit"}`)
			return
		}
		if f.staleTokenOnce {
			f.staleTokenOnce = false
			fmt.Fprint(w, `{"code":99991663,"msg":"access token invalid"}`)
			return
		}
		if r.Method == http.MethodPatch {
			fmt.Fprint(w, `{"code":0,"msg":"ok","data":{}}`)
			return
		}
		fmt.Fprintf(w, `{"code":0,"msg":"success","data":{"message_id":"om_sent_%d"}}`, len(f.calls))
	case r.URL.Path == "/open-apis/im/v1/images":
		fmt.Fprint(w, `{"code":0,"msg":"success","data":{"image_key":"img_v2_abc"}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"code":404,"msg":"no such route"}`)
	}
}

func (f *fakeAPI) snapshot() []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiCall(nil), f.calls...)
}

// messages is every call that is not a token fetch.
func (f *fakeAPI) messages() []apiCall {
	var out []apiCall
	for _, c := range f.snapshot() {
		if !strings.Contains(c.Path, "tenant_access_token") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeAPI) tokens() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls
}

// recSink records what the adapter hands the bridge.
type recSink struct {
	mu     sync.Mutex
	in     []chat.Inbound
	health []error
	oks    int
	got    chan chat.Inbound
}

func newSink() *recSink { return &recSink{got: make(chan chat.Inbound, 16)} }

func (s *recSink) Inbound(ctx context.Context, in chat.Inbound) {
	s.mu.Lock()
	s.in = append(s.in, in)
	s.mu.Unlock()
	s.got <- in
}
func (s *recSink) Health(ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.oks++
		return
	}
	s.health = append(s.health, err)
}
func (s *recSink) State(ctx context.Context, state json.RawMessage) {}

func (s *recSink) okCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oks
}

func (s *recSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.in)
}

func (s *recSink) wait(t *testing.T) chat.Inbound {
	t.Helper()
	select {
	case in := <-s.got:
		return in
	case <-time.After(3 * time.Second):
		t.Fatal("nothing arrived at the sink")
		return chat.Inbound{}
	}
}

func (s *recSink) none(t *testing.T) {
	t.Helper()
	select {
	case in := <-s.got:
		t.Fatalf("unexpected inbound: %+v", in)
	case <-time.After(60 * time.Millisecond):
	}
}

func (s *recSink) lastErr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.health) == 0 {
		return ""
	}
	return s.health[len(s.health)-1].Error()
}

// ─── rig ──────────────────────────────────────────────────────────────────

const (
	appID     = "cli_test_app"
	verify    = "verif-token-123"
	encKey    = "test key"
	senderOID = "ou_sender"
)

type rig struct {
	t     *testing.T
	api   *fakeAPI
	ad    *Adapter
	sink  *recSink
	now   time.Time
	slept []time.Duration
	mu    sync.Mutex
}

func newRig(t *testing.T, encryptKey string) *rig {
	t.Helper()
	api := newAPI(t)
	cfg, _ := json.Marshal(map[string]string{
		"app_id": appID, "app_secret": "shh", "verification_token": verify,
		"encrypt_key": encryptKey, "apiBase": api.srv.URL,
	})
	a, err := New(cfg, chat.Env{HTTP: api.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, api: api, ad: a.(*Adapter), sink: newSink(), now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	r.ad.now = func() time.Time {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.now
	}
	r.ad.sleep = func(ctx context.Context, d time.Duration) error {
		r.mu.Lock()
		r.slept = append(r.slept, d)
		r.mu.Unlock()
		return nil
	}
	return r
}

// run starts Run and waits for the sink to be held.
func (r *rig) run() {
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { _ = r.ad.Run(ctx, r.sink); close(done) }()
	r.t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := r.ad.running(); s != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatal("Run never held the sink")
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

// post hits the webhook with a body and optional headers.
func (r *rig) post(body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/chat/hooks/feishu", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ad.WebhookHandler().ServeHTTP(rec, req)
	return rec
}

// signed encrypts a plaintext body the way 飞书 does and signs it.
func (r *rig) signed(plain []byte) ([]byte, map[string]string) {
	return r.signedAt(plain, r.ad.now())
}

// signedAt signs with a chosen timestamp, for the replay test.
func (r *rig) signedAt(plain []byte, at time.Time) ([]byte, map[string]string) {
	body := encryptBody(r.t, encKey, plain)
	ts, nonce := strconv.FormatInt(at.Unix(), 10), "nonce-1"
	return body, map[string]string{
		"X-Lark-Request-Timestamp": ts,
		"X-Lark-Request-Nonce":     nonce,
		"X-Lark-Signature":         signature(ts, nonce, encKey, body),
	}
}

// encryptBody is the sender's half of the scheme decrypt reverses:
// AES-256-CBC under sha256(key), a random IV first, PKCS#7 padding.
func encryptBody(t *testing.T, key string, plain []byte) []byte {
	t.Helper()
	k := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	buf := make([]byte, aes.BlockSize+len(padded))
	if _, err := rand.Read(buf[:aes.BlockSize]); err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, buf[:aes.BlockSize]).CryptBlocks(buf[aes.BlockSize:], padded)
	out, _ := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(buf)})
	return out
}

// messageEvent builds an im.message.receive_v1 body.
func messageEvent(eventID, msgID, chatType, msgType, content string, extra map[string]any) []byte {
	msg := map[string]any{
		"message_id": msgID, "create_time": "1609073151345", "chat_id": "oc_1",
		"chat_type": chatType, "message_type": msgType, "content": content,
	}
	for k, v := range extra {
		msg[k] = v
	}
	sender := map[string]any{"sender_id": map[string]string{"open_id": senderOID}, "sender_type": "user"}
	if st, ok := extra["sender_type"]; ok {
		sender["sender_type"] = st
		delete(msg, "sender_type")
	}
	b, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]string{
			"event_id": eventID, "event_type": eventMessage, "create_time": "1608725989000",
			"token": verify, "app_id": appID, "tenant_key": "tk",
		},
		"event": map[string]any{"sender": sender, "message": msg},
	})
	return b
}

func textEvent(eventID, msgID, text string) []byte {
	return messageEvent(eventID, msgID, "p2p", "text", jsonString(map[string]string{"text": text}), nil)
}

func decodeCard(t *testing.T, body []byte) (msgType string, card map[string]any) {
	t.Helper()
	var outer struct {
		MsgType string `json:"msg_type"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &outer); err != nil {
		t.Fatalf("outer: %v: %s", err, body)
	}
	if err := json.Unmarshal([]byte(outer.Content), &card); err != nil {
		t.Fatalf("content is not a JSON string of JSON: %v: %s", err, outer.Content)
	}
	return outer.MsgType, card
}

// ─── registration ─────────────────────────────────────────────────────────

func TestRegisteredAsAWebhookAdapterWithTheFourFields(t *testing.T) {
	f, ok := chat.FactoryFor(Kind)
	if !ok || !f.Webhook || f.Login || f.Label != "飞书" {
		t.Fatalf("factory: %+v", f)
	}
	var names []string
	for _, fld := range f.Fields {
		names = append(names, fld.Name)
		if fld.Name != "app_id" && !fld.Secret {
			t.Fatalf("%s is not secret", fld.Name)
		}
	}
	if strings.Join(names, ",") != "app_id,app_secret,verification_token,encrypt_key" {
		t.Fatalf("fields: %v", names)
	}
	for _, missing := range []string{"app_id", "app_secret", "verification_token"} {
		cfg := map[string]string{"app_id": "a", "app_secret": "b", "verification_token": "c"}
		delete(cfg, missing)
		raw, _ := json.Marshal(cfg)
		if _, err := New(raw, chat.Env{}); err == nil {
			t.Fatalf("built without %s", missing)
		}
	}
	ad, err := New(json.RawMessage(`{"app_id":"a","app_secret":"b","verification_token":"c"}`), chat.Env{})
	if err != nil {
		t.Fatal(err)
	}
	caps := ad.Capabilities()
	if !caps.Edit || !caps.Buttons || !caps.QuoteRefs || !caps.Proactive || !caps.Images || caps.Typing ||
		caps.MaxText != 4000 {
		t.Fatalf("capabilities: %+v", caps)
	}
	if _, ok := ad.(chat.WebhookAdapter); !ok {
		t.Fatal("not a WebhookAdapter")
	}
}

// ─── the handshake and the checks in front of every event ─────────────────

func TestChallengeIsAnsweredInPlain(t *testing.T) {
	r := newRig(t, "")
	rec := r.post([]byte(`{"challenge":"ajls384kdj","token":"`+verify+`","type":"url_verification"}`), nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"challenge":"ajls384kdj"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q", ct)
	}
	// The handshake with somebody else's token is not answered: the reply
	// would tell them the URL is live and listening.
	rec = r.post([]byte(`{"challenge":"x","token":"wrong","type":"url_verification"}`), nil)
	if rec.Code != 401 {
		t.Fatalf("wrong token got %d", rec.Code)
	}
}

func TestDecryptMatchesTheDocumentedVector(t *testing.T) {
	got, err := decrypt("test key", "P37w+VZImNgPEO1RBhJ6RtKl7n6zymIbEG1pReEzghk=")
	if err != nil || string(got) != "hello world" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := decrypt("other key", "P37w+VZImNgPEO1RBhJ6RtKl7n6zymIbEG1pReEzghk="); err == nil {
		t.Fatal("a wrong key decrypted")
	}
	if _, err := decrypt("test key", "AAAA"); err == nil {
		t.Fatal("a short buffer decrypted")
	}
	// Padding is checked byte by byte, not only by its last byte: a
	// block ending 05 05 05 04 05 is corrupt, not eleven bytes of text.
	k := sha256.Sum256([]byte("test key"))
	block, _ := aes.NewCipher(k[:])
	bad := append([]byte("hello world"), 5, 5, 5, 4, 5)
	buf := make([]byte, 32)
	cipher.NewCBCEncrypter(block, buf[:16]).CryptBlocks(buf[16:], bad)
	if got, err := decrypt("test key", base64.StdEncoding.EncodeToString(buf)); err == nil {
		t.Fatalf("corrupt padding decrypted to %q", got)
	}
}

func TestChallengeIsAnsweredWhenEncryptedAndUnsigned(t *testing.T) {
	r := newRig(t, encKey)
	body := encryptBody(t, encKey, []byte(`{"challenge":"c-1","token":"`+verify+`","type":"url_verification"}`))
	// No X-Lark-* headers: the docs exclude the handshake from signing.
	rec := r.post(body, nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"challenge":"c-1"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body = encryptBody(t, encKey, []byte(`{"challenge":"c-2","token":"wrong","type":"url_verification"}`))
	if rec := r.post(body, nil); rec.Code != 401 {
		t.Fatalf("wrong token inside the encryption got %d", rec.Code)
	}
	// A plaintext handshake when a key is configured did not come from
	// 飞书, which encrypts everything once the key is set.
	if rec := r.post([]byte(`{"challenge":"c-3","token":"`+verify+`","type":"url_verification"}`), nil); rec.Code != 401 {
		t.Fatalf("plaintext handshake with a key configured got %d", rec.Code)
	}
}

func TestSignatureIsCheckedOnEveryEncryptedEvent(t *testing.T) {
	r := newRig(t, encKey)
	r.run()
	body, headers := r.signed(textEvent("ev1", "om_1", "hi"))
	if rec := r.post(body, headers); rec.Code != 200 {
		t.Fatalf("a correctly signed event got %d %s", rec.Code, rec.Body.String())
	}
	if in := r.sink.wait(t); in.Text != "hi" {
		t.Fatalf("inbound %+v", in)
	}

	body, headers = r.signed(textEvent("ev2", "om_2", "hi"))
	headers["X-Lark-Signature"] = signature("1694779200", "nonce-1", "other key", body)
	if rec := r.post(body, headers); rec.Code != 401 {
		t.Fatalf("a wrongly signed event got %d", rec.Code)
	}
	r.sink.none(t)
	if !strings.Contains(r.sink.lastErr(), "signature") {
		t.Fatalf("health did not say why: %q", r.sink.lastErr())
	}

	// Missing headers are a bad signature, not an unsigned pass.
	body, _ = r.signed(textEvent("ev3", "om_3", "hi"))
	if rec := r.post(body, nil); rec.Code != 401 {
		t.Fatalf("an unsigned event got %d", rec.Code)
	}
	r.sink.none(t)

	// The signature covers the raw bytes: the same plaintext re-encrypted
	// under a fresh IV has a different body and the old signature fails.
	body, headers = r.signed(textEvent("ev4", "om_4", "hi"))
	other := encryptBody(t, encKey, textEvent("ev4", "om_4", "hi"))
	if rec := r.post(other, headers); rec.Code != 401 {
		t.Fatalf("a signature over different bytes passed: %d", rec.Code)
	}

	// A plaintext event when a key is configured is refused outright.
	if rec := r.post(textEvent("ev5", "om_5", "hi"), nil); rec.Code != 401 {
		t.Fatalf("plaintext with a key configured got %d", rec.Code)
	}
	r.sink.none(t)
}

func TestEncryptedEventWithoutAKeyConfiguredIsRefused(t *testing.T) {
	r := newRig(t, "")
	r.run()
	body := encryptBody(t, encKey, textEvent("ev1", "om_1", "hi"))
	if rec := r.post(body, nil); rec.Code != 400 {
		t.Fatalf("got %d", rec.Code)
	}
	if !strings.Contains(r.sink.lastErr(), "encrypt key") {
		t.Fatalf("health: %q", r.sink.lastErr())
	}
}

func TestVerificationTokenIsCheckedOnEveryEvent(t *testing.T) {
	r := newRig(t, "")
	r.run()
	body := bytes.Replace(textEvent("ev1", "om_1", "hi"), []byte(verify), []byte("not-it"), 1)
	if rec := r.post(body, nil); rec.Code != 401 {
		t.Fatalf("wrong token got %d", rec.Code)
	}
	r.sink.none(t)
	if !strings.Contains(r.sink.lastErr(), "token") {
		t.Fatalf("health: %q", r.sink.lastErr())
	}
	body = bytes.Replace(textEvent("ev2", "om_2", "hi"), []byte(`"token":"`+verify+`"`), []byte(`"token":""`), 1)
	if rec := r.post(body, nil); rec.Code != 401 {
		t.Fatalf("empty token got %d", rec.Code)
	}
	r.sink.none(t)
	// A v1 body carries the token at the top; the same check applies.
	if rec := r.post([]byte(`{"uuid":"u","token":"nope","ts":"1","type":"event_callback","event":{"type":"message"}}`), nil); rec.Code != 401 {
		t.Fatalf("v1 with a wrong token got %d", rec.Code)
	}
	if rec := r.post([]byte(`{"uuid":"u","token":"`+verify+`","ts":"1","type":"event_callback","event":{"type":"message"}}`), nil); rec.Code != 200 {
		t.Fatalf("v1 with the right token got %d", rec.Code)
	}
	r.sink.none(t)
}

func TestAnotherAppsEventIsDroppedNotRetried(t *testing.T) {
	r := newRig(t, "")
	r.run()
	body := bytes.Replace(textEvent("ev1", "om_1", "hi"), []byte(appID), []byte("cli_other"), 1)
	if rec := r.post(body, nil); rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	r.sink.none(t)
	if !strings.Contains(r.sink.lastErr(), "cli_other") {
		t.Fatalf("health: %q", r.sink.lastErr())
	}
}

func TestOnlyPostAndOnlyAMebibyte(t *testing.T) {
	r := newRig(t, "")
	req := httptest.NewRequest(http.MethodGet, "/api/chat/hooks/feishu", nil)
	rec := httptest.NewRecorder()
	r.ad.WebhookHandler().ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Fatalf("GET got %d", rec.Code)
	}
	big := append([]byte(`{"pad":"`), bytes.Repeat([]byte("x"), maxBody)...)
	big = append(big, []byte(`"}`)...)
	if rec := r.post(big, nil); rec.Code != 413 {
		t.Fatalf("oversized body got %d", rec.Code)
	}
	if rec := r.post([]byte(`{not json`), nil); rec.Code != 400 {
		t.Fatalf("garbage got %d", rec.Code)
	}
}

func TestEventsBeforeRunAreLeftForARetry(t *testing.T) {
	r := newRig(t, "")
	if rec := r.post(textEvent("ev1", "om_1", "hi"), nil); rec.Code != 503 {
		t.Fatalf("got %d", rec.Code)
	}
	// The handshake still works: it is what the console sends while
	// the channel is being set up.
	if rec := r.post([]byte(`{"challenge":"c","token":"`+verify+`","type":"url_verification"}`), nil); rec.Code != 200 {
		t.Fatalf("handshake before Run got %d", rec.Code)
	}
}

// ─── messages ─────────────────────────────────────────────────────────────

func TestAPrivateTextMessageBecomesAnInbound(t *testing.T) {
	r := newRig(t, "")
	r.run()
	body := messageEvent("ev1", "om_1", "p2p", "text", `{"text":"@_user_1 fix the tests"}`, map[string]any{
		"parent_id": "om_parent", "root_id": "om_parent",
		"mentions": []map[string]any{{"key": "@_user_1", "name": "Panel", "id": map[string]string{"open_id": "ou_bot"}}},
	})
	rec := r.post(body, nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	in := r.sink.wait(t)
	if in.PeerID != senderOID || in.Ref != "om_1" || in.QuotedRef != "om_parent" || in.Text != "@Panel fix the tests" ||
		in.Action != nil || in.FetchImage != nil {
		t.Fatalf("inbound %+v", in)
	}
	if in.At.UnixMilli() != 1609073151345 {
		t.Fatalf("At %v", in.At)
	}
	if r.sink.okCount() == 0 {
		t.Fatal("no health ok")
	}
}

func TestAGroupMessageIsDropped(t *testing.T) {
	r := newRig(t, "")
	r.run()
	body := messageEvent("ev1", "om_1", "group", "text", `{"text":"hi"}`, nil)
	if rec := r.post(body, nil); rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	r.sink.none(t)
	// So is a bot's, which is what the app's own messages look like when
	// they come back around.
	body = messageEvent("ev2", "om_2", "p2p", "text", `{"text":"hi"}`, map[string]any{"sender_type": "bot"})
	if rec := r.post(body, nil); rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	r.sink.none(t)
}

func TestARedeliveredMessageIsDroppedForADay(t *testing.T) {
	r := newRig(t, "")
	r.run()
	r.post(textEvent("ev1", "om_1", "hi"), nil)
	r.sink.wait(t)
	// Same message, new event id: the receive page says message_id is
	// the one to trust.
	if rec := r.post(textEvent("ev2", "om_1", "hi"), nil); rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	r.sink.none(t)
	// Same event id, different message id: the overview says event_id.
	r.post(textEvent("ev1", "om_9", "hi"), nil)
	r.sink.none(t)
	// Still remembered 23 hours on.
	r.advance(23 * time.Hour)
	r.post(textEvent("ev1", "om_1", "hi"), nil)
	r.sink.none(t)
	// Forgotten after a day, so the map does not grow forever.
	r.advance(2 * time.Hour)
	r.post(textEvent("ev1", "om_1", "hi"), nil)
	if in := r.sink.wait(t); in.Ref != "om_1" {
		t.Fatalf("inbound %+v", in)
	}
	if r.sink.count() != 2 {
		t.Fatalf("delivered %d times", r.sink.count())
	}
}

func TestAnImageIsDownloadedAndDelivered(t *testing.T) {
	r := newRig(t, "")
	r.run()
	body := messageEvent("ev1", "om_img", "p2p", "image", `{"image_key":"img_v2_key"}`, nil)
	// The fake holds the download until told; the 200 must come back
	// before that, because 飞书 gives the response three seconds and the
	// download is the one thing here that can take longer.
	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	// Released on the way out too, or a failing run leaves the fake's
	// handler blocked and its Close waiting on it.
	t.Cleanup(release)
	r.api.mu.Lock()
	r.api.hold = hold
	r.api.mu.Unlock()
	answered := make(chan int, 1)
	go func() { answered <- r.post(body, nil).Code }()
	select {
	case code := <-answered:
		if code != 200 {
			t.Fatalf("got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the response waited on the download")
	}
	// Delivered at once, with the download deferred to the bridge's ask;
	// the request never waited on the CDN.
	in := r.sink.wait(t)
	if in.FetchImage == nil || in.PeerID != senderOID || in.Ref != "om_img" || in.Text != "" {
		t.Fatalf("inbound %+v", in)
	}
	release()
	img, err := in.FetchImage(context.Background())
	if err != nil || !bytes.Equal(img, r.api.resource) {
		t.Fatalf("fetch: %v, %d bytes", err, len(img))
	}
	var dl *apiCall
	for _, c := range r.api.messages() {
		if strings.Contains(c.Path, "/resources/") {
			c := c
			dl = &c
		}
	}
	if dl == nil || dl.Method != "GET" || dl.Path != "/open-apis/im/v1/messages/om_img/resources/img_v2_key" || dl.Query != "type=image" {
		t.Fatalf("download call: %+v", dl)
	}
	if !strings.HasPrefix(dl.Auth, "Bearer t-") {
		t.Fatalf("download auth %q", dl.Auth)
	}
}

func TestAVoiceNoteArrivesAsVoiceWithNoText(t *testing.T) {
	r := newRig(t, "")
	r.run()
	body := messageEvent("ev1", "om_a", "p2p", "audio", `{"file_key":"file_v2_x","duration":2140}`, nil)
	r.post(body, nil)
	in := r.sink.wait(t)
	if in.Text != "" || in.Ref != "om_a" {
		t.Fatalf("inbound %+v", in)
	}
	if len(r.api.messages()) != 0 {
		t.Fatal("something was downloaded for a voice note nothing can transcribe")
	}
}

func TestARichTextPostIsFlattened(t *testing.T) {
	r := newRig(t, "")
	r.run()
	post := `{"title":"Plan","content":[[{"tag":"text","text":"see "},{"tag":"a","text":"docs","href":"https://x.test/d"}],[{"tag":"at","user_id":"ou_1","user_name":"Tom"},{"tag":"text","text":" ok"}],[{"tag":"img","image_key":"img_1"}]]}`
	r.post(messageEvent("ev1", "om_p", "p2p", "post", post, nil), nil)
	in := r.sink.wait(t)
	if in.Text != "Plan\nsee docs (https://x.test/d)\n@Tom ok\n[图片]" {
		t.Fatalf("text %q", in.Text)
	}
	// The language-keyed form the send docs show is read the same way.
	r.post(messageEvent("ev2", "om_q", "p2p", "post", `{"zh_cn":{"title":"T","content":[[{"tag":"text","text":"x"}]]}}`, nil), nil)
	if in := r.sink.wait(t); in.Text != "T\nx" {
		t.Fatalf("text %q", in.Text)
	}
	// A sticker has nothing to type.
	r.post(messageEvent("ev3", "om_s", "p2p", "sticker", `{"file_key":"f"}`, nil), nil)
	r.sink.none(t)
}

// ─── card callbacks ───────────────────────────────────────────────────────

func cardActionBody(eventID string, value any) []byte {
	b, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]string{
			"event_id": eventID, "token": verify, "create_time": "1603977298000000",
			"event_type": eventCardAction, "tenant_key": "tk", "app_id": appID,
		},
		"event": map[string]any{
			"operator": map[string]string{"open_id": "ou_presser", "tenant_key": "tk"},
			"token":    "c-delayed",
			"action":   map[string]any{"value": value, "tag": "button"},
			"host":     "im_message",
			"context":  map[string]string{"open_message_id": "om_card", "open_chat_id": "oc_1"},
		},
	})
	return b
}

func TestAButtonPressBecomesAnAction(t *testing.T) {
	r := newRig(t, "")
	r.run()
	rec := r.post(cardActionBody("cb1", map[string]string{"v": "approve:s1"}), nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("%d %q: a toast or a card in the callback answer would be shown to the person", rec.Code, rec.Body.String())
	}
	in := r.sink.wait(t)
	if in.Action == nil || in.Action.Value != "approve:s1" || in.Action.ID != "cb1" || in.Action.MessageRef != "om_card" ||
		in.PeerID != "ou_presser" || in.Text != "" {
		t.Fatalf("inbound %+v action %+v", in, in.Action)
	}
	// The same event id again is the same press.
	r.post(cardActionBody("cb1", map[string]string{"v": "approve:s1"}), nil)
	r.sink.none(t)
	// A bare string value, from a card built by hand, reads the same.
	r.post(cardActionBody("cb2", "screen:s1"), nil)
	if in := r.sink.wait(t); in.Action == nil || in.Action.Value != "screen:s1" {
		t.Fatalf("string value: %+v", in.Action)
	}
	// An object without "v" is not this adapter's button.
	r.post(cardActionBody("cb3", map[string]string{"action": "allow"}), nil)
	r.sink.none(t)
	// Ack is a no-op: the response has already gone.
	if err := r.ad.Ack(context.Background(), *in.Action, "ok"); err != nil || len(r.api.messages()) != 0 {
		t.Fatalf("ack: %v, calls %d", err, len(r.api.messages()))
	}
	// Encrypted and signed, the same way as an event.
	r2 := newRig(t, encKey)
	r2.run()
	body, headers := r2.signed(cardActionBody("cb4", map[string]string{"v": "deny:s1"}))
	if rec := r2.post(body, headers); rec.Code != 200 {
		t.Fatalf("signed callback got %d", rec.Code)
	}
	if in := r2.sink.wait(t); in.Action == nil || in.Action.Value != "deny:s1" {
		t.Fatalf("signed callback: %+v", in.Action)
	}
	body, headers = r2.signed(cardActionBody("cb5", map[string]string{"v": "deny:s1"}))
	headers["X-Lark-Signature"] = "00"
	if rec := r2.post(body, headers); rec.Code != 401 {
		t.Fatalf("badly signed callback got %d", rec.Code)
	}
	r2.sink.none(t)
}

// ─── sending ──────────────────────────────────────────────────────────────

func sampleCard() *chat.Card {
	return &chat.Card{
		Handle: 3, Title: "fix *tmux*", Project: "vibepanel", State: "waiting",
		Glyph: "▲", StateText: "在等你", Body: "Allow Bash to run rm -rf build/?", Footer: "刚刚 · claude",
		URL: "https://panel.test/?session=s1",
	}
}

func TestSendCardIsASchema20InteractiveMessage(t *testing.T) {
	r := newRig(t, "")
	buttons := []chat.Button{
		{Label: "允许", Value: "approve:s1"},
		{Label: "拒绝", Value: "deny:s1", Danger: true},
		{Label: "看屏幕", Value: "screen:s1"},
	}
	ref, err := r.ad.Send(context.Background(), chat.Peer{ID: "ou_x"}, chat.Outbound{Card: sampleCard(), Buttons: buttons})
	if err != nil || ref != "om_sent_2" {
		t.Fatalf("ref %q err %v", ref, err)
	}
	calls := r.api.messages()
	if len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
	c := calls[0]
	if c.Method != "POST" || c.Path != "/open-apis/im/v1/messages" || c.Query != "receive_id_type=open_id" {
		t.Fatalf("call %+v", c)
	}
	if c.Auth != "Bearer t-1" || !strings.HasPrefix(c.CType, "application/json") {
		t.Fatalf("headers %q %q", c.Auth, c.CType)
	}
	var outer map[string]any
	_ = json.Unmarshal(c.Body, &outer)
	if outer["receive_id"] != "ou_x" {
		t.Fatalf("receive_id %v", outer["receive_id"])
	}
	msgType, card := decodeCard(t, c.Body)
	if msgType != "interactive" {
		t.Fatalf("msg_type %q", msgType)
	}
	if card["schema"] != "2.0" {
		t.Fatalf("schema %v", card["schema"])
	}
	if cfg, _ := card["config"].(map[string]any); cfg["update_multi"] != true {
		t.Fatalf("config %v: a card without update_multi cannot be patched later", card["config"])
	}
	header := card["header"].(map[string]any)
	if header["template"] != "orange" {
		t.Fatalf("template %v", header["template"])
	}
	title := header["title"].(map[string]any)
	if title["tag"] != "plain_text" || title["content"] != "▲ [3] fix *tmux*" {
		t.Fatalf("title %v", title)
	}
	if sub := header["subtitle"].(map[string]any); sub["content"] != "vibepanel" {
		t.Fatalf("subtitle %v", sub)
	}
	elements := card["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("elements: %v", elements)
	}
	md := elements[0].(map[string]any)
	content, _ := md["content"].(string)
	if md["tag"] != "markdown" || strings.Contains(content, "[3]") || !strings.Contains(content, "在等你 · 刚刚 · claude") ||
		!strings.Contains(content, "Allow Bash to run rm -rf build/?") || !strings.Contains(content, "https://panel.test/?session=s1") {
		t.Fatalf("markdown %q", content)
	}
	row := elements[1].(map[string]any)
	if row["tag"] != "column_set" {
		t.Fatalf("buttons in %v", row)
	}
	cols := row["columns"].([]any)
	if len(cols) != 3 {
		t.Fatalf("columns %v", cols)
	}
	types := []string{}
	for i, col := range cols {
		btn := col.(map[string]any)["elements"].([]any)[0].(map[string]any)
		if btn["tag"] != "button" || btn["text"].(map[string]any)["content"] != buttons[i].Label {
			t.Fatalf("button %d: %v", i, btn)
		}
		types = append(types, btn["type"].(string))
		behaviors, _ := btn["behaviors"].([]any)
		if len(behaviors) != 1 {
			t.Fatalf("button %d has no behaviors; a 2.0 button without one may never call back: %v", i, btn)
		}
		bh := behaviors[0].(map[string]any)
		if bh["type"] != "callback" || bh["value"].(map[string]any)["v"] != buttons[i].Value {
			t.Fatalf("button %d behavior %v", i, bh)
		}
		// The value the callback returns is what actionValue reads.
		raw, _ := json.Marshal(bh["value"])
		if actionValue(raw) != buttons[i].Value {
			t.Fatalf("round trip of %v gave %q", bh["value"], actionValue(raw))
		}
	}
	if strings.Join(types, ",") != "primary,danger,default" {
		t.Fatalf("types %v", types)
	}
}

func TestTemplateFollowsTheState(t *testing.T) {
	for state, want := range map[string]string{"waiting": "orange", "working": "blue", "done": "green", "": "grey"} {
		c := sampleCard()
		c.State = state
		if got := sessionCard(c, nil)["header"].(map[string]any)["template"]; got != want {
			t.Fatalf("%q: %v", state, got)
		}
	}
	if els := sessionCard(sampleCard(), nil)["body"].(map[string]any)["elements"].([]any); len(els) != 1 {
		t.Fatalf("a card without buttons has %d elements", len(els))
	}
}

func TestSendTextAndReply(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	if _, err := r.ad.Send(ctx, chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "→ [3] 已送入\nline 2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ad.Send(ctx, chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "quoted", ReplyTo: "om_theirs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ad.Send(ctx, chat.Peer{ID: "ou_x"}, chat.Outbound{}); err == nil {
		t.Fatal("an empty outbound was sent")
	}
	calls := r.api.messages()
	if len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	if string(calls[0].Body) != `{"content":"{\"text\":\"→ [3] 已送入\\nline 2\"}","msg_type":"text","receive_id":"ou_x"}` {
		t.Fatalf("text body %s", calls[0].Body)
	}
	if calls[1].Path != "/open-apis/im/v1/messages/om_theirs/reply" || calls[1].Query != "" ||
		string(calls[1].Body) != `{"content":"{\"text\":\"quoted\"}","msg_type":"text"}` {
		t.Fatalf("reply %+v %s", calls[1], calls[1].Body)
	}
}

func TestSendCodeIsACardWithAFence(t *testing.T) {
	r := newRig(t, "")
	code := "$ ls\nmain.go ```x```\n"
	if _, err := r.ad.Send(context.Background(), chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "[3] 屏幕", Code: code}); err != nil {
		t.Fatal(err)
	}
	msgType, card := decodeCard(t, r.api.messages()[0].Body)
	if msgType != "interactive" || card["schema"] != "2.0" || card["config"].(map[string]any)["update_multi"] != true {
		t.Fatalf("%s %v", msgType, card)
	}
	md := card["body"].(map[string]any)["elements"].([]any)[0].(map[string]any)["content"].(string)
	if md != "[3] 屏幕\n````\n"+code+"\n````" {
		t.Fatalf("markdown %q", md)
	}
}

func TestEditPatchesTheCardAndRefusesText(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	c := sampleCard()
	c.State, c.Glyph, c.StateText = "done", "✓", "完成"
	if err := r.ad.Edit(ctx, chat.Peer{ID: "ou_x"}, "om_status", chat.Outbound{Card: c}); err != nil {
		t.Fatal(err)
	}
	calls := r.api.messages()
	if len(calls) != 1 || calls[0].Method != "PATCH" || calls[0].Path != "/open-apis/im/v1/messages/om_status" {
		t.Fatalf("calls %+v", calls)
	}
	var body struct {
		Content string  `json:"content"`
		MsgType *string `json:"msg_type"`
		Receive *string `json:"receive_id"`
	}
	_ = json.Unmarshal(calls[0].Body, &body)
	if body.MsgType != nil || body.Receive != nil {
		t.Fatalf("a patch carries only content: %s", calls[0].Body)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(body.Content), &card); err != nil {
		t.Fatal(err)
	}
	if card["schema"] != "2.0" || card["config"].(map[string]any)["update_multi"] != true ||
		card["header"].(map[string]any)["template"] != "green" {
		t.Fatalf("patched card %v", card)
	}
	if len(card["body"].(map[string]any)["elements"].([]any)) != 1 {
		t.Fatal("an edit without buttons kept buttons")
	}
	// Text cannot be patched; the error is what makes the bridge send
	// fresh next time.
	if err := r.ad.Edit(ctx, chat.Peer{ID: "ou_x"}, "om_status", chat.Outbound{Text: "hi"}); err == nil {
		t.Fatal("a text edit was attempted")
	}
	if len(r.api.messages()) != 1 {
		t.Fatal("a text edit reached the API")
	}
}

func TestSendImageUploadsThenSends(t *testing.T) {
	r := newRig(t, "")
	png := []byte("\x89PNG\r\n\x1a\nfake")
	ref, err := r.ad.SendImage(context.Background(), chat.Peer{ID: "ou_x"}, png, "[3] fix tmux")
	if err != nil || ref == "" {
		t.Fatalf("ref %q err %v", ref, err)
	}
	calls := r.api.messages()
	if len(calls) != 3 {
		t.Fatalf("calls %+v", calls)
	}
	up := calls[0]
	if up.Path != "/open-apis/im/v1/images" || !strings.HasPrefix(up.CType, "multipart/form-data") {
		t.Fatalf("upload %+v", up)
	}
	req := httptest.NewRequest("POST", "/", bytes.NewReader(up.Body))
	req.Header.Set("Content-Type", up.CType)
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	if req.FormValue("image_type") != "message" {
		t.Fatalf("image_type %q", req.FormValue("image_type"))
	}
	f, _, err := req.FormFile("image")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, png) {
		t.Fatalf("uploaded %q", got)
	}
	if string(calls[1].Body) != `{"content":"{\"text\":\"[3] fix tmux\"}","msg_type":"text","receive_id":"ou_x"}` {
		t.Fatalf("caption %s", calls[1].Body)
	}
	if string(calls[2].Body) != `{"content":"{\"image_key\":\"img_v2_abc\"}","msg_type":"image","receive_id":"ou_x"}` {
		t.Fatalf("image %s", calls[2].Body)
	}
	// No caption, no text message.
	if _, err := r.ad.SendImage(context.Background(), chat.Peer{ID: "ou_x"}, png, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(r.api.messages()); n != 5 {
		t.Fatalf("%d calls after a captionless image", n)
	}
	if _, err := r.ad.SendImage(context.Background(), chat.Peer{ID: "ou_x"}, nil, ""); err == nil {
		t.Fatal("an empty image was uploaded")
	}
}

// ─── the token and the retries ────────────────────────────────────────────

func TestTokenIsCachedUntilItExpiresAndRefreshedWhenRefused(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	send := func() {
		t.Helper()
		if _, err := r.ad.Send(ctx, chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	send()
	send()
	if r.api.tokens() != 1 {
		t.Fatalf("%d token fetches for two sends", r.api.tokens())
	}
	// A minute before expiry it is dropped, not on the second.
	r.advance(7200*time.Second - 90*time.Second)
	send()
	if r.api.tokens() != 1 {
		t.Fatalf("refetched with 90s left: %d", r.api.tokens())
	}
	r.advance(45 * time.Second)
	send()
	if r.api.tokens() != 2 {
		t.Fatalf("not refetched with 45s left: %d", r.api.tokens())
	}
	// The gateway refusing the token forces one refresh and a retry.
	r.api.mu.Lock()
	r.api.staleTokenOnce = true
	r.api.mu.Unlock()
	send()
	if r.api.tokens() != 3 {
		t.Fatalf("no refresh on 99991663: %d", r.api.tokens())
	}
	calls := r.api.messages()
	last := calls[len(calls)-1]
	if last.Auth != "Bearer t-3" {
		t.Fatalf("retry used %q", last.Auth)
	}
	if calls[len(calls)-2].Auth != "Bearer t-2" {
		t.Fatalf("first try used %q", calls[len(calls)-2].Auth)
	}
	// Run reports a bad secret, which is the one moment it can.
	r.run()
	if r.sink.okCount() != 1 {
		t.Fatalf("Run did not report the token fetch: oks=%d err=%q", r.sink.okCount(), r.sink.lastErr())
	}
}

func TestATokenErrorIsRetriedOnceOnly(t *testing.T) {
	r := newRig(t, "")
	// Three refusals in a row, then success: one retry gives up with the
	// error; a loop would get through and hammer the token endpoint.
	r.api.mu.Lock()
	r.api.failMessages = 3
	r.api.fail = func(w http.ResponseWriter) { fmt.Fprint(w, `{"code":99991663,"msg":"access token invalid"}`) }
	r.api.mu.Unlock()
	_, err := r.ad.Send(context.Background(), chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "99991663") {
		t.Fatalf("err %v", err)
	}
	if n, tk := len(r.api.messages()), r.api.tokens(); n != 2 || tk != 2 {
		t.Fatalf("%d message calls, %d token fetches", n, tk)
	}
}

func TestA429WaitsForTheResetAndRetriesOnce(t *testing.T) {
	r := newRig(t, "")
	r.api.mu.Lock()
	r.api.rateLimitOnce = "3"
	r.api.mu.Unlock()
	ref, err := r.ad.Send(context.Background(), chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "x"})
	if err != nil || ref == "" {
		t.Fatalf("ref %q err %v", ref, err)
	}
	if len(r.api.messages()) != 2 {
		t.Fatalf("%d message calls", len(r.api.messages()))
	}
	if len(r.slept) != 1 || r.slept[0] != 3*time.Second {
		t.Fatalf("slept %v", r.slept)
	}
	// The wait is capped: a 60 second reset does not hold the bridge.
	r.api.mu.Lock()
	r.api.rateLimitOnce = "60"
	r.api.mu.Unlock()
	if _, err := r.ad.Send(context.Background(), chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if r.slept[1] != maxRateWait {
		t.Fatalf("slept %v", r.slept)
	}
	// A second 429 in a row is an error, not a loop: three in a row and
	// then success must still come back as the error.
	before := len(r.api.messages())
	r.api.mu.Lock()
	r.api.failMessages = 3
	r.api.fail = func(w http.ResponseWriter) {
		w.Header().Set("x-ogw-ratelimit-reset", "1")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"code":99991400,"msg":"request trigger frequency limit"}`)
	}
	r.api.mu.Unlock()
	if _, err := r.ad.Send(context.Background(), chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "z"}); err == nil {
		t.Fatal("two 429s in a row succeeded")
	}
	if len(r.slept) != 3 || len(r.api.messages())-before != 2 {
		t.Fatalf("slept %v, %d calls", r.slept, len(r.api.messages())-before)
	}
}

func TestAnAPIErrorCarriesTheCode(t *testing.T) {
	r := newRig(t, "")
	r.api.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"t","expire":7200}`)
			return
		}
		fmt.Fprint(w, `{"code":230013,"msg":"user not in availability range"}`)
	})
	_, err := r.ad.Send(context.Background(), chat.Peer{ID: "ou_x"}, chat.Outbound{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "230013") || !strings.Contains(err.Error(), "availability") {
		t.Fatalf("err %v", err)
	}
	if err := r.ad.Typing(context.Background(), chat.Peer{ID: "ou_x"}, true); err != nil {
		t.Fatal(err)
	}
}

// A signature is good for five minutes. The tombstones that stop a redelivery
// last a day; a request captured and replayed after that would otherwise be
// new again, and a press replayed is a keystroke replayed.
func TestASignedRequestExpires(t *testing.T) {
	r := newRig(t, encKey)
	r.run()
	body, h := r.signedAt(textEvent("ev-old", "om_old", "replayed"), r.ad.now().Add(-6*time.Minute))
	if rec := r.post(body, h); rec.Code != 401 {
		t.Fatalf("a six-minute-old signature got %d", rec.Code)
	}
	r.sink.none(t)
	body, h = r.signedAt(textEvent("ev-recent", "om_recent", "fresh"), r.ad.now().Add(-4*time.Minute))
	if rec := r.post(body, h); rec.Code != 200 {
		t.Fatalf("a four-minute-old signature got %d %s", rec.Code, rec.Body.String())
	}
	if in := r.sink.wait(t); in.Text != "fresh" {
		t.Fatalf("inbound %+v", in)
	}
}
