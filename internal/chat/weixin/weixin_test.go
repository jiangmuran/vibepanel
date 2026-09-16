package weixin

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// The adapter against a fake iLink server and CDN.
//
// What is pinned is the wire: which endpoint, which headers, which body
// fields, in which order, and what the adapter does with each answer the
// real server has been seen to give. None of this can be checked against
// Tencent without an account, so the fake speaks exactly what the protocol
// notes say the 2.4.8 client sees, and the notes say where each fact came
// from.

type request struct {
	Name   string
	Method string
	Header http.Header
	Query  url.Values
	Body   map[string]any
	Raw    []byte
}

type fake struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	reqs     []request
	updates  []any
	statuses []any
	handlers map[string]func(r *http.Request, body map[string]any) (int, any)
	files    map[string][]byte
	uploads  map[string][]byte
	msgID    int64
	qrMethod string
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, handlers: map[string]func(*http.Request, map[string]any) (int, any){}, files: map[string][]byte{}, uploads: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	name := strings.TrimPrefix(r.URL.Path, "/ilink/bot/")
	if strings.HasPrefix(r.URL.Path, "/cdn/") {
		name = strings.TrimPrefix(r.URL.Path, "/")
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, request{Name: name, Method: r.Method, Header: r.Header.Clone(), Query: r.URL.Query(), Body: body, Raw: raw})
	h := f.handlers[name]
	f.mu.Unlock()
	if h != nil {
		code, resp := h(r, body)
		writeJSON(w, code, resp)
		return
	}
	switch name {
	case "getupdates":
		f.mu.Lock()
		var resp any = map[string]any{"ret": 0, "msgs": []any{}}
		if len(f.updates) > 0 {
			resp, f.updates = f.updates[0], f.updates[1:]
		} else {
			f.mu.Unlock()
			// Nothing queued: hold the poll a little so a test's loop is
			// not a busy one, then answer empty like the server does.
			select {
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Millisecond):
			}
			writeJSON(w, 200, resp)
			return
		}
		f.mu.Unlock()
		writeJSON(w, 200, resp)
	case "sendmessage":
		f.mu.Lock()
		f.msgID++
		id := f.msgID
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ret": 0, "errmsg": "", "message_id": 1000 + id})
	case "getconfig":
		writeJSON(w, 200, map[string]any{"ret": 0, "typing_ticket": "ticket-1"})
	case "sendtyping", "msg/notifystart", "msg/notifystop":
		writeJSON(w, 200, map[string]any{"ret": 0})
	case "getuploadurl":
		writeJSON(w, 200, map[string]any{"ret": 0, "upload_param": "up-1", "thumb_upload_param": ""})
	case "get_bot_qrcode":
		f.mu.Lock()
		want := f.qrMethod
		f.mu.Unlock()
		if want != "" && r.Method != want {
			w.WriteHeader(405)
			return
		}
		writeJSON(w, 200, map[string]any{"qrcode": "qr-1", "qrcode_img_content": "https://weixin.qq.com/x/abc"})
	case "get_qrcode_status":
		f.mu.Lock()
		var resp any = map[string]any{"status": "wait"}
		if len(f.statuses) > 0 {
			resp, f.statuses = f.statuses[0], f.statuses[1:]
		}
		f.mu.Unlock()
		writeJSON(w, 200, resp)
	case "cdn/upload":
		key := r.URL.Query().Get("filekey")
		f.mu.Lock()
		f.uploads[key] = raw
		f.mu.Unlock()
		w.Header().Set("x-encrypted-param", "dl-"+key)
		w.WriteHeader(200)
	case "cdn/download":
		f.mu.Lock()
		b, ok := f.files[r.URL.Query().Get("encrypted_query_param")]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(b)
	default:
		f.t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fake) queue(updates ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, updates...)
}

func (f *fake) on(name string, h func(*http.Request, map[string]any) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[name] = h
}

func (f *fake) calls(name string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []request
	for _, r := range f.reqs {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

func (f *fake) waitCalls(t *testing.T, name string, n int) []request {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c := f.calls(name); len(c) >= n {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s called %d times, want %d", name, len(f.calls(name)), n)
	return nil
}

// batch is one getupdates answer.
func batch(cursor string, msgs ...any) map[string]any {
	return map[string]any{"ret": 0, "msgs": msgs, "get_updates_buf": cursor, "longpolling_timeout_ms": 35000}
}

func userMsg(id int64, token string, items ...any) map[string]any {
	return map[string]any{
		"seq": id, "message_id": id, "from_user_id": "u1@im.wechat", "to_user_id": "b1@im.bot",
		"create_time_ms": 1774158905123, "message_type": 1, "message_state": 2,
		"context_token": token, "item_list": items,
	}
}

func textItemOf(s string) map[string]any {
	return map[string]any{"type": 1, "text_item": map[string]any{"text": s}}
}

// recSink records everything the adapter hands back, with an event log so
// the order of state-then-inbound can be asserted.
type recSink struct {
	mu       sync.Mutex
	events   []string
	inbounds []chat.Inbound
	states   []persisted
	health   []error
	healthOK int
	got      chan chat.Inbound
	// wait blocks until Run returns of its own accord.
	wait func() error
}

func newSink() *recSink { return &recSink{got: make(chan chat.Inbound, 64)} }

func (s *recSink) Inbound(ctx context.Context, in chat.Inbound) {
	s.mu.Lock()
	s.events = append(s.events, "inbound:"+in.Ref)
	s.inbounds = append(s.inbounds, in)
	s.mu.Unlock()
	s.got <- in
}

func (s *recSink) Health(ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.healthOK++
		return
	}
	s.health = append(s.health, err)
}

func (s *recSink) State(ctx context.Context, state json.RawMessage) {
	var p persisted
	_ = json.Unmarshal(state, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "state:"+p.Cursor)
	s.states = append(s.states, p)
}

func (s *recSink) next(t *testing.T) chat.Inbound {
	t.Helper()
	select {
	case in := <-s.got:
		return in
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound arrived")
	}
	return chat.Inbound{}
}

func (s *recSink) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case in := <-s.got:
		t.Fatalf("unexpected inbound %+v", in)
	case <-time.After(d):
	}
}

