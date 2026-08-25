package session

import (
	"os"
	"regexp"
	"testing"
)

// The TypeScript side must list exactly these states, in this order.
//
// state.go used to claim the TS constants were generated from it by
// `go generate`, and that hand-editing them was forbidden. Neither the
// generator nor the generated file has ever existed: the constants live in
// web/src/protocol/wire.ts, written by hand, whose own comment admits as much.
// Two files in one repository disagreeing about whether a safety mechanism
// exists is worse than not having it, because the claim is what stops anyone
// looking.
//
// This delivers the property the claim promised — the two cannot drift —
// without a build step. If it fails, the Go enum is right and wire.ts needs
// editing to match.
func TestTypeScriptStatesMatchTheEnum(t *testing.T) {
	const path = "../../web/src/protocol/wire.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s, so the frontend and the enum were not compared: %v", path, err)
	}

	quoted := regexp.MustCompile(`'([a-z]+)'`)
	pick := func(pattern string) []string {
		line := regexp.MustCompile(pattern).Find(src)
		if line == nil {
			t.Fatalf("could not find %q in %s; the shape of the file changed and this "+
				"test is no longer comparing anything", pattern, path)
		}
		var out []string
		for _, m := range quoted.FindAllSubmatch(line, -1) {
			out = append(out, string(m[1]))
		}
		return out
	}

	want := make([]string, 0, len(AllStates))
	for _, s := range AllStates {
		want = append(want, string(s))
	}

	// The union may list them in any order, so compare as sets.
	union := pick(`export type SessionState =[^\n]*`)
	seen := map[string]bool{}
	for _, u := range union {
		seen[u] = true
	}
	if len(union) != len(want) {
		t.Errorf("SessionState lists %v; the enum has %v", union, want)
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("state %q exists in Go and not in the union; sessions in it render "+
				"with no indicator at all", w)
		}
	}

	// STATE_ORDER claims to match SortWeight, so order matters here.
	order := pick(`export const STATE_ORDER[^\n]*`)
	if len(order) != len(want) {
		t.Fatalf("STATE_ORDER is %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("STATE_ORDER is %v, want %v — the sidebar would sort differently "+
				"from the server", order, want)
			break
		}
	}
}

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
