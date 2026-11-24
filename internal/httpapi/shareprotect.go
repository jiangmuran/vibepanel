package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// Password protection and per-IP access analytics for handed-out share links.
//
// Why an outer shell + short-lived sandbox grant instead of a cookie alone:
// share pages run under `Content-Security-Policy: sandbox allow-scripts`
// (without allow-same-origin), so the document has an opaque origin (`null`).
// Subresources and fetch() from an opaque origin are cross-site, where
// SameSite=Lax cookies are withheld and third-party cookies are blocked in
// Safari, Firefox, incognito windows and mobile in-app webviews. Keeping the
// HttpOnly cookie on `/share/{token}/` (first-party top-level navigation) and
// framing the sandboxed page at `/share-u/{grant}/` keeps the address bar on
// the locked URL while serving every asset and API call without cookies.

const (
	sharedPasswordHashKey     = "sharing.password_hash"
	sharedPasswordEncKey      = "sharing.password_enc"
	sharedPasswordEpochKey    = "sharing.password_epoch"
	sharedPasswordSealContext = "share-global-password"

	minSharePasswordLen = 6
	maxSharePasswordLen = 256

	shareUnlockCookieTTL = 7 * 24 * time.Hour
	shareUnlockGrantTTL  = 4 * time.Hour

	sharedUnlockCookieName = "vp_share_shared"
	linkUnlockCookiePrefix = "vp_share_l_"

	shareAccessEventMaxAge  = 30 * 24 * 3600
	shareAccessEventMaxRows = 20000

	linkUnlockFailCap    = 30
	linkUnlockFailWindow = 5 * time.Minute
)

const (
	unlockKindSharedCookie byte = 1
	unlockKindLinkCookie   byte = 2
	unlockKindGrant        byte = 3
)

const (
	unlockModeNone   byte = 0
	unlockModeShared byte = 1
	unlockModeCustom byte = 2
)

type shareProtectState struct {
	mu             sync.Mutex
	unlockSecret   []byte
	unlockThrottle *auth.Throttle
	linkFails      map[string]*failWindow
	visitCool      *auth.Cooldown
	pollCool       *auth.Cooldown
	failEventCool  *auth.Cooldown
	actionCool     *auth.Cooldown
	trimCool       *auth.Cooldown
}

type failWindow struct {
	start time.Time
	count int
}

func (p *shareProtectState) throttle() *auth.Throttle {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unlockThrottle == nil {
		t := auth.NewThrottle()
		t.Max = 60 * time.Second
		p.unlockThrottle = t
	}
	return p.unlockThrottle
}

func (p *shareProtectState) linkFailBlocked(linkID string, now time.Time) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.linkFails == nil {
		return 0, false
	}
	fw, ok := p.linkFails[linkID]
	if !ok {
		return 0, false
	}
	if now.Sub(fw.start) >= linkUnlockFailWindow {
		delete(p.linkFails, linkID)
		return 0, false
	}
	if fw.count >= linkUnlockFailCap {
		return linkUnlockFailWindow - now.Sub(fw.start), true
	}
	return 0, false
}

func (p *shareProtectState) recordLinkFail(linkID string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.linkFails == nil {
		p.linkFails = map[string]*failWindow{}
	}
	if len(p.linkFails) > 4096 {
		for k, fw := range p.linkFails {
			if now.Sub(fw.start) >= linkUnlockFailWindow {
				delete(p.linkFails, k)
			}
		}
		if len(p.linkFails) > 4096 {
			p.linkFails = map[string]*failWindow{}
		}
	}
	fw, ok := p.linkFails[linkID]
	if !ok || now.Sub(fw.start) >= linkUnlockFailWindow {
		p.linkFails[linkID] = &failWindow{start: now, count: 1}
		return
	}
	fw.count++
}

func (p *shareProtectState) cooldowns() (visit, poll, failEv, action, trim *auth.Cooldown) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.visitCool == nil {
		p.visitCool = auth.NewCooldown(time.Minute)
		p.pollCool = auth.NewCooldown(5 * time.Minute)
		p.failEventCool = auth.NewCooldown(5 * time.Second)
		p.actionCool = auth.NewCooldown(time.Minute)
		p.trimCool = auth.NewCooldown(10 * time.Minute)
	}
	return p.visitCool, p.pollCool, p.failEventCool, p.actionCool, p.trimCool
}

func (s *Server) shareUnlockSecret(ctx context.Context) ([]byte, error) {
	s.pb.protect.mu.Lock()
	if len(s.pb.protect.unlockSecret) == 32 {
		out := append([]byte(nil), s.pb.protect.unlockSecret...)
		s.pb.protect.mu.Unlock()
		return out, nil
	}
	s.pb.protect.mu.Unlock()

	box, _ := s.secretBox()
	key, err := s.DB.EnsureShareUnlockSecret(ctx, box)
	if err != nil {
		return nil, err
	}
	s.pb.protect.mu.Lock()
	s.pb.protect.unlockSecret = append([]byte(nil), key...)
	s.pb.protect.mu.Unlock()
	return key, nil
}

// ─── signed unlock cookies & sandbox grants ────────────────────────────────

