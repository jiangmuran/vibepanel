package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/secret"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// What `vibepanel page data` and `vibepanel page run` call. They run in the
// CLI's own process against the database, through the same code the panel's
// routes use -- the manifest's rules, the same checks, the same sandbox -- so
// what an agent sees from a shell is what a screen would get.
//
// Two things differ, and both are said in the command's output: the running
// panel caches data for up to five seconds, so a change made here reaches a
// screen within that; and the CLI has no source results, because fetching is
// the panel's background job and not something a shell command should start.

// cliServer is a Server with only what page data and server.js need.
func cliServer(db *store.DB) *Server {
	s := &Server{DB: db, Log: slog.New(slog.DiscardHandler)}
	// The CLI prints its log; .vibepanel/server.log is the panel's.
	s.pb.server.quiet = true
	return s
}

// PageDataManifest is the manifest page data is checked against in a
// namespace: the draft directory's for draft, the published version's for
// live.
func PageDataManifest(ctx context.Context, db *store.DB, page store.SharePage, ns string) (pages.Manifest, error) {
	if !store.ValidPageDataNamespace(ns) {
		return pages.Manifest{}, fmt.Errorf("no data namespace %q", ns)
	}
	m, err := cliServer(db).pageManifestFor(ctx, page, ns)
	if errors.Is(err, errNotPublished) {
		return m, errors.New("this page has never been published, so it has no live data; drop --live")
	}
	return m, err
}

// PageDataValues is every declared key's value in a namespace, admin keys
// included.
func PageDataValues(ctx context.Context, db *store.DB, page store.SharePage, ns string) (map[string]any, pages.Manifest, error) {
	m, err := PageDataManifest(ctx, db, page, ns)
	if err != nil {
		return nil, m, err
	}
	rows, err := db.PageData(ctx, page.ID, ns)
	if err != nil {
		return nil, m, err
	}
	values, _ := resolvePageData(m, rows, true)
	return values, m, nil
}

// ChangePageData sets or resets one key as the owner would from settings, and
// returns its new value.
func ChangePageData(ctx context.Context, db *store.DB, page store.SharePage, ns, kind, key string, value any) (any, error) {
	m, err := PageDataManifest(ctx, db, page, ns)
	if err != nil {
		return nil, err
	}
	if kind != "set" && kind != "reset" {
		return nil, fmt.Errorf("no data change %q", kind)
	}
	changed, err := cliServer(db).applyPageData(ctx, page.ID, ns, m,
		[]pageDataOp{{Kind: kind, Key: key, Value: value}}, "cli", false, false)
	if err != nil {
		return nil, err
	}
	if aerr := db.Audit(ctx, store.AuditEntry{Event: "page.data_changed", Username: "cli",
		Detail: fmt.Sprintf("%s %s: %s %s", page.Name, ns, kind, key)}); aerr != nil {
		return changed[key], fmt.Errorf("changed, but not recorded in the audit log: %w", aerr)
	}
	return changed[key], nil
}

// ServerRun is what one `vibepanel page run` did.
type ServerRun struct {
	Result any             `json:"result"`
	Log    []serverLogLine `json:"log"`
}

