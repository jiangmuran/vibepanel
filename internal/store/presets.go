package store

// The purposes a share link can be made for.
//
// A preset is a starting arrangement, not a mode: creating a link from one
// stores the widgets it expands to, and from that moment the board is the
// board. Nothing later re-reads the preset to decide what to draw, so changing
// this catalogue in a future build cannot change a wall somebody already pasted
// into a television.
//
// They live on the server rather than in the settings page because the
// catalogue is part of the API — a link can be made with `curl` and a preset
// name — and because the editor should offer what the validator accepts. The
// frontend supplies a name and a sentence for each id in both languages, and a
// test fails if one is missing.
//
// The axis is who is reading, not which table the numbers came from. The same
// figures answer different questions depending on who is standing there, and a
// catalogue organised by data source produces eighteen arrangements of the same
// grid. So:
//
//	for whoever is working
//	  overview     the panel's own summary — what the dashboard was before boards
//	  attention    does anything need me, readable from across a room
//	  queue        triage: who has waited longest, and for how long
//	  wall         every session laid out at once, as large as the screen allows
//	  answer       only the ones that need an answer, and nothing else
//	  dense        everything, for somebody who is actually looking
//
//	for a screen on a wall
//	  glance       four numbers and a clock, read in one second while walking past
//	  single       one number, filling the screen
//	  pulse        how hard is it working right now: rate, today, states
//	  rotating     three pages that cycle: attention, then work, then cost
//
//	for whoever runs the machine
//	  machine      only the machine
//	  health       what has gone wrong: exits, pressure, the sessions in trouble
//
//	for a manager
//	  boss         what it cost and what came out of it, side by side
//	  cost         where it went: by agent, by project, by model
//	  leadership   calm and high level: progress, the month, the year
//	  year         the year as a grid of days
//
//	for a closer look at one thing
//	  projects     per project rather than per session
//	  models       which model is doing the work, and how fast
//	  spendToday   what today has cost, and how that compares
//
// Nineteen, and the shape of the list matters more than the count: there is one
// for three metres and one for thirty centimetres, one that is only the
// machine, one that is only spend, one that is a single number, and one that
// moves. A generator that produced nineteen variations on the same grid would
// have missed the point.

// Preset is one starting arrangement.
type Preset struct {
	ID string `json:"id"`
	// Audience groups the catalogue in the editor. It is a label, nothing
	// reads it to decide behaviour.
	Audience string `json:"audience"`
	// Widgets is what choosing this preset expands to. Copied on the way out,
	// because a caller that appended to it would edit the catalogue.
	Widgets []Widget `json:"widgets"`
	// Rotate is the page interval a rotating preset starts with.
	Rotate int `json:"rotate"`
}

// The audiences, which are also the order the editor lists them in.
const (
	audienceWorking = "working"
	audienceWall    = "wall"
	audienceOps     = "ops"
	audienceManager = "manager"
	audienceDetail  = "detail"
)

// DefaultPreset is what a link made without a board gets.
//
// The arrangement the dashboard had before boards existed, so a link created by
// an older build, or by a `curl` that says nothing about a board, opens the
// screen its author remembers rather than a new one.
const DefaultPreset = "overview"

