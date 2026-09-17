package resources

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jiangmuran/vibepanel/internal/cgroup"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// TickInterval is how often the governor looks. The same as the poller: the
// signal it acts on is a ten-second average, and looking more often buys
// nothing but a busier panel on a machine that is already short of breath.
const TickInterval = 2 * time.Second

// Timings, each with what it is for.
const (
	// quietAfterAnswer: no new question for this long after somebody answered
	// one or the panel acted. The stall is a ten-second average and takes that
	// long to fall; asking again in the meantime names the next process for a
	// pressure that is already going away.
	quietAfterAnswer = 20 * time.Second
	// quietAfterFailure: after a kill the kernel refused. Shorter, because
	// nothing was relieved.
	quietAfterFailure = 10 * time.Second
	// warnForgets: a countdown survives a critical stall easing to a warning
	// for this long. A stall hovering around the threshold -- the pattern of
	// the incident this was built after -- restarted the countdown on every
	// dip and the panel never acted.
	warnForgets = 30 * time.Second
	// clearAfterOK: ticks of OK before a question is taken down, for the same
	// hovering stall seen from the other side.
	clearAfterOK = 2
	// emitEvery: the least time between two pushes that differ only in level.
	emitEvery = 10 * time.Second
	// inputWindow: a session typed into this recently is one somebody is using.
	inputWindow = 2 * time.Minute
	// adoptBackoff: between attempts to move a user unit's sessions.
	adoptBackoff = 30 * time.Second
	// growthWindow: how far back "the session that grew" looks.
	growthWindow = time.Minute
	// growthMatters: growth smaller than this is not the story; the biggest is.
	growthMatters = 512 * mib
	// budgetHysteresis: the pool's limit is rewritten only when it moves by
	// more than this. A limit that jitters every two seconds is noise in the
	// kernel's reclaim targets and in anybody's trace.
	budgetHysteresis = 128 * mib
	// actionsKept: the recent actions the page lists.
	actionsKept = 20
)

// SessionMeta is what the governor needs to know about a session that is not
// in a cgroup: who it is to the person, and whether they are using it.
type SessionMeta struct {
	ID       string
	TmuxName string
	// Title is what the session is called on screen, kept with an action so the
	// list of recent actions can still say whose it was after the session has
	// gone.
	Title string
	// Waiting is the session asking for a person, the one state where being
	// slow costs a human's time rather than a machine's.
	Waiting   bool
	LastInput time.Time
}

// Env is everything the governor reads or calls outside this package. What
// changes while the panel runs is a function, so a test can hand it a world
// and nothing here holds a pointer into the HTTP server; the rest is fixed at
// start.
type Env struct {
	Log *slog.Logger
	// Scope is the unit name, from cgroup.ScopeName.
	Scope string
	// Manager is the systemd manager running the panel's own unit, or "" when
	// the panel is not a service, in which case it never adopts anything.
	Manager cgroup.Manager
	// Unit is the panel's own cgroup, where leftovers are looked for.
	Unit cgroup.Dir
	// Prepared says the unit runs `vibepanel service prepare` before the
	// panel, so a server found outside the scope on a system manager is fixed
	// by a restart rather than by nothing.
	Prepared bool
	// Disabled is --isolation=off.
	Disabled bool
	// ScopeMemoryMax replaces the scope's backstop when a user manager creates
	// it. Zero means 95% of the machine. Set only by the browser checks
	// (VIBEPANEL_DEBUG_SCOPE_MEMORY_MAX), which need a pool small enough to
	// fill on purpose without filling the machine they run on.
	ScopeMemoryMax uint64
	// Anchor is the command that holds a user manager's scope open; see
	// cgroup.StartAnchoredScope. The panel's own binary, `service anchor`.
	Anchor []string

	ServerPID func(ctx context.Context) (int, error)
	Panes     func() []Pane
	Sessions  func() []SessionMeta
	Policy    func(ctx context.Context) Policy
	// Emit pushes a change to every viewer.
	Emit func(Event)
	// Audit records an action in the activity log. It must not block: it is
	// called from the tick, which is what has to keep working while the
	// machine stalls.
	Audit func(event, who, detail string)
	// Restart asks the process to be restarted by its supervisor.
	Restart func()
}

