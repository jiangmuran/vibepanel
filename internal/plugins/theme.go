package plugins

import (
	"fmt"
	"regexp"
	"strings"
)

// Rung 0: a theme is a stylesheet of tokens. docs/plugins.md §5.
//
// The file may contain one rule, `:root[data-theme='ext-<id>'] { ... }`, and
// inside it only `--vp-*` tokens the base `:root` defines, plus
// `color-scheme`. No other selector, no `url()`, no `@import`, no escape.
// styles.test.ts already states that rule for the panel's own theme blocks
// (red line 5: theme blocks redefine tokens and never component styles); this
// states it for a plugin's and refuses the file otherwise, at publish and at
// install.
//
// Two things the bound buys. A theme is the rung anybody may install from
// anyone, and that is only true because a file that passes this cannot do
// anything but recolour: no network (url), no selector that reaches a
// component, no value a browser could read as something other than a colour
// or a length. And a theme written against the token names keeps matching the
// panel when the panel's components change, because the components are what
// read the tokens.

// BaseTokens is every token the panel's base `:root` defines, which is what a
// theme may redefine. A committed list rather than a read of styles.css,
// pinned both ways by a test: a token removed from the stylesheet is a red
// test and a decision, because every theme that set it would stop working; a
// token added is a one-line addition here.
var BaseTokens = []string{
	"--vp-bg", "--vp-surface", "--vp-surface-2", "--vp-elevated", "--vp-terminal-bg",
	"--vp-hairline", "--vp-hairline-strong",
	"--vp-ink", "--vp-ink-2", "--vp-ink-3",
	"--vp-accent", "--vp-accent-ink", "--vp-selection",
	"--vp-state-waiting", "--vp-state-working", "--vp-state-done", "--vp-state-dead", "--vp-state-crashed",
	"--vp-danger-ink",
	"--vp-term-fg", "--vp-term-cursor",
	"--vp-term-black", "--vp-term-red", "--vp-term-green", "--vp-term-yellow",
	"--vp-term-blue", "--vp-term-magenta", "--vp-term-cyan", "--vp-term-white",
	"--vp-term-bright-black", "--vp-term-bright-red", "--vp-term-bright-green", "--vp-term-bright-yellow",
	"--vp-term-bright-blue", "--vp-term-bright-magenta", "--vp-term-bright-cyan", "--vp-term-bright-white",
	"--vp-ease", "--vp-radius", "--vp-radius-lg", "--vp-control-h", "--vp-radius-control", "--vp-chrome-h",
}

var (
	cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	themeRule  = regexp.MustCompile(`(?s)^\s*:root\[data-theme=(['"])ext-([a-z0-9-]+)['"]\]\s*\{(.*)\}\s*$`)
	// A value is a colour, a length, a timing function or a var() of another
	// token: letters, digits, the punctuation those need, and nothing a
	// browser could read as a URL, an escape, a string or another rule.
	themeValue = regexp.MustCompile(`^[A-Za-z0-9#%.,()/\s+*-]{1,200}$`)
	tokenName  = regexp.MustCompile(`^--vp-[a-z0-9-]+$`)
)

// ThemeAttr is the data-theme value a plugin's theme is applied with.
func ThemeAttr(id string) string { return "ext-" + id }

// LintTheme checks a theme file for plugin id, and returns the declarations
// it would apply, in order.
func LintTheme(css []byte, id string) ([]Declaration, error) {
	if len(css) > MaxThemeByte {
		return nil, fmt.Errorf("a theme is at most %d KiB", MaxThemeByte>>10)
	}
	src := string(css)
	if strings.ContainsRune(src, 0) || strings.Contains(src, "\\") {
		return nil, fmt.Errorf("a theme has no escapes")
	}
	src = cssComment.ReplaceAllString(src, "")
	mm := themeRule.FindStringSubmatch(src)
	if mm == nil {
		return nil, fmt.Errorf("a theme is one rule, :root[data-theme='%s'] { --vp-...: ...; }, and nothing else", ThemeAttr(id))
	}
	if mm[2] != id {
		return nil, fmt.Errorf("the rule is for ext-%s; this plugin is %s", mm[2], id)
	}
	body := mm[3]
	if strings.ContainsAny(body, "{}") {
		return nil, fmt.Errorf("a theme is one rule, :root[data-theme='%s'] { --vp-...: ...; }, and nothing else", ThemeAttr(id))
	}
	if strings.Contains(body, "@") {
		return nil, fmt.Errorf("a theme has no at-rules")
	}
	var out []Declaration
	seen := map[string]bool{}
	for _, decl := range strings.Split(body, ";") {
		decl = strings.TrimSpace(decl)
		if decl == "" {
			continue
		}
		name, value, ok := strings.Cut(decl, ":")
		if !ok {
			return nil, fmt.Errorf("%q is not a declaration", decl)
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		switch {
		case name == "color-scheme":
			if value != "light" && value != "dark" {
				return nil, fmt.Errorf("color-scheme is light or dark, not %q", value)
			}
		case tokenName.MatchString(name):
			if !contains(BaseTokens, name) {
				return nil, fmt.Errorf("%s is not a token the panel defines; see BaseTokens in docs/plugins.md", name)
			}
		default:
			return nil, fmt.Errorf("%s is not a --vp-* token; a theme sets tokens and nothing else", name)
		}
		lower := strings.ToLower(value)
		if strings.Contains(lower, "url") || strings.Contains(lower, "expression") || strings.Contains(lower, "image") {
			return nil, fmt.Errorf("%s: a theme value loads nothing", name)
		}
		if !themeValue.MatchString(value) {
			return nil, fmt.Errorf("%s: a value is a colour, a length or var(--vp-...), not %q", name, value)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s is set twice", name)
		}
		seen[name] = true
		out = append(out, Declaration{Name: name, Value: value})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the theme sets no tokens")
	}
	return out, nil
}

// Declaration is one token a theme sets.
type Declaration struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// RenderTheme writes a linted theme back out as the one rule the panel
// serves, from the declarations rather than the file: what is served is what
// was checked, byte for byte.
func RenderTheme(id string, decls []Declaration) string {
	var b strings.Builder
	b.WriteString(":root[data-theme='" + ThemeAttr(id) + "'] {\n")
	for _, d := range decls {
		b.WriteString("  " + d.Name + ": " + d.Value + ";\n")
	}
	b.WriteString("}\n")
	return b.String()
}
