package store

import "testing"

// A board is data. These are the properties that keep it from being anything
// else, asserted here rather than only through HTTP, because this is where the
// decisions live and an HTTP test would pass on a validator that had been
// weakened as long as some other layer still refused.

func TestValidateBoardFillsInWhatItMayAndRefusesWhatItMayNot(t *testing.T) {
	t.Run("a missing span becomes the kind's own", func(t *testing.T) {
		out, err := ValidateBoard(Board{Widgets: []Widget{{Kind: "states"}}})
		if err != nil {
			t.Fatal(err)
		}
		spec, _ := WidgetOptions("states")
		if out.Widgets[0].Span != spec.Span {
			t.Errorf("span = %d, want the kind's default %d", out.Widgets[0].Span, spec.Span)
		}
	})

	t.Run("a caption is cut by runes, not by bytes", func(t *testing.T) {
		// A byte slice through a multi-byte character renders the last one as
		// U+FFFD, which is a visible defect on somebody's wall.
		long := ""
		for range MaxCaption + 10 {
			long += "看"
		}
		out, err := ValidateBoard(Board{Widgets: []Widget{{Kind: "caption", Text: long}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := len([]rune(out.Widgets[0].Text)); got != MaxCaption {
			t.Errorf("kept %d runes, want %d", got, MaxCaption)
		}
	})

	t.Run("every bound is a refusal", func(t *testing.T) {
		for name, board := range map[string]Board{
			"no widgets":     {},
			"unknown kind":   {Widgets: []Widget{{Kind: "shell"}}},
			"unknown metric": {Widgets: []Widget{{Kind: "bignumber", Metric: "secret"}}},
			"no metric":      {Widgets: []Widget{{Kind: "bignumber"}}},
			"unknown by":     {Widgets: []Widget{{Kind: "spendsplit", By: "cwd"}}},
			"unknown filter": {Widgets: []Widget{{Kind: "sessionlist", Filter: "everything"}}},
			"span too wide":  {Widgets: []Widget{{Kind: "states", Span: MaxSpan + 1}}},
			"span negative":  {Widgets: []Widget{{Kind: "states", Span: -1}}},
			"days too many":  {Widgets: []Widget{{Kind: "spendbars", Days: MaxSpendDays + 1}}},
			"days on a kind that takes none": {
				Widgets: []Widget{{Kind: "states", Days: 7}}},
			"text on a kind that takes none": {
				Widgets: []Widget{{Kind: "states", Text: "hello"}}},
			"page too far":  {Widgets: []Widget{{Kind: "states", Page: MaxPages}}},
			"board rotate":  {Rotate: MaxRotateSeconds + 1, Widgets: []Widget{{Kind: "states"}}},
			"widget rotate": {Widgets: []Widget{{Kind: "gauge", Metric: "cpu", Rotate: 10}}},
			"unknown preset": {Preset: "invented",
				Widgets: []Widget{{Kind: "states"}}},
		} {
			if _, err := ValidateBoard(board); err == nil {
				t.Errorf("%s was accepted", name)
			}
		}
	})

	t.Run("a board longer than the cap is refused", func(t *testing.T) {
		widgets := make([]Widget, MaxWidgets+1)
		for i := range widgets {
			widgets[i] = Widget{Kind: "states"}
		}
		if _, err := ValidateBoard(Board{Widgets: widgets}); err == nil {
			t.Error("a board past the cap was accepted")
		}
	})
}

// The read path is lenient where the write path is strict, and the asymmetry
// has a shape: unknown widgets are dropped, never repaired.
func TestSanitiseBoardDropsRatherThanRepairs(t *testing.T) {
	out := SanitiseBoard(Board{
		Preset: "invented",
		Widgets: []Widget{
			{Kind: "states", Span: 2},
			{Kind: "from-the-future", Span: 4},
			{Kind: "bignumber", Metric: "whatever"},
		},
	})
	if len(out.Widgets) != 1 || out.Widgets[0].Kind != "states" {
		t.Errorf("kept %+v; the two unrenderable widgets should be gone and the good one "+
			"kept", out.Widgets)
	}
	if out.Preset != "" {
		t.Errorf("preset = %q; an unknown preset name is not a preset", out.Preset)
	}

	// Nothing left means the default board rather than a blank screen: there is
	// nobody standing at a wall display to notice and reload.
	empty := SanitiseBoard(Board{Widgets: []Widget{{Kind: "from-the-future"}}})
	if len(empty.Widgets) == 0 {
		t.Error("a board of nothing but unknown widgets rendered nothing at all")
	}

	// And the column being unreadable is the same case.
	if len(DecodeBoard("not json").Widgets) == 0 {
		t.Error("an unparseable board rendered nothing at all")
	}
	if len(DecodeBoard("").Widgets) == 0 {
		t.Error("a link written before boards existed rendered nothing at all")
	}
}

// What a board asks for follows its settings, not just its kinds.
//
// The failure without this: a link made to show one count carries every figure
// the transcripts produced, and a board split by model carries the by-project
// and by-agent tables it is not drawing.
func TestNeedsFollowsTheSettingsAndNotOnlyTheKind(t *testing.T) {
	countOnly := Board{Widgets: []Widget{{Kind: "bignumber", Metric: "waiting"}}}
	if countOnly.Needs()[NeedSpend] {
		t.Error("a board counting waiting sessions asks for the spend section")
	}

	spendNumber := Board{Widgets: []Widget{{Kind: "bignumber", Metric: "tokensToday"}}}
	if !spendNumber.Needs()[NeedSpend] {
		t.Error("a board showing today's tokens does not ask for the spend section")
	}

	byModel := Board{Widgets: []Widget{{Kind: "spendsplit", By: "model"}}}
	needs := byModel.Needs()
	if !needs[NeedSpendModels] {
		t.Error("a board split by model does not ask for the model breakdown")
	}
	if needs[NeedSpendProjects] || needs[NeedSpendTools] {
		t.Error("a board split by model also asks for the project and agent breakdowns")
	}

	// A dimension left unset takes the first in the list, and the need has to
	// follow that rather than nothing -- otherwise the widget renders empty.
	unset := Board{Widgets: []Widget{{Kind: "spendsplit"}}}
	if !unset.Needs()[NeedSpendTools] {
		t.Error("a breakdown with no dimension asks for no section, so it draws nothing")
	}
}

func TestEveryWidgetKindIsInTheCatalogue(t *testing.T) {
	kinds := KnownWidgetKinds()
	if len(kinds) < 15 {
		t.Fatalf("%d widget kinds; the registry reader has stopped reading", len(kinds))
	}
	for _, kind := range kinds {
		spec, ok := WidgetOptions(kind)
		if !ok {
			t.Errorf("%s is listed and has no spec", kind)
			continue
		}
		if spec.Span < 1 || spec.Span > MaxSpan {
			t.Errorf("%s has a default span of %d", kind, spec.Span)
		}
	}
	if _, ok := WidgetOptions("no-such-widget"); ok {
		t.Error("WidgetOptions answered for a kind that does not exist")
	}
}

// Density is refused on the way in and clamped on the way out.
//
// The asymmetry is the same one the rest of this file has: on the way in there
// is a person at a keyboard to tell, and on the way out there is a wall display
// with nobody standing at it, so a board that has become strange must still
// leave a working screen.
func TestDensityIsRefusedOnTheWayInAndClampedOnTheWayOut(t *testing.T) {
	one := []Widget{{Kind: "states", Span: 12}}

	if _, err := ValidateBoard(Board{Grid: GridColumns, Density: 9, Widgets: one}); err == nil {
		t.Error("a density of 9 was accepted; the steps are a closed set and a board that " +
			"could name a tenth is a stored number choosing a code path")
	}
	if _, err := ValidateBoard(Board{Grid: GridColumns, Density: -1, Widgets: one}); err == nil {
		t.Error("a negative density was accepted")
	}
	// Zero means "this board was written before densities existed", which every
	// stored board is.
	got, err := ValidateBoard(Board{Grid: GridColumns, Widgets: one})
	if err != nil {
		t.Fatalf("a board with no density was refused: %v", err)
	}
	if got.Density != DefaultDensity {
		t.Errorf("a board with no density came back at %d, want %d", got.Density, DefaultDensity)
	}

	// And on the read path, where there is nobody to tell.
	for _, in := range []int{0, -3, 99} {
		out := SanitiseBoard(Board{Grid: GridColumns, Density: in, Widgets: one})
		if out.Density < MinDensity || out.Density > MaxDensity {
			t.Errorf("a stored density of %d was served as %d", in, out.Density)
		}
	}
	kept := SanitiseBoard(Board{Grid: GridColumns, Density: MaxDensity, Widgets: one})
	if kept.Density != MaxDensity {
		t.Errorf("a legal density of %d was served as %d", MaxDensity, kept.Density)
	}
}
