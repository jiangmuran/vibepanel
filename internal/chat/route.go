package chat

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Which changes reach which chats.
//
// Rules are read top down and the first match decides; when none matches the
// default applies. A rule that matches and names no destination is how a
// session is silenced for everybody, and a destination of "*" is every paired
// peer. What a rule cannot do is widen who may be talked to: destinations are
// checked against the paired peers at send time, so a rule naming a peer who
// has since been removed names nobody.

// RoutesKey is the settings row holding the rules, as JSON.
const RoutesKey = "chat.routes"

// Rule is one line of the routing table.
type Rule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Match   Match  `json:"match"`
	// To is the destinations: "channel:peer" strings, or "*" for every
	// paired peer. Empty means send to nobody, which is the point of a rule
	// that exists to silence.
	To []string `json:"to"`
	// Screenshot is when to attach a picture of the pane: "auto" (only for
	// full-screen programs), "always", "never".
	Screenshot string `json:"screenshot"`
	// CoalesceSeconds is how long to wait for a state to settle before
	// telling anyone. Zero takes the default.
	CoalesceSeconds int `json:"coalesceSeconds"`
	// QuietHours is "HH:MM-HH:MM" in the panel's zone; matches inside the
	// window are held until it ends, except prompts, which are sent anyway
	// because an agent blocked at 3am is the case the whole feature is for.
	QuietHours string `json:"quietHours"`
	// Body includes what the agent said in the card; off sends the state
	// line only, for people who consider the text private to the machine.
	Body bool `json:"body"`
}

// Match is what a rule applies to. Every empty list matches everything, so
// the zero Match is "all sessions on every change".
type Match struct {
	Projects []string `json:"projects"`
	Sessions []string `json:"sessions"`
	Tools    []string `json:"tools"`
	States   []string `json:"states"`
	Kinds    []string `json:"kinds"`
}

// Routes is the whole table.
type Routes struct {
	Rules []Rule `json:"rules"`
	// Default applies when no rule matches. Its Match is ignored.
	Default Rule `json:"default"`
}

// Screenshot policies.
const (
	ShotAuto   = "auto"
	ShotAlways = "always"
	ShotNever  = "never"
)

// DefaultCoalesce is how long a change is held before it is sent. Three
// seconds is longer than the flicker an agent produces between tool calls
// and shorter than anyone notices on a phone. A variable so the bridge's
// tests can run in seconds rather than minutes; nothing else writes it.
var DefaultCoalesce = 3 * time.Second

// DefaultRoutes is what a fresh panel does: waiting and done, to everyone,
// with what the agent said, a picture only for full-screen programs.
func DefaultRoutes() Routes {
	return Routes{Default: Rule{
		Enabled: true, To: []string{"*"}, Screenshot: ShotAuto, Body: true,
		Match: Match{States: []string{"waiting", "done"}},
	}}
}

// ParseRoutes reads the setting, falling back to the default table when the
// row is empty or does not parse.
func ParseRoutes(raw string) Routes {
	if raw == "" {
		return DefaultRoutes()
	}
	var r Routes
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return DefaultRoutes()
	}
	if r.Default.Screenshot == "" {
		r.Default.Screenshot = ShotAuto
	}
	if r.Default.To == nil {
		r.Default.To = []string{"*"}
	}
	r.Default.Enabled = true
	return r
}