func (s *recSink) lastState() persisted {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.states) == 0 {
		return persisted{}
	}
	return s.states[len(s.states)-1]
}

func (s *recSink) failures() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error(nil), s.health...)
}

func newAdapter(t *testing.T, f *fake, state string) *Adapter {
	t.Helper()
	cfg := fmt.Sprintf(`{"bot_token":"tok-1","base_url":%q,"bot_id":"b1@im.bot","user_id":"me@im.wechat","apiBase":%q,"cdnBase":%q}`,
		f.srv.URL, f.srv.URL, f.srv.URL+"/cdn")
	ad, err := New(json.RawMessage(cfg), chat.Env{HTTP: f.srv.Client(), State: json.RawMessage(state), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return ad.(*Adapter)
}

// running starts Run with a recording sink and stops it at the end. The
// returned stop cancels and waits; calling it twice is fine, because the
// cleanup calls it too.
func running(t *testing.T, a *Adapter) (*recSink, func() error) {
	t.Helper()
	s := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, s) }()
	var once sync.Once
	var result error
	wait := func() {
		select {
		case result = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not stop")
		}
	}
	stop := func() error {
		once.Do(func() { cancel(); wait() })
		return result
	}
	s.wait = func() error {
		once.Do(wait)
		cancel()
		return result
	}
	t.Cleanup(func() { stop() })
	return s, stop
}

func TestRegisteredAsAQRLogin(t *testing.T) {
	f, ok := chat.FactoryFor(Kind)
	// The fields are the sign-in's credentials, declared so the server can
	// withhold the token and show who is signed in without knowing this
	// adapter by name.
	if !ok || !f.Login || len(f.Fields) != 3 || f.Fields[0].Name != "bot_token" || !f.Fields[0].Secret || f.Label != "微信" {
		t.Fatalf("factory %+v", f)
	}
	ad, err := f.New(json.RawMessage(`{}`), chat.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Run(context.Background(), newSink()); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("Run without a token: %v", err)
	}
	caps := ad.Capabilities()
	if caps.Edit || caps.Buttons || caps.QuoteRefs || caps.Proactive ||
		caps.MaxText != 4000 || !caps.Images || !caps.Typing {
		t.Fatalf("capabilities %+v", caps)
	}
}

func TestLoginFlowThroughScanCodeAndConfirm(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	ctx := context.Background()
	l, err := a.StartLogin(ctx)
	if err != nil || l.ID != "qr-1" || l.QRURL != "https://weixin.qq.com/x/abc" || l.Status != "waiting" {
		t.Fatalf("StartLogin: %+v %v", l, err)
	}
	qr := f.calls("get_bot_qrcode")[0]
	if qr.Method != "POST" || qr.Query.Get("bot_type") != "3" || qr.Header.Get("iLink-App-Id") != "bot" ||
		qr.Header.Get("iLink-App-ClientVersion") != "132104" || qr.Header.Get("Authorization") != "" {
		t.Fatalf("qr request %+v", qr)
	}
	if _, ok := qr.Body["local_token_list"].([]any); !ok || string(qr.Raw) != `{"local_token_list":[]}` {
		t.Fatalf("qr body %s", qr.Raw)
	}
	f.mu.Lock()
	f.statuses = []any{
		map[string]any{"status": "wait"},
		map[string]any{"status": "scaned"},
		map[string]any{"status": "need_verifycode"},
		map[string]any{"status": "confirmed", "bot_token": "ilb_secret", "ilink_bot_id": "b1@im.bot",
			"ilink_user_id": "u1@im.wechat", "baseurl": "https://region.weixin.qq.com"},
	}
	f.mu.Unlock()
	for _, want := range []string{"waiting", "scanned", "needCode"} {
		st, err := a.LoginStatus(ctx, l.ID)
		if err != nil || st.Status != want {
			t.Fatalf("status %+v %v, want %s", st, err, want)
		}
	}
	if err := a.SubmitCode(ctx, l.ID, " 4321 "); err != nil {
		t.Fatal(err)
	}
	st, err := a.LoginStatus(ctx, l.ID)
	if err != nil || st.Status != "done" {
		t.Fatalf("status %+v %v", st, err)
	}
	polls := f.calls("get_qrcode_status")
	if len(polls) != 4 || polls[2].Query.Get("verify_code") != "" || polls[3].Query.Get("verify_code") != "4321" ||
		polls[3].Query.Get("qrcode") != "qr-1" || polls[3].Header.Get("iLink-App-Id") != "bot" {
		t.Fatalf("polls %+v", polls)
	}
	var creds map[string]string
	if err := json.Unmarshal(st.Credentials, &creds); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"bot_token": "ilb_secret", "base_url": "https://region.weixin.qq.com", "bot_id": "b1@im.bot", "user_id": "u1@im.wechat"}
	if len(creds) != len(want) {
		t.Fatalf("credentials %v", creds)
	}
	for k, v := range want {
		if creds[k] != v {
			t.Fatalf("credentials %v", creds)
		}
	}
	// The sign-in is forgotten once done.
	if st, _ := a.LoginStatus(ctx, l.ID); st.Status != "expired" {
		t.Fatalf("after done: %+v", st)
	}
}

