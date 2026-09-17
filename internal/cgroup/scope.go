package cgroup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Where the sessions live, and why it is a scope.
//
// The obvious design is a delegated service: Delegate=yes on the panel's own
// unit, the panel in one child cgroup and the sessions in another. It was
// built, and measured on systemd 259 it cannot be restarted. systemd spawns a
// service's main process into the unit's own cgroup and only then moves it
// into DelegateSubgroup (src/core/execute.c: "We cannot spawn the main service
// process into the subcgroup"), and cgroup v2 refuses to put a process into a
// cgroup whose subtree has controllers enabled while a child is populated.
// Every agent is a populated child, so `systemctl restart vibepanel` fails
// with "Failed to spawn executor: Device or resource busy" -- a panel that
// does not come back, which is the one outcome this whole feature exists to
// prevent.
//
// A transient scope has no main process and systemd never spawns into it. It
// is created once with a process already in it, it stays for as long as any
// process does, and it is collected when the last one exits. The panel's own
// unit is then an ordinary one, restartable as before, and the sessions are
// not inside it at all.

// Manager is which systemd instance a scope belongs to.
type Manager string

const (
	System Manager = "system"
	User   Manager = "user"
)

// ServiceManager says which systemd manager runs the service a cgroup path
// belongs to, and whether it is a service at all.
//
// From the path rather than from the environment: a panel started by hand in a
// terminal inherits INVOCATION_ID and XDG_RUNTIME_DIR from whatever started
// that terminal, and would conclude it is a service. A panel run from inside
// one of its own sessions is in that session's cgroup, which is a scope and
// not a service, and must not start moving processes around.
func ServiceManager(rel string) (Manager, bool) {
	if !strings.HasSuffix(rel, ".service") {
		return "", false
	}
	switch {
	case userManagerPath.MatchString(rel):
		return User, true
	case strings.HasPrefix(rel, "/system.slice/"):
		return System, true
	}
	return "", false
}

var userManagerPath = regexp.MustCompile(`^/user\.slice/user-\d+\.slice/user@\d+\.service/`)

// ScopeName is the unit that holds one panel's sessions.
//
// Derived from the tmux socket, because the socket is what one panel owns: the
// browser checks start throwaway panels on their own sockets, and two panels
// sharing a scope would organise each other's processes. On the system manager
// the account's uid is in it too, because every account's system unit shares
// that one namespace, and two accounts with the default socket would otherwise
// have one scope -- the second root helper handing the first account's
// sessions to the second.
//
// A socket name that had to be changed to be a unit name gets a short hash of
// the original, so "vp.a" and "vp_a" are two scopes and not one.
func ScopeName(m Manager, uid int, socket string) string {
	name := "vibepanel-sessions"
	if m == System {
		name += "-" + strconv.Itoa(uid)
	}
	if socket == "vibepanel" {
		return name + ".scope"
	}
	var b strings.Builder
	for _, r := range socket {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	clean := b.String()
	if len(clean) > 64 {
		clean = clean[:64]
	}
	if clean != socket {
		sum := sha256.Sum256([]byte(socket))
		clean += "-" + hex.EncodeToString(sum[:4])
	}
	return name + "-" + clean + ".scope"
}

// ScopeInfo is what systemd says about a scope.
type ScopeInfo struct {
	Active bool
	// Cgroup is relative to the mount, as /proc/<pid>/cgroup spells it.
	Cgroup string
}

func systemctlArgs(m Manager, args ...string) []string {
	if m == User {
		return append([]string{"--user"}, args...)
	}
	return args
}

// Scope asks systemd for a scope's state. An unknown scope is inactive, not an
// error: that is how one that was never created, or has been collected, looks.
func Scope(ctx context.Context, m Manager, name string) (ScopeInfo, error) {
	out, err := run(ctx, "systemctl", systemctlArgs(m, "show", "-p", "ActiveState", "-p", "ControlGroup", name)...)
	if err != nil {
		return ScopeInfo{}, err
	}
	var info ScopeInfo
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "ActiveState":
			info.Active = v == "active"
		case "ControlGroup":
			info.Cgroup = v
		}
	}
	if info.Cgroup != "" && !plausibleScopeCgroup(m, name, info.Cgroup) {
		return ScopeInfo{}, fmt.Errorf("cgroup: systemd put %s at %q, which is not where a scope goes", name, info.Cgroup)
	}
	return info, nil
}

