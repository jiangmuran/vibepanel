// Package sysmon samples what the machine is doing.
//
// Reads /proc directly rather than taking a dependency. The three or four
// numbers a person glances at while watching a build do not justify pulling in
// a cross-platform metrics library, and /proc is a stable interface.
package sysmon

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Sample is one reading.
type Sample struct {
	At int64 `json:"at"`

	// CPUReadable says whether the counters exist at all here.
	//
	// A nil CPUPercent means two different things and the panel showed one of
	// them: "no sample yet, one is coming" and "there is nothing to sample on
	// this machine". /proc/stat is Linux's, and this ships a darwin/arm64
	// build, so the second case renders "sampling…" forever on every Mac.
	CPUReadable bool `json:"cpuReadable"`

	// CPUPercent is usage across all cores since the previous sample, 0–100.
	// Nil on the first sample, when there is nothing to compare against.
	CPUPercent *float64 `json:"cpuPercent"`
	Cores      int      `json:"cores"`

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	MemTotal     uint64 `json:"memTotal"`
	MemAvailable uint64 `json:"memAvailable"`
	SwapTotal    uint64 `json:"swapTotal"`
	SwapFree     uint64 `json:"swapFree"`

	DiskTotal uint64 `json:"diskTotal"`
	DiskFree  uint64 `json:"diskFree"`
	DiskPath  string `json:"diskPath"`

	Uptime int64 `json:"uptime"`
}

// Sampler produces readings, keeping the previous CPU counters so that usage
// can be expressed as a percentage over the interval rather than since boot.
type Sampler struct {
	// DiskPath is the filesystem reported. Defaults to the data directory,
	// which is the one that filling up would actually break the panel.
	DiskPath string

	mu       sync.Mutex
	prevIdle uint64
	prevAll  uint64
	prevAt   time.Time
	lastPct  *float64
	haveprev bool
}

// minCPUWindow is the shortest interval a CPU percentage may be computed over.
//
// The previous counters live here, on the server, shared by every caller — so
// the window is not "since this viewer last asked" but "since anybody last
// asked". Two browsers with the monitor open land a few milliseconds apart
// often enough, and a percentage measured across five milliseconds is noise:
// it reads 0 or 100 depending on where the sample fell. The panel is built to
// be open in several places at once, so this is the ordinary case rather than
// a corner of it.
//
// Below the threshold the previous answer is repeated and the counters are
// left alone, so the next caller still gets a window worth measuring.
const minCPUWindow = 500 * time.Millisecond

// Sample takes a reading. Individual sources failing are tolerated: a missing
// /proc/swaps on some container is not a reason to show nothing at all.
func (s *Sampler) Sample() Sample {
	out := Sample{At: time.Now().Unix(), Cores: runtime.NumCPU(), DiskPath: s.DiskPath}

	if idle, all, ok := readCPU(); ok {
		out.CPUReadable = true
		now := time.Now()
		s.mu.Lock()
		switch {
		case s.haveprev && now.Sub(s.prevAt) < minCPUWindow:
			// Too soon to measure anything. Repeat the last answer and leave the
			// counters where they are, so whoever asks next still has a window.
			// Copied rather than shared: two responses holding one pointer into
			// this struct is a shape that invites trouble later for no gain.
			if s.lastPct != nil {
				v := *s.lastPct
				out.CPUPercent = &v
			}
		case s.haveprev && all > s.prevAll:
			totalDelta := float64(all - s.prevAll)
			idleDelta := float64(idle - s.prevIdle)
			pct := (1 - idleDelta/totalDelta) * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			out.CPUPercent = &pct
			s.lastPct = &pct
			s.prevIdle, s.prevAll, s.prevAt, s.haveprev = idle, all, now, true
		default:
			// First sample, or the counters went backwards. Nothing to report
			// yet; start the window here.
			s.prevIdle, s.prevAll, s.prevAt, s.haveprev = idle, all, now, true
		}
		s.mu.Unlock()
	}

	out.Load1, out.Load5, out.Load15 = readLoad()
	out.MemTotal, out.MemAvailable, out.SwapTotal, out.SwapFree = readMem()
	out.Uptime = readUptime()

	if s.DiskPath != "" {
		var st syscall.Statfs_t
		if err := syscall.Statfs(s.DiskPath, &st); err == nil {
			out.DiskTotal = st.Blocks * uint64(st.Bsize)
			// Bavail, not Bfree: Bfree includes the reserved blocks only root
			// can use, which overstates what is actually available.
			out.DiskFree = st.Bavail * uint64(st.Bsize)
		}
	}
	return out
}

// readCPU returns cumulative idle and total jiffies from /proc/stat.
func readCPU() (idle, all uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, v := range fields[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		all += n
		// Columns 4 and 5 are idle and iowait. Time spent waiting on disk is
		// not the CPU working, and counting it as busy makes a machine doing
		// nothing but reading files look pegged.
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return idle, all, true
}

func readLoad() (l1, l5, l15 float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0
	}
	l1, _ = strconv.ParseFloat(f[0], 64)
	l5, _ = strconv.ParseFloat(f[1], 64)
	l15, _ = strconv.ParseFloat(f[2], 64)
	return
}

func readMem() (total, available, swapTotal, swapFree uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		n *= 1024 // /proc/meminfo is in kB
		switch key {
		case "MemTotal":
			total = n
		case "MemAvailable":
			// MemAvailable, not MemFree: free memory on a healthy Linux box is
			// near zero because the rest is cache, and reporting that as
			// "almost out of memory" is alarming and wrong.
			available = n
		case "SwapTotal":
			swapTotal = n
		case "SwapFree":
			swapFree = n
		}
	}
	return
}

func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return int64(secs)
}

// FormatBytes renders a size the way a person reads it. Exported because the
// CLI's doctor output wants the same rendering as the panel.
func FormatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
