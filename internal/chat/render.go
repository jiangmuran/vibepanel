package chat

import (
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf8"
)

// How a session looks in a chat.
//
// The bridge composes; the adapter draws. What is fixed here, for every IM,
// is the order: handle, then what the session is, then its state as a glyph
// and a word, then what it said. The handle is first because on 微信 it is
// the address a quoted reply is read back from, and a person scanning a chat
// at 2am reads the first four characters of each message and nothing else.

// Glyphs for the three states: the same shapes the sidebar uses, so a person
// who learnt them on the wall reads them on the phone (red line 4).
func glyph(state string) string {
	switch state {
	case "waiting":
		return "▲"
	case "working":
		return "●"
	case "done":
		return "✓"
	}
	return "○"
}

// BodyLimit is how much of a message a card carries. Past it the card says
// how much is left and "more" fetches the rest.
const BodyLimit = 1200

// cut shortens s to n runes, on a line boundary where one is near, and
// reports whether anything was dropped.
func cut(s string, n int) (string, bool) {
	if utf8.RuneCountInString(s) <= n {
		return s, false
	}
	rs := []rune(s)
	head := string(rs[:n])
	if i := strings.LastIndex(head, "\n"); i > n/2 {
		head = head[:i]
	}
	return strings.TrimRight(head, " \n"), true
}

// Split divides text into pieces no longer than max runes, preferring line
// boundaries, for adapters with a per-message limit. Zero max means one
// piece.
func Split(text string, max int) []string {
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return []string{text}
	}
	var out []string
	for utf8.RuneCountInString(text) > max {
		head, _ := cut(text, max)
		if head == "" {
			head = string([]rune(text)[:max])
		}
		out = append(out, head)
		text = strings.TrimLeft(text[len(head):], "\n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

// ago renders a duration the way a person says it.
func ago(d time.Duration, lang string) string {
	switch {
	case d < time.Minute:
		return pick(lang, "刚刚", "just now")
	case d < time.Hour:
		return fmt.Sprintf(pick(lang, "%d 分钟前", "%dm ago"), int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf(pick(lang, "%d 小时前", "%dh ago"), int(d.Hours()))
	}
	return fmt.Sprintf(pick(lang, "%d 天前", "%dd ago"), int(d.Hours()/24))
}

// RenderPlain draws a card as text, for adapters with no markup. Also the
// fallback every adapter may use, so a new one is a working one before it
// learns its IM's markup.
func RenderPlain(c *Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%d] %s", c.Glyph, c.Handle, c.Title)
	if c.Project != "" {
		fmt.Fprintf(&b, " · %s", c.Project)
	}
	b.WriteString("\n")
	b.WriteString(c.StateText)
	if c.Footer != "" {
		b.WriteString(" · " + c.Footer)
	}
	if c.Body != "" {
		b.WriteString("\n\n" + c.Body)
	}
	if c.URL != "" {
		b.WriteString("\n" + c.URL)
	}
	return b.String()
}

// RenderHTML draws a card in the HTML subset Telegram accepts: bold title,
// the body escaped and, where it looks like output, in a pre block.
func RenderHTML(c *Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>[%d] %s</b>", html.EscapeString(c.Glyph), c.Handle, html.EscapeString(c.Title))
	if c.Project != "" {
		fmt.Fprintf(&b, " · %s", html.EscapeString(c.Project))
	}
	b.WriteString("\n" + html.EscapeString(c.StateText))
	if c.Footer != "" {
		b.WriteString(" · " + html.EscapeString(c.Footer))
	}
	if c.Body != "" {
		b.WriteString("\n\n" + html.EscapeString(c.Body))
	}
	if c.URL != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s\">%s</a>", html.EscapeString(c.URL), html.EscapeString(c.URL))
	}
	return b.String()
}

// RenderMarkdown draws a card for a markdown body: 飞书's card element.
func RenderMarkdown(c *Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s [%d] %s**", c.Glyph, c.Handle, mdEscape(c.Title))
	if c.Project != "" {
		fmt.Fprintf(&b, " · %s", mdEscape(c.Project))
	}
	b.WriteString("\n" + mdEscape(c.StateText))
	if c.Footer != "" {
		b.WriteString(" · " + mdEscape(c.Footer))
	}
	if c.Body != "" {
		b.WriteString("\n\n" + mdEscape(c.Body))
	}
	if c.URL != "" {
		fmt.Fprintf(&b, "\n[%s](%s)", mdEscape(c.URL), c.URL)
	}
	return b.String()
}

// mdEscape keeps an agent's message from becoming card markup. Only the
// characters that open a construct are escaped; a message full of
// backslashes is worse than one with a stray asterisk.
func mdEscape(s string) string {
	r := strings.NewReplacer("*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>")
	return r.Replace(s)
}
