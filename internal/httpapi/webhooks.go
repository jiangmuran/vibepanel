package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/notify"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// webhooksKey is where the list lives.
//
// The key/value settings table rather than a table of its own. This is a short
// list a person edits by hand a few times ever, read once per state change and
// never joined against anything -- a migration would buy indexing nobody needs
// and cost a schema version, which is currently the thing several people are
// colliding on.
const webhooksKey = "webhooks"

// maxWebhooks bounds the list.
//
// Not tidiness: every one of these is an outbound HTTP request made on a state
// change, so the list is a multiplier on how much this panel can be made to
// send. Twenty is far past what anybody configures by hand.
const maxWebhooks = 20

func (s *Server) registerWebhookRoutes(r chi.Router) {
	r.Get("/settings/webhooks", s.handleListWebhooks)
	r.Put("/settings/webhooks", s.handlePutWebhooks)
	r.Post("/settings/webhooks/test", s.handleTestWebhook)
}

func (s *Server) webhooks(r *http.Request) []notify.Webhook {
	raw, err := s.DB.GetSetting(r.Context(), webhooksKey, "")
	if err != nil || raw == "" {
		return nil
	}
	var out []notify.Webhook
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// A row that will not parse is a row somebody edited by hand or a
		// format that moved. Answering with none is the honest reading; the
		// alternative is a page that cannot load because of one bad character.
		s.Log.Warn("webhooks setting does not parse", "err", err)
		return nil
	}
	return out
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, emptyIfNil(s.webhooks(r)))
}

func (s *Server) handlePutWebhooks(w http.ResponseWriter, r *http.Request) {
	var in []notify.Webhook
	if !decode(w, r, &in) {
		return
	}
	if len(in) > maxWebhooks {
		writeErr(w, http.StatusRequestEntityTooLarge, "too many webhooks")
		return
	}
	for i := range in {
		if strings.TrimSpace(in[i].URL) == "" {
			writeErr(w, http.StatusBadRequest, "a webhook needs a URL")
			return
		}
		// An id assigned here, not accepted from the request. Ids come back in
		// the audit log and in the test endpoint's argument, and one the client
		// chose is one the client can collide or forge.
		if in[i].ID == "" {
			in[i].ID = id.New()
		}
	}
	body, err := json.Marshal(in)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.SetSetting(r.Context(), webhooksKey, string(body)); err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "webhooks.changed", u.Username, s.clientIP(r),
			strconv.Itoa(len(in))+" configured")
	}
	writeJSON(w, http.StatusOK, emptyIfNil(in))
}

// handleTestWebhook sends one, now, and answers with what came back.
//
// The destination is taken from the request body rather than from the stored
// list on purpose: the moment somebody wants to test one is *before* they have
// saved it, and a test button that only works on saved rows is a test button
// that makes people save something broken to find out it is broken.
func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	var wh notify.Webhook
	if !decode(w, r, &wh) {
		return
	}
	if strings.TrimSpace(wh.URL) == "" {
		writeErr(w, http.StatusBadRequest, "a webhook needs a URL")
		return
	}
	// Enabled for the duration of the test whatever the row says: pressing
	// "test" on a row you have not switched on yet should still tell you
	// whether it works.
	wh.Enabled = true
	said, err := notify.Send(r.Context(), nil, wh, sampleEvent(s.Cfg.PublicURL()))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "said": said})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "said": said})
}

// sampleEvent is what a test send carries.
//
// Recognisable rather than realistic: somebody pressing "test" is looking at
// their phone to see whether anything arrived, and a notification that says
// "waiting" about a session they have to go and find is a worse answer than one
// that says it is a test.
func sampleEvent(url string) notify.Event {
	return notify.Event{
		State:   "waiting",
		Session: "vibepanel test",
		Project: "vibepanel",
		URL:     url,
		At:      time.Now(),
	}
}

// fireWebhooks tells whoever asked that a session changed state.
//
// In a goroutine and never waited for. This is called from the poller, which
// holds the loop that keeps every session's state current -- a destination that
// takes eight seconds to answer would stall the panel's own idea of what is
// happening, which is a strange price to pay for a notification about it.
//
// Errors go to the log and nowhere else. A webhook that has stopped working is
// the operator's problem to notice on the settings page; making it the reason a
// poll tick fails would turn somebody else's outage into this panel's.
func (s *Server) fireWebhooks(ctx context.Context, row store.Session, state string) {
	raw, err := s.DB.GetSetting(ctx, webhooksKey, "")
	if err != nil || raw == "" {
		return
	}
	var hooks []notify.Webhook
	if err := json.Unmarshal([]byte(raw), &hooks); err != nil {
		return
	}
	var project string
	if p, perr := s.DB.GetProject(ctx, row.ProjectID); perr == nil {
		project = p.Name
	}
	ev := notify.Event{
		State:   state,
		Session: row.Title,
		Project: project,
		URL:     s.Cfg.PublicURL(),
		At:      time.Now(),
	}
	for _, wh := range hooks {
		if !wh.Fires(state) {
			continue
		}
		go func(wh notify.Webhook) {
			// context.WithoutCancel: the poll tick that noticed the change is
			// over in milliseconds, and a notification cancelled because its
			// caller returned is a notification that never arrives.
			if _, err := notify.Send(context.WithoutCancel(ctx), nil, wh, ev); err != nil {
				s.Log.Warn("webhook", "name", wh.Name, "err", err)
			}
		}(wh)
	}
}
