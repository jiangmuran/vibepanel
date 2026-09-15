package pages

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Parameters are the edits that are not code: the title for this room, the
// accent colour, the number at which a count turns amber. A page declares them,
// the owner sets them per link from a form, and the page reads them out of the
// snapshot without reloading.
//
// They are only ever echoed. A parameter never chooses a section, never reaches
// a query and never changes any other key of the snapshot, and that is what
// keeps "a page can only subtract" true with a free-text field in it. The
// values are the owner's words to whoever is looking at the screen, like a
// link's remark, so they are sent under both detail modes.

// The parameter types.
const (
	ParamText   = "text"
	ParamColor  = "color"
	ParamNumber = "number"
	ParamEnum   = "enum"
	ParamBool   = "bool"
)

// Bounds on a parameter list.
const (
	MaxParams      = 24
	MaxParamLabel  = 40
	MaxTextParam   = 200
	defaultTextMax = 80
	MaxEnumValues  = 24
	MaxEnumValue   = 40
)

// ParamSpec is one declared parameter.
//
// One flat struct, checked per type: a
// field that does not belong to the type is refused rather than ignored, and a
// `values` list on a text field that silently does nothing is a form its author
// believes is a select.
type ParamSpec struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	// Min and Max bound a number. Max also bounds a text's length.
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
	Values []string `json:"values,omitempty"`
	// Default is what a link that never set this reads. Optional; each type
	// has its own zero (empty text, #000000, min, the first value, false).
	Default any `json:"default,omitempty"`
}

var paramKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func validateParamSpecs(specs []ParamSpec) error {
	if len(specs) > MaxParams {
		return fmt.Errorf("params: at most %d", MaxParams)
	}
	seen := map[string]bool{}
	for i, p := range specs {
		where := fmt.Sprintf("params[%d]", i)
		if !paramKey.MatchString(p.Key) {
			return fmt.Errorf("%s: key %q must be a letter then up to 31 letters, digits or _", where, p.Key)
		}
		where = "params." + p.Key
		if seen[p.Key] {
			return fmt.Errorf("%s: listed twice", where)
		}
		seen[p.Key] = true
		if utf8.RuneCountInString(p.Label) > MaxParamLabel {
			return fmt.Errorf("%s: label is at most %d characters", where, MaxParamLabel)
		}
		if err := p.validate(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	return nil
}

func (p ParamSpec) validate() error {
	noRange := func() error {
		if p.Min != nil || p.Max != nil {
			return fmt.Errorf("a %s takes no min or max", p.Type)
		}
		return nil
	}
	noValues := func() error {
		if p.Values != nil {
			return fmt.Errorf("a %s takes no values", p.Type)
		}
		return nil
	}
	switch p.Type {
	case ParamText:
		if p.Min != nil {
			return errors.New("a text takes no min")
		}
		if err := noValues(); err != nil {
			return err
		}
		if p.Max != nil && (*p.Max < 1 || *p.Max > MaxTextParam || *p.Max != math.Trunc(*p.Max)) {
			return fmt.Errorf("max is a whole number of characters from 1 to %d", MaxTextParam)
		}
	case ParamColor, ParamBool:
		if err := noRange(); err != nil {
			return err
		}
		if err := noValues(); err != nil {
			return err
		}
	case ParamNumber:
		if err := noValues(); err != nil {
			return err
		}
		if p.Min == nil || p.Max == nil {
			return errors.New("a number needs both min and max")
		}
		if !finite(*p.Min) || !finite(*p.Max) || *p.Min >= *p.Max {
			return errors.New("min must be less than max")
		}
	case ParamEnum:
		if err := noRange(); err != nil {
			return err
		}
		if len(p.Values) == 0 || len(p.Values) > MaxEnumValues {
			return fmt.Errorf("an enum has 1 to %d values", MaxEnumValues)
		}
		seen := map[string]bool{}
		for _, v := range p.Values {
			if v == "" || utf8.RuneCountInString(v) > MaxEnumValue {
				return fmt.Errorf("an enum value is 1 to %d characters", MaxEnumValue)
			}
			if seen[v] {
				return fmt.Errorf("value %q is listed twice", v)
			}
			seen[v] = true
		}
	default:
		return fmt.Errorf("unknown type %q; types are text, color, number, enum, bool", p.Type)
	}
	if p.Default != nil {
		if _, err := p.check(p.Default); err != nil {
			return fmt.Errorf("default: %w", err)
		}
	}
	return nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// zero is the value a parameter has when neither the link nor the default says.
func (p ParamSpec) zero() any {
	if p.Default != nil {
		if v, err := p.check(p.Default); err == nil {
			return v
		}
	}
	switch p.Type {
	case ParamText:
		return ""
	case ParamColor:
		return "#000000"
	case ParamNumber:
		return *p.Min
	case ParamEnum:
		return p.Values[0]
	default:
		return false
	}
}

// check is one value against its spec, normalised: colours lower-cased, text
// as given.
func (p ParamSpec) check(v any) (any, error) {
	switch p.Type {
	case ParamText:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be text")
		}
		limit := defaultTextMax
		if p.Max != nil {
			limit = int(*p.Max)
		}
		if utf8.RuneCountInString(s) > limit {
			return nil, fmt.Errorf("is at most %d characters", limit)
		}
		return s, nil
	case ParamColor:
		s, ok := v.(string)
		if !ok || !hexColor.MatchString(s) {
			return nil, errors.New("must be a colour like #4f7cff")
		}
		return strings.ToLower(s), nil
	case ParamNumber:
		f, ok := v.(float64)
		if !ok {
			if n, isInt := v.(int); isInt {
				f, ok = float64(n), true
			}
		}
		if !ok || !finite(f) {
			return nil, errors.New("must be a number")
		}
		if f < *p.Min || f > *p.Max {
			return nil, fmt.Errorf("must be between %g and %g", *p.Min, *p.Max)
		}
		return f, nil
	case ParamEnum:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be one of the values")
		}
		for _, allowed := range p.Values {
			if s == allowed {
				return s, nil
			}
		}
		return nil, fmt.Errorf("%q is not one of %s", s, strings.Join(p.Values, ", "))
	case ParamBool:
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("must be true or false")
		}
		return b, nil
	}
	return nil, fmt.Errorf("unknown type %q", p.Type)
}

// ValidateParamValues checks values arriving from the settings form.
//
// Strict: a key the page does not declare, or a value that does not fit, is
// an error naming it. Keys left out are fine and read as their default.
func ValidateParamValues(specs []ParamSpec, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range in {
		spec, ok := specNamed(specs, k)
		if !ok {
			return nil, fmt.Errorf("this page has no parameter %q", k)
		}
		clean, err := spec.check(v)
		if err != nil {
			return nil, fmt.Errorf("%s %w", k, err)
		}
		out[k] = clean
	}
	return out, nil
}

// ResolveParams is what a page receives: every declared parameter, with the
// link's value where it still fits and the default where it does not.
//
// Lenient where ValidateParamValues is strict, because a wall has nobody at it. A
// page republished with a narrower range must not break the wall still holding
// a value from the old one; that value is replaced by the default rather than
// passed through, which is the direction that cannot surprise the page.
func ResolveParams(specs []ParamSpec, stored map[string]any) map[string]any {
	out := make(map[string]any, len(specs))
	for _, spec := range specs {
		if v, ok := stored[spec.Key]; ok {
			if clean, err := spec.check(v); err == nil {
				out[spec.Key] = clean
				continue
			}
		}
		out[spec.Key] = spec.zero()
	}
	return out
}

func specNamed(specs []ParamSpec, key string) (ParamSpec, bool) {
	for _, s := range specs {
		if s.Key == key {
			return s, true
		}
	}
	return ParamSpec{}, false
}
