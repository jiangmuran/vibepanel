package browse

import "testing"

func TestSniffMarkup(t *testing.T) {
	const page = "<h1>hi</h1>"
	for _, tc := range []struct {
		name string
		head string
		want Markup
		mime string
	}{
		{"report.html", page, MarkupHTML, "text/html; charset=utf-8"},
		{"report.htm", page, MarkupHTML, "text/html; charset=utf-8"},
		{"page.xhtml", page, MarkupHTML, "text/html; charset=utf-8"},
		// Case in the extension, which a filesystem does not care about.
		{"REPORT.HTML", page, MarkupHTML, "text/html; charset=utf-8"},
		{"chart.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`, MarkupSVG, "image/svg+xml"},

		// Everything else stays text. This is the list that decides how much of
		// a project directory can ever be handed to a browser as a document.
		{"notes.txt", page, MarkupNone, ""},
		{"Makefile", page, MarkupNone, ""},
		{"index.html.tmpl", page, MarkupNone, ""},
		{"", page, MarkupNone, ""},
		// A leading dot is not an extension.
		{".html", page, MarkupNone, ""},

		// Named like a page and holding a binary. Serving this as text/html
		// would be the panel taking a filename's word for what bytes are.
		{"report.html", "\x00\x01\x02", MarkupNone, ""},
		{"chart.svg", "\xff\xfe\x00", MarkupNone, ""},
	} {
		got, mime := SniffMarkup(tc.name, []byte(tc.head))
		if got != tc.want || mime != tc.mime {
			t.Errorf("SniffMarkup(%q) = %q, %q; want %q, %q",
				tc.name, got, mime, tc.want, tc.mime)
		}
	}
}

// The media type is chosen here and never echoed from anywhere.
func TestSniffMarkupOnlyEverNamesTwoTypes(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{
		"a.html", "a.htm", "a.xhtml", "a.svg", "a.txt", "a.js", "a.json", "a.xml", "a.pdf",
	} {
		_, mime := SniffMarkup(name, []byte("<x/>"))
		if mime != "" {
			seen[mime] = true
		}
	}
	if len(seen) != 2 || !seen["text/html; charset=utf-8"] || !seen["image/svg+xml"] {
		t.Errorf("SniffMarkup can produce %v; the isolation is written for exactly two", seen)
	}
}
