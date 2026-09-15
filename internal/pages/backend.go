package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// What a page may declare beyond drawing a snapshot: its own data, an admin
// page, sources the server fetches, server code, and actions. The rules, and
// nothing that runs them -- internal/httpapi runs them. docs/page-backend.md
// is the design and says why each bound is the number it is.

// Data types. docs/page-backend.md §2.
const (
	DataText    = "text"
	DataNumber  = "number"
	DataBool    = "bool"
	DataEnum    = "enum"
	DataColor   = "color"
	DataList    = "list"
	DataObject  = "object"
	DataJSON    = "json"
	DataCounter = "counter"
	DataLog     = "log"
)

// Bounds on data.
const (
	MaxDataKeys      = 64
	MaxDataBytes     = 256 << 10
	MaxDataText      = 10000
	defaultDataText  = 1000
	MaxDataList      = 500
	defaultDataList  = 100
	MaxDataLog       = 1000
	defaultDataLog   = 100
	MaxDataJSON      = 65536
	defaultDataJSON  = 8192
	MaxObjectFields  = 32
	MaxDataEnum      = 50
	MaxSources       = 16
	MaxSourceBytes   = 1 << 20
	defaultSourceMax = 256 << 10
	MaxSourceTimeout = 10 * time.Second
	defaultTimeout   = 5 * time.Second
	MaxActions       = 32
	MaxActionRate    = 1000
	MaxActionBody    = 4 << 10
	MaxServerResult  = 64 << 10
)

// Visibility of a data key.
const (
	VisibilityPublic = "public"
	VisibilityAdmin  = "admin"
)

// Who may run an action.
const (
	WhoVisitor = "visitor"
	WhoAdmin   = "admin"
	WhoBoth    = "both"
)

// Action effects.
const (
	EffectIncrement = "increment"
	EffectAppend    = "append"
	EffectSet       = "set"
	EffectServer    = "server"
)

// DataSpec is one declared key of a page's data, or an item or field of one.
type DataSpec struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	// Min and Max bound a number; Max also bounds a text's runes, a list's
	// items and a log's entries.
	Min      *float64             `json:"min,omitempty"`
	Max      *float64             `json:"max,omitempty"`
	Values   []string             `json:"values,omitempty"`
	MaxBytes int                  `json:"maxBytes,omitempty"`
	Item     *DataSpec            `json:"item,omitempty"`
	Fields   map[string]*DataSpec `json:"fields,omitempty"`
	Default  any                  `json:"default,omitempty"`
	// Visibility is "public" (the default: in every link's snapshot) or
	// "admin" (only through the admin API and settings). Top-level keys only.
	Visibility string `json:"visibility,omitempty"`
}

// AdminOptions is a page's own admin page.
type AdminOptions struct {
	// Entry is the admin page's HTML, inside a directory of its own so the
	// share routes can refuse the whole directory.
	Entry string `json:"entry"`
}

// Dir is the directory the admin page's files are in.
func (a AdminOptions) Dir() string { return path.Dir(a.Entry) }

// SourceSpec is one URL the server fetches for the page.
type SourceSpec struct {
	Key      string            `json:"key"`
	URL      string            `json:"url"`
	Every    string            `json:"every"`
	Headers  map[string]string `json:"headers,omitempty"`
	MaxBytes int               `json:"maxBytes,omitempty"`
	Timeout  string            `json:"timeout,omitempty"`
	Parse    string            `json:"parse,omitempty"`
}

// ServerOptions is the page's server.js.
type ServerOptions struct {
	Entry string `json:"entry"`
	// Every is how often onSchedule runs while the page is watched; "" never.
	Every string `json:"every,omitempty"`
}

// ActionSpec is one thing a visitor or an admin may do.
type ActionSpec struct {
	Who    string               `json:"who"`
	Effect ActionEffect         `json:"effect"`
	Input  map[string]*DataSpec `json:"input,omitempty"`
	Rate   string               `json:"rate,omitempty"`
	Label  string               `json:"label,omitempty"`
	// Writes are the data keys a "server" effect may set from a visitor
	// action. docs/page-backend.md §5.
	Writes []string `json:"writes,omitempty"`
}

