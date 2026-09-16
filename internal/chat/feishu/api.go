package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The open-platform API: one token, one envelope, two reasons to try again.

// maxResponse caps what is read back from the API. A downloaded picture is
// the largest thing; 飞书 allows 100 MB but a phone photo is under 10, and a
// picture that size typed into a tmux pane as a path is useless anyway.
const maxResponse = 32 << 20

// tokenSlack is how early a cached token is dropped. 飞书 hands out the same
// token while it has 30 minutes left, so anything under that is safe; a
// minute keeps a request that was in flight at the boundary from failing.
const tokenSlack = time.Minute

// maxRateWait caps the back-off a 429 asks for. The header has said 60 on a
// burst; a Send that blocks the bridge for a minute is worse than one that
// fails, because the next session change is waiting behind it.
const maxRateWait = 10 * time.Second

// envelope is every JSON answer the API gives.
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// tokenError reports a code the gateway uses for a token it no longer
// accepts. 99991663 (invalid) and 99991661 (expired) are the documented
// ones; the rest of the 9999166x range is the same family.
func tokenError(code int) bool { return code >= 99991661 && code <= 99991669 }

// accessToken returns the cached tenant token or fetches one. The lock is
// held across the fetch on purpose: three sends racing at start-up would
// otherwise each ask for a token, and 飞书 counts those against the app.
func (a *Adapter) accessToken(ctx context.Context) (string, error) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.token != "" && a.now().Before(a.tokExp) {
		return a.token, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": a.appID, "app_secret": a.appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiBase+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := a.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu: token: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("feishu: token: %w", err)
	}
	// The token answer is the one response whose fields sit beside code and
	// msg rather than under data.
	var out struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("feishu: token: %s: %w", resp.Status, err)
	}
	if out.Code != 0 || out.Token == "" {
		return "", fmt.Errorf("feishu: token: %d %s", out.Code, out.Msg)
	}
	a.token = out.Token
	a.tokExp = a.now().Add(time.Duration(out.Expire)*time.Second - tokenSlack)
	return a.token, nil
}

// forgetToken drops the cached token if it is still the one that failed, so
// a refresh another goroutine already did is not thrown away.
func (a *Adapter) forgetToken(tok string) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if a.token == tok {
		a.token = ""
	}
}

// do sends one authenticated request and reads the answer. Two answers mean
// "again", once each: a 429 with x-ogw-ratelimit-reset, after waiting that
// long, and a token error, after fetching a new token. Anything else is
// returned as is for the caller to read.
func (a *Adapter) do(ctx context.Context, build func(token string) (*http.Request, error)) (*http.Response, []byte, error) {
	waited, refreshed := false, false
	for {
		tok, err := a.accessToken(ctx)
		if err != nil {
			return nil, nil, err
		}
		req, err := build(tok)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := a.http.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("feishu: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
		resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("feishu: %w", err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && !waited {
			waited = true
			if err := a.sleep(ctx, resetAfter(resp.Header)); err != nil {
				return nil, nil, err
			}
			continue
		}
		if !refreshed && isJSON(resp) {
			var env envelope
			if json.Unmarshal(body, &env) == nil && tokenError(env.Code) {
				refreshed = true
				a.forgetToken(tok)
				continue
			}
		}
		return resp, body, nil
	}
}

// resetAfter reads how long a 429 asks the caller to wait, capped.
func resetAfter(h http.Header) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h.Get("x-ogw-ratelimit-reset")))
	if err != nil || n <= 0 {
		return time.Second
	}
	d := time.Duration(n) * time.Second
	if d > maxRateWait {
		d = maxRateWait
	}
	return d
}

func isJSON(resp *http.Response) bool {
	return strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "application/json")
}

// callJSON posts or patches a JSON body and decodes data into out. An API
// error is the envelope's code and msg, which is what a person needs to
// read off the settings page ("230013 user not in availability range" says
// exactly which console page to open).
func (a *Adapter) callJSON(ctx context.Context, method, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, raw, err := a.do(ctx, func(string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, a.apiBase+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		return req, nil
	})
	if err != nil {
		return err
	}
	return decodeEnvelope(resp, raw, out)
}

func decodeEnvelope(resp *http.Response, raw []byte, out any) error {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("feishu: %s: %w", resp.Status, err)
	}
	if env.Code != 0 {
		return fmt.Errorf("feishu: %d %s", env.Code, env.Msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("feishu: data: %w", err)
		}
	}
	return nil
}

// uploadImage puts a picture into the app's store and returns its key. The
// form field names are 飞书's, literally "image_type" and "image".
func (a *Adapter) uploadImage(ctx context.Context, data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("feishu: empty image")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("image_type", "message"); err != nil {
		return "", err
	}
	part, err := mw.CreateFormFile("image", "screen.png")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	resp, raw, err := a.do(ctx, func(string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiBase+"/open-apis/im/v1/images", bytes.NewReader(buf.Bytes()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return req, nil
	})
	if err != nil {
		return "", err
	}
	var out struct {
		ImageKey string `json:"image_key"`
	}
	if err := decodeEnvelope(resp, raw, &out); err != nil {
		return "", err
	}
	if out.ImageKey == "" {
		return "", errors.New("feishu: upload returned no image_key")
	}
	return out.ImageKey, nil
}

// download fetches a resource attached to a received message. Success is
// raw bytes; failure is a JSON envelope, which is how the two are told
// apart.
func (a *Adapter) download(ctx context.Context, messageID, fileKey, kind string) ([]byte, error) {
	resp, raw, err := a.do(ctx, func(string) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet,
			a.apiBase+"/open-apis/im/v1/messages/"+pathSeg(messageID)+"/resources/"+pathSeg(fileKey)+"?type="+url.QueryEscape(kind), nil)
	})
	if err != nil {
		return nil, err
	}
	if isJSON(resp) {
		if err := decodeEnvelope(resp, raw, nil); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("feishu: download: %s", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("feishu: download: %s", resp.Status)
	}
	if len(raw) == 0 {
		return nil, errors.New("feishu: download: empty")
	}
	return raw, nil
}

// pathSeg escapes an id for a URL path. Ids are 飞书's own, but one that
// arrived in a webhook body is still something a stranger typed.
func pathSeg(s string) string { return url.PathEscape(s) }

// jsonString serialises a message content: 飞书 wants the inner JSON as a
// string inside the outer JSON, so it is marshalled twice by design.
func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
