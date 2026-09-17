package resources

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// decide runs the question: raise it, update it, take it down, or act on it.
func (g *Governor) decide(now time.Time, params Params, level Level, reason Reason,
	r Reading, sessions []SessionView, l *Layout, ro roster, procs Procs) {
	g.mu.Lock()
	alert := g.view.Alert
	quiet := g.quiet
	g.mu.Unlock()

	if level == OK {
		g.t.okTicks++
		if alert != nil && g.t.okTicks >= clearAfterOK {
			g.setAlert(nil, now)
		}
		return
	}
	g.t.okTicks = 0
	if now.Before(quiet) {
		return
	}

	// A paused session is not the one to blame or to act on: somebody chose
	// to pause it, and its memory is not growing.
	candidates := make([]SessionView, 0, len(sessions))
	for _, s := range sessions {
		if !s.Frozen {
			candidates = append(candidates, s)
		}
	}
	culprit := g.culprit(candidates, now)
	if level == Warn && g.isSnoozed(culprit, now) {
		if alert != nil {
			g.setAlert(nil, now)
		}
		return
	}

	next := &Alert{
		Level: level.String(), Reason: reason,
		PoolCurrent: r.PoolCurrent, PoolMax: r.PoolMax, Available: r.Available, Total: r.Total,
		CanBoost: params.Mode != Performance,
	}
	if culprit != nil && Named(culprit.Held, r) {
		next.SessionID = culprit.ID
		next.CanPause = l != nil
		top := g.top(culprit.TmuxName, l, ro, procs, 3, g.t.unkillable)
		next.Proc = pickTarget(top)
		if alert != nil && alert.SessionID == next.SessionID {
			next.Proc = keepTarget(alert.Proc, next.Proc, top)
		}
	}
	if alert != nil && alert.SessionID == next.SessionID && sameProc(alert.Proc, next.Proc) {
		// The same question: same session, same process. A change of level is
		// an update to it, not a new one, and the countdown carries over.
		next.ID, next.AutoAt = alert.ID, alert.AutoAt
	} else {
		next.ID = newID()
		g.t.warnSince = time.Time{}
	}

	// The panel acts only on a process that is not the session's own -- the
	// agent is never ended unasked -- in a session that holds enough to be the
	// cause and that nobody is using, under a mode that allows it. A session
	// typed into in the last two minutes, or waiting on somebody, is asked about
	// and never acted on: a tester typing into one watched a ten-second
	// countdown start on the process beside her conversation.
	actable := params.AutoAct && next.Proc != nil && !next.Proc.Root &&
		culprit != nil && culprit.Priority != PriorityHigh && Responsible(culprit.Held, r, reason)
	switch {
	case !actable:
		next.AutoAt = 0
		g.t.warnSince = time.Time{}
	case level == Critical:
		if next.AutoAt == 0 {
			next.AutoAt = now.Add(time.Duration(params.GraceSeconds) * time.Second).Unix()
		}
		g.t.warnSince = time.Time{}
	case next.AutoAt != 0:
		if g.t.warnSince.IsZero() {
			g.t.warnSince = now
		} else if now.Sub(g.t.warnSince) > warnForgets {
			next.AutoAt = 0
		}
	}
	g.setAlert(next, now)

	if next.AutoAt == 0 || level != Critical || now.Unix() < next.AutoAt {
		return
	}
	act, err := g.kill(culprit.ID, next.Proc.PID, next.Proc.Start, "", true, l, ro, procs)
	if err != nil {
		// Not tried again: a process the kernel will not let this user signal
		// (a sudo in a session) was retried every two seconds, forever, and
		// nothing else in the session was ever offered.
		g.env.Log.Warn("ending the process the sessions were stalling on", "err", err)
		g.t.unkillable[procKey{next.Proc.PID, next.Proc.Start}] = true
		g.mu.Lock()
		g.quiet = now.Add(quietAfterFailure)
		g.mu.Unlock()
		g.setAlert(nil, now)
		return
	}
	g.env.Log.Warn("ended a process to relieve memory pressure", "name", act.Name, "rss", act.RSS, "session", act.SessionID)
	g.mu.Lock()
	g.quiet = now.Add(quietAfterAnswer)
	g.view.Alert = nil
	g.lastEmit = now
	g.mu.Unlock()
	if g.env.Emit != nil {
		g.env.Emit(Event{Alert: nil, Acted: &act})
	}
}

