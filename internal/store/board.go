package store

import (
	"encoding/json"
	"fmt"
)

// A board is what a share link shows, and it is data.
//
// The link is the capability; the board is the arrangement drawn with it. That
// separation is the whole design here, and the property that has to survive
// every edit to this file is that a board can only ever *subtract*. The server
// computes one redacted superset for a link at a given ShareDetail — see
// internal/httpapi/share.go for what is in it — and the board picks which parts
// of that superset are written to the wire. There is no widget, and no option
// on one, that makes the server read something it would not otherwise read.
//
// So a stored board is never a capability, and the two ways it could become one
// are both closed here rather than at the call site:
//
//   - An unknown widget kind is refused on the way in and dropped on the way
//     out, never resolved to a neighbouring kind. A kind no build understands
//     has to render as nothing, because the alternative is a stored string
//     choosing a code path.
//   - Every field is an enum or a bounded number. Nothing here is a URL, a
//     path, a query, a template or a colour, and the only free text is a
//     caption the owner typed, cut to a length and rendered through safeText on
//     the far side. A board that could name a source is a board that could
//     point the panel at one.

// MaxWidgets bounds a board.
//
// Not a rendering limit — a wall with forty widgets is unreadable long before
// this — but the bound on how much work one row of one table can make the
// dashboard do. Every list on a board is bounded somewhere, and this is where
// the list of lists is bounded.
const MaxWidgets = 24

// MaxCaption is how much text a caption widget keeps, in runes.
//
// Runes rather than bytes: this is cut and then rendered, and a byte slice
// through a multi-byte character renders the last one as U+FFFD. The same
// mistake session.TruncateTitle exists to avoid on a session title.
const MaxCaption = 64

// GridColumns is how many columns a board is divided into.
//
// Twelve rather than four, and the reason is the screen this is now aimed at.
// Four columns can express a half and a quarter and nothing else: a wall of
// 3840 pixels laid out in quarters is four cards in a row, forever, and a third
// -- the width that makes three things read as three things -- cannot be said
// at all. Twelve divides by 2, 3, 4 and 6, which is the whole reason every
// layout grid ever shipped picked it.
//
// Boards stored before this are in quarters and are converted on the way
// through, not migrated in SQL: see normaliseGrid. A board is a JSON document
// in a TEXT column, and parsing JSON in a migration to multiply one field is
// the sort of step that cannot be re-run and cannot be reviewed.
const GridColumns = 12

// MaxSpan is the widest a widget may be.
const MaxSpan = GridColumns

// gridSteps are the widths worth offering, in twelfths.
//
// Not all twelve, and this is a product decision served from the server rather
// than a second list in the editor. A select with twelve entries is a control
// nobody can aim at, and 5/12 and 7/12 are widths whose only use is stopping a
// row from lining up. These are the fractions twelve columns exist to provide:
// a sixth, a quarter, a third, a half, two thirds, three quarters, all of it.
//
// The validator still accepts any span from 1 to MaxSpan -- a hand-written
// board is allowed to be strange. This is what the editor offers, and it is
// here so that "what the editor offers" and "what a preset uses" cannot drift
// apart in two files.
var gridSteps = []int{2, 3, 4, 6, 8, 9, 12}

// GridSteps returns the widths the editor offers.
func GridSteps() []int {
	out := make([]int, len(gridSteps))
	copy(out, gridSteps)
	return out
}

// MaxRows is the tallest a widget may be, in grid rows.
//
// Height is the dimension a flat, ordered, auto-flowing list did not have, and
// the one a wall wants: a session list beside two stacked figures is a tall
// thing next to two short ones. Bounded low because a widget four rows tall
// already fills a television, and a row count driven by a stored number is a
// page whose height a row in a table decides.
const MaxRows = 4

// MaxSpendDays bounds the day range a bar widget may ask for.
//
// 371 is the same 53 whole weeks the year grid uses. Anything longer is a
// GROUP BY with no ceiling driven by a row in a table, which is the shape the
// token endpoint's clamp already exists to close.
const MaxSpendDays = 371

// MaxPages is how many pages a rotating board may have.
//
// Twelve at, say, twenty seconds each is four minutes before anything comes
// back round, which is already longer than anybody stands in front of a wall.
// The bound is here because a page number is a row in a table deciding how many
// arrangements the browser builds.
const MaxPages = 12

// MaxRotateSeconds bounds a rotation interval: an hour.
//
// The floor is where the thought is. Anything under a couple of seconds is a
// screen nobody can read, so the editor offers sensible steps; a hand-written
// board may still ask for one second, and one second is legible for a single
// large number even if it is not for a list.
const MaxRotateSeconds = 3600