type unlockToken struct {
	Kind        byte
	Mode        store.ShareAccessMode
	ExpiresAt   int64
	GlobalEpoch int64
	LinkEpoch   int64
	LinkID      string
}

func modeToByte(m store.ShareAccessMode) byte {
	switch m {
	case store.ShareAccessShared:
		return unlockModeShared
	case store.ShareAccessCustom:
		return unlockModeCustom
	default:
		return unlockModeNone
	}
}

func byteToMode(b byte) store.ShareAccessMode {
	switch b {
	case unlockModeShared:
		return store.ShareAccessShared
	case unlockModeCustom:
		return store.ShareAccessCustom
	default:
		return store.ShareAccessNone
	}
}

func encodeUnlockToken(secretKey []byte, tok unlockToken) (string, error) {
	if len(secretKey) != 32 || len(tok.LinkID) > 255 {
		return "", errors.New("invalid unlock token parameters")
	}
	payload := make([]byte, 27+len(tok.LinkID))
	payload[0] = tok.Kind
	payload[1] = modeToByte(tok.Mode)
	binary.BigEndian.PutUint64(payload[2:10], uint64(tok.ExpiresAt))
	binary.BigEndian.PutUint64(payload[10:18], uint64(tok.GlobalEpoch))
	binary.BigEndian.PutUint64(payload[18:26], uint64(tok.LinkEpoch))
	payload[26] = byte(len(tok.LinkID))
	copy(payload[27:], tok.LinkID)

	mac := hmac.New(sha256.New, secretKey)
	mac.Write(payload)
	sig := mac.Sum(nil)

	raw := make([]byte, len(payload)+len(sig))
	copy(raw, payload)
	copy(raw[len(payload):], sig)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeUnlockToken(secretKey []byte, raw string, wantKind byte, nowUnix int64) (unlockToken, bool) {
	if len(secretKey) != 32 || raw == "" || len(raw) > 512 {
		return unlockToken{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) < 27+sha256.Size {
		return unlockToken{}, false
	}
	payload := b[:len(b)-sha256.Size]
	sig := b[len(b)-sha256.Size:]
	mac := hmac.New(sha256.New, secretKey)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return unlockToken{}, false
	}
	idLen := int(payload[26])
	if len(payload) != 27+idLen {
		return unlockToken{}, false
	}
	tok := unlockToken{
		Kind:        payload[0],
		Mode:        byteToMode(payload[1]),
		ExpiresAt:   int64(binary.BigEndian.Uint64(payload[2:10])),
		GlobalEpoch: int64(binary.BigEndian.Uint64(payload[10:18])),
		LinkEpoch:   int64(binary.BigEndian.Uint64(payload[18:26])),
		LinkID:      string(payload[27:]),
	}
	if tok.Kind != wantKind {
		return unlockToken{}, false
	}
	if tok.ExpiresAt <= nowUnix {
		return unlockToken{}, false
	}
	return tok, true
}

// ─── password validation & settings helpers ────────────────────────────────

func validateSharePassword(pw string) error {
	runes := []rune(pw)
	if len(runes) < minSharePasswordLen {
		return fmt.Errorf("password must be at least %d characters", minSharePasswordLen)
	}
	if len(pw) > maxSharePasswordLen {
		return fmt.Errorf("password is too long (max %d bytes)", maxSharePasswordLen)
	}
	for _, r := range runes {
		if unicode.IsControl(r) {
			return errors.New("password cannot contain control characters")
		}
	}
	return nil
}

func linkPasswordSealContext(linkID string) string {
	return "share-link-password:" + linkID
}

func (s *Server) sealLinkPassword(linkID, password string) []byte {
	box, err := s.secretBox()
	if err != nil {
		s.Log.Warn("share link password will not be viewable", "err", err)
		return nil
	}
	return box.Seal([]byte(password), linkPasswordSealContext(linkID))
}

func (s *Server) sealSharedPassword(password string) string {
	box, err := s.secretBox()
	if err != nil {
		s.Log.Warn("shared share password will not be viewable", "err", err)
		return ""
	}
	return base64.StdEncoding.EncodeToString(box.Seal([]byte(password), sharedPasswordSealContext))
}

func (s *Server) sharedPasswordState(ctx context.Context) (hash string, viewable bool, epoch int64, err error) {
	hash, err = s.DB.GetSetting(ctx, sharedPasswordHashKey, "")
	if err != nil {
		return "", false, 0, err
	}
	enc, err := s.DB.GetSetting(ctx, sharedPasswordEncKey, "")
	if err != nil {
		return "", false, 0, err
	}
	epRaw, err := s.DB.GetSetting(ctx, sharedPasswordEpochKey, "1")
	if err != nil {
		return "", false, 0, err
	}
	epoch, _ = strconv.ParseInt(epRaw, 10, 64)
	if epoch < 1 {
		epoch = 1
	}
	return hash, hash != "" && enc != "", epoch, nil
}

func (s *Server) bumpSharedPasswordEpoch(ctx context.Context) (int64, error) {
	_, _, epoch, err := s.sharedPasswordState(ctx)
	if err != nil {
		return 0, err
	}
	next := epoch + 1
	if err := s.DB.SetSetting(ctx, sharedPasswordEpochKey, strconv.FormatInt(next, 10)); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *Server) defaultShareAccessMode(ctx context.Context) store.ShareAccessMode {
	hash, _, _, err := s.sharedPasswordState(ctx)
	if err == nil && hash != "" {
		return store.ShareAccessShared
	}
	return store.ShareAccessNone
}

func linkUnlockCookieName(linkID string) string {
	var b strings.Builder
	b.WriteString(linkUnlockCookiePrefix)
	for _, r := range linkID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Server) shareCookieSecure(r *http.Request) bool {
	if s.cookieSecureFor(r) {
		return true
	}
	origin, onShareHost := s.isShareHost(r)
	return onShareHost && strings.HasPrefix(origin, "https://")
}

func (s *Server) linkRequiresPassword(link store.ShareLink) bool {
	return link.Purpose == "" && link.AccessMode != "" && link.AccessMode != string(store.ShareAccessNone)
}

// verifyShareUnlockCookie reports whether r carries a valid, non-expired unlock
// cookie matching link's current mode and epoch.
func (s *Server) verifyShareUnlockCookie(ctx context.Context, r *http.Request, link store.ShareLink, nowUnix int64) (globalEpoch int64, ok bool) {
	key, err := s.shareUnlockSecret(ctx)
	if err != nil {
		return 0, false
	}
	mode := store.ShareAccessMode(link.AccessMode)
	switch mode {
	case store.ShareAccessShared:
		hash, _, ep, serr := s.sharedPasswordState(ctx)
		if serr != nil || hash == "" {
			return 0, false
		}
		c, cerr := r.Cookie(sharedUnlockCookieName)
		if cerr != nil || c.Value == "" {
			return 0, false
		}
		tok, valid := decodeUnlockToken(key, c.Value, unlockKindSharedCookie, nowUnix)
		if !valid || tok.Mode != store.ShareAccessShared || tok.GlobalEpoch != ep {
			return 0, false
		}
		return ep, true
	case store.ShareAccessCustom:
		c, cerr := r.Cookie(linkUnlockCookieName(link.ID))
		if cerr != nil || c.Value == "" {
			return 0, false
		}
		tok, valid := decodeUnlockToken(key, c.Value, unlockKindLinkCookie, nowUnix)
		if !valid || tok.Mode != store.ShareAccessCustom || tok.LinkID != link.ID || tok.LinkEpoch != link.PasswordEpoch {
			return 0, false
		}
		return 0, true
	default:
		return 0, false
	}
}

func (s *Server) mintShareUnlockGrant(ctx context.Context, link store.ShareLink, globalEpoch int64, now time.Time) (string, error) {
	key, err := s.shareUnlockSecret(ctx)
	if err != nil {
		return "", err
	}
	return encodeUnlockToken(key, unlockToken{
		Kind:        unlockKindGrant,
		Mode:        store.ShareAccessMode(link.AccessMode),
		ExpiresAt:   now.Add(shareUnlockGrantTTL).Unix(),
		GlobalEpoch: globalEpoch,
		LinkEpoch:   link.PasswordEpoch,
		LinkID:      link.ID,
	})
}

// resolveShareUnlockGrant validates a short-lived sandbox grant against the
// current database row and password epochs.
func (s *Server) resolveShareUnlockGrant(ctx context.Context, rawGrant string, nowUnix int64) (store.ShareLink, []byte, string, error) {
	key, err := s.shareUnlockSecret(ctx)
	if err != nil {
		return store.ShareLink{}, nil, "", err
	}
	tok, ok := decodeUnlockToken(key, rawGrant, unlockKindGrant, nowUnix)
	if !ok || tok.LinkID == "" {
		return store.ShareLink{}, nil, "grant_expired", store.ErrNotFound
	}
	rec, err := s.DB.ShareLinkAuthByID(ctx, tok.LinkID)
	if err != nil {
		return store.ShareLink{}, nil, "", err
	}
	if store.ShareAccessMode(rec.Link.AccessMode) != tok.Mode {
		return store.ShareLink{}, nil, "password_changed", store.ErrNotFound
	}
	if rec.Link.PasswordEpoch != tok.LinkEpoch {
		return store.ShareLink{}, nil, "password_changed", store.ErrNotFound
	}
	if tok.Mode == store.ShareAccessShared {
		hash, _, ep, serr := s.sharedPasswordState(ctx)
		if serr != nil {
			return store.ShareLink{}, nil, "", serr
		}
		if hash == "" || ep != tok.GlobalEpoch {
			return store.ShareLink{}, nil, "password_changed", store.ErrNotFound
		}
	} else if tok.Mode == store.ShareAccessCustom {
		if rec.PasswordHash == "" {
			return store.ShareLink{}, nil, "password_changed", store.ErrNotFound
		}
	} else {
		return store.ShareLink{}, nil, "", store.ErrNotFound
	}
	return rec.Link, rec.TokenHash, "", nil
}

// ─── unlock gate & sandbox shell pages ─────────────────────────────────────

const passwordGateHTML = `<!doctype html><html lang="%s"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow"><title>%s</title>
<style>
:root{color-scheme:light dark;--bg:#f5f5f4;--card:#ffffff;--border:#e5e5e4;--ink:#18181b;--muted:#71717a;--accent:#2563eb;--accent-ink:#ffffff;--err:#dc2626;--err-bg:rgba(220,38,38,.09)}
@media (prefers-color-scheme:dark){:root{--bg:#0d0e11;--card:#15171c;--border:#262932;--ink:#eceef2;--muted:#9499a8;--accent:#4f7cff;--accent-ink:#ffffff;--err:#f87171;--err-bg:rgba(248,113,113,.12)}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:1.25rem;background:var(--bg);color:var(--ink);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
main{width:100%%;max-width:22rem;background:var(--card);border:1px solid var(--border);border-radius:14px;padding:1.5rem;box-shadow:0 8px 30px rgba(0,0,0,.08)}
.ico{width:38px;height:38px;border-radius:10px;background:var(--bg);border:1px solid var(--border);display:grid;place-items:center;margin-bottom:1rem;color:var(--accent)}
h1{font-size:1.125rem;font-weight:600;margin:0 0 .35rem;letter-spacing:-.01em}
p{margin:0 0 1.1rem;font-size:.875rem;color:var(--muted)}
label{display:block;font-size:.8125rem;font-weight:500;margin-bottom:.4rem;color:var(--ink)}
input{width:100%%;padding:.625rem .75rem;border:1px solid var(--border);border-radius:9px;background:var(--bg);color:var(--ink);font:inherit;outline:none}
input:focus{border-color:var(--accent);box-shadow:0 0 0 3px rgba(79,124,255,.18)}
button{margin-top:.85rem;width:100%%;padding:.65rem 1rem;border:0;border-radius:9px;background:var(--accent);color:var(--accent-ink);font:inherit;font-weight:500;cursor:pointer}
button:hover{opacity:.94}
.err{margin:0 0 .9rem;padding:.6rem .75rem;border-radius:9px;background:var(--err-bg);color:var(--err);font-size:.8125rem}
</style></head><body><main>
<div class="ico" aria-hidden="true"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/></svg></div>
<h1>%s</h1>
<p>%s</p>
%s
<form method="POST" action="unlock">
<label for="pw">%s</label>
<input id="pw" name="password" type="password" autocomplete="off" required autofocus placeholder="%s">
<button type="submit">%s</button>
</form></main></body></html>
`

func prefersZh(r *http.Request) bool {
	al := strings.ToLower(r.Header.Get("Accept-Language"))
	if al == "" {
		return false
	}
	for _, part := range strings.Split(al, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if strings.HasPrefix(tag, "zh") {
			return true
		}
		if strings.HasPrefix(tag, "en") {
			return false
		}
	}
	return strings.Contains(al, "zh")
}

func writePasswordGatePage(w http.ResponseWriter, r *http.Request, status int, errKind string, retryAfter int) {
	zh := prefersZh(r)
	lang := "en"
	title := "Protected share link"
	heading := "Password required"
	subtitle := "Enter the access password to view this shared page."
	label := "Access password"
	placeholder := "Enter password"
	submit := "Unlock"
	if zh {
		lang = "zh-CN"
		title = "受保护的分享页面"
		heading = "需要访问密码"
		subtitle = "此分享链接已开启密码保护，请输入密码继续查看。"
		label = "访问密码"
		placeholder = "输入访问密码"
		submit = "解锁查看"
	}
	errHTML := ""
	switch errKind {
	case "wrong":
		msg := "Incorrect password. Please try again."
		if zh {
			msg = "密码错误，请重新输入。"
		}
		errHTML = `<div class="err" role="alert">` + html.EscapeString(msg) + `</div>`
	case "throttled":
		if retryAfter < 1 {
			retryAfter = 1
		}
		msg := fmt.Sprintf("Too many failed attempts. Please wait %d seconds and try again.", retryAfter)
		if zh {
			msg = fmt.Sprintf("输错次数过多，请等待 %d 秒后再试。", retryAfter)
		}
		errHTML = `<div class="err" role="alert">` + html.EscapeString(msg) + `</div>`
	case "unconfigured":
		msg := "This link requires a shared password that is currently unavailable. Ask the owner."
		if zh {
			msg = "此链接依赖的共用密码当前未配置，请联系分享者。"
		}
		errHTML = `<div class="err" role="alert">` + html.EscapeString(msg) + `</div>`
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	h.Set("Permissions-Policy", sharePagePermissions)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	if retryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, passwordGateHTML, lang, title, heading, subtitle, errHTML, label, placeholder, submit)
}

const unlockShellHTML = `<!doctype html><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>vibepanel</title>
<style>html,body{margin:0;padding:0;height:100%%;overflow:hidden;background:#0d0e11}iframe{position:fixed;inset:0;width:100%%;height:100%%;border:0}</style>
<iframe id="vp-frame" src="/share-u/%s/" sandbox="allow-scripts" referrerpolicy="no-referrer"></iframe>
<script>(function(){var f=document.getElementById('vp-frame'),done=false;window.addEventListener('message',function(e){if(!done&&f&&e.source===f.contentWindow&&e.data&&e.data.type==='vp.reauth'){done=true;location.reload()}})})();</script>
`

func writeUnlockShellPage(w http.ResponseWriter, grant string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-src 'self'; frame-ancestors 'none'")
	h.Set("Permissions-Policy", sharePagePermissions)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, unlockShellHTML, grant)
}

