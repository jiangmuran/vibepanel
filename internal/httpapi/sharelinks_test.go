package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/secret"
)

// A link's address can be shown again, and a copy of the database alone still
// opens nothing: the sealed token needs the key file beside it.
func TestALinkAddressCanBeCopiedAgainAndIsSealedAtRest(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"wall"}`)

	code, body, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+link.ID+"/url", "", nil)
	if code != http.StatusOK {
		t.Fatalf("url = %d: %s", code, body)
	}
	var got struct{ URL, Token string }
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != link.Token || !strings.HasSuffix(got.URL, "/share/"+link.Token+"/") {
		t.Errorf("url = %+v, want the token the link was made with", got)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodGet, "/api/share/"+got.Token+"/v1/snapshot", "", nil); code != http.StatusOK {
		t.Errorf("the address shown again does not work: %d", code)
	}

	var enc []byte
	if err := srv.DB.SQL().QueryRowContext(context.Background(),
		`SELECT token_enc FROM share_links WHERE id = ?`, link.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || bytes.Contains(enc, []byte(link.Token)) {
		t.Fatalf("token_enc = %q; it must hold the token sealed, never in the clear", enc)
	}
	info, err := os.Stat(filepath.Join(srv.Cfg.DataDir, secret.KeyFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the secrets key is %v, %v; want a 0600 file in the data directory", info, err)
	}

	// Sealed for one row, it does not open for another.
	other := newShare(t, ts, `{"name":"other"}`)
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET token_enc = ? WHERE id = ?`, enc, other.ID); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+other.ID+"/url", "", nil); code != http.StatusConflict ||
		strings.Contains(string(body), link.Token) {
		t.Errorf("a sealed token moved to another row opened there: %d %s", code, body)
	}

	// Nothing the list sends is the token.
	_, list, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares", "", nil)
	if strings.Contains(string(list), link.Token) {
		t.Error("the list carries a token")
	}
}

// A link made before tokens were sealed cannot be shown again, and says what to
// do; rotating gives any link a new address and ends the old one.
func TestALinkWithNoSealedTokenIsRotatedNotRevealed(t *testing.T) {
	ts, srv := newTestServer(t)
	link := newShare(t, ts, `{"name":"old"}`)
	if _, err := srv.DB.SQL().Exec(`UPDATE share_links SET token_enc = x'' WHERE id = ?`, link.ID); err != nil {
		t.Fatal(err)
	}
	links := getJSON[[]map[string]any](t, ts, "/api/settings/shares")
	for _, l := range links {
		if l["id"] == link.ID && l["copyable"] != false {
			t.Errorf("an unsealed link is listed as copyable: %v", l)
		}
	}
	code, body, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+link.ID+"/url", "", nil)
	if code != http.StatusConflict || !strings.Contains(string(body), `"rotatable":true`) {
		t.Fatalf("url of an old link = %d %s", code, body)
	}

	code, body, _ = sendRaw(t, ts, http.MethodPost, "/api/settings/shares/"+link.ID+"/rotate", "application/json", nil)
	if code != http.StatusOK {
		t.Fatalf("rotate = %d %s", code, body)
	}
	var fresh struct{ Token string }
	_ = json.Unmarshal(body, &fresh)
	if fresh.Token == "" || fresh.Token == link.Token {
		t.Fatalf("rotate returned %q", fresh.Token)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodGet, "/api/share/"+link.Token+"/v1/snapshot", "", nil); code != http.StatusUnauthorized {
		t.Errorf("the old address still works after rotating: %d", code)
	}
	if code, _, _ := sendRaw(t, ts, http.MethodGet, "/api/share/"+fresh.Token+"/v1/snapshot", "", nil); code != http.StatusOK {
		t.Errorf("the new address does not work: %d", code)
	}
	if code, body, _ := sendRaw(t, ts, http.MethodGet, "/api/settings/shares/"+link.ID+"/url", "", nil); code != http.StatusOK ||
		!strings.Contains(string(body), fresh.Token) {
		t.Errorf("a rotated link is not copyable: %d %s", code, body)
	}
	if !auditHas(t, srv, "share.rotated", "old") {
		t.Error("rotating left no audit row")
	}
}

