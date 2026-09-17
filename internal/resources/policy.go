// Package resources decides how much of the machine the sessions get, notices
// when they are taking too much, and asks -- or acts -- before the machine
// stops answering.
//
// Three layers, each there because the one below it was not enough:
//
//   - **Placement** (layout.go, adopt.go). Sessions live in a systemd scope of
//     their own, not in the panel's unit. Before this they shared one cgroup,
//     and one typst compile growing to 17 GiB held that cgroup at its limit for
//     minutes: 6.9 million limit hits, 460 million file pages evicted and read
//     back, 37 OOM kills, and the panel's own binary and database pages were
//     among the ones evicted -- so the panel stalled with everything else and
//     reported "store: get session: context canceled". Placement alone fixes
//     that, measured: the stand-in panel kept 1470 of ~1480 probes while the
//     sessions thrashed, against 458 when they shared a cgroup.
//   - **A budget** (this file). The kernel does not OOM-kill while reclaim is
//     making progress, and evicting file pages always looks like progress.
//     A budget below the machine keeps the thrash inside the sessions and off
//     the host, and it moves with what the rest of the host is using.
//   - **A person, then a decision** (governor.go). Before the budget is hit,
//     the panel asks. When the sessions are already stalling and nobody has
//     answered, it ends the process that is causing it -- unless the person
//     chose a mode that says never to.
package resources

import (
	"errors"
	"fmt"
	"time"
)

// Mode is a named policy.
type Mode string

const (
	// Conservative keeps a quarter of the machine back and acts quickly.
	Conservative Mode = "conservative"
	// Balanced is the default.
	Balanced Mode = "balanced"
	// Performance gives the sessions nearly everything and never ends a
	// process on its own; the kernel's OOM killer is the backstop.
	Performance Mode = "performance"
	// Custom takes its numbers from the policy itself.
	Custom Mode = "custom"
)

// Policy is what the person chose, as it is stored.
type Policy struct {
	Mode Mode `json:"mode"`
	// PoolPercent is the share of physical memory the sessions may use.
	PoolPercent int `json:"poolPercent"`
	// AskPercent is how full the budget gets before the panel asks.
	AskPercent int `json:"askPercent"`
	// AutoAct lets the panel end a process when the sessions are stalling and
	// nobody has answered.
	AutoAct bool `json:"autoAct"`
	// GraceSeconds is how long it waits for that answer.
	GraceSeconds int `json:"graceSeconds"`
	// BoostUntil, while in the future, runs Performance whatever Mode says.
	// It is how "give it more for an hour" is answered without a person having
	// to remember to switch back.
	BoostUntil int64 `json:"boostUntil,omitempty"`
}

// Params are the numbers a policy comes to at a moment.
type Params struct {
	Mode         Mode `json:"mode"`
	PoolPercent  int  `json:"poolPercent"`
	AskPercent   int  `json:"askPercent"`
	AutoAct      bool `json:"autoAct"`
	GraceSeconds int  `json:"graceSeconds"`
	// Dynamic shrinks the budget when the rest of the host needs the memory.
	Dynamic bool `json:"dynamic"`
	Boosted bool `json:"boosted"`
}

// Presets are the three named modes. Exported so the page shows the same
// numbers the governor uses, from one table.
var Presets = map[Mode]Params{
	Conservative: {Mode: Conservative, PoolPercent: 75, AskPercent: 80, AutoAct: true, GraceSeconds: 30, Dynamic: true},
	Balanced:     {Mode: Balanced, PoolPercent: 88, AskPercent: 90, AutoAct: true, GraceSeconds: 90, Dynamic: true},
	// Not dynamic: somebody who picked this has said the sessions come first,
	// and a budget that shrinks because a browser opened elsewhere on the
	// host is the panel overruling them.
	Performance: {Mode: Performance, PoolPercent: 95, AskPercent: 95, AutoAct: false, GraceSeconds: 0, Dynamic: false},
}