func TestLoginExpiredBlockedAndUnknown(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	ctx := context.Background()
	l, err := a.StartLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.statuses = []any{map[string]any{"status": "expired"}, map[string]any{"status": "verify_code_blocked"}, map[string]any{"status": "binded_redirect"}}
	f.mu.Unlock()
	if st, _ := a.LoginStatus(ctx, l.ID); st.Status != "expired" {
		t.Fatalf("%+v", st)
	}
	if st, _ := a.LoginStatus(ctx, l.ID); st.Status != "failed" || st.Error == "" {
		t.Fatalf("%+v", st)
	}
	if st, _ := a.LoginStatus(ctx, l.ID); st.Status != "failed" || !strings.Contains(st.Error, "bound") {
		t.Fatalf("%+v", st)
	}
	if st, _ := a.LoginStatus(ctx, "nope"); st.Status != "expired" {
		t.Fatalf("unknown id: %+v", st)
	}
	if err := a.SubmitCode(ctx, "nope", "1"); err == nil {
		t.Fatal("SubmitCode for an unknown sign-in succeeded")
	}
	// A sign-in older than its TTL is expired without asking the server.
	a.now = func() time.Time { return time.Now().Add(loginTTL + time.Minute) }
	if st, _ := a.LoginStatus(ctx, l.ID); st.Status != "expired" {
		t.Fatalf("stale: %+v", st)
	}
	if len(f.calls("get_qrcode_status")) != 3 {
		t.Fatal("a stale sign-in was polled")
	}
}

