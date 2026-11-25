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
	CPUPercent   *float64  `json:"cpuPercent"`
	CPUUser      *float64  `json:"cpuUser"`
	CPUSystem    *float64  `json:"cpuSystem"`
	CPUIowait    *float64  `json:"cpuIowait"`
	CPUSteal     *float64  `json:"cpuSteal"`
	CorePercents []float64 `json:"corePercents"`
	Cores        int       `json:"cores"`

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	MemTotal     uint64 `json:"memTotal"`
	MemAvailable uint64 `json:"memAvailable"`
	MemFree      uint64 `json:"memFree"`
	MemCached    uint64 `json:"memCached"`
	SwapTotal    uint64 `json:"swapTotal"`
	SwapFree     uint64 `json:"swapFree"`

	DiskTotal      uint64   `json:"diskTotal"`
	DiskFree       uint64   `json:"diskFree"`
	DiskPath       string   `json:"diskPath"`
	DiskIOReadable bool     `json:"diskIoReadable"`
	DiskReadRate   *float64 `json:"diskReadRate"`
	DiskWriteRate  *float64 `json:"diskWriteRate"`
	DiskReadBytes  uint64   `json:"diskReadBytes"`
	DiskWriteBytes uint64   `json:"diskWriteBytes"`

	// NetReadable is CPUReadable's story again: /proc/net/dev is Linux's, so a
	// darwin build has nothing to sample and must say so rather than render a
	// rate of zero, which is a real and very different reading.
	NetReadable bool `json:"netReadable"`
	// NetRxRate and NetTxRate are bytes per second across every interface
	// except loopback, since the previous sample. Nil on the first sample, and
	// on any sample too soon after it to measure -- see minCPUWindow.
	NetRxRate *float64 `json:"netRxRate"`
	NetTxRate *float64 `json:"netTxRate"`
	// NetRxBytes and NetTxBytes are the kernel's own running counters, so they
	// are since the interfaces came up rather than since this process started
	// watching them -- the same "survives a restart" property Uptime has.
	NetRxBytes uint64 `json:"netRxBytes"`
	NetTxBytes uint64 `json:"netTxBytes"`

	PSICPU *float64 `json:"psiCpu"`
	PSIMem *float64 `json:"psiMem"`
	PSIIO  *float64 `json:"psiIo"`

	Uptime int64 `json:"uptime"`
}

type cpuTimes struct {
	user, system, idle, iowait, steal, all uint64
}