// Event is what is pushed to viewers: the question as it stands now, and what
// the panel just did on its own, if anything.
type Event struct {
	Alert *Alert  `json:"alert"`
	Acted *Action `json:"acted,omitempty"`
}

// IsolationState says whether the sessions are in their own cgroup.
type IsolationState string

const (
	Isolated    IsolationState = "isolated"
	NotIsolated IsolationState = "none"
)

// Isolation is whether the sessions are in a cgroup of their own.
type Isolation struct {
	State  IsolationState `json:"state"`
	Reason string         `json:"reason,omitempty"`
	Scope  string         `json:"scope,omitempty"`
	Detail string         `json:"detail,omitempty"`
}

// Why there is no isolation. Codes, because the page says each one in two
// languages and says what to do about it; web/src/i18n.ts has a res.why.<code>
// for every one, and TestEveryIsolationReasonHasWords says so.
const (
	NoneDisabled      = "disabled"       // --isolation=off
	NonePlatform      = "platform"       // not Linux, or no /proc
	NoneCgroupV1      = "cgroup-v1"      // the unified hierarchy is not mounted
	NoneNotService    = "not-a-service"  // started by hand
	NoneNoServer      = "no-server"      // no tmux server yet
	NoneUnitOutdated  = "unit-outdated"  // a system unit without the root helper
	NonePrepareFailed = "prepare-failed" // the root helper ran and did not move it
	NoneRestarting    = "restarting"     // asked systemd for the restart that fixes it
	NoneNotDelegated  = "not-delegated"
	NoneFailed        = "failed"
)

// Reasons lists every code, for the test that pins them to the page's words.
var Reasons = []string{NoneDisabled, NonePlatform, NoneCgroupV1, NoneNotService, NoneNoServer,
	NoneUnitOutdated, NonePrepareFailed, NoneRestarting, NoneNotDelegated, NoneFailed}

// Priority is a session's claim on CPU while the sessions compete for it.
type Priority string

const (
	PriorityHigh   Priority = "high"   // in use, or waiting on somebody
	PriorityNormal Priority = "normal" //
	PriorityLow    Priority = "low"    // taking most of a CPU the machine is short of
)

// ProcView is one process as the page shows it.
type ProcView struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
	Name  string `json:"name"`
	RSS   uint64 `json:"rss"`
	// Root marks the pane's own process: ending it ends the session.
	Root bool `json:"root,omitempty"`
}

// SessionView is one session's share.
type SessionView struct {
	ID       string `json:"id"`
	TmuxName string `json:"-"`
	Memory   uint64 `json:"memory"`
	// Held is the part of Memory that only leaves when a process does: anon
	// and shmem. The rest is page cache. See Reading.PoolHeld.
	Held       uint64  `json:"held"`
	CPUPercent float64 `json:"cpuPercent"`
	// Procs is how many processes there are, so a page listing the largest
	// few can say how many it left out.
	Procs    int        `json:"procs"`
	Frozen   bool       `json:"frozen"`
	Priority Priority   `json:"priority"`
	Top      []ProcView `json:"top,omitempty"`
}

// Alert is the question the panel is asking, or the warning it is giving.
type Alert struct {
	ID     string `json:"id"`
	Level  string `json:"level"`
	Reason Reason `json:"reason"`
	// SessionID is the session named, or "" when no session holds enough to be
	// the cause -- pressure from outside the sessions, or spread across them.
	SessionID   string    `json:"sessionId,omitempty"`
	Proc        *ProcView `json:"proc,omitempty"`
	PoolCurrent uint64    `json:"poolCurrent"`
	PoolMax     uint64    `json:"poolMax"`
	Available   uint64    `json:"available"`
	Total       uint64    `json:"total"`
	// AutoAt is when the panel ends Proc on its own if nobody answers; 0 when
	// it will not.
	AutoAt int64 `json:"autoAt,omitempty"`
	// CanPause and CanBoost say which answers mean anything now: pausing needs
	// the sessions isolated, a boost is nothing under Performance, and neither
	// is a boost under machine pressure, which is about the host and not the
	// pool a boost widens.
	CanPause bool `json:"canPause,omitempty"`
	CanBoost bool `json:"canBoost,omitempty"`
}

