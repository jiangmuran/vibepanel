// Package pages is what a share page may be: its manifest, its parameters, the
// files it is made of, and the scaffold an agent starts from.
//
// Nothing here serves anything or touches the database. internal/httpapi
// serves a page and internal/store keeps one; this package is the set of rules
// both of them, and the CLI running inside a page's directory, check against.
// One copy of the rules is the point: a Preview pane that accepted a file the
// publish step then refused would be a preview of something that cannot ship.
//
// See docs/share-pages.md for the design.
package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// SDKVersion is the snapshot contract a page is written against.
//
// A page states it in its manifest and the panel refuses one it does not
// serve. v1 is additive only: fields may be added to the snapshot, never
// renamed, retyped or removed. A change that breaks that is v2, served beside
// v1 for as long as a published page asks for it.
const SDKVersion = 1

// ManifestFile is the manifest's name, at the root of a page.
const ManifestFile = "vibepanel.json"

// MaxName is how long a page's name may be, in runes.
const MaxName = 64

// The sections a page may ask for. Every one is a fixed struct in
// internal/httpapi/share.go; a page chooses among them and has no vocabulary
// for anything that is not one of them.
const (
	SectionSessions = "sessions"
	SectionTodos    = "todos"
	SectionSpend    = "spend"
	SectionTrend    = "trend"
	SectionFlow     = "flow"
	SectionFeed     = "feed"
	SectionRepo     = "repo"
)

// Sections lists them in the order the documentation does.
func Sections() []string {
	return []string{SectionSessions, SectionTodos, SectionSpend, SectionTrend,
		SectionFlow, SectionFeed, SectionRepo}
}

// ScriptHosts are the only third-party hosts a page may load a script from.
//
// A fixed list rather than a field anybody can fill in, and the argument is
// the one the preview links' `allowExternal` already made: a script fetched
// from anywhere can put anything in its own URL, so "may load a script from X"
// is "may send what it read to X". These two are CDNs whose paths are package
// names, not somebody's endpoint; a compromised package on one still runs, and
// still lands in the sandbox with nothing but the snapshot it could already
// see. connect-src does not move for either.
var ScriptHosts = []string{"cdnjs.cloudflare.com", "cdn.jsdelivr.net"}

// Viewports are the screens the Preview pane frames a page at, by name.
//
// The same list web/scripts/board-check.mjs measures boards against, moved
// rather than re-chosen: these are the screens a share link actually gets put
// on, and a page composed for a size nobody owns is a page nobody sees right.
var Viewports = []Viewport{
	{"phone", 390, 844},
	{"ipad-portrait", 820, 1180},
	{"ipad-landscape", 1180, 820},
	{"laptop", 1440, 900},
	{"tv-1080", 1920, 1080},
	{"tv-4k-scaled", 2560, 1440},
	{"kiosk-portrait", 1080, 1920},
}

// Viewport is one named screen size, in CSS pixels.
type Viewport struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Manifest is vibepanel.json.
type Manifest struct {
	SDK      int      `json:"sdk"`
	Name     string   `json:"name"`
	Sections []string `json:"sections"`
	// The options below each belong to a section and are refused without it:
	// a spend range on a page that did not ask for spend is a setting that
	// does nothing, and somebody would set it and wait.
	Spend *SpendOptions `json:"spend,omitempty"`
	Repo  *RepoOptions  `json:"repo,omitempty"`
	Flow  *FlowOptions  `json:"flow,omitempty"`
	// Params are the knobs the owner sets per link without touching the code.
	// An array rather than an object because the settings form draws them in
	// the order the author wrote them, and a JSON object's order does not
	// survive a Go map.
	Params      []ParamSpec `json:"params,omitempty"`
	ScriptHosts []string    `json:"scriptHosts,omitempty"`
	Viewports   []string    `json:"viewports,omitempty"`
}

// SpendOptions shapes the spend section.
type SpendOptions struct {
	// Days is how many days of the per-day series are sent, 0 for none.
	Days int `json:"days,omitempty"`
	// Months sends the per-month series; Heatmap the year of days.
	Months  bool `json:"months,omitempty"`
	Heatmap bool `json:"heatmap,omitempty"`
	// Split sends the breakdowns: any of "tool", "project", "model".
	Split []string `json:"split,omitempty"`
}

