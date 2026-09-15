package httpapi

import (
	"bytes"
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

func TestASourceReachesOnlyPublicAddresses(t *testing.T) {
	for addr, public := range map[string]bool{
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "224.0.0.1": false,
		"255.255.255.255": false, "198.18.0.1": false, "240.0.0.1": false, "192.0.2.1": false,
		"::1": false, "fd00:ec2::254": false, "fe80::1": false, "::ffff:127.0.0.1": false,
		"::ffff:10.0.0.1": false, "64:ff9b::7f00:1": false, "2002:7f00:1::": false, "::": false,
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700:4700::1111": true,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != public {
			t.Errorf("publicAddr(%s) = %v, want %v", addr, got, public)
		}
	}
}

// The default fetcher does not reach a server on loopback, and the server is
// never asked -- the refusal is before the connection, not after it.
func TestTheRealFetcherRefusesLoopbackBeforeConnecting(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	for _, raw := range []string{srv.URL + "/x", "https://localhost:" + u.Port() + "/x"} {
		res := (&sourceFetcher{}).fetch(context.Background(), pages.SourceSpec{Key: "s", URL: raw, Every: "1m"}, nil)
		if res.OK || !strings.Contains(res.Error, "may not reach") && !strings.Contains(res.Error, "resolve") {
			t.Errorf("%s: %+v, want refused", raw, res)
		}
	}
	// A name that answers one public and one private address is refused whole.
	f := &sourceFetcher{resolve: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}}
	if res := f.fetch(context.Background(), pages.SourceSpec{Key: "s", URL: "https://mixed.example/x", Every: "1m"}, nil); res.OK ||
		!strings.Contains(res.Error, "may not reach") {
		t.Errorf("a name resolving to a private address beside a public one = %+v, want refused before dialling", res)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the loopback server was asked %d times", n)
	}
	if res := (&sourceFetcher{}).fetch(context.Background(), pages.SourceSpec{Key: "s", URL: "http://example.com", Every: "1m"}, nil); res.OK {
		t.Error("an http source was fetched")
	}
}

// testFetcher reaches srv as api.test, which is the one way a test gets past
// the guard; it is the guard's own knobs, set by name.
func testFetcher(srv *httptest.Server) *sourceFetcher {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &sourceFetcher{
		resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		allowAddr:  func(netip.Addr) bool { return true },
		rootCAs:    pool,
		serverName: "example.com",
	}
}

