package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These assert properties of the module rather than of this package, which is
// not where they naturally belong — but they need to live somewhere that
// `go test ./...` reaches, and config is the package that already owns the
// question of how this binary is built and run. Move them if a better home
// appears; do not delete them because the placement is untidy.
//
// The distribution story is one constraint: CGO_ENABLED=0 has to keep working.
//
// It is what makes "download one static binary and run it" true, and it is the
// reason the SQLite driver is modernc's pure-Go one rather than mattn's. That
// choice is written down in AGENTS.md and enforced nowhere: adding a dependency
// that needs cgo compiles perfectly well on a machine with a C toolchain and
// only fails when someone cross-compiles a release — or, worse, produces a
// binary that will not start on the machine it was copied to.
//
// A build tag cannot express this and `go vet` will not either, so it is a
// list. The list is short because the document is short: these are the packages
// it names, plus the router alternatives it rules out.
func TestForbiddenDependencies(t *testing.T) {
	mod := readGoMod(t)

	for _, forbidden := range []struct {
		path, why string
	}{
		{"github.com/mattn/go-sqlite3", "needs cgo, which breaks the static binary"},
		{"github.com/gin-gonic/gin", "the project uses chi; one router is a decision, two is drift"},
		{"github.com/labstack/echo", "the project uses chi"},
		{"github.com/gofiber/fiber", "the project uses chi"},
	} {
		if strings.Contains(mod, forbidden.path) {
			t.Errorf("go.mod requires %s: %s", forbidden.path, forbidden.why)
		}
	}
}

// The other half: the things that must still be there. A dependency silently
// swapped out is the same problem from the other direction.
func TestExpectedDependencies(t *testing.T) {
	mod := readGoMod(t)
	for _, want := range []struct {
		path, why string
	}{
		{"modernc.org/sqlite", "the pure-Go driver; the cgo one cannot be cross-compiled"},
		{"github.com/go-chi/chi/v5", "the router the whole HTTP layer is written against"},
		{"github.com/creack/pty", "attaching to tmux needs a PTY"},
		{"github.com/coder/websocket", "the terminal transport"},
	} {
		if !strings.Contains(mod, want.path) {
			t.Errorf("go.mod no longer requires %s: %s", want.path, want.why)
		}
	}
}

// The Go directive has to stay at or above what the code uses. t.Chdir in the
// webui tests needs 1.24; the rest of the tree assumes a recent toolchain.
func TestGoDirectiveIsRecentEnough(t *testing.T) {
	mod := readGoMod(t)
	m := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindStringSubmatch(mod)
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	if m[1] != "1" {
		t.Fatalf("unexpected major version %q", m[1])
	}
	minor := 0
	for _, c := range m[2] {
		minor = minor*10 + int(c-'0')
	}
	if minor < 24 {
		t.Errorf("go %s.%s is below 1.24, which t.Chdir and the tests around it need",
			m[1], m[2])
	}
}

func readGoMod(t *testing.T) string {
	t.Helper()
	const path = "../../go.mod"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s, so the dependency rules were not checked: %v", path, err)
	}
	return string(b)
}

// The shipped unit must not let systemd kill the sessions.
//
// The panel's central promise is that restarting it leaves every agent
// running, and the code keeps its side of that: it never owns a PTY that a
// session's process is a child of. The unit file undid it. tmux's server is
// started by the panel and, although it daemonises and re-parents, cgroup
// membership does not change on re-parenting — so it sits in the panel's unit,
// and systemd's default KillMode=control-group SIGTERMs the whole cgroup on
// stop.
//
// Measured on a throwaway unit and socket with two sessions, `systemctl --user
// stop`: default 2 before / 0 after, KillMode=process 2 before / 2 after,
// KillMode=mixed 2 before / 0 after. `mixed` is the trap — it reads like the
// careful middle option and kills them anyway, because the SIGKILL phase still
// goes to the whole cgroup once the main process is gone.
//
// A comment cannot keep this line in the file. Anyone tidying the unit, or
// hardening it, would delete a bare `KillMode=process` without knowing it is
// the load-bearing one.
func TestUnitLeavesTheSessionsAlone(t *testing.T) {
	const path = "../../deploy/vibepanel.service"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var mode string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if v, ok := strings.CutPrefix(line, "KillMode="); ok {
			mode = strings.TrimSpace(v)
		}
	}
	switch mode {
	case "process":
		// The only setting measured to leave the tmux server running.
	case "":
		t.Fatalf("%s sets no KillMode, so it defaults to control-group and "+
			"`systemctl restart` kills every running session", path)
	default:
		t.Fatalf("%s sets KillMode=%s; only `process` leaves the tmux server "+
			"running (mixed was measured to kill the sessions too)", path, mode)
	}
}

