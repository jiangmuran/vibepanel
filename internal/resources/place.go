package resources

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// roster is the sessions and panes as the poller last saw them, read once per
// tick (or once per request) and looked up by key after that.
type roster struct {
	metas []SessionMeta
	byID  map[string]SessionMeta
	panes map[string]Pane // by tmux name
	list  []Pane
}

func (g *Governor) roster() roster {
	ro := roster{byID: map[string]SessionMeta{}, panes: map[string]Pane{}}
	if g.env.Sessions != nil {
		ro.metas = g.env.Sessions()
		for _, m := range ro.metas {
			ro.byID[m.ID] = m
		}
	}
	if g.env.Panes != nil {
		ro.list = g.env.Panes()
		for _, p := range ro.list {
			ro.panes[p.TmuxName] = p
		}
	}
	return ro
}

// place finds, builds and sorts the scope, adopting into it when this panel
// is allowed to.
func (g *Governor) place(ctx context.Context, ro roster, procs Procs) (Isolation, *Layout) {
	iso := Isolation{State: NotIsolated, Scope: g.env.Scope}
	switch {
	case g.env.Disabled:
		g.release(ctx)
		iso.Reason = NoneDisabled
		return iso, nil
	case !g.supported:
		iso.Reason = NonePlatform
		return iso, nil
	case !cgroup.Unified():
		iso.Reason = NoneCgroupV1
		return iso, nil
	case g.env.Manager == "":
		iso.Reason = NoneNotService
		return iso, nil
	}
	server := g.serverPID(ctx)
	if server.PID == 0 {
		iso.Reason = NoneNoServer
		return iso, nil
	}
	layout, err := Find(g.env.Scope, server.PID)
	if errors.Is(err, ErrNotInScope) {
		layout, err = g.adopt(ctx, server, procs)
	}
	if err == nil {
		err = layout.Build(server.PID)
	}
	if err != nil {
		iso.Reason, iso.Detail = noneReason(err)
		return iso, nil
	}
	for _, p := range ro.list {
		layout.Reclaim(p)
	}
	layout.Sort(ro.list, server.PID, procs)
	// Only once the poller has listed the sessions. An empty list at startup
	// is "not listed yet", and pruning then would take down leaves -- and thaw
	// them -- for sessions that are about to be listed.
	if len(ro.list) > 0 {
		live := map[string]bool{}
		for _, p := range ro.list {
			live[p.TmuxName] = true
		}
		layout.Prune(live)
	}
	iso.State, iso.Scope = Isolated, layout.Scope.Rel()
	return iso, &layout
}

// noneReason is the one place an error from finding or building the scope
// becomes what the page says.
func noneReason(err error) (reason, detail string) {
	switch {
	case errors.Is(err, errUnitOutdated):
		return NoneUnitOutdated, ""
	case errors.Is(err, errPrepareFailed):
		return NonePrepareFailed, ""
	case errors.Is(err, errRestarting):
		return NoneRestarting, ""
	case errors.Is(err, ErrNotDelegated):
		return NoneNotDelegated, ""
	case errors.Is(err, ErrNotInScope):
		return NoneFailed, ""
	}
	return NoneFailed, err.Error()
}

var (
	errUnitOutdated  = errors.New("resources: the system unit does not prepare the sessions scope")
	errPrepareFailed = errors.New("resources: the unit's root step did not move the sessions")
	errRestarting    = errors.New("resources: restarting so the unit can prepare the sessions scope")
)