// ActionKind is what was done.
type ActionKind string

const (
	ActionKill   ActionKind = "kill"
	ActionFreeze ActionKind = "freeze"
	ActionThaw   ActionKind = "thaw"
	ActionBoost  ActionKind = "boost"
	// ActionBoostEnded is a boost ended early. Its own kind, because a list
	// that showed the boost and not its end said the limit was still raised.
	ActionBoostEnded ActionKind = "boost_ended"
	// ActionOOM is the kernel ending a process in a session: the panel did
	// not do it, but without saying so a session just exited with nothing on
	// any page to say why.
	ActionOOM ActionKind = "oom"
)

// Action is something done about memory, by a person or by the panel.
type Action struct {
	At        int64      `json:"at"`
	Kind      ActionKind `json:"kind"`
	Auto      bool       `json:"auto"`
	SessionID string     `json:"sessionId,omitempty"`
	// Session is the session's name at the time.
	Session string `json:"session,omitempty"`
	Name    string `json:"name,omitempty"`
	RSS     uint64 `json:"rss,omitempty"`
	Who     string `json:"who,omitempty"`
}

// Bounds are a custom policy's limits, sent so the page's fields and the
// server's refusal are one set of numbers.
type Bounds struct {
	MinPool  int `json:"minPool"`
	MaxPool  int `json:"maxPool"`
	MinAsk   int `json:"minAsk"`
	MaxAsk   int `json:"maxAsk"`
	MinGrace int `json:"minGrace"`
	MaxGrace int `json:"maxGrace"`
}

// View is the whole picture, for the page.
type View struct {
	Supported bool          `json:"supported"`
	Isolation Isolation     `json:"isolation"`
	Policy    Policy        `json:"policy"`
	Params    Params        `json:"params"`
	Presets   []Params      `json:"presets"`
	Bounds    Bounds        `json:"bounds"`
	Level     string        `json:"level"`
	Total     uint64        `json:"total"`
	Available uint64        `json:"available"`
	Pool      PoolView      `json:"pool"`
	Panel     uint64        `json:"panel"`
	Tmux      uint64        `json:"tmux"`
	Sessions  []SessionView `json:"sessions"`
	Alert     *Alert        `json:"alert"`
	Actions   []Action      `json:"actions"`
	At        int64         `json:"at"`
}

// PoolView is the sessions' budget.
type PoolView struct {
	Current uint64 `json:"current"`
	Held    uint64 `json:"held"`
	// Max is the memory.max in force; 0 when there is no pool.
	Max   uint64  `json:"max"`
	Stall float64 `json:"stall"`
}

type procKey struct {
	PID   int
	Start uint64
}

// Governor is the loop.
//
// Two locks, and each field belongs to one of them. tickMu is held for a whole
// tick and guards t, which only the tick reads. mu guards what request
// handlers read or change between ticks, and is only ever held briefly.
type Governor struct {
	env       Env
	supported bool
	self      procKey

	tickMu sync.Mutex
	t      tickState

	mu       sync.Mutex
	view     View
	layout   *Layout
	server   procKey
	snoozed  map[string]time.Time
	actions  []Action
	quiet    time.Time
	lastEmit time.Time
}

type tickState struct {
	growth     map[string][]memSample // by session id
	cpuPrev    map[string]cpuSample   // by session id
	hotSince   map[string]time.Time   // by session id
	ooms       map[string]uint64      // by session id: oom_kill as last read
	refaults   memSample
	unkillable map[procKey]bool
	lastMax    uint64
	adoptTry   time.Time
	restarted  time.Time
	started    time.Time
	warnSince  time.Time
	okTicks    int
	released   bool
}

type memSample struct {
	at  time.Time
	cur uint64
}

type cpuSample struct {
	at    time.Time
	usage uint64
}

