package resources

import (
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// readSessions measures each session: from its cgroup when the sessions are
// isolated, from its pane's process tree when they are not.
func (g *Governor) readSessions(l *Layout, ro roster, now time.Time) []SessionView {
	var table map[int]sysmon.Proc
	var kids map[int][]int
	if l == nil {
		table = sysmon.ProcTable()
		kids = children(table)
	}
	out := make([]SessionView, 0, len(ro.metas))
	seen := map[string]bool{}
	for _, m := range ro.metas {
		v := SessionView{ID: m.ID, TmuxName: m.TmuxName, Priority: PriorityNormal}
		if l != nil {
			leaf, ok := l.Session(m.TmuxName)
			if !ok || !leaf.Exists() {
				continue
			}
			v.Memory, _ = leaf.Uint("memory.current")
			v.Held = held(leaf)
			v.Frozen = leaf.Frozen()
			pids, _ := leaf.Procs()
			v.Procs = len(pids)
			// A paused session whose processes have all gone -- its agent was
			// ended, or it exited -- is not paused in any sense a person would
			// recognise, and the page offered to resume nothing.
			if v.Frozen && len(pids) == 0 {
				_ = leaf.Freeze(false)
				v.Frozen = false
			}
			g.noteOOM(m.ID, leaf)
			usage := leaf.KeyValues("cpu.stat")["usage_usec"]
			if prev, ok := g.t.cpuPrev[m.ID]; ok && usage >= prev.usage {
				if el := now.Sub(prev.at).Seconds(); el > 0 {
					v.CPUPercent = float64(usage-prev.usage) / 1e6 / el / float64(runtime.NumCPU()) * 100
				}
			}
			g.t.cpuPrev[m.ID] = cpuSample{at: now, usage: usage}
		} else {
			pane, ok := ro.panes[m.TmuxName]
			if !ok || pane.PID <= 0 {
				continue
			}
			for _, pid := range walkTree(pane.PID, table, kids) {
				v.Memory += table[pid].RSS
				v.Procs++
			}
			// RSS is the closest reading without a cgroup. It includes mapped
			// files, so it errs towards holding a session responsible.
			v.Held = v.Memory
		}
		if m.Waiting || now.Sub(m.LastInput) < inputWindow {
			v.Priority = PriorityHigh
		}
		seen[m.ID] = true
		g.t.growth[m.ID] = appendSample(g.t.growth[m.ID], memSample{at: now, cur: v.Held}, now)
		out = append(out, v)
	}
	for id := range g.t.growth {
		if !seen[id] {
			delete(g.t.growth, id)
			delete(g.t.cpuPrev, id)
			delete(g.t.hotSince, id)
			delete(g.t.ooms, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Held > out[j].Held })
	return out
}

// noteOOM records the kernel's OOM kills in a session since the last tick.
// memory.events.local, not memory.events: the latter counts the leaf's
// descendants too, and a session leaf has none, but the local file says what
// is meant.
func (g *Governor) noteOOM(sessionID string, leaf cgroup.Dir) {
	n := leaf.KeyValues("memory.events.local")["oom_kill"]
	prev, seen := g.t.ooms[sessionID]
	g.t.ooms[sessionID] = n
	if !seen || n <= prev {
		return
	}
	act := Action{At: time.Now().Unix(), Kind: ActionOOM, Auto: true, SessionID: sessionID}
	g.record(act, fmt.Sprintf("%s: %d", sessionID, n-prev))
	if g.env.Emit != nil {
		g.mu.Lock()
		a := g.view.Alert
		g.mu.Unlock()
		g.env.Emit(Event{Alert: a, Acted: &act})
	}
}

func appendSample(s []memSample, x memSample, now time.Time) []memSample {
	s = append(s, x)
	for len(s) > 0 && now.Sub(s[0].at) > growthWindow {
		s = s[1:]
	}
	return s
}

// Weights. cpu.weight is relative among siblings, so these only matter while
// the sessions are competing for CPU, which is exactly when they should: the
// one somebody is typing in, or the one waiting on them, should not be the one
// that feels the build next door.
const (
	weightLow    = 25
	weightNormal = 100
	weightHigh   = 400
)

// When a session is moved down. The machine has to be short of CPU -- a
// quarter of the last ten seconds with something runnable and waiting -- and
// the session has to have been taking at least 40% of every core for a
// minute. Either alone is not a reason: a busy machine with no single hog is
// just busy, and a build using every core on an idle machine is using what is
// there.
const (
	cpuBusyPressure = 25.0
	hogPercent      = 40.0
	hogFor          = time.Minute
)

// demote marks the sessions that are hogging a contended CPU as low priority.
// It never touches one that is high: somebody is using that one.
func (g *Governor) demote(sessions []SessionView, cpuPressure float64, now time.Time) {
	busy := cpuPressure >= cpuBusyPressure
	for i := range sessions {
		s := &sessions[i]
		if s.CPUPercent < hogPercent {
			delete(g.t.hotSince, s.ID)
			continue
		}
		since, ok := g.t.hotSince[s.ID]
		if !ok {
			g.t.hotSince[s.ID] = now
			since = now
		}
		if busy && s.Priority != PriorityHigh && now.Sub(since) >= hogFor {
			s.Priority = PriorityLow
		}
	}
}

// applyWeights writes each session leaf's cpu.weight when the file disagrees.
// The file, not a remembered value: a leaf that was removed and made again for
// a restarted session starts at the default, and a cache said it was already
// set.
func (g *Governor) applyWeights(l *Layout, sessions []SessionView) {
	if l == nil {
		return
	}
	for _, s := range sessions {
		leaf, ok := l.Session(s.TmuxName)
		if !ok {
			continue
		}
		w := weightNormal
		switch s.Priority {
		case PriorityHigh:
			w = weightHigh
		case PriorityLow:
			w = weightLow
		}
		if cur, err := leaf.Read("cpu.weight"); err == nil && cur == strconv.Itoa(w) {
			continue
		}
		_ = leaf.SetUint("cpu.weight", uint64(w))
	}
}

// tree is a process and its descendants, for the case with no cgroups to ask.
func tree(root int) []int {
	table := sysmon.ProcTable()
	return walkTree(root, table, children(table))
}

func children(table map[int]sysmon.Proc) map[int][]int {
	out := map[int][]int{}
	for pid, p := range table {
		if p.PPID != pid {
			out[p.PPID] = append(out[p.PPID], pid)
		}
	}
	return out
}

// walkTree keeps a visited set for the reason sysmon's walk does: /proc is read
// without a lock, and a reparent between two reads can make a cycle.
func walkTree(root int, table map[int]sysmon.Proc, children map[int][]int) []int {
	var out []int
	stack := []int{root}
	seen := map[int]bool{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if _, ok := table[pid]; !ok {
			continue
		}
		out = append(out, pid)
		stack = append(stack, children[pid]...)
	}
	return out
}
