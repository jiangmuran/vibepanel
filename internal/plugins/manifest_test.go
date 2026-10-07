package plugins

import (
	"encoding/json"
	"strings"
	"testing"
)

// full is a manifest that climbs every rung, for tests that need one of
// everything.
const full = `{
  "plugin": 1, "id": "standup", "name": {"en": "Stand-up", "zh-CN": "站会"}, "version": "0.3.0",
  "panel": ">=1.0", "description": {"en": "A digest."}, "author": "jmr",
  "theme": {"file": "theme.css", "name": {"en": "Paper"}, "scheme": "light"},
  "panels": [
    {"slot": "sidepanel.pane", "entry": "pane.html", "title": {"en": "Stand-up"}, "icon": "list-checks"},
    {"slot": "settings.section", "entry": "settings.html", "title": {"en": "Stand-up"}, "group": "notify"},
    {"slot": "page", "entry": "index.html", "title": {"en": "Stand-up"}, "path": "standup"}
  ],
  "capabilities": ["read:panel", "read:notes", "write:todos", "ui:open"],
  "settings": {"group": "notify", "fields": [
    {"key": "quiet", "type": "bool", "default": true, "label": {"en": "Quiet"}},
    {"key": "channel", "type": "enum", "values": ["slack", "email"], "default": "slack", "label": {"en": "Channel"}},
    {"key": "webhook", "type": "secret", "label": {"en": "Webhook"}}
  ]},
  "data": {"digest": {"type": "text", "max": 2000}},
  "sources": [{"key": "calendar", "url": "https://cal.example.com/v1", "every": "10m"}],
  "server": {"entry": "server.js", "every": "15m", "on": ["session.state"], "routes": {"GET /digest": "digest"}},
  "inbound": {"path": "hook", "secret": "HOOK_SECRET"},
  "process": {"command": ["node", "bot/index.js"], "capabilities": ["read:panel", "sessions:input"],
              "env": ["SLACK_TOKEN"], "hosts": ["slack.com"]},
  "unsandboxed": {"entry": "main.mjs", "tested": "1.26.0 - 1.26.x"}
}`

func TestAFullManifestParses(t *testing.T) {
	m, err := ParseManifest([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	r := m.Rungs()
	if !(r.Theme && r.Panel && r.Service && r.Process && r.Unsandboxed) {
		t.Errorf("rungs = %+v, want all five", r)
	}
	if got := strings.Join(r.Names(), ","); got != "theme,panel,service,process,unsandboxed" {
		t.Errorf("rung names = %s", got)
	}
	want := []string{"read:panel", "read:notes", "write:todos", "sessions:input", "ui:open", "net:cal.example.com"}
	if got := m.AllCapabilities(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("AllCapabilities = %v, want %v (table order, net hosts last)", got, want)
	}
	if got := strings.Join(m.SecretNames(), ","); got != "SLACK_TOKEN,HOOK_SECRET,WEBHOOK" {
		t.Errorf("SecretNames = %s", got)
	}
}

// Each of these is one edit to a valid manifest that must be refused, with
// the word the error has to carry so the person knows which line to fix.
func TestAManifestIsRefusedForTheRightReason(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"manifest version", `"plugin": 1`, `"plugin": 2`, "plugin must be 1"},
		{"id", `"id": "standup"`, `"id": "Stand Up"`, "id is 3-40"},
		{"id too short", `"id": "standup"`, `"id": "ab"`, "id is 3-40"},
		{"english name", `"name": {"en": "Stand-up", "zh-CN": "站会"}`, `"name": {"zh-CN": "站会"}`, "name.en is required"},
		{"version", `"version": "0.3.0"`, `"version": "3"`, "MAJOR.MINOR.PATCH"},
		{"panel constraint", `"panel": ">=1.0"`, `"panel": "1.26"`, "panel:"},
		{"homepage", `"author": "jmr"`, `"author": "jmr", "homepage": "http://x.example"`, "https"},
		{"unknown key", `"plugin": 1`, `"plugin": 1, "colour": 1`, "unknown field"},
		{"slot", `"slot": "sidepanel.pane"`, `"slot": "sidebar.pane"`, "unknown slot"},
		{"page path", `"path": "standup"`, `"path": ""`, "a page slot needs a path"},
		{"group on a pane", `"icon": "list-checks"}`, `"icon": "list-checks", "group": "notify"}`, "belong to"},
		{"capability", `"capabilities": ["read:panel", "read:notes"`, `"capabilities": ["read:everything", "read:notes"`, "is not a capability"},
		{"capability twice", `"capabilities": ["read:panel", "read:notes"`, `"capabilities": ["read:panel", "read:panel"`, "listed twice"},
		{"settings key", `"key": "quiet"`, `"key": "Quiet!"`, `key "Quiet!"`},
		{"settings enum", `"type": "enum", "values": ["slack", "email"], "default": "slack",`, `"type": "enum",`, "an enum needs values"},
		{"secret default", `"type": "secret",`, `"type": "secret", "default": "x",`, "a secret has no default"},
		{"server entry", `"entry": "server.js"`, `"entry": "server.exe"`, "server.entry"},
		{"every", `"every": "15m"`, `"every": "15s"`, "server.every"},
		{"event", `"on": ["session.state"]`, `"on": ["session.started"]`, "is not an event"},
		{"route", `"routes": {"GET /digest": "digest"}`, `"routes": {"GET digest": "digest"}`, `is not "METHOD /path"`},
		{"inbound without server", `"server": {"entry": "server.js", "every": "15m", "on": ["session.state"], "routes": {"GET /digest": "digest"}},`, ``, `needs "server"`},
		{"process command", `"command": ["node", "bot/index.js"]`, `"command": []`, "process.command needs a program"},
		{"process ui cap", `"capabilities": ["read:panel", "sessions:input"]`, `"capabilities": ["read:panel", "ui:open"]`, "a frame's capability"},
		{"process env", `"env": ["SLACK_TOKEN"]`, `"env": ["slack token"]`, "process.env"},
		{"process host", `"hosts": ["slack.com"]`, `"hosts": ["slack com"]`, "process.hosts"},
		{"tested", `"tested": "1.26.0 - 1.26.x"`, `"tested": "soon"`, "unsandboxed.tested"},
		{"source scheme", `"url": "https://cal.example.com/v1"`, `"url": "http://cal.example.com/v1"`, "https"},
		{"title in one language only", `"title": {"en": "Stand-up"}, "icon"`, `"title": {"zh-CN": "站会"}, "icon"`, "title.en is required"},
	}
	for _, tc := range cases {
		if !strings.Contains(full, tc.from) {
			t.Fatalf("%s: the fixture no longer contains %q", tc.name, tc.from)
		}
		src := strings.Replace(full, tc.from, tc.to, 1)
		_, err := ParseManifest([]byte(src))
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not say %q", tc.name, err, tc.want)
		}
	}
}

