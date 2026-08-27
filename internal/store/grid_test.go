package store

import "testing"

// A board written when the grid was four columns wide is still the board its
// author drew.
//
// The failure this prevents is silent and lands on screens nobody is standing
// in front of: `span: 2` meant half a screen in quarters and is a sixth in
// twelfths, so without the conversion every wall in the wild would rearrange
// itself on upgrade and nothing anywhere would say so.
//
// Delete normaliseGrid, or call it on only one of the two paths, and this
// fails.
func TestABoardStoredInQuartersIsReadAsTwelfths(t *testing.T) {
	// The read path: what a row written by an older build looks like.
	old := `{"preset":"overview","rotate":0,"widgets":[` +
		`{"kind":"states","span":4},{"kind":"gauge","metric":"cpu","span":1},` +
		`{"kind":"projects","span":2}]}`
	got := DecodeBoard(old)
	if got.Grid != GridColumns {
		t.Errorf("grid = %d, want %d", got.Grid, GridColumns)
	}
	want := []int{12, 3, 6}
	for i, w := range want {
		if got.Widgets[i].Span != w {
			t.Errorf("widget %d span = %d, want %d (%s)", i, got.Widgets[i].Span, w,
				"a half stored in quarters must not become a sixth")
		}
	}

	// The write path: a `curl` written against the documented 1-4 spans.
	in := Board{Widgets: []Widget{{Kind: "states", Span: 4}, {Kind: "clock", Span: 1}}}
	clean, err := ValidateBoard(in)
	if err != nil {
		t.Fatalf("ValidateBoard: %v", err)
	}
	if clean.Grid != GridColumns || clean.Widgets[0].Span != 12 || clean.Widgets[1].Span != 3 {
		t.Errorf("a board sent without a grid was not converted: %+v", clean)
	}

	// And a board that already says twelve is left alone. Converting twice is
	// the other half of this bug and it has the same symptom.
	twice := ValidateOrFail(t, Board{Grid: GridColumns,
		Widgets: []Widget{{Kind: "states", Span: 6}}})
	if twice.Widgets[0].Span != 6 {
		t.Errorf("a board already in twelfths was converted again: span %d",
			twice.Widgets[0].Span)
	}
}

// A span outside the grid is still refused after the conversion.
//
// The tempting shape of normaliseGrid clamps as it multiplies, which turns the
// refusal validateWidget owes somebody into a silent repair -- and a board
// silently repaired into a different board is one whose author believes it says
// something it does not.
func TestAnImpossibleSpanIsRefusedRatherThanClampedByTheConversion(t *testing.T) {
	if _, err := ValidateBoard(Board{Widgets: []Widget{{Kind: "states", Span: 99}}}); err == nil {
		t.Error("a span of 99 was accepted; the grid conversion clamped it into range")
	}
	if _, err := ValidateBoard(Board{Grid: GridColumns,
		Widgets: []Widget{{Kind: "states", Span: 13}}}); err == nil {
		t.Error("a span of 13 in a twelve-column grid was accepted")
	}
}

// Height is bounded like every other number on a board.
//
// It reaches `grid-row: span N` in a browser, so an unbounded one is a tile
// that swallows the screen it was meant to share.
func TestAWidgetHeightIsBoundedAndDefaultsToOneRow(t *testing.T) {
	clean := ValidateOrFail(t, Board{Grid: GridColumns,
		Widgets: []Widget{{Kind: "states", Span: 12}}})
	if clean.Widgets[0].Height != 1 {
		t.Errorf("height defaulted to %d, want 1", clean.Widgets[0].Height)
	}
	for _, bad := range []int{-1, MaxRows + 1, 9999} {
		if _, err := ValidateBoard(Board{Grid: GridColumns,
			Widgets: []Widget{{Kind: "states", Span: 12, Height: bad}}}); err == nil {
			t.Errorf("height %d was accepted; a stored number decides how much of a "+
				"screen one tile takes", bad)
		}
	}
	// And the lenient read path drops it rather than failing the whole board.
	raw := `{"grid":12,"widgets":[{"kind":"states","span":12,"height":40},` +
		`{"kind":"clock","span":3}]}`
	got := DecodeBoard(raw)
	if len(got.Widgets) != 1 || got.Widgets[0].Kind != "clock" {
		t.Errorf("a stored widget with an impossible height was not dropped: %+v", got.Widgets)
	}
}

// Every preset is still a board this build accepts, in twelfths.
//
// TestEveryPresetIsAValidBoard next door checks the same thing through the API;
// this one checks the spans specifically, because the catalogue was rewritten
// by multiplying a column and the way to get that wrong is to miss one.
func TestEveryPresetUsesTheWholeGrid(t *testing.T) {
	offered := map[int]bool{}
	for _, n := range GridSteps() {
		offered[n] = true
	}
	for _, p := range Presets() {
		board, ok := PresetBoard(p.ID)
		if !ok {
			t.Fatalf("%s is in the catalogue and does not expand", p.ID)
		}
		if board.Grid != GridColumns {
			t.Errorf("%s expands to a board in %d columns", p.ID, board.Grid)
		}
		for i, w := range board.Widgets {
			if w.Span < 1 || w.Span > GridColumns {
				t.Errorf("%s widget %d has span %d", p.ID, i, w.Span)
			}
			if !offered[w.Span] {
				// The catalogue and the editor have to agree, and this is the
				// direction that goes wrong quietly: a preset using a width the
				// editor's select does not hold is a board that changes the
				// moment somebody opens it and touches anything else.
				t.Errorf("%s widget %d has span %d, which the editor does not offer",
					p.ID, i, w.Span)
			}
		}
	}
}

// ValidateOrFail is ValidateBoard with the error turned into a failure.
func ValidateOrFail(t *testing.T, b Board) Board {
	t.Helper()
	out, err := ValidateBoard(b)
	if err != nil {
		t.Fatalf("ValidateBoard: %v", err)
	}
	return out
}
