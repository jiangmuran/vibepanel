package resources

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// Real cgroup v2 paths, as /proc/<pid>/cgroup prints them on systemd hosts.
func TestAttributeReadsWhereAProcessLivesFromItsCgroup(t *testing.T) {
	const id = "3f4c2a9d8b7e6f5a4c3b2a1908f7e6d5c4b3a291807f6e5d4c3b2a1908f7e6d5"
	self := "/user.slice/user-1000.slice/user@1000.service/app.slice/vibepanel.service"
	for _, tc := range []struct {
		name, rel string
		want      Where
	}{
		{"docker", "/system.slice/docker-" + id + ".scope", Where{Kind: WhereContainer, Label: id[:12]}},
		{"docker, nested", "/system.slice/docker-" + id + ".scope/init", Where{Kind: WhereContainer, Label: id[:12]}},
		{"kubernetes", "/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod1a2b.slice/cri-containerd-" + id + ".scope",
			Where{Kind: WhereContainer, Label: id[:12]}},
		{"podman", "/user.slice/user-1000.slice/user@1000.service/user.slice/libpod-" + id + ".scope/container",
			Where{Kind: WhereContainer, Label: id[:12]}},
		{"crio", "/kubepods.slice/crio-" + id + ".scope", Where{Kind: WhereContainer, Label: id[:12]}},
		{"a session", "/user.slice/user-1000.slice/user@1000.service/app.slice/vibepanel-sessions.scope/pool/s-vp_ab12",
			Where{Kind: WhereSession, Label: "vp_ab12"}},
		{"a session, system unit", "/system.slice/vibepanel-sessions-1000.scope/pool/s-vp_ab12",
			Where{Kind: WhereSession, Label: "vp_ab12"}},
		{"a session of another socket", "/user.slice/user-1000.slice/user@1000.service/app.slice/vibepanel-sessions-wall.scope/pool/s-vp_cd",
			Where{Kind: WhereSession, Label: "vp_cd"}},
		{"the tmux server", "/system.slice/vibepanel-sessions-1000.scope/pool/tmux", Where{Kind: WhereTmux}},
		{"unsorted", "/system.slice/vibepanel-sessions-1000.scope/pool/other", Where{Kind: WhereOther}},
		{"a plugin", "/system.slice/vibepanel-sessions-1000.scope/pool/p-weather", Where{Kind: WherePlugin, Label: "weather"}},
		{"a leaf name that could climb", "/system.slice/vibepanel-sessions-1000.scope/pool/s-..%", Where{Kind: WhereOther}},
		{"the panel", self, Where{Kind: WherePanel}},
		{"a service", "/system.slice/ssh.service", Where{Kind: WhereUnit, Label: "ssh.service"}},
		{"a service's sub-cgroup", "/system.slice/containerd.service/kubepods", Where{Kind: WhereUnit, Label: "containerd.service"}},
		{"init", "/init.scope", Where{Kind: WhereUnit, Label: "init.scope"}},
		{"a login session", "/user.slice/user-1000.slice/session-3.scope", Where{Kind: WhereUser, Label: "session-3.scope"}},
		{"a desktop app", "/user.slice/user-1000.slice/user@1000.service/app.slice/app-gnome-firefox-4821.scope",
			Where{Kind: WhereUser, Label: "app-gnome-firefox-4821.scope"}},
		{"the user manager's own", "/user.slice/user-1000.slice/user@1000.service/init.scope",
			Where{Kind: WhereUser, Label: "init.scope"}},
		{"a user service", "/user.slice/user-1000.slice/user@1000.service/app.slice/syncthing.service",
			Where{Kind: WhereUser, Label: "syncthing.service"}},
		{"the root", "/", Where{Kind: WhereKernel}},
		{"something with no unit", "/mystery", Where{Kind: WhereOther}},
	} {
		if got := attribute(tc.rel, self); got != tc.want {
			t.Errorf("%s: %s = %+v, want %+v", tc.name, tc.rel, got, tc.want)
		}
	}
	// The panel started from a terminal shares its cgroup with the login
	// session; calling all of that "the panel" would mislabel everything in it.
	login := "/user.slice/user-1000.slice/session-3.scope"
	if got := attribute(login, login); got.Kind != WhereUser {
		t.Errorf("a panel run by hand labelled its whole login session as the panel: %+v", got)
	}
}