// ─── unlock route & sandbox grant routes ───────────────────────────────────

// handleShareUnlock handles POST /share/{token}/unlock.
func (s *Server) handleShareUnlock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := s.clientIP(r)
	if s.refuseAddress(ctx, w, r) {
		return
	}
	token := chi.URLParam(r, "token")
	link, err := s.DB.ShareLinkByToken(ctx, auth.HashToken(token))
	_, onShareHost := s.isShareHost(r)
	if err != nil || link.PageID == "" || (onShareHost && link.Purpose != "") {
		down := err != nil && !errors.Is(err, store.ErrNotFound)
		if down {
			s.noteStale(err)
		} else {
			s.auditFromOutside(ctx, "share.rejected", "", ip, "unknown or expired share link")
		}
		writeGonePage(w, down)
		return
	}
	if !s.linkRequiresPassword(link) {
		http.Redirect(w, r, "/share/"+token+"/", http.StatusSeeOther)
		return
	}

	now := time.Now()
	throttleKey := ip + "|" + link.ID
	if wait, blocked := s.pb.protect.throttle().Delay(throttleKey, now); blocked {
		secs := int(wait.Seconds() + 0.999)
		writePasswordGatePage(w, r, http.StatusTooManyRequests, "throttled", secs)
		return
	}
	if wait, blocked := s.pb.protect.linkFailBlocked(link.ID, now); blocked {
		secs := int(wait.Seconds() + 0.999)
		writePasswordGatePage(w, r, http.StatusTooManyRequests, "throttled", secs)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		writePasswordGatePage(w, r, http.StatusBadRequest, "wrong", 0)
		return
	}
	password := r.FormValue("password")

	var targetHash string
	var globalEpoch int64
	mode := store.ShareAccessMode(link.AccessMode)
	switch mode {
	case store.ShareAccessShared:
		var serr error
		targetHash, _, globalEpoch, serr = s.sharedPasswordState(ctx)
		if serr != nil {
			s.noteStale(serr)
			writeGonePage(w, true)
			return
		}
		if targetHash == "" {
			writePasswordGatePage(w, r, http.StatusForbidden, "unconfigured", 0)
			return
		}
	case store.ShareAccessCustom:
		rec, aerr := s.DB.ShareLinkAuthByID(ctx, link.ID)
		if aerr != nil {
			down := !errors.Is(aerr, store.ErrNotFound)
			if down {
				s.noteStale(aerr)
			}
			writeGonePage(w, down)
			return
		}
		targetHash = rec.PasswordHash
		if targetHash == "" {
			writePasswordGatePage(w, r, http.StatusForbidden, "unconfigured", 0)
			return
		}
	default:
		http.Redirect(w, r, "/share/"+token+"/", http.StatusSeeOther)
		return
	}

	matched, verr := auth.VerifyPassword(password, targetHash)
	if verr != nil || !matched {
		s.pb.protect.throttle().Fail(throttleKey, now)
		s.pb.protect.recordLinkFail(link.ID, now)
		s.recordShareAccess(ctx, link.ID, ip, "unlock_fail", r.UserAgent(), "wrong password")
		s.auditFromOutside(ctx, "share.unlock_failed", "", ip, link.Name)
		writePasswordGatePage(w, r, http.StatusUnauthorized, "wrong", 0)
		return
	}

	s.pb.protect.throttle().Succeed(throttleKey)
	key, err := s.shareUnlockSecret(ctx)
	if err != nil {
		s.noteStale(err)
		writeGonePage(w, true)
		return
	}
	exp := now.Add(shareUnlockCookieTTL).Unix()
	var cookieName, cookiePath, cookieVal string
	if mode == store.ShareAccessShared {
		cookieName = sharedUnlockCookieName
		cookiePath = "/share/"
		cookieVal, err = encodeUnlockToken(key, unlockToken{
			Kind:        unlockKindSharedCookie,
			Mode:        store.ShareAccessShared,
			ExpiresAt:   exp,
			GlobalEpoch: globalEpoch,
		})
	} else {
		cookieName = linkUnlockCookieName(link.ID)
		cookiePath = "/share/" + token + "/"
		cookieVal, err = encodeUnlockToken(key, unlockToken{
			Kind:      unlockKindLinkCookie,
			Mode:      store.ShareAccessCustom,
			ExpiresAt: exp,
			LinkEpoch: link.PasswordEpoch,
			LinkID:    link.ID,
		})
	}
	if err != nil {
		writeGonePage(w, true)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    cookieVal,
		Path:     cookiePath,
		MaxAge:   int(shareUnlockCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.shareCookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
	s.recordShareAccess(ctx, link.ID, ip, "unlock_ok", r.UserAgent(), "")
	http.Redirect(w, r, "/share/"+token+"/", http.StatusSeeOther)
}

// registerShareUnlockPageRoutes mounts `/share-u/{grant}/*` for sandboxed
// frames minted after passing the password gate.
func (s *Server) registerShareUnlockPageRoutes(r chi.Router) {
	r.Get("/share-u/{grant}", s.handleShareUnlockPage)
	r.Get("/share-u/{grant}/*", s.handleShareUnlockPage)
}

// registerShareUnlockAPIRoutes mounts `/api/share-u/{grant}/v1/*` mirroring
// the exact capabilities of `/api/share/{token}/v1/*`.
func (s *Server) registerShareUnlockAPIRoutes(r chi.Router) {
	r.Route("/share-u/{grant}", func(r chi.Router) {
		r.Use(shareReadableAnywhere)
		r.Use(s.requireShareUnlockGrant)
		r.Get("/v1/snapshot", s.handleShareSnapshot)
		r.Post("/v1/actions/{name}", s.handleVisitorAction)
		r.Options("/v1/actions/{name}", s.handleActionPreflight)
	})
}

func (s *Server) requireShareUnlockGrant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ip := s.clientIP(r)
		if s.refuseAddress(ctx, w, r) {
			return
		}
		grant := chi.URLParam(r, "grant")
		link, tokenHash, reason, err := s.resolveShareUnlockGrant(ctx, grant, time.Now().Unix())
		if errors.Is(err, store.ErrNotFound) {
			code := "invalid_grant"
			if reason != "" {
				code = reason
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "this session expired; reload to unlock again",
				"code":  code,
			})
			return
		}
		if err != nil {
			s.noteStale(err)
			writeErr(w, http.StatusServiceUnavailable, "the panel cannot reach its own database")
			return
		}
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		now := time.Now()
		if s.shareCooldowns().Allow("share.touch", link.ID, now) {
			if terr := s.DB.TouchShareLink(ctx, link.ID); terr != nil {
				s.Log.Debug("touch share link", "err", terr)
			}
		}
		s.recordShareAccess(ctx, link.ID, ip, "poll", r.UserAgent(), "")
		raw, vw, vh := shareViewerFrom(r, ip)
		s.viewers.saw(link.ID, shareID(tokenHash, raw), vw, vh, now)

		next.ServeHTTP(w, r.WithContext(
			context.WithValue(ctx, shareContextKey{}, shareContext{link: link, secret: tokenHash})))
	})
}

