package httpapi

import (
	"github.com/go-chi/chi/v5"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/browse"
	"github.com/jiangmuran/vibepanel/internal/git"
	"github.com/jiangmuran/vibepanel/internal/hooks"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// TestTypeScriptRowsMatchWhatIsSent compares the fields the server sends with
// the fields the frontend declares.
//
// wire.ts is written by hand — AGENTS.md says so, and an earlier version of it
// claimed to be generated, which protected nothing while reading as though it
// did. TestTypeScriptStatesMatchTheEnum pins the state enum. Nothing pinned the
// rest of the shape, and the drift it allows is silent in the direction that
// matters: the data arrives from JSON.parse cast to the interface, so a field
// the server has stopped sending is still declared, still type-checks, and is
// `undefined` at runtime.
//
// Written after removing `lastOutputAt` from the wire. Editing wire.ts to match
// was something I had to remember to do, and nothing would have said a word if
// I had not.
func TestTypeScriptRowsMatchWhatIsSent(t *testing.T) {
	const path = "../../web/src/protocol/wire.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s, so nothing was compared: %v", path, err)
	}

	for _, tc := range []struct {
		name string
		row  any
	}{
		{"Session", store.Session{}},
		{"Project", store.Project{}},
		{"Note", store.Note{}},
		{"Todo", store.Todo{}},
		{"AuditEntry", store.AuditEntry{}},
		{"FileEntry", browse.Entry{}},
		// The largest surface and the last one to be covered. This is what the
		// socket pushes on every change, and it lived in a package the old
		// home of this test could not import without a cycle — so the rows
		// were pinned and the envelope carrying them was not.
		{"PanelState", stateResponse{}},
		// The rest of the hand-written surface. These went uncovered only
		// because the test could not see this package from where it lived;
		// none of them is less hand-written than the rows above.
		{"AuthState", authState{}},
		{"SettingsInfo", settingsResponse{}},
		{"HookStatus", hooks.Status{}},
		{"FileListing", browse.Listing{}},
		{"SystemSample", sysmon.Sample{}},
		{"Passkey", store.Credential{}},
		// The share-link surface. The dashboard one matters more than most:
		// this struct is the definition of what a read-only link discloses,
		// and a field the server sends that wire.ts does not declare is a
		// field nobody reviewing the TypeScript would know had been added.
		// Launch profiles. The one row in the panel that can hold somebody
		// else's credential, so a field this side sends and wire.ts does not
		// declare is a field nobody reviewing the TypeScript would know had
		// started leaving the machine.
		{"LaunchProfile", store.LaunchProfile{}},
		{"LaunchEnvVar", store.LaunchEnvVar{}},
		// A VNC display. The field that matters here is the one that is NOT
		// on the wire: store.VncTarget carries the password as `json:"-"` and
		// a separate hasPassword beside it, so a declared `password` in
		// wire.ts would be a field the frontend believes it can read and a
		// hole somebody would then try to fill.
		{"VncTarget", store.VncTarget{}},
		{"ShareLink", store.ShareLink{}},
		{"ShareDashboard", shareDashboard{}},
		{"ShareMachine", shareMachine{}},
		{"ShareCounts", shareCounts{}},
		{"ShareProject", shareProject{}},
		{"ShareSession", shareSession{}},
		// Boards. The dashboard draws these and nothing else, so a field the
		// server stopped sending is a widget that renders blank on a wall with
		// nobody in front of it to notice.
		{"ShareBoard", store.Board{}},
		{"ShareWidget", store.Widget{}},
		{"ShareWidgetSpec", store.WidgetSpec{}},
		{"SharePreset", store.Preset{}},
		{"ShareCatalogue", shareCatalogue{}},
		// The two sections that were added when the board turned out to be
		// empty because the panel had no history. Pinned for the same reason
		// everything else here is, with one extra: the repository half is the
		// part of this surface that reads somebody's working tree, so a field
		// added to it and not declared in wire.ts is a disclosure nobody
		// reviewing the TypeScript would know had been made.
		{"ShareFlow", shareFlow{}},
		{"ShareFlowTotals", shareFlowTotals{}},
		{"ShareFlowBucket", shareFlowBucket{}},
		{"ShareFeed", shareFeed{}},
		{"ShareFeedEntry", shareFeedEntry{}},
		{"ShareRepo", shareRepo{}},
		{"ShareRepoTotals", shareRepoTotals{}},
		{"ShareRepoDay", shareRepoDay{}},
		{"ShareRepoProject", shareRepoProject{}},
		{"ShareRepoPRs", shareRepoPRs{}},
		{"ShareSpend", shareSpend{}},
		{"ShareSpendTotals", shareSpendTotals{}},
		{"ShareSpendBucket", shareSpendBucket{}},
		{"ShareSpendGroup", shareSpendGroup{}},
		// Token usage. Pinned from the first commit rather than after the
		// first drift, because this surface has more fields than anything
		// above it and every one of them is a number somebody will believe.
		// The git tab. Its rows come off a disk rather than out of the
		// database, which does not make them less hand-written on the other
		// side -- a field the server stops sending is a branch name that is
		// `undefined` at runtime and renders as nothing.
		{"GitInfo", gitResponse{}},
		{"GitStatus", git.Status{}},
		{"GitChange", git.Change{}},
		{"GitCommit", git.Commit{}},
		{"GitRemote", git.Remote{}},
		{"GitSession", gitSession{}},
		{"GitHubResult", githubResponse{}},
		{"GitPR", git.PR{}},
		{"TokenUsage", tokenUsageResponse{}},
		{"TokenUsageSource", tokenUsageSource{}},
		{"TokenUsageSession", tokenUsageSession{}},
		{"TokenUsageProject", tokenUsageProject{}},
		{"TokenUsageTool", store.UsageToolTotals{}},
		{"UsageDay", store.UsageDay{}},
		{"UsageTotals", store.UsageTotals{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := jsonKeys(t, tc.row)
			declared := interfaceFields(t, string(src), tc.name, path)

			missing := difference(declared, sent)
			extra := difference(sent, declared)
			if len(missing) > 0 {
				t.Errorf("wire.ts declares %v on %s, and the server does not send them. "+
					"They are undefined at runtime and nothing type-checks that.", missing, tc.name)
			}
			if len(extra) > 0 {
				t.Errorf("the server sends %v on %s, and wire.ts does not declare them. "+
					"The frontend cannot read a field it has not been told about.", extra, tc.name)
			}
		})
	}
}

