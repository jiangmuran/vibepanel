package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// The inbound side: one long poll after another, for as long as the
// channel is running.
//
// Telegram keeps an update until a later poll's offset passes it, so the
// offset is the only cursor and it is persisted after every batch. A panel
// restarted between the poll and the persist sees the same batch twice,
// which the dedupe below turns into once; a panel that lost its state sees
// everything Telegram still holds (24 hours), which is a burst of pairing
// prompts and nothing worse.

// update is the part of an Update this adapter reads. Everything else,
// including edited messages and anything from a group, is decoded into
// nothing.
type update struct {
	ID       int64          `json:"update_id"`
	Message  *message       `json:"message"`
	Callback *callbackQuery `json:"callback_query"`
}

type message struct {
	ID      int64  `json:"message_id"`
	Date    int64  `json:"date"`
	Chat    tgChat `json:"chat"`
	From    *user  `json:"from"`
	Text    string `json:"text"`
	Caption string `json:"caption"`
	ReplyTo *struct {
		ID int64 `json:"message_id"`
	} `json:"reply_to_message"`
	Photo []photoSize     `json:"photo"`
	Voice json.RawMessage `json:"voice"`
	Audio json.RawMessage `json:"audio"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type user struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type photoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	From    *user    `json:"from"`
	Data    string   `json:"data"`
	Message *message `json:"message"`
}

// Run polls until ctx ends. The error it returns is ctx's: every other
// failure is reported through Sink.Health and retried, because a bot whose
// network blinked must be a bot that is back when the network is.
func (a *Adapter) Run(ctx context.Context, sink chat.Sink) error {
	backoff := a.minWait
	for {
		updates, err := a.getUpdates(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			sink.Health(false, err)
			a.logf("poll: %v", err)
			var wait time.Duration
			wait, backoff = nextWait(backoff, err, a.minWait, a.maxWait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		backoff = a.minWait
		sink.Health(true, nil)
		delivered := false
		for _, u := range updates {
			// Telegram may hand back an update the last batch already
			// carried when a poll is retried after a timeout, and the
			// state may be a poll behind the offset after a restart.
			// Either way, the bridge acting on a press twice is a paste
			// typed twice.
			if u.ID <= a.offset {
				continue
			}
			a.offset = u.ID
			delivered = true
			a.deliver(ctx, sink, u)
		}
		if delivered {
			st, _ := json.Marshal(state{Offset: a.offset})
			sink.State(ctx, st)
		}
	}
}

// nextWait is the backoff ladder: how long to wait after a failed poll,
// and the rung after it. It doubles from min to max and a 429 overrides
// the wait with what Telegram asked for, since polling sooner extends it.
func nextWait(backoff time.Duration, err error, min, max time.Duration) (wait, next time.Duration) {
	wait = backoff
	var te *Error
	if errors.As(err, &te) && te.RetryAfter > 0 {
		wait = time.Duration(te.RetryAfter) * time.Second
	}
	next = backoff * 2
	if next > max {
		next = max
	}
	if next < min {
		next = min
	}
	return wait, next
}

func (a *Adapter) getUpdates(ctx context.Context) ([]update, error) {
	params := map[string]any{
		"timeout":         pollTimeout,
		"allowed_updates": []string{"message", "callback_query"},
	}
	if a.offset > 0 {
		params["offset"] = a.offset + 1
	}
	var out []update
	if err := a.call(ctx, a.poll, "getUpdates", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// deliver turns one update into at most one Inbound.
func (a *Adapter) deliver(ctx context.Context, sink chat.Sink, u update) {
	switch {
	case u.Callback != nil:
		a.deliverCallback(ctx, sink, u.Callback)
	case u.Message != nil:
		a.deliverMessage(ctx, sink, u.Message)
	}
}

func (a *Adapter) deliverMessage(ctx context.Context, sink chat.Sink, m *message) {
	// Everything but a private chat is dropped here, before any field of
	// it is looked at, so nothing of a group's traffic reaches a log line
	// either. See the package comment.
	if m.Chat.Type != "private" {
		return
	}
	in := chat.Inbound{
		PeerID:   strconv.FormatInt(m.Chat.ID, 10),
		PeerName: displayName(m.From),
		Ref:      strconv.FormatInt(m.ID, 10),
		Text:     m.Text,
		At:       time.Unix(m.Date, 0),
	}
	if in.Text == "" {
		in.Text = m.Caption
	}
	if m.ReplyTo != nil {
		in.QuotedRef = strconv.FormatInt(m.ReplyTo.ID, 10)
	}
	switch {
	case len(m.Photo) > 0:
		// Fetched only when the bridge asks, which it does for a paired
		// person and never for a stranger: a photo costs Telegram's
		// bandwidth and the panel's disk, and both are the panel's to spend.
		photo := largest(m.Photo)
		in.FetchImage = func(ctx context.Context) ([]byte, error) {
			img, err := a.download(ctx, photo)
			if err != nil {
				a.logf("photo from %s: %v", in.PeerID, a.scrub(err))
			}
			return img, err
		}
	case len(m.Voice) > 0 || len(m.Audio) > 0:
		// No transcription; the bridge drops an empty text, so this is a
		// receipt for the record only.
		in.Text = ""
	}
	sink.Inbound(ctx, in)
}

func (a *Adapter) deliverCallback(ctx context.Context, sink chat.Sink, q *callbackQuery) {
	if q.Message != nil && q.Message.Chat.Type != "private" {
		return
	}
	in := chat.Inbound{
		PeerName: displayName(q.From),
		Action:   &chat.Action{Value: q.Data, ID: q.ID},
		At:       time.Now(),
	}
	// The press's peer is the chat the button was in. Telegram omits the
	// message once it is too old to reach, and then the presser is the
	// only address left; in a private chat they are the same number.
	switch {
	case q.Message != nil:
		in.PeerID = strconv.FormatInt(q.Message.Chat.ID, 10)
		in.Action.MessageRef = strconv.FormatInt(q.Message.ID, 10)
		if q.Message.Date > 0 {
			in.At = time.Unix(q.Message.Date, 0)
		}
	case q.From != nil:
		in.PeerID = strconv.FormatInt(q.From.ID, 10)
	default:
		return
	}
	sink.Inbound(ctx, in)
}

// displayName is how the person appears in the panel: their name, or their
// handle when they have set none.
func displayName(u *user) string {
	if u == nil {
		return ""
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	return name
}

// largest picks the PhotoSize to download. Telegram lists sizes small to
// large, but that is convention rather than contract, so it is measured.
func largest(sizes []photoSize) photoSize {
	best := sizes[0]
	for _, s := range sizes[1:] {
		if s.Width*s.Height > best.Width*best.Height {
			best = s
		}
	}
	return best
}

// download fetches a photo's bytes: getFile for the path, then the file
// endpoint for the content.
func (a *Adapter) download(ctx context.Context, p photoSize) ([]byte, error) {
	if p.FileSize > maxImage {
		return nil, fmt.Errorf("telegram: photo is %d bytes, over the %d limit", p.FileSize, maxImage)
	}
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := a.call(ctx, a.http, "getFile", map[string]any{"file_id": p.FileID}, &f); err != nil {
		return nil, err
	}
	if f.FilePath == "" {
		return nil, errors.New("telegram: getFile returned no file_path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiBase+"/file/bot"+a.token+"/"+f.FilePath, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: file: %w", err)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: file: %w", a.scrub(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram: file: %s", resp.Status)
	}
	// One byte past the cap is read so that a file exactly at the cap is
	// told apart from one that was cut.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImage+1))
	if err != nil {
		return nil, fmt.Errorf("telegram: file: %w", err)
	}
	if len(data) > maxImage {
		return nil, fmt.Errorf("telegram: photo is over the %d byte limit", maxImage)
	}
	return data, nil
}