// ActionEffect is `{"increment": key}`, `{"append": key}`, `{"set": key}` or
// `"server"`.
type ActionEffect struct {
	Kind string
	Key  string
}

// UnmarshalJSON reads either shape.
func (e *ActionEffect) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s != EffectServer {
			return fmt.Errorf(`an effect is "server" or {"increment"|"append"|"set": key}, not %q`, s)
		}
		*e = ActionEffect{Kind: EffectServer}
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m map[string]string
	if err := dec.Decode(&m); err != nil || len(m) != 1 {
		return errors.New(`an effect is "server" or one of {"increment": key}, {"append": key}, {"set": key}`)
	}
	for k, v := range m {
		if k != EffectIncrement && k != EffectAppend && k != EffectSet {
			return fmt.Errorf("unknown effect %q", k)
		}
		*e = ActionEffect{Kind: k, Key: v}
	}
	return nil
}

// MarshalJSON writes the shape it was read from.
func (e ActionEffect) MarshalJSON() ([]byte, error) {
	if e.Kind == EffectServer {
		return json.Marshal(EffectServer)
	}
	return json.Marshal(map[string]string{e.Kind: e.Key})
}

// ─── validation ───────────────────────────────────────────────────────────

var secretRef = regexp.MustCompile(`\$\{secret:([^}]*)\}`)

// SecretName is what a `${secret:NAME}` may name.
var SecretName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

var headerName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

var rateSpec = regexp.MustCompile(`^([0-9]{1,4})/(s|min|hour)$`)

// forbiddenHeaders are set by the fetcher, or would change what it asked for.
var forbiddenHeaders = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true, "connection": true,
	"cookie": true, "proxy-authorization": true, "te": true, "upgrade": true,
}

func (m Manifest) validateBackend() error {
	if len(m.Data) > MaxDataKeys {
		return fmt.Errorf("data: at most %d keys", MaxDataKeys)
	}
	for _, key := range sortedKeys(m.Data) {
		spec := m.Data[key]
		if !paramKey.MatchString(key) {
			return fmt.Errorf("data: %q is not a key (letters, digits, underscore, starting with a letter)", key)
		}
		if spec == nil {
			return fmt.Errorf("data.%s: missing", key)
		}
		if err := spec.validate(0); err != nil {
			return fmt.Errorf("data.%s: %w", key, err)
		}
		if spec.Visibility != "" && spec.Visibility != VisibilityPublic && spec.Visibility != VisibilityAdmin {
			return fmt.Errorf("data.%s: visibility is %q or %q", key, VisibilityPublic, VisibilityAdmin)
		}
	}

	if m.Admin != nil {
		if !ValidPath(m.Admin.Entry) || path.Ext(m.Admin.Entry) != ".html" {
			return errors.New("admin.entry must be an .html file in the page, like admin/index.html")
		}
		if m.Admin.Dir() == "." {
			return errors.New("admin.entry must be inside a directory of its own, like admin/index.html, " +
				"so a share link can be refused every file of the admin page")
		}
	}

	if m.Server != nil {
		if !ValidPath(m.Server.Entry) || path.Ext(m.Server.Entry) != ".js" {
			return errors.New("server.entry must be a .js file in the page, like server.js")
		}
		if m.Admin != nil && strings.HasPrefix(m.Server.Entry, m.Admin.Dir()+"/") {
			return errors.New("server.entry must not be inside the admin page's directory")
		}
		if m.Server.Every != "" {
			if _, err := boundedDuration("server.every", m.Server.Every, time.Minute, 24*time.Hour); err != nil {
				return err
			}
		}
	}

	if len(m.Sources) > MaxSources {
		return fmt.Errorf("sources: at most %d", MaxSources)
	}
	seen := map[string]bool{}
	for i, src := range m.Sources {
		if err := src.validate(); err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
		if seen[src.Key] {
			return fmt.Errorf("sources: %q is listed twice", src.Key)
		}
		seen[src.Key] = true
	}

	if len(m.Actions) > MaxActions {
		return fmt.Errorf("actions: at most %d", MaxActions)
	}
	for _, name := range sortedKeys(m.Actions) {
		if !paramKey.MatchString(name) {
			return fmt.Errorf("actions: %q is not a name", name)
		}
		if err := m.validateAction(m.Actions[name]); err != nil {
			return fmt.Errorf("actions.%s: %w", name, err)
		}
	}
	return nil
}

