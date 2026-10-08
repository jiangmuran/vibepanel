package plugins

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// The install screen. docs/plugins.md §6.
//
// A pure function over the manifest: what it says is decided here and
// nowhere else, so the settings page, the CLI and the tests all read the same
// words. Every capability and every rung produces a line in both languages,
// and TestEveryCapabilityHasWordsOnTheScreen fails the build when one does
// not -- a capability added without words is a permission nobody is told
// about.

// Line kinds, in the order the screen draws them.
const (
	LineWhat   = "what"   // name, version, author, hash, panel version
	LineRung   = "rung"   // which rungs
	LineCap    = "cap"    // one capability, with a checkbox
	LineHost   = "host"   // one host, enforced or declared
	LineRuns   = "runs"   // the argv
	LineKeeps  = "keeps"  // data keys, settings, secrets
	LineDanger = "danger" // the rung-4 paragraph
	LineHTTP   = "http"   // a process on the panel's port, and who may call it
)

// LineKinds is every kind a screen may carry, in the order the page draws
// them. web/src/components/plugins/screen.ts has the same list as HEADINGS,
// and a kind missing there is a line the screen silently does not draw --
// which for a permission is the failure red line 10 exists for.
func LineKinds() []string {
	return []string{LineWhat, LineRung, LineCap, LineHost, LineRuns, LineHTTP, LineKeeps, LineDanger}
}

// Tones. The screen draws each differently and the word changes with the
// tone (red line 4): "enforced" and "declared" are words on the line, not
// only colours.
const (
	TonePlain    = "plain"
	ToneAmber    = "amber"
	ToneRed      = "red"
	ToneEnforced = "enforced"
)

// Line is one line of the screen.
type Line struct {
	Kind string `json:"kind"`
	Tone string `json:"tone"`
	Text Text   `json:"text"`
	// Code is the thing the line is about, verbatim: a capability name, a
	// host, the argv. Drawn in monospace beside the words.
	Code string `json:"code,omitempty"`
	// Granted is whether a capability line's box is ticked: what the owner
	// granted, or everything on a first install.
	Granted bool `json:"granted,omitempty"`
	// Checkable marks a capability line: the owner may untick it.
	Checkable bool `json:"checkable,omitempty"`
}

// Confirm words. The verb is what a person reads at the moment of deciding.
const (
	ConfirmInstall = "install" // a theme: "Install"
	ConfirmGrant   = "grant"   // rungs 1-2: "Install and grant"
	ConfirmRun     = "run"     // rungs 3-4: "Run this as you"
)

// Screen is what the install screen draws.
type Screen struct {
	Lines []Line `json:"lines"`
	// Confirm is which words the button carries.
	Confirm string `json:"confirm"`
	// Refused is set when the panel is older than the manifest asks for; the
	// button is then disabled and this line says why.
	Refused *Line `json:"refused,omitempty"`
}