// plausibleScopeCgroup checks where systemd says a scope is before anything is
// written there. The answer is a path that root goes on to chown, and a
// "systemctl" that is not systemd -- or a systemd reached by a way nobody
// expected -- answering "/../../home/somebody" turned that chown into one of a
// home directory.
func plausibleScopeCgroup(m Manager, name, rel string) bool {
	if path.Clean(rel) != rel || !strings.HasPrefix(rel, "/") || path.Base(rel) != name {
		return false
	}
	if m == System {
		return strings.HasPrefix(rel, "/system.slice/")
	}
	return userManagerPath.MatchString(rel)
}

// ScopeProps are the unit-level properties a scope is created with. They are
// the backstop, not the policy: the panel's policy lives on cgroups inside the
// scope, which it can change without root.
//
// There is deliberately no User here. systemd 252 and later chown a scope's
// delegation files to User= as part of the StartTransientUnit job -- before
// and while the PIDs are being attached -- so the account owns the inside of
// the scope before every move into it has been checked. The caller hands the
// scope over itself, with chownTree, once everything in it is the account's;
// see Adoption.Adopt.
type ScopeProps struct {
	// MemoryMax bounds everything in the scope; 0 leaves it unset.
	MemoryMax uint64
	// NoSwap sets MemorySwapMax=0. See deploy/vibepanel-system.service for the
	// measurement: with swap available, reaching the limit became a sustained
	// reclaim throttle on every session instead of an OOM kill.
	NoSwap bool
}

// StartScope creates a delegated scope holding pids.
//
// busctl rather than a D-Bus library: it ships with systemd, so it is present
// wherever this can work at all, and the binary stays free of a dependency for
// one call. systemd-run --scope cannot do this -- it only puts itself in the
// scope, and the processes to adopt already exist.
//
// Returns once systemd reports the scope active, because the caller's next
// step is to write into its cgroup.
func StartScope(ctx context.Context, m Manager, name string, pids []int, p ScopeProps) error {
	if len(pids) == 0 {
		return errors.New("cgroup: a scope needs at least one process")
	}
	props := [][]string{
		append([]string{"PIDs", "au", strconv.Itoa(len(pids))}, itoa(pids)...),
		{"Delegate", "b", "true"},
		{"Description", "s", "vibepanel sessions"},
		// Collected as soon as the last process exits, failed or not: a
		// failed scope left behind would make the next StartTransientUnit
		// under the same name refuse.
		{"CollectMode", "s", "inactive-or-failed"},
	}
	if p.MemoryMax > 0 {
		props = append(props, []string{"MemoryMax", "t", strconv.FormatUint(p.MemoryMax, 10)})
	}
	if p.NoSwap {
		props = append(props, []string{"MemorySwapMax", "t", "0"})
	}
	args := []string{}
	if m == User {
		args = append(args, "--user")
	}
	args = append(args, "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1",
		"org.freedesktop.systemd1.Manager", "StartTransientUnit", "ssa(sv)a(sa(sv))",
		name, "fail", strconv.Itoa(len(props)))
	for _, pr := range props {
		args = append(args, pr...)
	}
	args = append(args, "0")
	if _, err := run(ctx, "busctl", args...); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, serr := Scope(ctx, m, name)
		if serr == nil && info.Active && info.Cgroup != "" {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup: %s did not become active", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// StartAnchoredScope creates a delegated scope by running anchor inside it,
// through systemd-run, and returns once the scope is active. The anchor is
// left running: it is what keeps the scope in existence while nothing else is
// in it, and it is started detached so it outlives the process that started it.
//
// This is how a user manager's scope is made, rather than StartScope, because
// busctl --user needs the user's D-Bus bus, and a server without
// dbus-user-session has none -- measured on Ubuntu 22.04's minimal image, where
// `systemctl --user` answers and `busctl --user` fails with "No such file or
// directory". systemd-run reaches the manager without the bus.
func StartAnchoredScope(ctx context.Context, m Manager, name string, anchor []string, p ScopeProps) error {
	args := []string{}
	if m == User {
		args = append(args, "--user")
	}
	args = append(args, "--scope", "--quiet", "--collect", "--unit", name,
		"-p", "Delegate=yes", "--description", "vibepanel sessions")
	if p.MemoryMax > 0 {
		args = append(args, "-p", "MemoryMax="+strconv.FormatUint(p.MemoryMax, 10))
	}
	if p.NoSwap {
		args = append(args, "-p", "MemorySwapMax=0")
	}
	args = append(args, "--")
	args = append(args, anchor...)
	cmd := exec.Command("systemd-run", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cgroup: systemd-run: %w", err)
	}
	// Reaped whenever it exits, so a failed start is not a zombie.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := Scope(ctx, m, name)
		if err == nil && info.Active && info.Cgroup != "" {
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("cgroup: the anchor for %s exited: %v", name, err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup: %s did not become active", name)
		}
	}
}

func itoa(ns []int) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = strconv.Itoa(n)
	}
	return out
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
