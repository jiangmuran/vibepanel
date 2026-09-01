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

// The updater and the restart button answer the same question, and this puts
// the process in the case that told them apart.
//
// `INVOCATION_ID` is set by systemd on a unit and inherited by everything that
// unit goes on to spawn, so a panel built and started by hand from a pane of a
// vibepanel session has it. restart.go was fixed twice over exactly that, and
// the parent is the decisive half; the updater kept the old test, so for one
// process handleRestart answered 409 unsupervised while the updater reported
// `restarting: true` and shelled out to `systemctl --user restart vibepanel` --
// which, on a machine that does have a unit installed, restarts a different,
// production panel and drops every browser and terminal on it, for an update
// nobody applied to it.
func TestTheUpdaterRestartsOnlyWhereTheButtonWould(t *testing.T) {
	t.Setenv("INVOCATION_ID", "03636ea867064621b23c72f3d5c96b02")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if who := supervisorName(); who != "" {
		// A test binary's parent is the go tool or a shell, never a service
		// manager -- unless this really is running under one, in which case
		// there is nothing here to disagree about.
		t.Skipf("this process really is supervised by %s", who)
	}
	if cmd, err := restartCommand(); err == nil {
		t.Fatalf("the updater would run %q, while the restart button refuses this process as unsupervised", cmd.String())
	}

	// And the arms that do restart still do. The user unit is the install this
	// project recommends wherever there is no root, so losing it here would
	// leave all of those installing an update and never restarting into it.
	cmd, err := restartCommandFor("systemd")
	if err != nil {
		t.Fatalf("a systemd user unit: %v", err)
	}
	if got := strings.Join(cmd.Args[1:], " "); got != "--user restart vibepanel" {
		t.Errorf("a systemd user unit: %q, want `--user restart vibepanel`", got)
	}
	if _, err := restartCommandFor("launchd"); err == nil {
		t.Error("launchd got a systemctl command")
	}
}