// RunPageServer runs server.js from the draft directory against draft data:
// "transform" with a snapshot (a fixture's, or none), "schedule", or "action"
// with a name and a payload. An action runs as a visitor when visitors may
// run it, since that is the stricter reading, unless asAdmin.
func RunPageServer(ctx context.Context, db *store.DB, page store.SharePage, what, name string,
	payload map[string]any, snapshot json.RawMessage, asAdmin bool) (ServerRun, error) {
	ns := store.PageDataDraft
	s := cliServer(db)
	m, err := s.pageManifestFor(ctx, page, ns)
	if err != nil {
		return ServerRun{}, err
	}
	var out ServerRun
	finish := func(result any, err error) (ServerRun, error) {
		s.pb.server.mu.Lock()
		out.Log = append([]serverLogLine{}, s.pb.server.logs[page.ID]...)
		s.pb.server.mu.Unlock()
		out.Result = result
		if errors.Is(err, errNoHook) {
			err = fmt.Errorf("server.js does not define the hook for %s", what)
		}
		return out, err
	}
	switch what {
	case "transform", "schedule":
		if m.Server == nil {
			return out, errors.New(`this page has no "server" in vibepanel.json`)
		}
	}
	switch what {
	case "transform":
		rows, err := db.PageData(ctx, page.ID, ns)
		if err != nil {
			return out, err
		}
		data, _ := resolvePageData(m, rows, false)
		var snap any = map[string]any{}
		if len(snapshot) > 0 {
			if err := json.Unmarshal(snapshot, &snap); err != nil {
				return out, fmt.Errorf("the snapshot is not JSON: %w", err)
			}
		}
		if obj, ok := snap.(map[string]any); ok {
			obj["data"] = data
			obj["sources"] = map[string]any{}
		}
		input := map[string]any{"snapshot": snap, "data": data, "sources": map[string]any{}}
		return finish(s.runServer(ctx, serverCall{page: page, ns: ns, m: m, hook: "transform",
			args: []any{input}, budget: transformBudget, noCtx: true}))
	case "schedule":
		return finish(s.runServer(ctx, serverCall{page: page, ns: ns, m: m, hook: "onSchedule",
			budget: scheduleBudget, by: "cli"}))
	case "action":
		action := m.Actions[name]
		if action == nil {
			return out, fmt.Errorf("vibepanel.json declares no action %q", name)
		}
		visitor := action.VisitorMay() && !asAdmin
		if !visitor && !action.AdminMay() {
			return out, fmt.Errorf("%s is for visitors only; drop --admin", name)
		}
		if payload == nil {
			payload = map[string]any{}
		}
		input, err := pages.CheckInput(m.InputFields(action), payload, visitor)
		if err != nil {
			return out, err
		}
		var who map[string]any
		if visitor {
			who = map[string]any{"id": "cli", "link": "cli"}
		}
		return finish(s.runAction(ctx, page, ns, m, name, action, input, "cli", who, visitor, 0))
	}
	return out, fmt.Errorf("run transform, schedule or action, not %q", what)
}

// ShareLinkOptions configures `vibepanel page link` and `page publish --link`.
type ShareLinkOptions struct {
	Name        string
	Detail      store.ShareDetail
	ExpiresIn   int64
	Interactive bool
	ForceNew    bool
}

// ShareLinkResult is the resolved or newly minted handed-out share link.
type ShareLinkResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PageID    string `json:"pageId"`
	URL       string `json:"url"`
	Detail    string `json:"detail"`
	ExpiresAt int64  `json:"expiresAt"`
	Created   bool   `json:"created"`
	Reused    bool   `json:"reused"`
}