func (m Manifest) validateAction(a *ActionSpec) error {
	if a == nil {
		return errors.New("missing")
	}
	switch a.Who {
	case WhoVisitor, WhoAdmin, WhoBoth:
	default:
		return fmt.Errorf("who is %q, %q or %q", WhoVisitor, WhoAdmin, WhoBoth)
	}
	if utf8.RuneCountInString(a.Label) > MaxParamLabel {
		return fmt.Errorf("label is at most %d characters", MaxParamLabel)
	}
	if a.Rate != "" {
		if _, err := ParseRate(a.Rate); err != nil {
			return err
		}
	}
	for _, field := range sortedKeys(a.Input) {
		spec := a.Input[field]
		if !paramKey.MatchString(field) || spec == nil {
			return fmt.Errorf("input: %q is not a field", field)
		}
		if !scalarType(spec.Type) {
			return fmt.Errorf("input.%s: an input field is text, number, bool, enum or color", field)
		}
		if err := spec.validate(1); err != nil {
			return fmt.Errorf("input.%s: %w", field, err)
		}
	}
	var target *DataSpec
	if a.Effect.Kind != EffectServer {
		target = m.Data[a.Effect.Key]
		if target == nil {
			return fmt.Errorf("effect names %q, which is not declared in data", a.Effect.Key)
		}
	}
	switch a.Effect.Kind {
	case EffectIncrement:
		if target.Type != DataCounter {
			return fmt.Errorf("increment needs a counter, and data.%s is %s", a.Effect.Key, target.Type)
		}
		if len(a.Input) > 0 {
			return errors.New("an increment takes no input")
		}
	case EffectAppend:
		if target.Type != DataLog {
			return fmt.Errorf("append needs a log, and data.%s is %s", a.Effect.Key, target.Type)
		}
		if len(a.Input) > 0 && !sameFields(a.Input, ItemFields(target.Item)) {
			return fmt.Errorf("the input must be the fields of data.%s's item, or left out", a.Effect.Key)
		}
	case EffectSet:
		if a.Who != WhoAdmin {
			return errors.New(`a set effect is for who: "admin" only; a visitor changes data through ` +
				`increment, append or server`)
		}
		if target.Type == DataCounter || target.Type == DataLog {
			return fmt.Errorf("data.%s is a %s and is not set; use increment or append", a.Effect.Key, target.Type)
		}
		if len(a.Input) > 0 {
			return errors.New(`a set takes the value as {"value": …}; leave input out`)
		}
	case EffectServer:
		if m.Server == nil {
			return errors.New(`a "server" effect needs server.entry`)
		}
	default:
		return errors.New("effect is missing")
	}
	for _, key := range a.Writes {
		if m.Data[key] == nil {
			return fmt.Errorf("writes names %q, which is not declared in data", key)
		}
	}
	if len(a.Writes) > 0 && a.Effect.Kind != EffectServer {
		return errors.New(`writes is for a "server" effect`)
	}
	return nil
}

// ItemFields is the object fields an item carries: an object's own fields, or
// one field "value" for a scalar item.
func ItemFields(item *DataSpec) map[string]*DataSpec {
	if item == nil {
		return nil
	}
	if item.Type == DataObject {
		return item.Fields
	}
	return map[string]*DataSpec{"value": item}
}