// Widget is one thing on a board.
//
// One flat struct rather than a kind-tagged union, because a union in JSON is
// decoded by reading a string and choosing a type — which is exactly the move
// this file is trying not to make. Here every field is decoded the same way
// whatever the kind is, and the registry then says which of them this kind may
// have set. A field that does not belong to the kind is an error rather than an
// ignored value: ignoring it silently produces a board that does not do what
// its author asked and never says so.
type Widget struct {
	Kind string `json:"kind"`
	// Span is 1..MaxSpan columns. Zero means the kind's own default, which is
	// what a board written by hand gets.
	Span int `json:"span"`
	// Metric names which number a one-number widget shows.
	Metric string `json:"metric,omitempty"`
	// Filter narrows a list of sessions; Order sorts it; Group decides what it
	// is broken into.
	Filter string `json:"filter,omitempty"`
	Order  string `json:"order,omitempty"`
	Group  string `json:"group,omitempty"`
	// By is the dimension a chart is cut along: day or month for a series,
	// agent or project or model for a breakdown.
	//
	// A setting rather than four separate widget kinds, because "split it by X"
	// is the same question every time and a vocabulary that spelled each
	// answer as its own kind would grow by one every time a new column became
	// interesting.
	By string `json:"by,omitempty"`
	// Days is the range of a spend bar chart.
	Days int `json:"days,omitempty"`
	// Page is which page of a rotating board this widget belongs to, 0-based.
	//
	// A wall that shows one thing forever wastes the wall. Pages are a field on
	// the widget rather than a list of boards because everything else about a
	// board — its name, its link, its detail mode — is the same on every page,
	// and a list of boards would need all of it repeated per page and kept in
	// step by hand.
	Page int `json:"page,omitempty"`
	// Rotate is how many seconds one page of a long list stays on screen, or 0
	// for no rotation. A grid of forty sessions on a screen that fits twelve is
	// a grid showing twelve sessions.
	Rotate int `json:"rotate,omitempty"`
	// Text is a caption the owner typed. The only free text on a board.
	Text string `json:"text,omitempty"`
	// Height is 1..MaxRows grid rows. Zero means one row, which is what every
	// board written before heights existed meant.
	//
	// Omitted when it is one, so a board that never asked for a tall widget
	// serialises exactly as it did before -- which is what keeps the diff of a
	// stored column readable by whoever is looking at why a wall changed.
	Height int `json:"height,omitempty"`
}

// The data a widget needs, as tags the HTTP layer maps to sections of the
// response. A tag is not permission to read anything: every section is computed
// for every link, and these decide only what is put on the wire.
const (
	NeedSessions      = "sessions"
	NeedTodos         = "todos"
	NeedSpend         = "spend"
	NeedSpendDays     = "spendDays"
	NeedSpendMonths   = "spendMonths"
	NeedSpendHeatmap  = "spendHeatmap"
	NeedSpendTools    = "spendTools"
	NeedSpendProjects = "spendProjects"
	NeedSpendModels   = "spendModels"
	// NeedTrend is the short ring of recent machine and token readings.
	//
	// The one section that is not a reading of right now but a few minutes of
	// them, kept in memory on the server. It is per panel and not per viewer:
	// two walls on two links draw the same line, because it is the same
	// machine.
	NeedTrend = "trend"

	// NeedFlow and NeedFeed come out of the session-event log.
	//
	// The log is what made every trend on this dashboard buildable: before it,
	// the panel stored one `state_changed_at` per session and nothing about
	// what came before, so every widget with a time axis degraded to a single
	// current number. NeedFlow is the bucketed series; NeedFeed is the last few
	// transitions in order, which is the thing on a wall that visibly moves.
	NeedFlow = "flow"
	NeedFeed = "feed"

	// NeedRepo, NeedRepoDays and NeedRepoPRs come out of the working trees.
	//
	// The half of "what did it cost / what came out of it" that could not be
	// built while the panel did not read repositories. Split three ways because
	// they cost three different things: the totals are one `git log` per
	// project behind a background-refreshed cache, the day series is the same
	// read written out per day, and the pull requests are a request to GitHub.
	NeedRepo     = "repo"
	NeedRepoDays = "repoDays"
	NeedRepoPRs  = "repoPRs"
)

// widgetSpec is what one kind may carry.
//
// A nil list means the field must be empty for this kind. An empty non-nil
// list is not a thing: a kind either takes a value from a set or takes none.
type widgetSpec struct {
	span    int
	metrics []string
	filters []string
	orders  []string
	groups  []string
	bys     []string
	days    bool
	text    bool
	needs   []string
	// byNeeds is what a widget needs *because of which dimension it is split
	// by*. "Split it by X" is a setting rather than a different widget, so the
	// section it pulls has to follow the setting rather than the kind —
	// otherwise a board split by model still carries the by-project table.
	byNeeds map[string]string
}