// Sampler produces readings, keeping the previous CPU counters so that usage
// can be expressed as a percentage over the interval rather than since boot.
type Sampler struct {
	// DiskPath is the filesystem reported. Defaults to the data directory,
	// which is the one that filling up would actually break the panel.
	DiskPath string

	mu        sync.Mutex
	prevCPU   cpuTimes
	prevCores []cpuTimes
	prevAt    time.Time
	lastPct   *float64
	lastUser  *float64
	lastSys   *float64
	lastWait  *float64
	lastSteal *float64
	lastCores []float64
	haveprev  bool

	// Network counters keep their own window rather than sharing the CPU
	// one above. Both are read in the same Sample() call, but a second
	// caller inside minCPUWindow of the first must repeat both rates
	// unchanged, and giving the two their own state is what lets a future
	// reader delete one without reading the other's fields to be sure.
	prevRxBytes uint64
	prevTxBytes uint64
	prevNetAt   time.Time
	lastRxRate  *float64
	lastTxRate  *float64
	haveNetPrev bool

	prevDiskRead  uint64
	prevDiskWrite uint64
	prevDiskAt    time.Time
	lastDiskRead  *float64
	lastDiskWrite *float64
	haveDiskPrev  bool
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

func copyFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func clampPct(pct float64) float64 {
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// Sample takes a reading. Individual sources failing are tolerated: a missing
// /proc/swaps on some container is not a reason to show nothing at all.
func (s *Sampler) Sample() Sample {
	out := Sample{At: time.Now().Unix(), Cores: runtime.NumCPU(), DiskPath: s.DiskPath}

	if agg, cores, ok := readCPUDetail(); ok {
		out.CPUReadable = true
		now := time.Now()
		s.mu.Lock()
		switch {
		case s.haveprev && now.Sub(s.prevAt) < minCPUWindow:
			// Too soon to measure anything. Repeat the last answer and leave the
			// counters where they are, so whoever asks next still has a window.
			// Copied rather than shared: two responses holding one pointer into
			// this struct is a shape that invites trouble later for no gain.
			out.CPUPercent = copyFloat(s.lastPct)
			out.CPUUser = copyFloat(s.lastUser)
			out.CPUSystem = copyFloat(s.lastSys)
			out.CPUIowait = copyFloat(s.lastWait)
			out.CPUSteal = copyFloat(s.lastSteal)
			if len(s.lastCores) > 0 {
				out.CorePercents = append([]float64(nil), s.lastCores...)
			}
		case s.haveprev && agg.all > s.prevCPU.all:
			totalDelta := float64(agg.all - s.prevCPU.all)
			idlePrev := s.prevCPU.idle + s.prevCPU.iowait
			idleNow := agg.idle + agg.iowait
			var idleDelta uint64
			if idleNow >= idlePrev {
				idleDelta = idleNow - idlePrev
			}
			pct := clampPct((1 - float64(idleDelta)/totalDelta) * 100)
			delta := func(cur, prev uint64) *float64 {
				if cur < prev {
					v := 0.0
					return &v
				}
				v := clampPct(float64(cur-prev) / totalDelta * 100)
				return &v
			}
			uPct := delta(agg.user, s.prevCPU.user)
			sysPct := delta(agg.system, s.prevCPU.system)
			waitPct := delta(agg.iowait, s.prevCPU.iowait)
			stealPct := delta(agg.steal, s.prevCPU.steal)

			var perCore []float64
			if len(cores) > 0 && len(cores) == len(s.prevCores) {
				perCore = make([]float64, len(cores))
				for i, c := range cores {
					prev := s.prevCores[i]
					if c.all <= prev.all {
						continue
					}
					cTot := float64(c.all - prev.all)
					cIdleNow := c.idle + c.iowait
					cIdlePrev := prev.idle + prev.iowait
					var cIdle uint64
					if cIdleNow >= cIdlePrev {
						cIdle = cIdleNow - cIdlePrev
					}
					perCore[i] = clampPct((1 - float64(cIdle)/cTot) * 100)
				}
			}

			out.CPUPercent = copyFloat(&pct)
			out.CPUUser = copyFloat(uPct)
			out.CPUSystem = copyFloat(sysPct)
			out.CPUIowait = copyFloat(waitPct)
			out.CPUSteal = copyFloat(stealPct)
			if len(perCore) > 0 {
				out.CorePercents = append([]float64(nil), perCore...)
			}

			s.lastPct = &pct
			s.lastUser = uPct
			s.lastSys = sysPct
			s.lastWait = waitPct
			s.lastSteal = stealPct
			s.lastCores = perCore
			s.prevCPU, s.prevCores, s.prevAt, s.haveprev = agg, cores, now, true
		default:
			// First sample, or the counters went backwards. Nothing to report
			// yet; start the window here.
			s.prevCPU, s.prevCores, s.prevAt, s.haveprev = agg, cores, now, true
		}
		s.mu.Unlock()
	}

	if rx, tx, ok := readNet(); ok {
		out.NetReadable = true
		out.NetRxBytes, out.NetTxBytes = rx, tx
		now := time.Now()
		s.mu.Lock()
		switch {
		case s.haveNetPrev && now.Sub(s.prevNetAt) < minCPUWindow:
			// Same "too soon to measure" rule as the CPU window: repeat the
			// last answer rather than dividing by a handful of milliseconds.
			out.NetRxRate = copyFloat(s.lastRxRate)
			out.NetTxRate = copyFloat(s.lastTxRate)
		case s.haveNetPrev && (rx >= s.prevRxBytes) && (tx >= s.prevTxBytes):
			secs := now.Sub(s.prevNetAt).Seconds()
			rxRate := float64(rx-s.prevRxBytes) / secs
			txRate := float64(tx-s.prevTxBytes) / secs
			out.NetRxRate, out.NetTxRate = copyFloat(&rxRate), copyFloat(&txRate)
			s.lastRxRate, s.lastTxRate = &rxRate, &txRate
			s.prevRxBytes, s.prevTxBytes, s.prevNetAt, s.haveNetPrev = rx, tx, now, true
		default:
			// First sample, or a counter went backwards (an interface came
			// back up and reset). Nothing to report yet; start the window here.
			s.prevRxBytes, s.prevTxBytes, s.prevNetAt, s.haveNetPrev = rx, tx, now, true
		}
		s.mu.Unlock()
	}

	if rd, wr, ok := readDiskIO(); ok {
		out.DiskIOReadable = true
		out.DiskReadBytes, out.DiskWriteBytes = rd, wr
		now := time.Now()
		s.mu.Lock()
		switch {
		case s.haveDiskPrev && now.Sub(s.prevDiskAt) < minCPUWindow:
			out.DiskReadRate = copyFloat(s.lastDiskRead)
			out.DiskWriteRate = copyFloat(s.lastDiskWrite)
		case s.haveDiskPrev && rd >= s.prevDiskRead && wr >= s.prevDiskWrite:
			secs := now.Sub(s.prevDiskAt).Seconds()
			rRate := float64(rd-s.prevDiskRead) / secs
			wRate := float64(wr-s.prevDiskWrite) / secs
			out.DiskReadRate, out.DiskWriteRate = copyFloat(&rRate), copyFloat(&wRate)
			s.lastDiskRead, s.lastDiskWrite = &rRate, &wRate
			s.prevDiskRead, s.prevDiskWrite, s.prevDiskAt, s.haveDiskPrev = rd, wr, now, true
		default:
			s.prevDiskRead, s.prevDiskWrite, s.prevDiskAt, s.haveDiskPrev = rd, wr, now, true
		}
		s.mu.Unlock()
	}

	out.Load1, out.Load5, out.Load15 = readLoad()
	out.MemTotal, out.MemAvailable, out.MemFree, out.MemCached, out.SwapTotal, out.SwapFree = readMemDetail()
	out.PSICPU = readPSI("cpu")
	out.PSIMem = readPSI("memory")
	out.PSIIO = readPSI("io")
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

func parseCPUTimes(fields []string) (cpuTimes, bool) {
	if len(fields) < 5 {
		return cpuTimes{}, false
	}
	var t cpuTimes
	for i, v := range fields[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		t.all += n
		switch i {
		case 0, 1: // user, nice
			t.user += n
		case 2, 5, 6: // system, irq, softirq
			t.system += n
		case 3: // idle
			t.idle += n
		case 4: // iowait
			t.iowait += n
		case 7: // steal
			t.steal += n
		}
	}
	return t, true
}

func readCPUDetail() (agg cpuTimes, cores []cpuTimes, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, nil, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return cpuTimes{}, nil, false
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, nil, false
	}
	agg, ok = parseCPUTimes(fields)
	if !ok {
		return cpuTimes{}, nil, false
	}
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") || len(f[0]) == 3 {
			break
		}
		if ct, valid := parseCPUTimes(f); valid {
			cores = append(cores, ct)
		}
	}
	return agg, cores, true
}

