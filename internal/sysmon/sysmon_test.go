package sysmon

import (
	"os"
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