// jsonKeys reports the field names a struct sends, read from its tags.
//
// Tags rather than json.Marshal of a zero value, which is what this did
// first. Marshalling drops every `omitempty` field, so adding `omitempty`
// anywhere — a normal thing to do — made this report that "the server does not
// send them" about fields the server sends whenever they are not empty. The
// remedy it named was to delete a correct line from wire.ts.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	out := collectTags(t, reflect.TypeOf(v))
	if len(out) == 0 {
		t.Fatalf("no json fields found on %T; the reader is reading nothing", v)
	}
	sort.Strings(out)
	return out
}

func collectTags(t *testing.T, rt reflect.Type) []string {
	t.Helper()
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		t.Fatalf("not a struct: %s", rt)
	}
	var out []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, tagged := f.Tag.Lookup("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" && !strings.Contains(tag, ",") {
			continue
		}
		// An embedded struct with no name of its own promotes its fields into
		// the object, which is how the socket's message envelope is built.
		if f.Anonymous && name == "" {
			out = append(out, collectTags(t, f.Type)...)
			continue
		}
		if !tagged || name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// interfaceFields reads the property names out of one `export interface` block.
func interfaceFields(t *testing.T, src, name, path string) []string {
	t.Helper()
	start := regexp.MustCompile(`(?m)^export interface ` + name + ` \{$`).FindStringIndex(src)
	if start == nil {
		t.Fatalf("no `export interface %s` in %s; the shape of the file changed and this "+
			"test is no longer comparing anything", name, path)
	}
	rest := src[start[1]:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("interface %s in %s is not closed", name, path)
	}
	body := rest[:end]

	prop := regexp.MustCompile(`(?m)^\s{2}([A-Za-z_][A-Za-z0-9_]*)\??:`)
	var out []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if m := prop.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatalf("no properties found in interface %s; the parser is reading nothing", name)
	}
	sort.Strings(out)
	return out
}

