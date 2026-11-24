package httpapi

import (
	"context"
	"crypto/rand"
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
	// AccessMode optionally pins "none", "shared" or "custom". Empty defaults
	// to "shared" when a global shared password is configured, otherwise "none".
	AccessMode store.ShareAccessMode
	// CustomPassword sets the per-link password when AccessMode is "custom".
	CustomPassword string
	// RandomPassword generates a clean random password when AccessMode is "custom".
	RandomPassword bool
}

// ShareLinkResult is the resolved or newly minted handed-out share link.
type ShareLinkResult struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	PageID            string `json:"pageId"`
	URL               string `json:"url"`
	Detail            string `json:"detail"`
	AccessMode        string `json:"accessMode"`
	GeneratedPassword string `json:"generatedPassword,omitempty"`
	ExpiresAt         int64  `json:"expiresAt"`
	Created           bool   `json:"created"`
	Reused            bool   `json:"reused"`
	// Warnings are things the caller should say on stderr and that do not
	// change the link: it only opens locally, or an audit row was not written.
	Warnings []string `json:"warnings,omitempty"`
}

func generateRandomSharePassword(n int) (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	if n < minSharePasswordLen {
		n = 10
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i := range buf {
		out[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(out), nil
}

// EnsurePageShareLink returns a clickable handed-out share link for a published
// page. Unless opts.ForceNew is true, it reuses the newest active copyable link
// on that page with matching detail and interactivity so repeated publishes keep
// the same URL.
//
// A reused link must be exactly what was asked for, because the caller prints
// it as the answer: a locked link is never reused (it is somebody's fixed
// arrangement), a request with an expiry always mints a fresh link (an older
// link's expiry is not the one asked for), and a request without one reuses
// only a link that never expires.
func EnsurePageShareLink(ctx context.Context, db *store.DB, cfg config.Config,
	page store.SharePage, opts ShareLinkOptions) (ShareLinkResult, error) {
	if page.PublishedVersion == 0 {
		return ShareLinkResult{}, errors.New("this page has not been published yet; run `vibepanel page publish` first")
	}
	detail, err := validateShareTerms(opts.Detail, opts.ExpiresIn)
	if err != nil {
		return ShareLinkResult{}, err
	}
	if opts.AccessMode == "" && (opts.RandomPassword || opts.CustomPassword != "") {
		opts.AccessMode = store.ShareAccessCustom
	}
	if opts.AccessMode != "" && !store.ValidShareAccessMode(opts.AccessMode) {
		return ShareLinkResult{}, errors.New("access mode must be shared, custom or none")
	}
	if opts.AccessMode != store.ShareAccessCustom && (opts.RandomPassword || opts.CustomPassword != "") {
		return ShareLinkResult{}, errors.New("--random-password and --password-stdin only apply with --access custom")
	}
	sharedHash, _ := db.GetSetting(ctx, sharedPasswordHashKey, "")
	if opts.AccessMode == store.ShareAccessShared && sharedHash == "" {
		return ShareLinkResult{}, errors.New("shared password is not set; configure one in Sharing settings first")
	}
	var customPW, generatedPW string
	if opts.AccessMode == store.ShareAccessCustom {
		if opts.RandomPassword && opts.CustomPassword != "" {
			return ShareLinkResult{}, errors.New("pass either --random-password or --password-stdin, not both")
		}
		if opts.RandomPassword {
			var gerr error
			generatedPW, gerr = generateRandomSharePassword(10)
			if gerr != nil {
				return ShareLinkResult{}, fmt.Errorf("generate password: %w", gerr)
			}
			customPW = generatedPW
		} else if opts.CustomPassword != "" {
			customPW = opts.CustomPassword
		}
		if customPW != "" {
			if err := validateSharePassword(customPW); err != nil {
				return ShareLinkResult{}, err
			}
		}
	}

	box, err := secret.Open(filepath.Join(cfg.DataDir, secret.KeyFile))
	if err != nil {
		return ShareLinkResult{}, fmt.Errorf("open secrets key: %w", err)
	}
	base, local := cliShareBaseOrigin(ctx, db, cfg)
	var warnings []string
	if local {
		warnings = append(warnings, "this link only opens on this machine ("+base+"); set a share origin in Settings > Sharing, or start the panel with --domain, for a link others can open")
	}

	if !opts.ForceNew {
		links, lerr := db.ListShareLinks(ctx)
		if lerr != nil {
			return ShareLinkResult{}, lerr
		}
		for _, l := range links {
			if l.PageID != page.ID || l.Purpose != "" || !l.Copyable || l.Locked {
				continue
			}
			if opts.ExpiresIn > 0 || l.ExpiresAt != 0 {
				continue
			}
			if store.ShareDetail(l.Detail) != detail || l.Interactive != opts.Interactive {
				continue
			}
			if opts.AccessMode != "" && customPW == "" && store.ShareAccessMode(l.AccessMode) != opts.AccessMode {
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
			modeOut := l.AccessMode
			if opts.AccessMode == store.ShareAccessCustom && customPW != "" {
				pwHash, herr := auth.HashPassword(customPW)
				if herr != nil {
					return ShareLinkResult{}, herr
				}
				pwEnc := box.Seal([]byte(customPW), linkPasswordSealContext(l.ID))
				if serr := db.SetShareLinkAccess(ctx, l.ID, store.ShareAccessCustom, pwHash, pwEnc, false); serr != nil {
					return ShareLinkResult{}, serr
				}
				if aerr := db.Audit(ctx, store.AuditEntry{
					Event:    "share.access_changed",
					Username: "cli",
					Detail:   l.Name + " -> custom",
				}); aerr != nil {
					warnings = append(warnings, "access change not recorded in the audit log: "+aerr.Error())
				}
				modeOut = string(store.ShareAccessCustom)
			}
			return ShareLinkResult{
				ID:                l.ID,
				Name:              l.Name,
				PageID:            l.PageID,
				URL:               base + "/share/" + string(tok) + "/",
				Detail:            l.Detail,
				AccessMode:        modeOut,
				GeneratedPassword: generatedPW,
				ExpiresAt:         l.ExpiresAt,
				Reused:            true,
				Warnings:          warnings,
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
	accessMode := opts.AccessMode
	if accessMode == "" {
		if sharedHash != "" {
			accessMode = store.ShareAccessShared
		} else {
			accessMode = store.ShareAccessNone
		}
	}
	linkID := id.New()
	var pwHash string
	var pwEnc []byte
	if accessMode == store.ShareAccessCustom {
		if customPW == "" {
			return ShareLinkResult{}, errors.New("custom access mode requires --random-password or --password-stdin")
		}
		pwHash, err = auth.HashPassword(customPW)
		if err != nil {
			return ShareLinkResult{}, err
		}
		pwEnc = box.Seal([]byte(customPW), linkPasswordSealContext(linkID))
	}
	rec, err := db.CreateShareLink(ctx, store.NewShareLink{
		ID:           linkID,
		TokenHash:    auth.HashToken(token),
		TokenEnc:     box.Seal([]byte(token), shareTokenContext(linkID)),
		Prefix:       prefix,
		Name:         name,
		Detail:       detail,
		Scope:        store.ShareWhole,
		UserID:       owner,
		ExpiresAt:    expiresAt,
		PageID:       page.ID,
		Params:       params,
		Interactive:  opts.Interactive,
		AccessMode:   accessMode,
		PasswordHash: pwHash,
		PasswordEnc:  pwEnc,
	})
	if err != nil {
		return ShareLinkResult{}, err
	}
	// A failed audit write is said, not dropped: the link exists either way,
	// and the owner reading the trail should know it is missing a row.
	if aerr := db.Audit(ctx, store.AuditEntry{
		Event:    "share.created",
		Username: "cli",
		Detail:   name + " (" + string(detail) + ", access " + string(accessMode) + ", page " + page.ID + ")",
	}); aerr != nil {
		warnings = append(warnings, "not recorded in the audit log: "+aerr.Error())
	}
	// Interactive is a decision about what a visitor may do, and the HTTP route
	// audits it (red line 8); the CLI is the same decision from a shell.
	if opts.Interactive {
		if aerr := db.Audit(ctx, store.AuditEntry{
			Event: "share.interactive_changed", Username: "cli", Detail: name + " on",
		}); aerr != nil {
			warnings = append(warnings, "interactive not recorded in the audit log: "+aerr.Error())
		}
	}
	return ShareLinkResult{
		ID:                rec.ID,
		Name:              rec.Name,
		PageID:            rec.PageID,
		URL:               base + "/share/" + token + "/",
		Detail:            rec.Detail,
		AccessMode:        rec.AccessMode,
		GeneratedPassword: generatedPW,
		ExpiresAt:         rec.ExpiresAt,
		Created:           true,
		Warnings:          warnings,
	}, nil
}

// cliShareBaseOrigin resolves the public origin a CLI-generated share URL
// should use: the configured dedicated share origin first, then the panel's
// public HTTPS/HTTP origin, and finally its loopback address. The second result
// is true only for that last fallback, where the link opens on this machine
// alone.
func cliShareBaseOrigin(ctx context.Context, db *store.DB, cfg config.Config) (string, bool) {
	if raw, err := db.GetSetting(ctx, shareOriginKey, ""); err == nil {
		if origin, _, _, perr := parseShareOrigin(raw); perr == nil && origin != "" {
			return origin, false
		}
	}
	if cfg.Domain != "" {
		for _, raw := range cfg.PublicOrigins {
			u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
			if err != nil || u.Scheme == "" || u.Host == "" {
				continue
			}
			if strings.EqualFold(u.Hostname(), cfg.Domain) {
				return strings.ToLower(u.Scheme) + "://" + u.Host, false
			}
		}
		// Behind an HTTPS reverse proxy (`--tls off --public-port 443`),
		// PublicURL() formats http://<domain>:443. Prefer https://<domain>.
		if cfg.TLSMode == config.TLSOff && cfg.PublicPort == 443 {
			return "https://" + cfg.Domain, false
		}
		return cfg.PublicURL(), false
	}
	return cfg.LoopbackURL(), true
}
