package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/sysmon"
)

// The machine, on a phone: what it is doing when asked, and a message when it
// is running out of something.
//
// A session is rarely the thing that goes wrong at night. A build that eats
// the memory, a disk filled by logs, a runaway test holding every core: the
// agents stop making progress and nothing in a session card says why. So the
// bridge reads what the panel's own monitor reads, answers "系统" with it, and
// watches three numbers against thresholds the owner sets on the Messaging page.
//
// Everything here is local reads of /proc. An alert goes out only through a
// channel already switched on, to the people already paired, so it asks
// nothing new of the consent the channel was switched on with.

// Monitor is what the bridge reads about the machine: the panel's sample, and
// each session's process tree, by session id. Nil when the panel has none.
type Monitor func(ctx context.Context) (sysmon.Sample, map[string]sysmon.Usage)

// AlertsKey is the settings row holding the alert thresholds, as JSON.
const AlertsKey = "chat.alerts"

// Alerts is when the machine is worth a message.
type Alerts struct {
	Enabled bool `json:"enabled"`
	// CPUPercent held for CPUMinutes: a build pegging the cores for a few
	// seconds is the machine working, and a quarter of an hour is not.
	CPUPercent int `json:"cpuPercent"`
	CPUMinutes int `json:"cpuMinutes"`
	// MemPercent and DiskPercent are how much is used, not how much is left:
	// the numbers people say out loud.
	MemPercent  int `json:"memPercent"`
	DiskPercent int `json:"diskPercent"`
	// To is who is told, in the routing rules' form: "*" or "channel:peer".
	To []string `json:"to"`
}

// DefaultAlerts is what a panel that never saved any uses: on, to everyone
// paired, at levels that mean trouble rather than a busy afternoon.
func DefaultAlerts() Alerts {
	return Alerts{Enabled: true, CPUPercent: 90, CPUMinutes: 10, MemPercent: 90, DiskPercent: 95, To: []string{"*"}}
}

// ParseAlerts reads the setting, falling back to the defaults for anything
// unreadable.
func ParseAlerts(raw string) Alerts {
	a := DefaultAlerts()
	if raw == "" {
		return a
	}
	var stored Alerts
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || stored.Validate() != nil {
		return a
	}
	return stored
}

// Validate refuses thresholds that would alert all the time or never.
func (a Alerts) Validate() error {
	switch {
	case a.CPUPercent < 50 || a.CPUPercent > 100:
		return fmt.Errorf("cpuPercent %d is not 50..100", a.CPUPercent)
	case a.CPUMinutes < 1 || a.CPUMinutes > 120:
		return fmt.Errorf("cpuMinutes %d is not 1..120", a.CPUMinutes)
	case a.MemPercent < 50 || a.MemPercent > 100:
		return fmt.Errorf("memPercent %d is not 50..100", a.MemPercent)
	case a.DiskPercent < 50 || a.DiskPercent > 100:
		return fmt.Errorf("diskPercent %d is not 50..100", a.DiskPercent)
	}
	for _, d := range a.To {
		if d != "*" && !strings.Contains(d, ":") {
			return fmt.Errorf("destination %q is not \"*\" or channel:peer", d)
		}
	}
	return nil
}

// systemMute is the session id a person's mute of the alerts is stored under:
// not a session id anything can have, in the table session mutes already use.
const systemMute = "@system"

// DefaultAlertEvery is how often the machine is looked at for alerts.
const DefaultAlertEvery = 30 * time.Second

// The hysteresis: an alert clears only once the number is this far back under
// its threshold, so a machine sitting on the line is one alert and not one a
// minute.
const (
	cpuClearBelow  = 10
	memClearBelow  = 5
	diskClearBelow = 2
)

// alarm is one watched number's state.
type alarm struct {
	// over is when it first went over, for the CPU's duration; zero when not.
	over time.Time
	// firing is whether an alert has gone out that no recovery has followed.
	firing bool
}

func percentUsed(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// SetMonitor installs or removes the machine reader.
func (b *Bridge) SetMonitor(m Monitor) {
	b.mu.Lock()
	b.d.Monitor = m
	b.mu.Unlock()
}

func (b *Bridge) monitor() Monitor {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.d.Monitor
}

// watch looks at the machine every AlertEvery until ctx ends.
func (b *Bridge) watch(ctx context.Context) {
	t := time.NewTicker(b.d.AlertEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.checkAlerts(ctx)
		}
	}
}