// bigMetrics is what a single-number widget can be pointed at.
//
// Spelled out rather than derived from the response's field names. Deriving it
// would make every field added to the payload later into a metric somebody can
// put on a wall without anyone having decided that it should be.
var bigMetrics = []string{
	// What is happening.
	"waiting", "working", "done", "sessions", "projects", "crashed", "exited",
	"longestWait",
	// What was produced. These are the headline numbers, and which ones they
	// are is a decision that was made twice.
	//
	// It used to be "sessions finished today" and "todos ticked today", and on
	// a real wall both of them read 0 next to a four-figure request count. They
	// were not unlucky: both are *self-reported*. A todo is ticked because
	// somebody remembered to; a session reaches `done` because an agent's hook
	// said so, and a session left running all day never says it at all. They
	// measure whether the panel was told something.
	//
	// Commits, changed lines and merged pull requests measure things that exist
	// now and did not this morning, and anybody can check them by looking at
	// the repository. `todosClosedToday` is gone from this list entirely -- the
	// `todos` widget still shows a project's checklist as a fraction, which is
	// where a checklist figure belongs. `doneToday` stays, because a session
	// finishing is a real event; it is simply not the size of a hero, and no
	// preset here uses it as one any more.
	"commitsToday", "commitsWindow", "linesAdded", "linesRemoved", "linesChanged",
	"filesToday", "openPRs", "prsMergedToday", "checksRed",
	"doneToday", "todosOpen", "todosDone", "todoPercent",
	// How the day has gone, out of the session-event log.
	"startedToday", "waitsToday", "avgWaitToday",
	// What the machine is doing.
	"cpu", "memory", "disk", "swap", "load", "uptime",
	// What it cost, and how fast it is costing it.
	"tokensToday", "tokensMonth", "tokensWindow", "requestsToday", "tokensPerHour",
}

// gaugeMetrics is what an arc gauge can show: a figure with a ceiling, so the
// arc means something. Load has no ceiling; the todo percentage does.
var gaugeMetrics = []string{"cpu", "memory", "disk", "swap", "todoPercent"}

var sessionFilters = []string{"all", "active", "waiting", "trouble"}

var sessionOrders = []string{"state", "waited", "cpu"}

var sessionGroups = []string{"project", "state", "none"}

// seriesBy is what a time series can be bucketed by; splitBy is what a ranked
// breakdown can be split by.
//
// Two lists rather than one, because they are two questions. "How has it gone
// over time" and "where did it go" are different charts, and a widget that
// answered both from one setting would have to guess which one is being asked.
var seriesBy = []string{"day", "month"}

var splitBy = []string{"tool", "project", "model"}

// costBy is which cost a "heaviest sessions" list is ranked by.
var costBy = []string{"cpu", "memory"}

// pressureBy is which machine pressure the moving area chart draws.
var pressureBy = []string{"cpu", "memory", "load"}

// flowBy is how finely the session-event series is cut.
//
// Two, and they are two different questions rather than two resolutions of one.
// "Hour" is today, and it is the shape of an afternoon; "day" is the window, and
// it is whether this week is like last week. A single control with six steps
// would be a chart whose axis nobody standing in front of it can identify.
var flowBy = []string{"hour", "day"}

// churnBy is which production figure the repository series draws.
//
// Lines are two numbers and not one: +1200/-800 is a different day from
// +400/-0, and a net figure hides a refactor completely. That is why "lines" is
// one choice here rather than two, and why the widget draws both sides of the
// axis.
var churnBy = []string{"lines", "commits", "files"}

