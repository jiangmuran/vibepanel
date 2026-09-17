package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/resources"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// `vibepanel service prepare` is the system unit's ExecStartPre=-+, and the
// only code in this binary that runs as root.
//
// It exists because a system unit's panel cannot move its sessions into a
// cgroup of their own: the panel's unit and the sessions' scope meet at
// system.slice, and cgroup v2 wants write access there to move a process
// between them. So before the panel starts, as root, this makes sure a tmux
// server is running, creates the scope around it, and moves into the scope
// everything the unit's cgroup still holds -- which, on a machine upgraded
// from a panel that kept its sessions in its own unit, is every session.
//
// What it will not do, because it is root:
//
//   - Run anything with the unit's environment. The unit reads the account's
//     own env file, and a PATH or LD_PRELOAD line there reached this process:
//     measured, it made root run a "systemctl" out of the account's home. So
//     the environment is read for two values and cleared, PATH is fixed, and
//     the account's environment goes only to processes that run as the
//     account.
//   - Run anything of the person's as root. tmux, and the login shell that
//     starts it, run with the account's credentials.
//   - Take the account from the environment. --as is written into the unit by
//     the installer.
//   - Move a process that is not entirely the account's -- every uid, checked
//     before and after the move -- or hand over a scope that holds one. cgroup
//     v2 does not ask who owns a process being moved, and the scope is the
//     account's to freeze and kill in.
//   - Trust where systemd says the scope is before checking it is under
//     system.slice and named as asked; that path is chowned.
//   - Do anything at all when the env file says --isolation=off, which only
//     takes something away.
//
// And it never stops the panel starting: the unit ignores its exit status
// ("-"), and every failure here is printed and returned as nil. A panel whose
// sessions are not isolated is worse; a panel that will not start is the thing
// this whole feature exists to prevent.
func servicePrepare(args []string) error {
	fs := flag.NewFlagSet("vibepanel service prepare", flag.ContinueOnError)
	as := fs.String("as", "", "the account the panel runs as")
	startServer := fs.Bool("start-server", false, "internal: start the tmux server as the calling user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if *startServer {
		// The child half, already running as the account.
		cfg, err := config.Load(nil, os.Stderr)
		if err != nil {
			return err
		}
		if err := cfg.EnsureDirs(); err != nil {
			return err
		}
		return tmux.New(cfg.TmuxSocket, cfg.TmuxDir()).EnsureServer(ctx)
	}

	if err := prepare(ctx, *as); err != nil {
		fmt.Fprintf(os.Stderr, "vibepanel: sessions were not moved into their own scope: %v\n", err)
	}
	return nil
}

// serviceAnchor holds a user manager's sessions scope open: systemd collects a
// scope with nothing in it, and this is the one process in it that is not a
// session or the tmux server. It does nothing, and ends on SIGTERM -- which is
// what stopping the scope sends.
func serviceAnchor() error {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	signal.Ignore(syscall.SIGHUP)
	<-ch
	return nil
}

