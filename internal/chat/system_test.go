package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

const gib = 1 << 30

// machine is a monitor a test sets the readings of.
type machine struct {
	mu    sync.Mutex
	cpu   float64
	mem   float64 // percent used
	disk  float64 // percent used
	usage map[string]sysmon.Usage
}

func (m *machine) set(cpu, mem, disk float64) {
	m.mu.Lock()
	m.cpu, m.mem, m.disk = cpu, mem, disk
	m.mu.Unlock()
}

func (m *machine) read(context.Context) (sysmon.Sample, map[string]sysmon.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cpu := m.cpu
	const total = 16 * gib
	return sysmon.Sample{
		CPUReadable: true, CPUPercent: &cpu, Cores: 8, Load1: 3.5, Load5: 2, Load15: 1,
		MemTotal: total, MemAvailable: uint64(float64(total) * (100 - m.mem) / 100),
		SwapTotal: 2 * gib, SwapFree: 2 * gib,
		DiskTotal: 500 * gib, DiskFree: uint64(float64(500*gib) * (100 - m.disk) / 100), DiskPath: "/data",
		Uptime: 3 * 86400,
	}, m.usage
}

func alertsSent(r *rig) []string {
	var out []string
	for _, t := range r.ad.texts() {
		if strings.Contains(t, "机器告警") || strings.Contains(t, "机器恢复") {
			out = append(out, t)
		}
	}
	return out
}

func TestSystemNamesTheMachineAndWhoIsUsingIt(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "build", "claude", session.StateWorking)
	h, _ := r.db.ChatHandle(r.ctx, "s1")
	m := &machine{usage: map[string]sysmon.Usage{"s1": {CPUPercent: 31, RSS: 3 * gib, Procs: 4}}}
	m.set(42, 75, 60)
	r.b.SetMonitor(m.read)
	r.say("me", "系统")
	got := r.ad.last()
	for _, want := range []string{"已运行 3 天", "CPU 42%（8 核）", "负载 3.50", "内存 已用 12.0 GiB / 16.0 GiB（75%）", "磁盘 已用", "（60%）", fmt.Sprintf("[%d] build · CPU 31%%", h), "告警：开"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report lacks %q:\n%s", want, got)
		}
	}
	// Without a monitor it says so rather than making numbers up.
	r.b.SetMonitor(nil)
	r.say("me", "监控")
	if !strings.Contains(r.ad.last(), "读不到") {
		t.Fatalf("no monitor: %q", r.ad.last())
	}
}

// CPU alerts after its duration, once; memory and disk at once; each clears
// only once back under by the margin, and says so.
func TestAlertsFireOnceHoldAndRecover(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("me", store.PeerPaired, store.ModeNormal)
	r.session("s1", "build", "claude", session.StateWorking)
	m := &machine{usage: map[string]sysmon.Usage{"s1": {CPUPercent: 88, RSS: 9 * gib}}}
	r.b.SetMonitor(m.read)

	m.set(95, 40, 40)
	r.b.checkAlerts(r.ctx)
	r.advance(5 * time.Minute)
	r.b.checkAlerts(r.ctx)
	if n := len(alertsSent(r)); n != 0 {
		t.Fatalf("CPU alerted before ten minutes: %q", alertsSent(r))
	}
	r.advance(5 * time.Minute)
	r.b.checkAlerts(r.ctx)
	r.b.checkAlerts(r.ctx)
	if got := alertsSent(r); len(got) != 1 || !strings.Contains(got[0], "CPU 95%，已持续 10 分钟") || !strings.Contains(got[0], "build · CPU 88%") {
		t.Fatalf("cpu alert: %q", got)
	}
	// 85 is under 90 but not by the margin: still one alert, no recovery.
	m.set(85, 40, 40)
	r.b.checkAlerts(r.ctx)
	if len(alertsSent(r)) != 1 {
		t.Fatalf("a machine on the line: %q", alertsSent(r))
	}
	m.set(50, 40, 40)
	r.b.checkAlerts(r.ctx)
	if got := alertsSent(r); len(got) != 2 || !strings.Contains(got[1], "CPU 降到 50%") {
		t.Fatalf("cpu recovery: %q", got)
	}
	// A dip under the threshold restarts the CPU's clock.
	m.set(95, 40, 40)
	r.b.checkAlerts(r.ctx)
	r.advance(9 * time.Minute)
	m.set(70, 40, 40)
	r.b.checkAlerts(r.ctx)
	m.set(95, 40, 40)
	r.advance(2 * time.Minute)
	r.b.checkAlerts(r.ctx)
	if len(alertsSent(r)) != 2 {
		t.Fatalf("the CPU's duration did not restart: %q", alertsSent(r))
	}

	m.set(10, 93, 40)
	r.b.checkAlerts(r.ctx)
	if got := alertsSent(r); len(got) != 3 || !strings.Contains(got[2], "内存已用 93%") || !strings.Contains(got[2], "内存 9.0 GiB") {
		t.Fatalf("memory alert: %q", got)
	}
	// Still over: no second alert.
	r.b.checkAlerts(r.ctx)
	m.set(10, 87, 40)
	r.b.checkAlerts(r.ctx)
	if len(alertsSent(r)) != 3 {
		t.Fatalf("memory alerted again while over: %q", alertsSent(r))
	}
	m.set(10, 80, 96)
	r.b.checkAlerts(r.ctx)
	got := alertsSent(r)
	if len(got) != 5 || !strings.Contains(got[3], "内存降到 80%") || !strings.Contains(got[4], "磁盘已用 96%") || !strings.Contains(got[4], "/data") {
		t.Fatalf("memory recovery and disk: %q", got)
	}
	if !strings.Contains(got[4], "静音告警") {
		t.Fatalf("an alert should say how to pause them: %q", got[4])
	}
}

