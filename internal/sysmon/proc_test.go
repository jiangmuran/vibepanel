package sysmon

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// A process may be called anything. Chromium's renderers carry spaces; a
// program can be called "(foo) bar)" on purpose. Splitting the whole line on
// whitespace shifts every field after the name, and the numbers that come out
// are still numbers -- so this fails silently and reports a plausible ppid
// belonging to nobody.
func TestParseStatSurvivesAProcessNamedAnything(t *testing.T) {
	pageSize := uint64(os.Getpagesize())
	// pid comm state ppid pgrp session tty tpgid flags minflt cminflt majflt
	// cmajflt utime stime cutime cstime prio nice threads itrealvalue starttime
	// vsize rss
	tail := " S 4321 1 1 0 -1 0 0 0 0 0 70 30 0 0 20 0 3 0 900 12345 64" +
		" 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"

	for _, comm := range []string{"(bash)", "(a b c)", "(weird) name)", "(())"} {
		st, ok := parseStat("1234 " + comm + tail)
		if !ok {
			t.Fatalf("%s: refused a well-formed line", comm)
		}
		if st.ppid != 4321 {
			t.Errorf("%s: ppid = %d, want 4321 -- the comm field shifted the parse", comm, st.ppid)
		}
		if st.ticks != 100 {
			t.Errorf("%s: ticks = %d, want 100 (utime 70 + stime 30)", comm, st.ticks)
		}
		if st.rss != 64*pageSize {
			t.Errorf("%s: rss = %d, want %d", comm, st.rss, 64*pageSize)
		}
	}
}

func TestParseStatRefusesWhatItCannotRead(t *testing.T) {
	for _, line := range []string{"", "1234 (bash", "1234 (bash) S 1", "nonsense"} {
		if _, ok := parseStat(line); ok {
			t.Errorf("accepted %q", line)
		}
	}
}

// /proc is read without a lock, so a process can be reparented between reading
// its stat and reading its children's. The ppid graph assembled from two
// different instants can then contain a cycle that the kernel's real tree
// never had. Without the visited set this walk does not return.
func TestWalkTerminatesOnAReparentingRace(t *testing.T) {
	stats := map[int]procStat{
		10: {ppid: 11, ticks: 1},
		11: {ppid: 10, ticks: 2},
		12: {ppid: 10, ticks: 4},
	}
	children := map[int][]int{10: {11, 12}, 11: {10}}

	done := make(chan uint64, 1)
	go func() {
		var total uint64
		walk(10, stats, children, func(st procStat) { total += st.ticks })
		done <- total
	}()

	select {
	case total := <-done:
		if total != 7 {
			t.Errorf("ticks = %d, want 7 -- each process counted exactly once", total)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("walk did not terminate on a cycle in the ppid graph")
	}
}

// The number that matters is the tree's, not the pane process's own: the pane
// runs a shell, the shell runs the agent, and the agent is where the CPU goes.
func TestSampleCountsTheWholeTreeUnderThePane(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc here")
	}

	// A shell that does nothing but hold a child, which is the shape of a real
	// pane: sh -> agent.
	parent := exec.Command("sh", "-c", "sleep 30 & wait")
	if err := parent.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		_, _ = parent.Process.Wait()
	})
	// Give the shell time to fork.
	deadline := time.Now().Add(5 * time.Second)
	var ts TreeSampler
	var got Usage
	for time.Now().Before(deadline) {
		u := ts.Sample(map[string]int{"s1": parent.Process.Pid})
		if u["s1"].Procs >= 2 {
			got = u["s1"]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.Procs < 2 {
		t.Fatalf("procs = %d, want at least 2 (the shell and its child)", got.Procs)
	}
	if got.RSS == 0 {
		t.Error("rss = 0 for two live processes")
	}
}

// Zero is a real reading and "the session is gone" is not. Reporting a dead
// pane as 0% and 0 bytes puts a row on screen that looks like an idle session.
func TestSampleOmitsAPidThatIsGone(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc here")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	dead := cmd.Process.Pid

	var ts TreeSampler
	got := ts.Sample(map[string]int{"alive": os.Getpid(), "dead": dead})
	if _, ok := got["dead"]; ok {
		t.Errorf("reported pid %d, which has exited", dead)
	}
	if _, ok := got["alive"]; !ok {
		t.Error("dropped the live session as well")
	}
}

// The first sample has nothing to difference against, and busy work between
// two samples has to show up as more than zero.
func TestCPUPercentNeedsTwoSamples(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc here")
	}
	spin := exec.Command("sh", "-c", "while :; do :; done")
	if err := spin.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = spin.Process.Kill()
		_, _ = spin.Process.Wait()
	})
	pane := map[string]int{"s1": spin.Process.Pid}

	var ts TreeSampler
	if first := ts.Sample(pane)["s1"]; first.CPUPercent != 0 {
		t.Errorf("first sample reported %.2f%%, with nothing to compare against", first.CPUPercent)
	}
	// Long enough to be past minCPUWindow and to accumulate ticks: USER_HZ is
	// 100, so a shorter window can round a busy process down to nothing.
	time.Sleep(1200 * time.Millisecond)
	second := ts.Sample(pane)["s1"]
	if second.CPUPercent <= 0 {
		t.Errorf("a spinning process measured %.2f%%", second.CPUPercent)
	}
	if second.CPUPercent > 100 {
		t.Errorf("%.2f%% of the machine", second.CPUPercent)
	}
}

// Two viewers landing a few milliseconds apart must not consume each other's
// measuring window -- a percentage over five milliseconds reads 0 or 100
// depending on where the sample fell.
func TestASecondCallerTooSoonGetsTheSameAnswer(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc here")
	}
	pane := map[string]int{"self": os.Getpid()}
	var ts TreeSampler
	ts.Sample(pane)
	time.Sleep(600 * time.Millisecond)
	a := ts.Sample(pane)["self"]
	b := ts.Sample(pane)["self"]
	if a.CPUPercent != b.CPUPercent {
		t.Errorf("%.4f then %.4f within the window", a.CPUPercent, b.CPUPercent)
	}
}

func TestReadProcTableSeesThisProcess(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc here")
	}
	table := readProcTable()
	if _, ok := table[os.Getpid()]; !ok {
		t.Fatalf("pid %s missing from a table of %d", strconv.Itoa(os.Getpid()), len(table))
	}
}