// DefaultPolicy is what a panel that has never been told anything runs.
func DefaultPolicy() Policy {
	p := Presets[Balanced]
	return Policy{Mode: Balanced, PoolPercent: p.PoolPercent, AskPercent: p.AskPercent,
		AutoAct: p.AutoAct, GraceSeconds: p.GraceSeconds}
}

// Bounds on a custom policy. The pool floor is not about safety -- a small
// pool is a legitimate choice -- but below half the machine the dynamic budget
// and the ask threshold stop meaning different things. The ceiling matches
// the scope's own MemoryMax, which the pool cannot exceed anyway.
const (
	MinPoolPercent  = 30
	MaxPoolPercent  = 95
	MinAskPercent   = 50
	MaxAskPercent   = 100
	MinGraceSeconds = 10
	MaxGraceSeconds = 1800
	// MaxBoost is the longest "give it more" lasts.
	MaxBoost = 12 * time.Hour
)

// Validate refuses a policy that cannot be run as written. It does not clamp:
// a value outside the range came from something other than the page, and
// quietly storing a different number than the one sent is how a setting
// "does not stick".
func (p Policy) Validate() error {
	switch p.Mode {
	case Conservative, Balanced, Performance:
		return nil
	case Custom:
	default:
		return fmt.Errorf("unknown mode %q", p.Mode)
	}
	if p.PoolPercent < MinPoolPercent || p.PoolPercent > MaxPoolPercent {
		return fmt.Errorf("poolPercent must be %d–%d", MinPoolPercent, MaxPoolPercent)
	}
	if p.AskPercent < MinAskPercent || p.AskPercent > MaxAskPercent {
		return fmt.Errorf("askPercent must be %d–%d", MinAskPercent, MaxAskPercent)
	}
	// Always, not only with auto on: a stored value out of range is sent back
	// by the page the moment somebody ticks auto, and refused then.
	if p.GraceSeconds < MinGraceSeconds || p.GraceSeconds > MaxGraceSeconds {
		return fmt.Errorf("graceSeconds must be %d–%d", MinGraceSeconds, MaxGraceSeconds)
	}
	return nil
}

// ErrBoostTooLong is returned for a boost past MaxBoost.
var ErrBoostTooLong = errors.New("a boost lasts at most twelve hours")

// At resolves the policy into the numbers in force at now.
func (p Policy) At(now time.Time) Params {
	if p.BoostUntil > now.Unix() {
		out := Presets[Performance]
		out.Boosted = true
		return out
	}
	if pre, ok := Presets[p.Mode]; ok {
		return pre
	}
	return Params{Mode: Custom, PoolPercent: p.PoolPercent, AskPercent: p.AskPercent,
		AutoAct: p.AutoAct, GraceSeconds: p.GraceSeconds, Dynamic: true}
}

const (
	kib = 1024
	mib = 1024 * kib
	gib = 1024 * mib
)

// Reserve is the memory the host keeps no matter what the sessions want.
//
// Five percent with a floor, because on a 4 GiB machine five percent is 200
// MiB and sshd, journald and the panel together do not fit in that; and a cap
// at a quarter, because on a tiny VM the floor would otherwise be most of it.
func Reserve(total uint64) uint64 {
	r := total / 20
	if r < 768*mib {
		r = 768 * mib
	}
	if r > total/4 {
		r = total / 4
	}
	return r
}