// EnsurePageShareLink returns a clickable handed-out share link for a published
// page. Unless opts.ForceNew is true, it reuses the newest active copyable link
// on that page with matching detail and interactivity so repeated publishes keep
// the same URL.
func EnsurePageShareLink(ctx context.Context, db *store.DB, cfg config.Config,
	page store.SharePage, opts ShareLinkOptions) (ShareLinkResult, error) {
	if page.PublishedVersion == 0 {
		return ShareLinkResult{}, errors.New("this page has not been published yet; run `vibepanel page publish` first")
	}
	detail := opts.Detail
	if detail == "" {
		detail = store.ShareCounts
	}
	if !store.ValidShareDetail(detail) {
		return ShareLinkResult{}, errors.New("detail must be counts or names")
	}
	if opts.ExpiresIn < 0 || opts.ExpiresIn > maxShareSeconds {
		return ShareLinkResult{}, errors.New("expiresIn must be between 0 and a year, in seconds")
	}

	box, err := secret.Open(filepath.Join(cfg.DataDir, secret.KeyFile))
	if err != nil {
		return ShareLinkResult{}, fmt.Errorf("open secrets key: %w", err)
	}
	base := cliShareBaseOrigin(ctx, db, cfg)

	if !opts.ForceNew {
		links, lerr := db.ListShareLinks(ctx)
		if lerr != nil {
			return ShareLinkResult{}, lerr
		}
		now := time.Now().Unix()
		for _, l := range links {
			if l.PageID != page.ID || l.Purpose != "" || !l.Copyable {
				continue
			}
			if l.ExpiresAt > 0 && l.ExpiresAt <= now {
				continue
			}
			if store.ShareDetail(l.Detail) != detail || l.Interactive != opts.Interactive {
				continue
			}
			enc, eerr := db.ShareLinkTokenEnc(ctx, l.ID)
			if eerr != nil || len(enc) == 0 {
				continue
			}
			tok, uerr := box.Unseal(enc, shareTokenContext(l.ID))
			if uerr != nil || len(tok) == 0 {
				continue
			}
			return ShareLinkResult{
				ID:        l.ID,
				Name:      l.Name,
				PageID:    l.PageID,
				URL:       base + "/share/" + string(tok) + "/",
				Detail:    l.Detail,
				ExpiresAt: l.ExpiresAt,
				Reused:    true,
			}, nil
		}
	}

	v, err := db.SharePageVersionByNumber(ctx, page.ID, page.PublishedVersion)
	if err != nil {
		return ShareLinkResult{}, err
	}
	params, err := pages.ValidateParamValues(pages.DecodeStored(v.Manifest).Params, nil)
	if err != nil {
		return ShareLinkResult{}, err
	}
	owner, err := db.SharePageOwner(ctx, page.ID)
	if err != nil {
		owner, err = db.FirstUserID(ctx)
		if err != nil {
			return ShareLinkResult{}, errors.New("the panel has no account yet; finish setup first")
		}
	}
	name := session.TruncateTitle(strings.TrimSpace(opts.Name))
	if name == "" {
		name = session.TruncateTitle(strings.TrimSpace(page.Name))
	}
	if name == "" {
		name = "screen"
	}
	token, err := auth.NewToken()
	if err != nil {
		return ShareLinkResult{}, err
	}
	prefix := token
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	var expiresAt int64
	if opts.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(opts.ExpiresIn) * time.Second).Unix()
	}
	linkID := id.New()
	rec, err := db.CreateShareLink(ctx, store.NewShareLink{
		ID:          linkID,
		TokenHash:   auth.HashToken(token),
		TokenEnc:    box.Seal([]byte(token), shareTokenContext(linkID)),
		Prefix:      prefix,
		Name:        name,
		Detail:      detail,
		Scope:       store.ShareWhole,
		UserID:      owner,
		ExpiresAt:   expiresAt,
		PageID:      page.ID,
		Params:      params,
		Interactive: opts.Interactive,
	})
	if err != nil {
		return ShareLinkResult{}, err
	}
	_ = db.Audit(ctx, store.AuditEntry{
		Event:    "share.created",
		Username: "cli",
		Detail:   name + " (" + string(detail) + ", page " + page.ID + ")",
	})
	return ShareLinkResult{
		ID:        rec.ID,
		Name:      rec.Name,
		PageID:    rec.PageID,
		URL:       base + "/share/" + token + "/",
		Detail:    rec.Detail,
		ExpiresAt: rec.ExpiresAt,
		Created:   true,
	}, nil
}

// cliShareBaseOrigin resolves the public origin a CLI-generated share URL
// should use: the configured dedicated share origin first, then the panel's
// public HTTPS/HTTP origin, and finally its loopback address.
func cliShareBaseOrigin(ctx context.Context, db *store.DB, cfg config.Config) string {
	if raw, err := db.GetSetting(ctx, shareOriginKey, ""); err == nil {
		if origin, _, _, perr := parseShareOrigin(raw); perr == nil && origin != "" {
			return origin
		}
	}
	if cfg.Domain != "" {
		for _, raw := range cfg.PublicOrigins {
			u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
			if err != nil || u.Scheme == "" || u.Host == "" {
				continue
			}
			if strings.EqualFold(u.Hostname(), cfg.Domain) {
				return strings.ToLower(u.Scheme) + "://" + u.Host
			}
		}
		// Behind an HTTPS reverse proxy (`--tls off --public-port 443`),
		// PublicURL() formats http://<domain>:443. Prefer https://<domain>.
		if cfg.TLSMode == config.TLSOff && cfg.PublicPort == 443 {
			return "https://" + cfg.Domain
		}
		return cfg.PublicURL()
	}
	return cfg.LoopbackURL()
}