// handleShareUnlockPage serves one static file of a password-protected share
// page inside the sandboxed iframe.
func (s *Server) handleShareUnlockPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.refuseAddress(ctx, w, r) {
		return
	}
	grant := chi.URLParam(r, "grant")
	link, _, _, err := s.resolveShareUnlockGrant(ctx, grant, time.Now().Unix())
	if err != nil || link.PageID == "" {
		down := err != nil && !errors.Is(err, store.ErrNotFound)
		if down {
			s.noteStale(err)
		}
		writeGonePage(w, down)
		return
	}
	rel := chi.URLParam(r, "*")
	if !strings.HasSuffix(r.URL.Path, "/") && rel == "" {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusPermanentRedirect)
		return
	}
	s.serveSharePageFile(w, r, link, rel, "/share-u/"+grant+"/", "/api/share-u/"+grant+"/v1/", true)
}

// ─── access analytics & UA summary ─────────────────────────────────────────

func (s *Server) recordShareAccess(ctx context.Context, linkID, ip, event, rawUA, detail string) {
	if linkID == "" || ip == "" || s.DB == nil {
		return
	}
	now := time.Now()
	visitCool, pollCool, failEvCool, actionCool, trimCool := s.pb.protect.cooldowns()
	ua := summarizeUA(rawUA)

	recordEvent := true
	switch event {
	case "visit":
		if !visitCool.Allow("visit", linkID+"|"+ip, now) {
			return
		}
		if s.shareCooldowns().Allow("share.touch", linkID, now) {
			_ = s.DB.TouchShareLink(ctx, linkID)
		}
	case "unlock_ok":
		if s.shareCooldowns().Allow("share.touch", linkID, now) {
			_ = s.DB.TouchShareLink(ctx, linkID)
		}
	case "unlock_fail":
		recordEvent = failEvCool.Allow("fail", linkID+"|"+ip, now)
	case "action":
		if !actionCool.Allow("act", linkID+"|"+ip+"|"+detail, now) {
			return
		}
	case "poll":
		if !pollCool.Allow("poll", linkID+"|"+ip, now) {
			return
		}
		recordEvent = false
	}

	if err := s.DB.RecordShareAccess(ctx, linkID, ip, event, ua, detail, recordEvent); err != nil {
		s.Log.Debug("record share access", "link", linkID, "event", event, "err", err)
		return
	}
	if recordEvent && trimCool.Allow("trim", "share_access_events", now) {
		if err := s.DB.TrimShareAccessEvents(ctx, shareAccessEventMaxAge, shareAccessEventMaxRows); err != nil {
			s.Log.Debug("trim share access events", "err", err)
		}
	}
}

