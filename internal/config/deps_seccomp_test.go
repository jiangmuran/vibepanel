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

// Nothing in either unit may confine something a shell can notice.
//
// The seccomp list above is one instance of a bigger rule, and keeping them
// separate is why the same bug arrived three times. `KillMode=process` puts
// the tmux server inside the unit and leaves it there, so every namespace,
// every inherited bit and the umask reach every agent and every command
// anybody types. The panel's terminals are the product; a terminal that
// behaves differently from a login shell is the product being broken.
//
// Each entry says what a person saw, because the next person to add one of
// these will have a good reason and needs to know the cost.
var inheritedConfinement = map[string]string{
	"NoNewPrivileges": "inherited by every descendant and impossible to drop: " +
		`sudo says "the \"no new privileges\" flag is set" in every session, ` +
		"and su, pkexec, newgrp, ping and every other setuid or file-capability binary fail with it",
	"ProtectControlGroups": "mounts /sys/fs/cgroup read-only in a namespace every session inherits, " +
		"which is where rootless podman and anything else driving cgroupfs by hand writes",
	"UMask": "inherited, so every file every agent creates comes out 600 and every directory 700; " +
		"store.Open sets the database's own mode instead, which also covers `vibepanel serve` by hand",
	"ProtectSystem":       "a read-only /usr and /etc for every agent",
	"ProtectHome":         "the panel's entire job is running agents against the user's home directory",
	"PrivateTmp":          "sessions get a /tmp nothing else on the machine can see, including the user's own shell",
	"PrivateUsers":        "breaks anything that looks up its own uid, and every session inherits the mapping",
	"PrivateDevices":      "no /dev/kvm, no /dev/dri, no serial ports, in every session",
	"ProtectProc":         "hides the user's own processes from the user's own shell",
	"ProcSubset":          "same, by another route: /proc entries missing inside every session",
	"RootDirectory":       "a chroot every session is inside",
	"TemporaryFileSystem": "a mount every session inherits and cannot see out of",
	"ReadOnlyPaths":       "read-only for the agents too, and they are what the paths are for",
	"InaccessiblePaths":   "invisible to the agents too",
	"DynamicUser":         "a different user every start, and none of them owns the user's files",
	"CapabilityBoundingSet": "the bounding set is inherited, so it caps what any setuid binary in " +
		"a session can gain -- the same failure as NoNewPrivileges by a slower route",
	"MemoryHigh": "a sustained throttle on the whole cgroup, which is every agent and the panel " +
		"together: the kernel holds the group at that figure by reclaiming and swapping for as " +
		"long as it takes, so an agent doing real work throttles the console that is meant to " +
		"show it. Measured at 20G peak and 3.1G swap peak, with the proxy in front returning " +
		"intermittent 502s. MemoryMax is the ceiling and stays; a ceiling is not a throttle",
	"CPUQuota": "the same shape on the other axis -- a cap the whole cgroup shares, so the panel " +
		"waits behind whatever an agent is compiling",
	"TasksMax": "shared by every session, so one agent spawning workers stops the next session " +
		"from starting and the panel from forking git",
	"IOReadBandwidthMax":  "shared, so one agent reading a repository starves the rest",
	"IOWriteBandwidthMax": "shared, so one agent writing starves the rest",
}

func TestNothingInEitherUnitConfinesWhatAShellDoes(t *testing.T) {
	assign := regexp.MustCompile(`(?m)^\s*([A-Za-z]+)\s*=`)
	for _, unit := range []string{"vibepanel.service", "vibepanel-system.service"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "deploy", unit))
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			m := assign.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			seen++
			for bad, why := range inheritedConfinement {
				if strings.EqualFold(m[1], bad) {
					t.Errorf("deploy/%s sets %s.\n\nThe tmux server and every agent start inside "+
						"this unit and stay there, so this reaches every session: %s.\n\nIf the panel "+
						"itself needs it, it has to be done by the panel to the panel, not by the "+
						"unit to the cgroup.", unit, m[1], why)
				}
			}
		}
		// The unit is read at all. A path that has moved turns this whole file
		// into a test that passes by finding nothing, which is how the check
		// would come to be trusted while checking nothing.
		if seen < 5 {
			t.Fatalf("deploy/%s parsed as %d directives; this test is no longer reading the unit", unit, seen)
		}
	}
}