func sameFields(a, b map[string]*DataSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for k, spec := range a {
		other, ok := b[k]
		if !ok || other.Type != spec.Type {
			return false
		}
	}
	return true
}

func scalarType(t string) bool {
	switch t {
	case DataText, DataNumber, DataBool, DataEnum, DataColor:
		return true
	}
	return false
}

// validate checks a spec; depth 0 is a top-level key, 1 an item or field.
func (d *DataSpec) validate(depth int) error {
	if utf8.RuneCountInString(d.Label) > MaxParamLabel {
		return fmt.Errorf("label is at most %d characters", MaxParamLabel)
	}
	if depth > 0 && d.Visibility != "" {
		return errors.New("visibility belongs on a top-level key")
	}
	noMinMax := func() error {
		if d.Min != nil || d.Max != nil {
			return fmt.Errorf("min and max do not apply to %s", d.Type)
		}
		return nil
	}
	if d.Type != DataEnum && len(d.Values) > 0 {
		return fmt.Errorf("values do not apply to %s", d.Type)
	}
	if d.Type != DataJSON && d.MaxBytes != 0 {
		return fmt.Errorf("maxBytes does not apply to %s", d.Type)
	}
	if d.Type != DataList && d.Type != DataLog && d.Item != nil {
		return fmt.Errorf("item does not apply to %s", d.Type)
	}
	if d.Type != DataObject && d.Fields != nil {
		return fmt.Errorf("fields do not apply to %s", d.Type)
	}
	switch d.Type {
	case DataText:
		if d.Min != nil {
			return errors.New("min does not apply to text")
		}
		if d.Max != nil && (*d.Max < 1 || *d.Max > MaxDataText || *d.Max != math.Trunc(*d.Max)) {
			return fmt.Errorf("max is a whole number from 1 to %d", MaxDataText)
		}
	case DataNumber:
		if d.Min != nil && !finite(*d.Min) || d.Max != nil && !finite(*d.Max) {
			return errors.New("min and max must be finite")
		}
		if d.Min != nil && d.Max != nil && *d.Min > *d.Max {
			return errors.New("min is above max")
		}
	case DataBool, DataColor:
		if err := noMinMax(); err != nil {
			return err
		}
	case DataEnum:
		if err := noMinMax(); err != nil {
			return err
		}
		if len(d.Values) == 0 || len(d.Values) > MaxDataEnum {
			return fmt.Errorf("values is 1 to %d strings", MaxDataEnum)
		}
		seen := map[string]bool{}
		for _, v := range d.Values {
			if v == "" || utf8.RuneCountInString(v) > MaxEnumValue || seen[v] {
				return fmt.Errorf("values must be distinct, non-empty and at most %d characters", MaxEnumValue)
			}
			seen[v] = true
		}
	case DataList, DataLog:
		if depth > 0 {
			return fmt.Errorf("a %s is a top-level key, not an item or a field", d.Type)
		}
		if d.Min != nil {
			return fmt.Errorf("min does not apply to %s", d.Type)
		}
		limit := float64(MaxDataList)
		if d.Type == DataLog {
			limit = MaxDataLog
		}
		if d.Max != nil && (*d.Max < 1 || *d.Max > limit || *d.Max != math.Trunc(*d.Max)) {
			return fmt.Errorf("max is a whole number from 1 to %d", int(limit))
		}
		if d.Item == nil {
			return fmt.Errorf("a %s needs an item", d.Type)
		}
		if !scalarType(d.Item.Type) && d.Item.Type != DataObject {
			return errors.New("an item is text, number, bool, enum, color or object")
		}
		if err := d.Item.validate(1); err != nil {
			return fmt.Errorf("item: %w", err)
		}
		if d.Type == DataLog && d.Item.Type == DataObject && d.Item.Fields["at"] != nil {
			return errors.New(`a log item cannot have a field "at"; every entry is stamped with it`)
		}
	case DataObject:
		if err := noMinMax(); err != nil {
			return err
		}
		if len(d.Fields) == 0 || len(d.Fields) > MaxObjectFields {
			return fmt.Errorf("fields is 1 to %d fields", MaxObjectFields)
		}
		for _, name := range sortedKeys(d.Fields) {
			f := d.Fields[name]
			if !paramKey.MatchString(name) || f == nil {
				return fmt.Errorf("fields: %q is not a field", name)
			}
			if !scalarType(f.Type) {
				return fmt.Errorf("fields.%s: a field is text, number, bool, enum or color", name)
			}
			if err := f.validate(depth + 1); err != nil {
				return fmt.Errorf("fields.%s: %w", name, err)
			}
		}
	case DataJSON:
		if depth > 0 {
			return errors.New("json is a top-level key, not an item or a field")
		}
		if err := noMinMax(); err != nil {
			return err
		}
		if d.MaxBytes < 0 || d.MaxBytes > MaxDataJSON {
			return fmt.Errorf("maxBytes is at most %d", MaxDataJSON)
		}
	case DataCounter:
		if depth > 0 {
			return errors.New("a counter is a top-level key, not an item or a field")
		}
		if err := noMinMax(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown type %q", d.Type)
	}
	if d.Default != nil {
		if d.Type == DataCounter || d.Type == DataLog {
			return fmt.Errorf("a %s has no default", d.Type)
		}
		if _, err := d.Check(d.Default, false); err != nil {
			return fmt.Errorf("default %w", err)
		}
	}
	return nil
}

func (s SourceSpec) validate() error {
	if !paramKey.MatchString(s.Key) {
		return fmt.Errorf("key %q is not a key", s.Key)
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%s: url must be https://host/…, with no user or fragment", s.Key)
	}
	if _, err := boundedDuration(s.Key+".every", s.Every, time.Minute, 24*time.Hour); err != nil {
		return err
	}
	if s.Timeout != "" {
		if _, err := boundedDuration(s.Key+".timeout", s.Timeout, time.Second, MaxSourceTimeout); err != nil {
			return err
		}
	}
	if s.MaxBytes < 0 || s.MaxBytes > MaxSourceBytes {
		return fmt.Errorf("%s: maxBytes is at most %d", s.Key, MaxSourceBytes)
	}
	if s.Parse != "" && s.Parse != "json" && s.Parse != "text" {
		return fmt.Errorf(`%s: parse is "json" or "text"`, s.Key)
	}
	if len(s.Headers) > 16 {
		return fmt.Errorf("%s: at most 16 headers", s.Key)
	}
	for name, value := range s.Headers {
		if !headerName.MatchString(name) || forbiddenHeaders[strings.ToLower(name)] {
			return fmt.Errorf("%s: header %q is not allowed", s.Key, name)
		}
		if len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s: header %q is too long or has a line break", s.Key, name)
		}
		for _, m := range secretRef.FindAllStringSubmatch(value, -1) {
			if !SecretName.MatchString(m[1]) {
				return fmt.Errorf("%s: ${secret:%s} is not a secret name (A-Z, 0-9, _)", s.Key, m[1])
			}
		}
	}
	if strings.Contains(s.URL, "${") {
		return fmt.Errorf("%s: secrets go in headers, not in the url, which is shown when a host is approved", s.Key)
	}
	return nil
}