// New makes a governor. Run starts it.
func New(env Env) *Governor {
	if env.Log == nil {
		env.Log = slog.Default()
	}
	g := &Governor{
		env:       env,
		supported: Supported(),
		snoozed:   map[string]time.Time{},
		t: tickState{
			growth:     map[string][]memSample{},
			cpuPrev:    map[string]cpuSample{},
			hotSince:   map[string]time.Time{},
			ooms:       map[string]uint64{},
			unkillable: map[procKey]bool{},
			started:    time.Now(),
		},
	}
	if p, ok := sysmon.ReadProc(os.Getpid()); ok {
		g.self = procKey{PID: p.PID, Start: p.Start}
	}
	return g
}

// Run ticks until ctx ends. Its own goroutine, never the poller's: a tick
// reads cgroupfs and /proc, and on the machine this exists for, /proc is the
// slow part.
func (g *Governor) Run(ctx context.Context) {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	g.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Tick(ctx)
		}
	}
}

// Supported reports whether this platform has anything to govern.
func Supported() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}

// ServerStarted is called when the panel has just started a tmux server, from
// inside the request that needed one. It records the server, forgets the
// adoption back-off, and ticks, so the new server is in the scope before
// anything is created in it.
//
// On its own context with its own deadline: the request's would cancel an
// adoption half-way when the browser went away.
func (g *Governor) ServerStarted(ctx context.Context, pid int) {
	if p, ok := sysmon.ReadProc(pid); ok {
		g.mu.Lock()
		g.server = procKey{PID: pid, Start: p.Start}
		g.mu.Unlock()
	}
	g.tickMu.Lock()
	g.t.adoptTry = time.Time{}
	g.tickMu.Unlock()
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	g.Tick(tctx)
}

