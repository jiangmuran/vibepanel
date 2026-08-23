package session

import "testing"

func TestAllStatesCoversTheEnum(t *testing.T) {
	// AllStates drives the TypeScript generator. A state added to the constants
	// but forgotten here would exist in Go and not in the UI, which shows up as
	// a session rendering with no status indicator at all.
	for _, s := range []State{StateWorking, StateWaiting, StateDone} {
		found := false
		for _, a := range AllStates {
			if a == s {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("state %q is missing from AllStates", s)
		}
	}
	if len(AllStates) != 3 {
		t.Errorf("AllStates has %d entries, want 3", len(AllStates))
	}
}

func TestAllStatesIsOrderedByUrgency(t *testing.T) {
	for i := 1; i < len(AllStates); i++ {
		if AllStates[i-1].SortWeight() > AllStates[i].SortWeight() {
			t.Fatalf("AllStates is not ordered by urgency: %v", AllStates)
		}
	}
	if AllStates[0] != StateWaiting {
		t.Errorf("most urgent state is %q, want %q", AllStates[0], StateWaiting)
	}
}

func TestValid(t *testing.T) {
	for _, s := range AllStates {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	// Hook payloads are user-authored HTTP bodies; anything can arrive.
	for _, s := range []State{"", "banana", "Working", "WAITING"} {
		if State(s).Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}

func TestSourcePrecedence(t *testing.T) {
	// A manual override must survive the next heuristic tick, and a hook report
	// must beat a heuristic guess. Get this backwards and a session the user
	// marked done bounces back to waiting a second later.
	if SourceManual.Precedence() <= SourceHook.Precedence() {
		t.Error("manual must outrank hook")
	}
	if SourceHook.Precedence() <= SourceHeuristic.Precedence() {
		t.Error("hook must outrank heuristic")
	}
	if Source("nonsense").Precedence() >= SourceHeuristic.Precedence() {
		t.Error("an unknown source must rank below every known one")
	}
}

func TestUnknownStateSortsLast(t *testing.T) {
	if State("nonsense").SortWeight() <= StateDone.SortWeight() {
		t.Error("an unknown state must sort after every known one, not crash the comparator")
	}
}