func TestLoginFallsBackToGETAndFollowsARedirect(t *testing.T) {
	f := newFake(t)
	f.mu.Lock()
	f.qrMethod = "GET"
	f.mu.Unlock()
	a := newAdapter(t, f, "")
	ctx := context.Background()
	l, err := a.StartLogin(ctx)
	if err != nil || l.ID != "qr-1" {
		t.Fatalf("%+v %v", l, err)
	}
	if c := f.calls("get_bot_qrcode"); len(c) != 2 || c[0].Method != "POST" || c[1].Method != "GET" {
		t.Fatalf("qr calls %+v", c)
	}
	other := newFake(t)
	f.mu.Lock()
	f.statuses = []any{map[string]any{"status": "scaned_but_redirect", "redirect_host": other.srv.URL}}
	f.mu.Unlock()
	if st, _ := a.LoginStatus(ctx, l.ID); st.Status != "scanned" {
		t.Fatalf("%+v", st)
	}
	other.mu.Lock()
	other.statuses = []any{map[string]any{"status": "confirmed", "bot_token": "t2", "baseurl": other.srv.URL}}
	other.mu.Unlock()
	if st, _ := a.LoginStatus(ctx, l.ID); st.Status != "done" {
		t.Fatalf("%+v", st)
	}
	if len(other.calls("get_qrcode_status")) != 1 || len(f.calls("get_qrcode_status")) != 1 {
		t.Fatal("the poll did not move to the redirect host")
	}
	// A network failure on the poll is "waiting", not an error.
	l2, _ := a.StartLogin(ctx)
	f.on("get_qrcode_status", func(*http.Request, map[string]any) (int, any) { return 502, "" })
	if st, err := a.LoginStatus(ctx, l2.ID); err != nil || st.Status != "waiting" {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestGetUpdatesDeliversTextWithTheRightHeaders(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	f.queue(batch("cur-1", userMsg(1, "ctx-1", textItemOf("你好"))))
	s, _ := running(t, a)
	in := s.next(t)
	if in.PeerID != "u1@im.wechat" || in.Ref != "1" || in.Text != "你好" || in.ContextToken != "ctx-1" ||
		in.FetchImage != nil || in.At.UnixMilli() != 1774158905123 {
		t.Fatalf("inbound %+v", in)
	}
	polls := f.waitCalls(t, "getupdates", 2)
	p := polls[0]
	if p.Header.Get("Authorization") != "Bearer tok-1" || p.Header.Get("AuthorizationType") != "ilink_bot_token" ||
		p.Header.Get("iLink-App-Id") != "bot" || p.Header.Get("iLink-App-ClientVersion") != "132104" ||
		p.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", p.Header)
	}
	uin1, uin2 := polls[0].Header.Get("X-WECHAT-UIN"), polls[1].Header.Get("X-WECHAT-UIN")
	if uin1 == "" || uin1 == uin2 {
		t.Fatalf("X-WECHAT-UIN not fresh per request: %q %q", uin1, uin2)
	}
	if dec, err := base64.StdEncoding.DecodeString(uin1); err != nil || strings.Trim(string(dec), "0123456789") != "" {
		t.Fatalf("X-WECHAT-UIN %q is not base64 of a decimal", uin1)
	}
	bi, _ := p.Body["base_info"].(map[string]any)
	if bi["channel_version"] != "vibepanel/0.1" || bi["bot_agent"] != "vibepanel/0.1" || p.Body["get_updates_buf"] != "" {
		t.Fatalf("body %v", p.Body)
	}
	if _, has := p.Body["sync_buf"]; has {
		t.Fatal("the deprecated sync_buf was sent")
	}
	if len(f.calls("msg/notifystart")) != 1 {
		t.Fatal("notifystart not sent")
	}
}

func TestVoiceArrivesAsTranscribedText(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	f.queue(batch("c1",
		userMsg(1, "t", map[string]any{"type": 3, "voice_item": map[string]any{"encode_type": 6, "text": "我下午三点到。"}}),
		userMsg(2, "t", map[string]any{"type": 3, "voice_item": map[string]any{"encode_type": 6}}),
	))
	s, _ := running(t, a)
	in := s.next(t)
	if in.Text != "我下午三点到。" {
		t.Fatalf("%+v", in)
	}
	in = s.next(t)
	if in.Text != "" || in.ContextToken != "t" {
		t.Fatalf("untranscribed voice %+v", in)
	}
}

func encryptFixture(t *testing.T) (plain, key, ct []byte) {
	t.Helper()
	plain = bytes.Repeat([]byte("\x89PNG fixture bytes "), 20)
	key, _ = hex.DecodeString("00112233445566778899aabbccddeeff")
	ct, err := encryptECB(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct)%16 != 0 || len(ct) <= len(plain) || bytes.Contains(ct, []byte("PNG")) {
		t.Fatalf("ciphertext looks wrong: %d bytes", len(ct))
	}
	return
}

func TestImagesAreDownloadedAndDecryptedWithEitherKeyEncoding(t *testing.T) {
	f := newFake(t)
	plain, key, ct := encryptFixture(t)
	f.mu.Lock()
	f.files["p1"] = ct
	f.files["p2"] = ct
	f.files["p3"] = ct
	f.files["p4"] = plain
	f.mu.Unlock()
	media := func(param, aesKey string) map[string]any {
		m := map[string]any{"encrypt_query_param": param, "encrypt_type": 1}
		if aesKey != "" {
			m["aes_key"] = aesKey
		}
		return m
	}
	rawB64 := base64.StdEncoding.EncodeToString(key)
	hexB64 := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))
	f.queue(batch("c1",
		// item aeskey (hex) wins over a wrong media key.
		userMsg(1, "t", textItemOf("看这个"), map[string]any{"type": 2, "image_item": map[string]any{"media": media("p1", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 16))), "aeskey": hex.EncodeToString(key), "mid_size": len(ct)}}),
		// media.aes_key = base64(raw 16 bytes).
		userMsg(2, "t", map[string]any{"type": 2, "image_item": map[string]any{"media": media("p2", rawB64)}}),
		// media.aes_key = base64(hex string).
		userMsg(3, "t", map[string]any{"type": 2, "image_item": map[string]any{"media": media("p3", hexB64)}}),
		// No key at all: the bytes are plain. Also full_url is used as is.
		userMsg(4, "t", map[string]any{"type": 2, "image_item": map[string]any{"media": map[string]any{"encrypt_query_param": "ignored", "full_url": f.srv.URL + "/cdn/download?encrypted_query_param=p4"}}}),
	))
	s, _ := running(t, newAdapter(t, f, ""))
	for i := 1; i <= 4; i++ {
		in := s.next(t)
		if in.Ref != fmt.Sprint(i) || in.FetchImage == nil {
			t.Fatalf("message %d: ref %s, no picture to fetch", i, in.Ref)
		}
		img, err := in.FetchImage(context.Background())
		if err != nil || !bytes.Equal(img, plain) {
			t.Fatalf("message %d: fetch %v, %d bytes (want %d)", i, err, len(img), len(plain))
		}
		if i == 1 && in.Text != "看这个" {
			t.Fatalf("caption lost: %+v", in.Text)
		}
	}
	dl := f.calls("cdn/download")
	if len(dl) != 4 || dl[0].Query.Get("encrypted_query_param") != "p1" || dl[0].Header.Get("Authorization") != "" {
		t.Fatalf("downloads %+v", dl)
	}
}

func TestAQuotedMessageBecomesQuotedText(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	f.queue(batch("c1", userMsg(1, "t", map[string]any{
		"type": 1, "text_item": map[string]any{"text": "请继续补充这一条。"},
		"ref_msg": map[string]any{"title": "引用了一条消息", "message_item": map[string]any{"type": 1, "text_item": map[string]any{"text": "▲ [3] fix tmux\n在等你"}}},
	})))
	s, _ := running(t, a)
	in := s.next(t)
	if in.Text != "请继续补充这一条。" || in.QuotedText != "引用了一条消息\n▲ [3] fix tmux\n在等你" || in.QuotedRef != "" {
		t.Fatalf("%+v", in)
	}
}

