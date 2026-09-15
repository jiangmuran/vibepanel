package pages

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Lint is `vibepanel page check`: what is wrong with a page before anybody
// looks at it.
//
// Written for an agent to act on as much as for a person: every problem names
// a file and a line, says what, and says what to do instead. Most of what it
// reports would also fail in the browser -- the policy refuses the request --
// and the reason to say it here is that a refusal in a sandboxed frame is a
// console line nobody sees, while this is output in the terminal the agent is
// already reading.
//
// Errors are things the page's policy will refuse or the publish will reject.
// Warnings are things that work and are usually a mistake on a wall.

// Severity of a problem.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Problem is one finding.
type Problem struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
}

func (p Problem) String() string {
	loc := p.File
	if p.Line > 0 {
		loc = fmt.Sprintf("%s:%d", p.File, p.Line)
	}
	if loc == "" {
		loc = "page"
	}
	out := fmt.Sprintf("%s: %s: %s", loc, p.Severity, p.Message)
	if p.Fix != "" {
		out += "\n    fix: " + p.Fix
	}
	return out
}

// LintDir reads a directory and lints it. A directory that cannot be read as
// a page at all is one error, not a Go error: `check` reports it the same way
// as everything else.
func LintDir(root string) (Bundle, []Problem) {
	b, err := ReadDir(root)
	if err != nil {
		return Bundle{}, []Problem{{Severity: SeverityError, Code: "unreadable",
			Message: err.Error(), Fix: "see docs/share-pages.md for what a page directory holds"}}
	}
	return b, Lint(b)
}

type rule struct {
	code, severity string
	pattern        *regexp.Regexp
	message, fix   string
	// types limits a rule to some files, by content-type prefix.
	types []string
}

var scriptish = []string{"text/html", "text/javascript"}

var rules = []rule{
	{code: "html-sink", severity: SeverityWarning,
		pattern: regexp.MustCompile(`\.(innerHTML|outerHTML)\s*[+]?=|insertAdjacentHTML\s*\(|document\.write(ln)?\s*\(`),
		message: "writes HTML from a string; a session title is text somebody else chose",
		fix:     "use vp.text(el, value) or textContent, and build elements with createElement",
		types:   scriptish},
	{code: "storage", severity: SeverityError,
		pattern: regexp.MustCompile(`\b(localStorage|sessionStorage|indexedDB)\b`),
		message: "browser storage throws in a page: it runs in a sandbox with no origin",
		fix:     "use vp.storage, which keeps values for as long as the page is open",
		types:   scriptish},
	{code: "cookie", severity: SeverityError,
		pattern: regexp.MustCompile(`document\.cookie`),
		message: "document.cookie throws in a page: it runs in a sandbox with no origin",
		fix:     "keep state in vp.storage; a page has no cookies",
		types:   scriptish},
	{code: "eval", severity: SeverityError,
		pattern: regexp.MustCompile(`\beval\s*\(|new\s+Function\s*\(`),
		message: "eval is refused by the page's policy",
		fix:     "write the code out; libraries with a no-eval build need that build",
		types:   scriptish},
	{code: "modal", severity: SeverityWarning,
		pattern: regexp.MustCompile(`\b(alert|confirm|prompt)\s*\(`),
		message: "dialogs are blocked in a page, and nobody is standing at a wall to close one",
		fix:     "draw the message into the page",
		types:   scriptish},
	{code: "popup", severity: SeverityWarning,
		pattern: regexp.MustCompile(`window\.open\s*\(|target\s*=\s*["']?_blank`),
		message: "a page cannot open windows",
		fix:     "show the information in the page instead of linking out",
		types:   scriptish},
	{code: "form", severity: SeverityWarning,
		pattern: regexp.MustCompile(`(?i)<form\b`),
		message: "a form in a page cannot submit anywhere",
		fix:     "a page is read-only; use vp.storage for local toggles",
		types:   []string{"text/html"}},
}

