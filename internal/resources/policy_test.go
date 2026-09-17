package resources

import (
	"testing"
	"time"
)

const total48 = 48 * gib

func TestPresetsAreWhatThePageSays(t *testing.T) {
	// The page draws these numbers from the same table; a preset that changed
	// here without a reason changes what somebody already chose.
	cases := map[Mode][3]int{
		Conservative: {75, 80, 30},
		Balanced:     {88, 90, 90},
		Performance:  {95, 95, 0},
	}
	for m, want := range cases {
		p := Presets[m]
		if p.PoolPercent != want[0] || p.AskPercent != want[1] || p.GraceSeconds != want[2] {
			t.Errorf("%s: %+v", m, p)
		}
	}
	if Presets[Performance].AutoAct || Presets[Performance].Dynamic {
		t.Error("performance must never act on its own and never shrink the budget")
	}
	if !Presets[Balanced].AutoAct || !Presets[Balanced].Dynamic {
		t.Error("balanced acts and follows the host")
	}
	if d := DefaultPolicy(); d.Mode != Balanced || d.Validate() != nil {
		t.Errorf("default %+v", d)
	}
}

func TestValidateRefusesRatherThanClamps(t *testing.T) {
	ok := Policy{Mode: Custom, PoolPercent: 60, AskPercent: 80, AutoAct: true, GraceSeconds: 30}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Policy{
		{Mode: "turbo"},
		{Mode: Custom, PoolPercent: 29, AskPercent: 80, GraceSeconds: 30},
		{Mode: Custom, PoolPercent: 96, AskPercent: 80, GraceSeconds: 30},
		{Mode: Custom, PoolPercent: 60, AskPercent: 49, GraceSeconds: 30},
		{Mode: Custom, PoolPercent: 60, AskPercent: 101, GraceSeconds: 30},
		{Mode: Custom, PoolPercent: 60, AskPercent: 80, AutoAct: true, GraceSeconds: 9},
		{Mode: Custom, PoolPercent: 60, AskPercent: 80, AutoAct: true, GraceSeconds: 1801},
	}
	for _, p := range bad {
		if p.Validate() == nil {
			t.Errorf("accepted %+v", p)
		}
	}
	// Validated with auto off too: the page sends the stored value back the
	// moment auto is ticked, and it was refused then.
	if err := (Policy{Mode: Custom, PoolPercent: 60, AskPercent: 80}).Validate(); err == nil {
		t.Error("a grace of 0 was stored with auto off")
	}
}

func TestABoostRunsPerformanceUntilItEnds(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := Policy{Mode: Conservative, BoostUntil: now.Add(time.Hour).Unix()}
	got := p.At(now)
	if got.Mode != Performance || !got.Boosted {
		t.Fatalf("during: %+v", got)
	}
	if got := p.At(now.Add(2 * time.Hour)); got.Mode != Conservative || got.Boosted {
		t.Fatalf("after: %+v", got)
	}
	c := Policy{Mode: Custom, PoolPercent: 60, AskPercent: 70, AutoAct: true, GraceSeconds: 20}
	if got := c.At(now); got.PoolPercent != 60 || got.AskPercent != 70 || got.GraceSeconds != 20 || !got.Dynamic {
		t.Fatalf("custom: %+v", got)
	}
}

func TestReserve(t *testing.T) {
	if r := Reserve(total48); r != total48/20 {
		t.Errorf("48G: %d", r)
	}
	if r := Reserve(8 * gib); r != 768*mib {
		t.Errorf("8G floor: %d", r)
	}
	if r := Reserve(2 * gib); r != 2*gib/4 {
		t.Errorf("2G cap: %d", r)
	}
}

func TestBudget(t *testing.T) {
	bal := Presets[Balanced]
	perf := Presets[Performance]
	reserve := Reserve(total48)

	// A quiet host: the mode's share, bounded by the scope.
	if got := Budget(bal, total48, 45*gib, 2*gib, 0); got != total48/100*88 {
		t.Errorf("quiet: %d", got)
	}
	if got := Budget(bal, total48, 45*gib, 2*gib, 30*gib); got != 30*gib {
		t.Errorf("scope bound: %d", got)
	}
	// The host is using memory outside the sessions: the pool may have what it
	// holds and what is left above the reserve.
	if got := Budget(bal, total48, 6*gib, 10*gib, 0); got != 10*gib+6*gib-reserve {
		t.Errorf("busy host: %d", got)
	}
	// Measured before the fix: a pool of 40 GiB, nearly all of it cache, on a
	// host with 1 GiB available, kept a 40 GiB budget forever, because the
	// floor was memory.current. The floor is what is held.
	if got := Budget(bal, 64*gib, 1*gib, 2*gib, 0); got != 2*gib+budgetMargin {
		t.Errorf("a cache-full pool on a squeezed host: %d", got)
	}
	// Performance does not follow the host.
	if got := Budget(perf, total48, reserve/2, 10*gib, 0); got != total48/100*95 {
		t.Errorf("performance: %d", got)
	}
	// An idle pool on a busy host still has room for a shell.
	if got := Budget(bal, total48, 0, 0, 0); got != minBudget {
		t.Errorf("floor: %d", got)
	}
	if got := Budget(bal, 0, 0, 0, 0); got != 0 {
		t.Errorf("no reading: %d", got)
	}
}