// Describe builds the screen for a manifest.
//
// granted is the capability set already granted to this plugin id (nil on a
// first install, which ticks everything); panelVersion is the running panel's
// version string, "dev" for a local build.
func Describe(m Manifest, granted []string, panelVersion string) Screen {
	var s Screen
	add := func(l Line) { s.Lines = append(s.Lines, l) }

	// 1. What it is.
	what := m.Name.EN + " " + m.Version
	add(Line{Kind: LineWhat, Tone: TonePlain, Code: m.ID, Text: Text{
		EN: what + byAuthor(m.Author, "en"), ZH: m.Name.In("zh") + " " + m.Version + byAuthor(m.Author, "zh")}})
	if m.Panel != "" {
		c, _ := ParseConstraint(m.Panel)
		if !c.Satisfied(panelVersion) {
			s.Refused = &Line{Kind: LineWhat, Tone: ToneRed, Code: m.Panel, Text: Text{
				EN: "needs panel " + m.Panel + "; this panel is " + panelVersion + ": refused",
				ZH: "需要面板 " + m.Panel + "，当前 " + panelVersion + "：拒绝安装"}}
		}
	}

	// 2. Which rungs.
	r := m.Rungs()
	if r.Theme {
		add(Line{Kind: LineRung, Tone: TonePlain, Code: RungTheme, Text: Text{EN: "A theme: " + m.Theme.Name.EN + ".", ZH: "一个主题：" + m.Theme.Name.In("zh") + "。"}})
	}
	if r.Panel {
		add(Line{Kind: LineRung, Tone: TonePlain, Code: RungPanel, Text: panelsLine(m.Panels)})
	}
	if r.Service {
		add(Line{Kind: LineRung, Tone: TonePlain, Code: RungService, Text: serviceLine(*m.Server)})
	}
	if r.Process {
		add(Line{Kind: LineRung, Tone: ToneAmber, Code: RungProcess, Text: Text{EN: "Runs a command as you.", ZH: "以你的身份运行一条命令。"}})
	}
	if r.Unsandboxed {
		add(Line{Kind: LineRung, Tone: ToneRed, Code: RungUnsandboxed, Text: Text{EN: "Runs on the panel's page as you.", ZH: "以你的身份运行在面板的页面上。"}})
	}

	// 3. What it may do: the danger lines first, then reads, writes, ui.
	// net:<host> capabilities are drawn under 4 instead.
	caps := m.AllCapabilities()
	byKind := map[string][]Capability{}
	for _, name := range caps {
		c, _ := Lookup(name)
		if c.Kind == KindNet {
			continue
		}
		byKind[c.Kind] = append(byKind[c.Kind], c)
	}
	for _, kind := range []string{KindDanger, KindRead, KindWrite, KindUI} {
		for _, c := range byKind[kind] {
			tone := TonePlain
			if kind == KindDanger {
				tone = ToneRed
			}
			add(Line{Kind: LineCap, Tone: tone, Code: c.Name, Text: c.Words, Checkable: true,
				Granted: granted == nil || contains(granted, c.Name)})
		}
	}

	// 4. Where it reaches. Sources are enforced; what a process or a module
	// says it will contact is a promise, and the line says so.
	seenHost := map[string]bool{}
	for _, src := range m.Sources {
		if u, err := url.Parse(src.URL); err == nil && u.Host != "" && !seenHost[u.Host] {
			seenHost[u.Host] = true
			c := NetCapability(u.Host)
			add(Line{Kind: LineHost, Tone: ToneEnforced, Code: u.Host, Checkable: true,
				Granted: granted == nil || contains(granted, c.Name),
				Text:    Text{EN: c.Words.EN + " (enforced)", ZH: c.Words.ZH + "（强制）"}})
		}
	}
	declared := func(hosts []string) {
		for _, h := range hosts {
			if seenHost["declared:"+h] {
				continue
			}
			seenHost["declared:"+h] = true
			add(Line{Kind: LineHost, Tone: ToneAmber, Code: h, Text: Text{
				EN: "declares that it will contact " + h + "; the panel cannot check this",
				ZH: "声明会访问 " + h + "；面板无法核实"}})
		}
	}
	if m.Process != nil {
		declared(m.Process.Hosts)
	}
	for _, d := range m.Downloads {
		if h := d.Host(); h != "" && !seenHost["download:"+h] {
			seenHost["download:"+h] = true
			add(Line{Kind: LineHost, Tone: ToneAmber, Code: h, Text: Text{
				EN: "downloads from " + h + " (the panel fetches it; redirects followed, every address checked)",
				ZH: "从 " + h + " 下载（由面板抓取；跟随跳转，每一跳都检查地址）"}})
		}
	}
	if m.Unsandboxed != nil {
		declared(m.Unsandboxed.Hosts)
	}
	if m.Inbound != nil {
		add(Line{Kind: LineHost, Tone: ToneAmber, Code: "/api/plugin-hook/" + m.ID + "/" + m.Inbound.Path, Text: Text{
			EN: "accepts requests from the internet at /api/plugin-hook/" + m.ID + "/" + m.Inbound.Path + ", checked against the secret " + m.Inbound.Secret,
			ZH: "在 /api/plugin-hook/" + m.ID + "/" + m.Inbound.Path + " 接受来自互联网的请求，用 secret " + m.Inbound.Secret + " 校验"}})
	}

	// 5. What it runs.
	if m.Process != nil {
		add(Line{Kind: LineRuns, Tone: ToneAmber, Code: strings.Join(m.Process.Command, " "), Text: Text{
			EN: "This command, as you, from the plugin's own directory:", ZH: "这条命令，以你的身份，在插件自己的目录里："}})
	}
	if m.Process != nil && m.Process.HTTP != nil {
		add(Line{Kind: LineHTTP, Tone: ToneRed, Code: m.Process.HTTP.Auth, Text: HTTPLine(m.ID, m.Process.HTTP.Auth)})
	}
	for _, d := range m.Downloads {
		add(Line{Kind: LineKeeps, Tone: ToneAmber, Code: "download:" + d.Name, Text: DownloadLine(d)})
	}

	// 6. What it keeps.
	if n := len(m.Data); n > 0 {
		add(Line{Kind: LineKeeps, Tone: TonePlain, Code: joinKeys(m.Data), Text: Text{
			EN: "keeps data of its own, under these keys", ZH: "保存自己的数据，键如下"}})
	}
	if m.Settings != nil {
		var keys []string
		for _, f := range m.Settings.Fields {
			keys = append(keys, f.Key)
		}
		add(Line{Kind: LineKeeps, Tone: TonePlain, Code: strings.Join(keys, ", "), Text: Text{
			EN: "has settings the panel draws", ZH: "有一组由面板绘制的设置"}})
	}
	if secrets := m.SecretNames(); len(secrets) > 0 {
		add(Line{Kind: LineKeeps, Tone: ToneAmber, Code: strings.Join(secrets, ", "), Text: Text{
			EN: "needs these secrets, to be filled before enabling", ZH: "需要这些 secret，启用前必须填写"}})
	}

	// 7. Rung 4.
	if r.Unsandboxed {
		add(Line{Kind: LineDanger, Tone: ToneRed, Code: m.Unsandboxed.Tested, Text: DangerParagraph})
	}

	switch {
	case r.Process || r.Unsandboxed:
		s.Confirm = ConfirmRun
	case r.Panel || r.Service:
		s.Confirm = ConfirmGrant
	default:
		s.Confirm = ConfirmInstall
	}
	return s
}

