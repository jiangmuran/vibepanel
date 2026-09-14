package pages

import (
	"encoding/json"
	"strings"
	"testing"
)

func f(v float64) *float64 { return &v }

func TestParameterSpecsAreCheckedPerType(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    ParamSpec
		wantErr string
	}{
		{"text", ParamSpec{Key: "title", Type: ParamText, Max: f(40), Default: "Lobby"}, ""},
		{"color", ParamSpec{Key: "accent", Type: ParamColor, Default: "#4F7CFF"}, ""},
		{"number", ParamSpec{Key: "warnAt", Type: ParamNumber, Min: f(1), Max: f(10), Default: 5.0}, ""},
		{"enum", ParamSpec{Key: "unit", Type: ParamEnum, Values: []string{"tokens", "requests"}}, ""},
		{"bool", ParamSpec{Key: "compact", Type: ParamBool, Default: true}, ""},
		{"bad key", ParamSpec{Key: "1st", Type: ParamBool}, "key"},
		{"bad type", ParamSpec{Key: "x", Type: "date"}, "unknown type"},
		// A field that does not belong to the type is refused rather than
		// ignored: a values list on a text is a form its author thinks is a select.
		{"values on text", ParamSpec{Key: "x", Type: ParamText, Values: []string{"a"}}, "no values"},
		{"range on color", ParamSpec{Key: "x", Type: ParamColor, Max: f(2)}, "no min or max"},
		{"number without range", ParamSpec{Key: "x", Type: ParamNumber, Min: f(1)}, "both min and max"},
		{"inverted range", ParamSpec{Key: "x", Type: ParamNumber, Min: f(5), Max: f(1)}, "less than"},
		{"text max", ParamSpec{Key: "x", Type: ParamText, Max: f(MaxTextParam + 1)}, "max"},
		{"empty enum", ParamSpec{Key: "x", Type: ParamEnum}, "1 to"},
		{"default out of range", ParamSpec{Key: "x", Type: ParamNumber, Min: f(1), Max: f(2), Default: 9.0}, "default"},
		{"default not a colour", ParamSpec{Key: "x", Type: ParamColor, Default: "red"}, "default"},
		{"default not in enum", ParamSpec{Key: "x", Type: ParamEnum, Values: []string{"a"}, Default: "b"}, "default"},
		{"label", ParamSpec{Key: "x", Type: ParamBool, Label: strings.Repeat("l", MaxParamLabel+1)}, "label"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateParamSpecs([]ParamSpec{tc.spec})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
	dup := []ParamSpec{{Key: "a", Type: ParamBool}, {Key: "a", Type: ParamBool}}
	if err := validateParamSpecs(dup); err == nil {
		t.Error("a key listed twice was accepted")
	}
}

var specs = []ParamSpec{
	{Key: "title", Type: ParamText, Max: f(10), Default: "Lobby"},
	{Key: "accent", Type: ParamColor},
	{Key: "warnAt", Type: ParamNumber, Min: f(1), Max: f(10)},
	{Key: "unit", Type: ParamEnum, Values: []string{"tokens", "requests"}},
	{Key: "compact", Type: ParamBool},
}

func TestValuesFromTheFormAreStrict(t *testing.T) {
	if _, err := ValidateParamValues(specs, map[string]any{"nope": 1.0}); err == nil {
		t.Error("an undeclared key was accepted")
	}
	for k, v := range map[string]any{
		"title": strings.Repeat("x", 11), "accent": "blue", "warnAt": 11.0,
		"unit": "bytes", "compact": "yes",
	} {
		if _, err := ValidateParamValues(specs, map[string]any{k: v}); err == nil {
			t.Errorf("%s = %v was accepted", k, v)
		}
	}
	got, err := ValidateParamValues(specs, map[string]any{"accent": "#ABCDEF", "warnAt": 3.0})
	if err != nil {
		t.Fatal(err)
	}
	if got["accent"] != "#abcdef" {
		t.Errorf("colour not normalised: %v", got["accent"])
	}
}

// What a page receives is every declared key, always, with a stored value only
// where it still fits. A page republished with a narrower range must not be
// handed a value from the old one.
func TestAPageReceivesEveryParameterAndOnlyValuesThatFit(t *testing.T) {
	stored := map[string]any{"title": "Kitchen", "warnAt": 50.0, "unit": "requests", "stray": "x"}
	got := ResolveParams(specs, stored)
	want := map[string]any{"title": "Kitchen", "accent": "#000000", "warnAt": 1.0,
		"unit": "requests", "compact": false}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Errorf("resolved = %s, want %s", a, b)
	}
	if _, ok := got["stray"]; ok {
		t.Error("an undeclared stored key reached the page")
	}
}
