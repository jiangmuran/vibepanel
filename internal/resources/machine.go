package resources

import (
	"os"
	"os/user"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// The machine's largest processes, wherever they live.
//
// The sessions list answers "which of mine"; this answers "what is eating the
// machine", which on a box that ran out of memory an hour ago is usually a
// container or a VM the sessions never started. It is read-only on purpose:
// the panel ends processes that are inside a session, which it can prove, and
// shows everything else.

// machineProcs is how many are listed.
const machineProcs = 12

// machineTTL bounds how often /proc is walked for this. The page polls every
// two seconds and several tabs may be open; the answer does not change that
// fast and the walk reads a status file for every process.
const machineTTL = 4 * time.Second

// Kinds of place a process can live.
const (
	WhereSession   = "session"
	WherePanel     = "panel"
	WhereTmux      = "tmux"
	WherePlugin    = "plugin"
	WhereContainer = "container"
	WhereUnit      = "unit"
	WhereUser      = "user"
	WhereKernel    = "kernel"
	WhereOther     = "other"
)

// Where says where a process comes from: the answer to "what is this and who
// started it".
type Where struct {
	Kind string `json:"kind"`
	// Label is a container's short id, a unit's name, a user's session or app
	// unit, a plugin's id.
	Label        string `json:"label,omitempty"`
	SessionID    string `json:"sessionId,omitempty"`
	SessionTitle string `json:"sessionTitle,omitempty"`
	ProjectName  string `json:"projectName,omitempty"`
}

// MachineProc is one of the largest processes on the machine.
type MachineProc struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
	Name  string `json:"name"`
	Cmd   string `json:"cmd,omitempty"`
	RSS   uint64 `json:"rss"`
	// Swap is what the process has out in swap. A VM that sits mostly in swap
	// has a small RSS and is exactly the thing somebody is looking for.
	Swap      uint64 `json:"swap"`
	User      string `json:"user"`
	StartedAt int64  `json:"startedAt"`
	Where     Where  `json:"where"`
}

var (
	containerUnit = regexp.MustCompile(`^(?:docker-|libpod-|cri-containerd-|crio-)([0-9a-f]{12,64})\.scope$`)
	sessionsScope = regexp.MustCompile(`^vibepanel-sessions[A-Za-z0-9_.@-]*\.scope$`)
)

// attribute says where a process lives from its cgroup path (cgroup v2, as
// /proc/<pid>/cgroup prints it). self is the panel's own path: a process in
// it that is a service is the panel's, and one that is not (the panel started
// by hand from a terminal) is not, or every process in that login session
// would be labelled the panel.
//
// A session is returned by its tmux name; the caller resolves it against the
// roster, because this has no business knowing the sessions.
func attribute(rel, self string) Where {
	parts := strings.FieldsFunc(rel, func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return Where{Kind: WhereKernel}
	}
	for i, p := range parts {
		if m := containerUnit.FindStringSubmatch(p); m != nil {
			return Where{Kind: WhereContainer, Label: m[1][:12]}
		}
		if sessionsScope.MatchString(p) && i+2 < len(parts) && parts[i+1] == poolName {
			leaf := parts[i+2]
			switch {
			case leaf == tmuxLeaf:
				return Where{Kind: WhereTmux}
			case leaf == otherLeaf:
				return Where{Kind: WhereOther}
			case strings.HasPrefix(leaf, sessionLeaf) && leafName.MatchString(leaf[len(sessionLeaf):]):
				return Where{Kind: WhereSession, Label: leaf[len(sessionLeaf):]}
			case strings.HasPrefix(leaf, pluginLeaf):
				return Where{Kind: WherePlugin, Label: leaf[len(pluginLeaf):]}
			}
			return Where{Kind: WhereOther}
		}
	}
	if self != "" && path.Clean(rel) == path.Clean(self) && strings.HasSuffix(parts[len(parts)-1], ".service") {
		return Where{Kind: WherePanel}
	}
	last := parts[len(parts)-1]
	for i, p := range parts {
		if strings.HasPrefix(p, "user@") && strings.HasSuffix(p, ".service") {
			// An app the person started: the deepest unit under their manager,
			// or the manager itself for what systemd runs for them.
			label := p
			for _, q := range parts[i+1:] {
				if strings.HasSuffix(q, ".scope") || strings.HasSuffix(q, ".service") {
					label = q
				}
			}
			return Where{Kind: WhereUser, Label: label}
		}
	}
	if len(parts) >= 1 && parts[0] == "user.slice" && strings.HasPrefix(last, "session-") && strings.HasSuffix(last, ".scope") {
		return Where{Kind: WhereUser, Label: last}
	}
	for _, p := range parts {
		if strings.HasSuffix(p, ".service") || strings.HasSuffix(p, ".scope") {
			return Where{Kind: WhereUnit, Label: p}
		}
	}
	return Where{Kind: WhereOther}
}

// parseStatus reads VmSwap (bytes) and the real uid from /proc/<pid>/status.
// A kernel thread has no VmSwap line, which is zero here.
func parseStatus(s string) (swap uint64, uid int, ok bool) {
	uid = -1
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(line, "VmSwap:"):
			f := strings.Fields(line[len("VmSwap:"):])
			if len(f) >= 1 {
				if n, err := strconv.ParseUint(f[0], 10, 64); err == nil {
					swap = n * 1024
				}
			}
		case strings.HasPrefix(line, "Uid:"):
			f := strings.Fields(line[len("Uid:"):])
			if len(f) >= 1 {
				if n, err := strconv.Atoi(f[0]); err == nil {
					uid = n
				}
			}
		}
	}
	return swap, uid, uid >= 0
}

