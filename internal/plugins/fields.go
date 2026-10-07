package plugins

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Values for a declarative setting. docs/plugins.md §5a.
//
// Checked strictly when the owner writes one (a value outside the schema is a
// 400 naming the field) and read leniently (a stored value the schema no
// longer accepts reads as the default), the same two directions a page's
// parameters have.

const (
	maxSettingText = 2000
	maxSettingList = 100
)

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// CheckFieldValue checks v against f and returns it normalised: a number as
// float64, text trimmed of control characters, a list of strings.
func CheckFieldValue(f FieldSpec, v any) (any, error) {
	switch f.Type {
	case FieldText, FieldSecret:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be text")
		}
		if !cleanText(s) {
			return nil, errors.New("has control characters or bidi overrides")
		}
		max := maxSettingText
		if f.Max != nil && *f.Max > 0 && int(*f.Max) < max {
			max = int(*f.Max)
		}
		if utf8.RuneCountInString(s) > max {
			return nil, fmt.Errorf("is at most %d characters", max)
		}
		return s, nil
	case FieldNumber:
		n, ok := toFloat(v)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, errors.New("must be a number")
		}
		if f.Min != nil && n < *f.Min {
			return nil, fmt.Errorf("is at least %g", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return nil, fmt.Errorf("is at most %g", *f.Max)
		}
		return n, nil
	case FieldBool:
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("must be true or false")
		}
		return b, nil
	case FieldEnum:
		s, ok := v.(string)
		if !ok || !contains(f.Values, s) {
			return nil, fmt.Errorf("must be one of %s", strings.Join(f.Values, ", "))
		}
		return s, nil
	case FieldColor:
		s, ok := v.(string)
		if !ok || !colorPattern.MatchString(s) {
			return nil, errors.New("must be a colour like #1a2b3c")
		}
		return strings.ToLower(s), nil
	case FieldList:
		items, ok := v.([]any)
		if !ok {
			return nil, errors.New("must be a list of text")
		}
		max := maxSettingList
		if f.Max != nil && *f.Max > 0 && int(*f.Max) < max {
			max = int(*f.Max)
		}
		if len(items) > max {
			return nil, fmt.Errorf("holds at most %d items", max)
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			s, ok := it.(string)
			if !ok || !cleanText(s) || utf8.RuneCountInString(s) > maxSettingText {
				return nil, errors.New("every item is one line of text")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown type %q", f.Type)
}

// DefaultValue is what a field reads as before anybody set it.
func DefaultValue(f FieldSpec) any {
	if f.Default != nil {
		if v, err := CheckFieldValue(f, f.Default); err == nil {
			return v
		}
	}
	switch f.Type {
	case FieldText, FieldSecret:
		return ""
	case FieldNumber:
		if f.Min != nil {
			return *f.Min
		}
		return float64(0)
	case FieldBool:
		return false
	case FieldEnum:
		if len(f.Values) > 0 {
			return f.Values[0]
		}
		return ""
	case FieldColor:
		return "#000000"
	case FieldList:
		return []string{}
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// cleanText refuses control characters (line breaks included) and the bidi
// overrides, which is what a settings value drawn in the panel's own chrome
// must not carry.
func cleanText(s string) bool {
	for _, r := range s {
		if r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return false
		}
		switch r {
		case 0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069:
			return false
		}
	}
	return true
}
