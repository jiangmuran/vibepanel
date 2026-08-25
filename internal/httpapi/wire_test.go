package httpapi

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/browse"
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
