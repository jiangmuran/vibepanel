package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

const testToken = "123456:ABC-DEF"

// fakeAPI is a Bot API that records every request and answers from a queue
// of canned bodies per method. getUpdates is a real long poll: it blocks on
// the updates channel until a test pushes a batch or the client goes away,
// which is the only way to test a loop that must not spin.
type fakeAPI struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []call
	canned  map[string][]string
	updates chan string
	files   map[string][]byte
	// called wakes a waiter whenever a request is recorded.
	called chan struct{}
}

type call struct {
	method string
	ctype  string
	body   []byte
	at     time.Time
}

func (c call) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.body, &m); err != nil {
		t.Fatalf("%s body is not JSON: %v\n%s", c.method, err, c.body)
	}
	return m
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, canned: map[string][]string{}, updates: make(chan string, 16), files: map[string][]byte{}, called: make(chan struct{}, 256)}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

// fromChannel, queued for getUpdates, means "answer this poll from the
// updates channel", so a test can put a success between canned failures.
const fromChannel = "<poll>"

// on queues canned response bodies for a method, consumed in order. An
// {"ok":false} body is served with its error_code as the HTTP status, as
// Telegram does.
func (f *fakeAPI) on(method string, bodies ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canned[method] = append(f.canned[method], bodies...)
}

// push hands the next getUpdates one batch.
func (f *fakeAPI) push(updates ...string) {
	f.updates <- "[" + strings.Join(updates, ",") + "]"
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/file/bot"+testToken+"/") {
		f.record("file:"+strings.TrimPrefix(r.URL.Path, "/file/bot"+testToken+"/"), r.Header.Get("Content-Type"), body)
		data, ok := f.files[strings.TrimPrefix(r.URL.Path, "/file/bot"+testToken+"/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/bot"+testToken+"/") {
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/bot"+testToken+"/")
	f.record(method, r.Header.Get("Content-Type"), body)
	f.mu.Lock()
	var resp string
	if q := f.canned[method]; len(q) > 0 {
		resp, f.canned[method] = q[0], q[1:]
	}
	f.mu.Unlock()
	if (resp == "" || resp == fromChannel) && method == "getUpdates" {
		select {
		case batch := <-f.updates:
			resp = `{"ok":true,"result":` + batch + `}`
		case <-r.Context().Done():
			resp = `{"ok":true,"result":[]}`
		}
	}
	if resp == "" {
		switch method {
		case "sendMessage", "sendPhoto":
			resp = `{"ok":true,"result":{"message_id":123}}`
		case "getFile":
			resp = `{"ok":true,"result":{"file_path":"photos/x.png"}}`
		default:
			resp = `{"ok":true,"result":true}`
		}
	}
	var env struct {
		OK   bool `json:"ok"`
		Code int  `json:"error_code"`
	}
	if json.Unmarshal([]byte(resp), &env) == nil && !env.OK && env.Code > 0 {
		w.WriteHeader(env.Code)
	}
	_, _ = io.WriteString(w, resp)
}

func (f *fakeAPI) record(method, ctype string, body []byte) {
	f.mu.Lock()
	f.calls = append(f.calls, call{method: method, ctype: ctype, body: body, at: time.Now()})
	f.mu.Unlock()
	select {
	case f.called <- struct{}{}:
	default:
	}
}

// of returns every recorded call of a method.
func (f *fakeAPI) of(method string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// waitCalls blocks until n calls of a method have been recorded.
func (f *fakeAPI) waitCalls(t *testing.T, method string, n int) []call {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if c := f.of(method); len(c) >= n {
			return c
		}
		select {
		case <-f.called:
		case <-deadline:
			t.Fatalf("waited for %d %s calls, have %d", n, method, len(f.of(method)))
		}
	}
}

// fakeSink is what the bridge would be: channels, so a test can wait for
// the next thing rather than sleep and look.
type fakeSink struct {
	in     chan chat.Inbound
	health chan healthEntry
	states chan string
}

type healthEntry struct {
	ok  bool
	err error
}

func newSink() *fakeSink {
	return &fakeSink{in: make(chan chat.Inbound, 64), health: make(chan healthEntry, 64), states: make(chan string, 64)}
}

func (s *fakeSink) Inbound(ctx context.Context, in chat.Inbound) { s.in <- in }
func (s *fakeSink) Health(ok bool, err error)                    { s.health <- healthEntry{ok, err} }
func (s *fakeSink) State(ctx context.Context, st json.RawMessage) {
	s.states <- string(st)
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s within 5s", what)
		var zero T
		return zero
	}
}

func none[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s: %+v", what, v)
	case <-time.After(150 * time.Millisecond):
	}
}