// machineReader is what ranking reads, so a test hands it a world.
type machineReader struct {
	table  func() map[int]sysmon.Proc
	status func(pid int) (swap uint64, uid int, ok bool)
	cgroup func(pid int) (string, error)
	cmd    func(pid int) string
	user   func(uid int) string
	// startedAt turns a start time in clock ticks into a unix time.
	startedAt func(ticks uint64) int64
	self      string
	selfPID   int
}

func osMachineReader() machineReader {
	names := map[int]string{}
	up, upOK := uptimeTicks()
	now := time.Now().Unix()
	self, _ := cgroup.Of(0)
	return machineReader{
		table: sysmon.ProcTable,
		status: func(pid int) (uint64, int, bool) {
			b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
			if err != nil {
				return 0, 0, false
			}
			return parseStatus(string(b))
		},
		cgroup: cgroup.Of,
		cmd:    sysmon.CommandOf,
		user: func(uid int) string {
			if n, ok := names[uid]; ok {
				return n
			}
			n := strconv.Itoa(uid)
			if u, err := user.LookupId(n); err == nil {
				n = u.Username
			}
			names[uid] = n
			return n
		},
		startedAt: func(ticks uint64) int64 {
			if !upOK || ticks > up {
				return 0
			}
			return now - int64((up-ticks)/sysmon.ClockTicks)
		},
		self:    self,
		selfPID: os.Getpid(),
	}
}

// rankMachine lists the n processes with the most memory in RAM and swap
// together.
//
// The table is stat-level, and the status file is read for every process
// that could be a user's: a process mostly in swap has a small RSS and would
// never reach the top by it. Kernel threads are skipped by parent, and have no
// memory anyway. cgroup, command line and user are read for the n kept only.
// A process that exits while it is read is skipped.
func rankMachine(rd machineReader, ro roster, n int) []MachineProc {
	table := rd.table()
	type cand struct {
		p    sysmon.Proc
		swap uint64
		uid  int
	}
	cands := make([]cand, 0, len(table))
	for pid, p := range table {
		if pid == 2 || p.PPID == 2 {
			continue
		}
		swap, uid, ok := rd.status(pid)
		if !ok {
			continue
		}
		if p.RSS+swap == 0 {
			continue
		}
		cands = append(cands, cand{p, swap, uid})
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i].p.RSS+cands[i].swap, cands[j].p.RSS+cands[j].swap
		if a != b {
			return a > b
		}
		return cands[i].p.PID < cands[j].p.PID
	})
	paneOf := map[int]string{}
	for _, pn := range ro.list {
		if pn.PID > 0 {
			paneOf[pn.PID] = pn.TmuxName
		}
	}
	byName := map[string]SessionMeta{}
	for _, m := range ro.metas {
		byName[m.TmuxName] = m
	}
	out := make([]MachineProc, 0, n)
	for _, c := range cands {
		if len(out) == n {
			break
		}
		pid := c.p.PID
		rel, err := rd.cgroup(pid)
		if err != nil {
			// Gone between the table and now; the next candidate takes the place.
			continue
		}
		w := attribute(rel, rd.self)
		if pid == rd.selfPID {
			w = Where{Kind: WherePanel}
		}
		if w.Kind != WhereSession && w.Kind != WherePanel && w.Kind != WhereTmux {
			// Without isolation nothing is in a leaf; the pane's tree is the
			// only thing that says a process is a session's.
			if name := ancestorSession(pid, table, paneOf); name != "" {
				w = Where{Kind: WhereSession, Label: name}
			}
		}
		if w.Kind == WhereSession {
			m, ok := byName[w.Label]
			if !ok {
				// A leaf for a session the roster does not have: nothing a
				// button could reach.
				w = Where{Kind: WhereOther, Label: w.Label}
			} else {
				w = Where{Kind: WhereSession, Label: w.Label, SessionID: m.ID, SessionTitle: m.Title, ProjectName: m.ProjectName}
			}
		}
		cmd := rd.cmd(pid)
		if cmd == "" && w.Kind == WhereOther && c.p.PPID == 2 {
			w.Kind = WhereKernel
		}
		out = append(out, MachineProc{
			PID: pid, Start: c.p.Start, Name: c.p.Comm, Cmd: cmd,
			RSS: c.p.RSS, Swap: c.swap, User: rd.user(c.uid),
			StartedAt: rd.startedAt(c.p.Start), Where: w,
		})
	}
	return out
}

// ancestorSession is the session whose pane process pid descends from.
func ancestorSession(pid int, table map[int]sysmon.Proc, paneOf map[int]string) string {
	for i := 0; i < 64 && pid > 1; i++ {
		if name, ok := paneOf[pid]; ok {
			return name
		}
		p, ok := table[pid]
		if !ok {
			return ""
		}
		pid = p.PPID
	}
	return ""
}

// machine is the cached list.
type machineCache struct {
	mu   sync.Mutex
	at   time.Time
	list []MachineProc
}

func (g *Governor) machine(ro roster) []MachineProc {
	g.mc.mu.Lock()
	defer g.mc.mu.Unlock()
	if g.mc.list != nil && time.Since(g.mc.at) < machineTTL {
		return g.mc.list
	}
	var list []MachineProc
	if g.supported {
		list = rankMachine(osMachineReader(), ro, machineProcs)
	}
	if list == nil {
		list = []MachineProc{}
	}
	g.mc.list, g.mc.at = list, time.Now()
	return list
}