// Secrets lists the secret names a source's headers use.
func (s SourceSpec) Secrets() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, name := range sortedKeys(s.Headers) {
		for _, m := range secretRef.FindAllStringSubmatch(s.Headers[name], -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// ExpandSecrets replaces ${secret:NAME} with lookup(NAME); a name lookup does
// not have is an error naming it.
func ExpandSecrets(value string, lookup func(string) (string, bool)) (string, error) {
	var missing string
	out := secretRef.ReplaceAllStringFunc(value, func(m string) string {
		name := secretRef.FindStringSubmatch(m)[1]
		v, ok := lookup(name)
		if !ok {
			missing = name
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("secret %s is not set", missing)
	}
	return out, nil
}

// Interval is how often the source is fetched.
func (s SourceSpec) Interval() time.Duration {
	d, _ := boundedDuration("", s.Every, time.Minute, 24*time.Hour)
	return d
}

// TimeoutOrDefault is the fetch's deadline.
func (s SourceSpec) TimeoutOrDefault() time.Duration {
	if s.Timeout == "" {
		return defaultTimeout
	}
	d, _ := boundedDuration("", s.Timeout, time.Second, MaxSourceTimeout)
	return d
}

// MaxBytesOrDefault is the largest body the fetch reads.
func (s SourceSpec) MaxBytesOrDefault() int {
	if s.MaxBytes == 0 {
		return defaultSourceMax
	}
	return s.MaxBytes
}

// ParseDuration reads "30s", "10m", "2h": one number and one unit, because a
// manifest is read by people and "1h30m" is a thing to get wrong.
func ParseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("%q is not a duration like 30s, 10m or 2h", s)
	}
	unit := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour}[s[len(s)-1]]
	n, err := strconv.Atoi(s[:len(s)-1])
	if unit == 0 || err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a duration like 30s, 10m or 2h", s)
	}
	return time.Duration(n) * unit, nil
}

