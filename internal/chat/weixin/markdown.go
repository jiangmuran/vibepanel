package weixin

import (
	"regexp"
	"strings"
)

// What the WeChat client will not draw, removed before sending.
//
// There is no markdown message type; the client renders a plain text
// bubble and draws fenced code, inline code, tables, rules and **bold** in
// place. Three constructs it shows literally instead, and the official
// client strips exactly these (its markdown-filter, 2.1.3+): an image
// reference, which would be a URL in a bubble; a level-5 or level-6
// heading, which would be a row of hashes; and *italics* around CJK text,
// which would be asterisks around the words. Nothing else is touched,
// because everything else it draws, and a filter that guessed further
// would be rewriting what an agent said.

var (
	mdImage    = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdDeepHead = regexp.MustCompile(`(?m)^#{5,6}[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	// A single asterisk on each side of Han characters only; **bold** is
	// not matched because the character before the opening star must not
	// be one, and neither may the one after the closing star.
	mdCJKItalic = regexp.MustCompile(`(^|[^*])\*(\p{Han}+)\*($|[^*])`)
)

func filterMarkdown(s string) string {
	s = mdImage.ReplaceAllString(s, "")
	s = mdDeepHead.ReplaceAllString(s, "**$1**")
	// A global replace cannot see two adjacent italics through one match
	// (the closing context of one is the opening context of the next), so
	// it runs until nothing changes.
	for {
		next := mdCJKItalic.ReplaceAllString(s, "$1$2$3")
		if next == s {
			break
		}
		s = next
	}
	return strings.TrimRight(s, "\n ")
}
