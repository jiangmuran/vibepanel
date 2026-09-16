package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// Signing in: a QR code, a phone, and a status poll the server holds open.
//
// The adapter keeps each sign-in in memory under the qrcode the server
// issued, because the settings page polls by that id and a verification
// code typed on the page has to reach the next poll. There is no reason to
// persist any of it: an unfinished sign-in after a restart is a new QR
// code, which is what the page shows anyway.

// loginTTL is how long an unfinished sign-in is remembered. The official
// client gives the whole flow 480 s; ten minutes covers that with margin.
const loginTTL = 10 * time.Minute

type login struct {
	qrcode string
	// host is where the status is polled: the fixed base until the server
	// says scaned_but_redirect, then the host it named.
	host    string
	code    string
	started time.Time
}

type qrResp struct {
	QRCode    string `json:"qrcode"`
	QRCodeImg string `json:"qrcode_img_content"`
}

type statusResp struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token"`
	BotID        string `json:"ilink_bot_id"`
	UserID       string `json:"ilink_user_id"`
	BaseURL      string `json:"baseurl"`
	RedirectHost string `json:"redirect_host"`
}

// StartLogin asks for a QR code. The 2.4.8 client POSTs with a list of
// tokens it already holds so the server can say "already bound"; a fresh
// install sends none. A server that only knows the older GET answers 404
// or 405 to the POST, and the GET is tried then.
func (a *Adapter) StartLogin(ctx context.Context) (chat.Login, error) {
	a.pruneLogins()
	target := endpoint(a.qrBase, "get_bot_qrcode") + "?bot_type=3"
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	res, err := a.qrRequest(ctx, http.MethodPost, target)
	if err != nil {
		return chat.Login{}, err
	}
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
		res.Body.Close()
		if res, err = a.qrRequest(ctx, http.MethodGet, target); err != nil {
			return chat.Login{}, err
		}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return chat.Login{}, err
	}
	if res.StatusCode/100 != 2 {
		return chat.Login{}, fmt.Errorf("weixin: get_bot_qrcode: http %d", res.StatusCode)
	}
	var qr qrResp
	if err := json.Unmarshal(raw, &qr); err != nil {
		return chat.Login{}, fmt.Errorf("weixin: get_bot_qrcode: %w", err)
	}
	if qr.QRCode == "" || qr.QRCodeImg == "" {
		return chat.Login{}, errors.New("weixin: get_bot_qrcode: no qrcode in the answer")
	}
	a.loginMu.Lock()
	a.logins[qr.QRCode] = &login{qrcode: qr.QRCode, host: a.qrBase, started: a.now()}
	a.loginMu.Unlock()
	return chat.Login{ID: qr.QRCode, QRURL: qr.QRCodeImg, Status: "waiting"}, nil
}

func (a *Adapter) qrRequest(ctx context.Context, method, target string) (*http.Response, error) {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{"local_token_list":[]}`)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	// The POST headers minus Authorization: there is no token yet.
	appHeaders(req.Header)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("AuthorizationType", authType)
		req.Header.Set("X-WECHAT-UIN", uin())
	}
	return a.c.http.Do(req)
}

func (a *Adapter) pruneLogins() {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	for id, l := range a.logins {
		if a.now().Sub(l.started) > loginTTL {
			delete(a.logins, id)
		}
	}
}

// LoginStatus polls once. The server holds the request until something
// happens or 35 s pass, so the page's poll is slow by design; a timeout or
// a network error is "still waiting", not a failure, because the phone
// may be halfway through and a red page would make the person start over.
func (a *Adapter) LoginStatus(ctx context.Context, id string) (chat.Login, error) {
	a.loginMu.Lock()
	l, ok := a.logins[id]
	var host, code string
	if ok {
		host, code = l.host, l.code
	}
	a.loginMu.Unlock()
	if !ok {
		return chat.Login{ID: id, Status: "expired", Error: "sign-in not found; start again"}, nil
	}
	if a.now().Sub(l.started) > loginTTL {
		a.loginMu.Lock()
		delete(a.logins, id)
		a.loginMu.Unlock()
		return chat.Login{ID: id, Status: "expired"}, nil
	}
	q := url.Values{"qrcode": {id}}
	if code != "" {
		q.Set("verify_code", code)
	}
	ctx, cancel := context.WithTimeout(ctx, longPollTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint(host, "get_qrcode_status")+"?"+q.Encode(), nil)
	if err != nil {
		return chat.Login{}, err
	}
	appHeaders(req.Header)
	res, err := a.c.http.Do(req)
	if err != nil {
		return chat.Login{ID: id, Status: "waiting"}, nil
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil || res.StatusCode/100 != 2 {
		return chat.Login{ID: id, Status: "waiting"}, nil
	}
	var st statusResp
	if err := json.Unmarshal(raw, &st); err != nil {
		return chat.Login{ID: id, Status: "waiting"}, nil
	}
	switch st.Status {
	case "wait", "":
		return chat.Login{ID: id, Status: "waiting"}, nil
	case "scaned":
		// The server's spelling, not a typo here.
		return chat.Login{ID: id, Status: "scanned"}, nil
	case "need_verifycode":
		return chat.Login{ID: id, Status: "needCode"}, nil
	case "verify_code_blocked":
		return chat.Login{ID: id, Status: "failed", Error: "too many wrong codes; start again with a new QR code"}, nil
	case "expired":
		return chat.Login{ID: id, Status: "expired"}, nil
	case "scaned_but_redirect":
		if st.RedirectHost != "" {
			host := st.RedirectHost
			if !strings.Contains(host, "://") {
				host = "https://" + host
			}
			a.loginMu.Lock()
			l.host = host
			a.loginMu.Unlock()
		}
		return chat.Login{ID: id, Status: "scanned"}, nil
	case "binded_redirect":
		// The server would only say this had we sent it tokens we hold,
		// and we send none; if it says so anyway there is nothing to
		// store and no way on but a fresh scan on a bot that is not
		// bound elsewhere.
		return chat.Login{ID: id, Status: "failed", Error: "this bot is already bound to another sign-in; no credentials were issued"}, nil
	case "confirmed":
		if st.BotToken == "" {
			return chat.Login{ID: id, Status: "failed", Error: "confirmed without a bot_token"}, nil
		}
		baseURL := st.BaseURL
		if baseURL == "" {
			baseURL = a.qrBase
		}
		creds, _ := json.Marshal(map[string]string{
			"bot_token": st.BotToken, "base_url": baseURL, "bot_id": st.BotID, "user_id": st.UserID,
		})
		a.loginMu.Lock()
		delete(a.logins, id)
		a.loginMu.Unlock()
		return chat.Login{ID: id, Status: "done", Credentials: creds}, nil
	}
	return chat.Login{ID: id, Status: "failed", Error: "unknown status " + st.Status}, nil
}

// SubmitCode stores the number the phone showed; the next poll carries it.
func (a *Adapter) SubmitCode(ctx context.Context, id, code string) error {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	l, ok := a.logins[id]
	if !ok {
		return errors.New("weixin: sign-in not found; start again")
	}
	l.code = strings.TrimSpace(code)
	return nil
}