// checkAlerts reads the machine once and sends what changed.
func (b *Bridge) checkAlerts(ctx context.Context) {
	raw, _ := b.d.DB.GetSetting(ctx, AlertsKey, "")
	cfg := ParseAlerts(raw)
	b.mu.Lock()
	if !cfg.Enabled {
		// Off means no memory of what was firing, either: switched back on,
		// a machine still out of disk is news again.
		b.alarms = map[string]*alarm{}
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	read := b.monitor()
	if read == nil {
		return
	}
	sample, usage := read(ctx)
	now := b.d.Now()
	lang := b.language()

	// The decisions under the lock, the words after it: naming the session
	// behind an alert looks up its handle, which takes the same lock.
	type event struct {
		kind    string
		firing  bool
		pct     float64
		minutes int
	}
	var events []event
	b.mu.Lock()
	get := func(k string) *alarm {
		a, ok := b.alarms[k]
		if !ok {
			a = &alarm{}
			b.alarms[k] = a
		}
		return a
	}
	if sample.CPUReadable && sample.CPUPercent != nil {
		pct := *sample.CPUPercent
		a := get("cpu")
		switch {
		case pct >= float64(cfg.CPUPercent):
			if a.over.IsZero() {
				a.over = now
			}
			if !a.firing && now.Sub(a.over) >= time.Duration(cfg.CPUMinutes)*time.Minute {
				a.firing = true
				events = append(events, event{"cpu", true, pct, int(now.Sub(a.over).Minutes())})
			}
		case a.firing && pct < float64(cfg.CPUPercent-cpuClearBelow):
			a.firing, a.over = false, time.Time{}
			events = append(events, event{"cpu", false, pct, 0})
		case pct < float64(cfg.CPUPercent):
			a.over = time.Time{}
		}
	}
	if sample.MemTotal > 0 {
		pct := percentUsed(sample.MemTotal-sample.MemAvailable, sample.MemTotal)
		a := get("mem")
		switch {
		case !a.firing && pct >= float64(cfg.MemPercent):
			a.firing = true
			events = append(events, event{"mem", true, pct, 0})
		case a.firing && pct < float64(cfg.MemPercent-memClearBelow):
			a.firing = false
			events = append(events, event{"mem", false, pct, 0})
		}
	}
	if sample.DiskTotal > 0 {
		pct := percentUsed(sample.DiskTotal-sample.DiskFree, sample.DiskTotal)
		a := get("disk")
		switch {
		case !a.firing && pct >= float64(cfg.DiskPercent):
			a.firing = true
			events = append(events, event{"disk", true, pct, 0})
		case a.firing && pct < float64(cfg.DiskPercent-diskClearBelow):
			a.firing = false
			events = append(events, event{"disk", false, pct, 0})
		}
	}
	b.mu.Unlock()

	var say []string
	for _, e := range events {
		switch {
		case e.kind == "cpu" && e.firing:
			say = append(say, msg(lang, "alertCPU", e.pct, e.minutes)+b.topLine(ctx, usage, false, lang))
		case e.kind == "cpu":
			say = append(say, msg(lang, "recoveredCPU", e.pct))
		case e.kind == "mem" && e.firing:
			say = append(say, msg(lang, "alertMem", e.pct, sysmon.FormatBytes(sample.MemAvailable))+b.topLine(ctx, usage, true, lang))
		case e.kind == "mem":
			say = append(say, msg(lang, "recoveredMem", e.pct))
		case e.kind == "disk" && e.firing:
			say = append(say, msg(lang, "alertDisk", e.pct, sysmon.FormatBytes(sample.DiskFree), sample.DiskPath))
		default:
			say = append(say, msg(lang, "recoveredDisk", e.pct))
		}
	}

	for _, text := range say {
		b.d.Audit(ctx, "chat.alert", firstLine(text))
		b.broadcast(ctx, cfg.To, text+"\n"+msg(lang, "alertHint"))
	}
}

// broadcast sends a line to every paired person the destinations name who
// has not muted the alerts, through channels that are running.
func (b *Bridge) broadcast(ctx context.Context, to []string, text string) {
	peers, err := b.d.DB.PairedChatPeers(ctx)
	if err != nil {
		return
	}
	now := b.d.Now().Unix()
	for _, p := range peers {
		if !Destined(to, p.Channel, p.PeerID) {
			continue
		}
		ch, ok := b.channel(p.Channel)
		if !ok || ch.placeholder {
			continue
		}
		if until, _ := b.d.DB.ChatMutedUntil(ctx, p.Channel, p.PeerID, systemMute, now); until > 0 {
			continue
		}
		if !ch.caps.Proactive && p.ContextToken == "" {
			b.undelivered(ctx, ch, p, "", "", ErrNeedsHello)
			continue
		}
		b.reply(ctx, ch, p, text)
	}
}

// consumer is one session's share of the machine, for "who is it".
type consumer struct {
	handle int
	title  string
	u      sysmon.Usage
}

// consumers is the sessions using the most, by CPU or by memory.
func (b *Bridge) consumers(ctx context.Context, usage map[string]sysmon.Usage, byMemory bool, n int) []consumer {
	var out []consumer
	for id, u := range usage {
		row, err := b.d.DB.GetSession(ctx, id)
		if err != nil || !Addressable(row) {
			continue
		}
		h, err := b.Handle(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, consumer{handle: h, title: row.Title, u: u})
	}
	sort.Slice(out, func(i, j int) bool {
		if byMemory {
			return out[i].u.RSS > out[j].u.RSS
		}
		return out[i].u.CPUPercent > out[j].u.CPUPercent
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// topLine names the session using the most of what an alert is about, if one
// is using a noticeable part of it.
func (b *Bridge) topLine(ctx context.Context, usage map[string]sysmon.Usage, byMemory bool, lang string) string {
	top := b.consumers(ctx, usage, byMemory, 1)
	if len(top) == 0 {
		return ""
	}
	c := top[0]
	if byMemory {
		if c.u.RSS == 0 {
			return ""
		}
		return "\n" + msg(lang, "alertTopMem", c.handle, c.title, sysmon.FormatBytes(c.u.RSS))
	}
	if c.u.CPUPercent < 1 {
		return ""
	}
	return "\n" + msg(lang, "alertTopCPU", c.handle, c.title, c.u.CPUPercent)
}

// systemReport answers "系统".
func (b *Bridge) systemReport(ctx context.Context, p store.ChatPeer, lang string) string {
	read := b.monitor()
	if read == nil {
		return msg(lang, "systemUnavailable")
	}
	s, usage := read(ctx)
	var lines []string
	lines = append(lines, msg(lang, "systemHead", uptime(s.Uptime, lang)))
	switch {
	case !s.CPUReadable:
	case s.CPUPercent == nil:
		lines = append(lines, msg(lang, "systemCPUWait", s.Cores, s.Load1, s.Load5, s.Load15))
	default:
		lines = append(lines, msg(lang, "systemCPU", *s.CPUPercent, s.Cores, s.Load1, s.Load5, s.Load15))
	}
	if s.MemTotal > 0 {
		used := s.MemTotal - s.MemAvailable
		lines = append(lines, msg(lang, "systemMem", sysmon.FormatBytes(used), sysmon.FormatBytes(s.MemTotal), percentUsed(used, s.MemTotal)))
	}
	if s.SwapTotal > 0 {
		lines = append(lines, msg(lang, "systemSwap", sysmon.FormatBytes(s.SwapTotal-s.SwapFree), sysmon.FormatBytes(s.SwapTotal)))
	}
	if s.DiskTotal > 0 {
		used := s.DiskTotal - s.DiskFree
		lines = append(lines, msg(lang, "systemDisk", sysmon.FormatBytes(used), sysmon.FormatBytes(s.DiskTotal), percentUsed(used, s.DiskTotal), sysmon.FormatBytes(s.DiskFree)))
	}
	if top := b.consumers(ctx, usage, false, 3); len(top) > 0 {
		lines = append(lines, msg(lang, "systemTop"))
		for _, c := range top {
			lines = append(lines, fmt.Sprintf("  [%d] %s · CPU %.0f%% · %s", c.handle, c.title, c.u.CPUPercent, sysmon.FormatBytes(c.u.RSS)))
		}
	}
	raw, _ := b.d.DB.GetSetting(ctx, AlertsKey, "")
	cfg := ParseAlerts(raw)
	switch {
	case !cfg.Enabled:
		lines = append(lines, msg(lang, "systemAlertsOff"))
	default:
		line := msg(lang, "systemAlertsOn", cfg.CPUPercent, cfg.CPUMinutes, cfg.MemPercent, cfg.DiskPercent)
		if until, _ := b.d.DB.ChatMutedUntil(ctx, p.Channel, p.PeerID, systemMute, b.d.Now().Unix()); until > 0 {
			line += msg(lang, "systemAlertsMuted", time.Unix(until, 0).In(b.d.Zone()).Format("01-02 15:04"))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// uptime says how long the machine has been up the way people say it.
func uptime(seconds int64, lang string) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf(pick(lang, "%d 天", "%dd"), int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf(pick(lang, "%d 小时", "%dh"), int(d.Hours()))
	}
	return fmt.Sprintf(pick(lang, "%d 分钟", "%dm"), int(d.Minutes()))
}
