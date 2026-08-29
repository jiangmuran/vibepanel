package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Four files state the default port and none of them imports the others.
//
// internal/config.DefaultPort is the one the panel uses. deploy/install.sh
// carries a fallback for when there is no env file yet, and uses it to warn
// that something else is already on the port. deploy/vibepanel.env is the file
// that gets installed, so it is what the panel actually reads afterwards. And
// deploy/docker-compose.yml publishes and binds it.
//
// Drift here is quiet in the worst direction: the installer checks one port,
// the panel binds another, and nothing warns. The panel then fails to bind and
// systemd restarts it every few seconds -- which is the failure this port was
// changed to avoid in the first place, arrived at from the other side.
func TestTheInstallerAgreesAboutThePort(t *testing.T) {
	port := fmt.Sprint(DefaultPort)

	// Sanity on the constant itself: an unprivileged port, and not one of the
	// crowded ones. 8443 is the conventional second HTTPS port and is the
	// reason this moved.
	if DefaultPort < 1024 || DefaultPort > 65535 {
		t.Fatalf("DefaultPort = %d, which needs root or does not exist", DefaultPort)
	}
	if Default().Addr != ":"+port {
		t.Errorf("Default().Addr = %q, want %q", Default().Addr, ":"+port)
	}
	if got := Default().Port(); got != DefaultPort {
		t.Errorf("Default().Port() = %d, want %d", got, DefaultPort)
	}

	for _, f := range []struct {
		path string
		re   *regexp.Regexp
		what string
	}{
		{"install.sh", regexp.MustCompile(`(?m)^PORT=(\d+)`), "the installer's fallback"},
		{"vibepanel.env", regexp.MustCompile(`(?m)^VIBEPANEL_ADDR=:(\d+)`), "the env file that ships"},
		{"docker-compose.yml", regexp.MustCompile(`VIBEPANEL_PORT:-(\d+)`), "the published container port"},
		{"docker-compose.yml", regexp.MustCompile(`VIBEPANEL_ADDR=:(\d+)`), "the address inside the container"},
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", "deploy", f.path))
		if err != nil {
			t.Errorf("%s: %v", f.path, err)
			continue
		}
		m := f.re.FindStringSubmatch(string(b))
		if m == nil {
			t.Errorf("deploy/%s: %s is not there any more; this test cannot see it drift",
				f.path, f.what)
			continue
		}
		if m[1] != port {
			t.Errorf("deploy/%s: %s is %s, config.DefaultPort is %s", f.path, f.what, m[1], port)
		}
	}
}

// And what a reader is told to open.
//
// Documentation that names the old port sends somebody to a page that is not
// there, on the install they just did. The build log is the exception: it
// records what was true when each entry was written, and rewriting that is
// rewriting history.
func TestTheDocsNameThePortThePanelUses(t *testing.T) {
	port := fmt.Sprint(DefaultPort)
	old := regexp.MustCompile(`\b8443\b`)

	for _, f := range []string{
		"docs/install.md", "docs/features.md", "docs/features.zh-CN.md",
		"docs/runbook.md", "README.md", "README.zh-CN.md",
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", f))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if old.MatchString(line) {
				t.Errorf("%s:%d names port 8443; the panel listens on %s\n  %s",
					f, i+1, port, strings.TrimSpace(line))
			}
		}
	}
}
