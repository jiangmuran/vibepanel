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
// There are two axes, and they answer the two questions somebody making a link
// can actually answer. *Who is reading* — the same figures mean different
// things depending on who is standing there. And *what am I putting this on* —
// which is the question people can always answer and the one the old catalogue
// made them translate into a guess about widget counts.
//
//	for whoever is working
//	  overview     the panel's own summary — what the dashboard was before boards
//	  attention    does anything need me, readable from across a room
//	  queue        triage: who has waited longest, and for how long
//	  wall         every session laid out at once, as large as the screen allows
//	  answer       only the ones that need an answer, and nothing else
//	  dense        everything, for somebody who is actually looking
//	  phone        one column, for the screen in a pocket
//
//	for a screen on a wall
//	  glance       four numbers and a clock, read in one second while walking past
//	  single       one number, filling the screen
//	  pulse        how hard is it working right now: rate, today, states
//	  rotating     three pages that cycle: attention, then work, then cost
//	  burn         what it is spending, as the whole screen
//	  atrium       a corridor screen: the room's name, the clock, the sessions
//
//	for whoever runs the machine
//	  machine      only the machine
//	  health       what has gone wrong: exits, pressure, the sessions in trouble
//
//	for a manager
//	  exec         the one for a 4K wall: hero, movement, texture, filled
//	  client       one customer's own project, with no other customer's name on it
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
// The shape of the list matters more than the count: there is one for three
// metres and one for thirty centimetres, one that is only the machine, one that
// is only spend, one that is a single number, and one that moves. A generator
// producing twenty-four variations on the same grid would have missed the point.

// Preset is one starting arrangement.
type Preset struct {
	ID string `json:"id"`
	// Audience groups the catalogue in the editor. It is a label, nothing
	// reads it to decide behaviour.
	Audience string `json:"audience"`
	// Screen is what this arrangement was composed for.
	//
	// A label as well, and the more useful of the two: "which of twenty-four do
	// I want" is a question nobody can answer and "what am I putting this on"
	// is one everybody can. It says what the board was *composed* for, not what
	// it is limited to — every board still collapses with the viewport, so a
	// wall preset opened on a phone is a phone screen rather than an error.
	Screen string `json:"screen"`
	// Widgets is what choosing this preset expands to. Copied on the way out,
	// because a caller that appended to it would edit the catalogue.
	Widgets []Widget `json:"widgets"`
	// Rotate is the page interval a rotating preset starts with.
	Rotate int `json:"rotate"`
	// Fill says this arrangement was drawn to occupy a whole screen rather
	// than to flow down one.
	Fill bool `json:"fill"`
	// Density is how much each widget on it says: MinDensity is one figure at a
	// time, MaxDensity is everything it knows. Zero means DefaultDensity.
	//
	// On the preset rather than derived from Screen, because they are two
	// different questions and the whole point of the field is that they are not
	// the same axis: an atrium board and a board somebody sits in front of can
	// both be for a wall. See store.Board.Density.
	Density int `json:"density"`
	// Detail is the disclosure mode this preset is only correct at, or "" when
	// that is the owner's call.
	//
	// A hint the editor applies and the server does not read: detail is
	// validated on its own, from the request, exactly as before. It exists
	// because one preset in this list is wrong at any other setting — see
	// "client" — and leaving that to somebody to remember is how a customer
	// ends up reading another customer's project name off a screen.
	Detail string `json:"detail"`
	// NeedsScope says this arrangement is meaningless pointed at the whole
	// panel. Same standing as Detail: a hint, checked again by the handler.
	NeedsScope bool `json:"needsScope"`
}

// The audiences, which are also the order the editor lists them in.
const (
	audienceWorking = "working"
	audienceWall    = "wall"
	audienceOps     = "ops"
	audienceManager = "manager"
	audienceDetail  = "detail"
)

// The screens a preset can be composed for.
//
// Four rather than a pixel width, because this is a label somebody chooses from
// and not a breakpoint anything compares against. The renderer bands the
// viewport itself; these say what the author had in mind.
const (
	screenPhone  = "phone"
	screenLaptop = "laptop"
	screenWall   = "wall"
	screenBig    = "bigwall"
)

// Screens lists them in the order the editor offers them: smallest first,
// because that is the order somebody scanning for their own case reads in.
func Screens() []string {
	return []string{screenPhone, screenLaptop, screenWall, screenBig}
}