// widgetKinds is the vocabulary. Nothing outside this map renders.
//
// Spans are in twelfths. The set is grouped by the question it answers rather
// than by the table it reads, because that is the axis somebody composing a
// screen is thinking on -- and because a catalogue organised by data source
// produces thirty arrangements of the same grid.
//
// It is also composed in *tiers*, which is the thing a wall needs and a
// settings page does not. A screen read at three metres carries five to nine
// things before it is noise, and the failure is uniform card size: every tile
// the same size is a dashboard, not a display. So:
//
//	hero      one figure that dominates, legible across a room
//	movement  something that visibly changes, which is what proves the screen
//	          is live rather than a screenshot somebody left up
//	texture   the fine grid that fills the rest and rewards a closer look
//	furniture space, rules and words, without which "filled" becomes "crowded"
//
// The tier is not a field. It is which kind you pick and how many twelfths and
// rows you give it, because a tier stored on a widget would be a second way of
// saying the same thing and the two would come to disagree.
var widgetKinds = map[string]widgetSpec{
	// -- does anything need me --
	// "Does anything need me", from across a room: the waiting count at
	// headline size, and how long the oldest one has been waiting.
	"attention": {span: 12, needs: []string{NeedSessions}},
	// The state tallies, each with its own shape.
	"states": {span: 12},
	// The same tallies as one proportional strip. A count says how many; a
	// strip says what the shape of the afternoon is, from the door.
	"statebar": {span: 12},
	// One row, full width: working, waiting, done, load, uptime. The cheapest
	// thing that makes a wall read as composed rather than assembled, and the
	// only widget meant to sit under a hero rather than beside one.
	"nowstrip": {span: 12},

	// -- what is running --
	// Every session as a tile: the many-at-once view.
	"sessiongrid": {span: 12, filters: sessionFilters, orders: sessionOrders,
		needs: []string{NeedSessions}},
	// Every session as a row: the dense view. `group` is the dimension --
	// by project, by state, or a flat list.
	"sessionlist": {span: 12, filters: sessionFilters, orders: sessionOrders,
		groups: sessionGroups, needs: []string{NeedSessions}},
	// How long each session has been where it is, as bars on one shared scale.
	//
	// Deliberately dwell and not history. The panel stores when a session
	// entered its current state and not the states before it, so a bar
	// segmented by state over the last hour would be drawn from data that does
	// not exist. This draws the part that is true, and it is still the widget
	// that turns "seventeen agents" from a number into a picture.
	"timeline": {span: 12, filters: sessionFilters, orders: sessionOrders,
		needs: []string{NeedSessions}},
	// Agents, shells and everything else, counted. "Four agents and two
	// shells" is a different sentence from "six sessions".
	"kinds": {span: 3, needs: []string{NeedSessions}},
	// Per-project progress: how many of each project's sessions are where.
	"projects": {span: 6},
	// The projects with the most running, ranked and cut. `projects` lists
	// every group; this answers "where is it all happening" in four rows.
	"busiest": {span: 6, needs: []string{NeedSessions}},
	// What has exited, and what exited badly.
	"exits": {span: 6, needs: []string{NeedSessions}},

	// -- what came out --
	// How much of each project's checklist is finished. Counts only -- a todo
	// line is never on the wire, at either detail setting.
	"todos": {span: 6, needs: []string{NeedTodos}},
	// What was produced today: commits, changed lines, files touched.
	//
	// This widget used to be "sessions finished, todos ticked off, requests
	// made", and on a real wall the first two read 0 next to a four-figure
	// request count. Both were self-reported -- a todo is ticked because
	// somebody remembered to, and a session reaches `done` because a hook said
	// so -- so they measured whether the panel had been told something. Commits
	// and changed lines are things that exist now and did not this morning, and
	// they can be checked by looking at the repository.
	//
	// The lines are labelled as change, never as output, and they are two
	// numbers rather than a net one. See churnBy.
	"output": {span: 6, needs: []string{NeedRepo}},
	// The repository series: commits, changed lines or files touched, per day.
	// The movement tier for the production half.
	"codechurn": {span: 6, bys: churnBy, days: true,
		needs: []string{NeedRepo, NeedRepoDays}},
	// What it cost and what came out of it, on one time axis.
	//
	// The thing this whole dashboard was asked for and could not do: the panel
	// had the cost half and nothing to put beside it. Two series, deliberately
	// not one ratio -- tokens per line is a nonsense number that would be quoted
	// at somebody in a meeting.
	"spentmade": {span: 12, days: true,
		needs: []string{NeedSpend, NeedSpendDays, NeedRepo, NeedRepoDays}},
	// Where the commits went, by project.
	"repoprojects": {span: 6, bys: churnBy, needs: []string{NeedRepo}},
	// Open pull requests, drafts, and whether the checks are green.
	"prs": {span: 6, needs: []string{NeedRepo, NeedRepoPRs}},

	// -- how the day has gone --
	// Sessions started, sessions that went quiet waiting, sessions finished --
	// per hour of today or per day of the window.
	//
	// Out of the session-event log, which is the reason a board can show a
	// trend at all. `by` chooses the axis; see flowBy.
	"flow": {span: 6, bys: flowBy, days: true, needs: []string{NeedFlow}},
	// How long things sat waiting before somebody got to them, over the same
	// axis. The queue question, answered as a duration rather than a depth --
	// see internal/store/events.go for why a flow log cannot honestly draw a
	// depth.
	"waits": {span: 6, bys: flowBy, days: true, needs: []string{NeedFlow}},
	// What just happened, newest first.
	//
	// The other half of the movement tier, and the honest way to fill a
	// television: a screen where nothing ever changes cannot be told from a
	// screenshot somebody left up, and a list that gains a line when an agent
	// finishes is the cheapest proof it is live. It carries exactly what a
	// session row already carries -- a pseudonym, a state, a time -- so it is
	// no new disclosure, only the same facts in the order they happened.
	"feed": {span: 6, needs: []string{NeedFeed}},

	// -- the machine --
	// The four machine meters together.
	"machine": {span: 6},
	// One pressure as an arc.
	"gauge": {span: 3, metrics: gaugeMetrics},
	// CPU, memory or load over the last few minutes, as a filled line.
	//
	// The movement tier, and the reason it exists rather than a third gauge: a
	// gauge is a still picture of a number, and a wall of still pictures cannot
	// be told from a wall that has frozen. A line that moves is the cheapest
	// honest proof the screen is alive.
	"machinearea": {span: 6, bys: pressureBy, needs: []string{NeedTrend}},
	// Uptime and the three load averages.
	"uptime": {span: 3},
	// The sessions costing the most right now. `by` chooses which cost: a
	// session pinning a core and one holding eight gigabytes are two different
	// problems and one list answers neither on its own.
	"cputop": {span: 6, bys: costBy, needs: []string{NeedSessions}},
	// Whether the panel behind this screen is well: is it keeping its records
	// up to date, can it read the process tree, when does this link go dark.
	// A wall that has quietly stopped being true looks exactly like a quiet
	// afternoon, and this is the widget that says which.
	"health": {span: 3},

	// -- what it cost --
	// One number, as large as the widget is.
	"bignumber": {span: 3, metrics: bigMetrics},
	// The hero for token spend: today's total at headline size, the rate under
	// it, and the recent trend behind it.
	//
	// It says when it was counted, and that is not decoration. The figures come
	// from a pass over the agents' transcripts, so "now" is "as of the last
	// pass" -- a live meter implying a per-second reading would be a lie told
	// in large type. See internal/httpapi/share.go for the cadence.
	"tokenburn": {span: 6, needs: []string{NeedSpend, NeedTrend}},
	// Every token this panel has ever recorded, as one accumulating figure.
	// A chart says how it is going; an odometer says how far it has come, and
	// it is the only number here that only ever goes up.
	"odometer": {span: 6, needs: []string{NeedSpend}},
	// Today, this month and the window, with input, output and cache split out.
	"spendtotals": {span: 6, needs: []string{NeedSpend}},
	// How fast it is being spent, rather than how much in total.
	"spendrate": {span: 6, needs: []string{NeedSpend}},
	// Today against yesterday, this month against last. A total says what; a
	// comparison says whether that is a lot.
	"spendcompare": {span: 6, needs: []string{NeedSpend}},
	// Spend over time. `by` chooses the bucket.
	"spendbars": {span: 6, bys: seriesBy, days: true, needs: []string{NeedSpend},
		byNeeds: map[string]string{"day": NeedSpendDays, "month": NeedSpendMonths}},
	// The same series as one line at tile size. A bar chart needs a tile to
	// itself; a line reads beside the number it belongs to, which is the only
	// way a trend fits on a board that is mostly figures.
	"sparkline": {span: 3, bys: seriesBy, days: true, needs: []string{NeedSpend},
		byNeeds: map[string]string{"day": NeedSpendDays, "month": NeedSpendMonths}},
	// The series with the four token columns stacked. A total hides the thing
	// worth seeing: a day that is nine tenths cache reads and a day that is
	// nine tenths output are the same number and different afternoons.
	"spendstack": {span: 6, bys: seriesBy, days: true, needs: []string{NeedSpend},
		byNeeds: map[string]string{"day": NeedSpendDays, "month": NeedSpendMonths}},
	// Where the spend went, ranked. `by` chooses the dimension: which agent,
	// which project, which model.
	"spendsplit": {span: 6, bys: splitBy, needs: []string{NeedSpend},
		byNeeds: map[string]string{
			"tool": NeedSpendTools, "project": NeedSpendProjects, "model": NeedSpendModels}},
	// The year, as a grid of days.
	"spendheatmap": {span: 12, needs: []string{NeedSpend, NeedSpendHeatmap}},

	// -- the furniture --
	// The wall clock, for a screen somebody walks past.
	"clock": {span: 3},
	// The clock with the date and the day of the week under it. A screen in a
	// corridor is a clock most of the time, and the date is what somebody
	// standing in front of one actually looks for.
	"datetime": {span: 3},
	// Words the owner typed.
	"caption": {span: 12, text: true},
	// Words the owner typed, as a section heading over what follows.
	//
	// A caption is a sentence in a tile; this is a label with a rule under it
	// and no surface of its own. Grouping is the half of composition that gets
	// forgotten, and without it a filled screen is a crowded one.
	"heading": {span: 12, text: true},
	// A hairline across the board.
	"rule": {span: 12},
	// The remark the owner put on the link itself, at heading size.
	//
	// The same string the dashboard shows in its header, placeable: on a
	// rotating board the header is one line above every page, and the name of
	// the room the screen is in belongs on the page. It is the one widget whose
	// words the owner can change without touching the board at all.
	"remark": {span: 12},
	// Nothing at all, occupying its span.
	//
	// A flat auto-flowing list has no way to leave a hole, and a hole is how a
	// wall stops being a solid brick of tiles. This is the whole of the
	// explicit-placement vocabulary: a gap you can put anywhere, instead of a
	// coordinate per widget per breakpoint.
	"spacer": {span: 3},
}

