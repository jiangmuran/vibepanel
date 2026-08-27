package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The management command is a mapping and nothing else, which is why plan() is
// pure: the interesting failures are "it ran the user command against a system
// unit" and "it asked for a password to read your own log", and both are
// visible in an argv without a service manager, a Mac or root anywhere near
// the test.

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectServicePicksTheKindThatIsInstalled(t *testing.T) {
	home := t.TempDir()
	dest := t.TempDir()

	if got := detectService("linux", home, dest, "1000").Kind; got != svcNone {
		t.Errorf("with nothing installed, kind is %q, want none", got)
	}
	if got := detectService("darwin", home, dest, "501").Kind; got != svcNone {
		t.Errorf("with no plist, kind is %q, want none", got)
	}

	writeFile(t, filepath.Join(home, ".config", "systemd", "user", "vibepanel.service"))
	if got := detectService("linux", home, dest, "1000").Kind; got != svcUser {
		t.Errorf("with a user unit, kind is %q, want user", got)
	}

	// Both present is the state the installer refuses to create and the
	// runbook has a section for. The system one wins, because that is the
	// order the installer resolves an ambiguous re-run in — the two answers
	// disagreeing is how somebody stops one panel and watches the other keep
	// serving.
	writeFile(t, filepath.Join(dest, "etc", "systemd", "system", "vibepanel.service"))
	if got := detectService("linux", home, dest, "1000").Kind; got != svcSystem {
		t.Errorf("with both units, kind is %q, want system", got)
	}

	mac := t.TempDir()
	writeFile(t, filepath.Join(mac, "Library", "LaunchAgents", macLabel+".plist"))
	got := detectService("darwin", mac, "", "501")
	if got.Kind != svcAgent {
		t.Errorf("with a plist, kind is %q, want agent", got.Kind)
	}
	if got.Log != filepath.Join(mac, "Library", "Logs", "vibepanel.log") {
		t.Errorf("the LaunchAgent log is %q; launchd has no journal, and the plist "+
			"writes to ~/Library/Logs/vibepanel.log", got.Log)
	}
}

func TestPlanIsTheRightCommandForEachKind(t *testing.T) {
	user := svcTarget{Platform: "linux", Kind: svcUser, Unit: "/h/.config/systemd/user/vibepanel.service",
		Bin: "/h/.local/bin/vibepanel", Root: []string{"sudo"}, UID: "1000"}
	system := svcTarget{Platform: "linux", Kind: svcSystem, Unit: "/etc/systemd/system/vibepanel.service",
		Bin: "/h/.local/bin/vibepanel", Root: []string{"sudo"}, UID: "1000"}
	agent := svcTarget{Platform: "darwin", Kind: svcAgent, Unit: "/h/Library/LaunchAgents/" + macLabel + ".plist",
		Log: "/h/Library/Logs/vibepanel.log", Bin: "/h/.local/bin/vibepanel", UID: "501"}

	cases := []struct {
		name string
		t    svcTarget
		sub  string
		want string
	}{
		{"user status", user, "status", "systemctl --user status vibepanel"},
		// enable --now rather than start: "run it" and "and after a reboot"
		// are the same request from anyone typing this.
		{"user start", user, "start", "systemctl --user enable --now vibepanel"},
		{"user stop", user, "stop", "systemctl --user stop vibepanel"},
		{"user restart", user, "restart", "systemctl --user restart vibepanel"},
		{"user logs", user, "logs", "journalctl --user -u vibepanel -n 40"},

		{"system status", system, "status", "sudo systemctl status vibepanel"},
		{"system start", system, "start", "sudo systemctl enable --now vibepanel"},
		{"system logs", system, "logs", "sudo journalctl -u vibepanel -n 40"},

		{"agent status", agent, "status", "launchctl print gui/501/" + macLabel},
		{"agent start", agent, "start", "launchctl bootstrap gui/501 " + agent.Unit},
		{"agent stop", agent, "stop", "launchctl bootout gui/501/" + macLabel},
		// kickstart -k, not bootout+bootstrap: the pair has a window in which
		// the job does not exist at all.
		{"agent restart", agent, "restart", "launchctl kickstart -k gui/501/" + macLabel},
		{"agent logs", agent, "logs", "tail -n 40 " + agent.Log},
	}
	for _, c := range cases {
		steps, err := plan(c.t, c.sub, svcOpts{})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(steps) != 1 || steps[0].String() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, steps, c.want)
		}
	}
}