// Who is told: the destinations, not a person who muted them, not at all when
// the alerts are off.
func TestAlertsGoWhereTheyAreSentAndNotToWhoMutedThem(t *testing.T) {
	r := newRig(t, Capabilities{Proactive: true})
	r.peer("a", store.PeerPaired, store.ModeNormal)
	r.peer("b", store.PeerPaired, store.ModeNormal)
	r.peer("c", store.PeerPaired, store.ModeNormal)
	m := &machine{}
	r.b.SetMonitor(m.read)
	cfg := DefaultAlerts()
	cfg.To = []string{r.ad.kind + ":a", r.ad.kind + ":b"}
	raw, _ := json.Marshal(cfg)
	_ = r.db.SetSetting(r.ctx, AlertsKey, string(raw))

	r.say("b", "静音告警 1小时")
	if !strings.Contains(r.ad.last(), "告警已静音到") {
		t.Fatalf("mute reply: %q", r.ad.last())
	}
	sent := r.ad.count()
	m.set(10, 40, 97)
	r.b.checkAlerts(r.ctx)
	if n := r.ad.count() - sent; n != 1 {
		t.Fatalf("expected one person told (a), got %d: %q", n, r.ad.texts()[sent:])
	}
	// Off: nothing, and switched back on a machine still full is news again.
	cfg.Enabled = false
	raw, _ = json.Marshal(cfg)
	_ = r.db.SetSetting(r.ctx, AlertsKey, string(raw))
	r.b.checkAlerts(r.ctx)
	cfg.Enabled = true
	raw, _ = json.Marshal(cfg)
	_ = r.db.SetSetting(r.ctx, AlertsKey, string(raw))
	sent = r.ad.count()
	r.b.checkAlerts(r.ctx)
	if n := r.ad.count() - sent; n != 1 {
		t.Fatalf("after switching back on: %d", n)
	}
	r.say("b", "恢复告警")
	if until, _ := r.db.ChatMutedUntil(r.ctx, r.ad.kind, "b", systemMute, r.now.Unix()); until != 0 {
		t.Fatal("still muted")
	}
}

func TestAlertThresholdsAreValidated(t *testing.T) {
	for _, bad := range []Alerts{
		{CPUPercent: 40, CPUMinutes: 5, MemPercent: 90, DiskPercent: 90},
		{CPUPercent: 90, CPUMinutes: 0, MemPercent: 90, DiskPercent: 90},
		{CPUPercent: 90, CPUMinutes: 5, MemPercent: 101, DiskPercent: 90},
		{CPUPercent: 90, CPUMinutes: 5, MemPercent: 90, DiskPercent: 30},
		{CPUPercent: 90, CPUMinutes: 5, MemPercent: 90, DiskPercent: 90, To: []string{"telegram"}},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if DefaultAlerts().Validate() != nil || ParseAlerts("{bad").CPUMinutes != DefaultAlerts().CPUMinutes {
		t.Error("defaults")
	}
}
