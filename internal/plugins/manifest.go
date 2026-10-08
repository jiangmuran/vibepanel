package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jiangmuran/vibepanel/internal/pages"
)

// Text is a string in both languages the panel speaks.
//
// Every string a plugin hands the panel to draw -- its name, a panel's title,
// a setting's label -- is one of these, because the install screen and the
// slot chrome are drawn in the owner's language, and a plugin with only
// English is an English line in a Chinese settings page. ZH may be empty; EN
// may not.
type Text struct {
	EN string `json:"en"`
	ZH string `json:"zh-CN,omitempty"`
}

// In picks the string for a language ("zh" or anything else), falling back to
// English.
func (t Text) In(lang string) string {
	if lang == "zh" && t.ZH != "" {
		return t.ZH
	}
	return t.EN
}

func (t Text) empty() bool { return strings.TrimSpace(t.EN) == "" }

// Manifest is plugin.json. docs/plugins.md §3.
type Manifest struct {
	Plugin      int    `json:"plugin"`
	ID          string `json:"id"`
	Name        Text   `json:"name"`
	Version     string `json:"version"`
	Panel       string `json:"panel,omitempty"`
	Description *Text  `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
	Homepage    string `json:"homepage,omitempty"`

	Theme  *ThemeSpec  `json:"theme,omitempty"`
	Panels []PanelSpec `json:"panels,omitempty"`
	// Capabilities is what the plugin's frames and server.js ask for, by the
	// names in caps.go. A process rung declares its own list.
	Capabilities []string `json:"capabilities,omitempty"`

	Settings *SettingsSpec `json:"settings,omitempty"`
	// Data, Sources and Server use the share page vocabulary: the same
	// types, the same bounds, the same code that checks them.
	Data    map[string]*pages.DataSpec `json:"data,omitempty"`
	Sources []pages.SourceSpec         `json:"sources,omitempty"`
	Server  *ServerSpec                `json:"server,omitempty"`
	Inbound *InboundSpec               `json:"inbound,omitempty"`

	Process     *ProcessSpec     `json:"process,omitempty"`
	Unsandboxed *UnsandboxedSpec `json:"unsandboxed,omitempty"`
}

// ThemeSpec is rung 0: a stylesheet of tokens.
type ThemeSpec struct {
	File string `json:"file"`
	Name Text   `json:"name"`
	// Scheme is which of the panel's two schemes the theme is closer to, so
	// the pre-paint script and form controls can follow it before the
	// stylesheet arrives.
	Scheme string `json:"scheme,omitempty"`
}

// PanelSpec is rung 1: one entry at one slot.
type PanelSpec struct {
	Slot  string `json:"slot"`
	Entry string `json:"entry"`
	Title Text   `json:"title"`
	// Icon is a lucide icon name; the panel draws the ones it knows and a
	// generic one otherwise.
	Icon string `json:"icon,omitempty"`
	// Group is the settings rail item a settings.section goes under.
	Group string `json:"group,omitempty"`
	// Path is the address of a page slot: /x/<path>.
	Path string `json:"path,omitempty"`
}

// SettingsSpec is a schema the panel draws as a form. docs/plugins.md §5a.
type SettingsSpec struct {
	Group  string      `json:"group,omitempty"`
	Fields []FieldSpec `json:"fields"`
}

// Setting field types. `secret` is stored sealed and never read back by a
// frame; the rest are the share page's scalar types.
const (
	FieldText   = "text"
	FieldNumber = "number"
	FieldBool   = "bool"
	FieldEnum   = "enum"
	FieldColor  = "color"
	FieldList   = "list"
	FieldSecret = "secret"
)

var fieldTypes = []string{FieldText, FieldNumber, FieldBool, FieldEnum, FieldColor, FieldList, FieldSecret}

// FieldSpec is one setting. An array in the manifest rather than an object,
// for the reason a page's params are: the form draws them in the author's
// order, and a JSON object's order does not survive a Go map.
type FieldSpec struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Label   Text     `json:"label"`
	Hint    *Text    `json:"hint,omitempty"`
	Default any      `json:"default,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Values  []string `json:"values,omitempty"`
}

// ServerSpec is rung 2.
type ServerSpec struct {
	Entry string `json:"entry"`
	// Every is how often onSchedule runs; "" never. At least a minute.
	Every string `json:"every,omitempty"`
	// On lists the events onEvent is called for.
	On []string `json:"on,omitempty"`
	// Routes maps "METHOD /path" to the handler function's name.
	Routes map[string]string `json:"routes,omitempty"`
}

