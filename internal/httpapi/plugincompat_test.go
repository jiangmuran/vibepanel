package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/plugins"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// compatExpect is a fixture's expect.json: what the plugin did when it was
// frozen, which a later panel must still do (docs/plugins.md §10).
type compatExpect struct {
	Frozen  string            `json:"frozen"`
	Caps    []string          `json:"caps"`
	Secrets map[string]string `json:"secrets"`
	Theme   bool              `json:"theme"`
	Frames  []string          `json:"frames"`
	// API is called on the frame's credential, as the SDK would.
	API []compatCall `json:"api"`
	// Routes are the plugin's own, through the owner's door /api/ext/{id}/.
	Routes []compatCall `json:"routes"`
	Event  *struct {
		Name string  `json:"name"`
		Data string  `json:"data"`
		Want float64 `json:"want"`
	} `json:"event"`
	Process *struct {
		Output string `json:"output"`
	} `json:"process"`
}

type compatCall struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body"`
	Status int    `json:"status"`
	Want   string `json:"want"`
}

// Every fixture in internal/plugins/testdata/compat is a plugin written
// against contract v1 when it was frozen. Each is installed through the real
// routes with the capabilities it asks for, its frames served, its SDK calls
// answered, its routes called, its event delivered and its process run. A
// change to the panel that breaks a two-year-old plugin fails here, on the
// branch that made it, rather than on somebody's wall.
func TestEveryCompatFixtureStillInstallsAndRuns(t *testing.T) {
	root := filepath.Join("..", "plugins", "testdata", "compat")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 5 {
		t.Fatalf("%d fixtures; the corpus is one per rung at least", len(entries))
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			runCompatFixture(t, filepath.Join(root, e.Name()))
		})
	}
}

func runCompatFixture(t *testing.T, dir string) {
	t.Helper()
	var ex compatExpect
	raw, err := os.ReadFile(filepath.Join(dir, "expect.json")) //nolint:gosec // testdata
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &ex); err != nil {
		t.Fatal(err)
	}
	b, err := plugins.ReadDir(dir)
	if err != nil {
		t.Fatalf("the fixture no longer reads as a plugin: %v", err)
	}
	for _, ig := range b.Ignored {
		if ig.Path != "expect.json" {
			t.Errorf("the reader ignores %s: %s", ig.Path, ig.Reason)
		}
	}
	id := b.Manifest.ID
	if sc := plugins.Describe(b.Manifest, nil, "99.0.0"); sc.Refused != nil {
		t.Fatalf("the install screen refuses it: %s", sc.Refused.Text.EN)
	}

	ts, srv := newTestServer(t)
	var zipped bytes.Buffer
	if err := plugins.WriteArchive(&zipped, b.Raw, b.Files); err != nil {
		t.Fatal(err)
	}
	if status, body := postZip(t, ts, zipped.Bytes()); status != http.StatusCreated {
		t.Fatalf("add: %d %s", status, body)
	}
	caps, _ := json.Marshal(ex.Caps)
	if status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/"+id+"/install", `{"caps":`+string(caps)+`}`); status != http.StatusOK {
		t.Fatalf("install: %d %s", status, body)
	}
	for name, value := range ex.Secrets {
		if status, body := doJSON(t, ts, http.MethodPut, "/api/settings/plugins/"+id+"/secrets/"+name, `{"value":"`+value+`"}`); status != http.StatusNoContent {
			t.Fatalf("secret %s: %d %s", name, status, body)
		}
	}
	if status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/"+id+"/enable", `{}`); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, body)
	}
	srv.serviceCtx = t.Context()
	srv.ensurePluginWorkers(t.Context())

	if ex.Theme {
		res, err := ts.Client().Get(ts.URL + "/plugin-themes.css")
		if err != nil {
			t.Fatal(err)
		}
		css, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK || !strings.Contains(string(css), "ext-"+id) {
			t.Errorf("the theme is not served: %d %q", res.StatusCode, css)
		}
	}

	if len(ex.Frames) > 0 || len(ex.API) > 0 {
		status, body := doJSON(t, ts, http.MethodPost, "/api/settings/plugins/"+id+"/grant", `{}`)
		if status != http.StatusCreated {
			t.Fatalf("grant: %d %s", status, body)
		}
		var g mintedGrant
		_ = json.Unmarshal(body, &g)
		for _, f := range ex.Frames {
			res, _ := anonGET(t, ts, g.Base+f)
			if res.StatusCode != http.StatusOK {
				t.Errorf("frame %s: %d", f, res.StatusCode)
			}
		}
		for _, call := range ex.API {
			status, body := anonJSON(t, ts, call.Method, g.API+call.Path, call.Body)
			check(t, "api "+call.Method+" "+call.Path, status, body, call)
		}
	}

	for _, call := range ex.Routes {
		status, body := doJSON(t, ts, call.Method, "/api/ext/"+id+"/"+call.Path, call.Body)
		check(t, "route "+call.Method+" "+call.Path, status, body, call)
	}

	if ex.Event != nil {
		srv.pluginEventRaised(pluginEvent{Name: ex.Event.Name, SessionID: "s", ProjectID: "p"})
		deadline := time.Now().Add(5 * time.Second)
		for {
			rows, _ := srv.DB.PluginData(t.Context(), id, store.PageDataLive)
			var v float64
			if row, ok := rows[ex.Event.Data]; ok {
				_ = json.Unmarshal(row.Value, &v)
			}
			if v == ex.Event.Want {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("after %s, data %q = %v, want %v", ex.Event.Name, ex.Event.Data, v, ex.Event.Want)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	if ex.Process != nil {
		srv.ensurePluginProcesses(t.Context())
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, body := doJSON(t, ts, http.MethodGet, "/api/settings/plugins/"+id+"/process", "")
			if strings.Contains(string(body), ex.Process.Output) {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("the process did not print %q: %s", ex.Process.Output, body)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func check(t *testing.T, what string, status int, body []byte, call compatCall) {
	t.Helper()
	wantStatus := call.Status
	if wantStatus == 0 {
		wantStatus = http.StatusOK
	}
	if status != wantStatus {
		t.Errorf("%s: %d %s, want %d", what, status, body, wantStatus)
		return
	}
	if call.Want != "" && !strings.Contains(string(body), call.Want) {
		t.Errorf("%s: %s does not contain %s", what, body, call.Want)
	}
}
