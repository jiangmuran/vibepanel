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

// MaxSpan is the widest a widget may be, in a four-column grid.
const MaxSpan = 4

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
	// What came out, which is the half a board of costs alone leaves off.
	"doneToday", "todosOpen", "todosDone", "todosClosedToday", "todoPercent",
	// What the machine is doing.
	"cpu", "memory", "disk", "load", "uptime",
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

// widgetKinds is the vocabulary. Nothing outside this map renders.
var widgetKinds = map[string]widgetSpec{
	// "Does anything need me", from across a room: the waiting count at
	// headline size, and how long the oldest one has been waiting.
	"attention": {span: 4, needs: []string{NeedSessions}},
	// The state tallies, each with its own shape.
	"states": {span: 4},
	// One number, as large as the widget is.
	"bignumber": {span: 1, metrics: bigMetrics},
	// The wall clock, for a screen somebody walks past.
	"clock": {span: 1},
	// Words the owner typed. Nothing else on a board is free text.
	"caption": {span: 4, text: true},
	// Every session as a tile: the many-at-once view.
	"sessiongrid": {span: 4, filters: sessionFilters, orders: sessionOrders,
		needs: []string{NeedSessions}},
	// Every session as a row: the dense view. `group` is the dimension —
	// by project, by state, or a flat list.
	"sessionlist": {span: 4, filters: sessionFilters, orders: sessionOrders,
		groups: sessionGroups, needs: []string{NeedSessions}},
	// Per-project progress: how many of each project's sessions are where.
	"projects": {span: 2},
	// How much of each project's checklist is finished. Counts only — a todo
	// line is never on the wire, at either detail setting.
	"todos": {span: 2, needs: []string{NeedTodos}},
	// What came out today rather than what went in: sessions finished, todos
	// ticked off, requests made.
	"output": {span: 2, needs: []string{NeedSessions, NeedTodos, NeedSpend}},
	// The four machine meters together.
	"machine": {span: 2},
	// One pressure as an arc.
	"gauge": {span: 1, metrics: gaugeMetrics},
	// Uptime and the three load averages.
	"uptime": {span: 1},
	// The sessions costing the most right now.
	"cputop": {span: 2, needs: []string{NeedSessions}},
	// What has exited, and what exited badly.
	"exits": {span: 2, needs: []string{NeedSessions}},
	// Today, this month and the window, with input, output and cache split out.
	"spendtotals": {span: 2, needs: []string{NeedSpend}},
	// How fast it is being spent, rather than how much in total.
	"spendrate": {span: 2, needs: []string{NeedSpend}},
	// Today against yesterday, this month against last. A total says what; a
	// comparison says whether that is a lot.
	"spendcompare": {span: 2, needs: []string{NeedSpend}},
	// Spend over time. `by` chooses the bucket.
	"spendbars": {span: 2, bys: seriesBy, days: true, needs: []string{NeedSpend},
		byNeeds: map[string]string{"day": NeedSpendDays, "month": NeedSpendMonths}},
	// Where the spend went, ranked. `by` chooses the dimension: which agent,
	// which project, which model.
	"spendsplit": {span: 2, bys: splitBy, needs: []string{NeedSpend},
		byNeeds: map[string]string{
			"tool": NeedSpendTools, "project": NeedSpendProjects, "model": NeedSpendModels}},
	// The year, as a grid of days.
	"spendheatmap": {span: 4, needs: []string{NeedSpend, NeedSpendHeatmap}},
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
}

// Board is one arrangement, as it is stored and as it is served.
type Board struct {
	// Preset is where this board started, kept so the editor can say so. It is
	// provenance and nothing reads it to decide behaviour — validated against
	// the catalogue anyway, because an unchecked string echoed back to the
	// dashboard is a string somebody will eventually render.
	Preset string `json:"preset"`
	// Rotate is how many seconds each page stays on screen, or 0 for a board
	// that does not move. Ignored when every widget is on page 0.
	Rotate  int      `json:"rotate"`
	Widgets []Widget `json:"widgets"`
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
var metricNeeds = map[string]string{
	"todosOpen":        NeedTodos,
	"todosDone":        NeedTodos,
	"todosClosedToday": NeedTodos,
	"todoPercent":      NeedTodos,
	"tokensToday":      NeedSpend,
	"tokensMonth":      NeedSpend,
	"tokensWindow":     NeedSpend,
	"requestsToday":    NeedSpend,
	"tokensPerHour":    NeedSpend,
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
		if need, ok := metricNeeds[w.Metric]; ok {
			out = append(out, need)
		}
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
	out := Board{Preset: b.Preset, Rotate: b.Rotate, Widgets: make([]Widget, 0, len(b.Widgets))}
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
	out := Board{}
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