// DangerParagraph is the rung-4 paragraph, in the words of docs/plugins.md §7.
var DangerParagraph = Text{
	EN: "This plugin runs on the panel's own page as you. It can read every terminal, type into any of them, " +
		"change your password, revoke your passkeys and install other plugins. The panel cannot limit it. " +
		"Install it only if you trust its author as you trust yourself.",
	ZH: "这个插件以你的身份运行在面板自己的页面上。它能读每一个终端、往任何一个里输入、改你的密码、" +
		"吊销你的 passkey、安装别的插件。面板无法限制它。只有当你像信任自己一样信任它的作者时才安装。",
}

// SecretNames is every secret the plugin needs: a process's env, an inbound
// route's secret, a secret-typed setting, and ${secret:NAME} in a source.
func (m Manifest) SecretNames() []string {
	seen := map[string]bool{}
	var out []string
	put := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if m.Process != nil {
		for _, e := range m.Process.Env {
			put(e)
		}
		if m.Process.HTTP != nil {
			put(m.Process.HTTP.Secret)
		}
	}
	if m.Inbound != nil {
		put(m.Inbound.Secret)
	}
	if m.Settings != nil {
		for _, f := range m.Settings.Fields {
			if f.Type == FieldSecret {
				put(strings.ToUpper(f.Key))
			}
		}
	}
	for _, s := range m.Sources {
		for _, n := range secretRefs(s.URL) {
			put(n)
		}
		for _, v := range s.Headers {
			for _, n := range secretRefs(v) {
				put(n)
			}
		}
	}
	return out
}

func secretRefs(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "${secret:")
		if i < 0 {
			return out
		}
		s = s[i+len("${secret:"):]
		j := strings.Index(s, "}")
		if j < 0 {
			return out
		}
		out = append(out, s[:j])
		s = s[j+1:]
	}
}

