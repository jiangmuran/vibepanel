package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The wire: one HTTP client for the iLink bot API and its CDN.
//
// Everything about the protocol that is a literal lives here, with the
// source it came from, because the server documents nothing and the only
// contract is what Tencent's own client (openclaw-weixin 2.4.8) sends today.
// Where community write-ups disagreed with that client, the client won; the
// commit message and the spec in the build log say which facts those were.

const (
	// defaultBase is where the QR code is fetched from, always; after a
	// sign-in the server names the base to use for everything else, which
	// can be a regional host.
	defaultBase = "https://ilinkai.weixin.qq.com"
	// defaultCDN is the fallback media host; 2.4.8 prefers the full URLs
	// the server returns and only builds one from this when it must.
	defaultCDN = "https://novac2c.cdn.weixin.qq.com/c2c"

	// channelVersion and botAgent are base_info, sent in every
	// authenticated body. The server is not known to validate either; the
	// official client sends its own version and "OpenClaw". Ours says who we
	// are, because that is the only user-agent-like field the protocol has.
	channelVersion = "vibepanel/0.1"
	botAgent       = "vibepanel/0.1"

	// appID and clientVersion are the two headers added in the 2.x client.
	// clientVersion is (2<<16 | 4<<8 | 8) for 2.4.8, in decimal; it is what
	// the server was seeing from the official client when this was written.
	appID         = "bot"
	clientVersion = "132104"
	authType      = "ilink_bot_token"

	// Timeouts of the official client, per call class. The long poll is
	// what the server holds; everything else should answer at once.
	longPollTimeout = 35 * time.Second
	sendTimeout     = 15 * time.Second
	quickTimeout    = 10 * time.Second
	mediaTimeout    = 60 * time.Second
)

type baseInfo struct {
	ChannelVersion string `json:"channel_version"`
	BotAgent       string `json:"bot_agent"`
}

func base() baseInfo { return baseInfo{ChannelVersion: channelVersion, BotAgent: botAgent} }

// retCode is the part of every response that says whether it worked.
// Both fields are checked because the official client checks both: the
// stale-token answer has been seen as ret and as errcode.
type retCode struct {
	Ret     int    `json:"ret"`
	Errcode int    `json:"errcode"`
	Errmsg  string `json:"errmsg"`
}

// apiError is a response the server answered but refused.
type apiError struct {
	Endpoint string
	Ret      int
	Errcode  int
	Errmsg   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("weixin: %s: ret %d errcode %d %s", e.Endpoint, e.Ret, e.Errcode, strings.TrimSpace(e.Errmsg))
}

// code is the one number a caller keys on: -14 is a sign-in that has
// expired, wherever the server put it.
func (e *apiError) code() int {
	if e.Ret != 0 {
		return e.Ret
	}
	return e.Errcode
}

// staleToken is the code for a bot_token the server no longer accepts.
// There is no refresh call; the only way on is a new QR scan.
const staleToken = -14

type client struct {
	http  *http.Client
	base  string
	cdn   string
	token string
	logf  func(string, ...any)
}

func endpoint(base, name string) string {
	return strings.TrimRight(base, "/") + "/ilink/bot/" + name
}

// uin is X-WECHAT-UIN: a random uint32 as a decimal string, base64. It is
// regenerated per request because the official client does; nothing is
// known to read it.
func uin() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(binary.BigEndian.Uint32(b[:])), 10)))
}

// appHeaders are the two headers every call carries, signed in or not.
func appHeaders(h http.Header) {
	h.Set("iLink-App-Id", appID)
	h.Set("iLink-App-ClientVersion", clientVersion)
}

// postHeaders are the headers of an API POST. Content-Length is left to
// net/http on purpose: the official client used to set it by hand and had
// to stop when its runtime began rejecting a pre-set value.
func (c *client) postHeaders(h http.Header) {
	appHeaders(h)
	h.Set("Content-Type", "application/json")
	h.Set("AuthorizationType", authType)
	h.Set("X-WECHAT-UIN", uin())
	if c.token != "" {
		h.Set("Authorization", "Bearer "+c.token)
	}
}

// post sends one API call and decodes the answer into out, refusing a
// non-2xx status or a non-zero ret/errcode. The raw body comes back too,
// for the caller that needs to know what the server did not say (a send
// without a message_id).
func (c *client) post(ctx context.Context, name string, body any, out any, timeout time.Duration) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(c.base, name), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.postHeaders(req.Header)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		return raw, fmt.Errorf("weixin: %s: http %d", name, res.StatusCode)
	}
	var rc retCode
	if err := json.Unmarshal(raw, &rc); err != nil {
		return raw, fmt.Errorf("weixin: %s: %w", name, err)
	}
	if rc.Ret != 0 || rc.Errcode != 0 {
		return raw, &apiError{Endpoint: name, Ret: rc.Ret, Errcode: rc.Errcode, Errmsg: rc.Errmsg}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, fmt.Errorf("weixin: %s: %w", name, err)
		}
	}
	return raw, nil
}

