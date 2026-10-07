package plugins

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// BaseTokens against the stylesheet, both ways.
//
// A token the stylesheet dropped is a theme that set it and now sets nothing,
// with no error anywhere: this is the test that makes that a decision. A
// token the stylesheet added is one a theme may not set until it is listed
// here, which is the one-line addition the comment on BaseTokens asks for.
func TestNothingAThemeCanNameWasRemoved(t *testing.T) {
	css, err := os.ReadFile("../../web/src/styles.css")
	if err != nil {
		t.Fatalf("read styles.css: %v", err)
	}
	src := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(css), "")
	i := strings.Index(src, "\n:root {")
	if i < 0 {
		t.Fatal("styles.css has no base :root block")
	}
	j := strings.Index(src[i:], "\n}")
	body := src[i : i+j]
	var inCSS []string
	for _, m := range regexp.MustCompile(`(?m)^\s*(--vp-[a-z0-9-]+)\s*:`).FindAllStringSubmatch(body, -1) {
		inCSS = append(inCSS, m[1])
	}
	slices.Sort(inCSS)
	want := append([]string(nil), BaseTokens...)
	slices.Sort(want)
	if !slices.Equal(inCSS, want) {
		t.Errorf("the base :root block and BaseTokens disagree.\n  styles.css: %v\n  BaseTokens: %v\n"+
			"A token removed from the stylesheet breaks every theme that sets it; one added is a line in theme.go.",
			inCSS, want)
	}
}

func TestATheme(t *testing.T) {
	good := `/* Paper */
:root[data-theme='ext-paper'] {
  color-scheme: light;
  --vp-bg: #f4f1ea;
  --vp-ink: rgb(20 20 20 / 0.9);
  --vp-accent: var(--vp-state-done);
  --vp-radius: 4px;
}`
	decls, err := LintTheme([]byte(good), "paper")
	if err != nil {
		t.Fatalf("a good theme was refused: %v", err)
	}
	if len(decls) != 5 || decls[1].Name != "--vp-bg" || decls[1].Value != "#f4f1ea" {
		t.Errorf("decls = %+v", decls)
	}
	out := RenderTheme("paper", decls)
	if !strings.HasPrefix(out, ":root[data-theme='ext-paper'] {") || !strings.Contains(out, "  --vp-bg: #f4f1ea;\n") {
		t.Errorf("rendered:\n%s", out)
	}
	if _, err := LintTheme([]byte(out), "paper"); err != nil {
		t.Errorf("the rendered theme does not pass its own lint: %v", err)
	}
}

// Each of these is a thing a stylesheet could do that a theme may not, and
// the error has to name it.
func TestAThemeIsRefusedFor(t *testing.T) {
	cases := []struct{ name, css, want string }{
		{"another plugin's id", `:root[data-theme='ext-other'] { --vp-bg: #fff; }`, "ext-other"},
		{"a second selector", `:root[data-theme='ext-p'] { --vp-bg: #fff; } .vp-control { color: red; }`, "one rule"},
		{"no selector", `--vp-bg: #fff;`, "one rule"},
		{"a nested rule", `:root[data-theme='ext-p'] { --vp-bg: #fff; .x { color: red } }`, "one rule"},
		{"an at-rule", `:root[data-theme='ext-p'] { --vp-bg: #fff; @import 'x'; }`, "at-rules"},
		{"a component property", `:root[data-theme='ext-p'] { background: #fff; }`, "not a --vp-* token"},
		{"an unknown token", `:root[data-theme='ext-p'] { --vp-nope: #fff; }`, "not a token the panel defines"},
		{"a url", `:root[data-theme='ext-p'] { --vp-bg: url(https://x.example/a.png); }`, "loads nothing"},
		{"an escape", `:root[data-theme='ext-p'] { --vp-bg: \66 ff; }`, "no escapes"},
		{"a string", `:root[data-theme='ext-p'] { --vp-bg: "red"; }`, "a value is a colour"},
		{"a semicolon smuggled in a value", `:root[data-theme='ext-p'] { --vp-bg: red; --vp-ink: <b>; }`, "a value is a colour"},
		{"a bad color-scheme", `:root[data-theme='ext-p'] { --vp-bg: #fff; color-scheme: auto; }`, "color-scheme is light or dark"},
		{"a token twice", `:root[data-theme='ext-p'] { --vp-bg: #fff; --vp-bg: #000; }`, "set twice"},
		{"nothing", `:root[data-theme='ext-p'] { }`, "sets no tokens"},
		{"too large", `:root[data-theme='ext-p'] { --vp-bg: #fff; /*` + strings.Repeat("x", MaxThemeByte) + `*/ }`, "at most"},
	}
	for _, tc := range cases {
		_, err := LintTheme([]byte(tc.css), "p")
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %q does not say %q", tc.name, err, tc.want)
		}
	}
}