// Events a service may subscribe to. docs/plugins.md §5, rung 2.
var Events = []string{"session.state", "session.created", "session.gone",
	"project.archived", "todo.changed", "note.changed"}

// InboundSpec is a route the internet may call, verified against a secret.
type InboundSpec struct {
	Path   string `json:"path"`
	Secret string `json:"secret"`
}

// ProcessSpec is rung 3.
type ProcessSpec struct {
	Command []string `json:"command"`
	// Restart is "on-failure" (the default), "always" or "never".
	Restart      string   `json:"restart,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	// Env names the secrets the process is given as environment variables.
	Env []string `json:"env,omitempty"`
	// Hosts is what the process says it will contact. The panel cannot check
	// it, and the install screen says so.
	Hosts []string `json:"hosts,omitempty"`
	// HTTP mounts the process on the panel's port: docs/plugins.md §5,
	// "a process on the panel's port".
	HTTP *ProcessHTTPSpec `json:"http,omitempty"`
}

// ProcessHTTPSpec is a process served through the panel at
// /api/plugin-http/{id}/. The process listens on a unix socket the panel
// names; the panel does the authentication, the cross-origin rules, the
// rate limits and the response hygiene, and the process sees none of the
// owner's credentials.
type ProcessHTTPSpec struct {
	// Auth is who may call: "owner" (the panel's session or API token),
	// "token" (a plugin token the owner mints on the card) or "hmac" (a
	// shared secret, as the inbound door: bearer or X-Signature-256).
	Auth string `json:"auth"`
	// Secret names the secret "hmac" verifies against.
	Secret string `json:"secret,omitempty"`
	// Stream allows responses that stay open: SSE, long polls. Off, a
	// response is bounded by the request budget.
	Stream bool `json:"stream,omitempty"`
	// MaxBody is the largest request body, "8m", "512k"; the panel caps it
	// at MaxProcessHTTPBody whatever is asked.
	MaxBody string `json:"maxBody,omitempty"`
	// IdleTimeout ends a streaming response that has sent nothing for this
	// long, "15m"; at most an hour.
	IdleTimeout string `json:"idleTimeout,omitempty"`
}

// MaxProcessHTTPBody is the hard cap on a proxied request body.
const MaxProcessHTTPBody = 16 << 20

// ProcessHTTPAuthModes is the closed list.
var ProcessHTTPAuthModes = []string{"owner", "token", "hmac"}

func (h ProcessHTTPSpec) validate() error {
	if !slices.Contains(ProcessHTTPAuthModes, h.Auth) {
		return errors.New(`process.http.auth is "owner", "token" or "hmac"; there is no anonymous mode`)
	}
	if h.Auth == "hmac" {
		if !secretName.MatchString(h.Secret) {
			return errors.New("process.http.secret names the secret (UPPER_CASE) that hmac verifies against")
		}
	} else if h.Secret != "" {
		return errors.New("process.http.secret is only for auth \"hmac\"")
	}
	if h.MaxBody != "" {
		n, err := ParseByteSize(h.MaxBody)
		if err != nil || n <= 0 {
			return errors.New(`process.http.maxBody is a size like "8m" or "512k"`)
		}
	}
	if h.IdleTimeout != "" {
		d, err := time.ParseDuration(h.IdleTimeout)
		if err != nil || d < time.Second || d > time.Hour {
			return errors.New(`process.http.idleTimeout is a duration from 1s to 1h, like "15m"`)
		}
	}
	return nil
}

// MaxBodyBytes is the request body cap this spec asks for, under the hard cap.
func (h ProcessHTTPSpec) MaxBodyBytes() int64 {
	n, err := ParseByteSize(h.MaxBody)
	if err != nil || n <= 0 || n > MaxProcessHTTPBody {
		return MaxProcessHTTPBody
	}
	return n
}

// IdleTimeoutOrDefault is the stream idle timeout, fifteen minutes unless asked.
func (h ProcessHTTPSpec) IdleTimeoutOrDefault() time.Duration {
	d, err := time.ParseDuration(h.IdleTimeout)
	if err != nil || d < time.Second || d > time.Hour {
		return 15 * time.Minute
	}
	return d
}

// ParseByteSize reads "8m", "512k", "1g" or a plain byte count.
func ParseByteSize(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, errors.New("empty")
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k':
		mult, s = 1<<10, s[:len(s)-1]
	case 'm':
		mult, s = 1<<20, s[:len(s)-1]
	case 'g':
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(s, "b"), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

// UnsandboxedSpec is rung 4.
type UnsandboxedSpec struct {
	Entry string `json:"entry"`
	// Tested is the panel version range the module was tested on, for the
	// card to turn amber outside of. "1.26.0 - 1.26.x" or "1.26.x".
	Tested string   `json:"tested"`
	Hosts  []string `json:"hosts,omitempty"`
}

var (
	semverPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$`)
	keyPattern    = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]{0,39}$`)
	routePattern  = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) /[a-z0-9][a-z0-9/_-]{0,79}$`)
	handlerName   = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]{0,63}$`)
	secretName    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	hostPattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(:\d{1,5})?$`)
	pagePath      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	iconPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	durationSpec  = regexp.MustCompile(`^(\d{1,4})(m|h)$`)
)