// Budget is the memory.max the sessions' pool should have.
//
// total and available are the machine's (MemTotal, MemAvailable). poolHeld is
// what the sessions hold that reclaim cannot take back (anon and shmem), and
// scopeMax the scope's own limit, which the pool can never exceed and which the
// panel cannot raise without root.
//
// Dynamic: the pool may have what it holds plus whatever the host can still
// give above its reserve, and no more. Held rather than memory.current, because
// MemAvailable already counts the pool's own page cache as available: adding
// memory.current on top counted that cache twice, and a pool full of cache --
// the ordinary state of sessions that read files -- had a budget that could
// never shrink, so the squeeze this exists to put on the sessions never came.
// That lets the budget fall when something outside the panel takes memory, so
// the pressure lands on the sessions, which the panel can see and ask about,
// rather than on the whole machine.
//
// It never falls below what the sessions hold plus a margin. Below held, the
// kernel has nothing it may reclaim and goes straight to killing; cache above
// it is what a lower limit is meant to squeeze out.
func Budget(p Params, total, available, poolHeld, scopeMax uint64) uint64 {
	if total == 0 {
		return 0
	}
	limit := total / 100 * uint64(p.PoolPercent)
	if scopeMax > 0 && limit > scopeMax {
		limit = scopeMax
	}
	if !p.Dynamic {
		return limit
	}
	reserve := Reserve(total)
	room := uint64(0)
	if available > reserve {
		room = available - reserve
	}
	if grow := poolHeld + room; grow < limit {
		limit = grow
	}
	// The floor: what is held, with room for the next allocation, and never so
	// little that the next session cannot start its shell.
	floor := poolHeld + budgetMargin
	if floor < minBudget {
		floor = minBudget
	}
	if floor > total/2 && poolHeld < total/2 {
		floor = total / 2
	}
	if limit < floor {
		limit = floor
	}
	if scopeMax > 0 && limit > scopeMax {
		limit = scopeMax
	}
	return limit
}

// Numbers the policy is built from, each with what it is for.
const (
	// budgetMargin is left above held memory when the host squeezes the pool.
	budgetMargin = 256 * mib
	// minBudget is the least a pool is given, for an idle panel on a busy host.
	minBudget = 512 * mib
	// poolFullPercent: how close to its limit the pool must be before its I/O
	// stall is read as a memory stall. See Reading.PoolIOStall.
	poolFullPercent = 95
	// refaultRate, in pages a second, is how fast pages must be coming back
	// from disk after reclaim for that I/O to be the pool thrashing rather than
	// a build reading files it has not read before. A cold read is not a
	// refault; a thrash is nothing but.
	refaultRate = 2000
	// responsibleFloor is the least a session must hold to be named, or acted
	// on, at all: below it the session is not what filled anything.
	responsibleFloor = 256 * mib
)

// Level is how worried the governor is.
type Level int

const (
	OK Level = iota
	Warn
	Critical
)

func (l Level) String() string {
	switch l {
	case Warn:
		return "warn"
	case Critical:
		return "critical"
	}
	return "ok"
}

// Reason says which reading made the level what it is.
type Reason string

const (
	// ReasonPool: the sessions are near their budget.
	ReasonPool Reason = "pool"
	// ReasonMachine: the host itself is short -- below its reserve, or stalled
	// on memory while the sessions are not. The pressure may be from anything
	// on the machine.
	ReasonMachine Reason = "machine"
	// ReasonStall: the sessions are stalled on memory -- the reclaim loop the
	// kernel will not break on its own.
	ReasonStall Reason = "stall"
)

// Reading is the governor's view of one moment.
type Reading struct {
	Total, Available uint64
	// PoolCurrent and PoolMax are the sessions' pool; PoolMax 0 means there is
	// no pool (no isolation), and only the machine readings count. An
	// unlimited memory.max is read as 0 too.
	PoolCurrent, PoolMax uint64
	// PoolHeld is the part of PoolCurrent the kernel cannot give back: anon
	// and shmem, with swap off. memory.current also counts page cache, and a
	// pool whose sessions have read a lot of files sits at its limit
	// indefinitely without anything being wrong -- measured, right after the
	// process that caused a stall was ended the pool still read 89% full, all
	// of it cache, and the panel asked again about nothing.
	PoolHeld uint64
	// PoolStall is the pool's memory.pressure "full avg10": the share of the
	// last ten seconds in which every task in it was waiting on memory.
	PoolStall float64
	// PoolIOStall is the pool's io.pressure "full avg10". It counts as a memory
	// stall only while the pool is at its limit and pages are coming back from
	// disk faster than refaultRate: that is when reading from disk means
	// reading back pages reclaim just threw away, and the incident this was
	// built after showed it more clearly there (49% over five minutes) than on
	// memory.pressure (14%). Otherwise it is a build doing I/O -- an npm
	// install on a slow disk read as a critical stall before the refault test
	// was added.
	PoolIOStall float64
	// PoolRefaults is workingset_refault_file per second since the last tick.
	PoolRefaults float64
	// HostStall is the same for the whole machine, from /proc/pressure/memory.
	HostStall float64
}

