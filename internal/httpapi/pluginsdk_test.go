package httpapi

import (
	"testing"

	"github.com/jiangmuran/vibepanel/internal/plugins"
)

// The SDK's types are the contract a plugin is written against, and the Go
// structs are what is sent; the two are hand-written on either side of a
// JSON boundary with no type system across it. Pinned both ways: a field the
// server stops sending is `undefined` in a plugin that still type-checks,
// and a field the server adds without declaring is one no plugin can see.
func TestTheSDKTypesMatchThePluginView(t *testing.T) {
	src := string(plugins.Types)
	const path = "internal/plugins/sdk/vibepanel-plugin.d.ts"
	for _, pair := range []struct {
		ts  string
		gos any
	}{
		{"PluginView", pluginView{}},
		{"PluginIdentity", pluginIdentity{}},
		{"PluginViewProject", pluginViewProject{}},
		{"PluginViewSession", pluginViewSession{}},
		{"Note", pluginNote{}},
		{"Todo", pluginTodo{}},
	} {
		want := interfaceFields(t, src, pair.ts, path)
		got := jsonKeys(t, pair.gos)
		if missing := difference(want, got); len(missing) > 0 {
			t.Errorf("%s declares %v and %T does not send them: a plugin reads undefined", pair.ts, missing, pair.gos)
		}
		if extra := difference(got, want); len(extra) > 0 {
			t.Errorf("%T sends %v and %s does not declare them: no plugin can see them", pair.gos, extra, pair.ts)
		}
	}
}
