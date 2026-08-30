package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/config"
)

// A write-only setting can be set from the panel and never read back out of
// it.
//
// CLOUDFLARE_API_TOKEN is an ACME credential. It was kept out of the editable
// list entirely, which kept it off the screen and also meant the TLS mode the
// panel recommends could not be configured from the panel at all. Write-only
// is what gives both -- and the half that is easy to lose is this one, because
// adding a key to EditableEnv is one line and puts the value in the response.
func TestTheAcmeTokenIsNeverInTheResponse(t *testing.T) {
	ts, _ := newTestServer(t)

	// The panel's own env file, with a token in it, the way a working ACME
	// install has.
	dir := t.TempDir()
	path := filepath.Join(dir, "vibepanel.env")
	const secret = "cf-token-do-not-disclose-8f21"
	if err := os.WriteFile(path,
		[]byte("VIBEPANEL_DOMAIN=panel.example.com\nCLOUDFLARE_API_TOKEN="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIBEPANEL_ENV_FILE", path)

	res, err := ts.Client().Get(ts.URL + "/api/settings/env")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/settings/env: %d", res.StatusCode)
	}

	// The whole response, not one field: the value must not arrive anywhere in
	// it, including somewhere added later that nobody thought about.
	if strings.Contains(string(body), secret) {
		t.Errorf("the response carries the ACME token verbatim:\n%s", body)
	}

	var got envResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.SecretSet["CLOUDFLARE_API_TOKEN"] {
		t.Error("a token that is set is reported as not set; the page will offer to add one that is already there")
	}
	// And it is not smuggled through the editable map either.
	for k, v := range got.Values {
		if config.Secret(k) {
			t.Errorf("%s is in values (=%q); a write-only setting has no value in a response", k, v)
		}
	}
}

// The same key can still be written. A setting nobody can set is not a
// setting, and this is the half the disclosure test above would happily pass
// with the feature deleted.
func TestTheAcmeTokenCanBeSetFromThePanel(t *testing.T) {
	ts, _ := newTestServer(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "vibepanel.env")
	if err := os.WriteFile(path, []byte("VIBEPANEL_DOMAIN=panel.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIBEPANEL_ENV_FILE", path)

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/settings/env",
		strings.NewReader(`{"values":{"CLOUDFLARE_API_TOKEN":"cf-new-token"}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("PUT: %d %s", res.StatusCode, strings.TrimSpace(string(b)))
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "CLOUDFLARE_API_TOKEN=cf-new-token") {
		t.Errorf("the file does not have the new token:\n%s", b)
	}
	// The paragraph above every other variable is still there. This file is
	// the user's and a tool that rewrites it wholesale is one people stop
	// trusting.
	if !strings.Contains(string(b), "VIBEPANEL_DOMAIN=panel.example.com") {
		t.Errorf("writing the token lost the rest of the file:\n%s", b)
	}
}
