package resources

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// A pane with a child, as real processes: the checks a kill makes are against
// /proc, and a fake /proc reproduces none of the ways a pid goes stale.
func spawnPane(t *testing.T) (pane, child int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 300 & echo $!; wait")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	child, err = strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatalf("child pid %q: %v", buf[:n], err)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	return cmd.Process.Pid, child
}

type world struct {
	mu       sync.Mutex
	panes    []Pane
	sessions []SessionMeta
	emitted  []Event
	audited  []string
}

func (w *world) env() Env {
	return Env{
		Panes: func() []Pane { w.mu.Lock(); defer w.mu.Unlock(); return append([]Pane(nil), w.panes...) },
		Sessions: func() []SessionMeta {
			w.mu.Lock()
			defer w.mu.Unlock()
			return append([]SessionMeta(nil), w.sessions...)
		},
		Emit:  func(e Event) { w.mu.Lock(); w.emitted = append(w.emitted, e); w.mu.Unlock() },
		Audit: func(e, who, d string) { w.mu.Lock(); w.audited = append(w.audited, e+" "+d); w.mu.Unlock() },
	}
}

func (w *world) emits() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.emitted) }

// alive treats a zombie as gone: the test's own shell is the parent and reaps
// on its own schedule, and a zombie has already received the signal.
func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	return i > 0 && i+2 < len(s) && s[i+2] != 'Z'
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if !alive(pid) {
			return
		}
	}
	t.Fatalf("pid %d is still running", pid)
}

// oneSession is a world with one session, "a", whose pane is a real shell with
// a real child.
func oneSession(t *testing.T) (*world, *Governor, int, int) {
	pane, child := spawnPane(t)
	w := &world{
		panes:    []Pane{{TmuxName: "vp_a", PID: pane}},
		sessions: []SessionMeta{{ID: "a", TmuxName: "vp_a"}},
	}
	return w, New(w.env()), pane, child
}

func (g *Governor) tickDecide(now time.Time, p Params, l Level, reason Reason, r Reading, s []SessionView) {
	g.tickMu.Lock()
	defer g.tickMu.Unlock()
	g.decide(now, p, l, reason, r, s, nil, g.roster(), LazyProcs())
}

// A pool of 8 GiB holding 4, and session "a" holding all of it: named and
// responsible, whatever the reason.
var (
	poolReading = Reading{Total: total48, Available: 30 * gib, PoolMax: 8 * gib, PoolCurrent: 8 * gib, PoolHeld: 4 * gib}
	heavy       = []SessionView{{ID: "a", TmuxName: "vp_a", Held: 4 * gib, Memory: 4 * gib}}
)