func TestARedeliveredMessageIsDroppedAcrossPollsAndRestarts(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	f.queue(
		batch("c1", userMsg(7, "t", textItemOf("once"))),
		batch("c2", userMsg(7, "t", textItemOf("once")), userMsg(8, "t", textItemOf("twice"))),
	)
	s, stop := running(t, a)
	if in := s.next(t); in.Ref != "7" {
		t.Fatalf("%+v", in)
	}
	if in := s.next(t); in.Ref != "8" {
		t.Fatalf("%+v", in)
	}
	s.none(t, 100*time.Millisecond)
	_ = stop()
	// The seen set is in the persisted state, so a restart does not replay.
	raw, _ := json.Marshal(s.lastState())
	b := newAdapter(t, f, string(raw))
	f.queue(batch("c3", userMsg(8, "t", textItemOf("twice")), userMsg(9, "t", textItemOf("new"))))
	s2, _ := running(t, b)
	if in := s2.next(t); in.Ref != "9" {
		t.Fatalf("after restart: %+v", in)
	}
}

func TestTheCursorIsPersistedBeforeDeliveryAndSentNext(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, `{"cursor":"old"}`)
	f.queue(batch("cur-A", userMsg(1, "t", textItemOf("a"))), batch("", userMsg(2, "t", textItemOf("b"))))
	s, _ := running(t, a)
	s.next(t)
	s.next(t)
	polls := f.waitCalls(t, "getupdates", 3)
	if polls[0].Body["get_updates_buf"] != "old" || polls[1].Body["get_updates_buf"] != "cur-A" || polls[2].Body["get_updates_buf"] != "cur-A" {
		t.Fatalf("cursors sent: %v %v %v", polls[0].Body["get_updates_buf"], polls[1].Body["get_updates_buf"], polls[2].Body["get_updates_buf"])
	}
	s.mu.Lock()
	events := append([]string(nil), s.events...)
	s.mu.Unlock()
	if len(events) < 2 || events[0] != "state:cur-A" || events[1] != "inbound:1" {
		t.Fatalf("events %v", events)
	}
	// An empty cursor from the server does not erase the stored one.
	if st := s.lastState(); st.Cursor != "cur-A" || len(st.Seen) != 2 {
		t.Fatalf("state %+v", st)
	}
}

func TestSeenIdsAgeOut(t *testing.T) {
	f := newFake(t)
	old := time.Now().Add(-25 * time.Hour).Unix()
	a := newAdapter(t, f, fmt.Sprintf(`{"cursor":"c","seen":{"1":%d,"2":%d}}`, old, time.Now().Unix()))
	f.queue(batch("c2", userMsg(3, "t", textItemOf("x"))))
	s, _ := running(t, a)
	s.next(t)
	st := s.lastState()
	if _, has := st.Seen["1"]; has || len(st.Seen) != 2 {
		t.Fatalf("seen %v", st.Seen)
	}
}

func TestBotEchoesAreSkipped(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	echo := userMsg(5, "t", textItemOf("what I said"))
	echo["message_type"] = 2
	echo["from_user_id"] = "b1@im.bot"
	f.queue(batch("c1", echo, userMsg(6, "t", textItemOf("real"))))
	s, _ := running(t, a)
	if in := s.next(t); in.Ref != "6" {
		t.Fatalf("%+v", in)
	}
	s.none(t, 100*time.Millisecond)
	if st := s.lastState(); len(st.Seen) != 1 {
		t.Fatalf("an echo was remembered: %v", st.Seen)
	}
}

func TestAStaleTokenEndsTheRun(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	f.queue(map[string]any{"ret": -14, "errcode": -14, "errmsg": "session timeout"})
	s, _ := running(t, a)
	err := s.wait()
	if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "QR") {
		t.Fatalf("Run returned %v", err)
	}
	if fs := s.failures(); len(fs) != 1 || fs[0] == nil || fs[0].Error() != err.Error() {
		t.Fatalf("health %v", fs)
	}
	if len(f.calls("getupdates")) != 1 {
		t.Fatal("polled again after the sign-in expired")
	}
	if len(f.calls("msg/notifystop")) != 1 {
		t.Fatal("notifystop not sent")
	}
}

func TestAStaleTokenIsRecognisedInErrcodeAlone(t *testing.T) {
	// The stale-token answer has been seen with the code in errcode and
	// ret at zero; a check on ret alone would poll forever with a dead
	// token, and the settings page would say the channel is running.
	f := newFake(t)
	a := newAdapter(t, f, "")
	f.queue(map[string]any{"ret": 0, "errcode": -14, "errmsg": "session timeout"})
	s, _ := running(t, a)
	if err := s.wait(); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Run returned %v", err)
	}
}

func TestOtherErrorsBackOffAndRecover(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	a.backoffShort = 10 * time.Millisecond
	f.queue(map[string]any{"ret": -2, "errmsg": "rate limited"}, batch("c1", userMsg(1, "t", textItemOf("ok"))))
	s, _ := running(t, a)
	s.next(t)
	fs := s.failures()
	if len(fs) != 1 || !strings.Contains(fs[0].Error(), "rate limited") {
		t.Fatalf("failures %v", fs)
	}
	s.mu.Lock()
	ok := s.healthOK
	s.mu.Unlock()
	if ok == 0 {
		t.Fatal("no healthy round reported after recovery")
	}
}

