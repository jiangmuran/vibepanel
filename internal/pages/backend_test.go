package pages

import (
	"strings"
	"testing"
)

// The example in docs/page-backend.md, which has to be a manifest the panel
// accepts or the document is teaching something that does not work.
const backendManifest = `{
  "sdk": 1, "name": "Lobby", "sections": ["sessions"],
  "data": {
    "announcement": { "type": "text", "max": 280, "default": "" },
    "goals":        { "type": "list", "item": { "type": "text", "max": 80 }, "max": 20 },
    "votes":        { "type": "counter" },
    "guestbook":    { "type": "log", "item": { "type": "object", "fields": {
                        "name": { "type": "text", "max": 40 },
                        "msg":  { "type": "text", "max": 140 } } }, "max": 200 },
    "notes":        { "type": "text", "max": 2000, "visibility": "admin" }
  },
  "admin": { "entry": "admin/index.html" },
  "sources": [
    { "key": "weather", "url": "https://api.example.com/v1/now?city=shanghai",
      "every": "10m", "headers": { "Authorization": "Bearer ${secret:WEATHER_TOKEN}" },
      "maxBytes": 65536, "timeout": "5s", "parse": "json" }
  ],
  "server": { "entry": "server.js", "every": "5m" },
  "actions": {
    "vote":  { "who": "visitor", "effect": { "increment": "votes" }, "rate": "5/min" },
    "sign":  { "who": "visitor", "input": { "name": { "type": "text", "max": 40 },
                                            "msg":  { "type": "text", "max": 140 } },
               "effect": { "append": "guestbook" }, "rate": "2/min" },
    "reset": { "who": "admin", "effect": "server" }
  }
}`

func TestTheDocumentedBackendManifestIsAccepted(t *testing.T) {
	m, err := ParseManifest([]byte(backendManifest))
	if err != nil {
		t.Fatal(err)
	}
	c := m.Capabilities()
	if !c.Data || !c.Admin || !c.Sources || !c.Server || !c.Actions || !c.VisitorActions {
		t.Errorf("capabilities = %+v", c)
	}
	if got := m.Sources[0].Secrets(); len(got) != 1 || got[0] != "WEATHER_TOKEN" {
		t.Errorf("secrets = %v", got)
	}
	raw, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if again := DecodeStored(raw); len(again.Actions) != 3 || again.Actions["vote"].Effect.Key != "votes" {
		t.Errorf("a stored backend did not survive: %+v", again.Actions)
	}
	for rel, private := range map[string]bool{"server.js": true, "admin/index.html": true,
		"admin/app.js": true, "index.html": false, "administrator.js": false} {
		if m.Private(rel) != private {
			t.Errorf("Private(%q) = %v", rel, !private)
		}
	}
}

func TestABackendManifestIsRefusedWithTheReason(t *testing.T) {
	base := func(extra string) string {
		return `{"sdk":1,"name":"x","sections":[],` + extra + `}`
	}
	for _, tc := range []struct{ why, manifest, want string }{
		{"a set effect for visitors", base(`"data":{"a":{"type":"text"}},"actions":{"x":{"who":"visitor","effect":{"set":"a"}}}`), "admin"},
		{"increment on text", base(`"data":{"a":{"type":"text"}},"actions":{"x":{"who":"visitor","effect":{"increment":"a"}}}`), "counter"},
		{"append on a counter", base(`"data":{"a":{"type":"counter"}},"actions":{"x":{"who":"visitor","effect":{"append":"a"}}}`), "log"},
		{"an effect on an undeclared key", base(`"actions":{"x":{"who":"visitor","effect":{"increment":"nope"}}}`), "not declared"},
		{"server effect with no server", base(`"actions":{"x":{"who":"admin","effect":"server"}}`), "server.entry"},
		{"an unknown who", base(`"data":{"a":{"type":"counter"}},"actions":{"x":{"who":"anyone","effect":{"increment":"a"}}}`), "who"},
		{"a bad rate", base(`"data":{"a":{"type":"counter"}},"actions":{"x":{"who":"visitor","effect":{"increment":"a"},"rate":"lots"}}`), "rate"},
		{"a list input field", base(`"data":{"a":{"type":"counter"}},"actions":{"x":{"who":"admin","effect":"server","input":{"l":{"type":"list","item":{"type":"text"}}}}},"server":{"entry":"server.js"}`), "input"},
		{"an http source", base(`"sources":[{"key":"s","url":"http://example.com","every":"10m"}]`), "https"},
		{"a secret in the url", base(`"sources":[{"key":"s","url":"https://example.com/?k=${secret:K}","every":"10m"}]`), "headers"},
		{"a source too often", base(`"sources":[{"key":"s","url":"https://example.com","every":"5s"}]`), "between"},
		{"a slow timeout", base(`"sources":[{"key":"s","url":"https://example.com","every":"10m","timeout":"30s"}]`), "between"},
		{"a Host header", base(`"sources":[{"key":"s","url":"https://example.com","every":"10m","headers":{"Host":"internal"}}]`), "not allowed"},
		{"a bad secret name", base(`"sources":[{"key":"s","url":"https://example.com","every":"10m","headers":{"A":"${secret:lower}"}}]`), "secret name"},
		{"an admin page at the root", base(`"admin":{"entry":"admin.html"}`), "directory"},
		{"server code in the admin dir", base(`"admin":{"entry":"admin/index.html"},"server":{"entry":"admin/server.js"}`), "admin"},
		{"a nested list", base(`"data":{"a":{"type":"list","item":{"type":"list","item":{"type":"text"}}}}`), "item"},
		{"a counter default", base(`"data":{"a":{"type":"counter","default":3}}`), "default"},
		{"text too long", base(`"data":{"a":{"type":"text","max":20000}}`), "max"},
		{"a log item with at", base(`"data":{"a":{"type":"log","item":{"type":"object","fields":{"at":{"type":"number"}}}}}`), "at"},
		{"a visibility on a field", base(`"data":{"a":{"type":"object","fields":{"b":{"type":"text","visibility":"admin"}}}}`), "visibility"},
	} {
		_, err := ParseManifest([]byte(tc.manifest))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.why, err, tc.want)
		}
	}
}