// KnownWidgetKinds lists the vocabulary, sorted, for the settings page and for
// the test that pins it against the frontend.
func KnownWidgetKinds() []string { return sortedKeys(widgetKinds) }

// WidgetOptions reports what one kind may be given: the metrics, filters and
// orders it accepts, whether it takes a day range or a caption, and how wide it
// is by default.
//
// Exported so the settings page builds its editor from the server's answer
// rather than from a second copy of this table. A second copy is how a UI comes
// to offer a choice the server refuses.
func WidgetOptions(kind string) (spec WidgetSpec, ok bool) {
	s, ok := widgetKinds[kind]
	if !ok {
		return WidgetSpec{}, false
	}
	return WidgetSpec{
		Kind: kind, Span: s.span, Metrics: s.metrics, Filters: s.filters,
		Orders: s.orders, Groups: s.groups, Bys: s.bys, Days: s.days, Text: s.text,
		// Every kind takes a height, so this is a constant rather than a field
		// on the spec. It is served anyway: the editor builds its controls from
		// this answer and nothing else, and a control it has to know about
		// without being told is the first copy of the table.
		Rows: MaxRows,
		// A rotation only means something for a kind that draws a list longer
		// than its tile. Offering it on a gauge would be a control that does
		// nothing, which is worse than one that is missing.
		Rotate: s.filters != nil,
	}, true
}