func TestSendCarriesTheTokenAndAFreshClientID(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	ctx := context.Background()
	peer := chat.Peer{ID: "u1@im.wechat", ContextToken: "ctx-9"}
	ref, err := a.Send(ctx, peer, chat.Outbound{Text: "hello"})
	if err != nil || ref != "1001" {
		t.Fatalf("%q %v", ref, err)
	}
	card := &chat.Card{Handle: 3, Title: "fix tmux", Project: "vibepanel", State: "waiting", Glyph: "▲", StateText: "在等你", Body: "Allow rm?", URL: "https://p/s/1"}
	if _, err := a.Send(ctx, peer, chat.Outbound{Card: card, Code: "$ ls\nmain.go\n", Buttons: []chat.Button{{Label: "x", Value: "y"}}}); err != nil {
		t.Fatal(err)
	}
	sends := f.calls("sendmessage")
	if len(sends) != 2 {
		t.Fatalf("%d sends", len(sends))
	}
	msg := sends[0].Body["msg"].(map[string]any)
	if msg["to_user_id"] != "u1@im.wechat" || msg["context_token"] != "ctx-9" || msg["message_type"] != 2.0 ||
		msg["message_state"] != 2.0 || msg["from_user_id"] != "" {
		t.Fatalf("msg %v", msg)
	}
	items := msg["item_list"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["text_item"].(map[string]any)["text"] != "hello" {
		t.Fatalf("items %v", items)
	}
	if bi := sends[0].Body["base_info"].(map[string]any); bi["bot_agent"] != "vibepanel/0.1" {
		t.Fatalf("base_info %v", bi)
	}
	if sends[0].Header.Get("Authorization") != "Bearer tok-1" {
		t.Fatalf("headers %v", sends[0].Header)
	}
	id1, id2 := msg["client_id"].(string), sends[1].Body["msg"].(map[string]any)["client_id"].(string)
	if !strings.HasPrefix(id1, "vibepanel-") || id1 == id2 {
		t.Fatalf("client ids %q %q", id1, id2)
	}
	text := sends[1].Body["msg"].(map[string]any)["item_list"].([]any)[0].(map[string]any)["text_item"].(map[string]any)["text"].(string)
	if text != chat.RenderPlain(card)+"\n```\n$ ls\nmain.go\n```" {
		t.Fatalf("card text %q", text)
	}
	if _, has := sends[1].Body["msg"].(map[string]any)["buttons"]; has {
		t.Fatal("buttons were sent")
	}
	if _, err := a.Send(ctx, chat.Peer{ID: "u1@im.wechat"}, chat.Outbound{Text: "x"}); err == nil || err.Error() != "weixin: no context token for this person yet" {
		t.Fatalf("no token: %v", err)
	}
	if _, err := a.SendImage(ctx, chat.Peer{ID: "u1@im.wechat"}, []byte("png"), ""); err == nil {
		t.Fatal("SendImage without a token succeeded")
	}
	if err := a.Edit(ctx, peer, "1001", chat.Outbound{Text: "no"}); err == nil {
		t.Fatal("Edit succeeded")
	}
	if len(f.calls("sendmessage")) != 2 {
		t.Fatal("a refused send reached the server")
	}
}

func TestSendSplitsLongTextAndReportsARefusal(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	ctx := context.Background()
	peer := chat.Peer{ID: "u1@im.wechat", ContextToken: "ctx"}
	long := strings.Repeat("字", 4001)
	if _, err := a.Send(ctx, peer, chat.Outbound{Text: long}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.calls("sendmessage")); n != 2 {
		t.Fatalf("%d sends for 4001 runes", n)
	}
	f.on("sendmessage", func(*http.Request, map[string]any) (int, any) {
		return 200, map[string]any{"ret": -2, "errmsg": "prepare failed"}
	})
	if _, err := a.Send(ctx, peer, chat.Outbound{Text: "x"}); err == nil || !strings.Contains(err.Error(), "prepare failed") {
		t.Fatalf("refusal: %v", err)
	}
	f.on("sendmessage", func(*http.Request, map[string]any) (int, any) { return 500, "" })
	if _, err := a.Send(ctx, peer, chat.Outbound{Text: "x"}); err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Fatalf("http error: %v", err)
	}
}

func TestASendWithoutAMessageIDIsWarnedAboutAndKeepsTheClientID(t *testing.T) {
	f := newFake(t)
	f.on("sendmessage", func(*http.Request, map[string]any) (int, any) { return 200, map[string]any{"ret": 0, "errmsg": ""} })
	var logs []string
	var mu sync.Mutex
	cfg := fmt.Sprintf(`{"bot_token":"tok-1","base_url":%q}`, f.srv.URL)
	ad, _ := New(json.RawMessage(cfg), chat.Env{HTTP: f.srv.Client(), Logf: func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}})
	ref, err := ad.Send(context.Background(), chat.Peer{ID: "u", ContextToken: "c"}, chat.Outbound{Text: "x"})
	if err != nil || !strings.HasPrefix(ref, "vibepanel-") {
		t.Fatalf("%q %v", ref, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "message_id") {
		t.Fatalf("logs %v", logs)
	}
}