func byAuthor(author, lang string) string {
	if author == "" {
		return ""
	}
	if lang == "zh" {
		return " · 作者 " + author
	}
	return " · by " + author
}

func panelsLine(panels []PanelSpec) Text {
	where := map[string]Text{
		SlotSidePanel: {EN: "a pane in the side panel", ZH: "侧栏一个 pane"},
		SlotSettings:  {EN: "a settings section", ZH: "设置里一段"},
		SlotSession:   {EN: "an action on every session", ZH: "每个会话上一个操作"},
		SlotProject:   {EN: "an action on every project", ZH: "每个项目上一个操作"},
		SlotPage:      {EN: "a page of its own", ZH: "一个独立页面"},
		SlotHeader:    {EN: "an item in the header", ZH: "顶栏一项"},
	}
	var en, zh []string
	seen := map[string]bool{}
	for _, p := range panels {
		if seen[p.Slot] {
			continue
		}
		seen[p.Slot] = true
		en = append(en, where[p.Slot].EN)
		zh = append(zh, where[p.Slot].ZH)
	}
	return Text{EN: "Adds " + strings.Join(en, ", ") + ".", ZH: "加上" + strings.Join(zh, "、") + "。"}
}

func serviceLine(s ServerSpec) Text {
	var en, zh []string
	if s.Every != "" {
		en = append(en, "every "+s.Every)
		zh = append(zh, "每 "+s.Every)
	}
	if len(s.On) > 0 {
		en = append(en, "on "+strings.Join(s.On, ", "))
		zh = append(zh, "在 "+strings.Join(s.On, "、")+" 事件时")
	}
	if len(s.Routes) > 0 {
		en = append(en, "when its routes are called")
		zh = append(zh, "它的路由被调用时")
	}
	if len(en) == 0 {
		return Text{EN: "Runs code inside the panel.", ZH: "在面板内运行代码。"}
	}
	return Text{EN: "Runs code inside the panel " + strings.Join(en, ", ") + ".",
		ZH: "在面板内运行代码：" + strings.Join(zh, "，") + "。"}
}

func joinKeys[T any](m map[string]T) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return strings.Join(keys, ", ")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// HTTPLine is the install screen's sentence for a process on the panel's
// port: where it answers, and who may call it. Red, because it is a door on
// the panel's own origin, even though every call still passes the panel's
// own checks.
func HTTPLine(id, auth string) Text {
	where := "/api/plugin-http/" + id + "/"
	switch auth {
	case "token":
		return Text{EN: "Accepts requests on the panel's port at " + where + ", with a plugin token you mint.",
			ZH: "在面板端口 " + where + " 上接受请求，凭你签发的插件令牌访问。"}
	case "hmac":
		return Text{EN: "Accepts requests on the panel's port at " + where + ", verified against its declared secret.",
			ZH: "在面板端口 " + where + " 上接受请求，按它声明的 secret 校验。"}
	}
	return Text{EN: "Accepts requests on the panel's port at " + where + ", with your sign-in.",
		ZH: "在面板端口 " + where + " 上接受请求，凭你的登录态访问。"}
}

// DownloadLine is one declared download on the screen: what, how big, from
// where, and that the hash on the screen is what the panel will accept.
func DownloadLine(d DownloadSpec) Text {
	size := HumanBytes(d.Size)
	short := strings.ToLower(d.SHA256)[:12]
	if d.Optional {
		return Text{
			EN: "May download " + d.Label.EN + " (" + size + ") from " + d.Host() + " when you choose to, verified against sha256 " + short + "….",
			ZH: "可按你的选择下载 " + d.Label.In("zh") + "（" + size + "），来自 " + d.Host() + "，校验 sha256 " + short + "…。"}
	}
	return Text{
		EN: "Needs " + d.Label.EN + " (" + size + ") from " + d.Host() + ", downloaded when enabled, verified against sha256 " + short + "….",
		ZH: "需要 " + d.Label.In("zh") + "（" + size + "），启用时从 " + d.Host() + " 下载，校验 sha256 " + short + "…。"}
}

// HumanBytes is "239 MB" for a screen.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