// summarizeUA distills a raw User-Agent header into a short readable client+OS
// label for the owner's access statistics table.
func summarizeUA(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)

	var client string
	switch {
	case strings.Contains(low, "micromessenger"):
		client = "WeChat"
	case strings.Contains(low, "lark") || strings.Contains(low, "feishu"):
		client = "Feishu"
	case strings.Contains(low, "dingtalk"):
		client = "DingTalk"
	case strings.Contains(low, "curl/"):
		return "curl"
	case strings.Contains(low, "wget/"):
		return "wget"
	case strings.Contains(low, "go-http-client"):
		return "Go-http-client"
	case strings.Contains(low, "python-requests") || strings.Contains(low, "python-urllib"):
		return "Python"
	case strings.Contains(low, "edg/") || strings.Contains(low, "edgios/") || strings.Contains(low, "edga/"):
		client = "Edge"
	case strings.Contains(low, "firefox/") || strings.Contains(low, "fxios/"):
		client = "Firefox"
	case strings.Contains(low, "chrome/") || strings.Contains(low, "crios/"):
		client = "Chrome"
	case strings.Contains(low, "safari/"):
		client = "Safari"
	default:
		// Strip control chars and take first token as fallback.
		tok := strings.Fields(s)[0]
		var clean strings.Builder
		for _, r := range tok {
			if !unicode.IsControl(r) {
				clean.WriteRune(r)
			}
		}
		out := clean.String()
		if len([]rune(out)) > 32 {
			out = string([]rune(out)[:32])
		}
		return out
	}

	var osName string
	switch {
	case strings.Contains(low, "iphone") || strings.Contains(low, "ipad") || strings.Contains(low, "ipod"):
		osName = "iOS"
	case strings.Contains(low, "android"):
		osName = "Android"
	case strings.Contains(low, "mac os x") || strings.Contains(low, "macintosh"):
		osName = "macOS"
	case strings.Contains(low, "windows"):
		osName = "Windows"
	case strings.Contains(low, "linux"):
		osName = "Linux"
	}
	if osName == "" {
		return client
	}
	return client + " · " + osName
}