// readCPU returns cumulative idle and total jiffies from /proc/stat.
func readCPU() (idle, all uint64, ok bool) {
	agg, _, ok := readCPUDetail()
	if !ok {
		return 0, 0, false
	}
	// Columns 4 and 5 are idle and iowait. Time spent waiting on disk is
	// not the CPU working, and counting it as busy makes a machine doing
	// nothing but reading files look pegged.
	return agg.idle + agg.iowait, agg.all, true
}

// readNet returns cumulative received and transmitted bytes summed across
// every interface except loopback, which never carries traffic to or from
// anywhere and would otherwise make an idle machine with SSH open to itself
// look busy.
func readNet() (rx, tx uint64, ok bool) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		if line <= 2 {
			// Two header lines: the split of "Receive" / "Transmit" over the
			// column names, which is why the columns cannot be counted by
			// position from the top of the file.
			continue
		}
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		if strings.TrimSpace(name) == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		// Column 0 is received bytes; column 8 is transmitted bytes, after the
		// seven other receive-side counters (packets, errs, drop, fifo,
		// frame, compressed, multicast).
		if len(fields) < 9 {
			continue
		}
		if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			rx += v
		}
		if v, err := strconv.ParseUint(fields[8], 10, 64); err == nil {
			tx += v
		}
	}
	return rx, tx, true
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

func readMemDetail() (total, available, free, cached, swapTotal, swapFree uint64) {
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
		case "MemFree":
			free = n
		case "Buffers", "Cached", "SReclaimable":
			cached += n
		case "SwapTotal":
			swapTotal = n
		case "SwapFree":
			swapFree = n
		}
	}
	return
}

func readMem() (total, available, swapTotal, swapFree uint64) {
	total, available, _, _, swapTotal, swapFree = readMemDetail()
	return
}

// readDiskIO reads cumulative block read and write bytes from /proc/vmstat's
// pgpgin and pgpgout (counted in 1 KiB units across all block devices, without
// partition-vs-whole-disk double-counting).
func readDiskIO() (readBytes, writeBytes uint64, ok bool) {
	f, err := os.Open("/proc/vmstat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	var haveIn, haveOut bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if rest, found := strings.CutPrefix(line, "pgpgin "); found {
			if n, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64); err == nil {
				readBytes = n * 1024
				haveIn = true
			}
		} else if rest, found := strings.CutPrefix(line, "pgpgout "); found {
			if n, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64); err == nil {
				writeBytes = n * 1024
				haveOut = true
			}
		}
		if haveIn && haveOut {
			return readBytes, writeBytes, true
		}
	}
	return 0, 0, false
}

// readPSI reads the 10-second Pressure Stall Information percentage (`some avg10=...`)
// from /proc/pressure/<kind>, or nil where PSI is not exposed.
func readPSI(kind string) *float64 {
	b, err := os.ReadFile("/proc/pressure/" + kind)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, tok := range strings.Fields(line) {
			if rest, ok := strings.CutPrefix(tok, "avg10="); ok {
				if v, err := strconv.ParseFloat(rest, 64); err == nil {
					c := clampPct(v)
					return &c
				}
			}
		}
	}
	return nil
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

// Memory reads MemTotal and MemAvailable, for callers that want the two numbers
// without taking a CPU sample -- which would move the shared counters under
// every other viewer's window. See minCPUWindow.
func Memory() (total, available uint64) {
	total, available, _, _ = readMem()
	return total, available
}
