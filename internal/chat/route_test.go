package chat

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDefaultRoutesSendWaitingAndDoneToEveryoneAndNotWorking(t *testing.T) {
	r := ParseRoutes("")
	now := time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		state string
		send  bool
	}{{"waiting", true}, {"done", true}, {"working", false}} {
		d := r.Decide(Change{SessionID: "s", State: c.state}, now)
		if d.Send != c.send {
			t.Errorf("%s: send=%v", c.state, d.Send)
		}
		// Coalesce zero: the default rule asks for nothing and the bridge's
		// own window applies.
		if c.send && (len(d.To) != 1 || d.To[0] != "*" || d.Screenshot != ShotAuto || d.Coalesce != 0 || !d.Body) {
			t.Errorf("%s: %+v", c.state, d)
		}
	}
}

func TestFirstMatchingRuleDecidesAndAnEmptyToSilences(t *testing.T) {
	raw, _ := json.Marshal(Routes{
		Rules: []Rule{
			{ID: "1", Name: "quiet project", Enabled: true, Match: Match{Projects: []string{"p1"}}, To: nil},
			{ID: "2", Name: "codex to phone", Enabled: true, Match: Match{Tools: []string{"codex"}}, To: []string{"telegram:7"}, Screenshot: ShotAlways, CoalesceSeconds: 10},
			{ID: "3", Name: "disabled", Enabled: false, Match: Match{}, To: []string{"weixin:x"}},
		},
		Default: Rule{To: []string{"*"}, Match: Match{States: []string{"waiting"}}},
	})
	r := ParseRoutes(string(raw))
	now := time.Now()
	d := r.Decide(Change{ProjectID: "p1", Tool: "codex", State: "waiting"}, now)
	if d.Send || d.Rule != "quiet project" {
		t.Fatalf("silenced project: %+v", d)
	}
	d = r.Decide(Change{ProjectID: "p2", Tool: "codex", State: "working"}, now)
	if !d.Send || d.Rule != "codex to phone" || d.To[0] != "telegram:7" || d.Screenshot != ShotAlways || d.Coalesce != 10*time.Second {
		t.Fatalf("codex rule: %+v", d)
	}
	d = r.Decide(Change{ProjectID: "p2", Tool: "claude", State: "waiting"}, now)
	if !d.Send || d.Rule != "default" {
		t.Fatalf("default: %+v", d)
	}
	d = r.Decide(Change{ProjectID: "p2", Tool: "claude", State: "done"}, now)
	if d.Send {
		t.Fatalf("default should not match done: %+v", d)
	}
}

func TestQuietHoursHoldEverythingButRequests(t *testing.T) {
	raw, _ := json.Marshal(Routes{Default: Rule{To: []string{"*"}, QuietHours: "23:00-08:00"}})
	r := ParseRoutes(string(raw))
	night := time.Date(2026, 9, 15, 2, 30, 0, 0, time.UTC)
	day := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if d := r.Decide(Change{State: "done", Kind: "assistant"}, night); !d.Hold {
		t.Fatalf("a done at 2:30 was not held: %+v", d)
	}
	if d := r.Decide(Change{State: "waiting", Kind: "prompt"}, night); d.Hold {
		t.Fatalf("a prompt at 2:30 was held: %+v", d)
	}
	// A question blocks the session as surely as a prompt does.
	if d := r.Decide(Change{State: "waiting", Kind: "question"}, night); d.Hold {
		t.Fatalf("a question at 2:30 was held: %+v", d)
	}
	for _, typed := range []string{"23：00～08：00", "23:00 ~ 8:00", "23:00到08:00"} {
		from, to, err := parseQuiet(typed)
		if err != nil || from != 23*60 || to != 8*60 {
			t.Errorf("parseQuiet(%q) = %d %d %v", typed, from, to, err)
		}
	}
	if d := r.Decide(Change{State: "done"}, day); d.Hold {
		t.Fatalf("held at noon: %+v", d)
	}
	// A window that does not cross midnight.
	raw, _ = json.Marshal(Routes{Default: Rule{To: []string{"*"}, QuietHours: "12:00-13:00"}})
	r = ParseRoutes(string(raw))
	if d := r.Decide(Change{State: "done"}, day); !d.Hold {
		t.Fatalf("not held inside a same-day window: %+v", d)
	}
	if d := r.Decide(Change{State: "done"}, day.Add(time.Hour)); d.Hold {
		t.Fatalf("held at the window's end: %+v", d)
	}
}

func TestRoutesValidateRefusesWhatCannotBeApplied(t *testing.T) {
	bad := []Routes{
		{Default: Rule{Screenshot: "sometimes"}},
		{Default: Rule{CoalesceSeconds: -1}},
		{Default: Rule{QuietHours: "25:00-08:00"}},
		{Default: Rule{QuietHours: "night"}},
		{Rules: []Rule{{Match: Match{States: []string{"asleep"}}}}},
		{Rules: []Rule{{Match: Match{Kinds: []string{"shout"}}}}},
		{Rules: []Rule{{To: []string{"telegram"}}}},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("case %d accepted: %+v", i, r)
		}
	}
	good := Routes{Rules: []Rule{{Enabled: true, To: []string{"*", "weixin:abc"}, Screenshot: ShotNever, QuietHours: "22:30-07:15"}}, Default: DefaultRoutes().Default}
	if err := good.Validate(); err != nil {
		t.Fatalf("good table refused: %v", err)
	}
}

func TestAnUnparseableRowFallsBackToTheDefaultTable(t *testing.T) {
	r := ParseRoutes("{not json")
	if d := r.Decide(Change{State: "waiting"}, time.Now()); !d.Send || d.To[0] != "*" {
		t.Fatalf("fallback: %+v", d)
	}
}