func TestOnlyTheSystemUnitAsksForRoot(t *testing.T) {
	// A user unit and a LaunchAgent live entirely inside $HOME. Asking for a
	// password to read your own log is how people stop reading it, and a sudo
	// in a non-interactive context is how a script hangs.
	for _, k := range []svcKind{svcUser, svcAgent} {
		tgt := svcTarget{Kind: k, Platform: "linux", Root: []string{"sudo"}, UID: "1000",
			Unit: "/h/u", Log: "/h/l", Bin: "/h/b"}
		if k == svcAgent {
			tgt.Platform = "darwin"
		}
		for _, sub := range []string{"status", "start", "stop", "restart", "logs", "uninstall"} {
			steps, err := plan(tgt, sub, svcOpts{})
			if err != nil {
				t.Fatalf("%s %s: %v", k, sub, err)
			}
			for _, s := range steps {
				if len(s.Argv) > 0 && s.Argv[0] == "sudo" {
					t.Errorf("%s %s runs sudo: %s", k, sub, s)
				}
			}
		}
	}
	sys := svcTarget{Kind: svcSystem, Platform: "linux", Root: []string{"sudo"}, UID: "1000",
		Unit: "/etc/systemd/system/vibepanel.service", Bin: "/h/b"}
	steps, err := plan(sys, "status", svcOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].Argv[0] != "sudo" {
		t.Errorf("the system unit needs root and this does not ask for it: %s", steps[0])
	}
	// ...and does not, when we already are root. detectService is what decides
	// that, so this is the pair that pins it.
	if root := detectService("linux", t.TempDir(), t.TempDir(), "0").Root; len(root) != 0 {
		t.Errorf("running as root, the plan still prefixes %v", root)
	}
}

func TestUninstallRemovesTheServiceAndNothingElse(t *testing.T) {
	// The one thing this must never do is take the database with it. Sessions
	// belong to tmux and survive regardless; the data directory holds the
	// password hashes, the passkeys and every project, and "uninstall" is
	// typed by people trying a different service kind.
	for _, tgt := range []svcTarget{
		{Kind: svcUser, Platform: "linux", Unit: "/h/.config/systemd/user/vibepanel.service", Bin: "/h/.local/bin/vibepanel", UID: "1000"},
		{Kind: svcSystem, Platform: "linux", Unit: "/etc/systemd/system/vibepanel.service", Bin: "/h/.local/bin/vibepanel", Root: []string{"sudo"}, UID: "1000"},
		{Kind: svcAgent, Platform: "darwin", Unit: "/h/Library/LaunchAgents/" + macLabel + ".plist", Bin: "/h/.local/bin/vibepanel", UID: "501"},
	} {
		steps, err := plan(tgt, "uninstall", svcOpts{})
		if err != nil {
			t.Fatal(err)
		}
		var removed []string
		stopped := false
		for _, s := range steps {
			if s.Remove != "" {
				removed = append(removed, s.Remove)
			}
			line := s.String()
			if strings.Contains(line, "disable --now") || strings.Contains(line, "bootout") {
				stopped = true
				if !s.Ignore {
					t.Errorf("%s: stopping a service that is not running must not abort the "+
						"uninstall, or a half-installed machine keeps its unit file: %s", tgt.Kind, s)
				}
			}
		}
		if !stopped {
			t.Errorf("%s: uninstall removes the unit file without stopping the service first", tgt.Kind)
		}
		want := map[string]bool{tgt.Unit: true, tgt.Bin: true}
		for _, r := range removed {
			if !want[r] {
				t.Errorf("%s: uninstall removes %q, which is neither the unit nor the binary", tgt.Kind, r)
			}
			delete(want, r)
		}
		for r := range want {
			t.Errorf("%s: uninstall leaves %q behind", tgt.Kind, r)
		}
	}
}