// ParseManifest reads and checks plugin.json.
//
// Strict: unknown keys are refused rather than ignored, because a misspelt
// "capabilities" that is silently ignored is a plugin that asks for nothing
// and then fails on its first call with nothing on the install screen having
// said so.
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
	if m.Plugin != ManifestVersion {
		return fmt.Errorf("plugin must be %d, the manifest version this panel reads", ManifestVersion)
	}
	if !ValidID(m.ID) {
		return errors.New("id is 3-40 lower-case letters, digits and dashes, starting with a letter")
	}
	if err := checkText("name", m.Name, MaxName); err != nil {
		return err
	}
	if !semverPattern.MatchString(m.Version) {
		return fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", m.Version)
	}
	if m.Panel != "" {
		if _, err := ParseConstraint(m.Panel); err != nil {
			return fmt.Errorf("panel: %w", err)
		}
	}
	if m.Description != nil {
		if err := checkText("description", *m.Description, MaxTextLine); err != nil {
			return err
		}
	}
	if utf8.RuneCountInString(m.Author) > MaxName {
		return fmt.Errorf("author is at most %d characters", MaxName)
	}
	if m.Homepage != "" {
		u, err := url.Parse(m.Homepage)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("homepage must be an https URL")
		}
	}
	if m.Theme != nil {
		if err := m.Theme.validate(); err != nil {
			return err
		}
	}
	if len(m.Panels) > MaxPanels {
		return fmt.Errorf("at most %d panels", MaxPanels)
	}
	seenPath := map[string]bool{}
	for i, p := range m.Panels {
		if err := p.validate(); err != nil {
			return fmt.Errorf("panels[%d]: %w", i, err)
		}
		if p.Slot == SlotPage {
			if seenPath[p.Path] {
				return fmt.Errorf("panels[%d]: path %q is used twice", i, p.Path)
			}
			seenPath[p.Path] = true
		}
	}
	if err := checkCapabilities("capabilities", m.Capabilities); err != nil {
		return err
	}
	if m.Settings != nil {
		if err := m.Settings.validate(); err != nil {
			return err
		}
	}
	if err := m.validateBackend(); err != nil {
		return err
	}
	if m.Server != nil {
		if err := m.Server.validate(); err != nil {
			return err
		}
	}
	if m.Inbound != nil {
		if m.Server == nil {
			return errors.New(`"inbound" needs "server": the request is handled by server.js`)
		}
		if !pagePath.MatchString(m.Inbound.Path) {
			return errors.New("inbound.path is lower-case letters, digits and dashes")
		}
		if !secretName.MatchString(m.Inbound.Secret) {
			return errors.New("inbound.secret is a secret's name: UPPER_CASE")
		}
	}
	if m.Process != nil {
		if err := m.Process.validate(); err != nil {
			return err
		}
	}
	if m.Unsandboxed != nil {
		if err := m.Unsandboxed.validate(); err != nil {
			return err
		}
	}
	if !m.Rungs().any() {
		return errors.New("a plugin needs at least one of: theme, panels, server, process, unsandboxed")
	}
	return nil
}

func checkText(field string, t Text, max int) error {
	if t.empty() {
		return fmt.Errorf("%s.en is required", field)
	}
	for lang, s := range map[string]string{"en": t.EN, "zh-CN": t.ZH} {
		if utf8.RuneCountInString(s) > max {
			return fmt.Errorf("%s.%s is at most %d characters", field, lang, max)
		}
		if strings.ContainsAny(s, "\n\r\t") {
			return fmt.Errorf("%s.%s is one line", field, lang)
		}
	}
	return nil
}