type harness struct {
	api  *fakeAPI
	sink *fakeSink
	ad   *Adapter
	logs *bytes.Buffer
	mu   sync.Mutex
}

func (h *harness) logf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(h.logs, format+"\n", args...)
}

func (h *harness) logged() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logs.String()
}

// newHarness builds an adapter against a fake and, when run is set, starts
// its poll loop with a context that ends with the test.
func newHarness(t *testing.T, state string, run bool) *harness {
	t.Helper()
	h := &harness{api: newFakeAPI(t), sink: newSink(), logs: &bytes.Buffer{}}
	cfg := fmt.Sprintf(`{"token":%q,"apiBase":%q}`, testToken, h.api.srv.URL)
	var st json.RawMessage
	if state != "" {
		st = json.RawMessage(state)
	}
	ad, err := New(json.RawMessage(cfg), chat.Env{HTTP: h.api.srv.Client(), State: st, Logf: h.logf})
	if err != nil {
		t.Fatal(err)
	}
	h.ad = ad.(*Adapter)
	if run {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- h.ad.Run(ctx, h.sink) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Errorf("Run returned %v, want context.Canceled", err)
			}
		})
	}
	return h
}

func private(updateID, msgID int64, text string, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":1700000000,"chat":{"id":42,"type":"private"},"from":{"id":42,"first_name":"Ada","last_name":"Lovelace","username":"ada"},"text":%q%s}}`, updateID, msgID, text, extra)
}