// timedOut reports a client-side timeout on a call, which for the long
// poll is not a failure: the server simply had nothing to say in time.
func timedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne interface{ Timeout() bool }
	return errors.As(err, &ne) && ne.Timeout()
}

// --- getupdates ---

type updatesReq struct {
	GetUpdatesBuf string   `json:"get_updates_buf"`
	BaseInfo      baseInfo `json:"base_info"`
}

type updatesResp struct {
	Msgs                 []message `json:"msgs"`
	GetUpdatesBuf        string    `json:"get_updates_buf"`
	LongpollingTimeoutMs int       `json:"longpolling_timeout_ms"`
}

// message is one WeixinMessage. Every field is optional in the official
// type, so every field is optional here; ids are numbers on the wire.
type message struct {
	Seq          int64  `json:"seq"`
	MessageID    int64  `json:"message_id"`
	FromUserID   string `json:"from_user_id"`
	ToUserID     string `json:"to_user_id"`
	ClientID     string `json:"client_id"`
	CreateTimeMs int64  `json:"create_time_ms"`
	MessageType  int    `json:"message_type"`
	MessageState int    `json:"message_state"`
	ContextToken string `json:"context_token"`
	ItemList     []item `json:"item_list"`
}

const (
	itemText  = 1
	itemImage = 2
	itemVoice = 3

	messageFromUser = 1
	messageFromBot  = 2
	stateFinished   = 2
)

type item struct {
	Type      int        `json:"type"`
	TextItem  *textItem  `json:"text_item,omitempty"`
	ImageItem *imageItem `json:"image_item,omitempty"`
	VoiceItem *voiceItem `json:"voice_item,omitempty"`
	RefMsg    *refMsg    `json:"ref_msg,omitempty"`
}

type textItem struct {
	Text string `json:"text"`
}

type cdnMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type"`
	FullURL           string `json:"full_url,omitempty"`
}

type imageItem struct {
	Media *cdnMedia `json:"media,omitempty"`
	// AESKey is the raw key as 32 hex characters, inbound only, and the
	// official client prefers it over media.aes_key when both are there.
	AESKey  string `json:"aeskey,omitempty"`
	MidSize int    `json:"mid_size,omitempty"`
}

type voiceItem struct {
	Media *cdnMedia `json:"media,omitempty"`
	// Text is the server's transcript; absent when it did not run one.
	Text string `json:"text"`
}

type refMsg struct {
	Title       string `json:"title"`
	MessageItem *item  `json:"message_item"`
}

func (c *client) getUpdates(ctx context.Context, cursor string, timeout time.Duration) (*updatesResp, error) {
	var out updatesResp
	_, err := c.post(ctx, "getupdates", updatesReq{GetUpdatesBuf: cursor, BaseInfo: base()}, &out, timeout)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// --- sendmessage ---

type sendReq struct {
	Msg      outMessage `json:"msg"`
	BaseInfo baseInfo   `json:"base_info"`
}

type outMessage struct {
	FromUserID   string `json:"from_user_id"`
	ToUserID     string `json:"to_user_id"`
	ClientID     string `json:"client_id"`
	MessageType  int    `json:"message_type"`
	MessageState int    `json:"message_state"`
	ContextToken string `json:"context_token"`
	ItemList     []item `json:"item_list"`
}

type sendResp struct {
	MessageID int64 `json:"message_id"`
}

// clientID is a fresh per-request id. The server uses it to dedupe, so
// reusing one across chunks would lose every chunk but the first.
func clientID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "vibepanel-" + hex.EncodeToString(b[:])
}

// sendItem sends exactly one item, which is what the official client does:
// a caption and an image are two requests with two client ids. It returns
// the ref to remember: the server's message_id when it gave one, else the
// client id with a warning, because a 200 without a message_id has been
// seen to mean the phone never shows the message.
func (c *client) sendItem(ctx context.Context, to, token string, it item) (string, error) {
	id := clientID()
	req := sendReq{Msg: outMessage{
		ToUserID: to, ClientID: id, MessageType: messageFromBot, MessageState: stateFinished,
		ContextToken: token, ItemList: []item{it},
	}, BaseInfo: base()}
	var out sendResp
	raw, err := c.post(ctx, "sendmessage", req, &out, sendTimeout)
	if err != nil {
		return "", err
	}
	if out.MessageID == 0 {
		c.logf("sendmessage accepted without a message_id; the phone may not show it: %s", strings.TrimSpace(string(raw)))
		return id, nil
	}
	return strconv.FormatInt(out.MessageID, 10), nil
}

// --- getconfig / sendtyping ---

