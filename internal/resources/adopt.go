package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// Adoption: getting the tmux server, and everything that was already running
// under it, out of the panel's unit and into the scope.
//
// Two callers, because the permission to do it lives in two places.
//
//   - A **user** unit does it itself, at startup and whenever the server turns
//     up outside the scope. The user manager is the user's own, the scope it
//     creates belongs to them, and moving a process between two cgroups under
//     user@<uid>.service needs nothing more than owning both.
//   - A **system** unit cannot: its cgroup and the scope meet at system.slice,
//     which is root's, and cgroup v2 checks write access on the common ancestor
//     of the two. So the unit runs `vibepanel service prepare` as root, with
//     ExecStartPre=+, before the panel starts. That is the only thing in this
//     package that runs as root, and it takes nothing from the environment it
//     could be steered with: the user comes from the unit file, the scope name
//     is sanitised, and every path it writes is under the cgroup mount.

// Leftovers lists the processes in a unit's cgroup that are not this process
// or its descendants: the tmux server and every session, when the panel ran
// the old way, plus whatever a previous panel orphaned.
//
// Descendants are excluded because they are the panel's own work -- its tmux
// clients, a git subprocess, the chat assistant -- and belong with it.
func Leftovers(unit cgroup.Dir, self int, procs Procs) []int {
	var out []int
	_ = filepath.WalkDir(string(unit), func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		pids, perr := cgroup.Dir(path).Procs()
		if perr != nil {
			return nil
		}
		for _, pid := range pids {
			if !descendsFrom(pid, self, procs) {
				out = append(out, pid)
			}
		}
		return nil
	})
	sort.Ints(out)
	return out
}

func descendsFrom(pid, ancestor int, procs Procs) bool {
	for cur, n := pid, 0; cur > 0 && n < 128; n++ {
		if cur == ancestor {
			return true
		}
		p, ok := procs(cur)
		if !ok {
			return false
		}
		cur = p.PPID
	}
	return false
}

// Adoption is one attempt at moving processes into the scope.
type Adoption struct {
	Manager cgroup.Manager
	Scope   string
	Props   cgroup.ScopeProps
	// Owner is who the delegation files are handed to when systemd would not
	// do it (a scope on systemd before 252). Negative means leave them alone,
	// which is right for a user manager: everything under it is the user's.
	OwnerUID, OwnerGID int
	// Anchor, when set, creates the scope by running this command in it
	// (cgroup.StartAnchoredScope) instead of by handing systemd the server's
	// pid. A user manager needs it; see StartAnchoredScope.
	Anchor []string
}

