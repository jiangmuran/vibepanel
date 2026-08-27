package sysmon

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Usage is what one session's process tree is costing.
type Usage struct {
	// CPUPercent is a share of the *whole machine*, the same denominator the
	// machine-level sample uses.
	//
	// top and htop use a different one -- 100% means one core saturated -- and
	// mixing the two on one screen is how a reader concludes that a session
	// using "310%" is somehow using more than the machine's "31%". The panel
	// shows both numbers within an inch of each other, so they have to be
	// comparable. Cores is carried alongside for anyone who wants to convert.
	CPUPercent float64 `json:"cpuPercent"`

	// RSS is the sum of resident set sizes across the tree.
	//
	// It double-counts pages shared between a parent and its forks, so it is
	// an over-estimate. Every process viewer that shows a tree total has the
	// same problem; the alternative is walking smaps for every process on
	// every sample, which costs more than the number is worth.
	RSS uint64 `json:"rss"`

	// Procs is how many processes were found under the pane, which is the
	// number that says whether the reading means anything: 1 is a bare shell.
	Procs int `json:"procs"`
}

// TreeSampler reports per-process-tree usage, keeping the previous CPU
// counters so a percentage can be expressed over the interval.
type TreeSampler struct {
	mu     sync.Mutex
	prev   map[int]uint64 // pane pid -> cumulative ticks over its tree
	prevAt time.Time
	last   map[int]Usage
}

// clockTicks is USER_HZ, which is 100 on every Linux this will run on.
//
// The correct way to ask is sysconf(_SC_CLK_TCK), which needs cgo -- and cgo
// is ruled out for this binary, since it has to run on a machine that has tmux
// and nothing else. The kernel's own procfs documentation states the value as
// 100, and utilities that read /proc without libc hardcode it for the same
// reason.
const clockTicks = 100

// Sample reads /proc once and attributes usage to each pane pid.
//
// Once, not once per session: a panel with two dozen sessions open would
// otherwise walk the whole process table two dozen times per tick, and the
// answer would be inconsistent between the first walk and the last.
//
// A pid that has gone is simply absent from the result rather than reported as
// zero -- zero is a real reading and "the session died" is not.
func (t *TreeSampler) Sample(paneOf map[string]int) map[string]Usage {
	stats := readProcTable()
	now := time.Now()

	children := make(map[int][]int, len(stats))
	for pid, st := range stats {
		if st.ppid != pid {
			children[st.ppid] = append(children[st.ppid], pid)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Too soon to measure anything: repeat the previous answer and leave the
	// counters alone, so the next caller still has a window worth measuring.
	// Same reasoning as minCPUWindow on the machine sampler, and the panel
	// being open in several places at once makes it the ordinary case.
	if !t.prevAt.IsZero() && now.Sub(t.prevAt) < minCPUWindow {
		out := make(map[string]Usage, len(paneOf))
		for id, pid := range paneOf {
			if u, ok := t.last[pid]; ok {
				out[id] = u
			}
		}
		return out
	}

	elapsed := now.Sub(t.prevAt).Seconds()
	haveprev := !t.prevAt.IsZero()

	ticks := make(map[int]uint64, len(paneOf))
	fresh := make(map[int]Usage, len(paneOf))
	out := make(map[string]Usage, len(paneOf))

	for id, pid := range paneOf {
		if _, alive := stats[pid]; !alive {
			continue
		}
		var total uint64
		u := Usage{}
		walk(pid, stats, children, func(st procStat) {
			total += st.ticks
			u.RSS += st.rss
			u.Procs++
		})
		ticks[pid] = total
		if haveprev && elapsed > 0 {
			if before, ok := t.prev[pid]; ok && total >= before {
				used := float64(total-before) / clockTicks
				u.CPUPercent = used / elapsed / float64(runtime.NumCPU()) * 100
				if u.CPUPercent > 100 {
					u.CPUPercent = 100
				}
			}
		}
		fresh[pid] = u
		out[id] = u
	}

	t.prev = ticks
	t.prevAt = now
	t.last = fresh
	return out
}

// walk visits a pid and everything under it.
//
// The visited set is not defensive tidiness. /proc is read without a lock, so
// a process can be reparented between reading its stat and reading its
// children's, and a cycle in the ppid graph is then representable even though
// the kernel's real tree has none. Without this the walk does not terminate.
func walk(root int, stats map[int]procStat, children map[int][]int, visit func(procStat)) {
	seen := make(map[int]bool)
	stack := []int{root}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		st, ok := stats[pid]
		if !ok {
			continue
		}
		visit(st)
		stack = append(stack, children[pid]...)
	}
}

type procStat struct {
	ppid  int
	ticks uint64 // utime + stime
	rss   uint64 // bytes
}

func readProcTable() map[int]procStat {
	dir, err := os.Open("/proc")
	if err != nil {
		return nil
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil
	}
	out := make(map[int]procStat, len(names))
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		// A process exiting between the listing and the read is ordinary, not
		// an error worth reporting anywhere.
		b, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil {
			continue
		}
		if st, ok := parseStat(string(b)); ok {
			out[pid] = st
		}
	}
	return out
}

// parseStat pulls ppid, cpu time and rss out of one /proc/<pid>/stat line.
//
// Field 2 is the executable name in parentheses and it is not escaped: a
// process can be called "(foo) bar)" and Chromium's renderers routinely carry
// spaces. Splitting the line on whitespace therefore shifts every field after
// it, which is the classic way this parse goes wrong -- silently, since the
// numbers it then reads are still numbers. Everything after the *last* ')' is
// fixed-position, so that is where parsing starts.
func parseStat(line string) (procStat, bool) {
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 >= len(line) {
		return procStat{}, false
	}
	// Fields from here are numbered as in proc(5) minus the first two: index 0
	// is state (field 3), so ppid is 1, utime is 11, stime is 12, rss is 21.
	f := strings.Fields(line[end+2:])
	if len(f) < 22 {
		return procStat{}, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return procStat{}, false
	}
	utime, err1 := strconv.ParseUint(f[11], 10, 64)
	stime, err2 := strconv.ParseUint(f[12], 10, 64)
	pages, err3 := strconv.ParseUint(f[21], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return procStat{}, false
	}
	return procStat{ppid: ppid, ticks: utime + stime, rss: pages * uint64(os.Getpagesize())}, true
}

// ProcReadable says whether per-process sampling is possible at all here.
//
// Same reasoning as CPUReadable on the machine sample: a UI that cannot tell
// "no reading yet" from "this platform has no /proc" renders "sampling…"
// forever on every Mac.
func ProcReadable() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}