// adopt moves the server into the scope, or arranges for that to happen.
func (g *Governor) adopt(ctx context.Context, server procKey, procs Procs) (Layout, error) {
	now := time.Now()
	switch g.env.Manager {
	case cgroup.System:
		if !g.env.Prepared {
			return Layout{}, errUnitOutdated
		}
		// The unit's root step ran before this process started and would have
		// moved a server that existed then. A server older than this process
		// is therefore one it failed on, and restarting would fail the same
		// way -- a panel that restarted itself every minute, forever, dropping
		// every terminal each time, was what an unconditional restart here did
		// to a unit whose account the helper refuses. Only a server started
		// since -- the old one died and this panel started another -- is
		// fixed by a restart.
		if server.Start <= g.self.Start {
			return Layout{}, errPrepareFailed
		}
		if g.env.Restart != nil && now.Sub(g.t.started) > time.Minute && now.Sub(g.t.restarted) > 10*time.Minute {
			g.t.restarted = now
			g.env.Log.Warn("the tmux server is outside the sessions scope; restarting so the unit can move it")
			g.env.Restart()
			return Layout{}, errRestarting
		}
		return Layout{}, ErrNotInScope
	case cgroup.User:
		if now.Sub(g.t.adoptTry) < adoptBackoff {
			return Layout{}, ErrNotInScope
		}
		g.t.adoptTry = now
		total, _ := sysmon.Memory()
		backstop := total / 100 * MaxPoolPercent
		if g.env.ScopeMemoryMax > 0 {
			backstop = g.env.ScopeMemoryMax
		}
		a := Adoption{
			Manager:  cgroup.User,
			Scope:    g.env.Scope,
			Props:    cgroup.ScopeProps{MemoryMax: backstop, NoSwap: true},
			OwnerUID: -1, OwnerGID: -1,
			Anchor: g.env.Anchor,
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		left := Leftovers(g.env.Unit, os.Getpid(), procs)
		if err := a.Adopt(actx, server.PID, left); err != nil {
			g.env.Log.Warn("adopting sessions into their scope", "err", err)
			// Whatever did move stays moved; Find says where the server is.
		} else {
			g.env.Log.Info("sessions moved into their own scope", "scope", g.env.Scope, "processes", len(left))
		}
		return Find(g.env.Scope, server.PID)
	}
	return Layout{}, ErrNotInScope
}

// release undoes the panel's hold on a scope it has been told not to manage.
//
// The scope outlives the panel, and so do the limits and the freezer state
// inside it. A panel started with --isolation=off after one that managed the
// scope would otherwise leave the sessions under a budget nobody was adjusting
// any more -- possibly one shrunk for a busy host -- and a paused session with
// nothing on any page that could resume it.
func (g *Governor) release(ctx context.Context) {
	if g.t.released {
		return
	}
	server := g.serverPID(ctx)
	if server.PID == 0 {
		return
	}
	g.t.released = true
	l, err := Find(g.env.Scope, server.PID)
	if err != nil {
		return
	}
	_ = l.Pool.SetUint("memory.max", cgroup.Unlimited)
	names, _ := l.Pool.Children()
	for _, n := range names {
		if strings.HasPrefix(n, sessionLeaf) {
			_ = l.Pool.Child(n).Freeze(false)
		}
	}
}

// serverPID is the tmux server, remembered by pid and start time while it
// lives so that a tick asks tmux nothing. A stalled tmux server answers slowly,
// and the governor is the thing that has to keep working while one does. The
// start time is what makes the memory safe: the panel forks tmux clients all
// the time, and a server that died and had its pid given to one of them would
// otherwise still look like tmux.
func (g *Governor) serverPID(ctx context.Context) procKey {
	g.mu.Lock()
	cached := g.server
	g.mu.Unlock()
	if cached.PID > 0 && isTmux(cached) {
		return cached
	}
	key := procKey{}
	if g.env.ServerPID != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		if pid, err := g.env.ServerPID(cctx); err == nil {
			if p, ok := sysmon.ReadProc(pid); ok {
				key = procKey{PID: pid, Start: p.Start}
			}
		}
		cancel()
	}
	g.mu.Lock()
	g.server = key
	g.mu.Unlock()
	return key
}

// Place puts a pane that has just been created into its session's cgroup,
// without waiting for the next tick. Its descendants follow it on their own.
func (g *Governor) Place(tmuxName string, panePID int) {
	g.mu.Lock()
	l := g.layout
	g.mu.Unlock()
	if l == nil {
		return
	}
	if l.Reclaim(Pane{TmuxName: tmuxName, PID: panePID}) > 0 {
		return
	}
	leaf, ok := l.Session(tmuxName)
	if !ok || leaf.Ensure() != nil {
		return
	}
	if leaf.Move(panePID) == nil {
		raiseOOMScore(panePID)
	}
}

// EndSession ends every process still in a session's cgroup, once the session
// itself has been ended. cgroup.kill does it in one write and cannot miss a
// process forked during it; a kernel without it (before 5.14) gets each
// process signalled instead.
func (g *Governor) EndSession(tmuxName string) {
	g.mu.Lock()
	l := g.layout
	g.mu.Unlock()
	if l == nil {
		return
	}
	leaf, ok := l.Session(tmuxName)
	if !ok || !leaf.Exists() {
		return
	}
	if leaf.Frozen() {
		_ = leaf.Freeze(false)
	}
	if leaf.Write("cgroup.kill", "1") == nil {
		return
	}
	pids, _ := leaf.Procs()
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