// keepTarget holds on to the process a question already named while it is
// still one of the largest. Two workers of the same size swapped places every
// few seconds, the question's id changed with them, and a person who read the
// button and pressed it ended the other one.
func keepTarget(prev, next *ProcView, top []ProcView) *ProcView {
	if prev == nil || next == nil || sameProc(prev, next) {
		return next
	}
	for i := range top {
		if top[i].PID == prev.PID && top[i].Start == prev.Start && top[i].RSS*10 >= next.RSS*9 {
			return &top[i]
		}
	}
	return next
}

func sameProc(a, b *ProcView) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.PID == b.PID && a.Start == b.Start
}

// culprit is the session to hold responsible: the one that grew most over the
// last minute, when that growth is large enough to be the story, and otherwise
// the one holding most.
//
// Growth first because the session that is biggest is usually just the one
// with the most work in it, and the one that just took four gigabytes is
// usually the reason anybody is being asked anything.
func (g *Governor) culprit(sessions []SessionView, now time.Time) *SessionView {
	if len(sessions) == 0 {
		return nil
	}
	var best *SessionView
	var bestGrowth int64
	for i := range sessions {
		s := &sessions[i]
		samples := g.t.growth[s.ID]
		if len(samples) < 2 {
			continue
		}
		grown := int64(samples[len(samples)-1].cur) - int64(samples[0].cur)
		if grown > bestGrowth {
			best, bestGrowth = s, grown
		}
	}
	if best != nil && bestGrowth >= growthMatters {
		return best
	}
	biggest := &sessions[0]
	for i := range sessions {
		if sessions[i].Held > biggest.Held {
			biggest = &sessions[i]
		}
	}
	return biggest
}

// pickTarget is the process to name: the largest, except that the pane's own
// process -- the agent -- is passed over for a child at least half its size.
// Ending typst costs a rerun; ending the agent costs the conversation.
func pickTarget(top []ProcView) *ProcView {
	if len(top) == 0 {
		return nil
	}
	pick := top[0]
	if pick.Root && len(top) > 1 && top[1].RSS*2 >= pick.RSS {
		pick = top[1]
	}
	return &pick
}

// top lists a session's largest processes, leaving out any in skip.
func (g *Governor) top(tmuxName string, l *Layout, ro roster, procs Procs, n int, skip map[procKey]bool) []ProcView {
	pane := ro.panes[tmuxName].PID
	var pids []int
	if l != nil {
		leaf, ok := l.Session(tmuxName)
		if !ok {
			return nil
		}
		pids, _ = leaf.Procs()
	} else if pane > 0 {
		pids = tree(pane)
	}
	out := make([]ProcView, 0, len(pids))
	for _, pid := range pids {
		p, ok := procs(pid)
		if !ok || skip[procKey{pid, p.Start}] {
			continue
		}
		out = append(out, ProcView{PID: pid, Start: p.Start, Name: p.Comm, RSS: p.RSS, Root: pid == pane})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RSS > out[j].RSS })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func (g *Governor) isSnoozed(s *SessionView, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.snoozed[""]) {
		return true
	}
	return s != nil && now.Before(g.snoozed[s.ID])
}

// Kill ends a process a person chose.
func (g *Governor) Kill(sessionID string, pid int, start uint64, who string) (Action, error) {
	g.mu.Lock()
	l := g.layout
	g.mu.Unlock()
	act, err := g.kill(sessionID, pid, start, who, false, l, g.roster(), LazyProcs())
	if err == nil {
		g.answered(sessionID)
	}
	return act, err
}