func TestParseStatusReadsSwapAndTheRealUID(t *testing.T) {
	swap, uid, ok := parseStatus("Name:\tqemu\nUid:\t1001\t1001\t1001\t1001\nVmRSS:\t  4096 kB\nVmSwap:\t  2048 kB\n")
	if !ok || uid != 1001 || swap != 2048*1024 {
		t.Fatalf("swap %d uid %d ok %v", swap, uid, ok)
	}
	// A kernel thread has no VmSwap; that is zero, and it is still a reading.
	if swap, uid, ok := parseStatus("Name:\tkworker\nUid:\t0\t0\t0\t0\n"); !ok || uid != 0 || swap != 0 {
		t.Fatalf("kernel thread: %d %d %v", swap, uid, ok)
	}
	if _, _, ok := parseStatus(""); ok {
		t.Fatal("an empty status read as a process")
	}
}

type fakeProc struct {
	p    sysmon.Proc
	swap uint64
	uid  int
	rel  string
	cmd  string
	gone bool // vanishes between the table and the cgroup read
}

func fakeReader(procs ...fakeProc) machineReader {
	by := map[int]fakeProc{}
	for _, f := range procs {
		by[f.p.PID] = f
	}
	return machineReader{
		table: func() map[int]sysmon.Proc {
			out := map[int]sysmon.Proc{}
			for pid, f := range by {
				out[pid] = f.p
			}
			return out
		},
		status: func(pid int) (uint64, int, bool) { return by[pid].swap, by[pid].uid, true },
		cgroup: func(pid int) (string, error) {
			if by[pid].gone {
				return "", errors.New("gone")
			}
			return by[pid].rel, nil
		},
		cmd:       func(pid int) string { return by[pid].cmd },
		user:      func(uid int) string { return map[int]string{0: "root", 1000: "me"}[uid] },
		startedAt: func(ticks uint64) int64 { return 1000 + int64(ticks) },
		self:      "/system.slice/vibepanel.service",
		selfPID:   99,
	}
}

func TestTheMachineListIsRankedByWhatAProcessHoldsInMemoryAndSwap(t *testing.T) {
	const gib = 1 << 30
	pr := func(pid, ppid int, comm string, rss uint64) sysmon.Proc {
		return sysmon.Proc{PID: pid, PPID: ppid, Comm: comm, Start: uint64(pid), RSS: rss}
	}
	rd := fakeReader(
		fakeProc{p: pr(10, 1, "small", 1<<20), uid: 1000, rel: "/system.slice/a.service"},
		// Mostly in swap: the smallest RSS and the largest footprint.
		fakeProc{p: pr(11, 1, "qemu", 100<<20), swap: 6 * gib, uid: 1000, rel: "/system.slice/docker-" + "ab12cd34ef56ab12cd34.scope", cmd: "qemu-system-x86_64 -m 8G"},
		fakeProc{p: pr(12, 1, "node", 2*gib), uid: 0, rel: "/system.slice/vibepanel-sessions-1000.scope/pool/s-vp_one", cmd: "node server.js"},
		fakeProc{p: pr(13, 1, "vanishing", 9*gib), rel: "/x", gone: true},
		fakeProc{p: pr(99, 1, "vibepanel", 3*gib), rel: "/system.slice/elsewhere.service"},
		fakeProc{p: pr(14, 2, "kworker", 5*gib), rel: "/"},
		// Not in the leaf (no isolation), but under the pane of a session.
		fakeProc{p: pr(20, 1, "bash", 1<<20), uid: 1000, rel: "/user.slice/user-1000.slice/session-3.scope"},
		fakeProc{p: pr(21, 20, "cc1plus", 1*gib), uid: 1000, rel: "/user.slice/user-1000.slice/session-3.scope", cmd: "cc1plus big.cc"},
		fakeProc{p: pr(30, 1, "idle", 0), rel: "/"},
	)
	ro := roster{
		metas: []SessionMeta{{ID: "s1", TmuxName: "vp_one", Title: "build", ProjectName: "api"}, {ID: "s2", TmuxName: "vp_two", Title: "docs", ProjectName: "web"}},
		list:  []Pane{{TmuxName: "vp_two", PID: 20}},
	}
	got := rankMachine(rd, ro, 4)
	if len(got) != 4 {
		t.Fatalf("%d rows: %+v", len(got), got)
	}
	order := []int{11, 99, 12, 21}
	for i, pid := range order {
		if got[i].PID != pid {
			t.Fatalf("row %d is pid %d, want %d: %+v", i, got[i].PID, pid, got)
		}
	}
	if got[0].Swap != 6*gib || got[0].User != "me" || got[0].Where.Kind != WhereContainer || got[0].Where.Label != "ab12cd34ef56" || got[0].Cmd == "" {
		t.Errorf("the VM in swap: %+v", got[0])
	}
	if got[1].Where.Kind != WherePanel {
		t.Errorf("the panel's own pid was %+v", got[1].Where)
	}
	if w := got[2].Where; w.Kind != WhereSession || w.SessionID != "s1" || w.SessionTitle != "build" || w.ProjectName != "api" {
		t.Errorf("a session's process: %+v", w)
	}
	if w := got[3].Where; w.Kind != WhereSession || w.SessionID != "s2" || w.ProjectName != "web" {
		t.Errorf("a process under a pane, with no leaf: %+v", w)
	}
	if got[3].StartedAt != 1000+21 {
		t.Errorf("startedAt %d", got[3].StartedAt)
	}
	for _, m := range got {
		if m.PID == 13 || m.PID == 14 || m.PID == 30 {
			t.Errorf("listed pid %d: a vanished process, a kernel thread and one holding nothing are not rows", m.PID)
		}
	}
}