// WidgetSpec is one kind as the settings page is told about it.
//
// Lists are the same slices the validator compares against, so an option the
// editor offers is an option the server accepts, by construction rather than
// by two people keeping two lists in step.
type WidgetSpec struct {
	Kind    string   `json:"kind"`
	Span    int      `json:"span"`
	Metrics []string `json:"metrics"`
	Filters []string `json:"filters"`
	Orders  []string `json:"orders"`
	Groups  []string `json:"groups"`
	Bys     []string `json:"bys"`
	Days    bool     `json:"days"`
	Text    bool     `json:"text"`
	// Rotate says this kind can page through a list that does not fit.
	Rotate bool `json:"rotate"`
	// Rows is how many grid rows tall this kind may be made.
	Rows int `json:"rows"`
}

// Board is one arrangement, as it is stored and as it is served.
type Board struct {
	// Grid is how many columns the spans below are counted in.
	//
	// Present so that a board stored when the grid was four columns wide is
	// still the board its author drew. Anything that is not GridColumns is read
	// as the old quarters and converted once, on the way through -- see
	// normaliseGrid. Without it, every span of 2 stored by an older build would
	// silently become a sixth of a screen instead of a half, on walls nobody is
	// standing in front of.
	Grid int `json:"grid"`
	// Preset is where this board started, kept so the editor can say so. It is
	// provenance and nothing reads it to decide behaviour — validated against
	// the catalogue anyway, because an unchecked string echoed back to the
	// dashboard is a string somebody will eventually render.
	Preset string `json:"preset"`
	// Rotate is how many seconds each page stays on screen, or 0 for a board
	// that does not move. Ignored when every widget is on page 0.
	Rotate int `json:"rotate"`
	// Fill stretches the rows to the height of the screen instead of letting
	// them flow and scroll.
	//
	// The difference between a board and a wall. A screen behind somebody's
	// desk with six tiles at the top and a field of background under them is
	// not filled, and nobody is going to scroll it -- there is nobody standing
	// there. Off by default, because the same board opens on a phone, where
	// stretching four rows to the height of a handset is four unreadable tiles.
	Fill bool `json:"fill"`
	// Density is how much each widget says, 1 (spare) to MaxDensity (dense).
	// Zero means DefaultDensity, which is what every board written before this
	// existed meant.
	//
	// **Density is not scale, and conflating them was the mistake this field
	// exists to correct.** The first design keyed how much a widget said to how
	// large it was drawn, on the reasoning that a television is read from three
	// metres and so must be sparse. That is wrong as the only case: the person
	// who asked for this sits in front of the same screen half the time, and
	// then wants it packed. So there are two axes and they are independent —
	// how large everything is drawn is a property of the *viewport* and is
	// settled in CSS with no stored value at all (see `.vp-wall` in
	// styles.css), and how much is on screen is a property of the *board* and
	// is this field. All four corners are real: dense and close, spare and
	// across the room, and both crosses.
	//
	// **Per board rather than per widget**, and that is the argument worth
	// having. Per widget it is one more control on every tile in the editor, it
	// is twenty-four decisions where one was wanted, and — the deciding
	// reason — the thing being adjusted is a property of the *room*: somebody
	// walks up to the screen and everything on it should have more to say, not
	// the one tile they remembered to set. A board is also the unit that
	// already travels: one stored board opens on a phone and on a wall, and one
	// step of this turns a wall back into a working dashboard without
	// rebuilding it.
	//
	// A widget with nothing more to say ignores it, which is why this is a hint
	// rather than a mode: it must never be the difference between a widget
	// rendering and not rendering, because that would be a stored number
	// choosing a code path.
	Density int      `json:"density"`
	Widgets []Widget `json:"widgets"`
}

// The density steps, and the default.
//
// Three, because the useful distinctions are "one thing at a time", "a working
// dashboard" and "everything it knows", and a fourth would be a slider nobody
// can aim at. DefaultDensity is the middle one so that a board written before
// this field existed opens as what it already was.
const (
	MinDensity     = 1
	MaxDensity     = 3
	DefaultDensity = 2
)

// normaliseGrid brings a board into this build's column count.
//
// A board stored when the grid was four columns wide says `span: 2` and means
// half a screen. Read against twelve columns that is a sixth, so every wall
// written before this change would have quietly rearranged itself -- on screens
// with nobody in front of them, which is the whole failure mode this file keeps
// coming back to.
//
// Applied on both paths, deliberately. The read path needs it for stored rows;
// the write path needs it because docs/api.md described spans as 1-4 and
// somebody's `curl` still says so. A board that arrives without a grid is a
// board in quarters, whichever direction it arrived from.
func normaliseGrid(b Board) Board {
	if b.Grid == GridColumns {
		return b
	}
	scale := GridColumns / 4
	out := b
	out.Grid = GridColumns
	out.Widgets = make([]Widget, len(b.Widgets))
	copy(out.Widgets, b.Widgets)
	for i := range out.Widgets {
		// Only a span that was legal in quarters is converted. A span of 99 is
		// not a wide widget, it is a bad value, and clamping it here would turn
		// the refusal validateWidget owes somebody into a silent repair -- the
		// exact thing the top of this file says a board must never get. Left as
		// it is, it fails the bound below, which is where it should fail. It
		// also cannot overflow, which multiplying an unbounded int could.
		if out.Widgets[i].Span > 0 && out.Widgets[i].Span <= 4 {
			out.Widgets[i].Span *= scale
		}
	}
	return out
}