func TestAKillIsRefusedForAnythingButTheProcessShown(t *testing.T) {
	w, g, _, child := oneSession(t)
	_, other := spawnPane(t)
	p, ok := sysmon.ReadProc(child)
	if !ok {
		t.Fatal("child not readable")
	}

	if _, err := g.Kill("nope", child, p.Start, "t"); !errors.Is(err, ErrUnknownSession) {
		t.Errorf("unknown session: %v", err)
	}
	// Same pid, different start: the process shown has gone and the number
	// belongs to something else now.
	if _, err := g.Kill("a", child, p.Start+1, "t"); !errors.Is(err, ErrGone) {
		t.Errorf("stale start time: %v", err)
	}
	op, _ := sysmon.ReadProc(other)
	if _, err := g.Kill("a", other, op.Start, "t"); !errors.Is(err, ErrNotInSession) {
		t.Errorf("another session's process: %v", err)
	}
	self, _ := sysmon.ReadProc(os.Getpid())
	if _, err := g.Kill("a", os.Getpid(), self.Start, "t"); !errors.Is(err, ErrNotInSession) {
		t.Errorf("the panel itself: %v", err)
	}
	if !alive(child) || !alive(other) {
		t.Fatal("a refused kill sent a signal")
	}

	act, err := g.Kill("a", child, p.Start, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if act.Kind != ActionKill || act.Auto || act.Name != "sleep" || act.Who != "tester" {
		t.Errorf("action %+v", act)
	}
	waitGone(t, child)
	if len(w.audited) != 1 || !strings.HasPrefix(w.audited[0], `resources.kill "sleep"`) {
		t.Errorf("audit %v", w.audited)
	}
	if v := g.View(); len(v.Actions) != 1 {
		t.Errorf("recent actions %+v", v.Actions)
	}
}

func TestTheQuestionAsksThenActsOnlyWhenAllowed(t *testing.T) {
	w, g, pane, child := oneSession(t)
	now := time.Now()
	bal := Presets[Balanced]

	g.tickDecide(now, bal, Warn, ReasonPool, poolReading, heavy)
	a := g.View().Alert
	if a == nil || a.Level != "warn" || a.AutoAt != 0 || a.SessionID != "a" || a.Proc == nil || !a.CanBoost {
		t.Fatalf("warn alert %+v", a)
	}
	if w.emits() != 1 {
		t.Fatalf("emitted %d", w.emits())
	}
	g.tickDecide(now.Add(2*time.Second), bal, Warn, ReasonPool, poolReading, heavy)
	if w.emits() != 1 || g.View().Alert.ID != a.ID {
		t.Fatalf("a repeated tick re-asked: %d emitted", w.emits())
	}

	perf := Presets[Performance]
	g.tickDecide(now, perf, Critical, ReasonStall, poolReading, heavy)
	if a := g.View().Alert; a.AutoAt != 0 || a.CanBoost {
		t.Fatalf("performance: %+v", a)
	}

	g.tickDecide(now, bal, Critical, ReasonStall, poolReading, heavy)
	crit := g.View().Alert
	if crit.ID != a.ID {
		t.Fatal("a level change made a new question")
	}
	if crit.AutoAt != now.Add(time.Duration(bal.GraceSeconds)*time.Second).Unix() {
		t.Fatalf("countdown %+v", crit)
	}
	if crit.Proc.PID != child || crit.Proc.Root {
		t.Fatalf("named %+v, want the child %d and never the pane %d", crit.Proc, child, pane)
	}
	g.tickDecide(now.Add(10*time.Second), bal, Critical, ReasonStall, poolReading, heavy)
	if !alive(child) || g.View().Alert.AutoAt != crit.AutoAt {
		t.Fatal("ended early, or the countdown moved")
	}

	g.tickDecide(now.Add(time.Duration(bal.GraceSeconds+1)*time.Second), bal, Critical, ReasonStall, poolReading, heavy)
	waitGone(t, child)
	if g.View().Alert != nil {
		t.Fatal("the question outlived the answer")
	}
	w.mu.Lock()
	last := w.emitted[len(w.emitted)-1]
	audited := append([]string(nil), w.audited...)
	w.mu.Unlock()
	if last.Alert != nil || last.Acted == nil || !last.Acted.Auto {
		t.Fatalf("the push after acting did not say so: %+v", last)
	}
	if !strings.HasPrefix(audited[len(audited)-1], "resources.kill.auto") {
		t.Fatalf("audit %v", audited)
	}
	before := w.emits()
	g.tickDecide(now.Add(time.Duration(bal.GraceSeconds+5)*time.Second), bal, Critical, ReasonStall, poolReading, heavy)
	if w.emits() != before {
		t.Fatal("asked again during the quiet period")
	}
}

// Measured before the fix: 400 seconds of critical with a dip to warn every
// 20 seconds never ended anything and pushed forty questions.
func TestAStallHoveringAtTheThresholdStillActs(t *testing.T) {
	w, g, _, child := oneSession(t)
	bal := Presets[Balanced]
	now := time.Now()
	for s := 0; s <= bal.GraceSeconds+20; s += 2 {
		level := Critical
		if s%20 == 10 {
			level = Warn
		}
		g.tickDecide(now.Add(time.Duration(s)*time.Second), bal, level, ReasonStall, poolReading, heavy)
	}
	waitGone(t, child)
	if w.emits() > 12 {
		t.Fatalf("%d pushes for one question", w.emits())
	}
}

func TestACountdownIsForgottenWhenTheStallEases(t *testing.T) {
	_, g, _, child := oneSession(t)
	bal := Presets[Balanced]
	now := time.Now()
	g.tickDecide(now, bal, Critical, ReasonStall, poolReading, heavy)
	last := 0
	for s := 2; s <= int(warnForgets.Seconds())+4; s += 2 {
		g.tickDecide(now.Add(time.Duration(s)*time.Second), bal, Warn, ReasonStall, poolReading, heavy)
		last = s
	}
	if a := g.View().Alert; a == nil || a.AutoAt != 0 {
		t.Fatalf("a countdown survived %v of warning: %+v", warnForgets, a)
	}
	// Critical again: a fresh countdown, not the old one already run out.
	g.tickDecide(now.Add(time.Duration(last+2)*time.Second), bal, Critical, ReasonStall, poolReading, heavy)
	if !alive(child) {
		t.Fatal("acted on a countdown that had been forgotten")
	}
}

func TestTheAgentItselfIsNeverEndedUnasked(t *testing.T) {
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	w := &world{
		sessions: []SessionMeta{{ID: "a", TmuxName: "vp_a"}},
		panes:    []Pane{{TmuxName: "vp_a", PID: cmd.Process.Pid}},
	}
	g := New(w.env())
	now := time.Now()
	bal := Presets[Balanced]
	g.tickDecide(now, bal, Critical, ReasonStall, poolReading, heavy)
	a := g.View().Alert
	if a == nil || a.Proc == nil || !a.Proc.Root || a.AutoAt != 0 {
		t.Fatalf("%+v", a)
	}
	g.tickDecide(now.Add(time.Hour), bal, Critical, ReasonStall, poolReading, heavy)
	if !alive(cmd.Process.Pid) {
		t.Fatal("the session's own process was ended unasked")
	}
}

func TestHostPressureNeverEndsAProcessInASmallSession(t *testing.T) {
	_, g, _, child := oneSession(t)
	now := time.Now()
	bal := Presets[Balanced]
	// A pool exists and "a" holds all of it -- but the machine is short for
	// reasons of its own, and 600 MiB is nothing against the machine.
	r := Reading{Total: total48, Available: 1 * gib, HostStall: 30, PoolMax: 8 * gib, PoolHeld: 600 * mib}
	small := []SessionView{{ID: "a", TmuxName: "vp_a", Held: 600 * mib}}
	g.tickDecide(now, bal, Critical, ReasonMachine, r, small)
	if a := g.View().Alert; a == nil || a.AutoAt != 0 {
		t.Fatalf("a countdown against a session that is not the cause: %+v", a)
	}
	g.tickDecide(now.Add(time.Hour), bal, Critical, ReasonMachine, r, small)
	if !alive(child) {
		t.Fatal("ended a process in a session that was not the cause")
	}
}

func TestASessionTooSmallIsNotNamed(t *testing.T) {
	w := &world{sessions: []SessionMeta{{ID: "a", TmuxName: "vp_a"}}}
	g := New(w.env())
	r := Reading{Total: total48, Available: 30 * gib, PoolMax: 8 * gib, PoolCurrent: 8 * gib, PoolHeld: 6 * gib}
	// The biggest holds 200 MiB; the rest of the pool is spread thin. Naming
	// it would send somebody to end the wrong thing.
	s := []SessionView{{ID: "a", TmuxName: "vp_a", Held: 200 * mib}}
	g.tickDecide(time.Now(), Presets[Balanced], Critical, ReasonStall, r, s)
	if a := g.View().Alert; a == nil || a.SessionID != "" || a.Proc != nil {
		t.Fatalf("%+v", a)
	}
}

func TestAPausedSessionIsNotBlamed(t *testing.T) {
	_, g, _, child := oneSession(t)
	frozen := []SessionView{{ID: "a", TmuxName: "vp_a", Held: 4 * gib, Frozen: true}}
	now := time.Now()
	g.tickDecide(now, Presets[Balanced], Critical, ReasonStall, poolReading, frozen)
	if a := g.View().Alert; a == nil || a.SessionID != "" {
		t.Fatalf("%+v", a)
	}
	g.tickDecide(now.Add(time.Hour), Presets[Balanced], Critical, ReasonStall, poolReading, frozen)
	if !alive(child) {
		t.Fatal("ended a process in a paused session")
	}
}

func TestAProcessTheKernelRefusedIsNotOfferedAgain(t *testing.T) {
	_, g, _, child := oneSession(t)
	p, _ := sysmon.ReadProc(child)
	g.t.unkillable[procKey{child, p.Start}] = true
	g.tickDecide(time.Now(), Presets[Balanced], Critical, ReasonStall, poolReading, heavy)
	if a := g.View().Alert; a.Proc != nil && a.Proc.PID == child {
		t.Fatalf("offered a process already refused: %+v", a.Proc)
	}
}

func TestAnAnswerTakesTheQuestionDown(t *testing.T) {
	_, g, _, child := oneSession(t)
	g.tickDecide(time.Now(), Presets[Balanced], Critical, ReasonStall, poolReading, heavy)
	p, _ := sysmon.ReadProc(child)
	if _, err := g.Kill("a", child, p.Start, "t"); err != nil {
		t.Fatal(err)
	}
	if g.View().Alert != nil {
		t.Fatal("the question stayed after the person ended the process")
	}
	g.tickDecide(time.Now(), Presets[Balanced], Critical, ReasonStall, poolReading, heavy)
	if g.View().Alert != nil {
		t.Fatal("asked again straight after the answer")
	}
}

func TestSnoozeSilencesAWarningButNotAStall(t *testing.T) {
	w := &world{sessions: []SessionMeta{{ID: "a", TmuxName: "vp_a"}}}
	g := New(w.env())
	now := time.Now()
	s := []SessionView{{ID: "a", TmuxName: "vp_a", Held: 4 * gib}}
	bal := Presets[Balanced]
	g.tickDecide(now, bal, Warn, ReasonPool, poolReading, s)
	a := g.View().Alert
	if err := g.Snooze("wrong", "a", time.Minute); !errors.Is(err, ErrNoAlert) {
		t.Fatalf("stale id: %v", err)
	}
	if err := g.Snooze(a.ID, "nobody", time.Minute); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session: %v", err)
	}
	if err := g.Snooze(a.ID, "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if g.View().Alert != nil {
		t.Fatal("snoozed warning still standing")
	}
	g.tickDecide(now.Add(time.Second), bal, Warn, ReasonPool, poolReading, s)
	if g.View().Alert != nil {
		t.Fatal("snoozed warning raised again")
	}
	g.tickDecide(now.Add(2*time.Second), bal, Critical, ReasonStall, poolReading, s)
	if a := g.View().Alert; a == nil || a.Level != "critical" {
		t.Fatalf("a stall was snoozed: %+v", a)
	}
	// Easing back to a warning on a snoozed session takes the critical
	// question down rather than leaving it up with a countdown that cannot run.
	g.tickDecide(now.Add(4*time.Second), bal, Warn, ReasonStall, poolReading, s)
	if g.View().Alert != nil {
		t.Fatal("a critical question stayed up after easing to a snoozed warning")
	}
	g.tickDecide(now.Add(2*time.Minute), bal, Warn, ReasonPool, poolReading, s)
	if g.View().Alert == nil {
		t.Fatal("the snooze did not end")
	}
	g.tickDecide(now.Add(3*time.Minute), bal, OK, "", poolReading, s)
	g.tickDecide(now.Add(3*time.Minute+2*time.Second), bal, OK, "", poolReading, s)
	if g.View().Alert != nil {
		t.Fatal("OK readings left the question up")
	}
}