func TestAssess(t *testing.T) {
	p := Presets[Balanced] // asks at 90%
	calm := Reading{Total: total48, Available: 30 * gib, PoolMax: 20 * gib, PoolCurrent: 19 * gib, PoolHeld: 10 * gib}
	if l, _ := Assess(p, calm); l != OK {
		t.Errorf("a pool full of cache is not a question: %v", l)
	}
	near := calm
	near.PoolHeld = 18 * gib
	if l, r := Assess(p, near); l != Warn || r != ReasonPool {
		t.Errorf("held at 90%%: %v %v", l, r)
	}
	below := calm
	below.PoolHeld = 17 * gib
	if l, _ := Assess(p, below); l != OK {
		t.Errorf("held below the line: %v", l)
	}

	stall := calm
	stall.PoolStall = StallWarn
	if l, r := Assess(p, stall); l != Warn || r != ReasonStall {
		t.Errorf("stall warn: %v %v", l, r)
	}
	stall.PoolStall = StallCritical
	if l, r := Assess(p, stall); l != Critical || r != ReasonStall {
		t.Errorf("stall critical: %v %v", l, r)
	}

	// I/O stall counts only while the pool is at its limit and pages are
	// coming back from disk.
	io := calm
	io.PoolCurrent = io.PoolMax
	io.PoolIOStall = 40
	if l, _ := Assess(p, io); l != OK {
		t.Errorf("an npm install at the limit, reading new files: %v", l)
	}
	io.PoolRefaults = refaultRate
	if l, r := Assess(p, io); l != Critical || r != ReasonStall {
		t.Errorf("I/O stall while thrashing at the limit: %v %v", l, r)
	}
	io.PoolCurrent = 15 * gib
	if l, _ := Assess(p, io); l != OK {
		t.Errorf("refaults below the limit: %v", l)
	}

	host := calm
	host.Available = Reserve(total48) - 1
	if l, r := Assess(p, host); l != Warn || r != ReasonMachine {
		t.Errorf("machine warn: %v %v", l, r)
	}
	host.Available = Reserve(total48)/2 - 1
	if l, r := Assess(p, host); l != Critical || r != ReasonMachine {
		t.Errorf("machine critical: %v %v", l, r)
	}
	hostStall := calm
	hostStall.HostStall = StallCritical
	if l, r := Assess(p, hostStall); l != Critical || r != ReasonMachine {
		t.Errorf("the host stalling while the sessions do not is the machine: %v %v", l, r)
	}
	both := hostStall
	both.PoolStall = StallCritical
	if _, r := Assess(p, both); r != ReasonStall {
		t.Errorf("the sessions stalling outranks the host: %v", r)
	}

	nopool := Reading{Total: total48, Available: 30 * gib, PoolHeld: 40 * gib}
	if l, _ := Assess(p, nopool); l != OK {
		t.Errorf("no pool: %v", l)
	}
}

func TestResponsible(t *testing.T) {
	r := Reading{Total: total48, PoolMax: 20 * gib, PoolHeld: 10 * gib}
	if !Responsible(5*gib, r, ReasonStall) || Responsible(5*gib-1, r, ReasonStall) {
		t.Error("half the pool, when the sessions stall")
	}
	small := Reading{Total: total48, PoolMax: 2 * gib, PoolHeld: 300 * mib}
	if Responsible(200*mib, small, ReasonStall) {
		t.Error("under the floor is never responsible")
	}
	// The machine is short for reasons of its own: a pool does not lower the bar.
	if Responsible(total48/20-1, r, ReasonMachine) || !Responsible(total48/20, r, ReasonMachine) {
		t.Error("the machine's bar is a twentieth of it")
	}
	nopool := Reading{Total: total48}
	if Responsible(total48/20-1, nopool, ReasonStall) || !Responsible(total48/20, nopool, ReasonStall) {
		t.Error("without a pool the bar is a twentieth of the machine")
	}
}

func TestNamed(t *testing.T) {
	r := Reading{Total: total48, PoolMax: 20 * gib, PoolHeld: 8 * gib}
	if !Named(2*gib, r) || Named(2*gib-1, r) {
		t.Error("a quarter of what the pool holds")
	}
	if Named(200*mib, Reading{Total: total48, PoolMax: 1 * gib, PoolHeld: 300 * mib}) {
		t.Error("under the floor")
	}
}
