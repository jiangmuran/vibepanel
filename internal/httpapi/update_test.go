package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/selfupdate"
)

// A panel that cannot replace its own binary says so, before downloading, and
// names the command that can.
//
// The reported failure was the other order:
//
//	selfupdate: open /usr/local/bin/.vibepanel-update-2717682195: permission denied
//
// after the archive had been fetched, from a button that had offered to
// install it. A system install owns the binary as root and runs the panel as
// the user, so the answer was knowable before anything was downloaded.
//
// The check is behind a field here for one reason: the real one asks about the
// running executable, and in a test that is the test binary in a writable
// temp directory, so this branch is otherwise unreachable and shipped
// unexercised.
func TestAPanelThatCannotReplaceItsBinarySaysSoFirst(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.installable = func() error {
		return fmt.Errorf("%w: /usr/local/bin", selfupdate.ErrNotWritable)
	}

	// The check tells the page not to offer the button, and carries the
	// sentence it should show instead.
	res, err := ts.Client().Get(ts.URL + "/api/update")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		// Only assert on byHand when GitHub was reachable; an offline runner
		// answers `unreachable` and has nothing to say about installing.
		if out["unreachable"] == nil {
			by, _ := out["byHand"].(string)
			if by == "" {
				t.Error("the check does not say the binary cannot be replaced; the page will offer a button that fails")
			} else if !strings.Contains(by, "service upgrade") {
				t.Errorf("byHand = %q, want it to name the command that works", by)
			}
		}
	}

	// And applying refuses with 409 rather than a 500 from a temp file.
	res2, err := ts.Client().Post(ts.URL+"/api/update", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	b2, _ := io.ReadAll(res2.Body)
	said := string(b2)
	// 502 is "GitHub could not be reached", which is a legitimate answer on an
	// offline machine and not what this test is about.
	if res2.StatusCode == http.StatusBadGateway {
		t.Skip("no route to the release API on this machine")
	}
	if res2.StatusCode != http.StatusConflict {
		t.Fatalf("apply: %d %s, want 409", res2.StatusCode, strings.TrimSpace(said))
	}
	if !strings.Contains(said, "service upgrade") {
		t.Errorf("the refusal does not name the command that works: %s", strings.TrimSpace(said))
	}
	if strings.Contains(said, ".vibepanel-update-") {
		t.Errorf("the refusal is still the raw temp-file error: %s", strings.TrimSpace(said))
	}
}