func TestVisitorTextIsHeldToTheStricterReading(t *testing.T) {
	spec := &DataSpec{Type: DataText}
	for _, s := range []string{"a\u202eb", "a\u2066b", "a\x00b", "a\x1bb"} {
		if _, err := spec.Check(s, false); err == nil {
			t.Errorf("owner text %q was accepted", s)
		}
	}
	if _, err := spec.Check("line one\nline two", false); err != nil {
		t.Errorf("the owner's line break was refused: %v", err)
	}
	if _, err := spec.Check("line one\nline two", true); err == nil {
		t.Error("a visitor's line break was accepted")
	}

	fields := map[string]*DataSpec{"name": {Type: DataText}, "n": {Type: DataNumber}}
	if _, err := CheckInput(fields, map[string]any{"name": "x", "extra": true}, true); err == nil {
		t.Error("an extra field in an action payload was accepted")
	}
	got, err := CheckInput(fields, map[string]any{"name": "x"}, true)
	if err != nil || got["n"] != float64(0) {
		t.Errorf("a missing field did not read as its zero: %v, %v", got, err)
	}
	if _, err := CheckInput(map[string]*DataSpec{}, map[string]any{"x": 1}, true); err == nil {
		t.Error("a payload for an action with no input was accepted")
	}
}

func TestALogKeepsItsNewestEntries(t *testing.T) {
	max := float64(2)
	spec := &DataSpec{Type: DataLog, Max: &max, Item: &DataSpec{Type: DataText}}
	got, err := spec.Check([]any{
		map[string]any{"at": float64(1), "value": "a"},
		map[string]any{"at": float64(2), "value": "b"},
		map[string]any{"at": float64(3), "value": "c"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	entries := got.([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["value"] != "b" {
		t.Errorf("log = %v, want the newest two", entries)
	}
}

func TestSecretsExpandOnlyWhenAllAreSet(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "A" {
			return "one", true
		}
		return "", false
	}
	if got, err := ExpandSecrets("Bearer ${secret:A}", lookup); err != nil || got != "Bearer one" {
		t.Errorf("expand = %q, %v", got, err)
	}
	if _, err := ExpandSecrets("${secret:A} ${secret:B}", lookup); err == nil || !strings.Contains(err.Error(), "B") {
		t.Errorf("a missing secret: %v", err)
	}
}

func TestLintSaysWhatABackendPointsAtThatIsNotThere(t *testing.T) {
	m, err := ParseManifest([]byte(backendManifest))
	if err != nil {
		t.Fatal(err)
	}
	b := Bundle{Manifest: m, Files: []File{
		{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte(`<script src="vibepanel.js"></script>`)},
		{Path: "server.js", ContentType: "text/javascript; charset=utf-8", Data: []byte("function onSchedule(ctx) { fetch('https://x') }")},
	}}
	codes := map[string]bool{}
	for _, p := range Lint(b) {
		codes[p.Code] = true
	}
	for _, want := range []string{"admin-missing", "server-api", "source-approval"} {
		if !codes[want] {
			t.Errorf("lint did not report %s: %v", want, codes)
		}
	}
}

func TestRatesAndDurationsReadOneWay(t *testing.T) {
	for s, ok := range map[string]bool{"5/min": true, "2/s": true, "100/hour": true, "0/min": false,
		"5/day": false, "5 / min": false, "5000/min": false} {
		if _, err := ParseRate(s); (err == nil) != ok {
			t.Errorf("ParseRate(%q) err = %v", s, err)
		}
	}
	for s, ok := range map[string]bool{"30s": true, "10m": true, "2h": true, "1h30m": false, "m": false, "-5m": false} {
		if _, err := ParseDuration(s); (err == nil) != ok {
			t.Errorf("ParseDuration(%q) err = %v", s, err)
		}
	}
}
