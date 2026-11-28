package httpapi

import (
	"context"
	"html"
	"net/url"
	"path"
	"strings"
	"unicode"

	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// defaultShareFaviconHref is the panel's own mark (web/public/icon.svg) as a
// compact inline SVG data URI. Served on the password gate, the gone page, and
// unlocked share shells whose page does not declare a custom icon, so browsers
// never fall back to a blank globe or trigger a 404/CSP error on /favicon.ico.
const defaultShareFaviconHref = "data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20viewBox='0%200%2064%2064'%3E%3Crect%20width='64'%20height='64'%20rx='14'%20fill='%230A84FF'/%3E%3Cg%20fill='%23fff'%3E%3Crect%20x='13'%20y='17'%20width='26'%20height='6'%20rx='3'/%3E%3Crect%20x='13'%20y='29'%20width='34'%20height='6'%20rx='3'/%3E%3Crect%20x='13'%20y='41'%20width='20'%20height='6'%20rx='3'/%3E%3C/g%3E%3Cpath%20d='M50%2015L56%2025.5H44Z'%20fill='%23FF9F0A'/%3E%3C/svg%3E"

const (
	maxShareHeadScanBytes = 64 * 1024
	maxShareShellTitleLen = 160
	maxShareIconDataBytes = 32 * 1024
)

type shareShellMeta struct {
	Title    string
	IconHref string
}

// resolveShareShellMeta extracts the published page's <title> and <link rel="icon">
// for the outer unlock shell so browser tabs display the actual page's identity
// instead of a generic placeholder. Called only after the visitor has unlocked
// the link, never on the password gate.
func (s *Server) resolveShareShellMeta(ctx context.Context, link store.ShareLink, grant string, nowUnix int64) shareShellMeta {
	out := shareShellMeta{
		Title:    "vibepanel",
		IconHref: defaultShareFaviconHref,
	}
	var pageName string
	var manifest pages.Manifest
	var rawTitle, rawIcon string

	if page, err := s.DB.SharePageByID(ctx, link.PageID); err == nil {
		pageName = page.Name
		verNum := link.ResolvePageVersion(page.PublishedVersion, nowUnix)
		if verNum > 0 {
			if v, verr := s.DB.SharePageVersionByNumber(ctx, page.ID, verNum); verr == nil {
				manifest = pages.DecodeStored(v.Manifest)
			}
			if _, data, ferr := s.DB.SharePageFileData(ctx, page.ID, verNum, pages.IndexFile); ferr == nil && len(data) > 0 {
				rawTitle, rawIcon = extractShareHeadMeta(data)
			}
		}
	}

	switch {
	case rawTitle != "" && !strings.EqualFold(rawTitle, "vibepanel"):
		out.Title = rawTitle
	case cleanShellTitle(pageName) != "":
		out.Title = cleanShellTitle(pageName)
	case cleanShellTitle(link.Name) != "":
		out.Title = cleanShellTitle(link.Name)
	case rawTitle != "":
		out.Title = rawTitle
	}

	if icon := resolveShellIconHref(rawIcon, grant, manifest.Private); icon != "" {
		out.IconHref = icon
	}
	return out
}

// extractShareHeadMeta scans the <head> portion of an HTML document (capped at
// maxShareHeadScanBytes) for the first non-empty <title> and first non-empty
// <link rel="...icon..."> href. Comments, <script>, and <style> blocks are
// skipped so strings inside them cannot spoof metadata, and scanning stops at
// </head> or <body>.
func extractShareHeadMeta(doc []byte) (title, iconHref string) {
	if len(doc) > maxShareHeadScanBytes {
		doc = doc[:maxShareHeadScanBytes]
	}
	s := string(doc)
	n := len(s)
	for i := 0; i < n; {
		lt := strings.IndexByte(s[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if i+1 >= n {
			break
		}
		// Skip <!doctype ...> or processing instructions.
		if s[i+1] == '!' || s[i+1] == '?' {
			gt := strings.IndexByte(s[i+2:], '>')
			if gt < 0 {
				break
			}
			i += 2 + gt + 1
			continue
		}

		closing := false
		j := i + 1
		if s[j] == '/' {
			closing = true
			j++
		}
		nameStart := j
		for j < n && isASCIIAlphaNum(s[j]) {
			j++
		}
		if j == nameStart {
			i++
			continue
		}
		tag := strings.ToLower(s[nameStart:j])
		tagEnd := findHTMLTagEnd(s, j)
		if tagEnd < 0 {
			break
		}
		attrSlice := s[j:tagEnd]
		next := tagEnd + 1

		if closing {
			if tag == "head" {
				break
			}
			i = next
			continue
		}
		if tag == "body" {
			break
		}
		if tag == "script" || tag == "style" {
			endIdx := indexCaseInsensitive(s[next:], "</"+tag)
			if endIdx < 0 {
				break
			}
			closeEnd := findHTMLTagEnd(s, next+endIdx+2+len(tag))
			if closeEnd < 0 {
				break
			}
			i = closeEnd + 1
			continue
		}
		if tag == "title" {
			endIdx := indexCaseInsensitive(s[next:], "</title")
			if endIdx < 0 {
				break
			}
			if title == "" {
				title = cleanShellTitle(html.UnescapeString(s[next : next+endIdx]))
			}
			closeEnd := findHTMLTagEnd(s, next+endIdx+len("</title"))
			if closeEnd < 0 {
				break
			}
			i = closeEnd + 1
			if title != "" && iconHref != "" {
				break
			}
			continue
		}
		if tag == "link" && iconHref == "" {
			rel, href := parseLinkAttrs(attrSlice)
			if isIconRel(rel) {
				cand := strings.TrimSpace(html.UnescapeString(href))
				if cand != "" && !isEmptyDataURI(cand) {
					iconHref = cand
				}
			}
			if title != "" && iconHref != "" {
				break
			}
		}
		i = next
	}
	return title, iconHref
}

func isASCIIAlphaNum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isHTMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

// findHTMLTagEnd returns the index of the closing '>' starting from pos,
// ignoring '>' inside single- or double-quoted attribute values.
func findHTMLTagEnd(s string, pos int) int {
	var quote byte
	for i := pos; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '>' {
			return i
		}
	}
	return -1
}

func indexCaseInsensitive(haystack, needleLower string) int {
	n := len(needleLower)
	if n == 0 || len(haystack) < n {
		return -1
	}
	firstLower := needleLower[0]
	firstUpper := firstLower
	if firstLower >= 'a' && firstLower <= 'z' {
		firstUpper = firstLower - 'a' + 'A'
	}
	limit := len(haystack) - n
	for i := 0; i <= limit; i++ {
		b := haystack[i]
		if b != firstLower && b != firstUpper {
			continue
		}
		if strings.EqualFold(haystack[i:i+n], needleLower) {
			return i
		}
	}
	return -1
}

func parseLinkAttrs(attrs string) (rel, href string) {
	n := len(attrs)
	for i := 0; i < n; {
		for i < n && (isHTMLSpace(attrs[i]) || attrs[i] == '/') {
			i++
		}
		if i >= n {
			break
		}
		nameStart := i
		for i < n && !isHTMLSpace(attrs[i]) && attrs[i] != '=' && attrs[i] != '/' {
			i++
		}
		name := strings.ToLower(attrs[nameStart:i])
		for i < n && isHTMLSpace(attrs[i]) {
			i++
		}
		var val string
		if i < n && attrs[i] == '=' {
			i++
			for i < n && isHTMLSpace(attrs[i]) {
				i++
			}
			if i < n && (attrs[i] == '"' || attrs[i] == '\'') {
				q := attrs[i]
				i++
				valStart := i
				for i < n && attrs[i] != q {
					i++
				}
				val = attrs[valStart:i]
				if i < n {
					i++
				}
			} else {
				valStart := i
				for i < n && !isHTMLSpace(attrs[i]) {
					i++
				}
				val = attrs[valStart:i]
			}
		}
		switch name {
		case "rel":
			rel = val
		case "href":
			href = val
		}
	}
	return rel, href
}

func isIconRel(rel string) bool {
	for _, tok := range strings.Fields(strings.ToLower(rel)) {
		if tok == "icon" {
			return true
		}
	}
	return false
}

func isEmptyDataURI(href string) bool {
	if len(href) < 5 || !strings.EqualFold(href[:5], "data:") {
		return false
	}
	comma := strings.IndexByte(href, ',')
	if comma < 0 {
		return true
	}
	return strings.TrimSpace(href[comma+1:]) == ""
}

// cleanShellTitle normalizes whitespace and strips control and bidi override
// characters so a page title cannot spoof or break the outer document.
func cleanShellTitle(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	lastSpace := true
	runes := 0
	for _, r := range raw {
		if isUnsafeTitleRune(r) {
			continue
		}
		if unicode.IsSpace(r) {
			if !lastSpace && runes < maxShareShellTitleLen {
				b.WriteByte(' ')
				lastSpace = true
				runes++
			}
			continue
		}
		if runes >= maxShareShellTitleLen {
			break
		}
		b.WriteRune(r)
		lastSpace = false
		runes++
	}
	return strings.TrimSpace(b.String())
}

func isUnsafeTitleRune(r rune) bool {
	if unicode.IsControl(r) {
		return true
	}
	switch {
	case r == 0x00AD || r == 0x061C || r == 0x200B || r == 0x200E || r == 0x200F || r == 0xFEFF:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// resolveShellIconHref validates a <link rel="icon"> href from a page's
// index.html and returns a safe href for the outer unlock shell, or "" when
// the icon is empty, external, invalid, or private.
func resolveShellIconHref(rawHref, grant string, privateFn func(string) bool) string {
	href := strings.TrimSpace(rawHref)
	if href == "" || len(href) > maxShareIconDataBytes {
		return ""
	}
	for i := 0; i < len(href); i++ {
		if href[i] < 0x20 || href[i] == 0x7f {
			return ""
		}
	}
	if len(href) >= 5 && strings.EqualFold(href[:5], "data:") {
		// The payload is copied into the shell's <link href="…">, and the shell
		// is the one document here that is not sandboxed and runs inline
		// script on the panel's origin. Escaping on the way out is the first
		// guard; this is the second, so that a later edit to the template that
		// drops the escape is not a page's HTML running as its owner. A real
		// data URI percent-encodes these. `'` and spaces stay: hand-written
		// SVG icons use both (the templates' own uses `'`), and neither ends a
		// double-quoted attribute.
		if strings.ContainsAny(href, "\"<>") {
			return ""
		}
		comma := strings.IndexByte(href, ',')
		if comma < 0 || strings.TrimSpace(href[comma+1:]) == "" {
			return ""
		}
		meta := href[5:comma]
		mime := meta
		if semi := strings.IndexByte(meta, ';'); semi >= 0 {
			mime = meta[:semi]
		}
		switch strings.ToLower(strings.TrimSpace(mime)) {
		case "image/svg+xml", "image/png", "image/webp",
			"image/x-icon", "image/vnd.microsoft.icon",
			"image/gif", "image/jpeg":
			return href
		default:
			return ""
		}
	}
	// Refuse absolute URLs, protocol-relative URLs, root paths, and other schemes
	// so the outer shell never reaches outside this grant's published bundle.
	if strings.HasPrefix(href, "/") || strings.HasPrefix(href, "\\") {
		return ""
	}
	if colon := strings.IndexByte(href, ':'); colon >= 0 {
		slash := strings.IndexByte(href, '/')
		if slash < 0 || colon < slash {
			return ""
		}
	}
	rel := href
	if idx := strings.IndexAny(rel, "?#"); idx >= 0 {
		rel = rel[:idx]
	}
	rel = strings.TrimPrefix(rel, "./")
	clean := path.Clean(rel)
	if !pages.ValidPath(clean) {
		return ""
	}
	if privateFn != nil && privateFn(clean) {
		return ""
	}
	switch strings.ToLower(path.Ext(clean)) {
	case ".svg", ".png", ".ico", ".webp", ".gif", ".jpg", ".jpeg":
	default:
		return ""
	}
	if grant == "" {
		return ""
	}
	parts := strings.Split(clean, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/share-u/" + grant + "/" + strings.Join(parts, "/")
}