func TestAManifestNeedsOneRung(t *testing.T) {
	_, err := ParseManifest([]byte(`{"plugin":1,"id":"empty","name":{"en":"Empty"},"version":"1.0.0"}`))
	if err == nil || !strings.Contains(err.Error(), "at least one of") {
		t.Errorf("an empty plugin was accepted: %v", err)
	}
}

func TestEncodeRoundTrips(t *testing.T) {
	m, err := ParseManifest([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, dropped := DecodeStored(raw)
	if len(dropped) != 0 {
		t.Errorf("a valid manifest dropped %v", dropped)
	}
	again, _ := back.Encode()
	if string(again) != string(raw) {
		t.Errorf("encode → decode → encode changed the manifest:\n%s\n%s", raw, again)
	}
}

// What DecodeStored exists for: a stored manifest this build no longer
// accepts in one part is read with that part dropped and named, not refused.
func TestAStoredManifestDropsWhatNoLongerValidatesAndNamesIt(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(full), &m); err != nil {
		t.Fatal(err)
	}
	// A slot this build does not know, as a later build might have added.
	m["panels"] = []any{map[string]any{"slot": "sidebar.footer", "entry": "x.html", "title": map[string]any{"en": "x"}}}
	// An event this build does not know.
	m["server"] = map[string]any{"entry": "server.js", "on": []any{"session.renamed"}}
	raw, _ := json.Marshal(m)
	got, dropped := DecodeStored(raw)
	if got.Validate() != nil {
		t.Fatalf("the result does not validate: %v", got.Validate())
	}
	if len(got.Panels) != 0 || got.Server != nil {
		t.Errorf("panels and server should have been dropped, got %d panels and server %v", len(got.Panels), got.Server != nil)
	}
	if strings.Join(dropped, ",") != "panels,server" && strings.Join(dropped, ",") != "server,panels" {
		t.Errorf("dropped = %v, want panels and server named", dropped)
	}
	if got.Theme == nil || got.Process == nil {
		t.Errorf("parts that still validate were dropped too")
	}
}

func TestAStoredManifestThatIsNotJSONIsABareRow(t *testing.T) {
	got, dropped := DecodeStored([]byte("{nope"))
	if got.ID != "plugin" || len(dropped) != 1 || dropped[0] != "manifest" {
		t.Errorf("got %+v (%v)", got, dropped)
	}
}

func TestParseEvery(t *testing.T) {
	for in, want := range map[string]int{"1m": 1, "15m": 15, "1h": 60, "24h": 1440} {
		if got, err := ParseEvery(in); err != nil || got != want {
			t.Errorf("%s = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"30s", "0m", "25h", "5", "5 m"} {
		if _, err := ParseEvery(in); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}