type configReq struct {
	UserID       string   `json:"ilink_user_id"`
	ContextToken string   `json:"context_token,omitempty"`
	BaseInfo     baseInfo `json:"base_info"`
}

type configResp struct {
	TypingTicket string `json:"typing_ticket"`
}

func (c *client) getConfig(ctx context.Context, user, token string) (string, error) {
	var out configResp
	if _, err := c.post(ctx, "getconfig", configReq{UserID: user, ContextToken: token, BaseInfo: base()}, &out, quickTimeout); err != nil {
		return "", err
	}
	return out.TypingTicket, nil
}

type typingReq struct {
	UserID   string   `json:"ilink_user_id"`
	Ticket   string   `json:"typing_ticket"`
	Status   int      `json:"status"`
	BaseInfo baseInfo `json:"base_info"`
}

const (
	typingOn  = 1
	typingOff = 2
)

func (c *client) sendTyping(ctx context.Context, user, ticket string, status int) error {
	_, err := c.post(ctx, "sendtyping", typingReq{UserID: user, Ticket: ticket, Status: status, BaseInfo: base()}, nil, quickTimeout)
	return err
}

// notify is msg/notifystart and msg/notifystop. The official client sends
// them; nobody has seen them change anything, so a failure is logged and
// forgotten.
func (c *client) notify(ctx context.Context, name string) {
	if _, err := c.post(ctx, "msg/"+name, struct {
		BaseInfo baseInfo `json:"base_info"`
	}{base()}, nil, quickTimeout); err != nil {
		c.logf("%s: %v", name, err)
	}
}

// --- media ---

type uploadURLReq struct {
	FileKey     string   `json:"filekey"`
	MediaType   int      `json:"media_type"`
	ToUserID    string   `json:"to_user_id"`
	RawSize     int      `json:"rawsize"`
	RawFileMD5  string   `json:"rawfilemd5"`
	FileSize    int      `json:"filesize"`
	NoNeedThumb bool     `json:"no_need_thumb"`
	AESKey      string   `json:"aeskey"`
	BaseInfo    baseInfo `json:"base_info"`
}

type uploadURLResp struct {
	UploadParam   string `json:"upload_param"`
	UploadFullURL string `json:"upload_full_url"`
}

const mediaImage = 1

// maxMedia bounds a download. The official client accepts 100 MiB; a
// picture for an agent to read is a screenshot, and the bound is on what a
// server-chosen URL can make this process hold.
const maxMedia = 20 << 20

// trusted says whether a URL the server handed over may be dialled: https,
// and a host that is the IM's own (the API or CDN base this client was
// built with, or anything under weixin.qq.com). A response is not allowed
// to point the panel at a loopback or private address; a URL that fails
// the test is replaced by the one built from the query parameter.
func (c *client) trusted(target string) bool {
	if target == "" {
		return false
	}
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return false
	}
	// The hosts this client was built to talk to, scheme and all: a test
	// points both at a plain-http fake, and production at the IM's https.
	for _, base := range []string{c.base, c.cdn} {
		if bu, err := url.Parse(base); err == nil && bu.Host != "" && strings.EqualFold(bu.Scheme, u.Scheme) && strings.EqualFold(bu.Host, u.Host) {
			return true
		}
	}
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "weixin.qq.com" || strings.HasSuffix(host, ".weixin.qq.com")
}

// upload puts ciphertext on the CDN and returns the download parameter the
// receiver's client will need. The parameter comes back in a response
// header, and a 200 without it is a failure, because an image item built
// without it is a bubble the phone cannot open.
func (c *client) upload(ctx context.Context, u uploadURLResp, fileKey string, ciphertext []byte) (string, error) {
	target := u.UploadFullURL
	if !c.trusted(target) {
		q := url.Values{"encrypted_query_param": {u.UploadParam}, "filekey": {fileKey}}
		target = strings.TrimRight(c.cdn, "/") + "/upload?" + q.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(ciphertext))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("weixin: cdn upload: http %d %s", res.StatusCode, res.Header.Get("x-error-message"))
	}
	param := res.Header.Get("x-encrypted-param")
	if param == "" {
		return "", errors.New("weixin: cdn upload: no x-encrypted-param in the response")
	}
	return param, nil
}

// download fetches ciphertext (or plaintext, for an image sent without a
// key) from the CDN: the full URL when the server gave one, else the one
// built from the query parameter.
func (c *client) download(ctx context.Context, m *cdnMedia) ([]byte, error) {
	target := m.FullURL
	if !c.trusted(target) {
		q := url.Values{"encrypted_query_param": {m.EncryptQueryParam}}
		target = strings.TrimRight(c.cdn, "/") + "/download?" + q.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("weixin: cdn download: http %d", res.StatusCode)
	}
	// 100 MiB is the official client's ceiling on inbound media.
	return io.ReadAll(io.LimitReader(res.Body, maxMedia))
}
