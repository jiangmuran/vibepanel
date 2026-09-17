package sysmon

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestSampleReadsTheMachine(t *testing.T) {
	if _, err := os.Stat("/proc/stat"); err != nil {
		t.Skip("no /proc")
	}
	s := &Sampler{DiskPath: os.TempDir()}

	// The first sample has no previous counters to difference against, so CPU
	// is deliberately absent rather than a made-up zero.
	first := s.Sample()
	if first.CPUPercent != nil {
		t.Errorf("first sample reported CPU %v, want nil", *first.CPUPercent)
	}
	if first.NetRxRate != nil || first.NetTxRate != nil {
		t.Errorf("first sample reported a network rate, want nil until there is a second reading")
	}
	if first.Cores < 1 {
		t.Errorf("cores = %d", first.Cores)
	}
	if first.MemTotal == 0 {
		t.Error("MemTotal = 0")
	}
	if first.MemAvailable == 0 || first.MemAvailable > first.MemTotal {
		t.Errorf("MemAvailable = %d against a total of %d", first.MemAvailable, first.MemTotal)
	}
	if first.DiskTotal == 0 || first.DiskFree > first.DiskTotal {
		t.Errorf("disk: free %d of total %d", first.DiskFree, first.DiskTotal)
	}
	if first.Uptime <= 0 {
		t.Errorf("uptime = %d", first.Uptime)
	}

	// Jiffies advance on a timer tick, so two samples taken microseconds apart
	// have identical counters and nothing to difference. Real sampling is
	// seconds apart; the test just has to wait for the clock to move.
	var second Sample
	deadline := time.Now().Add(3 * time.Second)
	for {
		time.Sleep(40 * time.Millisecond)
		second = s.Sample()
		if second.CPUPercent != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no CPU reading after three seconds of sampling")
		}
	}
	if *second.CPUPercent < 0 || *second.CPUPercent > 100 {
		t.Errorf("CPU = %v, want 0-100", *second.CPUPercent)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{
		0:                      "0 B",
		512:                    "512 B",
		1024:                   "1.0 KiB",
		1536:                   "1.5 KiB",
		1024 * 1024:            "1.0 MiB",
		3 * 1024 * 1024 * 1024: "3.0 GiB",
	}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestReadNetExcludesLoopback(t *testing.T) {
	if _, err := os.Stat("/proc/net/dev"); err != nil {
		t.Skip("no /proc/net/dev")
	}
	// Loopback carries whatever this test process and everything else on the
	// box happens to send itself, which is not "network traffic" in the sense
	// anyone glancing at the monitor means -- it would make a machine with SSH
	// open to itself look busy for no outside reason.
	rx, tx, ok := readNet()
	if !ok {
		t.Fatal("readNet reported not ok on a machine with /proc/net/dev")
	}
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "lo:") {
		// Loopback's own counters grow with everything this test suite does
		// over localhost; the only thing pinned here is that summing does not
		// panic and produces something no smaller than zero and no larger
		// than a sane bound, which catches a column-index mistake that reads
		// packets or errors as bytes.
		if rx > 1<<50 || tx > 1<<50 {
			t.Errorf("rx=%d tx=%d looks like the wrong column, not a byte count", rx, tx)
		}
	}
}

func TestNetReadableSaysWhetherThereIsAnythingToSample(t *testing.T) {
	s := &Sampler{DiskPath: t.TempDir()}
	got := s.Sample()
	if _, _, ok := readNet(); ok != got.NetReadable {
		t.Errorf("NetReadable = %v but readNet reports %v", got.NetReadable, ok)
	}
}

// Two viewers landing a few milliseconds apart must not consume each other's
// window, exactly like the CPU rate above them.
func TestASecondNetworkCallerTooSoonGetsTheSameAnswer(t *testing.T) {
	if _, err := os.Stat("/proc/net/dev"); err != nil {
		t.Skip("no /proc/net/dev")
	}
	s := &Sampler{DiskPath: t.TempDir()}
	s.Sample()
	time.Sleep(600 * time.Millisecond)
	a := s.Sample()
	b := s.Sample()
	if (a.NetRxRate == nil) != (b.NetRxRate == nil) {
		t.Fatalf("nil-ness changed between calls inside the window: %v then %v", a.NetRxRate, b.NetRxRate)
	}
	if a.NetRxRate != nil && *a.NetRxRate != *b.NetRxRate {
		t.Errorf("rx rate %.2f then %.2f within the window", *a.NetRxRate, *b.NetRxRate)
	}
	if a.NetTxRate != nil && *a.NetTxRate != *b.NetTxRate {
		t.Errorf("tx rate %.2f then %.2f within the window", *a.NetTxRate, *b.NetTxRate)
	}
}

func TestCPUReadableSaysWhetherThereIsAnythingToSample(t *testing.T) {
	// A nil CPUPercent means two different things and the panel could only see
	// one of them: "no sample yet, one is coming" and "there is nothing to
	// sample on this machine". /proc/stat is Linux's, and build-release.sh
	// ships a darwin/arm64 binary, so the second case rendered
	// "8 cores · sampling…" on every Mac — a promise that renewed itself every
	// two seconds and was never going to be kept.
	//
	// On this machine the counters exist, so what is pinned here is that the
	// flag tracks them rather than being hardcoded: readCPU succeeding and
	// CPUReadable being false would be the same silence as before.
	s := &Sampler{DiskPath: t.TempDir()}
	got := s.Sample()
	if _, _, ok := readCPU(); ok != got.CPUReadable {
		t.Errorf("CPUReadable = %v but readCPU reports %v; the panel cannot tell "+
			"'no sample yet' from 'nothing to sample here'", got.CPUReadable, ok)
	}
	if got.Cores <= 0 {
		t.Errorf("cores = %d; runtime.NumCPU works on every platform and is the one "+
			"number the monitor can always show", got.Cores)
	}
}