// Interactive is the owner's switch, off by default, audited, and fixed by a
// lock like the rest of what a link draws; a View copy never has it.
func TestInteractiveIsAnAuditedOwnerSwitch(t *testing.T) {
	ts, srv := newTestServer(t)
	plain := newShare(t, ts, `{"name":"plain"}`)
	on := newShare(t, ts, `{"name":"kiosk","interactive":true}`)
	byID := map[string]map[string]any{}
	for _, l := range getJSON[[]map[string]any](t, ts, "/api/settings/shares") {
		byID[l["id"].(string)] = l
	}
	if byID[plain.ID]["interactive"] != false || byID[on.ID]["interactive"] != true {
		t.Fatalf("interactive = %v / %v", byID[plain.ID]["interactive"], byID[on.ID]["interactive"])
	}
	if !auditHas(t, srv, "share.interactive_changed", "kiosk on") {
		t.Error("making an interactive link left no audit row")
	}

	patch := func(id, body string) int {
		code, _, _ := sendRaw(t, ts, http.MethodPatch, "/api/settings/shares/"+id, "application/json", []byte(body))
		return code
	}
	if code := patch(plain.ID, `{"name":"plain","remark":"","locked":false,"interactive":true}`); code != http.StatusNoContent {
		t.Fatalf("turning interactive on = %d", code)
	}
	if l, _ := srv.DB.ShareLinkByID(context.Background(), plain.ID); !l.Interactive {
		t.Error("PATCH interactive did not take")
	}
	if code := patch(plain.ID, `{"name":"plain","remark":"","locked":true}`); code != http.StatusNoContent {
		t.Fatal("lock")
	}
	if code := patch(plain.ID, `{"interactive":false}`); code != http.StatusConflict {
		t.Errorf("changing interactive on a locked link = %d, want 409", code)
	}
	if l, _ := srv.DB.ShareLinkByID(context.Background(), plain.ID); !l.Interactive {
		t.Error("a locked link's interactive changed")
	}

	code, body, _ := sendRaw(t, ts, http.MethodPost, "/api/settings/shares/"+on.ID+"/view", "application/json", nil)
	if code != http.StatusCreated {
		t.Fatalf("view = %d %s", code, body)
	}
	var peek struct{ Token string }
	_ = json.Unmarshal(body, &peek)
	var interactive bool
	if err := srv.DB.SQL().QueryRow(`SELECT interactive FROM share_links WHERE purpose = 'peek'`).Scan(&interactive); err != nil {
		t.Fatal(err)
	}
	if interactive {
		t.Error("a View copy of an interactive link is interactive")
	}
}

func TestVisitorWritesIsAPanelWideAuditedSwitch(t *testing.T) {
	ts, srv := newTestServer(t)
	got := getJSON[map[string]bool](t, ts, "/api/settings/sharing")
	if !got["visitorWrites"] {
		t.Errorf("visitor writes default = %v, want on", got)
	}
	code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json", []byte(`{"visitorWrites":false}`))
	if code != http.StatusOK || srv.visitorWritesAllowed(context.Background()) {
		t.Errorf("turning visitor writes off = %d, allowed %v", code, srv.visitorWritesAllowed(context.Background()))
	}
	if !auditHas(t, srv, "sharing.visitor_writes", "off") {
		t.Error("the switch left no audit row")
	}
	if code, _, _ := sendRaw(t, ts, http.MethodPut, "/api/settings/sharing", "application/json", []byte(`{}`)); code != http.StatusBadRequest {
		t.Errorf("an empty body = %d, want 400", code)
	}
}