func boundedDuration(field, s string, lo, hi time.Duration) (time.Duration, error) {
	d, err := ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	if d < lo || d > hi {
		return 0, fmt.Errorf("%s is between %s and %s", field, lo, hi)
	}
	return d, nil
}

// Rate is a number of actions per window.
type Rate struct {
	N      int
	Window time.Duration
}

// DefaultRate is an action's rate when it does not say.
var DefaultRate = Rate{N: 30, Window: time.Minute}

// ParseRate reads "5/min", "2/s", "100/hour".
func ParseRate(s string) (Rate, error) {
	m := rateSpec.FindStringSubmatch(s)
	if m == nil {
		return Rate{}, fmt.Errorf(`rate %q is not like "5/min", "2/s" or "100/hour"`, s)
	}
	n, _ := strconv.Atoi(m[1])
	if n < 1 || n > MaxActionRate {
		return Rate{}, fmt.Errorf("rate is 1 to %d per window", MaxActionRate)
	}
	w := map[string]time.Duration{"s": time.Second, "min": time.Minute, "hour": time.Hour}[m[2]]
	return Rate{N: n, Window: w}, nil
}

// RateOrDefault is the action's rate.
func (a ActionSpec) RateOrDefault() Rate {
	if a.Rate == "" {
		return DefaultRate
	}
	r, err := ParseRate(a.Rate)
	if err != nil {
		return DefaultRate
	}
	return r
}

// VisitorMay reports whether a visitor may run the action.
func (a ActionSpec) VisitorMay() bool { return a.Who == WhoVisitor || a.Who == WhoBoth }

// AdminMay reports whether an admin may run the action.
func (a ActionSpec) AdminMay() bool { return a.Who == WhoAdmin || a.Who == WhoBoth }

// InputFields is what the action's payload must match: its input, or for an
// append with none the log item's fields, or for a set {"value": spec}.
func (m Manifest) InputFields(a *ActionSpec) map[string]*DataSpec {
	if len(a.Input) > 0 {
		return a.Input
	}
	switch a.Effect.Kind {
	case EffectAppend:
		if target := m.Data[a.Effect.Key]; target != nil {
			return ItemFields(target.Item)
		}
	case EffectSet:
		if target := m.Data[a.Effect.Key]; target != nil {
			return map[string]*DataSpec{"value": target}
		}
	}
	return map[string]*DataSpec{}
}

// Capabilities says what a manifest declares beyond drawing a snapshot.
type Capabilities struct {
	Data           bool `json:"data"`
	Admin          bool `json:"admin"`
	Sources        bool `json:"sources"`
	Server         bool `json:"server"`
	Actions        bool `json:"actions"`
	VisitorActions bool `json:"visitorActions"`
}