func TestMarkdownTheClientCannotDrawIsFiltered(t *testing.T) {
	cases := map[string]string{
		"see ![shot](https://x/y.png) here":        "see  here",
		"##### Small heading\ntext\n###### six ##": "**Small heading**\ntext\n**six**",
		"## kept heading":                          "## kept heading",
		"这是*重点*和*另一个*词":                            "这是重点和另一个词",
		"this is *emphasis* here":                  "this is *emphasis* here",
		"**粗体**stays":                              "**粗体**stays",
		"`code` and ```\nfence\n```":               "`code` and ```\nfence\n```",
	}
	for in, want := range cases {
		if got := filterMarkdown(in); got != want {
			t.Errorf("filterMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
	f := newFake(t)
	a := newAdapter(t, f, "")
	if _, err := a.Send(context.Background(), chat.Peer{ID: "u", ContextToken: "c"}, chat.Outbound{Text: "看*这里*"}); err != nil {
		t.Fatal(err)
	}
	text := f.calls("sendmessage")[0].Body["msg"].(map[string]any)["item_list"].([]any)[0].(map[string]any)["text_item"].(map[string]any)["text"]
	if text != "看这里" {
		t.Fatalf("sent %q", text)
	}
}

func TestSendImageEncryptsUploadsAndDescribesTheKey(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	png := bytes.Repeat([]byte("\x89PNG\r\n\x1a\n"), 50)
	ref, err := a.SendImage(context.Background(), chat.Peer{ID: "u1@im.wechat", ContextToken: "ctx"}, png, "[3] fix tmux")
	if err != nil || ref != "1002" {
		t.Fatalf("%q %v", ref, err)
	}
	up := f.calls("getuploadurl")
	if len(up) != 1 {
		t.Fatalf("%d getuploadurl calls", len(up))
	}
	b := up[0].Body
	sum := md5.Sum(png)
	keyHex, _ := b["aeskey"].(string)
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 16 {
		t.Fatalf("aeskey %q", keyHex)
	}
	fileKey, _ := b["filekey"].(string)
	if fk, err := hex.DecodeString(fileKey); err != nil || len(fk) != 16 {
		t.Fatalf("filekey %q", fileKey)
	}
	wantSize := (len(png)/16 + 1) * 16
	if b["media_type"] != 1.0 || b["to_user_id"] != "u1@im.wechat" || b["rawsize"] != float64(len(png)) ||
		b["rawfilemd5"] != hex.EncodeToString(sum[:]) || b["filesize"] != float64(wantSize) || b["no_need_thumb"] != true ||
		b["base_info"] == nil {
		t.Fatalf("getuploadurl body %v", b)
	}
	cdn := f.calls("cdn/upload")
	if len(cdn) != 1 || cdn[0].Method != "POST" || cdn[0].Header.Get("Content-Type") != "application/octet-stream" ||
		cdn[0].Query.Get("encrypted_query_param") != "up-1" || cdn[0].Query.Get("filekey") != fileKey || cdn[0].Header.Get("Authorization") != "" {
		t.Fatalf("cdn upload %+v", cdn)
	}
	ct := cdn[0].Raw
	if len(ct) != wantSize {
		t.Fatalf("ciphertext %d bytes, want %d", len(ct), wantSize)
	}
	if back, err := decryptECB(key, ct); err != nil || !bytes.Equal(back, png) {
		t.Fatalf("ciphertext does not decrypt to the image: %v", err)
	}
	sends := f.calls("sendmessage")
	if len(sends) != 2 {
		t.Fatalf("%d sends; the caption and the image are separate", len(sends))
	}
	capt := sends[0].Body["msg"].(map[string]any)["item_list"].([]any)[0].(map[string]any)
	if capt["text_item"].(map[string]any)["text"] != "[3] fix tmux" {
		t.Fatalf("caption %v", capt)
	}
	msg := sends[1].Body["msg"].(map[string]any)
	if msg["context_token"] != "ctx" || msg["message_type"] != 2.0 {
		t.Fatalf("image msg %v", msg)
	}
	it := msg["item_list"].([]any)[0].(map[string]any)
	img := it["image_item"].(map[string]any)
	media := img["media"].(map[string]any)
	if it["type"] != 2.0 || media["encrypt_query_param"] != "dl-"+fileKey || media["encrypt_type"] != 1.0 ||
		media["aes_key"] != base64.StdEncoding.EncodeToString([]byte(keyHex)) || img["mid_size"] != float64(len(ct)) {
		t.Fatalf("image item %v", it)
	}
	if _, has := img["thumb_media"]; has {
		t.Fatal("a thumbnail was sent")
	}
}

func TestSendImagePrefersTheFullUploadURLAndNeedsTheParam(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	f.on("getuploadurl", func(*http.Request, map[string]any) (int, any) {
		return 200, map[string]any{"ret": 0, "upload_param": "up-2", "upload_full_url": f.srv.URL + "/cdn/upload?encrypted_query_param=full&filekey=given"}
	})
	if _, err := a.SendImage(context.Background(), chat.Peer{ID: "u", ContextToken: "c"}, []byte("img"), ""); err != nil {
		t.Fatal(err)
	}
	if c := f.calls("cdn/upload"); len(c) != 1 || c[0].Query.Get("encrypted_query_param") != "full" {
		t.Fatalf("%+v", c)
	}
	if len(f.calls("sendmessage")) != 1 {
		t.Fatal("an empty caption was sent")
	}
	f.on("cdn/upload", func(*http.Request, map[string]any) (int, any) { return 200, "" })
	if _, err := a.SendImage(context.Background(), chat.Peer{ID: "u", ContextToken: "c"}, []byte("img"), ""); err == nil || !strings.Contains(err.Error(), "x-encrypted-param") {
		t.Fatalf("upload without the param: %v", err)
	}
	if len(f.calls("sendmessage")) != 1 {
		t.Fatal("an image whose upload failed was sent anyway")
	}
}

func TestTypingFetchesTheTicketOnceAndKeepsItAlive(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	a.keepalive, a.typingMax = 15*time.Millisecond, time.Second
	s, _ := running(t, a)
	ctx := context.Background()
	peer := chat.Peer{ID: "u1@im.wechat", ContextToken: "ctx"}
	if err := a.Typing(ctx, peer, true); err != nil {
		t.Fatal(err)
	}
	cfg := f.calls("getconfig")
	if len(cfg) != 1 || cfg[0].Body["ilink_user_id"] != "u1@im.wechat" || cfg[0].Body["context_token"] != "ctx" || cfg[0].Body["base_info"] == nil {
		t.Fatalf("getconfig %+v", cfg)
	}
	f.waitCalls(t, "sendtyping", 3)
	if err := a.Typing(ctx, peer, false); err != nil {
		t.Fatal(err)
	}
	calls := f.calls("sendtyping")
	n := len(calls)
	for i, c := range calls {
		want := 1.0
		if i == n-1 {
			want = 2.0
		}
		if c.Body["status"] != want || c.Body["typing_ticket"] != "ticket-1" || c.Body["ilink_user_id"] != "u1@im.wechat" {
			t.Fatalf("sendtyping %d: %v", i, c.Body)
		}
		if _, has := c.Body["context_token"]; has {
			t.Fatal("sendtyping carried a context token")
		}
	}
	time.Sleep(60 * time.Millisecond)
	if len(f.calls("sendtyping")) != n {
		t.Fatal("the keepalive did not stop")
	}
	// The second round reuses the cached ticket, which is in the state.
	if st := s.lastState(); st.TypingTicket != "ticket-1" || st.TypingTicketAt == 0 {
		t.Fatalf("state %+v", st)
	}
	if err := a.Typing(ctx, peer, true); err != nil {
		t.Fatal(err)
	}
	_ = a.Typing(ctx, peer, false)
	if len(f.calls("getconfig")) != 1 {
		t.Fatal("getconfig called again with a fresh ticket cached")
	}
	// A day later it is fetched again; another person gets their own.
	a.now = func() time.Time { return time.Now().Add(ticketTTL + time.Minute) }
	_ = a.Typing(ctx, peer, true)
	_ = a.Typing(ctx, peer, false)
	if len(f.calls("getconfig")) != 2 {
		t.Fatal("a day-old ticket was reused")
	}
	_ = a.Typing(ctx, chat.Peer{ID: "u2@im.wechat", ContextToken: "c2"}, true)
	_ = a.Typing(ctx, chat.Peer{ID: "u2@im.wechat", ContextToken: "c2"}, false)
	if c := f.calls("getconfig"); len(c) != 3 || c[2].Body["ilink_user_id"] != "u2@im.wechat" {
		t.Fatal("a second person did not get their own ticket")
	}
}

func TestTypingWithNoTicketSendsNothing(t *testing.T) {
	f := newFake(t)
	f.on("getconfig", func(*http.Request, map[string]any) (int, any) {
		return 200, map[string]any{"ret": 0, "typing_ticket": ""}
	})
	a := newAdapter(t, f, "")
	peer := chat.Peer{ID: "u", ContextToken: "c"}
	if err := a.Typing(context.Background(), peer, true); err != nil {
		t.Fatal(err)
	}
	_ = a.Typing(context.Background(), peer, false)
	if len(f.calls("sendtyping")) != 0 {
		t.Fatal("sendtyping with an empty ticket")
	}
	// A cached ticket is used from the state, without a getconfig.
	st := fmt.Sprintf(`{"typing_ticket":"kept","typing_ticket_at":%d,"typing_ticket_user":"u"}`, time.Now().Unix())
	b := newAdapter(t, f, st)
	_ = b.Typing(context.Background(), peer, true)
	_ = b.Typing(context.Background(), peer, false)
	if len(f.calls("getconfig")) != 1 {
		t.Fatal("getconfig called with a cached ticket")
	}
	if c := f.calls("sendtyping"); len(c) != 2 || c[0].Body["typing_ticket"] != "kept" {
		t.Fatalf("%+v", c)
	}
}

func TestTypingKeepaliveStopsAtItsCap(t *testing.T) {
	f := newFake(t)
	a := newAdapter(t, f, "")
	a.keepalive, a.typingMax = 10*time.Millisecond, 50*time.Millisecond
	if err := a.Typing(context.Background(), chat.Peer{ID: "u", ContextToken: "c"}, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	n := len(f.calls("sendtyping"))
	time.Sleep(50 * time.Millisecond)
	if m := len(f.calls("sendtyping")); m != n || n < 2 || n > 8 {
		t.Fatalf("keepalives: %d then %d", n, m)
	}
}