func TestALeafOfASessionTheRosterDoesNotHaveIsNotOfferedAsOne(t *testing.T) {
	rd := fakeReader(fakeProc{p: sysmon.Proc{PID: 5, PPID: 1, Comm: "x", RSS: 1 << 20}, rel: "/system.slice/vibepanel-sessions-1000.scope/pool/s-vp_old"})
	got := rankMachine(rd, roster{}, 3)
	if len(got) != 1 || got[0].Where.Kind != WhereOther || got[0].Where.SessionID != "" {
		t.Fatalf("%+v", got)
	}
}

// EndSession at the function boundary: no user manager here to build a scope,
// so the cgroup mount is a directory of plain files and the thing checked is
// which file is written.
func TestEndingASessionKillsItsOwnLeafAndNothingElse(t *testing.T) {
	root := t.TempDir()
	old := cgroup.Mount
	cgroup.Mount = root
	t.Cleanup(func() { cgroup.Mount = old })

	scope := cgroup.At("/scope")
	pool := scope.Child("pool")
	for _, leaf := range []string{"s-vp_a", "s-vp_ab", "tmux", "other"} {
		if err := os.MkdirAll(filepath.Join(string(pool), leaf), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(string(pool), leaf, "cgroup.kill"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A control file outside the pool that a name with dots could reach.
	if err := os.WriteFile(filepath.Join(string(scope), "cgroup.kill"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	g := New(Env{})
	g.layout = &Layout{Scope: scope, Pool: pool}

	g.EndSession("vp_a")
	g.EndSession("../cgroup.kill")
	g.EndSession("x/../..")

	read := func(p string) string {
		b, _ := os.ReadFile(p)
		return string(b)
	}
	if got := read(filepath.Join(string(pool), "s-vp_a", "cgroup.kill")); got != "1" {
		t.Errorf("the session's leaf was written %q", got)
	}
	for _, other := range []string{"s-vp_ab", "tmux", "other"} {
		if got := read(filepath.Join(string(pool), other, "cgroup.kill")); got != "" {
			t.Errorf("%s was written %q: a session's end reached a leaf that is not its own", other, got)
		}
	}
	if got := read(filepath.Join(string(scope), "cgroup.kill")); got != "" {
		t.Errorf("the scope itself was written %q", got)
	}
}

func TestLastUsedIsTheLaterOfInputAndOutput(t *testing.T) {
	typed := time.Unix(2000, 0)
	for _, tc := range []struct {
		m    SessionMeta
		want int64
	}{
		{SessionMeta{}, 0},
		{SessionMeta{LastOutput: 1500}, 1500},
		{SessionMeta{LastOutput: 1500, LastInput: typed}, 2000},
		{SessionMeta{LastOutput: 2500, LastInput: typed}, 2500},
	} {
		if got := lastUsed(tc.m); got != tc.want {
			t.Errorf("%+v = %d, want %d", tc.m, got, tc.want)
		}
	}
}