// DefaultPreset is what a link made without a board gets.
//
// The arrangement the dashboard had before boards existed, so a link created by
// an older build, or by a `curl` that says nothing about a board, opens the
// screen its author remembers rather than a new one.
const DefaultPreset = "overview"

var presets = []Preset{
	{ID: "overview", Audience: audienceWorking, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "states", Span: 12},
		{Kind: "gauge", Metric: "cpu", Span: 3},
		{Kind: "gauge", Metric: "memory", Span: 3},
		{Kind: "gauge", Metric: "disk", Span: 3},
		{Kind: "uptime", Span: 3},
		{Kind: "sessionlist", Filter: "all", Order: "state", Group: "project", Span: 12},
	}},
	{ID: "attention", Audience: audienceWorking, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "attention", Span: 12},
		{Kind: "sessionlist", Filter: "waiting", Order: "waited", Group: "none", Span: 12},
	}},
	{ID: "queue", Audience: audienceWorking, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "bignumber", Metric: "longestWait", Span: 6},
		{Kind: "bignumber", Metric: "waiting", Span: 6},
		{Kind: "timeline", Filter: "active", Order: "waited", Span: 12, Height: 2},
	}},
	// Every session at once. The grid sizes its own tiles to the viewport, so
	// this is one board that is a summary on a laptop and a wall of forty on a
	// television — rather than two boards somebody has to choose between.
	{ID: "wall", Audience: audienceWorking, Screen: screenWall, Fill: true, Widgets: []Widget{
		{Kind: "statebar", Span: 12},
		{Kind: "sessiongrid", Filter: "all", Order: "state", Span: 12, Height: 4},
	}},
	{ID: "answer", Audience: audienceWorking, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "sessiongrid", Filter: "waiting", Order: "waited", Span: 12, Height: 2},
	}},
	// How today has gone, out of the session-event log: what started, what went
	// quiet waiting, what finished, and how long things sat before somebody got
	// to them. None of this could be drawn before the log existed.
	{ID: "today", Audience: audienceWorking, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "flow", By: "hour", Span: 8, Height: 2},
		{Kind: "feed", Span: 4, Height: 2},
		{Kind: "waits", By: "hour", Span: 6},
		{Kind: "output", Span: 6},
	}},
	{ID: "dense", Audience: audienceWorking, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "states", Span: 6},
		{Kind: "output", Span: 6},
		{Kind: "machine", Span: 6},
		{Kind: "spendtotals", Span: 6},
		{Kind: "sessionlist", Filter: "all", Order: "state", Group: "project", Span: 12},
		{Kind: "spendbars", By: "day", Days: 30, Span: 6},
		{Kind: "projects", Span: 6},
	}},
	// One column, three things, nothing that needs a chart to be legible.
	// A phone is held at thirty centimetres and read for four seconds.
	{ID: "phone", Audience: audienceWorking, Screen: screenPhone, Widgets: []Widget{
		{Kind: "attention", Span: 12},
		{Kind: "statebar", Span: 12},
		{Kind: "sessionlist", Filter: "active", Order: "waited", Group: "none", Span: 12},
	}},

	{ID: "glance", Audience: audienceWall, Screen: screenWall, Fill: true, Widgets: []Widget{
		{Kind: "datetime", Span: 3, Height: 2},
		{Kind: "bignumber", Metric: "waiting", Span: 3, Height: 2},
		{Kind: "bignumber", Metric: "working", Span: 3, Height: 2},
		{Kind: "bignumber", Metric: "tokensToday", Span: 3, Height: 2},
		{Kind: "nowstrip", Span: 12},
	}},
	{ID: "single", Audience: audienceWall, Screen: screenWall, Fill: true, Widgets: []Widget{
		{Kind: "bignumber", Metric: "waiting", Span: 12, Height: 4},
	}},
	{ID: "pulse", Audience: audienceWall, Screen: screenWall, Fill: true, Widgets: []Widget{
		{Kind: "spendrate", Span: 6, Height: 2},
		{Kind: "bignumber", Metric: "working", Span: 6, Height: 2},
		{Kind: "machinearea", By: "cpu", Span: 12, Height: 2},
		{Kind: "statebar", Span: 12},
	}},
	// Three pages, twenty seconds each: what needs answering, what is running,
	// what it is costing. A wall that shows one thing forever wastes the wall.
	{ID: "rotating", Audience: audienceWall, Screen: screenWall, Rotate: 20, Fill: true,
		Widgets: []Widget{
			{Kind: "attention", Span: 12, Height: 2, Page: 0},
			{Kind: "sessiongrid", Filter: "waiting", Order: "waited", Span: 12, Height: 2, Page: 0},
			{Kind: "states", Span: 12, Page: 1},
			{Kind: "sessiongrid", Filter: "active", Order: "cpu", Span: 12, Height: 3,
				Rotate: 10, Page: 1},
			{Kind: "tokenburn", Span: 6, Height: 2, Page: 2},
			{Kind: "spendcompare", Span: 6, Height: 2, Page: 2},
			{Kind: "spendbars", By: "day", Days: 14, Span: 12, Height: 2, Page: 2},
		}},
	// The one they asked for by name: what it is spending, as the whole screen.
	// A hero that says when it was counted, a line that moves, and the split
	// underneath so the hero means something.
	{ID: "burn", Audience: audienceWall, Screen: screenWall, Fill: true, Widgets: []Widget{
		{Kind: "tokenburn", Span: 12, Height: 3},
		{Kind: "machinearea", By: "cpu", Span: 6, Height: 2},
		{Kind: "spendstack", By: "day", Days: 14, Span: 6, Height: 2},
		{Kind: "odometer", Span: 6, Height: 2},
		{Kind: "spendsplit", By: "model", Span: 6, Height: 2},
	}},
	// A screen in a corridor. It is a clock most of the time, so it is a clock
	// first, and the room's own name is on it rather than in a settings page.
	{ID: "atrium", Audience: audienceWall, Screen: screenBig, Fill: true, Widgets: []Widget{
		{Kind: "remark", Span: 8},
		{Kind: "datetime", Span: 4, Height: 2},
		{Kind: "statebar", Span: 8},
		{Kind: "sessiongrid", Filter: "all", Order: "state", Span: 12, Height: 4},
	}},

	{ID: "machine", Audience: audienceOps, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "machinearea", By: "cpu", Span: 6, Height: 2},
		{Kind: "machinearea", By: "memory", Span: 6, Height: 2},
		{Kind: "gauge", Metric: "disk", Span: 3},
		{Kind: "gauge", Metric: "swap", Span: 3},
		{Kind: "uptime", Span: 3},
		{Kind: "health", Span: 3},
		{Kind: "cputop", By: "cpu", Span: 6},
		{Kind: "cputop", By: "memory", Span: 6},
	}},
	{ID: "health", Audience: audienceOps, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "exits", Span: 6},
		{Kind: "cputop", By: "cpu", Span: 6},
		{Kind: "gauge", Metric: "cpu", Span: 3},
		{Kind: "gauge", Metric: "memory", Span: 3},
		{Kind: "gauge", Metric: "disk", Span: 3},
		{Kind: "health", Span: 3},
		{Kind: "sessionlist", Filter: "trouble", Order: "state", Group: "project", Span: 12},
	}},

	// The 4K wall behind somebody's desk, in three tiers.
	//
	// Hero: what it is spending today, and how many agents are working, at a
	// size that reads across a room. Movement: the machine, drawn as a line
	// that changes — a wall of still numbers cannot be told from a wall that
	// has frozen. Texture: where the work went and what is running, which is
	// what somebody walking closer is walking closer to read.
	//
	// Nothing here is a value or a productivity figure. The panel knows what
	// the agents recorded; it does not know money and it does not read a
	// repository, so the impressive axis is scale and liveness, both of which
	// are true.
	{ID: "exec", Audience: audienceManager, Screen: screenBig, Fill: true, Widgets: []Widget{
		{Kind: "tokenburn", Span: 6, Height: 3},
		{Kind: "output", Span: 3, Height: 3},
		{Kind: "bignumber", Metric: "working", Span: 3, Height: 3},
		{Kind: "machinearea", By: "cpu", Span: 6, Height: 2},
		{Kind: "spendstack", By: "day", Days: 30, Span: 6, Height: 2},
		{Kind: "spendsplit", By: "project", Span: 6, Height: 2},
		{Kind: "sessiongrid", Filter: "active", Order: "state", Span: 6, Height: 2},
		{Kind: "nowstrip", Span: 12},
	}},

	// The television as the room's central display, and the design decision it
	// is: hierarchy from size ratio rather than from colour, five things rather
	// than twelve, and something that visibly moves.
	//
	// The hero is *production* -- commits, lines changed, files touched today.
	// It used to be "sessions finished today" and on a real wall that read 0,
	// because it is self-reported and a session left running all day never
	// reports it. Beside it, at a third of the width, is the one number that is
	// an instruction rather than a report: how many agents are waiting for a
	// person. Under both, the two series that answer "what did it cost, what
	// came out of it" on one axis, which is the pairing this whole board exists
	// for and could not be drawn before the panel read repositories.
	//
	// The feed is what stops a wall being a screenshot. Empty space and one
	// full-width strip at the bottom are the composition -- a grid of equal
	// cards is a dashboard; a hero, a movement band and a rule is a display.
	{ID: "newsroom", Audience: audienceManager, Screen: screenBig, Fill: true,
		Density: MinDensity, Widgets: []Widget{
			{Kind: "output", Span: 8, Height: 2},
			{Kind: "bignumber", Metric: "waiting", Span: 4, Height: 2},
			{Kind: "spentmade", Span: 8, Height: 2, Days: 14},
			{Kind: "feed", Span: 4, Height: 2},
			{Kind: "nowstrip", Span: 12},
		}},
	// The same room, from the chair in front of it.
	//
	// Same screen, same distance from the wall, and everything on it says more:
	// this is what Density is for and why it is not derived from the viewport.
	// Nothing here is smaller than the board above -- it is denser, which is a
	// different axis.
	{ID: "deskwall", Audience: audienceManager, Screen: screenWall, Fill: true,
		Density: MaxDensity, Widgets: []Widget{
			{Kind: "output", Span: 4, Height: 2},
			{Kind: "prs", Span: 4, Height: 2},
			{Kind: "tokenburn", Span: 4, Height: 2},
			{Kind: "spentmade", Span: 12, Height: 2, Days: 30},
			{Kind: "codechurn", By: "lines", Days: 14, Span: 4, Height: 2},
			{Kind: "flow", By: "hour", Span: 4, Height: 2},
			{Kind: "feed", Span: 4, Height: 2},
			{Kind: "sessionlist", Filter: "active", Order: "state", Group: "project", Span: 12},
		}},
	// What was built today, and nothing about what it cost.
	//
	// The board for somebody who wants the answer to "did anything ship" and
	// does not want a cost figure in the same glance -- the two are worth
	// pairing on a chart and not worth mixing in a headline.
	{ID: "made", Audience: audienceManager, Screen: screenWall, Fill: true, Widgets: []Widget{
		{Kind: "bignumber", Metric: "commitsToday", Span: 4, Height: 2},
		{Kind: "bignumber", Metric: "linesChanged", Span: 4, Height: 2},
		{Kind: "bignumber", Metric: "prsMergedToday", Span: 4, Height: 2},
		{Kind: "codechurn", By: "lines", Days: 30, Span: 12, Height: 2},
		{Kind: "repoprojects", By: "commits", Span: 6},
		{Kind: "prs", Span: 6},
	}},
	// The pairing on its own, over a month, with the split underneath.
	{ID: "spentmade", Audience: audienceManager, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "spentmade", Span: 12, Days: 30, Height: 2},
		{Kind: "output", Span: 6},
		{Kind: "spendtotals", Span: 6},
		{Kind: "repoprojects", By: "lines", Span: 6},
		{Kind: "spendsplit", By: "project", Span: 6},
	}},
	// Pull requests and what is in front of them.
	{ID: "shipping", Audience: audienceManager, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "prs", Span: 6, Height: 2},
		{Kind: "output", Span: 6, Height: 2},
		{Kind: "codechurn", By: "commits", Days: 30, Span: 12},
		{Kind: "repoprojects", By: "commits", Span: 12},
	}},
	// One customer's own project, and nobody else's name anywhere near it.
	//
	// Detail and scope are carried by the preset rather than left to somebody
	// to remember, because the failure here is not an ugly screen: it is a
	// customer reading another customer's project name off the wall they were
	// sat in front of. The editor applies both; the handler validates both
	// again, from the request, exactly as it does for every other link.
	{ID: "client", Audience: audienceManager, Screen: screenWall, Fill: true,
		Detail: string(ShareCounts), NeedsScope: true, Widgets: []Widget{
			{Kind: "remark", Span: 12},
			{Kind: "bignumber", Metric: "doneToday", Span: 4, Height: 2},
			{Kind: "bignumber", Metric: "working", Span: 4, Height: 2},
			{Kind: "gauge", Metric: "todoPercent", Span: 4, Height: 2},
			{Kind: "statebar", Span: 12},
			{Kind: "todos", Span: 6, Height: 2},
			{Kind: "timeline", Filter: "active", Order: "waited", Span: 6, Height: 2},
		}},
	// What it cost next to what came out of it. A board of costs alone reads as
	// an expense report; a board with both reads as work.
	{ID: "boss", Audience: audienceManager, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "output", Span: 6},
		{Kind: "spendcompare", Span: 6},
		{Kind: "todos", Span: 6},
		{Kind: "spendtotals", Span: 6},
		{Kind: "spendsplit", By: "project", Span: 12},
	}},
	{ID: "cost", Audience: audienceManager, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "odometer", Span: 6},
		{Kind: "spendrate", Span: 6},
		{Kind: "spendsplit", By: "tool", Span: 4},
		{Kind: "spendsplit", By: "model", Span: 4},
		{Kind: "spendsplit", By: "project", Span: 4},
		{Kind: "spendstack", By: "month", Span: 12, Height: 2},
	}},
	{ID: "leadership", Audience: audienceManager, Screen: screenWall, Widgets: []Widget{
		{Kind: "gauge", Metric: "todoPercent", Span: 3},
		{Kind: "bignumber", Metric: "doneToday", Span: 3},
		{Kind: "bignumber", Metric: "tokensMonth", Span: 6},
		{Kind: "projects", Span: 6},
		{Kind: "todos", Span: 6},
		{Kind: "spendheatmap", Span: 12},
	}},
	{ID: "year", Audience: audienceManager, Screen: screenWall, Widgets: []Widget{
		{Kind: "spendheatmap", Span: 12, Height: 2},
		{Kind: "spendbars", By: "month", Span: 12, Height: 2},
	}},

	{ID: "projects", Audience: audienceDetail, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "projects", Span: 6},
		{Kind: "todos", Span: 6},
		{Kind: "busiest", Span: 6},
		{Kind: "spendsplit", By: "project", Span: 6},
		{Kind: "sessionlist", Filter: "all", Order: "state", Group: "project", Span: 12},
	}},
	{ID: "models", Audience: audienceDetail, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "spendsplit", By: "model", Span: 6},
		{Kind: "spendrate", Span: 6},
		{Kind: "spendtotals", Span: 6},
		{Kind: "spendsplit", By: "tool", Span: 6},
	}},
	{ID: "spendToday", Audience: audienceDetail, Screen: screenLaptop, Widgets: []Widget{
		{Kind: "bignumber", Metric: "tokensToday", Span: 6},
		{Kind: "bignumber", Metric: "requestsToday", Span: 6},
		{Kind: "spendcompare", Span: 6},
		{Kind: "sparkline", By: "day", Days: 14, Span: 3},
		{Kind: "spendsplit", By: "tool", Span: 3},
		{Kind: "spendbars", By: "day", Days: 14, Span: 12},
	}},
}

// density is the preset's own setting, or the default where it did not say.
func (p Preset) density() int {
	if p.Density < MinDensity || p.Density > MaxDensity {
		return DefaultDensity
	}
	return p.Density
}

// Presets returns the catalogue, in the order it is offered.
func Presets() []Preset {
	out := make([]Preset, 0, len(presets))
	for _, p := range presets {
		out = append(out, Preset{
			ID: p.ID, Audience: p.Audience, Screen: p.Screen, Rotate: p.Rotate, Fill: p.Fill,
			Density: p.density(), Detail: p.Detail, NeedsScope: p.NeedsScope,
			Widgets: copyWidgets(p.Widgets)})
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
			return Board{Grid: GridColumns, Preset: p.ID, Rotate: p.Rotate, Fill: p.Fill,
				Density: p.density(), Widgets: copyWidgets(p.Widgets)}, true
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
		return Board{Grid: GridColumns, Density: DefaultDensity,
			Widgets: []Widget{{Kind: "states", Span: MaxSpan}}}
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
