package plugins

import (
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

func TestCheckFieldValue(t *testing.T) {
	ok := []struct {
		f FieldSpec
		v any
	}{
		{FieldSpec{Type: FieldText, Max: f64(5)}, "hello"},
		{FieldSpec{Type: FieldNumber, Min: f64(1), Max: f64(9)}, float64(5)},
		{FieldSpec{Type: FieldNumber}, 7},
		{FieldSpec{Type: FieldBool}, true},
		{FieldSpec{Type: FieldEnum, Values: []string{"a", "b"}}, "b"},
		{FieldSpec{Type: FieldColor}, "#AABBCC"},
		{FieldSpec{Type: FieldList, Max: f64(2)}, []any{"x", "y"}},
		{FieldSpec{Type: FieldSecret}, "s3cret"},
	}
	for _, c := range ok {
		if _, err := CheckFieldValue(c.f, c.v); err != nil {
			t.Errorf("%s %v refused: %v", c.f.Type, c.v, err)
		}
	}
	bad := []struct {
		f    FieldSpec
		v    any
		want string
	}{
		{FieldSpec{Type: FieldText, Max: f64(5)}, "hello!", "at most 5"},
		{FieldSpec{Type: FieldText}, "a\nb", "control"},
		{FieldSpec{Type: FieldText}, "a\u202eb", "bidi"},
		{FieldSpec{Type: FieldText}, 3, "must be text"},
		{FieldSpec{Type: FieldNumber, Min: f64(1)}, float64(0), "at least 1"},
		{FieldSpec{Type: FieldNumber}, "3", "must be a number"},
		{FieldSpec{Type: FieldBool}, "true", "true or false"},
		{FieldSpec{Type: FieldEnum, Values: []string{"a", "b"}}, "c", "one of a, b"},
		{FieldSpec{Type: FieldColor}, "red", "colour"},
		{FieldSpec{Type: FieldList, Max: f64(1)}, []any{"x", "y"}, "at most 1"},
		{FieldSpec{Type: FieldList}, []any{1}, "one line of text"},
	}
	for _, c := range bad {
		_, err := CheckFieldValue(c.f, c.v)
		if err == nil {
			t.Errorf("%s %v accepted", c.f.Type, c.v)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %q does not say %q", c.f.Type, c.v, err, c.want)
		}
	}
	if v, _ := CheckFieldValue(FieldSpec{Type: FieldColor}, "#AABBCC"); v != "#aabbcc" {
		t.Errorf("a colour is normalised to lower case, got %v", v)
	}
}

func TestDefaultValue(t *testing.T) {
	if v := DefaultValue(FieldSpec{Type: FieldEnum, Values: []string{"a", "b"}}); v != "a" {
		t.Errorf("enum default = %v", v)
	}
	if v := DefaultValue(FieldSpec{Type: FieldNumber, Min: f64(2)}); v != float64(2) {
		t.Errorf("number default = %v", v)
	}
	if v := DefaultValue(FieldSpec{Type: FieldBool, Default: true}); v != true {
		t.Errorf("bool default = %v", v)
	}
	// A default the schema itself refuses reads as the type's zero rather than
	// as the broken value.
	if v := DefaultValue(FieldSpec{Type: FieldColor, Default: "red"}); v != "#000000" {
		t.Errorf("bad default = %v", v)
	}
}
