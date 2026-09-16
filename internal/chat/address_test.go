package chat

import "testing"

func TestSplitHandleReadsTheThreeSpellingsAndNotABareNumber(t *testing.T) {
	cases := []struct {
		in   string
		h    int
		rest string
		ok   bool
	}{
		{"3: continue", 3, "continue", true},
		{"3：继续", 3, "继续", true},
		{"#3 continue", 3, "continue", true},
		{"[3] continue", 3, "continue", true},
		{"【3】拒绝", 3, "拒绝", true},
		{"  [ 12 ]  go", 12, "go", true},
		{"3", 0, "3", false}, // an answer to a numbered menu
		{"3 continue", 0, "3 continue", false},
		{"continue 3:", 0, "continue 3:", false},
		{"0: x", 0, "0: x", false},
		{"", 0, "", false},
	}
	for _, c := range cases {
		h, rest, ok := SplitHandle(c.in)
		if h != c.h || rest != c.rest || ok != c.ok {
			t.Errorf("SplitHandle(%q) = %d %q %v, want %d %q %v", c.in, h, rest, ok, c.h, c.rest, c.ok)
		}
	}
}

func TestHandleInQuoteFindsTheCardsHandle(t *testing.T) {
	if h, ok := HandleInQuote("▲ [7] vibepanel · fix tmux\n在等你"); !ok || h != 7 {
		t.Fatalf("got %d %v", h, ok)
	}
	if _, ok := HandleInQuote("no handle here 7"); ok {
		t.Fatal("a bare number in a quote resolved")
	}
}

func TestResolveOrder(t *testing.T) {
	cands := []Candidate{
		{SessionID: "a", Handle: 1, Waiting: true},
		{SessionID: "b", Handle: 2, Waiting: false},
		{SessionID: "c", Handle: 3, Waiting: true},
	}
	// A quote wins over everything.
	tg, rest, reason := Resolve("3: hi", "b", "a", cands, false)
	if reason != "" || tg.SessionID != "b" || tg.How != "quote" || rest != "3: hi" {
		t.Fatalf("quote: %+v %q %q", tg, rest, reason)
	}
	// A handle wins over the focus and is stripped.
	tg, rest, reason = Resolve("2: hi", "", "a", cands, false)
	if reason != "" || tg.SessionID != "b" || tg.How != "handle" || rest != "hi" {
		t.Fatalf("handle: %+v %q %q", tg, rest, reason)
	}
	// An unknown handle is refused, not guessed.
	if _, _, reason = Resolve("9: hi", "", "a", cands, false); reason != ReasonUnknown {
		t.Fatalf("unknown handle: %q", reason)
	}
	// Two waiting sessions and no address: refused even with a focus.
	if _, _, reason = Resolve("y", "", "a", cands, true); reason != ReasonSeveral {
		t.Fatalf("several: %q", reason)
	}
	// Exactly one waiting: it gets the words, over the focus.
	one := []Candidate{{SessionID: "a", Handle: 1}, {SessionID: "c", Handle: 3, Waiting: true}}
	tg, _, reason = Resolve("y", "", "a", one, true)
	if reason != "" || tg.SessionID != "c" || tg.How != "only-waiting" {
		t.Fatalf("only waiting: %+v %q", tg, reason)
	}
	// Words follow the focus even while something is waiting; without a
	// focus they go to the one waiting session.
	tg, _, reason = Resolve("add a test", "", "a", one, false)
	if reason != "" || tg.SessionID != "a" || tg.How != "focus" {
		t.Fatalf("words with a focus: %+v %q", tg, reason)
	}
	tg, _, reason = Resolve("add a test", "", "", one, false)
	if reason != "" || tg.SessionID != "c" || tg.How != "only-waiting" {
		t.Fatalf("words without a focus: %+v %q", tg, reason)
	}
	if _, _, reason = Resolve("add a test", "", "", cands, false); reason != ReasonSeveral {
		t.Fatalf("words, two waiting, no focus: %q", reason)
	}
	// Nothing waiting: the focus, when it still exists.
	none := []Candidate{{SessionID: "a", Handle: 1}, {SessionID: "c", Handle: 3}}
	tg, _, reason = Resolve("hi", "", "a", none, false)
	if reason != "" || tg.SessionID != "a" || tg.How != "focus" {
		t.Fatalf("focus: %+v %q", tg, reason)
	}
	// A focus on a session that is gone is no focus.
	if _, _, reason = Resolve("hi", "", "zz", none, false); reason != ReasonNone {
		t.Fatalf("stale focus: %q", reason)
	}
}
