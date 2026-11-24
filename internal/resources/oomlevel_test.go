package resources

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
)

// A process that has raised its own score, the way Chrome does for every
// renderer, started for real: what is being tested is whether the kernel lets
// the panel write the number back, and a mock would say yes.
func selfRaised(t *testing.T, to int) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "echo "+strconv.Itoa(to)+" > /proc/self/oom_score_adj; exec sleep 60")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start a process:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if readScore(t, cmd.Process.Pid) == to {
			return cmd.Process.Pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Skip("the process could not raise its own score here")
	return 0
}

func readScore(t *testing.T, pid int) int {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/oom_score_adj")
	if err != nil {
		t.Skip("no oom_score_adj on this system:", err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// poolOf lays out a pool as the kernel would list it: a directory per leaf
// with the pids in cgroup.procs.
func poolOf(t *testing.T, leaves map[string]int) Layout {
	t.Helper()
	root := t.TempDir()
	for leaf, pid := range leaves {
		dir := filepath.Join(root, leaf)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Layout{Scope: cgroup.Dir(root), Pool: cgroup.Dir(root)}
}

func TestAHeadStartInASessionIsTakenBack(t *testing.T) {
	base := oomBase()
	renderer := selfRaised(t, base+300)
	l := poolOf(t, map[string]int{sessionLeaf + "vp_a": renderer})

	if n := l.LevelOOMScores(base); n != 1 {
		t.Fatalf("lowered %d processes, want the one", n)
	}
	if got := readScore(t, renderer); got != base {
		t.Fatalf("score is %d after levelling, want %d: the kernel still takes this before anything larger", got, base)
	}
	if n := l.LevelOOMScores(base); n != 0 {
		t.Fatalf("a second pass lowered %d, and there was nothing left to lower", n)
	}
}

func TestTheServerAndThePluginsKeepTheirScores(t *testing.T) {
	base := oomBase()
	server := selfRaised(t, base+200)
	plugin := selfRaised(t, base+200)
	stray := selfRaised(t, base+200)
	l := poolOf(t, map[string]int{tmuxLeaf: server, pluginLeaf + "x": plugin, otherLeaf: stray})

	l.LevelOOMScores(base)
	if got := readScore(t, server); got != base+200 {
		t.Errorf("the tmux leaf was visited: %d", got)
	}
	if got := readScore(t, plugin); got != base+200 {
		t.Errorf("a plugin's leaf was visited: %d", got)
	}
	if got := readScore(t, stray); got != base {
		t.Errorf("a process that left its session's tree kept its head start: %d", got)
	}
}

func TestAScoreAtOrBelowTheBaseIsLeftAlone(t *testing.T) {
	base := oomBase()
	p := selfRaised(t, base+50)
	l := poolOf(t, map[string]int{sessionLeaf + "vp_a": p})
	// A base above the process's score: nothing is raised here. Raising is
	// raiseOOMScore's job, on placement, and only from below zero.
	if n := l.LevelOOMScores(base + 100); n != 0 || readScore(t, p) != base+50 {
		t.Fatalf("lowered %d, score %d", n, readScore(t, p))
	}
}

func TestTheTickLevelsScoresAndNotOnEveryTick(t *testing.T) {
	base := oomBase()
	p := selfRaised(t, base+300)
	l := poolOf(t, map[string]int{sessionLeaf + "vp_a": p})
	g := &Governor{}
	now := time.Now()

	g.levelScores(nil, now) // no scope: nothing to read, and no time recorded
	g.levelScores(&l, now)
	if got := readScore(t, p); got != base {
		t.Fatalf("the first tick with a scope left the score at %d", got)
	}
	// Raised again, as the next renderer would be.
	if err := os.WriteFile("/proc/"+strconv.Itoa(p)+"/oom_score_adj", []byte(strconv.Itoa(base+300)), 0); err != nil {
		t.Skip("cannot raise the score again:", err)
	}
	g.levelScores(&l, now.Add(oomLevelEvery/2))
	if got := readScore(t, p); got != base+300 {
		t.Fatalf("read the pool again after %v: %d", oomLevelEvery/2, got)
	}
	g.levelScores(&l, now.Add(oomLevelEvery))
	if got := readScore(t, p); got != base {
		t.Fatalf("not levelled once the interval had passed: %d", got)
	}
}