func TestUpgradeWorksWithNoServiceInstalled(t *testing.T) {
	// `upgrade` is the one subcommand that has to work before anything is
	// installed — it is what `vibepanel service` offers somebody who ran the
	// binary from a tarball and never installed a unit.
	none := detectService("linux", t.TempDir(), t.TempDir(), "1000")
	steps, err := plan(none, "upgrade", svcOpts{})
	if err != nil {
		t.Fatalf("upgrade with nothing installed: %v", err)
	}
	if len(steps) != 1 || !strings.Contains(steps[0].String(), installURL) {
		t.Errorf("upgrade does not run the published one-liner: %v", steps)
	}
	// Everything else must say so rather than produce a command that fails.
	for _, sub := range []string{"status", "start", "logs", "uninstall"} {
		if _, err := plan(none, sub, svcOpts{}); err == nil {
			t.Errorf("%s with nothing installed produced a command instead of an explanation", sub)
		}
	}
}

func TestEveryDocumentedServiceCommandIsHandled(t *testing.T) {
	// The same shape as TestEveryDocumentedCommandExists, and for the same
	// reason: the help text and the dispatch were separate lists once.
	var documented []string
	for _, line := range strings.Split(serviceCommands, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			documented = append(documented, f[0])
		}
	}
	if len(documented) == 0 {
		t.Fatal("serviceCommands parsed to nothing; this test compares nothing")
	}
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "systemd", "user", "vibepanel.service"))
	tgt := detectService("linux", home, t.TempDir(), "1000")
	for _, sub := range documented {
		if sub == "token" {
			// Read out of the log rather than planned, which is why it has its
			// own test below.
			continue
		}
		if _, err := plan(tgt, sub, svcOpts{}); err != nil {
			t.Errorf("`vibepanel service %s` is offered in --help and nothing plans it: %v", sub, err)
		}
	}
}

func TestScrapeTokenReadsTheLastOne(t *testing.T) {
	// journalctl shape: every line carries a timestamp, the host and the unit,
	// so the token is the last field and not the line.
	log := strings.Join([]string{
		`Aug 27 09:00:01 box vibepanel[11]: vibepanel v1.0.0`,
		`Aug 27 09:00:01 box vibepanel[11]:   No account yet. Open http://box:8443 and use this one-time setup token:`,
		`Aug 27 09:00:01 box vibepanel[11]: `,
		`Aug 27 09:00:01 box vibepanel[11]:       AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA`,
		`Aug 27 09:00:01 box vibepanel[11]: `,
		`Aug 27 10:00:01 box vibepanel[99]:   No account yet. Open http://box:8443 and use this one-time setup token:`,
		`Aug 27 10:00:01 box vibepanel[99]: `,
		`Aug 27 10:00:01 box vibepanel[99]:       BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB`,
	}, "\n")
	// A panel restarted before anyone claimed it has printed several, and only
	// the newest is live. Taking the first would hand somebody a token the
	// server has already forgotten, which fails as "bad setup token" and looks
	// like the wizard is broken.
	if got := scrapeToken(log); got != "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" {
		t.Errorf("scrapeToken = %q, want the most recent token", got)
	}
	// The plain-file shape a LaunchAgent writes: no prefix at all.
	plain := "  No account yet. Open http://mac:8443 and use this one-time setup token:\n\n      CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC\n"
	if got := scrapeToken(plain); got != "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC" {
		t.Errorf("scrapeToken on a LaunchAgent log = %q", got)
	}
	if got := scrapeToken("nothing here\nand nothing there\n"); got != "" {
		t.Errorf("scrapeToken invented %q out of a log with no token in it", got)
	}
	// A claimed panel prints the banner and never the token line. Returning
	// the URL or the version as if it were a token is worse than saying there
	// is none: it sends somebody to paste a wrong string into the wizard.
	claimed := "vibepanel v1.0.0\n  url          http://box:8443\n  data dir     /home/me/.local/share/vibepanel\n"
	if got := scrapeToken(claimed); got != "" {
		t.Errorf("scrapeToken on a claimed panel returned %q", got)
	}
}