func prepare(ctx context.Context, as string) error {
	if os.Geteuid() != 0 {
		return errors.New("prepare runs as root, from the system unit; a user unit does this itself")
	}
	// The unit's environment includes the account's own env file. Everything
	// root does from here runs with none of it: a PATH or an LD_PRELOAD in that
	// file reached this process, and every program it ran as root -- systemctl,
	// busctl -- was then the account's choice of program. Measured: a PATH line
	// in the env file ran a "systemctl" from the account's home as root.
	// The two values this needs are read out of it first, and the account's
	// own processes, started with the account's credentials, get it back.
	unitEnv := os.Environ()
	os.Clearenv()
	_ = os.Setenv("PATH", "/usr/sbin:/usr/bin:/sbin:/bin")
	if envValue(unitEnv, "VIBEPANEL_ISOLATION") == "off" {
		// Turning isolation off only takes something away, so the account
		// may.
		return nil
	}
	if as == "" {
		return errors.New("--as is required")
	}
	u, err := user.Lookup(as)
	if err != nil {
		return err
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil || uid == 0 {
		return fmt.Errorf("refusing account %q", as)
	}
	if !cgroup.Unified() {
		return errors.New("the cgroup v2 hierarchy is not mounted")
	}
	rel, err := cgroup.Of(0)
	if err != nil {
		return err
	}
	if m, ok := cgroup.ServiceManager(rel); !ok || m != cgroup.System {
		return fmt.Errorf("not running in a system service's cgroup (%s)", rel)
	}
	socket := envValue(unitEnv, "VIBEPANEL_TMUX_SOCKET")
	if socket == "" {
		socket = config.Default().TmuxSocket
	}
	env := accountEnv(u, unitEnv)

	server := serverPIDAs(ctx, u, uid, gid, env, socket)
	if server == 0 {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, self, "service", "prepare", "--start-server")
		cmd.Env, cmd.Dir = env, u.HomeDir
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		cmd.SysProcAttr = asAccount(u, uid, gid)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("starting tmux as %s: %w", as, err)
		}
		if server = serverPIDAs(ctx, u, uid, gid, env, socket); server == 0 {
			return errors.New("tmux did not start")
		}
	}

	total, _ := sysmon.Memory()
	a := resources.Adoption{
		Manager: cgroup.System,
		Scope:   cgroup.ScopeName(cgroup.System, uid, socket),
		// No User here, on any systemd version: 252+ chown the delegation
		// files as part of creating the scope, which hands the account the
		// inside of it before the moves have finished being checked. The scope
		// stays root's and Adopt hands it over itself, last.
		Props: cgroup.ScopeProps{
			MemoryMax: total / 100 * resources.MaxPoolPercent,
			NoSwap:    true,
		},
		OwnerUID: uid, OwnerGID: gid,
		// Where a pid that was reused mid-move goes back to. Root's, and not
		// where the process had been, which is the account's to choose when
		// the "tmux server" is a fake on the panel's socket.
		Unit: cgroup.At(rel),
	}
	left := resources.Leftovers(cgroup.At(rel), os.Getpid(), resources.LazyProcs())
	if err := a.Adopt(ctx, server, left); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "vibepanel: sessions are in %s (%d processes moved)\n", a.Scope, len(left))
	return nil
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// accountEnv is the unit's environment with the identity variables replaced by
// the account's own. Only ever given to processes that run as the account,
// for whom the env file is their own business.
func accountEnv(u *user.User, unitEnv []string) []string {
	shell := "/bin/sh"
	if b, err := os.ReadFile("/etc/passwd"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Split(line, ":")
			if len(f) >= 7 && f[0] == u.Username && f[6] != "" {
				shell = f[6]
			}
		}
	}
	over := map[string]string{"HOME": u.HomeDir, "USER": u.Username, "LOGNAME": u.Username, "SHELL": shell}
	var env []string
	for _, kv := range unitEnv {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := over[k]; ok {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range over {
		env = append(env, k+"="+v)
	}
	return env
}

func asAccount(u *user.User, uid, gid int) *syscall.SysProcAttr {
	var groups []uint32
	if ids, err := u.GroupIds(); err == nil {
		for _, g := range ids {
			if n, err := strconv.Atoi(g); err == nil {
				groups = append(groups, uint32(n))
			}
		}
	}
	return &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}}
}

// serverPIDAs asks the account's tmux for its server pid; 0 when there is none.
// As the account, because tmux finds its socket under /tmp/tmux-<uid>.
func serverPIDAs(ctx context.Context, u *user.User, uid, gid int, env []string, socket string) int {
	cmd := exec.CommandContext(ctx, "tmux", "-L", socket, "display-message", "-p", "#{pid}")
	cmd.Env, cmd.Dir = env, u.HomeDir
	cmd.SysProcAttr = asAccount(u, uid, gid)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return pid
}