func TestAFetchIsBoundedAndNeverFollowsARedirect(t *testing.T) {
	var auth atomic.Value
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "https://169.254.169.254/latest/meta-data", http.StatusFound)
		case "/big":
			_, _ = w.Write(bytes.Repeat([]byte("x"), 5000))
		case "/text":
			_, _ = w.Write([]byte("plain words"))
		case "/fail":
			http.Error(w, "nope", http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{"temp":21}`))
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	f := testFetcher(srv)
	base := "https://api.test:" + u.Port()

	res := f.fetch(context.Background(), pages.SourceSpec{Key: "w", URL: base + "/now", Every: "1m"},
		map[string]string{"Authorization": "Bearer s3cr3t"})
	if !res.OK || res.Status != 200 || res.Value.(map[string]any)["temp"] != float64(21) || auth.Load() != "Bearer s3cr3t" {
		t.Fatalf("a good fetch = %+v, auth %v", res, auth.Load())
	}
	for path, want := range map[string]string{"/redirect": "redirect", "/big": "larger", "/fail": "500", "/text": "not JSON"} {
		res := f.fetch(context.Background(), pages.SourceSpec{Key: "w", URL: base + path, Every: "1m", MaxBytes: 1000}, nil)
		if res.OK || !strings.Contains(res.Error, want) {
			t.Errorf("%s = %+v, want an error mentioning %q", path, res, want)
		}
	}
	if res := f.fetch(context.Background(), pages.SourceSpec{Key: "w", URL: base + "/text", Every: "1m", Parse: "text"}, nil); !res.OK || res.Value != "plain words" {
		t.Errorf("a text source = %+v", res)
	}
}

const sourceManifest = `{"sdk":1,"name":"Weather","sections":[],
	"sources":[{"key":"weather","url":"https://api.test:PORT/now","every":"1m",
		"headers":{"Authorization":"Bearer ${secret:WEATHER_TOKEN}"}}]}`

func TestSourcesNeedAnApprovedHostAndTheirSecrets(t *testing.T) {
	var auth atomic.Value
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			http.Error(w, "who are you", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"temp":19}`))
	}))
	defer api.Close()
	apiURL, _ := url.Parse(api.URL)
	ts, srv := newTestServer(t)
	srv.pb.sources.fetcher = testFetcher(api)
	p := newPublishedPage(t, ts, strings.ReplaceAll(sourceManifest, "PORT", apiURL.Port()), nil)
	link := pageLink(t, ts, p.page.ID, "")
	ctx := context.Background()
	refresh := func() map[string]*sourceResult {
		page, _ := srv.DB.SharePageByID(ctx, p.page.ID)
		m, _ := srv.pageManifestFor(ctx, page, store.PageDataLive)
		srv.refreshSources(ctx, page, store.PageDataLive, m, true)
		_, snap, _ := snapshotGET(t, ts, link.Token)
		return snap.Sources
	}

	if got := refresh()["weather"]; got.OK || got.Error != "host not approved" {
		t.Errorf("before approval = %+v", got)
	}
	host := "api.test:" + apiURL.Port()
	for _, bad := range []string{"https://api.test", "*.test", "api.test:443", "10.0.0.1/8", "api test"} {
		if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/pages/"+p.page.ID+"/hosts", "application/json",
			[]byte(`{"hosts":["`+bad+`"]}`)); code != http.StatusBadRequest {
			t.Errorf("approving %q = %d, want 400", bad, code)
		}
	}
	if code, body, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/pages/"+p.page.ID+"/hosts", "application/json",
		[]byte(`{"hosts":["`+host+`"]}`)); code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, body)
	}
	if got := refresh()["weather"]; got.OK || !strings.Contains(got.Error, "WEATHER_TOKEN is not set") {
		t.Errorf("without the secret = %+v", got)
	}

	if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/pages/"+p.page.ID+"/secrets/WEATHER_TOKEN",
		"application/json", []byte(`{"value":"tok-123"}`)); code != http.StatusNoContent {
		t.Fatalf("set secret = %d", code)
	}
	got := refresh()["weather"]
	if !got.OK || got.Value.(map[string]any)["temp"] != float64(19) {
		t.Fatalf("with host and secret = %+v", got)
	}

	// The secret is never readable back: not from the list, not from the
	// snapshot, not from the database file's column.
	_, list, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/pages/"+p.page.ID+"/secrets", "", nil)
	_, sources, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/pages/"+p.page.ID+"/sources", "", nil)
	_, snapRaw := anonGET(t, ts, "/api/share/"+link.Token+"/v1/snapshot")
	var sealed []byte
	_ = srv.DB.SQL().QueryRow(`SELECT value_enc FROM share_page_secrets`).Scan(&sealed)
	for where, b := range map[string][]byte{"secrets": list, "sources": sources, "snapshot": snapRaw, "column": sealed} {
		if bytes.Contains(b, []byte("tok-123")) {
			t.Errorf("the secret is readable in %s", where)
		}
	}
	if !strings.Contains(string(list), "WEATHER_TOKEN") || !strings.Contains(string(sources), `"set":true`) {
		t.Errorf("settings does not say the secret is set: %s / %s", list, sources)
	}
	if !auditHas(t, srv, "page.secret_set", "WEATHER_TOKEN") || !auditHas(t, srv, "page.hosts_changed", host) {
		t.Error("approving a host or setting a secret left no audit row")
	}
	if auditHas(t, srv, "page.secret_set", "tok-123") {
		t.Error("the audit trail carries a secret's value")
	}

	// A secret that the source echoes in an error is redacted.
	srv.DB.SQL().Exec(`DELETE FROM share_page_secrets`) //nolint:errcheck
	sendRaw(t, ts, http.MethodPut, "/api/settings/pages/"+p.page.ID+"/secrets/WEATHER_TOKEN", "application/json", []byte(`{"value":"wrong-one"}`))
	if got := refresh()["weather"]; got.OK || strings.Contains(got.Error, "wrong-one") {
		t.Errorf("with a wrong secret = %+v", got)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/pages/"+p.page.ID+"/secrets/lower", "application/json", []byte(`{"value":"x"}`)); code != http.StatusBadRequest {
		t.Errorf("a badly named secret = %d", code)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodDelete, "/api/settings/pages/"+p.page.ID+"/secrets/WEATHER_TOKEN", "", nil); code != http.StatusNoContent {
		t.Errorf("delete secret = %d", code)
	}
}