// Validate refuses a table the bridge could not apply.
func (r Routes) Validate() error {
	if len(r.Rules) > 100 {
		return fmt.Errorf("more than 100 rules")
	}
	for i, rule := range append(r.Rules, r.Default) {
		if err := rule.validate(); err != nil {
			if i == len(r.Rules) {
				return fmt.Errorf("default: %w", err)
			}
			return fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	return nil
}

func (rule Rule) validate() error {
	switch rule.Screenshot {
	case "", ShotAuto, ShotAlways, ShotNever:
	default:
		return fmt.Errorf("screenshot %q is not auto, always or never", rule.Screenshot)
	}
	if rule.CoalesceSeconds < 0 || rule.CoalesceSeconds > 600 {
		return fmt.Errorf("coalesce %d is not 0..600 seconds", rule.CoalesceSeconds)
	}
	if rule.QuietHours != "" {
		if _, _, err := parseQuiet(rule.QuietHours); err != nil {
			return err
		}
	}
	for _, s := range rule.Match.States {
		switch s {
		case "waiting", "working", "done":
		default:
			return fmt.Errorf("state %q", s)
		}
	}
	for _, k := range rule.Match.Kinds {
		switch k {
		case "assistant", "prompt", "question", "notice", "user":
		default:
			return fmt.Errorf("kind %q", k)
		}
	}
	for _, to := range rule.To {
		if to != "*" && !strings.Contains(to, ":") {
			return fmt.Errorf("destination %q is not channel:peer or *", to)
		}
	}
	return nil
}

// Change is one thing that happened to a session, as routing sees it.
type Change struct {
	SessionID string
	ProjectID string
	Tool      string
	State     string
	// Kind is the latest message's kind, or "" when the change carried no
	// message.
	Kind string
}

// Decision is what to do about a Change.
type Decision struct {
	// Send is false when the change is not worth telling anyone about.
	Send bool
	// To is the destinations, still to be intersected with the paired peers.
	To         []string
	Screenshot string
	Coalesce   time.Duration
	// Hold is true inside quiet hours for a change that can wait.
	Hold bool
	Body bool
	// Rule is which rule decided, for the "why did this go here" preview.
	Rule string
}

// Decide finds the first enabled rule that matches, else the default.
func (r Routes) Decide(c Change, now time.Time) Decision {
	for _, rule := range r.Rules {
		if !rule.Enabled || !rule.Match.matches(c) {
			continue
		}
		return rule.decision(c, now, rule.Name)
	}
	if !r.Default.Match.matches(c) {
		return Decision{Rule: "default"}
	}
	return r.Default.decision(c, now, "default")
}

func (rule Rule) decision(c Change, now time.Time, name string) Decision {
	d := Decision{
		Send: len(rule.To) > 0, To: rule.To, Screenshot: rule.Screenshot,
		Coalesce: time.Duration(rule.CoalesceSeconds) * time.Second, Body: rule.Body, Rule: name,
	}
	if d.Screenshot == "" {
		d.Screenshot = ShotAuto
	}
	if d.Coalesce == 0 {
		d.Coalesce = DefaultCoalesce
	}
	if rule.QuietHours != "" && c.Kind != "prompt" {
		if from, to, err := parseQuiet(rule.QuietHours); err == nil && inQuiet(now, from, to) {
			d.Hold = true
		}
	}
	return d
}

func (m Match) matches(c Change) bool {
	return in(m.Projects, c.ProjectID) && in(m.Sessions, c.SessionID) &&
		in(m.Tools, c.Tool) && in(m.States, c.State) && in(m.Kinds, c.Kind)
}

func in(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// parseQuiet reads "HH:MM-HH:MM" into minutes past midnight.
func parseQuiet(s string) (from, to int, err error) {
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("quiet hours %q is not HH:MM-HH:MM", s)
	}
	minutes := func(hm string) (int, error) {
		hh, mm, ok := strings.Cut(strings.TrimSpace(hm), ":")
		if !ok {
			return 0, fmt.Errorf("quiet hours %q is not HH:MM-HH:MM", s)
		}
		h, err1 := strconv.Atoi(hh)
		m, err2 := strconv.Atoi(mm)
		if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return 0, fmt.Errorf("quiet hours %q is not HH:MM-HH:MM", s)
		}
		return h*60 + m, nil
	}
	if from, err = minutes(parts[0]); err != nil {
		return
	}
	if to, err = minutes(parts[1]); err != nil {
		return
	}
	return from, to, nil
}

// inQuiet says whether now falls in the window, which may cross midnight.
func inQuiet(now time.Time, from, to int) bool {
	cur := now.Hour()*60 + now.Minute()
	if from == to {
		return false
	}
	if from < to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to
}