func checkCapabilities(field string, caps []string) error {
	seen := map[string]bool{}
	for _, c := range caps {
		if !KnownCapability(c) {
			return fmt.Errorf("%s: %q is not a capability; see docs/plugins.md §4", field, c)
		}
		if seen[c] {
			return fmt.Errorf("%s: %q is listed twice", field, c)
		}
		seen[c] = true
	}
	return nil
}

func checkHosts(field string, hosts []string) error {
	seen := map[string]bool{}
	for _, h := range hosts {
		if !hostPattern.MatchString(h) {
			return fmt.Errorf("%s: %q is not a host name", field, h)
		}
		if seen[h] {
			return fmt.Errorf("%s: %q is listed twice", field, h)
		}
		seen[h] = true
	}
	return nil
}

func entryPath(field, p string) error {
	if !ValidPath(p) {
		return fmt.Errorf("%s: %q is not a path a plugin can serve", field, p)
	}
	return nil
}

func (t ThemeSpec) validate() error {
	if err := entryPath("theme.file", t.File); err != nil {
		return err
	}
	if path.Ext(t.File) != ".css" {
		return errors.New("theme.file is a .css file")
	}
	if err := checkText("theme.name", t.Name, MaxName); err != nil {
		return err
	}
	if t.Scheme != "" && t.Scheme != "light" && t.Scheme != "dark" {
		return errors.New(`theme.scheme is "light" or "dark"`)
	}
	return nil
}

func (p PanelSpec) validate() error {
	if !slices.Contains(Slots(), p.Slot) {
		return fmt.Errorf("unknown slot %q; slots are %s", p.Slot, strings.Join(Slots(), ", "))
	}
	if err := entryPath("entry", p.Entry); err != nil {
		return err
	}
	if path.Ext(p.Entry) != ".html" {
		return errors.New("entry is an .html file")
	}
	if err := checkText("title", p.Title, MaxName); err != nil {
		return err
	}
	if p.Icon != "" && !iconPattern.MatchString(p.Icon) {
		return fmt.Errorf("icon %q is not an icon name", p.Icon)
	}
	switch p.Slot {
	case SlotSettings:
		if p.Group != "" && !slices.Contains(SettingsGroups, p.Group) {
			return fmt.Errorf("group %q is not a settings group; groups are %s", p.Group, strings.Join(SettingsGroups, ", "))
		}
	case SlotPage:
		if !pagePath.MatchString(p.Path) {
			return errors.New("a page slot needs a path: lower-case letters, digits and dashes")
		}
	default:
		if p.Group != "" || p.Path != "" {
			return fmt.Errorf("group and path belong to settings.section and page slots, not %s", p.Slot)
		}
	}
	return nil
}

func (s SettingsSpec) validate() error {
	if s.Group != "" && !slices.Contains(SettingsGroups, s.Group) {
		return fmt.Errorf("settings.group %q is not a settings group; groups are %s", s.Group, strings.Join(SettingsGroups, ", "))
	}
	if len(s.Fields) == 0 {
		return errors.New("settings.fields is empty")
	}
	if len(s.Fields) > MaxSettings {
		return fmt.Errorf("at most %d settings", MaxSettings)
	}
	seen := map[string]bool{}
	for i, f := range s.Fields {
		where := fmt.Sprintf("settings.fields[%d]", i)
		if !keyPattern.MatchString(f.Key) {
			return fmt.Errorf("%s: key %q is a letter followed by letters, digits and underscores", where, f.Key)
		}
		if seen[f.Key] {
			return fmt.Errorf("%s: key %q is used twice", where, f.Key)
		}
		seen[f.Key] = true
		if !slices.Contains(fieldTypes, f.Type) {
			return fmt.Errorf("%s: type %q is not one of %s", where, f.Type, strings.Join(fieldTypes, ", "))
		}
		if err := checkText(where+".label", f.Label, MaxName); err != nil {
			return err
		}
		if f.Hint != nil {
			if err := checkText(where+".hint", *f.Hint, MaxTextLine); err != nil {
				return err
			}
		}
		if f.Type == FieldEnum && len(f.Values) == 0 {
			return fmt.Errorf("%s: an enum needs values", where)
		}
		if f.Type != FieldEnum && len(f.Values) > 0 {
			return fmt.Errorf("%s: values belong to an enum", where)
		}
		if f.Type == FieldSecret && f.Default != nil {
			return fmt.Errorf("%s: a secret has no default; it would be published with the plugin", where)
		}
		if f.Default != nil {
			if _, err := CheckFieldValue(f, f.Default); err != nil {
				return fmt.Errorf("%s: default: %w", where, err)
			}
		}
	}
	return nil
}