// ─── owner admin handlers (behind RequireAuth) ─────────────────────────────

func (s *Server) handleGetSharedPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hash, _, _, err := s.sharedPasswordState(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if hash == "" {
		writeErr(w, http.StatusNotFound, "shared password is not set")
		return
	}
	encB64, err := s.DB.GetSetting(ctx, sharedPasswordEncKey, "")
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if encB64 == "" {
		writeErr(w, http.StatusConflict, "shared password cannot be viewed; set a new one to make it copyable")
		return
	}
	enc, err := base64.StdEncoding.DecodeString(encB64)
	if err != nil {
		writeErr(w, http.StatusConflict, "shared password copy is unreadable; set a new one")
		return
	}
	box, err := s.secretBox()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "the panel's secrets key cannot be read: "+err.Error())
		return
	}
	pw, err := box.Unseal(enc, sharedPasswordSealContext)
	if err != nil {
		writeErr(w, http.StatusConflict, "shared password does not open under the panel's current key; set a new one")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"password": string(pw)})
}

func (s *Server) handlePutSharedPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.Password == "" {
		// Clearing the shared password while active links still rely on it
		// would silently break or expose those links; require the owner to
		// switch them first.
		inUse, err := s.DB.CountShareLinksByAccessMode(ctx, store.ShareAccessShared)
		if err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if inUse > 0 {
			writeErr(w, http.StatusConflict,
				fmt.Sprintf("%d share link(s) currently use the shared password; switch them before clearing it", inUse))
			return
		}
		if err := s.DB.SetSetting(ctx, sharedPasswordHashKey, ""); err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if err := s.DB.SetSetting(ctx, sharedPasswordEncKey, ""); err != nil {
			s.writeStoreErr(w, err)
			return
		}
		_, _ = s.bumpSharedPasswordEpoch(ctx)
		if u, ok := currentUserFrom(r); ok {
			s.audit(ctx, "sharing.password_cleared", u.Username, s.clientIP(r), "")
		}
		writeJSON(w, http.StatusOK, s.buildSharingSettings(ctx))
		return
	}

	if err := validateSharePassword(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.DB.SetSetting(ctx, sharedPasswordHashKey, hash); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if err := s.DB.SetSetting(ctx, sharedPasswordEncKey, s.sealSharedPassword(req.Password)); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if _, err := s.bumpSharedPasswordEpoch(ctx); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "sharing.password_set", u.Username, s.clientIP(r), "shared password updated")
	}
	writeJSON(w, http.StatusOK, s.buildSharingSettings(ctx))
}

func (s *Server) handleApplySharedPasswordAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hash, _, _, err := s.sharedPasswordState(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if hash == "" {
		writeErr(w, http.StatusConflict, "set a shared password first")
		return
	}
	n, err := s.DB.ApplySharedPasswordToPublicLinks(ctx)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "sharing.password_applied_all", u.Username, s.clientIP(r), fmt.Sprintf("%d link(s)", n))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updated":  n,
		"settings": s.buildSharingSettings(ctx),
	})
}

func (s *Server) handleRevokeSharedSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := s.bumpSharedPasswordEpoch(ctx); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "sharing.sessions_revoked", u.Username, s.clientIP(r), "all shared-password viewers")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUpdateShareAccess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	linkID := chi.URLParam(r, "shareID")
	link, err := s.DB.ShareLinkByID(ctx, linkID)
	if err != nil || link.Purpose != "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		AccessMode     string `json:"accessMode"`
		Password       string `json:"password"`
		RevokeSessions bool   `json:"revokeSessions"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.RevokeSessions && req.AccessMode == "" && req.Password == "" {
		if err := s.DB.BumpShareLinkPasswordEpoch(ctx, linkID); err != nil {
			s.writeStoreErr(w, err)
			return
		}
		if u, ok := currentUserFrom(r); ok {
			s.audit(ctx, "share.sessions_revoked", u.Username, s.clientIP(r), link.Name)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	mode := store.ShareAccessMode(strings.TrimSpace(req.AccessMode))
	if !store.ValidShareAccessMode(mode) {
		writeErr(w, http.StatusBadRequest, "accessMode must be none, shared or custom")
		return
	}
	var pwHash string
	var pwEnc []byte
	keepExisting := false
	switch mode {
	case store.ShareAccessShared:
		hash, _, _, serr := s.sharedPasswordState(ctx)
		if serr != nil {
			s.writeStoreErr(w, serr)
			return
		}
		if hash == "" {
			writeErr(w, http.StatusBadRequest, "set a shared password before using shared mode")
			return
		}
	case store.ShareAccessCustom:
		if req.Password == "" {
			if link.AccessMode == string(store.ShareAccessCustom) {
				keepExisting = true
			} else {
				writeErr(w, http.StatusBadRequest, "a custom password is required")
				return
			}
		} else {
			if err := validateSharePassword(req.Password); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			pwHash, err = auth.HashPassword(req.Password)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "could not hash password")
				return
			}
			pwEnc = s.sealLinkPassword(linkID, req.Password)
		}
	}

	if err := s.DB.SetShareLinkAccess(ctx, linkID, mode, pwHash, pwEnc, keepExisting); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(ctx, "share.access_changed", u.Username, s.clientIP(r), link.Name+" -> "+string(mode))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetShareLinkPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	linkID := chi.URLParam(r, "shareID")
	enc, err := s.DB.ShareLinkPasswordEnc(ctx, linkID)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if len(enc) == 0 {
		writeErr(w, http.StatusConflict, "this link has no viewable custom password")
		return
	}
	box, err := s.secretBox()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "the panel's secrets key cannot be read: "+err.Error())
		return
	}
	pw, err := box.Unseal(enc, linkPasswordSealContext(linkID))
	if err != nil {
		writeErr(w, http.StatusConflict, "this link's password does not open under the panel's current key; set a new one")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"password": string(pw)})
}

type shareAccessStatsResponse struct {
	LinkID          string                   `json:"linkId"`
	UniqueIPs       int                      `json:"uniqueIps"`
	TotalVisits     int                      `json:"totalVisits"`
	TotalUnlockOK   int                      `json:"totalUnlockOk"`
	TotalUnlockFail int                      `json:"totalUnlockFail"`
	LoopbackOnly    bool                     `json:"loopbackOnly"`
	IPs             []store.ShareAccessIP    `json:"ips"`
	Events          []store.ShareAccessEvent `json:"events"`
}

func (s *Server) handleShareAccessStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	linkID := chi.URLParam(r, "shareID")
	link, err := s.DB.ShareLinkByID(ctx, linkID)
	if err != nil || link.Purpose != "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	ips, err := s.DB.ShareAccessIPs(ctx, linkID, 200)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	events, err := s.DB.ShareAccessEvents(ctx, linkID, 100)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	out := shareAccessStatsResponse{
		LinkID: linkID,
		IPs:    ips,
		Events: events,
	}
	nonLoopback := 0
	for _, row := range ips {
		out.UniqueIPs++
		out.TotalVisits += row.VisitCount
		out.TotalUnlockOK += row.UnlockOKCount
		out.TotalUnlockFail += row.UnlockFailCount
		if row.IP != "127.0.0.1" && row.IP != "::1" && row.IP != "" {
			nonLoopback++
		}
	}
	out.LoopbackOnly = out.UniqueIPs > 0 && nonLoopback == 0
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) buildSharingSettings(ctx context.Context) sharingSettingsResponse {
	origin, _, _ := s.shareOrigin(ctx)
	hash, viewable, _, _ := s.sharedPasswordState(ctx)
	pubCount, _ := s.DB.CountShareLinksByAccessMode(ctx, store.ShareAccessNone)
	loopbackOnly, _ := s.DB.HasOnlyLoopbackShareAccess(ctx, time.Now().Add(-7*24*time.Hour).Unix())
	return sharingSettingsResponse{
		VisitorWrites:          s.visitorWritesAllowed(ctx),
		ShareOrigin:            origin,
		SharedPasswordSet:      hash != "",
		SharedPasswordViewable: viewable,
		PublicLinkCount:        pubCount,
		LoopbackOnlyWarning:    loopbackOnly,
	}
}

// Keep io import check happy if unused — read limited form body uses MaxBytesReader.
var _ = io.EOF