// Tick looks once and does whatever that calls for.
func (g *Governor) Tick(ctx context.Context) {
	g.tickMu.Lock()
	defer g.tickMu.Unlock()
	now := time.Now()
	policy := DefaultPolicy()
	if g.env.Policy != nil {
		policy = g.env.Policy(ctx)
	}
	params := policy.At(now)
	total, avail := sysmon.Memory()
	procs := LazyProcs()
	ro := g.roster()
	for k := range g.t.unkillable {
		if p, ok := sysmon.ReadProc(k.PID); !ok || p.Start != k.Start {
			delete(g.t.unkillable, k)
		}
	}

	iso, layout := g.place(ctx, ro, procs)

	r := Reading{Total: total, Available: avail, HostStall: hostPressure("memory").Full10}
	pool := PoolView{}
	if layout != nil {
		// A memory.stat that cannot be read is not a pool that holds nothing:
		// a budget built on that zero fell to its floor with every session
		// still in it, and the kernel killed them before the next tick's
		// successful read put the limit back.
		poolHeld, heldOK := held(layout.Pool)
		if heldOK {
			g.applyBudget(layout, Budget(params, total, avail, poolHeld, limitOf(layout.Scope)))
		}
		pool.Held = poolHeld
		pool.Current, _ = layout.Pool.Uint("memory.current")
		pool.Max = limitOf(layout.Pool)
		mem, _ := layout.Pool.Pressure("memory")
		io, _ := layout.Pool.Pressure("io")
		r.PoolCurrent, r.PoolMax, r.PoolHeld = pool.Current, pool.Max, pool.Held
		r.PoolStall, r.PoolIOStall = mem.Full10, io.Full10
		r.PoolRefaults = g.refaultRate(layout.Pool, now)
		pool.Stall = r.Stall()
	}

	sessions := g.readSessions(layout, ro, now)
	g.demote(sessions, hostPressure("cpu").Some10, now)
	g.applyWeights(layout, sessions)

	level, reason := Assess(params, r)
	g.decide(now, params, level, reason, r, sessions, layout, ro, procs)

	tmuxMem := uint64(0)
	if layout != nil {
		tmuxMem, _ = layout.Tmux().Uint("memory.current")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.layout = layout
	g.view = View{
		Supported: g.supported,
		Isolation: iso,
		Policy:    policy,
		Params:    params,
		Level:     level.String(),
		Total:     total,
		Available: avail,
		Pool:      pool,
		Panel:     ownMemory(),
		Tmux:      tmuxMem,
		Sessions:  sessions,
		Alert:     g.view.Alert,
		At:        now.Unix(),
	}
}

// refaultRate is workingset_refault_file per second since the last tick.
//
// A read that fails keeps the previous sample rather than storing zero: a zero
// followed by the real cumulative counter computed a refault rate of everything
// the pool had ever refaulted over two seconds, and an npm install on a slow
// disk read as the memory stall this rate exists to rule out.
func (g *Governor) refaultRate(d cgroup.Dir, now time.Time) float64 {
	st, err := d.KeyValues("memory.stat")
	if err != nil {
		return 0
	}
	cur := st["workingset_refault_file"]
	prev := g.t.refaults
	g.t.refaults = memSample{at: now, cur: cur}
	if prev.at.IsZero() || cur < prev.cur {
		return 0
	}
	el := now.Sub(prev.at).Seconds()
	if el <= 0 {
		return 0
	}
	return float64(cur-prev.cur) / el
}

// applyBudget writes the pool's memory.max when it has moved enough to matter.
func (g *Governor) applyBudget(l *Layout, want uint64) {
	if want == 0 {
		return
	}
	cur, err := l.Pool.Uint("memory.max")
	if err == nil && cur != cgroup.Unlimited && g.t.lastMax != 0 {
		diff := int64(want) - int64(cur)
		if diff < 0 {
			diff = -diff
		}
		if diff < budgetHysteresis {
			return
		}
	}
	if err := l.Pool.SetUint("memory.max", want); err != nil {
		g.env.Log.Warn("setting the sessions' memory budget", "err", err)
		return
	}
	g.t.lastMax = want
}

// View returns the picture from the last tick, with each session's largest
// processes read now -- only when somebody is looking, since it reads /proc
// for every process in every session.
func (g *Governor) View() View {
	g.mu.Lock()
	v := g.view
	l := g.layout
	v.Sessions = append([]SessionView(nil), v.Sessions...)
	v.Actions = append([]Action{}, g.actions...)
	g.mu.Unlock()
	// The policy as stored now, not as the last tick read it: this is what a
	// save answers with, and a mode that snaps back for two seconds after it
	// was chosen reads as the choice not having taken.
	if g.env.Policy != nil {
		v.Policy = g.env.Policy(context.Background())
		v.Params = v.Policy.At(time.Now())
	}
	v.Presets = []Params{Presets[Conservative], Presets[Balanced], Presets[Performance]}
	v.Bounds = Bounds{MinPoolPercent, MaxPoolPercent, MinAskPercent, MaxAskPercent, MinGraceSeconds, MaxGraceSeconds}
	ro := g.roster()
	procs := LazyProcs()
	for i := range v.Sessions {
		v.Sessions[i].Top = g.top(v.Sessions[i].TmuxName, l, ro, procs, 5, nil)
	}
	if v.Sessions == nil {
		v.Sessions = []SessionView{}
	}
	return v
}

// Alert is the question standing now, for a page that has just opened.
func (g *Governor) Alert() *Alert {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.view.Alert
}

// Isolation is the last tick's answer.
func (g *Governor) Isolation() Isolation {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.view.Isolation
}

// Frozen lists the sessions that are paused, for the snapshot: a paused session
// has to look paused everywhere, not only on the resources page.
func (g *Governor) Frozen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, s := range g.view.Sessions {
		if s.Frozen {
			out = append(out, s.ID)
		}
	}
	return out
}

// setAlert replaces the question and tells viewers when that is a change a
// person would notice: a new question, a countdown starting or stopping, the
// question going away, or -- at most every emitEvery -- a change of level. The
// numbers inside move every tick and the page that is open reads them from the
// view.
func (g *Governor) setAlert(a *Alert, now time.Time) {
	g.mu.Lock()
	prev := g.view.Alert
	g.view.Alert = a
	emit := (prev == nil) != (a == nil) ||
		(a != nil && (prev.ID != a.ID || prev.AutoAt != a.AutoAt ||
			(prev.Level != a.Level && now.Sub(g.lastEmit) >= emitEvery)))
	if emit {
		g.lastEmit = now
	}
	g.mu.Unlock()
	if emit && g.env.Emit != nil {
		g.env.Emit(Event{Alert: a})
	}
}