// kill checks that pid is still the process that was shown, and still in the
// session it was shown in, and ends it.
//
// Both checks are made immediately before the signal, because the person saw
// the process seconds ago and a pid is reused: the process they chose has
// exited and something else has the number is not a hypothetical on a machine
// that has just been through an OOM kill.
func (g *Governor) kill(sessionID string, pid int, start uint64, who string, auto bool, l *Layout, ro roster, procs Procs) (Action, error) {
	meta, ok := ro.byID[sessionID]
	if !ok {
		return Action{}, ErrUnknownSession
	}
	if pid <= 1 {
		return Action{}, ErrNotInSession
	}
	pane := ro.panes[meta.TmuxName].PID
	p, ok := sysmon.ReadProc(pid)
	if !ok || p.Start != start {
		return Action{}, ErrGone
	}
	g.mu.Lock()
	server := g.server
	g.mu.Unlock()
	if pid == os.Getpid() || pid == server.PID || pid <= 1 {
		return Action{}, ErrNotInSession
	}
	if l != nil {
		leaf, ok := l.Session(meta.TmuxName)
		rel, err := cgroup.Of(pid)
		if !ok || err != nil || rel != leaf.Rel() {
			return Action{}, ErrNotInSession
		}
	} else if pane <= 0 || !descendsFrom(pid, pane, procs) {
		return Action{}, ErrNotInSession
	}

	// A person's choice gets a chance to exit cleanly. The panel's own does
	// not: by the time it acts the sessions have been stalled for the whole
	// grace period, and a process that handles SIGTERM by allocating is the
	// likeliest one to be in that state.
	sig := syscall.SIGTERM
	if auto {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return Action{}, ErrGone
		}
		return Action{}, err
	}
	if !auto {
		go func() {
			time.Sleep(5 * time.Second)
			if q, ok := sysmon.ReadProc(pid); ok && q.Start == start {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}()
	}
	act := Action{At: time.Now().Unix(), Kind: ActionKill, Auto: auto, SessionID: sessionID, Name: p.Comm, RSS: p.RSS, Who: who}
	g.record(act, fmt.Sprintf("%q (pid %d, %s)", p.Comm, pid, sysmon.FormatBytes(p.RSS)))
	return act, nil
}

// Freeze pauses or resumes a session.
//
// Resuming does not need the sessions to be isolated now, only to have been:
// a panel that lost its hold on the scope, or was started with isolation off
// after one that paused something, still has to be able to let it go.
func (g *Governor) Freeze(sessionID string, on bool, who string) (Action, error) {
	ro := g.roster()
	meta, ok := ro.byID[sessionID]
	if !ok {
		return Action{}, ErrUnknownSession
	}
	g.mu.Lock()
	l := g.layout
	server := g.server
	g.mu.Unlock()
	if l == nil && !on && server.PID > 0 {
		if found, err := Find(g.env.Scope, server.PID); err == nil {
			l = &found
		}
	}
	if l == nil {
		return Action{}, ErrNoFreezer
	}
	leaf, ok := l.Session(meta.TmuxName)
	if !ok || !leaf.Exists() {
		return Action{}, ErrUnknownSession
	}
	if err := leaf.Freeze(on); err != nil {
		return Action{}, err
	}
	kind := ActionThaw
	if on {
		kind = ActionFreeze
		g.answered(sessionID)
	}
	act := Action{At: time.Now().Unix(), Kind: kind, SessionID: sessionID, Who: who}
	g.record(act, sessionID)
	return act, nil
}

// Snooze stops asking about a session, or about anything when sessionID is
// empty, for d. A warning only: a stall is still raised, because a person who
// said "not now" about a session near its budget did not say "let the machine
// stop answering".
func (g *Governor) Snooze(alertID, sessionID string, d time.Duration) error {
	if sessionID != "" {
		if _, ok := g.roster().byID[sessionID]; !ok {
			return ErrUnknownSession
		}
	}
	g.mu.Lock()
	a := g.view.Alert
	if a == nil || a.ID != alertID {
		g.mu.Unlock()
		return ErrNoAlert
	}
	now := time.Now()
	for k, until := range g.snoozed {
		if now.After(until) {
			delete(g.snoozed, k)
		}
	}
	g.snoozed[sessionID] = now.Add(d)
	g.mu.Unlock()
	if a.Level == Warn.String() {
		g.setAlert(nil, now)
	}
	return nil
}