// Capabilities reads them off the manifest.
func (m Manifest) Capabilities() Capabilities {
	c := Capabilities{Data: len(m.Data) > 0, Admin: m.Admin != nil, Sources: len(m.Sources) > 0,
		Server: m.Server != nil, Actions: len(m.Actions) > 0}
	for _, a := range m.Actions {
		if a.VisitorMay() {
			c.VisitorActions = true
		}
	}
	return c
}

// Private reports whether rel is a file of the page that a share link must
// never serve: the admin page's directory, and server code.
func (m Manifest) Private(rel string) bool {
	if m.Server != nil && rel == m.Server.Entry {
		return true
	}
	if m.Admin != nil && strings.HasPrefix(rel, m.Admin.Dir()+"/") {
		return true
	}
	return false
}

// ─── values ───────────────────────────────────────────────────────────────

// Check validates a value against a spec and returns it normalised (numbers
// as float64, colours lower-cased, missing object fields filled with their
// zero). visitor is the stricter reading for text a visitor typed: no control
// characters at all and no bidirectional overrides, where the owner's text
// may carry line breaks and tabs.
func (d *DataSpec) Check(v any, visitor bool) (any, error) {
	switch d.Type {
	case DataText:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be text")
		}
		limit := defaultDataText
		if d.Max != nil {
			limit = int(*d.Max)
		}
		if utf8.RuneCountInString(s) > limit {
			return nil, fmt.Errorf("is at most %d characters", limit)
		}
		if !utf8.ValidString(s) {
			return nil, errors.New("is not valid UTF-8")
		}
		if bad := badRune(s, visitor); bad != "" {
			return nil, errors.New("contains " + bad)
		}
		return s, nil
	case DataNumber:
		f, ok := number(v)
		if !ok {
			return nil, errors.New("must be a number")
		}
		if d.Min != nil && f < *d.Min || d.Max != nil && f > *d.Max {
			return nil, errors.New("is out of range")
		}
		return f, nil
	case DataBool:
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("must be true or false")
		}
		return b, nil
	case DataEnum:
		s, ok := v.(string)
		if ok {
			for _, allowed := range d.Values {
				if s == allowed {
					return s, nil
				}
			}
		}
		return nil, fmt.Errorf("must be one of %s", strings.Join(d.Values, ", "))
	case DataColor:
		s, ok := v.(string)
		if !ok || !hexColor.MatchString(s) {
			return nil, errors.New("must be a colour like #4f7cff")
		}
		return strings.ToLower(s), nil
	case DataList:
		arr, ok := v.([]any)
		if !ok {
			return nil, errors.New("must be a list")
		}
		limit := defaultDataList
		if d.Max != nil {
			limit = int(*d.Max)
		}
		if len(arr) > limit {
			return nil, fmt.Errorf("has at most %d items", limit)
		}
		out := make([]any, 0, len(arr))
		for i, item := range arr {
			clean, err := d.Item.Check(item, visitor)
			if err != nil {
				return nil, fmt.Errorf("item %d %w", i, err)
			}
			out = append(out, clean)
		}
		return out, nil
	case DataObject:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("must be an object")
		}
		out := map[string]any{}
		for k := range obj {
			if d.Fields[k] == nil {
				return nil, fmt.Errorf("has no field %q", k)
			}
		}
		for name, f := range d.Fields {
			fv, present := obj[name]
			if !present {
				out[name] = f.Zero()
				continue
			}
			clean, err := f.Check(fv, visitor)
			if err != nil {
				return nil, fmt.Errorf("%s %w", name, err)
			}
			out[name] = clean
		}
		return out, nil
	case DataJSON:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, errors.New("is not JSON")
		}
		limit := defaultDataJSON
		if d.MaxBytes > 0 {
			limit = d.MaxBytes
		}
		if len(raw) > limit {
			return nil, fmt.Errorf("is at most %d bytes", limit)
		}
		return v, nil
	case DataCounter:
		f, ok := number(v)
		if !ok || f < 0 || f != math.Trunc(f) || f > 1<<53 {
			return nil, errors.New("must be a whole number, 0 or more")
		}
		return f, nil
	case DataLog:
		arr, ok := v.([]any)
		if !ok {
			return nil, errors.New("must be a list of entries")
		}
		limit := defaultDataLog
		if d.Max != nil {
			limit = int(*d.Max)
		}
		if len(arr) > limit {
			arr = arr[len(arr)-limit:]
		}
		out := make([]any, 0, len(arr))
		fields := ItemFields(d.Item)
		for i, e := range arr {
			entry, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("entry %d must be an object", i)
			}
			at, ok := number(entry["at"])
			if !ok {
				return nil, fmt.Errorf("entry %d has no at", i)
			}
			rest := map[string]any{}
			for k, val := range entry {
				if k != "at" {
					rest[k] = val
				}
			}
			clean, err := (&DataSpec{Type: DataObject, Fields: fields}).Check(rest, false)
			if err != nil {
				return nil, fmt.Errorf("entry %d %w", i, err)
			}
			obj := clean.(map[string]any)
			obj["at"] = at
			out = append(out, obj)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown type %q", d.Type)
}