// validateBackend runs the share page checks over the parts borrowed from
// it: data keys and sources, which are the same shapes under the same bounds.
func (m Manifest) validateBackend() error {
	pm := pages.Manifest{SDK: pages.SDKVersion, Name: m.ID, Data: m.Data, Sources: m.Sources}
	// pages refuses anything but https://host/… there, so a plugin's sources
	// are bounded by the same rule without a second copy of it.
	return pm.Validate()
}

func (s ServerSpec) validate() error {
	if err := entryPath("server.entry", s.Entry); err != nil {
		return err
	}
	if ext := path.Ext(s.Entry); ext != ".js" && ext != ".mjs" {
		return errors.New("server.entry is a .js file")
	}
	if s.Every != "" {
		if _, err := ParseEvery(s.Every); err != nil {
			return fmt.Errorf("server.every: %w", err)
		}
	}
	seen := map[string]bool{}
	for _, ev := range s.On {
		if !slices.Contains(Events, ev) {
			return fmt.Errorf("server.on: %q is not an event; events are %s", ev, strings.Join(Events, ", "))
		}
		if seen[ev] {
			return fmt.Errorf("server.on: %q is listed twice", ev)
		}
		seen[ev] = true
	}
	if len(s.Routes) > 32 {
		return errors.New("at most 32 routes")
	}
	for r, fn := range s.Routes {
		if !routePattern.MatchString(r) {
			return fmt.Errorf("server.routes: %q is not \"METHOD /path\"", r)
		}
		if !handlerName.MatchString(fn) {
			return fmt.Errorf("server.routes: %q is not a function name", fn)
		}
	}
	return nil
}

func (p ProcessSpec) validate() error {
	if len(p.Command) == 0 || strings.TrimSpace(p.Command[0]) == "" {
		return errors.New("process.command needs a program")
	}
	if len(p.Command) > 64 {
		return errors.New("process.command is at most 64 words")
	}
	for _, a := range p.Command {
		if strings.ContainsAny(a, "\x00\n\r") || len(a) > 1024 {
			return errors.New("process.command: an argument is one line of at most 1024 bytes")
		}
	}
	if p.Restart != "" && p.Restart != "on-failure" && p.Restart != "always" && p.Restart != "never" {
		return errors.New(`process.restart is "on-failure", "always" or "never"`)
	}
	if err := checkCapabilities("process.capabilities", p.Capabilities); err != nil {
		return err
	}
	for _, c := range p.Capabilities {
		if strings.HasPrefix(c, "ui:") {
			return fmt.Errorf("process.capabilities: %q is a frame's capability; a process has no frame", c)
		}
	}
	seen := map[string]bool{}
	for _, e := range p.Env {
		if !secretName.MatchString(e) {
			return fmt.Errorf("process.env: %q is not a secret's name (UPPER_CASE)", e)
		}
		if seen[e] {
			return fmt.Errorf("process.env: %q is listed twice", e)
		}
		seen[e] = true
	}
	if p.HTTP != nil {
		if err := p.HTTP.validate(); err != nil {
			return err
		}
	}
	return checkHosts("process.hosts", p.Hosts)
}

func (u UnsandboxedSpec) validate() error {
	if err := entryPath("unsandboxed.entry", u.Entry); err != nil {
		return err
	}
	if ext := path.Ext(u.Entry); ext != ".js" && ext != ".mjs" {
		return errors.New("unsandboxed.entry is a .js or .mjs module")
	}
	if u.Tested == "" {
		return errors.New("unsandboxed.tested is required: the panel versions the module was tested on, e.g. \"1.26.0 - 1.26.x\"")
	}
	if _, err := ParseRange(u.Tested); err != nil {
		return fmt.Errorf("unsandboxed.tested: %w", err)
	}
	return checkHosts("unsandboxed.hosts", u.Hosts)
}

// Rungs reads which rungs the manifest climbs.
func (m Manifest) Rungs() Rungs {
	return Rungs{
		Theme:       m.Theme != nil,
		Panel:       len(m.Panels) > 0,
		Service:     m.Server != nil,
		Process:     m.Process != nil,
		Unsandboxed: m.Unsandboxed != nil,
	}
}

func (r Rungs) any() bool { return r.Theme || r.Panel || r.Service || r.Process || r.Unsandboxed }

