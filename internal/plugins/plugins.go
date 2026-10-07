// Package plugins is what a plugin may be: its manifest, the capability table,
// the install screen, the rules a theme file has to pass, and the files a
// plugin is made of.
//
// Nothing here serves anything or touches the database. internal/httpapi
// installs and runs a plugin and internal/store keeps one; this package is the
// set of rules both of them, and the CLI, check against. One copy of the rules
// is the point: a plugin that `vibepanel plugin check` accepted and the install
// screen then refused would be a check that checked nothing.
//
// docs/plugins.md is the design. The parts that matter most for anybody
// changing this package:
//
//   - caps.go is the whole permission model. A capability has a name, a
//     sentence in both languages, and the routes and ctx members it opens.
//     Nothing else in the panel decides what a plugin may do.
//   - describe.go is the install screen, a pure function over the manifest.
//     Every capability and every rung has words there, and a test fails the
//     build when one does not.
//   - The manifest is strict on the way in and lenient on the way out
//     (DecodeStored), the same shape as a share page's.
package plugins

import "regexp"

// ManifestVersion is the manifest contract this build reads. A manifest names
// it and the panel refuses one it does not serve. v1 is additive only.
const ManifestVersion = 1

// ManifestFile is the manifest's name, at the root of a plugin.
const ManifestFile = "plugin.json"

// SDKFile is the plugin SDK's name inside a panel's files. Served from the
// binary, never from the plugin, so a plugin always runs the SDK that matches
// the panel serving it.
const SDKFile = "vibepanel-plugin.js"

// TypesFile is the SDK's TypeScript declarations, for editors. Never served.
const TypesFile = "vibepanel-plugin.d.ts"

// UIFile is the stylesheet a frame loads to look like the panel: the tokens
// and a short list of classes with stable names. Served from the binary.
const UIFile = "vibepanel-ui.css"

// Limits on a bundle. A plugin is larger than a page -- a process rung ships
// its own code -- and still not a site: twenty megabytes is a small Node
// program with its dependencies, and anything larger is a thing that should
// be installed with a package manager rather than stored in SQLite.
const (
	MaxFiles     = 256
	MaxFileBytes = 5 << 20
	MaxBytes     = 20 << 20
	MaxThemeByte = 64 << 10
	MaxName      = 64
	MaxTextLine  = 200
	MaxPanels    = 8
	MaxSettings  = 32
)

// The rungs of the ladder, by the word the install screen uses. docs/plugins.md §1.
const (
	RungTheme       = "theme"
	RungPanel       = "panel"
	RungService     = "service"
	RungProcess     = "process"
	RungUnsandboxed = "unsandboxed"
)

// Rungs is which rungs a manifest climbs. One plugin may be several of 0-3;
// the fourth is a kind of its own and the screen says so.
type Rungs struct {
	Theme       bool `json:"theme"`
	Panel       bool `json:"panel"`
	Service     bool `json:"service"`
	Process     bool `json:"process"`
	Unsandboxed bool `json:"unsandboxed"`
}

// Slots are the places the panel will draw a plugin's frame. A closed list
// that only grows: a slot removed is a plugin that drew something and now
// draws nothing, with nothing on screen saying so. docs/plugins.md §5.
const (
	SlotSidePanel = "sidepanel.pane"
	SlotSettings  = "settings.section"
	SlotSession   = "session.action"
	SlotProject   = "project.action"
	SlotPage      = "page"
	SlotHeader    = "header.item"
)

// Slots lists them in the order the documentation does.
func Slots() []string {
	return []string{SlotSidePanel, SlotSettings, SlotSession, SlotProject, SlotPage, SlotHeader}
}

// SettingsGroups are the settings rail items a declarative settings schema
// may ask to be drawn under. The same five as web/src/components/settings/groups.ts.
var SettingsGroups = []string{"sessions", "notify", "account", "resources", "panel"}

// idPattern is a plugin's id: lower-case, digits and dashes, 3-40 characters,
// starting with a letter. It becomes a URL path segment, a CSS attribute
// value (`ext-<id>`), a cgroup leaf name and a table key, and every character
// it refuses has a second meaning in one of those.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,39}$`)

// ValidID reports whether id may name a plugin.
func ValidID(id string) bool { return idPattern.MatchString(id) }