// RepoOptions shapes the repository section.
type RepoOptions struct {
	// Days is how many days of the per-day series are sent, 0 for none.
	Days int `json:"days,omitempty"`
	// PRs sends open pull requests, which is the one read that reaches
	// github.com; internal/git/warm.go says under what conditions.
	PRs bool `json:"prs,omitempty"`
}

// FlowOptions shapes the session-flow section.
type FlowOptions struct {
	// By is "hour" (today) or "day" (the window).
	By   string `json:"by,omitempty"`
	Days int    `json:"days,omitempty"`
}

var splitDimensions = []string{"tool", "project", "model"}

// ParseManifest reads and checks vibepanel.json.
//
// Strict, as ValidateBoard is: the person is at a keyboard, or an agent is,
// and an error naming the field is something either can act on. An unknown key
// is refused rather than ignored, because a misspelt "sections" that is
// silently ignored is a page that draws nothing and never says why.
func ParseManifest(raw []byte) (Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if dec.More() {
		return Manifest{}, fmt.Errorf("%s: more than one JSON value", ManifestFile)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	return m, nil
}

// Validate checks a manifest that has already been decoded.
func (m Manifest) Validate() error {
	if m.SDK != SDKVersion {
		return fmt.Errorf("sdk must be %d, the version this panel serves", SDKVersion)
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		return errors.New("name is required")
	}
	if utf8.RuneCountInString(name) > MaxName {
		return fmt.Errorf("name is at most %d characters", MaxName)
	}
	seen := map[string]bool{}
	for _, s := range m.Sections {
		if !slices.Contains(Sections(), s) {
			return fmt.Errorf("unknown section %q; sections are %s", s, strings.Join(Sections(), ", "))
		}
		if seen[s] {
			return fmt.Errorf("section %q is listed twice", s)
		}
		seen[s] = true
	}
	if m.Spend != nil {
		if !seen[SectionSpend] {
			return errors.New(`"spend" options need "spend" in sections`)
		}
		if err := dayRange("spend.days", m.Spend.Days); err != nil {
			return err
		}
		dims := map[string]bool{}
		for _, d := range m.Spend.Split {
			if !slices.Contains(splitDimensions, d) {
				return fmt.Errorf("spend.split: %q is not one of %s", d, strings.Join(splitDimensions, ", "))
			}
			if dims[d] {
				return fmt.Errorf("spend.split: %q is listed twice", d)
			}
			dims[d] = true
		}
	}
	if m.Repo != nil {
		if !seen[SectionRepo] {
			return errors.New(`"repo" options need "repo" in sections`)
		}
		if err := dayRange("repo.days", m.Repo.Days); err != nil {
			return err
		}
	}
	if m.Flow != nil {
		if !seen[SectionFlow] {
			return errors.New(`"flow" options need "flow" in sections`)
		}
		if m.Flow.By != "" && m.Flow.By != "hour" && m.Flow.By != "day" {
			return fmt.Errorf(`flow.by must be "hour" or "day", not %q`, m.Flow.By)
		}
		if err := dayRange("flow.days", m.Flow.Days); err != nil {
			return err
		}
	}
	if err := validateParamSpecs(m.Params); err != nil {
		return err
	}
	hosts := map[string]bool{}
	for _, h := range m.ScriptHosts {
		if !slices.Contains(ScriptHosts, h) {
			return fmt.Errorf("scriptHosts: %q is not allowed; the hosts a page may load scripts "+
				"from are %s", h, strings.Join(ScriptHosts, ", "))
		}
		if hosts[h] {
			return fmt.Errorf("scriptHosts: %q is listed twice", h)
		}
		hosts[h] = true
	}
	for _, v := range m.Viewports {
		if _, ok := ViewportNamed(v); !ok {
			return fmt.Errorf("viewports: unknown screen %q", v)
		}
	}
	return m.checkBoard()
}

func dayRange(field string, n int) error {
	if n < 0 || n > store.MaxSpendDays {
		return fmt.Errorf("%s must be between 0 and %d", field, store.MaxSpendDays)
	}
	return nil
}

// ViewportNamed finds one of Viewports by name.
func ViewportNamed(name string) (Viewport, bool) {
	for _, v := range Viewports {
		if v.Name == name {
			return v, true
		}
	}
	return Viewport{}, false
}

// Board compiles the manifest into the board the snapshot builder already
// reads.
//
// This is the whole reason a page cannot ask for more than a board can: it
// asks in the board's own vocabulary. buildShareDashboard decides which
// sections to compute, and how many days of each, from a store.Board, and a
// second way of deciding it would be a second reduction of the panel's state
// -- the thing that function exists to be the only one of. The widgets here
// are never drawn by anything; each is there for what Board.Needs reads off it.
func (m Manifest) Board() store.Board {
	b := store.Board{Grid: store.GridColumns, Density: store.DefaultDensity}
	add := func(w store.Widget) { b.Widgets = append(b.Widgets, w) }
	for _, s := range m.Sections {
		switch s {
		case SectionSessions:
			add(store.Widget{Kind: "sessionlist"})
		case SectionTodos:
			add(store.Widget{Kind: "todos"})
		case SectionSpend:
			add(store.Widget{Kind: "spendtotals"})
			if o := m.Spend; o != nil {
				if o.Days > 0 {
					add(store.Widget{Kind: "spendbars", By: "day", Days: o.Days})
				}
				if o.Months {
					add(store.Widget{Kind: "spendbars", By: "month"})
				}
				if o.Heatmap {
					add(store.Widget{Kind: "spendheatmap"})
				}
				for _, d := range o.Split {
					add(store.Widget{Kind: "spendsplit", By: d})
				}
			}
		case SectionTrend:
			add(store.Widget{Kind: "machinearea"})
		case SectionFlow:
			w := store.Widget{Kind: "flow", By: "hour"}
			if o := m.Flow; o != nil {
				if o.By != "" {
					w.By = o.By
				}
				w.Days = o.Days
			}
			add(w)
		case SectionFeed:
			add(store.Widget{Kind: "feed"})
		case SectionRepo:
			add(store.Widget{Kind: "output"})
			if o := m.Repo; o != nil {
				if o.Days > 0 {
					add(store.Widget{Kind: "codechurn", Days: o.Days})
				}
				if o.PRs {
					add(store.Widget{Kind: "prs"})
				}
			}
		}
	}
	if len(b.Widgets) == 0 {
		// A page that asked for no sections still gets the counts and the
		// machine, which every snapshot carries. A board needs one widget to be
		// a board, and this one needs nothing.
		add(store.Widget{Kind: "states"})
	}
	return b
}

// checkBoard runs the compiled board through the board's own validator.
//
// It cannot fail for a manifest that passed the checks above, and that is
// what it is for: if the two sets of rules ever disagree, a manifest the panel
// accepted would compile to a board the panel refuses, and this is where that
// is caught -- at publish, with a person there -- rather than on a wall.
func (m Manifest) checkBoard() error {
	if _, err := store.ValidateBoard(m.Board()); err != nil {
		return fmt.Errorf("the sections do not compile: %w", err)
	}
	return nil
}

// Needs reports the section names the compiled board asks for, in the
// manifest's vocabulary, for the snapshot's `sections` field.
func (m Manifest) Needs() []string {
	out := []string{}
	for _, s := range Sections() {
		if slices.Contains(m.Sections, s) {
			out = append(out, s)
		}
	}
	return out
}

// Encode renders a manifest the way it is stored: compact, and without
// anything a decode would not have read.
func (m Manifest) Encode() (json.RawMessage, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return raw, nil
}

// DecodeStored reads a manifest back out of the database.
//
// Lenient in one direction only. A stored manifest was valid when it was
// published; if this build no longer accepts it -- a section retired, a host
// taken off the list -- the page is drawn with what still validates rather
// than refused, because the screen showing it has nobody at it. Sections are
// dropped rather than repaired, and so are script hosts, which is the
// direction that fails closed.
func DecodeStored(raw []byte) Manifest {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{SDK: SDKVersion, Name: "page"}
	}
	if err := m.Validate(); err == nil {
		return m
	}
	clean := Manifest{SDK: SDKVersion, Name: m.Name, Viewports: nil}
	if strings.TrimSpace(clean.Name) == "" || utf8.RuneCountInString(clean.Name) > MaxName {
		clean.Name = "page"
	}
	for _, s := range m.Sections {
		if slices.Contains(Sections(), s) && !slices.Contains(clean.Sections, s) {
			clean.Sections = append(clean.Sections, s)
		}
	}
	for _, h := range m.ScriptHosts {
		if slices.Contains(ScriptHosts, h) && !slices.Contains(clean.ScriptHosts, h) {
			clean.ScriptHosts = append(clean.ScriptHosts, h)
		}
	}
	if validateParamSpecs(m.Params) == nil {
		clean.Params = m.Params
	}
	if clean.Validate() != nil {
		return Manifest{SDK: SDKVersion, Name: clean.Name}
	}
	return clean
}
