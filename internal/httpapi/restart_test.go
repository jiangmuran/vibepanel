package httpapi

import "testing"

// What counts as "something will start this again".
//
// Two rounds of getting this wrong, both found by pressing the button rather
// than by reading the code.
//
// First it read INVOCATION_ID alone. systemd sets that for every unit it
// starts and it is inherited like any other variable, so a panel started by
// hand from a terminal inside a systemd user session saw it and concluded a
// service manager was watching. Pressing restart stopped the panel for good,
// in a browser tab with no way to bring it back -- the exact case the check
// exists to refuse.
//
// Then it tested for pid 1, which is right for a systemd *system* unit and for
// launchd, and wrong for a systemd **user** unit: that one's parent is the
// account's own `systemd --user`, an ordinary process with an ordinary pid.
// The user unit is the install this project recommends wherever there is no
// root, so the fix for the first bug refused to restart the most common
// installation there is.
func TestOnlyASupervisorCounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ppid   int
		parent string
		invoc  string
		xpc    string
		want   string
	}{
		{"a systemd system unit", 1, "systemd", "abc123", "", "systemd"},
		{"a systemd user unit", 1915, "systemd", "abc123", "", "systemd"},
		{"a launchd job", 1, "launchd", "", "io.github.jiangmuran.vibepanel", "launchd"},

		// Inherited variables, ordinary parents.
		{"a terminal in a systemd session", 4242, "bash", "abc123", "", ""},
		{"a test harness", 4242, "node", "abc123", "", ""},
		{"a terminal on a Mac", 4242, "zsh", "", "0", ""},
		{"a plain terminal", 4242, "bash", "", "", ""},

		// Reparented to init with nothing naming a supervisor: a container, or
		// an orphan. Whether anything restarts a container is the runtime's
		// policy and is not visible from in here, and refusing is the safe
		// direction.
		{"pid 1 and nothing else", 1, "", "", "", ""},
		{"a Mac terminal's own XPC name", 1, "launchd", "", "com.apple.Terminal.x", ""},
	} {
		got := supervisorFrom(tc.ppid, tc.parent, tc.invoc, tc.xpc)
		if got != tc.want {
			t.Errorf("%s: supervisorFrom(%d, %q, %q, %q) = %q, want %q",
				tc.name, tc.ppid, tc.parent, tc.invoc, tc.xpc, got, tc.want)
		}
	}
}