func TestTheCulpritIsWhatGrewNotWhatIsBiggest(t *testing.T) {
	g := New(Env{})
	now := time.Now()
	g.t.growth["big"] = []memSample{{now.Add(-50 * time.Second), 10 * gib}, {now, 10 * gib}}
	g.t.growth["grew"] = []memSample{{now.Add(-50 * time.Second), 1 * gib}, {now, 4 * gib}}
	sessions := []SessionView{{ID: "big", Held: 10 * gib}, {ID: "grew", Held: 4 * gib}}
	if c := g.culprit(sessions, now); c.ID != "grew" {
		t.Fatalf("culprit %s", c.ID)
	}
	g.t.growth["grew"] = []memSample{{now.Add(-50 * time.Second), 3900 * mib}, {now, 4 * gib}}
	if c := g.culprit(sessions, now); c.ID != "big" {
		t.Fatalf("culprit %s", c.ID)
	}
	if c := g.culprit([]SessionView{{ID: "s", Held: gib}, {ID: "l", Held: 3 * gib}}, now); c.ID != "l" {
		t.Fatalf("not sorted, still the biggest: %s", c.ID)
	}
}

func TestPickTarget(t *testing.T) {
	root := ProcView{PID: 1, RSS: 1000, Root: true}
	cases := []struct {
		name string
		top  []ProcView
		want int
	}{
		{"root alone", []ProcView{root}, 1},
		{"root with a small child", []ProcView{root, {PID: 2, RSS: 499}}, 1},
		{"root with a half-size child", []ProcView{root, {PID: 2, RSS: 500}}, 2},
		{"a child is the largest", []ProcView{{PID: 3, RSS: 2000}, root}, 3},
	}
	for _, c := range cases {
		if got := pickTarget(c.top); got == nil || got.PID != c.want {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if pickTarget(nil) != nil {
		t.Error("nothing to pick")
	}
}

func TestGrowthForgetsWhatIsOlderThanAMinute(t *testing.T) {
	now := time.Now()
	var s []memSample
	s = appendSample(s, memSample{now.Add(-2 * time.Minute), 1}, now)
	s = appendSample(s, memSample{now.Add(-30 * time.Second), 2}, now)
	s = appendSample(s, memSample{now, 3}, now)
	if len(s) != 2 || s[0].cur != 2 {
		t.Fatalf("%+v", s)
	}
}

func TestAHogIsMovedDownOnlyWhenTheMachineIsShort(t *testing.T) {
	g := New(Env{})
	now := time.Now()
	hog := func() []SessionView {
		return []SessionView{{ID: "hog", CPUPercent: 60, Priority: PriorityNormal}, {ID: "typing", CPUPercent: 70, Priority: PriorityHigh}}
	}
	s := hog()
	g.demote(s, 90, now)
	if s[0].Priority != PriorityNormal {
		t.Fatal("moved down on the first reading")
	}
	s = hog()
	g.demote(s, 10, now.Add(2*time.Minute))
	if s[0].Priority != PriorityNormal {
		t.Fatal("moved down on an idle machine")
	}
	s = hog()
	g.demote(s, 90, now.Add(2*time.Minute))
	if s[0].Priority != PriorityLow {
		t.Fatalf("a minute of hogging a busy machine: %q", s[0].Priority)
	}
	if s[1].Priority != PriorityHigh {
		t.Fatal("the session somebody is using was moved down")
	}
	quiet := []SessionView{{ID: "hog", CPUPercent: 5, Priority: PriorityNormal}}
	g.demote(quiet, 90, now.Add(3*time.Minute))
	s = hog()
	g.demote(s, 90, now.Add(3*time.Minute+time.Second))
	if s[0].Priority != PriorityNormal {
		t.Fatal("the clock did not restart")
	}
}

// Measured before the fix: a unit whose account the root helper refuses
// restarted the panel every minute, forever.
func TestASystemUnitRestartsOnlyForAServerStartedSinceIt(t *testing.T) {
	restarts := 0
	g := New(Env{Manager: cgroup.System, Prepared: true, Restart: func() { restarts++ }})
	g.t.started = time.Now().Add(-time.Hour)
	g.self = procKey{PID: 1, Start: 1000}

	if _, err := g.adopt(t.Context(), procKey{PID: 2, Start: 900}, LazyProcs()); !errors.Is(err, errPrepareFailed) || restarts != 0 {
		t.Fatalf("an older server: %v, %d restarts", err, restarts)
	}
	if _, err := g.adopt(t.Context(), procKey{PID: 2, Start: 1100}, LazyProcs()); !errors.Is(err, errRestarting) || restarts != 1 {
		t.Fatalf("a newer server: %v, %d restarts", err, restarts)
	}
	if _, err := g.adopt(t.Context(), procKey{PID: 2, Start: 1100}, LazyProcs()); errors.Is(err, errRestarting) || restarts != 1 {
		t.Fatalf("a second restart within ten minutes: %v, %d restarts", err, restarts)
	}
	g2 := New(Env{Manager: cgroup.System})
	if _, err := g2.adopt(t.Context(), procKey{PID: 2, Start: 1}, LazyProcs()); !errors.Is(err, errUnitOutdated) {
		t.Fatalf("no root step: %v", err)
	}
}

func TestEveryErrorMapsToOneReason(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		err    error
		reason string
		detail string
	}{
		{errUnitOutdated, NoneUnitOutdated, ""},
		{errPrepareFailed, NonePrepareFailed, ""},
		{errRestarting, NoneRestarting, ""},
		{ErrNotDelegated, NoneNotDelegated, ""},
		{ErrNotInScope, NoneFailed, ""},
		{boom, NoneFailed, "boom"},
	}
	for _, c := range cases {
		if r, d := noneReason(c.err); r != c.reason || d != c.detail {
			t.Errorf("%v: %q %q", c.err, r, d)
		}
	}
}

func TestASessionSomebodyIsUsingIsAskedAboutAndNeverActedOn(t *testing.T) {
	_, g, _, child := oneSession(t)
	typing := []SessionView{{ID: "a", TmuxName: "vp_a", Held: 4 * gib, Priority: PriorityHigh}}
	now := time.Now()
	bal := Presets[Balanced]
	g.tickDecide(now, bal, Critical, ReasonStall, poolReading, typing)
	if a := g.View().Alert; a == nil || a.Proc == nil || a.AutoAt != 0 {
		t.Fatalf("%+v", a)
	}
	g.tickDecide(now.Add(time.Hour), bal, Critical, ReasonStall, poolReading, typing)
	if !alive(child) {
		t.Fatal("ended a process in the session somebody was typing into")
	}
}

func TestTheNamedProcessDoesNotSwapWithOneTheSameSize(t *testing.T) {
	a := ProcView{PID: 10, Start: 1, RSS: 1000}
	b := ProcView{PID: 11, Start: 1, RSS: 1010}
	top := []ProcView{b, a}
	if got := keepTarget(&a, &b, top); got.PID != 10 {
		t.Fatalf("swapped to %d", got.PID)
	}
	// Not when the named one has fallen well behind.
	small := ProcView{PID: 10, Start: 1, RSS: 500}
	if got := keepTarget(&a, &b, []ProcView{b, small}); got.PID != 11 {
		t.Fatalf("kept a process at half the size: %d", got.PID)
	}
	// Nor when it has gone.
	if got := keepTarget(&a, &b, []ProcView{b}); got.PID != 11 {
		t.Fatalf("kept a process that is not listed: %d", got.PID)
	}
}
