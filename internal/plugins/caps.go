package plugins

import "strings"

// The capability table. docs/plugins.md §4.
//
// This file is the whole permission model. A capability has a name, a sentence
// in both languages for the install screen, a kind that decides where on the
// screen it is drawn, and -- once the runtimes exist -- the routes it opens
// under /api/plugin/{cred}/v1/ and the ctx members a server.js gets. Nothing
// else in the panel decides what a plugin may do: a handler under the plugin
// prefix answers because this table says so, and TestEveryCapabilityOpensOnlyItsRoutes
// mints a grant with each capability alone and expects 403 everywhere else.
//
// What is not here and never will be, as capability names: the tmux socket,
// a session's PTY, the database, the hook endpoint, any share or admin route,
// another plugin's data, the account routes, and installing plugins. The last
// one matters most: a plugin that can install plugins can grant itself the
// rest.

// Capability kinds decide where on the install screen a line is drawn, and in
// what tone. KindDanger is the two rungs writable-links.md refuses at any
// setting for a URL: allowed here because the credential is not a URL, and
// drawn in the danger tone, above the rest, with the button's words changed.
const (
	KindRead   = "read"
	KindWrite  = "write"
	KindDanger = "danger"
	KindUI     = "ui"
	KindNet    = "net"
)

// Capability names.
const (
	CapReadPanel       = "read:panel"
	CapReadPaths       = "read:paths"
	CapReadTerminal    = "read:terminal"
	CapReadNotes       = "read:notes"
	CapReadTodos       = "read:todos"
	CapWriteNotes      = "write:notes"
	CapWriteTodos      = "write:todos"
	CapWriteState      = "write:state"
	CapSessionsControl = "sessions:control"
	CapSessionsCreate  = "sessions:create"
	CapSessionsInput   = "sessions:input"
	CapReadResources   = "read:resources"
	CapReadUsage       = "read:usage"
	CapReadGit         = "read:git"
	CapUIOpen          = "ui:open"
	CapUINotify        = "ui:notify"
	// CapNet is a prefix: "net:<host>" is one capability per host.
	CapNet = "net:"
)

// Capability is one row of the table.
type Capability struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Words is the sentence the install screen draws, in both languages.
	// Written as what the plugin *can* do, because that is what the person
	// is deciding.
	Words Text `json:"words"`
}

// capabilities is the table, in the order the screen draws them within a
// kind. The order is the ladder from writable-links.md: reads, then the writes
// that touch a row, then the two that touch a process.
var capabilities = []Capability{
	{CapReadPanel, KindRead, Text{EN: "can see your projects, sessions, their names and states", ZH: "能看到你的项目、会话、它们的名字和状态"}},
	{CapReadPaths, KindRead, Text{EN: "can see directories, commands and working directories", ZH: "能看到目录、命令行和工作目录"}},
	{CapReadTerminal, KindRead, Text{EN: "can read what is on any terminal's screen", ZH: "能读任何终端屏幕上的内容"}},
	{CapReadNotes, KindRead, Text{EN: "can read notes", ZH: "能读笔记"}},
	{CapReadTodos, KindRead, Text{EN: "can read checklists", ZH: "能读清单"}},
	{CapReadResources, KindRead, Text{EN: "can read the machine's memory and CPU readings", ZH: "能读机器的内存和 CPU 读数"}},
	{CapReadUsage, KindRead, Text{EN: "can read token usage", ZH: "能读 token 用量"}},
	{CapReadGit, KindRead, Text{EN: "can read repository summaries", ZH: "能读仓库摘要"}},
	{CapWriteNotes, KindWrite, Text{EN: "can change notes", ZH: "能修改笔记"}},
	{CapWriteTodos, KindWrite, Text{EN: "can change checklists", ZH: "能修改清单"}},
	{CapWriteState, KindWrite, Text{EN: "can mark a session working, waiting or done", ZH: "能把会话标成 working、waiting 或 done"}},
	{CapSessionsControl, KindWrite, Text{EN: "can restart and end sessions", ZH: "能重启和结束会话"}},
	{CapSessionsCreate, KindDanger, Text{EN: "can start programs in your projects", ZH: "能在你的项目里启动程序"}},
	{CapSessionsInput, KindDanger, Text{EN: "can type into any of your terminals", ZH: "能往你的任何终端里输入"}},
	{CapUIOpen, KindUI, Text{EN: "can ask the panel to open a session, project or settings page", ZH: "能请面板打开某个会话、项目或设置页"}},
	{CapUINotify, KindUI, Text{EN: "can show a toast or a notification", ZH: "能显示 toast 或通知"}},
}

// Capabilities lists the fixed table. net:<host> is not in it: it is one
// capability per host, made by NetCapability.
func Capabilities() []Capability {
	return append([]Capability(nil), capabilities...)
}

// Lookup finds a capability by name, net:<host> included.
func Lookup(name string) (Capability, bool) {
	for _, c := range capabilities {
		if c.Name == name {
			return c, true
		}
	}
	if host, ok := strings.CutPrefix(name, CapNet); ok && hostPattern.MatchString(host) {
		return NetCapability(host), true
	}
	return Capability{}, false
}

// KnownCapability reports whether name is a capability.
func KnownCapability(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// NetCapability is the capability to contact one host over https.
func NetCapability(host string) Capability {
	return Capability{Name: CapNet + host, Kind: KindNet,
		Words: Text{EN: "will contact " + host + " over https", ZH: "会通过 https 访问 " + host}}
}

// IsDanger reports whether a capability is one of the two drawn in red.
func IsDanger(name string) bool {
	c, ok := Lookup(name)
	return ok && c.Kind == KindDanger
}