// Stall thresholds, as "full avg10" percentages.
//
// Measured rather than chosen: the incident that started this read 49% on
// io.pressure full over five minutes while the panel was unreachable, and the
// deliberate thrash used to test the fix read between 20 and 60. An ordinary
// busy build with a warm cache reads 0 to 2. Twenty is well clear of the second
// and reached within seconds of the first.
const (
	StallWarn     = 10.0
	StallCritical = 20.0
)

func (r Reading) hasPool() bool { return r.PoolMax > 0 }

// Stall is the sessions' stall as Assess counts it: memory, or I/O while the
// pool is thrashing. See PoolIOStall.
func (r Reading) Stall() float64 {
	stall := r.PoolStall
	if r.hasPool() && r.PoolCurrent >= r.PoolMax/100*poolFullPercent &&
		r.PoolRefaults >= refaultRate && r.PoolIOStall > stall {
		stall = r.PoolIOStall
	}
	return stall
}

// Named reports whether a session holds enough to be named in a question: a
// quarter of what the pool holds, or a twentieth of the machine without a pool.
// Naming a 200 MiB shell while two others hold gigabytes sends somebody to end
// the wrong thing.
func Named(sessionHeld uint64, r Reading) bool {
	if sessionHeld < responsibleFloor {
		return false
	}
	if r.hasPool() {
		return sessionHeld >= r.PoolHeld/4
	}
	return sessionHeld >= machineFloor(r.Total)
}

// Responsible reports whether a session holds enough -- in memory the kernel
// cannot reclaim -- to be the cause of the pressure, and so enough for the
// panel to end a process in it unasked.
//
// When the sessions are the ones stalling, half of the pool: the pressure is
// inside the pool, and a session holding half of it is what filled it. When
// the machine is short for reasons of its own, a twentieth of the machine,
// pool or not: the pressure may come from a container or a desktop, and a
// session that is small against the machine is only the nearest thing the
// panel is allowed to touch.
func Responsible(sessionHeld uint64, r Reading, reason Reason) bool {
	if sessionHeld < responsibleFloor {
		return false
	}
	if r.hasPool() && reason != ReasonMachine {
		return sessionHeld >= r.PoolHeld/2
	}
	return sessionHeld >= machineFloor(r.Total)
}

func machineFloor(total uint64) uint64 {
	f := total / 20
	if f < gib {
		f = gib
	}
	return f
}

// Assess turns a reading into a level and the reason for it. The most severe
// reason wins; on a tie the order is the sessions' stall, then the machine,
// then the pool, because that is the order in which they stop the machine
// answering.
func Assess(p Params, r Reading) (Level, Reason) {
	reserve := Reserve(r.Total)
	stall := r.Stall()
	switch {
	case stall >= StallCritical:
		return Critical, ReasonStall
	case r.HostStall >= StallCritical, r.Total > 0 && r.Available < reserve/2:
		return Critical, ReasonMachine
	case stall >= StallWarn:
		return Warn, ReasonStall
	case r.HostStall >= StallWarn, r.Total > 0 && r.Available < reserve:
		return Warn, ReasonMachine
	case r.hasPool() && r.PoolHeld*100 >= r.PoolMax*uint64(p.AskPercent):
		return Warn, ReasonPool
	}
	return OK, ""
}