// Names lists the rungs by their word, in ladder order.
func (r Rungs) Names() []string {
	var out []string
	for _, x := range []struct {
		on   bool
		name string
	}{{r.Theme, RungTheme}, {r.Panel, RungPanel}, {r.Service, RungService}, {r.Process, RungProcess}, {r.Unsandboxed, RungUnsandboxed}} {
		if x.on {
			out = append(out, x.name)
		}
	}
	return out
}

// AllCapabilities is every capability the manifest asks for across its rungs,
// each once, in table order. What the install screen lists and what a grant
// records.
func (m Manifest) AllCapabilities() []string {
	want := map[string]bool{}
	for _, c := range m.Capabilities {
		want[c] = true
	}
	if m.Process != nil {
		for _, c := range m.Process.Capabilities {
			want[c] = true
		}
	}
	for _, s := range m.Sources {
		if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
			want[CapNet+u.Host] = true
		}
	}
	var out []string
	for _, c := range Capabilities() {
		if want[c.Name] {
			out = append(out, c.Name)
			delete(want, c.Name)
		}
	}
	// net:<host> capabilities are per host, after the fixed ones, sorted.
	var hosts []string
	for c := range want {
		hosts = append(hosts, c)
	}
	slices.Sort(hosts)
	return append(out, hosts...)
}

// Encode renders a manifest the way it is stored.
func (m Manifest) Encode() (json.RawMessage, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return raw, nil
}

// DecodeStored reads a manifest back out of the database.
//
// Lenient in one direction only: a stored manifest was valid when it was
// published, and if this build no longer accepts a part of it -- a slot
// retired, an event renamed -- the plugin is read with what still validates
// rather than refused, so a plugin published against an older panel keeps
// installing. What is dropped is dropped whole (the panels list, the server,
// the process) rather than repaired, which is the direction that fails
// closed, and the caller learns what was dropped from the second result so
// the card can say so.
//
// Which part to drop is read off the validation error, whose messages name
// the section they are about; a failure that names no section is the core
// of the manifest and the plugin is read as a bare row.
func DecodeStored(raw []byte) (Manifest, []string) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{Plugin: ManifestVersion, ID: "plugin", Name: Text{EN: "plugin"}, Version: "0.0.0"}, []string{"manifest"}
	}
	var dropped []string
	sections := []struct {
		name  string
		clear func(*Manifest)
	}{
		{"unsandboxed", func(x *Manifest) { x.Unsandboxed = nil }},
		{"process", func(x *Manifest) { x.Process = nil }},
		{"inbound", func(x *Manifest) { x.Inbound = nil }},
		{"server", func(x *Manifest) { x.Server, x.Inbound = nil, nil }},
		{"sources", func(x *Manifest) { x.Sources = nil }},
		{"data", func(x *Manifest) { x.Data = nil }},
		{"settings", func(x *Manifest) { x.Settings = nil }},
		{"panels", func(x *Manifest) { x.Panels = nil }},
		{"theme", func(x *Manifest) { x.Theme = nil }},
		{"capabilities", func(x *Manifest) { x.Capabilities = nil }},
	}
	for range sections {
		err := m.Validate()
		if err == nil {
			return m, dropped
		}
		msg := err.Error()
		found := false
		for _, s := range sections {
			if strings.HasPrefix(msg, s.name) || strings.Contains(msg, "\""+s.name+"\"") {
				s.clear(&m)
				dropped = append(dropped, s.name)
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	if m.Validate() == nil {
		return m, dropped
	}
	bare := Manifest{Plugin: ManifestVersion, ID: m.ID, Name: m.Name, Version: m.Version}
	if !ValidID(bare.ID) {
		bare.ID = "plugin"
	}
	if bare.Name.empty() {
		bare.Name = Text{EN: bare.ID}
	}
	if !semverPattern.MatchString(bare.Version) {
		bare.Version = "0.0.0"
	}
	return bare, append(dropped, "manifest")
}

// ParseEvery reads a schedule interval: 1m to 24h.
func ParseEvery(s string) (minutes int, err error) {
	mm := durationSpec.FindStringSubmatch(s)
	if mm == nil {
		return 0, fmt.Errorf("%q is not a duration like 5m or 1h", s)
	}
	n := 0
	fmt.Sscanf(mm[1], "%d", &n)
	if mm[2] == "h" {
		n *= 60
	}
	if n < 1 || n > 24*60 {
		return 0, errors.New("every is between 1m and 24h")
	}
	return n, nil
}