// Pages is how many pages this board has.
func (b Board) Pages() int {
	most := 0
	for _, w := range b.Widgets {
		if w.Page > most {
			most = w.Page
		}
	}
	return most + 1
}

// Needs reports which sections of the dashboard payload this board asks for.
//
// Asks for, not may see: every section is computed for every link, and the
// answer here decides only what is written to the wire. A board narrows what a
// link discloses and has no way to widen it.
func (b Board) Needs() map[string]bool {
	out := map[string]bool{}
	for _, w := range b.Widgets {
		for _, n := range w.needs() {
			out[n] = true
		}
	}
	return out
}

// metricNeeds says which section a metric comes out of.
//
// Every metric not listed here comes from the counts, which are on every
// dashboard. Without this a board whose only figure is "sessions waiting" would
// still carry the whole spend section: every number the transcripts produced,
// on a link made to show one count.
var metricNeeds = map[string][]string{
	"todosOpen":     {NeedTodos},
	"todosDone":     {NeedTodos},
	"todoPercent":   {NeedTodos},
	"tokensToday":   {NeedSpend},
	"tokensMonth":   {NeedSpend},
	"tokensWindow":  {NeedSpend},
	"requestsToday": {NeedSpend},
	"tokensPerHour": {NeedSpend},
	// The production figures. NeedRepo is one `git log` per project behind a
	// background-refreshed cache, so a board whose only number is "sessions
	// waiting" must not pull it -- which is the whole reason this table exists.
	"commitsToday":  {NeedRepo},
	"commitsWindow": {NeedRepo},
	"linesAdded":    {NeedRepo},
	"linesRemoved":  {NeedRepo},
	"linesChanged":  {NeedRepo},
	"filesToday":    {NeedRepo},
	// The pull-request figures pull the rollup and *not* the working-tree
	// totals. A board whose only number is "how many are open" must not keep a
	// `git log` alive per project for a figure it does not draw.
	"openPRs":        {NeedRepoPRs},
	"prsMergedToday": {NeedRepoPRs},
	"checksRed":      {NeedRepoPRs},
	"startedToday":   {NeedFlow},
	"waitsToday":     {NeedFlow},
	"avgWaitToday":   {NeedFlow},
}

// needs is what one widget asks for, which for a widget with settings depends
// on the settings: which number it was pointed at, and which dimension it is
// split by. "Split it by X" being a setting rather than a separate kind is
// worth the indirection here — without it, a board split by model would still
// carry the by-project and by-agent tables it is not drawing.
func (w Widget) needs() []string {
	spec := widgetKinds[w.Kind]
	out := append([]string{}, spec.needs...)
	if spec.metrics != nil {
		out = append(out, metricNeeds[w.Metric]...)
	}
	if spec.bys != nil {
		by := w.By
		if by == "" && len(spec.bys) > 0 {
			by = spec.bys[0]
		}
		if need, ok := spec.byNeeds[by]; ok {
			out = append(out, need)
		}
	}
	return out
}

// ValidateBoard checks a board that arrived from a request.
//
// Refuses rather than repairs, everywhere except a caption's length. The person
// is at a keyboard looking at an editor, so an error naming the widget is
// something they can act on — and a board silently repaired into a different
// board is one whose author believes it says something it does not.
func ValidateBoard(b Board) (Board, error) {
	b = normaliseGrid(b)
	if b.Preset != "" && !KnownPreset(b.Preset) {
		return Board{}, fmt.Errorf("unknown preset %q", b.Preset)
	}
	if len(b.Widgets) == 0 {
		return Board{}, fmt.Errorf("a board needs at least one widget")
	}
	if len(b.Widgets) > MaxWidgets {
		return Board{}, fmt.Errorf("a board holds at most %d widgets", MaxWidgets)
	}
	if b.Rotate < 0 || b.Rotate > MaxRotateSeconds {
		return Board{}, fmt.Errorf("rotate must be between 0 and %d seconds", MaxRotateSeconds)
	}
	density := b.Density
	if density == 0 {
		density = DefaultDensity
	}
	if density < MinDensity || density > MaxDensity {
		return Board{}, fmt.Errorf("density must be between %d and %d", MinDensity, MaxDensity)
	}
	out := Board{Grid: GridColumns, Preset: b.Preset, Rotate: b.Rotate, Fill: b.Fill,
		Density: density, Widgets: make([]Widget, 0, len(b.Widgets))}
	for i, w := range b.Widgets {
		clean, err := validateWidget(w)
		if err != nil {
			return Board{}, fmt.Errorf("widget %d: %w", i+1, err)
		}
		out.Widgets = append(out.Widgets, clean)
	}
	return out, nil
}