func difference(a, b []string) []string {
	have := make(map[string]bool, len(b))
	for _, s := range b {
		have[s] = true
	}
	var out []string
	for _, s := range a {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

// The fourth constant on the wire, which nothing pinned.
//
// TestBinaryFrameLayoutMatchesTheClient in internal/ws pins three of the four
// constants wire.ts declares. EXIT_VANISHED is the one it does not, for a
// mechanical reason: that test parses `0x..|\d+`, which cannot match `-1`, and
// compares as uint64, while this value is a store constant. Here instead,
// where store is already imported and wire.ts is already read.
//
// What drift there does is not subtle, and it already happened once. The
// frontend treats any non-zero exit status that is not EXIT_VANISHED as a
// crash -- Sidebar.tsx counts those for the project summary -- so a session
// whose tmux session merely disappeared would be reported as having crashed,
// with a badge reading a number no process could have returned. wire.ts's own
// comment describes the first version doing exactly that: "a badge reading
// 'exit -1', a tooltip promising 'the process exited with status -1', and a
// project summary that counted it as a crash".
func TestExitVanishedMatchesTheStore(t *testing.T) {
	const path = "../../web/src/protocol/wire.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	// The leading minus is the point: a pattern without it silently matches
	// the `1` and reports agreement, which is worse than not testing at all.
	m := regexp.MustCompile(`EXIT_VANISHED\s*=\s*(-?\d+)`).FindStringSubmatch(string(src))
	if m == nil {
		t.Fatalf("EXIT_VANISHED is not declared in %s", path)
	}
	got, perr := strconv.ParseInt(m[1], 10, 64)
	if perr != nil {
		t.Fatalf("EXIT_VANISHED = %q: %v", m[1], perr)
	}
	if got != store.ExitStatusVanished {
		t.Errorf("EXIT_VANISHED is %d in %s and %d in store; a session whose tmux session "+
			"vanished will be counted as a crash and shown a status no process could return",
			got, path, store.ExitStatusVanished)
	}
}

// Every audit event this server can write, spelled out.
//
// The field is what `GROUP BY event` groups on -- the query the runbook hands
// an operator asking "is somebody hammering this panel" -- what the settings
// page lists, and what a fail2ban rule matches. So the names are an interface,
// and pairs that belong together have to share a prefix: login / login.failed,
// setup.completed / setup.rejected, passkey.registered / passkey.removed.
//
// `password_changed` and `password_change_refused` did not, which is the one
// pair a reader most wants together. They are `password.changed` and
// `password.change_refused` now, and migration v7 renames the rows already
// written -- a history spelled two ways is worse than either spelling, because
// the group-by the rename exists to fix would still return two rows for one
// thing.
//
// An explicit list rather than a pattern, for the same reason openRoutes is a
// list: a pattern says the shape is plausible, and a list makes adding one an
// edit somebody has to look at.
func TestEveryAuditEventIsAccountedFor(t *testing.T) {
	want := map[string]bool{
		"blocked":                 true,
		"hooks.installed":         true,
		"hooks.removed":           true,
		"hook.rejected":           true,
		"login":                   true,
		"login.failed":            true,
		"passkey.clone_warning":   true,
		"passkey.register.failed": true,
		"passkey.registered":      true,
		"passkey.removed":         true,
		"password.change_refused": true,
		"password.changed":        true,
		"profile.created":         true,
		"profile.deleted":         true,
		"profile.updated":         true,
		"setup.completed":         true,
		"share.created":           true,
		"share.locked":            true,
		"share.rejected":          true,
		"share.revoked":           true,
		"share.unlocked":          true,
		"share.updated":           true,
		"token.created":           true,
		"token.revoked":           true,
		"update.installed":        true,
		"vnc.added":               true,
		"vnc.changed":             true,
		"vnc.removed":             true,
		"webhooks.changed":        true,
		"setup.rejected":          true,
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	re := regexp.MustCompile(`\.audit(?:FromOutside)?\([^,]+,\s*"([a-z][a-z._]*)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatal(rerr)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			found[m[1]] = true
		}
	}
	if len(found) < 10 {
		t.Fatalf("only found %d audit events in the source (%v); the pattern has stopped "+
			"matching and this test is checking nothing", len(found), found)
	}

	for e := range found {
		if !want[e] {
			t.Errorf("the server writes audit event %q and this list does not have it. If it is "+
				"new, check it shares a prefix with its pair -- that prefix is what GROUP BY "+
				"and a fail2ban rule work on.", e)
		}
	}
	for e := range want {
		if !found[e] {
			t.Errorf("this list has %q and nothing writes it any more", e)
		}
	}
}

// Every route the panel serves is written down in docs/api.md.
//
// The API is offered as something to build against, which makes the document a
// promise rather than a description. Two ways it rots: an endpoint is added and
// the page is not, so the thing an agent needs is undiscoverable; or an endpoint
// is removed and the page still describes it, which is worse, because somebody
// writes code against a paragraph.
//
// The same shape as the runbook and doctor, the state enum and its three
// mirrors, and wire.ts: a definition with a copy somewhere no compiler looks.
func TestTheAPIDocCoversEveryRoute(t *testing.T) {
	ts, srv := newTestServer(t)
	_ = ts

	doc, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatalf("read the API doc: %v", err)
	}
	text := string(doc)

	routes := map[string]bool{}
	err = chi.Walk(srv.Routes().(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			// The static frontend and the method-fanout chi generates for the
			// WebSocket are not API surface. `/ws` itself is, and is listed.
			if !strings.HasPrefix(route, "/api/") && route != "/ws" {
				return nil
			}
			if route == "/ws" && method != http.MethodGet {
				return nil
			}
			// chi reports a trailing slash on some groups; the doc writes the
			// path the way a caller types it.
			route = strings.TrimSuffix(route, "/")
			routes[method+" "+route] = true
			return nil
		})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(routes) < 20 {
		t.Fatalf("only found %d API routes (%v); the walk has stopped matching and this test "+
			"is checking nothing", len(routes), routes)
	}

	// The doc's headings, parsed once. A query string is how a caller uses the
	// endpoint and is not part of the route, so it comes off here rather than
	// being special-cased at each comparison -- which is what the first version
	// did, and it reported four documented endpoints as missing.
	documented := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "### `") {
			continue
		}
		entry := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "### `"), "`")
		if i := strings.Index(entry, "?"); i >= 0 {
			entry = strings.TrimSpace(entry[:i])
		}
		if entry != "" {
			documented[entry] = true
		}
	}

	var missing []string
	for r := range routes {
		if !documented[r] {
			missing = append(missing, r)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("docs/api.md does not mention %v. The API is offered as something to build "+
			"against, so an endpoint missing from it is one nobody can find.", missing)
	}

	// And the other direction: a heading for a route that no longer exists.
	for entry := range documented {
		if routes[entry] {
			continue
		}
		t.Errorf("docs/api.md documents %q and the router has no such route; somebody will "+
			"write code against that paragraph", entry)
	}
}

// The API doc named a request field that does not exist.
//
// `PUT /api/projects/{id}/notes` takes `baseRev`; the doc said `rev`, which is
// what the *response* calls the same number. Anyone building against the page
// got a 400. TestTheAPIDocCoversEveryRoute did not see it, and could not: it
// compares routes, and this was a field name inside one.
//
// Pinned narrowly rather than by a scheme for checking every documented field
// against every struct tag. The asymmetry is the thing that goes wrong here --
// read `rev`, send `baseRev` -- and a test that says so by name is worth more
// than a general one that would have passed anyway, since `rev` is a real tag
// on the response.
func TestTheNotesRequestFieldIsDocumentedByItsRealName(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "api.md"))
	if err != nil {
		t.Fatalf("read docs/api.md: %v", err)
	}
	text := string(doc)

	// The name the handler actually decodes, taken from the struct rather than
	// written out here, so renaming the field fails this instead of drifting.
	field := jsonTagOf(t, putNoteRequest{}, "BaseRev")
	if !strings.Contains(text, "`"+field+"`") {
		t.Errorf("docs/api.md never mentions %q, which is the field PUT notes decodes", field)
	}
	if strings.Contains(text, `"content": "...", "rev"`) {
		t.Error(`docs/api.md describes the notes request as taking "rev"; that is the ` +
			`response field name, and the server rejects it as unknown`)
	}
}

// jsonTagOf reads the json name of one field, so a test can assert about the
// wire name without repeating it.
func jsonTagOf(t *testing.T, v any, field string) string {
	t.Helper()
	f, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		t.Fatalf("%T has no field %s", v, field)
	}
	return strings.Split(f.Tag.Get("json"), ",")[0]
}