// Adopt puts serverPID and pids into the scope, creating it if needed.
//
// The server goes first and alone: it creates the scope if there is none, and
// if any other pid had exited by the time systemd read the list, the whole
// StartTransientUnit would fail and the server with it.
//
// With an owner (the root helper), every process is checked to belong to that
// account before it is moved, and checked again after. cgroup v2 asks only for
// write access on the common ancestor, not who owns the process, and the scope
// is delegated to the account: a pid the account could get root to move here
// -- by answering the tmux pid query itself, or by racing a pid's reuse -- is a
// process the account can then freeze or kill through cgroup.kill, whoever it
// belongs to.
func (a Adoption) Adopt(ctx context.Context, serverPID int, pids []int) error {
	owned := func(pid int) (procKey, string, bool) { return procKey{PID: pid}, "", true }
	if a.OwnerUID >= 0 {
		owned = func(pid int) (procKey, string, bool) { return ownedBy(pid, a.OwnerUID) }
	}
	server, serverFrom, ok := owned(serverPID)
	if !ok {
		return fmt.Errorf("resources: the tmux server %d does not belong to the account", serverPID)
	}
	if a.OwnerUID >= 0 {
		if p, ok := sysmon.ReadProc(serverPID); !ok || !strings.HasPrefix(p.Comm, "tmux") {
			return fmt.Errorf("resources: %d is not a tmux server", serverPID)
		}
	}

	info, err := cgroup.Scope(ctx, a.Manager, a.Scope)
	if err != nil {
		return err
	}
	if !info.Active {
		var serr error
		if len(a.Anchor) > 0 {
			serr = cgroup.StartAnchoredScope(ctx, a.Manager, a.Scope, a.Anchor, a.Props)
		} else {
			serr = cgroup.StartScope(ctx, a.Manager, a.Scope, []int{serverPID}, a.Props)
			if a.OwnerUID >= 0 {
				stillSame(server, serverFrom)
			}
		}
		if serr != nil && !errors.Is(serr, cgroup.ErrUserUnsupported) {
			return fmt.Errorf("resources: create %s: %w", a.Scope, serr)
		}
		if info, err = cgroup.Scope(ctx, a.Manager, a.Scope); err != nil {
			return err
		}
	}
	if info.Cgroup == "" {
		return fmt.Errorf("resources: %s has no cgroup", a.Scope)
	}
	scope := cgroup.At(info.Cgroup)

	// Into the scope itself while it has no controllers enabled, which is the
	// case until the panel has built the layout once. After that the scope
	// may not hold processes, so they go where Build would have put them.
	target, serverTarget := scope, scope
	if len(scope.Enabled()) > 0 {
		l := Layout{Scope: scope, Pool: scope.Child(poolName)}
		for _, d := range []cgroup.Dir{l.Pool, l.Other(), l.Tmux()} {
			if err := d.Ensure(); err != nil {
				return err
			}
		}
		target, serverTarget = l.Other(), l.Tmux()
	}
	move := func(to cgroup.Dir, pid int) error {
		key, from, ok := owned(pid)
		if !ok {
			return fmt.Errorf("not the account's")
		}
		if err := to.Move(pid); err != nil {
			return err
		}
		if a.OwnerUID >= 0 && !stillSame(key, from) {
			return fmt.Errorf("exited while being moved")
		}
		return nil
	}
	if err := move(serverTarget, serverPID); err != nil {
		return fmt.Errorf("resources: move the tmux server: %w", err)
	}
	var failed []string
	for _, pid := range pids {
		if pid == serverPID {
			continue
		}
		if err := move(target, pid); err != nil {
			failed = append(failed, fmt.Sprintf("%d: %v", pid, err))
		}
	}

	// Handed over last, and only if everything in it is the account's.
	if a.OwnerUID >= 0 {
		if err := onlyOwnedIn(scope, a.OwnerUID); err != nil {
			return err
		}
		if err := chownTree(scope, a.OwnerUID, a.OwnerGID); err != nil {
			return err
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("resources: %d processes stayed behind: %s", len(failed), strings.Join(failed, "; "))
	}
	return nil
}

// ownedBy reads a process's identity and cgroup, and whether every one of its
// uids -- real, effective, saved, filesystem -- is uid. All four, because a
// setuid program the account started has its real uid and somebody else's
// effective one, and is not the account's to hand over.
func ownedBy(pid, uid int) (procKey, string, bool) {
	p, ok := sysmon.ReadProc(pid)
	if !ok {
		return procKey{}, "", false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return procKey{}, "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, found := strings.CutPrefix(line, "Uid:")
		if !found {
			continue
		}
		ids := strings.Fields(rest)
		if len(ids) != 4 {
			return procKey{}, "", false
		}
		for _, id := range ids {
			if id != strconv.Itoa(uid) {
				return procKey{}, "", false
			}
		}
		rel, err := cgroup.Of(pid)
		if err != nil {
			return procKey{}, "", false
		}
		return procKey{PID: pid, Start: p.Start}, rel, true
	}
	return procKey{}, "", false
}

// stillSame checks, after a move, that the pid is the process that was
// checked. If it is not -- the process exited and its number was reused in
// between -- whatever now has the number is put back where it was.
func stillSame(k procKey, from string) bool {
	p, ok := sysmon.ReadProc(k.PID)
	if ok && p.Start == k.Start {
		return true
	}
	if ok && from != "" {
		_ = cgroup.At(from).Move(k.PID)
	}
	return false
}

// onlyOwnedIn refuses to hand a scope over while it holds a process that is
// not the account's.
func onlyOwnedIn(scope cgroup.Dir, uid int) error {
	return filepath.WalkDir(string(scope), func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		pids, _ := cgroup.Dir(path).Procs()
		for _, pid := range pids {
			if _, _, ok := ownedBy(pid, uid); !ok {
				if _, alive := sysmon.ReadProc(pid); alive {
					return fmt.Errorf("resources: %s holds process %d, which is not the account's", cgroup.Dir(path).Rel(), pid)
				}
			}
		}
		return nil
	})
}

// chownTree hands the scope and every cgroup under it to the owner. Only the
// cgroups this package creates exist under it, and each needs the same three
// files for the user to keep organising below it.
func chownTree(scope cgroup.Dir, uid, gid int) error {
	return filepath.WalkDir(string(scope), func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return cgroup.Dir(path).Chown(uid, gid)
	})
}
