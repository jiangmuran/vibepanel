package resources

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
)

// Against a real user manager and real cgroupfs, for the reason the tmux
// wrapper is tested against a real tmux: the failures worth catching here are
// the kernel's and systemd's -- a controller that will not enable while a
// process sits in the cgroup, a move refused for want of a common ancestor, a
// scope that is collected -- and a fake reproduces none of them.
//
// Skipped where there is no user manager to ask, which is most CI runners.

func userManager(t *testing.T) {
	t.Helper()
	if !cgroup.Unified() {
		t.Skip("no cgroup v2")
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		t.Skip("no user manager (XDG_RUNTIME_DIR unset)")
	}
	for _, bin := range []string{"systemctl", "systemd-run", "busctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("no %s", bin)
		}
	}
	if err := exec.Command("systemctl", "--user", "show", "-p", "Version").Run(); err != nil {
		t.Skipf("user manager not answering: %v", err)
	}
}

// startUnit runs argv as a transient user service and returns its main pid.
//
// A service, not this test's own child: this process is wherever the test
// runner put it -- often a system unit's cgroup -- and a process can only be
// moved into a user scope from somewhere under the same user manager.
func startUnit(t *testing.T, name string, argv ...string) int {
	t.Helper()
	args := append([]string{"--user", "--unit", name, "--collect", "-p", "KillMode=process", "--"}, argv...)
	if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
		t.Fatalf("systemd-run: %v\n%s", err, out)
	}
	// Kill, then stop: the unit runs with KillMode=process, so a stop alone
	// ends the main process and leaves its children -- the orphan the
	// leftovers test makes on purpose -- running for ten minutes after every
	// run.
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "--user", "kill", "--signal=KILL", name).Run()
		_ = exec.Command("systemctl", "--user", "stop", name).Run()
		_ = exec.Command("systemctl", "--user", "reset-failed", name).Run()
	})
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		out, _ := exec.Command("systemctl", "--user", "show", "-p", "MainPID", "--value", name).Output()
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(out))); pid > 0 {
			return pid
		}
	}
	t.Fatalf("%s never started", name)
	return 0
}

func childOf(t *testing.T, parent int) int {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile("/proc/" + strconv.Itoa(parent) + "/task/" + strconv.Itoa(parent) + "/children")
		if f := strings.Fields(string(b)); len(f) > 0 {
			n, _ := strconv.Atoi(f[0])
			return n
		}
	}
	t.Fatalf("pid %d has no child", parent)
	return 0
}

func TestAdoptBuildSortInARealScope(t *testing.T) {
	userManager(t)
	ctx := context.Background()
	tag := strconv.Itoa(os.Getpid())
	scope := cgroup.ScopeName(cgroup.User, os.Getuid(), "gotest-"+tag)
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "--user", "kill", "--signal=KILL", scope).Run()
		_ = exec.Command("systemctl", "--user", "stop", scope).Run()
	})

	// A stand-in tmux server, and a pane with a child, in a unit of their own
	// the way sessions were in the panel's unit before any of this.
	server := startUnit(t, "vp-gotest-server-"+tag, "sleep", "600")
	pane := startUnit(t, "vp-gotest-pane-"+tag, "sh", "-c", "sleep 601 & wait")
	child := childOf(t, pane)

	a := Adoption{Manager: cgroup.User, Scope: scope, OwnerUID: -1, OwnerGID: -1,
		Props: cgroup.ScopeProps{MemoryMax: 1 << 30, NoSwap: true}}
	if err := a.Adopt(ctx, server, []int{pane, child}); err != nil {
		t.Fatal(err)
	}
	l, err := Find(scope, server)
	if err != nil {
		t.Fatal(err)
	}
	if max, _ := l.Scope.Uint("memory.max"); max != 1<<30 {
		t.Errorf("scope backstop %d", max)
	}

	if err := l.Build(server); err != nil {
		t.Fatal(err)
	}
	// Build twice: every tick does, and the second must be a no-op rather
	// than an EBUSY on controllers that are already enabled.
	if err := l.Build(server); err != nil {
		t.Fatalf("second build: %v", err)
	}
	in := func(pid int) string {
		rel, _ := cgroup.Of(pid)
		return strings.TrimPrefix(rel, l.Scope.Rel())
	}
	if got := in(server); got != "/pool/tmux" {
		t.Errorf("server in %q", got)
	}
	if got := in(pane); got != "/pool/other" {
		t.Errorf("pane before sorting in %q", got)
	}
	if !l.Scope.Enabled()["memory"] || !l.Pool.Enabled()["memory"] {
		t.Error("memory controller not enabled below the scope and the pool")
	}

	moved := l.Sort([]Pane{{TmuxName: "vp_gotest", PID: pane}}, server, LazyProcs())
	if moved != 2 {
		t.Errorf("sorted %d, want the pane and its child", moved)
	}
	for _, pid := range []int{pane, child} {
		if got := in(pid); got != "/pool/s-vp_gotest" {
			t.Errorf("pid %d in %q", pid, got)
		}
	}

	// A process forked by a sorted process is born in the session's leaf.
	leaf, _ := l.Session("vp_gotest")
	if err := leaf.SetUint("memory.max", 64<<20); err != nil {
		t.Fatalf("a session leaf's limit is not writable: %v", err)
	}
	if err := leaf.Freeze(true); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(3 * time.Second); !leaf.Frozen() && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
	}
	if !leaf.Frozen() {
		t.Error("freeze did not take")
	}
	if err := leaf.Freeze(false); err != nil {
		t.Fatal(err)
	}

	// A session that has gone: its leaf goes once it is empty, and not before.
	l.Prune(map[string]bool{})
	if !leaf.Exists() {
		t.Fatal("pruned a leaf with processes in it")
	}
	// Ending the session ends what it left behind: the pane's child is still
	// in the leaf, the way a nohup'ed worker is after tmux ends the pane.
	g := New(Env{})
	g.layout = &l
	g.EndSession("vp_gotest")
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if procs, _ := leaf.Procs(); len(procs) == 0 {
			break
		}
	}
	l.Prune(map[string]bool{})
	if leaf.Exists() {
		t.Error("an empty leaf of a gone session was kept")
	}
}

func TestLeftoversAreEverythingButThePanelsOwnTree(t *testing.T) {
	userManager(t)
	tag := strconv.Itoa(os.Getpid())
	unit := "vp-gotest-left-" + tag
	// "self" is the shell and its sleep is its own work. The subshell's sleep
	// is orphaned the moment the subshell exits, which is what a session left
	// in the unit by an older panel looks like: in the cgroup, and nobody's
	// child.
	self := startUnit(t, unit, "bash", "-c", "(exec -a vp-orphan sleep 603 &); sleep 602 & wait")
	mine := childOf(t, self)
	out, _ := exec.Command("systemctl", "--user", "show", "-p", "ControlGroup", "--value", unit).Output()
	dir := cgroup.At(strings.TrimSpace(string(out)))

	var left []int
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if left = Leftovers(dir, self, LazyProcs()); len(left) > 0 {
			break
		}
	}
	if len(left) != 1 {
		t.Fatalf("leftovers %v, want exactly the orphan", left)
	}
	if left[0] == self || left[0] == mine {
		t.Fatalf("counted the panel's own process %d as a leftover", left[0])
	}
	if b, _ := os.ReadFile("/proc/" + strconv.Itoa(left[0]) + "/cmdline"); !strings.HasPrefix(string(b), "vp-orphan") {
		t.Fatalf("leftover %d is %q", left[0], b)
	}
}