var (
	// References that make the browser fetch something: attributes, CSS
	// url() and @import, ES imports, and the fetch family. Absolute URLs only;
	// a relative path is the page's own file.
	scriptSrc = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=\s*["']?((?:https?:)?//[^"'\s>]+)`)
	anyRef    = regexp.MustCompile(`(?i)(?:\b(?:src|href|poster|data|action)\s*=\s*["']?|url\(\s*["']?|@import\s+(?:url\(\s*)?["']?|\bimport\s*(?:[^'"]*?\bfrom\s*)?\(?\s*["']|\bfetch\s*\(\s*["'\x60]|new\s+(?:WebSocket|EventSource)\s*\(\s*["'\x60])((?:https?:|wss?:)?//[^"'\s)\x60>]+)`)
	anchorRef = regexp.MustCompile(`(?i)<a\b[^>]*\bhref\s*=\s*["']?(?:https?:)?//`)
	sdkRef    = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=\s*["']?(?:\./)?vibepanel\.js`)
)

// Lint checks the contents of a bundle that has already been read.
func Lint(b Bundle) []Problem {
	var out []Problem
	for _, f := range b.Files {
		if !isTextual(f.ContentType) {
			continue
		}
		src := string(f.Data)
		lines := lineIndex(src)
		for _, r := range rules {
			if !typeIn(f.ContentType, r.types) {
				continue
			}
			// An admin page may have forms: it is sandboxed with allow-forms,
			// and submitting through vp.admin is what it is for.
			if r.code == "form" && b.Manifest.Admin != nil && strings.HasPrefix(f.Path, b.Manifest.Admin.Dir()+"/") {
				continue
			}
			for _, loc := range r.pattern.FindAllStringIndex(src, -1) {
				out = append(out, Problem{File: f.Path, Line: lineOf(lines, loc[0]),
					Severity: r.severity, Code: r.code, Message: r.message, Fix: r.fix})
			}
		}
		out = append(out, lintReferences(b.Manifest, f, src, lines)...)
	}
	for _, f := range b.Files {
		if f.Path == IndexFile && !sdkRef.Match(f.Data) {
			out = append(out, Problem{File: f.Path, Severity: SeverityWarning, Code: "no-sdk",
				Message: "index.html does not load vibepanel.js, so it has no data",
				Fix:     `add <script src="vibepanel.js"></script> before your own script`})
		}
	}
	out = append(out, lintBackend(b)...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func lintReferences(m Manifest, f File, src string, lines []int) []Problem {
	var out []Problem
	scripts := map[int]bool{}
	if typeIn(f.ContentType, []string{"text/html"}) {
		for _, match := range scriptSrc.FindAllStringSubmatchIndex(src, -1) {
			scripts[match[2]] = true
			host := hostOf(src[match[2]:match[3]])
			if slices.Contains(m.ScriptHosts, host) {
				continue
			}
			p := Problem{File: f.Path, Line: lineOf(lines, match[0]), Severity: SeverityError,
				Code: "external-script"}
			if slices.Contains(ScriptHosts, host) {
				p.Message = fmt.Sprintf("a script from %s is refused until the manifest allows it", host)
				p.Fix = fmt.Sprintf(`add "%s" to "scriptHosts" in %s`, host, ManifestFile)
			} else {
				p.Message = fmt.Sprintf("a script from %s is refused; a page may load scripts only "+
					"from %s", host, strings.Join(ScriptHosts, " and "))
				p.Fix = "download the file into the page directory and load it by relative path"
			}
			out = append(out, p)
		}
		for _, loc := range anchorRef.FindAllStringIndex(src, -1) {
			out = append(out, Problem{File: f.Path, Line: lineOf(lines, loc[0]),
				Severity: SeverityWarning, Code: "external-link",
				Message: "a link to another site takes the screen away from the page when clicked",
				Fix:     "show the address as text, or leave the link off a wall"})
		}
	}
	for _, match := range anyRef.FindAllStringSubmatchIndex(src, -1) {
		if scripts[match[2]] {
			continue
		}
		// An <a href> is reported above as a link, not as a fetch.
		start := strings.LastIndex(src[:match[0]+1], "<")
		if start >= 0 && anchorRef.MatchString(src[start:match[3]]) {
			continue
		}
		out = append(out, Problem{File: f.Path, Line: lineOf(lines, match[0]),
			Severity: SeverityError, Code: "external-request",
			Message: fmt.Sprintf("a request to %s is refused; a page reaches the panel and nothing else",
				hostOf(src[match[2]:match[3]])),
			Fix: "put the file in the page directory (fonts and images included) and use a relative path"})
	}
	return out
}

func hostOf(ref string) string {
	if strings.HasPrefix(ref, "//") {
		ref = "https:" + ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return u.Hostname()
}

func isTextual(ct string) bool {
	return strings.HasPrefix(ct, "text/") || ct == "image/svg+xml"
}

func typeIn(ct string, prefixes []string) bool {
	if prefixes == nil {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}

func lineIndex(src string) []int {
	idx := []int{0}
	for i, c := range src {
		if c == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func lineOf(idx []int, offset int) int {
	return sort.Search(len(idx), func(i int) bool { return idx[i] > offset })
}

var serverForbidden = regexp.MustCompile(`\b(require|import|fetch|XMLHttpRequest|setTimeout|setInterval)\s*\(`)

// lintBackend checks what a manifest's backend points at. docs/page-backend.md.
func lintBackend(b Bundle) []Problem {
	m := b.Manifest
	var out []Problem
	if m.Admin != nil && !b.Has(m.Admin.Entry) {
		out = append(out, Problem{File: ManifestFile, Severity: SeverityError, Code: "admin-missing",
			Message: "admin.entry names " + m.Admin.Entry + ", which is not in the page",
			Fix:     "create it, or remove admin from vibepanel.json and use the form settings draws from data"})
	}
	if m.Server != nil {
		f, ok := fileNamed(b, m.Server.Entry)
		if !ok {
			out = append(out, Problem{File: ManifestFile, Severity: SeverityError, Code: "server-missing",
				Message: "server.entry names " + m.Server.Entry + ", which is not in the page",
				Fix:     "create it, or remove server from vibepanel.json"})
		} else {
			src := string(f.Data)
			lines := lineIndex(src)
			for _, loc := range serverForbidden.FindAllStringIndex(src, -1) {
				out = append(out, Problem{File: f.Path, Line: lineOf(lines, loc[0]), Severity: SeverityError,
					Code: "server-api", Message: "server.js has no modules, network or timers",
					Fix: "use sources for data from the network, and onSchedule for anything periodic"})
			}
		}
	}
	for _, src := range m.Sources {
		out = append(out, Problem{File: ManifestFile, Severity: SeverityWarning, Code: "source-approval",
			Message: "source " + src.Key + " fetches nothing until the owner approves its host in settings",
			Fix:     "ask the owner to approve " + hostOf(src.URL) + " under the page's Sources"})
	}
	return out
}

func fileNamed(b Bundle, rel string) (File, bool) {
	for _, f := range b.Files {
		if f.Path == rel {
			return f, true
		}
	}
	return File{}, false
}