// Zero is the value a key has when nothing was stored: its default, or the
// type's own zero.
func (d *DataSpec) Zero() any {
	if d.Default != nil {
		if v, err := d.Check(d.Default, false); err == nil {
			return v
		}
	}
	switch d.Type {
	case DataText:
		return ""
	case DataNumber:
		if d.Min != nil && *d.Min > 0 {
			return *d.Min
		}
		return float64(0)
	case DataBool:
		return false
	case DataEnum:
		return d.Values[0]
	case DataColor:
		return "#000000"
	case DataList, DataLog:
		return []any{}
	case DataObject:
		out := map[string]any{}
		for name, f := range d.Fields {
			out[name] = f.Zero()
		}
		return out
	case DataCounter:
		return float64(0)
	}
	return nil
}

// LogLimit is how many entries a log keeps.
func (d *DataSpec) LogLimit() int {
	if d.Max != nil {
		return int(*d.Max)
	}
	return defaultDataLog
}

// NewLogEntry makes a log entry out of a payload that matched the item's
// fields, stamped with at.
func NewLogEntry(fields map[string]any, at int64) map[string]any {
	out := map[string]any{}
	for k, v := range fields {
		out[k] = v
	}
	out["at"] = float64(at)
	return out
}

// CheckInput validates an action payload: exactly the declared fields, no
// others, each held to the visitor's reading of text.
func CheckInput(fields map[string]*DataSpec, payload map[string]any, visitor bool) (map[string]any, error) {
	obj := &DataSpec{Type: DataObject, Fields: fields}
	if len(fields) == 0 {
		if len(payload) > 0 {
			return nil, errors.New("this action takes no input")
		}
		return map[string]any{}, nil
	}
	clean, err := obj.Check(payload, visitor)
	if err != nil {
		return nil, err
	}
	return clean.(map[string]any), nil
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, finite(n)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil && finite(f)
	}
	return 0, false
}

// badRune names the first character text may not carry, or "".
//
// The bidirectional overrides and isolates reverse what follows them on the
// screen, which is how a guestbook entry makes the next one read as something
// it does not say. Everyone's text is refused them when a visitor wrote it;
// the owner's is, too, apart from line breaks and tabs, because the owner
// wrote it on purpose and can see it.
func badRune(s string, visitor bool) string {
	for _, r := range s {
		switch {
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
			return "a bidirectional control character"
		case r == '\n' || r == '\t':
			if visitor {
				return "a line break or tab"
			}
		case unicode.IsControl(r):
			return "a control character"
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