func TestRegistered(t *testing.T) {
	f, ok := chat.FactoryFor(Kind)
	if !ok || f.Label != "Telegram" || len(f.Fields) != 1 || f.Fields[0].Name != "token" || !f.Fields[0].Secret {
		t.Fatalf("factory = %+v, %v", f, ok)
	}
	if _, err := f.New(json.RawMessage(`{"token":"  "}`), chat.Env{}); err == nil {
		t.Fatal("an empty token was accepted")
	}
	if _, err := f.New(json.RawMessage(`{"token":`), chat.Env{}); err == nil {
		t.Fatal("broken JSON was accepted")
	}
	ad, err := f.New(json.RawMessage(`{"token":"bot9:x","apiBase":"https://example.test/"}`), chat.Env{HTTP: &http.Client{Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	a := ad.(*Adapter)
	if a.url("getMe") != "https://example.test/bot9:x/getMe" {
		t.Fatalf("url = %q: the bot prefix and the trailing slash should both be gone", a.url("getMe"))
	}
	if a.http.Timeout != 5*time.Second || a.poll.Timeout != httpTimeout {
		t.Fatalf("timeouts: http %v poll %v; the poll client must outlive a %ds long poll without changing the shared one", a.http.Timeout, a.poll.Timeout, pollTimeout)
	}
	ad, _ = f.New(json.RawMessage(`{"token":"9:x"}`), chat.Env{})
	if ad.(*Adapter).http.Timeout != httpTimeout || ad.(*Adapter).apiBase != defaultAPIBase {
		t.Fatalf("defaults: %+v", ad.(*Adapter))
	}
	caps := ad.Capabilities()
	want := chat.Capabilities{Edit: true, Buttons: true, QuoteRefs: true, Proactive: true, Flavor: chat.FlavorHTML, MaxText: 4096, Images: true, Typing: true}
	if caps != want || ad.Kind() != "telegram" {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestPrivateChatsOnly(t *testing.T) {
	h := newHarness(t, "", true)
	first := h.api.waitCalls(t, "getUpdates", 1)[0].json(t)
	if _, has := first["offset"]; has {
		t.Fatalf("first poll with no state carries an offset: %v", first)
	}
	if first["timeout"] != float64(pollTimeout) || fmt.Sprint(first["allowed_updates"]) != "[message callback_query]" {
		t.Fatalf("poll params: %v", first)
	}
	group := `{"update_id":1,"message":{"message_id":3,"date":1700000000,"chat":{"id":-100,"type":"supergroup"},"from":{"id":7,"first_name":"Eve"},"text":"SECRET GROUP TEXT"}}`
	h.api.push(group, private(2, 7, "hello", `"reply_to_message":{"message_id":5,"text":"earlier"}`))
	in := recv(t, h.sink.in, "inbound")
	if in.PeerID != "42" || in.Ref != "7" || in.QuotedRef != "5" || in.Text != "hello" || in.PeerName != "Ada Lovelace" || in.At.Unix() != 1700000000 || in.Action != nil {
		t.Fatalf("inbound = %+v", in)
	}
	// The group message had the lower update id, so had it been delivered
	// it would have arrived first.
	none(t, h.sink.in, "second inbound")
	if strings.Contains(h.logged(), "GROUP TEXT") {
		t.Fatalf("a group's text reached the log:\n%s", h.logged())
	}
	if h := recv(t, h.sink.health, "health"); !h.ok || h.err != nil {
		t.Fatalf("health after a good poll = %+v", h)
	}
}

func TestPeerNameFallsBackToUsername(t *testing.T) {
	h := newHarness(t, "", true)
	h.api.push(`{"update_id":1,"message":{"message_id":3,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"username":"ada"},"text":"x"}}`)
	if in := recv(t, h.sink.in, "inbound"); in.PeerName != "ada" {
		t.Fatalf("PeerName = %q", in.PeerName)
	}
}

func TestOffsetIsPersistedAndCarried(t *testing.T) {
	h := newHarness(t, "", true)
	h.api.waitCalls(t, "getUpdates", 1)
	h.api.push(private(10, 1, "a", ""), private(11, 2, "b", ""))
	recv(t, h.sink.in, "inbound")
	recv(t, h.sink.in, "inbound")
	if st := recv(t, h.sink.states, "state"); st != `{"offset":11}` {
		t.Fatalf("state = %s", st)
	}
	second := h.api.waitCalls(t, "getUpdates", 2)[1].json(t)
	if second["offset"] != float64(12) {
		t.Fatalf("second poll offset = %v, want 12 (last update id + 1)", second["offset"])
	}
	// An empty batch persists nothing: the offset did not move.
	h.api.push()
	h.api.waitCalls(t, "getUpdates", 3)
	none(t, h.sink.states, "state after an empty batch")
}

func TestOffsetRestoredFromState(t *testing.T) {
	h := newHarness(t, `{"offset":41}`, true)
	first := h.api.waitCalls(t, "getUpdates", 1)[0].json(t)
	if first["offset"] != float64(42) {
		t.Fatalf("first poll offset = %v, want 42", first["offset"])
	}
	// What the state already covers is not delivered again even if
	// Telegram sends it.
	h.api.push(private(41, 1, "old", ""), private(42, 2, "new", ""))
	if in := recv(t, h.sink.in, "inbound"); in.Text != "new" {
		t.Fatalf("delivered %q, want only the update past the stored offset", in.Text)
	}
	none(t, h.sink.in, "replayed inbound")

	bad := newHarness(t, `{"offset":"forty"}`, false)
	if bad.ad.offset != 0 {
		t.Fatalf("unparseable state gave offset %d", bad.ad.offset)
	}
}

func TestDedupeByUpdateID(t *testing.T) {
	h := newHarness(t, "", true)
	h.api.push(private(5, 1, "one", ""))
	recv(t, h.sink.in, "inbound")
	h.api.push(private(5, 1, "one", ""), private(6, 2, "two", ""))
	if in := recv(t, h.sink.in, "inbound"); in.Text != "two" {
		t.Fatalf("got %q again; update 5 was already delivered", in.Text)
	}
	none(t, h.sink.in, "duplicate")
}

func TestCallbackQueryBecomesAction(t *testing.T) {
	h := newHarness(t, "", true)
	h.api.push(`{"update_id":1,"callback_query":{"id":"cb-9","from":{"id":42,"first_name":"Ada"},"data":"approve:s1","message":{"message_id":77,"date":1700000000,"chat":{"id":42,"type":"private"}}}}`)
	in := recv(t, h.sink.in, "inbound")
	if in.Action == nil || in.Action.Value != "approve:s1" || in.Action.ID != "cb-9" || in.Action.MessageRef != "77" || in.PeerID != "42" || in.Text != "" {
		t.Fatalf("inbound = %+v action = %+v", in, in.Action)
	}
	// A press in a group is dropped like a message in one.
	h.api.push(`{"update_id":2,"callback_query":{"id":"cb-10","from":{"id":42},"data":"deny:s1","message":{"message_id":78,"chat":{"id":-5,"type":"group"}}}}`)
	// An inaccessible message: no chat to read, the presser is the peer.
	h.api.push(`{"update_id":3,"callback_query":{"id":"cb-11","from":{"id":42},"data":"screen:s1"}}`)
	in = recv(t, h.sink.in, "inbound")
	if in.Action == nil || in.Action.ID != "cb-11" || in.PeerID != "42" || in.Action.MessageRef != "" {
		t.Fatalf("inbound = %+v action = %+v", in, in.Action)
	}
	none(t, h.sink.in, "group press")
}

func TestPhotoIsDownloaded(t *testing.T) {
	h := newHarness(t, "", true)
	png := []byte("\x89PNG\r\n\x1a\nfake")
	h.api.files["photos/big.png"] = png
	h.api.on("getFile", `{"ok":true,"result":{"file_id":"big","file_path":"photos/big.png"}}`)
	h.api.push(`{"update_id":1,"message":{"message_id":3,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"first_name":"Ada"},"caption":"look","photo":[{"file_id":"small","width":90,"height":60},{"file_id":"big","width":800,"height":600},{"file_id":"mid","width":320,"height":240}]}}`)
	in := recv(t, h.sink.in, "inbound")
	if !bytes.Equal(in.Image, png) || in.Text != "look" {
		t.Fatalf("inbound = %+v", in)
	}
	if gf := h.api.of("getFile"); len(gf) != 1 || gf[0].json(t)["file_id"] != "big" {
		t.Fatalf("getFile calls = %+v; the largest size is the one asked for", gf)
	}
	if len(h.api.of("file:photos/big.png")) != 1 {
		t.Fatal("the file endpoint was not hit")
	}
	// A photo the API will not serve: the caption still arrives, nothing
	// hangs, and the failure is in the log.
	h.api.on("getFile", `{"ok":false,"error_code":400,"description":"Bad Request: file is too big"}`)
	h.api.push(`{"update_id":2,"message":{"message_id":4,"date":1,"chat":{"id":42,"type":"private"},"caption":"again","photo":[{"file_id":"huge","width":8000,"height":6000}]}}`)
	in = recv(t, h.sink.in, "inbound")
	if in.Image != nil || in.Text != "again" || !strings.Contains(h.logged(), "file is too big") {
		t.Fatalf("inbound = %+v\nlog: %s", in, h.logged())
	}
	// Over the cap on the wire, undeclared: read to the cap and dropped.
	h.api.files["photos/vast.png"] = make([]byte, maxImage+1)
	h.api.on("getFile", `{"ok":true,"result":{"file_path":"photos/vast.png"}}`)
	h.api.push(`{"update_id":3,"message":{"message_id":5,"date":1,"chat":{"id":42,"type":"private"},"caption":"vast","photo":[{"file_id":"vast","width":1,"height":1}]}}`)
	in = recv(t, h.sink.in, "inbound")
	if in.Image != nil || in.Text != "vast" || !strings.Contains(h.logged(), "limit") {
		t.Fatalf("an oversized download was delivered: %d bytes", len(in.Image))
	}
	// Over the cap by declaration: not even asked for.
	h.api.push(fmt.Sprintf(`{"update_id":4,"message":{"message_id":6,"date":1,"chat":{"id":42,"type":"private"},"photo":[{"file_id":"vast","width":1,"height":1,"file_size":%d}]}}`, maxImage+1))
	in = recv(t, h.sink.in, "inbound")
	if in.Image != nil || len(h.api.of("getFile")) != 3 {
		t.Fatalf("an oversized photo was fetched: %+v", in)
	}
}

func TestVoiceArrivesEmptyAndMarked(t *testing.T) {
	h := newHarness(t, "", true)
	h.api.push(`{"update_id":1,"message":{"message_id":3,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"first_name":"Ada"},"caption":"cap","voice":{"file_id":"v","duration":3}}}`)
	in := recv(t, h.sink.in, "inbound")
	if !in.Voice || in.Text != "" || in.Image != nil {
		t.Fatalf("inbound = %+v", in)
	}
	h.api.push(`{"update_id":2,"message":{"message_id":4,"date":1,"chat":{"id":42,"type":"private"},"audio":{"file_id":"a"}}}`)
	if in := recv(t, h.sink.in, "inbound"); !in.Voice {
		t.Fatalf("audio not marked: %+v", in)
	}
}

func TestSendMessageBody(t *testing.T) {
	h := newHarness(t, "", false)
	h.api.on("sendMessage", `{"ok":true,"result":{"message_id":501}}`)
	ref, err := h.ad.Send(context.Background(), chat.Peer{ID: "42"}, chat.Outbound{
		Text:    "a <b>tag</b> & more",
		Code:    "$ ls\n<dir>",
		Buttons: []chat.Button{{Label: "Allow", Value: "approve:s1"}, {Label: "Deny", Value: "deny:s1", Danger: true}},
		ReplyTo: "77",
	})
	if err != nil || ref != "501" {
		t.Fatalf("Send = %q, %v", ref, err)
	}
	c := h.api.waitCalls(t, "sendMessage", 1)[0]
	if !strings.HasPrefix(c.ctype, "application/json") {
		t.Fatalf("content type %q", c.ctype)
	}
	body := c.json(t)
	if body["chat_id"] != float64(42) || body["parse_mode"] != "HTML" {
		t.Fatalf("body = %v", body)
	}
	if body["text"] != "a &lt;b&gt;tag&lt;/b&gt; &amp; more\n<pre>$ ls\n&lt;dir&gt;</pre>" {
		t.Fatalf("text = %q", body["text"])
	}
	kb, _ := json.Marshal(body["reply_markup"])
	if string(kb) != `{"inline_keyboard":[[{"callback_data":"approve:s1","text":"Allow"},{"callback_data":"deny:s1","text":"Deny"}]]}` {
		t.Fatalf("reply_markup = %s", kb)
	}
	rp, _ := body["reply_parameters"].(map[string]any)
	if rp["message_id"] != float64(77) || rp["allow_sending_without_reply"] != true || body["reply_to_message_id"] != float64(77) {
		t.Fatalf("reply: %v / %v", body["reply_parameters"], body["reply_to_message_id"])
	}
	if lp, _ := body["link_preview_options"].(map[string]any); lp["is_disabled"] != true {
		t.Fatalf("link preview not disabled: %v", body)
	}

	// A card, no buttons, no reply: rendered by chat.RenderHTML, no
	// keyboard, no reply fields.
	card := &chat.Card{Handle: 3, Title: "fix <login>", State: "waiting", Glyph: "▲", StateText: "waiting for you", Body: "ok?", URL: "https://p/s/1"}
	if _, err := h.ad.Send(context.Background(), chat.Peer{ID: "42"}, chat.Outbound{Card: card}); err != nil {
		t.Fatal(err)
	}
	body = h.api.waitCalls(t, "sendMessage", 2)[1].json(t)
	if body["text"] != chat.RenderHTML(card) {
		t.Fatalf("card text = %q", body["text"])
	}
	for _, k := range []string{"reply_markup", "reply_parameters", "reply_to_message_id"} {
		if _, has := body[k]; has {
			t.Fatalf("%s sent with nothing to put in it: %v", k, body)
		}
	}

	if _, err := h.ad.Send(context.Background(), chat.Peer{ID: "not-a-chat"}, chat.Outbound{Text: "x"}); err == nil || len(h.api.of("sendMessage")) != 2 {
		t.Fatalf("a non-numeric peer was sent: %v", err)
	}
}

func TestSendFallsBackToPlainWhenHTMLIsRejected(t *testing.T) {
	h := newHarness(t, "", false)
	h.api.on("sendMessage",
		`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: Unsupported start tag \"x\" at byte offset 3"}`,
		`{"ok":true,"result":{"message_id":9}}`)
	card := &chat.Card{Handle: 1, Title: "t", Glyph: "●", StateText: "working", Body: "b"}
	ref, err := h.ad.Send(context.Background(), chat.Peer{ID: "42"}, chat.Outbound{Card: card})
	if err != nil || ref != "9" {
		t.Fatalf("Send = %q, %v", ref, err)
	}
	calls := h.api.waitCalls(t, "sendMessage", 2)
	first, second := calls[0].json(t), calls[1].json(t)
	if first["parse_mode"] != "HTML" {
		t.Fatalf("first attempt = %v", first)
	}
	if _, has := second["parse_mode"]; has || second["text"] != chat.RenderPlain(card) {
		t.Fatalf("retry = %v; want plain text and no parse_mode", second)
	}
	if !strings.Contains(h.logged(), "can't parse entities") {
		t.Fatalf("the rejection was not logged: %s", h.logged())
	}

	// Any other 400 is the caller's to see, once, with Telegram's words.
	h.api.on("sendMessage", `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	_, err = h.ad.Send(context.Background(), chat.Peer{ID: "42"}, chat.Outbound{Text: "x"})
	var te *Error
	if !errors.As(err, &te) || te.Code != 400 || !strings.Contains(err.Error(), "chat not found") || len(h.api.of("sendMessage")) != 3 {
		t.Fatalf("err = %v, calls = %d", err, len(h.api.of("sendMessage")))
	}
	// A rejection of the plain retry too is an error, not a loop.
	h.api.on("sendMessage",
		`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`,
		`{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`)
	if _, err := h.ad.Send(context.Background(), chat.Peer{ID: "42"}, chat.Outbound{Text: "x"}); err == nil || !strings.Contains(err.Error(), "too long") || len(h.api.of("sendMessage")) != 5 {
		t.Fatalf("err = %v, calls = %d", err, len(h.api.of("sendMessage")))
	}
}

func TestEditMessage(t *testing.T) {
	h := newHarness(t, "", false)
	err := h.ad.Edit(context.Background(), chat.Peer{ID: "42"}, "77", chat.Outbound{Text: "done <ok>", Buttons: []chat.Button{{Label: "More", Value: "more:s1"}}})
	if err != nil {
		t.Fatal(err)
	}
	body := h.api.waitCalls(t, "editMessageText", 1)[0].json(t)
	if body["chat_id"] != float64(42) || body["message_id"] != float64(77) || body["parse_mode"] != "HTML" || body["text"] != "done &lt;ok&gt;" {
		t.Fatalf("body = %v", body)
	}
	kb, _ := json.Marshal(body["reply_markup"])
	if string(kb) != `{"inline_keyboard":[[{"callback_data":"more:s1","text":"More"}]]}` {
		t.Fatalf("reply_markup = %s", kb)
	}

	// No buttons: an empty keyboard is sent, which is what removes the
	// old one.
	if err := h.ad.Edit(context.Background(), chat.Peer{ID: "42"}, "77", chat.Outbound{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	kb, _ = json.Marshal(h.api.waitCalls(t, "editMessageText", 2)[1].json(t)["reply_markup"])
	if string(kb) != `{"inline_keyboard":[]}` {
		t.Fatalf("reply_markup with no buttons = %s", kb)
	}

	h.api.on("editMessageText", `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message"}`)
	if err := h.ad.Edit(context.Background(), chat.Peer{ID: "42"}, "77", chat.Outbound{Text: "x"}); err != nil {
		t.Fatalf("not modified is not a failure: %v", err)
	}
	h.api.on("editMessageText", `{"ok":false,"error_code":400,"description":"Bad Request: message can't be edited"}`)
	if err := h.ad.Edit(context.Background(), chat.Peer{ID: "42"}, "77", chat.Outbound{Text: "x"}); err == nil || !strings.Contains(err.Error(), "can't be edited") {
		t.Fatalf("err = %v", err)
	}
	// The HTML fallback applies to edits too.
	h.api.on("editMessageText", `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities: nope"}`, `{"ok":true,"result":true}`)
	if err := h.ad.Edit(context.Background(), chat.Peer{ID: "42"}, "77", chat.Outbound{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if calls := h.api.of("editMessageText"); len(calls) != 6 || calls[5].json(t)["parse_mode"] != nil {
		t.Fatalf("edit calls = %d", len(calls))
	}
	if err := h.ad.Edit(context.Background(), chat.Peer{ID: "42"}, "m1", chat.Outbound{Text: "x"}); err == nil {
		t.Fatal("a non-numeric ref was sent")
	}
}

func TestSendPhotoMultipart(t *testing.T) {
	h := newHarness(t, "", false)
	h.api.on("sendPhoto", `{"ok":true,"result":{"message_id":31}}`)
	png := bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 0, 1}, 100)
	ref, err := h.ad.SendImage(context.Background(), chat.Peer{ID: "42"}, png, "[3] title")
	if err != nil || ref != "31" {
		t.Fatalf("SendImage = %q, %v", ref, err)
	}
	c := h.api.waitCalls(t, "sendPhoto", 1)[0]
	mt, params, err := mime.ParseMediaType(c.ctype)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type %q", c.ctype)
	}
	form, err := multipart.NewReader(bytes.NewReader(c.body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if form.Value["chat_id"][0] != "42" || form.Value["caption"][0] != "[3] title" {
		t.Fatalf("fields = %v", form.Value)
	}
	fh := form.File["photo"]
	if len(fh) != 1 || fh[0].Filename != "screen.png" {
		t.Fatalf("photo part = %+v", fh)
	}
	f, _ := fh[0].Open()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, png) {
		t.Fatalf("photo bytes differ: %d vs %d", len(got), len(png))
	}
	h.api.on("sendPhoto", `{"ok":false,"error_code":400,"description":"Bad Request: IMAGE_PROCESS_FAILED"}`)
	if _, err := h.ad.SendImage(context.Background(), chat.Peer{ID: "42"}, png, ""); err == nil || !strings.Contains(err.Error(), "IMAGE_PROCESS_FAILED") {
		t.Fatalf("err = %v", err)
	}
}

func TestAckAndTyping(t *testing.T) {
	h := newHarness(t, "", false)
	long := strings.Repeat("y", 250)
	if err := h.ad.Ack(context.Background(), chat.Action{ID: "cb-1", Value: "v"}, long); err != nil {
		t.Fatal(err)
	}
	body := h.api.waitCalls(t, "answerCallbackQuery", 1)[0].json(t)
	if body["callback_query_id"] != "cb-1" || len(body["text"].(string)) != maxAck {
		t.Fatalf("body = %v", body)
	}
	if err := h.ad.Ack(context.Background(), chat.Action{ID: "cb-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, has := h.api.waitCalls(t, "answerCallbackQuery", 2)[1].json(t)["text"]; has {
		t.Fatal("an empty text was sent")
	}

	if err := h.ad.Typing(context.Background(), chat.Peer{ID: "42"}, true); err != nil {
		t.Fatal(err)
	}
	body = h.api.waitCalls(t, "sendChatAction", 1)[0].json(t)
	if body["chat_id"] != float64(42) || body["action"] != "typing" {
		t.Fatalf("body = %v", body)
	}
	if err := h.ad.Typing(context.Background(), chat.Peer{ID: "42"}, false); err != nil || len(h.api.of("sendChatAction")) != 1 {
		t.Fatalf("typing off made a request (err %v)", err)
	}
}

func TestErrorsCarryTelegramsWords(t *testing.T) {
	h := newHarness(t, "", false)
	h.api.on("sendChatAction", `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
	err := h.ad.Typing(context.Background(), chat.Peer{ID: "42"}, true)
	var te *Error
	if !errors.As(err, &te) || te.Code != 403 || !strings.Contains(te.Description, "blocked by the user") {
		t.Fatalf("err = %v", err)
	}
	// Not JSON at all: a proxy in the way. No panic, an error that says so.
	h.api.on("sendChatAction", `<html>login</html>`)
	if err := h.ad.Typing(context.Background(), chat.Peer{ID: "42"}, true); err == nil || !strings.Contains(err.Error(), "not a Bot API response") {
		t.Fatalf("err = %v", err)
	}
	// JSON, but not the shape asked for: still no panic.
	h.api.on("sendMessage", `{"ok":true,"result":"yes"}`)
	if _, err := h.ad.Send(context.Background(), chat.Peer{ID: "42"}, chat.Outbound{Text: "x"}); err == nil {
		t.Fatal("a result of the wrong shape was accepted")
	}
	h.api.on("sendMessage", `{"ok":false}`)
	if _, err := h.ad.Send(context.Background(), chat.Peer{ID: "42"}, chat.Outbound{Text: "x"}); err == nil || !errors.As(err, &te) || te.Code != 200 {
		t.Fatalf("err = %v", err)
	}
}

func TestPollBacksOffOnFailure(t *testing.T) {
	h := newHarness(t, "", false)
	h.ad.minWait, h.ad.maxWait = 40*time.Millisecond, 300*time.Millisecond
	// Three failures, a success, a failure: the ladder climbs 40, 80, 160
	// (the rung after would be 320, capped to 300), the success resets it,
	// and the last failure waits 40 again rather than the cap.
	h.api.on("getUpdates",
		`{"ok":false,"error_code":500,"description":"Internal Server Error"}`,
		`{"ok":false,"error_code":502,"description":"Bad Gateway"}`,
		`{"ok":false,"error_code":500,"description":"Internal Server Error"}`,
		fromChannel,
		`{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
	h.api.push(private(1, 1, "back", ""), private(2, 2, "again", ""))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.ad.Run(ctx, h.sink) }()
	defer func() { cancel(); <-done }()

	for i, want := range []string{"Internal Server Error", "Bad Gateway", "Internal Server Error"} {
		he := recv(t, h.sink.health, "health")
		if he.ok || he.err == nil || !strings.Contains(he.err.Error(), want) {
			t.Fatalf("health %d = %+v", i, he)
		}
	}
	if he := recv(t, h.sink.health, "health"); !he.ok {
		t.Fatalf("health after recovery = %+v", he)
	}
	recv(t, h.sink.in, "inbound")
	recv(t, h.sink.in, "inbound")
	if he := recv(t, h.sink.health, "health"); he.ok {
		t.Fatalf("health for the failure after recovery = %+v", he)
	}
	calls := h.api.waitCalls(t, "getUpdates", 6)
	var gaps []time.Duration
	for i := 1; i < len(calls); i++ {
		gaps = append(gaps, calls[i].at.Sub(calls[i-1].at))
	}
	// Lower bounds only: a loaded machine stretches a sleep and never
	// shortens one.
	for i, floor := range []time.Duration{40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond} {
		if gaps[i] < floor {
			t.Fatalf("gap %d = %v, want at least %v (gaps %v)", i, gaps[i], floor, gaps)
		}
	}
	// The one upper bound: without the reset this wait would be the
	// 300ms cap, and a 40ms sleep does not stretch to that.
	if gaps[4] < 40*time.Millisecond || gaps[4] >= 300*time.Millisecond {
		t.Fatalf("wait after a success then a failure = %v; the ladder did not reset (gaps %v)", gaps[4], gaps)
	}
	if !strings.Contains(h.logged(), "Bad Gateway") {
		t.Fatalf("poll failure not logged: %s", h.logged())
	}
}

func TestPollHonoursRetryAfter(t *testing.T) {
	if wait, next := nextWait(minBackoff, &Error{Code: 429, RetryAfter: 7}, minBackoff, maxBackoff); wait != 7*time.Second || next != 2*time.Second {
		t.Fatalf("429: wait %v next %v", wait, next)
	}
	wait, next := nextWait(16*time.Second, errors.New("x"), minBackoff, maxBackoff)
	if wait != 16*time.Second || next != 30*time.Second {
		t.Fatalf("ladder: wait %v next %v", wait, next)
	}
	if _, next := nextWait(30*time.Second, errors.New("x"), minBackoff, maxBackoff); next != 30*time.Second {
		t.Fatalf("cap: next %v", next)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	h := newHarness(t, "", false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.ad.Run(ctx, h.sink) }()
	h.api.waitCalls(t, "getUpdates", 1)
	cancel()
	if err := recv(t, done, "Run to return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	// Stopping is not a failure: the channel page must not show "context
	// canceled" as the last error after every reconfigure.
	none(t, h.sink.health, "health report at shutdown")
}