func validateWidget(w Widget) (Widget, error) {
	spec, ok := widgetKinds[w.Kind]
	if !ok {
		// Named in the error on purpose. Falling back to some default kind
		// turns a typo into a board that renders something nobody asked for,
		// and turns a newer build's kind into a quietly different board on an
		// older one.
		return Widget{}, fmt.Errorf("unknown widget kind %q", w.Kind)
	}
	if w.Span == 0 {
		w.Span = spec.span
	}
	if w.Span < 1 || w.Span > MaxSpan {
		return Widget{}, fmt.Errorf("span must be between 1 and %d", MaxSpan)
	}
	if err := oneOf("metric", w.Metric, spec.metrics, w.Kind); err != nil {
		return Widget{}, err
	}
	if err := oneOf("filter", w.Filter, spec.filters, w.Kind); err != nil {
		return Widget{}, err
	}
	if err := oneOf("order", w.Order, spec.orders, w.Kind); err != nil {
		return Widget{}, err
	}
	if err := oneOf("group", w.Group, spec.groups, w.Kind); err != nil {
		return Widget{}, err
	}
	if err := oneOf("by", w.By, spec.bys, w.Kind); err != nil {
		return Widget{}, err
	}
	if !spec.days && w.Days != 0 {
		return Widget{}, fmt.Errorf("%s takes no day range", w.Kind)
	}
	if w.Days < 0 || w.Days > MaxSpendDays {
		return Widget{}, fmt.Errorf("days must be between 1 and %d", MaxSpendDays)
	}
	if w.Height == 0 {
		w.Height = 1
	}
	if w.Height < 1 || w.Height > MaxRows {
		return Widget{}, fmt.Errorf("height must be between 1 and %d", MaxRows)
	}
	if w.Page < 0 || w.Page >= MaxPages {
		return Widget{}, fmt.Errorf("page must be between 0 and %d", MaxPages-1)
	}
	if w.Rotate != 0 && spec.filters == nil {
		// Rotation pages through a list that does not fit. A gauge has no list,
		// so a rotation on one is a setting that does nothing -- which is worse
		// than one that is missing, because somebody sets it and then waits.
		return Widget{}, fmt.Errorf("%s has no list to rotate through", w.Kind)
	}
	if w.Rotate < 0 || w.Rotate > MaxRotateSeconds {
		return Widget{}, fmt.Errorf("rotate must be between 0 and %d seconds", MaxRotateSeconds)
	}
	if !spec.text {
		if w.Text != "" {
			return Widget{}, fmt.Errorf("%s takes no text", w.Kind)
		}
	} else {
		w.Text = truncateRunes(w.Text, MaxCaption)
	}
	return w, nil
}

// oneOf checks one bounded string field.
//
// A kind with no list for this field must have it empty. A kind with a list may
// have it empty — the renderer's own default applies — except for a metric,
// because a one-number widget with no number is a blank rectangle whose author
// will report it as a bug in the dashboard.
func oneOf(field, value string, allowed []string, kind string) error {
	if allowed == nil {
		if value != "" {
			return fmt.Errorf("%s takes no %s", kind, field)
		}
		return nil
	}
	if value == "" {
		if field == "metric" {
			return fmt.Errorf("%s needs a metric", kind)
		}
		return nil
	}
	for _, a := range allowed {
		if a == value {
			return nil
		}
	}
	return fmt.Errorf("%q is not a %s %s can show", value, field, kind)
}

// SanitiseBoard is the read path: whatever is in the column, made safe.
//
// Lenient where ValidateBoard is strict, and the asymmetry is deliberate. On
// the way in there is a person to tell. On the way out there is a wall display
// with nobody standing at it, so a board that has become unreadable — written
// by a newer build, edited in the database by hand, truncated — must still
// leave a working screen rather than an error page. Unknown widgets are
// dropped, never repaired; if nothing survives, the default board is used.
func SanitiseBoard(b Board) Board {
	b = normaliseGrid(b)
	out := Board{Grid: GridColumns, Fill: b.Fill, Density: DefaultDensity}
	// Clamped rather than refused, unlike the way in: there is nobody at the
	// wall to be told, and a density out of range is a screen that is still
	// worth drawing at the setting every board written before this had.
	if b.Density >= MinDensity && b.Density <= MaxDensity {
		out.Density = b.Density
	}
	if KnownPreset(b.Preset) {
		out.Preset = b.Preset
	}
	if b.Rotate > 0 && b.Rotate <= MaxRotateSeconds {
		out.Rotate = b.Rotate
	}
	for _, w := range b.Widgets {
		if len(out.Widgets) >= MaxWidgets {
			break
		}
		clean, err := validateWidget(w)
		if err != nil {
			continue
		}
		out.Widgets = append(out.Widgets, clean)
	}
	if len(out.Widgets) == 0 {
		return DefaultBoard()
	}
	return out
}

// EncodeBoard renders a board for the column.
func EncodeBoard(b Board) (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("store: encode board: %w", err)
	}
	return string(raw), nil
}

// DecodeBoard reads the column, and never fails.
//
// A row that cannot be parsed becomes the default board. The alternative is a
// dashboard answering 500 because of a character in a text column, on a screen
// with nobody in front of it.
func DecodeBoard(raw string) Board {
	if raw == "" {
		return DefaultBoard()
	}
	var b Board
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return DefaultBoard()
	}
	return SanitiseBoard(b)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	insertionSort(out)
	return out
}

// insertionSort keeps a sort import out of this file for two call sites over
// twenty-element slices.
func insertionSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