var presets = []Preset{
	{ID: "overview", Audience: audienceWorking, Widgets: []Widget{
		{Kind: "states", Span: 4},
		{Kind: "gauge", Metric: "cpu", Span: 1},
		{Kind: "gauge", Metric: "memory", Span: 1},
		{Kind: "gauge", Metric: "disk", Span: 1},
		{Kind: "uptime", Span: 1},
		{Kind: "sessionlist", Filter: "all", Order: "state", Group: "project", Span: 4},
	}},
	{ID: "attention", Audience: audienceWorking, Widgets: []Widget{
		{Kind: "attention", Span: 4},
		{Kind: "sessionlist", Filter: "waiting", Order: "waited", Group: "none", Span: 4},
	}},
	{ID: "queue", Audience: audienceWorking, Widgets: []Widget{
		{Kind: "bignumber", Metric: "longestWait", Span: 2},
		{Kind: "bignumber", Metric: "waiting", Span: 2},
		{Kind: "sessionlist", Filter: "active", Order: "waited", Group: "none", Span: 4},
	}},
	// Every session at once. The grid sizes its own tiles to the viewport, so
	// this is one board that is a summary on a laptop and a wall of forty on a
	// television — rather than two boards somebody has to choose between.
	{ID: "wall", Audience: audienceWorking, Widgets: []Widget{
		{Kind: "states", Span: 4},
		{Kind: "sessiongrid", Filter: "all", Order: "state", Span: 4},
	}},
	{ID: "answer", Audience: audienceWorking, Widgets: []Widget{
		{Kind: "sessiongrid", Filter: "waiting", Order: "waited", Span: 4},
	}},
	{ID: "dense", Audience: audienceWorking, Widgets: []Widget{
		{Kind: "states", Span: 2},
		{Kind: "output", Span: 2},
		{Kind: "machine", Span: 2},
		{Kind: "spendtotals", Span: 2},
		{Kind: "sessionlist", Filter: "all", Order: "state", Group: "project", Span: 4},
		{Kind: "spendbars", By: "day", Days: 30, Span: 2},
		{Kind: "projects", Span: 2},
	}},

	{ID: "glance", Audience: audienceWall, Widgets: []Widget{
		{Kind: "clock", Span: 1},
		{Kind: "bignumber", Metric: "waiting", Span: 1},
		{Kind: "bignumber", Metric: "working", Span: 1},
		{Kind: "bignumber", Metric: "tokensToday", Span: 1},
	}},
	{ID: "single", Audience: audienceWall, Widgets: []Widget{
		{Kind: "bignumber", Metric: "waiting", Span: 4},
	}},
	{ID: "pulse", Audience: audienceWall, Widgets: []Widget{
		{Kind: "spendrate", Span: 2},
		{Kind: "bignumber", Metric: "working", Span: 2},
		{Kind: "states", Span: 4},
		{Kind: "spendbars", By: "day", Days: 14, Span: 4},
	}},
	// Three pages, twenty seconds each: what needs answering, what is running,
	// what it is costing. A wall that shows one thing forever wastes the wall.
	{ID: "rotating", Audience: audienceWall, Rotate: 20, Widgets: []Widget{
		{Kind: "attention", Span: 4, Page: 0},
		{Kind: "sessiongrid", Filter: "waiting", Order: "waited", Span: 4, Page: 0},
		{Kind: "states", Span: 4, Page: 1},
		{Kind: "sessiongrid", Filter: "active", Order: "cpu", Span: 4, Rotate: 10, Page: 1},
		{Kind: "spendrate", Span: 2, Page: 2},
		{Kind: "spendcompare", Span: 2, Page: 2},
		{Kind: "spendbars", By: "day", Days: 14, Span: 4, Page: 2},
	}},

	{ID: "machine", Audience: audienceOps, Widgets: []Widget{
		{Kind: "gauge", Metric: "cpu", Span: 2},
		{Kind: "gauge", Metric: "memory", Span: 2},
		{Kind: "gauge", Metric: "disk", Span: 2},
		{Kind: "gauge", Metric: "swap", Span: 2},
		{Kind: "uptime", Span: 2},
		{Kind: "cputop", Span: 2},
	}},
	{ID: "health", Audience: audienceOps, Widgets: []Widget{
		{Kind: "exits", Span: 2},
		{Kind: "cputop", Span: 2},
		{Kind: "gauge", Metric: "cpu", Span: 1},
		{Kind: "gauge", Metric: "memory", Span: 1},
		{Kind: "gauge", Metric: "disk", Span: 1},
		{Kind: "uptime", Span: 1},
		{Kind: "sessionlist", Filter: "trouble", Order: "state", Group: "project", Span: 4},
	}},

	// What it cost next to what came out of it. A board of costs alone reads as
	// an expense report; a board with both reads as work.
	{ID: "boss", Audience: audienceManager, Widgets: []Widget{
		{Kind: "output", Span: 2},
		{Kind: "spendcompare", Span: 2},
		{Kind: "todos", Span: 2},
		{Kind: "spendtotals", Span: 2},
		{Kind: "spendsplit", By: "project", Span: 4},
	}},
	{ID: "cost", Audience: audienceManager, Widgets: []Widget{
		{Kind: "spendtotals", Span: 2},
		{Kind: "spendrate", Span: 2},
		{Kind: "spendsplit", By: "tool", Span: 2},
		{Kind: "spendsplit", By: "model", Span: 2},
		{Kind: "spendsplit", By: "project", Span: 2},
		{Kind: "spendbars", By: "month", Span: 2},
	}},
	{ID: "leadership", Audience: audienceManager, Widgets: []Widget{
		{Kind: "gauge", Metric: "todoPercent", Span: 1},
		{Kind: "bignumber", Metric: "doneToday", Span: 1},
		{Kind: "bignumber", Metric: "tokensMonth", Span: 2},
		{Kind: "projects", Span: 2},
		{Kind: "todos", Span: 2},
		{Kind: "spendheatmap", Span: 4},
	}},
	{ID: "year", Audience: audienceManager, Widgets: []Widget{
		{Kind: "spendheatmap", Span: 4},
		{Kind: "spendbars", By: "month", Span: 4},
	}},

	{ID: "projects", Audience: audienceDetail, Widgets: []Widget{
		{Kind: "projects", Span: 2},
		{Kind: "todos", Span: 2},
		{Kind: "spendsplit", By: "project", Span: 4},
		{Kind: "sessionlist", Filter: "all", Order: "state", Group: "project", Span: 4},
	}},
	{ID: "models", Audience: audienceDetail, Widgets: []Widget{
		{Kind: "spendsplit", By: "model", Span: 2},
		{Kind: "spendrate", Span: 2},
		{Kind: "spendtotals", Span: 2},
		{Kind: "spendsplit", By: "tool", Span: 2},
	}},
	{ID: "spendToday", Audience: audienceDetail, Widgets: []Widget{
		{Kind: "bignumber", Metric: "tokensToday", Span: 2},
		{Kind: "bignumber", Metric: "requestsToday", Span: 2},
		{Kind: "spendcompare", Span: 2},
		{Kind: "spendsplit", By: "tool", Span: 2},
		{Kind: "spendbars", By: "day", Days: 14, Span: 4},
	}},
}

