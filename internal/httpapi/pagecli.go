package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jiangmuran/vibepanel/internal/pages"
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
