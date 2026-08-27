package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
)

// The half of an upgrade nothing else can see.
//
// tmux reads its `-f` file once, at start-server, and the panel never kills its
// server -- that is the premise of the whole project. So a new binary writes a
// new config and the running server goes on using the old one, and both look
// installed. `vibepanel doctor` says so, and nobody runs doctor after a
// `systemctl restart`; the settings page is where a person looks.
//
// The stamp is a tmux server option, so an upgraded binary meeting an old
// server is reproduced exactly by writing a different value into it. Set from
// outside rather than through a method added for the purpose: production API
// that exists only for a test is API somebody will later find and use.
func TestSettingsSaysWhenTheRunningTmuxConfigIsStale(t *testing.T) {
	ts, srv := newTestServer(t)
	socket := srv.Tmux.Socket

	setStamp := func(value string) {
		t.Helper()
		out, err := exec.Command("tmux", "-L", socket, "set-option", "-s", "@vibepanel-conf", value).CombinedOutput()
		if err != nil {
			t.Fatalf("set the stamp to %q: %v\n%s", value, err, out)
		}
	}

	fresh := getSettings(t, ts)
	if fresh.TmuxConfigStale || fresh.TmuxConfigUnknown {
		t.Errorf("a server this binary just started is not reported as current: %+v", fresh)
	}

	setStamp("0000000000000000")
	stale := getSettings(t, ts)
	if !stale.TmuxConfigStale {
		t.Error("a server running a different config is reported as current; an upgrade " +
			"leaves the new file on disk and the old settings in memory, and this is the " +
			"only place a person would see it")
	}

	// Empty is a third answer, not the same as current: a server with no stamp
	// predates the config change that added the stamp.
	setStamp("")
	unknown := getSettings(t, ts)
	if !unknown.TmuxConfigUnknown {
		t.Error("a server with no stamp is reported as answering the question")
	}
	if unknown.TmuxConfigStale {
		t.Error("a server with no stamp is reported as stale, which is a claim nobody can make")
	}
}

func getSettings(t *testing.T, ts *httptest.Server) settingsResponse {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + "/api/settings")
	if err != nil {
		t.Fatalf("GET /api/settings: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/settings: %s", res.Status)
	}
	var out settingsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}