// Presets returns the catalogue, in the order it is offered.
func Presets() []Preset {
	out := make([]Preset, 0, len(presets))
	for _, p := range presets {
		out = append(out, Preset{
			ID: p.ID, Audience: p.Audience, Rotate: p.Rotate, Widgets: copyWidgets(p.Widgets)})
	}
	return out
}

// KnownPreset reports whether id names one.
func KnownPreset(id string) bool {
	for _, p := range presets {
		if p.ID == id {
			return true
		}
	}
	return false
}

// PresetBoard expands a preset, or reports that it is not one.
func PresetBoard(id string) (Board, bool) {
	for _, p := range presets {
		if p.ID == id {
			return Board{Preset: p.ID, Rotate: p.Rotate, Widgets: copyWidgets(p.Widgets)}, true
		}
	}
	return Board{}, false
}

// DefaultBoard is the arrangement a link gets when nothing else says.
func DefaultBoard() Board {
	b, ok := PresetBoard(DefaultPreset)
	if !ok {
		// Unreachable while DefaultPreset names a row above, and a panic here
		// would take down a wall display. One widget that needs nothing is the
		// floor: a screen showing the state tallies is still a dashboard.
		return Board{Widgets: []Widget{{Kind: "states", Span: MaxSpan}}}
	}
	return b
}

// Audiences lists the groups, in the order the editor shows them.
func Audiences() []string {
	return []string{audienceWorking, audienceWall, audienceOps, audienceManager, audienceDetail}
}

func copyWidgets(in []Widget) []Widget {
	out := make([]Widget, len(in))
	copy(out, in)
	return out
}
