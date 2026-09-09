package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

// TestASuppliedSecretIsRefusedWhereItWouldNotBeUsed.
//
// Two directions, and the second is the one worth having.
//
// A panel that can write its own binary needs nothing typed, so a request that
// carries something is refused rather than quietly having it ignored: a
// credential sent for no reason is still a credential that travelled, and the
// caller has to be told it was pointless so they stop sending it.
//
// And a panel that cannot write its binary, for a reason that is *not*
// permissions -- a full disk, a read-only mount -- must not take one either.
// Nothing typed would help, and spending it on a program that is going to fail
// anyway is the worst of both.
func TestASuppliedSecretIsRefusedWhereItWouldNotBeUsed(t *testing.T) {
	ts, srv := newTestServer(t)

	// Writable: nothing to authorise.
	srv.installable = func() error { return nil }
	code, body := send(t, ts, http.MethodPost, "/api/update", `{"password":"hunter2"}`)
	if code != http.StatusBadRequest {
		t.Errorf("a writable panel took a password: %d %s", code, body)
	}
	if strings.Contains(body, "hunter2") {
		t.Errorf("the refusal echoed what was sent: %s", body)
	}

	// Not writable, but not a permissions problem either.
	srv.installable = func() error { return errors.New("no space left on device") }
	code, body = send(t, ts, http.MethodPost, "/api/update", `{"password":"hunter2"}`)
	if code != http.StatusConflict {
		t.Errorf("a full disk took a password: %d %s", code, body)
	}
	if strings.Contains(body, "hunter2") {
		t.Errorf("the refusal echoed what was sent: %s", body)
	}
}

// TestTheCheckOffersToAskOnlyWhenSomethingCanBeAsked.
//
// `elevate` is what decides whether the page shows a field or only a command,
// and it is two facts and-ed together: the binary is unwritable *because of
// permissions*, and the helper exists on this machine. Either one alone is a
// field that cannot lead anywhere.
func TestTheCheckOffersToAskOnlyWhenSomethingCanBeAsked(t *testing.T) {
	ts, srv := newTestServer(t)

	for _, tc := range []struct {
		what string
		err  error
		want bool
	}{
		{"permissions", fmt.Errorf("%w: /usr/local/bin", selfupdate.ErrNotWritable), elevateAvailable()},
		{"a full disk", errors.New("no space left on device"), false},
	} {
		srv.installable = func() error { return tc.err }
		res, err := ts.Client().Get(ts.URL + "/api/update")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close() //nolint:errcheck // test
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		if out["unreachable"] != nil {
			t.Skip("no network on this runner, and the check says nothing about installing")
		}
		got, _ := out["elevate"].(bool)
		if got != tc.want {
			t.Errorf("%s: elevate = %v, want %v", tc.what, got, tc.want)
		}
	}
}

// TestNothingTypedReachesACommandLine.
//
// The one property that cannot be checked by making a request, so it is read
// out of the source: what somebody types goes to the helper on stdin. On a
// command line it would be visible to every account on the machine through
// `ps`, and in the environment through /proc -- both of which are the whole
// reason the field exists rather than telling people to run it themselves.
func TestNothingTypedReachesACommandLine(t *testing.T) {
	src, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)

	i := strings.Index(code, "func (s *Server) applyElevated")
	if i < 0 {
		t.Fatal("applyElevated is gone; this test is checking nothing")
	}
	body := code[i:]
	if j := strings.Index(body, "\n}\n"); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "Stdin = strings.NewReader(") {
		t.Error("what is typed no longer goes in on stdin")
	}
	// Every exec built here, and none of them may carry it. `-S` is what makes
	// the helper read stdin at all; without it the process waits on a terminal
	// that is not there.
	if !strings.Contains(body, `"-S"`) {
		t.Error("the helper is not being told to read stdin")
	}
	for _, bad := range []string{"password)", "password,", "password +"} {
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, "exec.Command") {
				continue
			}
			if strings.Contains(line, bad) {
				t.Errorf("a command line is being built with it: %s", strings.TrimSpace(line))
			}
		}
	}
}