// The user unit has to actually start, which nothing checked until it did not.
//
// `deploy/vibepanel.service` shipped with `ProtectClock=yes` and
// `ProtectKernelModules=yes` and could never start on any machine:
//
//	vibepanel.service: Failed to drop capabilities: Operation not permitted
//	Failed at step CAPABILITIES spawning .../vibepanel
//	status=218/CAPABILITIES
//
// Both shrink CapabilityBoundingSet, and shrinking it needs CAP_SETPCAP, which
// a per-user systemd does not have. Everything around it was green:
// `systemd-analyze verify` accepts the file, install-check drives a *stub*
// systemctl on purpose -- it must not touch the real user manager, or a check
// run would stop the panel of whoever is running it -- and both READMEs
// documented the install. Nothing anywhere executed a unit.
//
// So this does, with a name that cannot collide and `/bin/true` in place of the
// panel: what is under test is the `[Service]` block, not the program. The
// directives are read out of the shipped file rather than restated here, or
// this passes while the real unit rots.
func TestTheUserUnitCanActuallyStart(t *testing.T) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("no systemctl")
	}
	// A user manager, not merely the binary. Container and CI images often have
	// systemd installed and no per-user instance to talk to.
	out, err := exec.Command("systemctl", "--user", "is-system-running").CombinedOutput()
	state := strings.TrimSpace(string(out))
	if err != nil && state != "degraded" && state != "running" {
		t.Skipf("no user manager to talk to: %q", state)
	}

	body, err := os.ReadFile("../../deploy/vibepanel.service")
	if err != nil {
		t.Fatalf("read the unit: %v", err)
	}

	// Everything in [Service] except what names the panel: ExecStart is
	// replaced, and the restart and stop directives would make a oneshot look
	// like a failure.
	var keep []string
	inService := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inService = line == "[Service]"
			continue
		}
		if !inService || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "ExecStart="),
			strings.HasPrefix(line, "Type="),
			strings.HasPrefix(line, "Restart="),
			strings.HasPrefix(line, "RestartSec="),
			strings.HasPrefix(line, "EnvironmentFile="):
			continue
		}
		keep = append(keep, line)
	}
	if len(keep) < 5 {
		t.Fatalf("only %d [Service] directives were read out of the unit; the "+
			"parse is broken and this test would pass by checking nothing", len(keep))
	}

	const name = "vibepanel-unitcheck-test.service"
	dir := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Skipf("cannot write a unit here: %v", err)
	}
	path := filepath.Join(dir, name)
	unit := "[Unit]\nDescription=vibepanel unit check (test)\n\n[Service]\nType=oneshot\nExecStart=/bin/true\n" +
		strings.Join(keep, "\n") + "\n"
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		t.Skipf("cannot write a unit here: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "--user", "stop", name).Run()
		_ = os.Remove(path)
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	})
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		t.Skipf("daemon-reload: %v", err)
	}

	if out, err := exec.Command("systemctl", "--user", "start", name).CombinedOutput(); err != nil {
		shown, _ := exec.Command("systemctl", "--user", "show", name,
			"-p", "Result", "-p", "ExecMainStatus").CombinedOutput()
		t.Fatalf("the shipped [Service] block does not start under a user manager.\n"+
			"start: %v %s\n%s\ndirectives:\n  %s",
			err, strings.TrimSpace(string(out)), strings.TrimSpace(string(shown)),
			strings.Join(keep, "\n  "))
	}
}
