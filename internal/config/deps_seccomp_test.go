package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Neither unit may install a seccomp filter.
//
// `KillMode=process` means the tmux server outlives the panel process, and it
// also means the tmux server *starts inside the unit*: same cgroup, same
// seccomp filter. So does every agent in every session, and every command
// anybody types into one -- so a filter written to confine a Go web server is
// applied to whatever the person at the keyboard runs.
//
// What that cost was `tar -xzf` of any archive with a subdirectory, inside any
// session the panel had created:
//
//	tar: deploy: Cannot mkdir: Function not implemented
//
// ENOSYS rather than EPERM, because systemd rejects syscalls the libseccomp it
// was built against does not know -- Linux 7.0 against libseccomp 2.6 on the
// machine this was found on. Three of the five directives reproduced it by
// bisection and the other two install filters of their own, which is the same
// hazard waiting for a tool one syscall newer.
//
// A list rather than a general test, because "does this install a filter"
// cannot be answered by reading a file. Each name here was measured with
// `systemd-run --user -p <name>=yes grep Seccomp_filters /proc/self/status`.
var seccompDirectives = []string{
	"ProtectClock",
	"ProtectKernelTunables",
	"ProtectKernelModules",
	"ProtectKernelLogs",
	"RestrictSUIDSGID",
	"RestrictRealtime",
	"RestrictNamespaces",
	"RestrictAddressFamilies",
	"MemoryDenyWriteExecute",
	"LockPersonality",
	"PrivateDevices",
	"SystemCallFilter",
	"SystemCallArchitectures",
}

// The same check, written so the error message is readable. The version above
// shadows `t`; this is the one that runs.
func TestNoSeccompHardeningInEitherUnit(t *testing.T) {
	assign := regexp.MustCompile(`(?m)^\s*([A-Za-z]+)\s*=`)
	for _, unit := range []string{"vibepanel.service", "vibepanel-system.service"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "deploy", unit))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			m := assign.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			for _, bad := range seccompDirectives {
				if strings.EqualFold(m[1], bad) {
					t.Errorf("deploy/%s sets %s, which installs a seccomp filter. "+
						"The tmux server and every agent start inside this unit, so the filter "+
						"is applied to whatever anybody runs -- it broke `tar -xzf` of any "+
						"archive with a subdirectory in it.", unit, m[1])
				}
			}
		}
	}
}