// quietened re-reads the answer deadline during a tick that is already past
// its first read. The reads between -- the culprit's process list out of /proc,
// which is the slow part of a tick on exactly the machine this runs on -- take
// real time, and an answer that lands inside them must not be overwritten by
// the question it answered, or acted on.
func (g *Governor) quietened() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.quiet)
}

// answered takes the question down after somebody acted on the session it
// names, and keeps the next one back while the pressure falls.
func (g *Governor) answered(sessionID string) {
	g.mu.Lock()
	g.quiet = time.Now().Add(quietAfterAnswer)
	a := g.view.Alert
	g.mu.Unlock()
	if a != nil && a.SessionID == sessionID {
		g.setAlert(nil, time.Now())
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (g *Governor) remember(a Action) {
	g.mu.Lock()
	g.actions = append([]Action{a}, g.actions...)
	if len(g.actions) > actionsKept {
		g.actions = g.actions[:actionsKept]
	}
	g.mu.Unlock()
}

func (g *Governor) record(a Action, detail string) {
	if a.SessionID != "" && a.Session == "" {
		a.Session = g.roster().byID[a.SessionID].Title
	}
	g.remember(a)
	if g.env.Audit != nil {
		event := "resources." + string(a.Kind)
		if a.Auto {
			event += ".auto"
		}
		g.env.Audit(event, a.Who, detail)
	}
}

// NoteBoost puts a boost in the page's list of recent actions. The handler
// that stored it has already written the audit row.
func (g *Governor) NoteBoost(who string, ended bool) {
	kind := ActionBoost
	if ended {
		kind = ActionBoostEnded
	}
	g.remember(Action{At: time.Now().Unix(), Kind: kind, Who: who})
}

// Errors an action can answer with.
var (
	ErrUnknownSession = errors.New("no such session")
	ErrGone           = errors.New("that process has already exited")
	ErrNotInSession   = errors.New("that process is not in this session")
	ErrNoFreezer      = errors.New("pausing needs the sessions to be isolated")
	ErrNoAlert        = errors.New("that question has already been answered")
)

// hostPressure reads /proc/pressure/<resource>.
func hostPressure(resource string) cgroup.Pressure {
	b, err := os.ReadFile("/proc/pressure/" + resource)
	if err != nil {
		return cgroup.Pressure{}
	}
	return cgroup.ParsePressure(string(b))
}

// limitOf reads memory.max, with "max" as 0: no limit, and the one value every
// comparison against it can treat as "there is no pool".
func limitOf(d cgroup.Dir) uint64 {
	v, err := d.Uint("memory.max")
	if err != nil || v == cgroup.Unlimited {
		return 0
	}
	return v
}

// held is anon plus shmem from memory.stat: what a cgroup holds that reclaim
// cannot take back while swap is off. The bool says the stat was read: false
// means unknown, not zero, and callers that build anything on the number must
// keep what they had rather than act on the zero.
func held(d cgroup.Dir) (uint64, bool) {
	st, err := d.KeyValues("memory.stat")
	if err != nil {
		return 0, false
	}
	return st["anon"] + st["shmem"], true
}

// ownMemory is what the panel's own cgroup holds, or its RSS where that
// cannot be read.
func ownMemory() uint64 {
	if rel, err := cgroup.Of(0); err == nil {
		if v, err := cgroup.At(rel).Uint("memory.current"); err == nil && v != cgroup.Unlimited {
			return v
		}
	}
	if p, ok := sysmon.ReadProc(os.Getpid()); ok {
		return p.RSS
	}
	return 0
}

// isTmux reports whether a process is still the tmux server it was recorded as.
func isTmux(k procKey) bool {
	p, ok := sysmon.ReadProc(k.PID)
	return ok && p.Start == k.Start && strings.HasPrefix(p.Comm, "tmux")
}
