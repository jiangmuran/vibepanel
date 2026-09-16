package feishu

import (
	"fmt"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/chat"
)

// How an Outbound becomes a 飞书 message.
//
// Cards are JSON 2.0: the current documented structure, the one the callback
// docs are written for, and the only one whose buttons carry `behaviors`.
// Two things about 2.0 are load-bearing and pinned by tests:
//
//   - config.update_multi must be true, in the card as sent and in every
//     patch, or the PATCH that turns "waiting" into "done" is refused.
//   - a button fires card.action.trigger through behaviors[].value. The
//     docs also show a bare top-level value on 2.0 buttons, but whether that
//     alone fires the callback is undocumented, so every button gets
//     behaviors and nothing relies on the bare form. The value is an object
//     because the SDKs accept only objects; the bridge's string rides in
//     its "v" key and the webhook reads it back out.

// render returns the msg_type and the serialised content for an Outbound.
func render(m chat.Outbound) (msgType, content string, err error) {
	switch {
	case m.Card != nil:
		return "interactive", jsonString(sessionCard(m.Card, m.Buttons)), nil
	case m.Code != "":
		// A text message shows a fence as three literal backticks; only a
		// card's markdown element draws monospace.
		return "interactive", jsonString(codeCard(m.Text, m.Code)), nil
	case m.Text != "":
		return "text", jsonString(map[string]string{"text": m.Text}), nil
	}
	return "", "", fmt.Errorf("feishu: nothing to send")
}

// template is the header colour for a state. Colour is never the only
// carrier (red line 4): the glyph and the state word are in the title too.
func template(state string) string {
	switch state {
	case "waiting":
		return "orange"
	case "working":
		return "blue"
	case "done":
		return "green"
	}
	return "grey"
}

// sessionCard lays out one session. The header carries what the shared
// renderer puts on its first line -- glyph, handle, title -- as plain text,
// and the body is the rest of chat.RenderMarkdown, so the escaping rules
// live in one place for every markdown IM.
func sessionCard(c *chat.Card, buttons []chat.Button) map[string]any {
	md := chat.RenderMarkdown(c)
	body := ""
	if i := strings.IndexByte(md, '\n'); i >= 0 {
		body = strings.TrimLeft(md[i+1:], "\n")
	}
	header := map[string]any{
		"title":    plainText(fmt.Sprintf("%s [%d] %s", c.Glyph, c.Handle, c.Title)),
		"template": template(c.State),
	}
	if c.Project != "" {
		header["subtitle"] = plainText(c.Project)
	}
	elements := []any{}
	if body != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": body})
	}
	if len(buttons) > 0 {
		elements = append(elements, buttonRow(buttons))
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"header": header,
		"body":   map[string]any{"direction": "vertical", "elements": elements},
	}
}

// codeCard is a heading line and a fenced block: a screen capture.
func codeCard(head, code string) map[string]any {
	fence := "```"
	for strings.Contains(code, fence) {
		fence += "`"
	}
	var b strings.Builder
	if head != "" {
		b.WriteString(head + "\n")
	}
	b.WriteString(fence + "\n" + code + "\n" + fence)
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"body": map[string]any{"direction": "vertical", "elements": []any{
			map[string]any{"tag": "markdown", "content": b.String()},
		}},
	}
}

// buttonRow lays buttons side by side. 2.0 has no action block; buttons sit
// in the body, and a column_set is what keeps three of them on one line.
func buttonRow(buttons []chat.Button) map[string]any {
	cols := make([]any, 0, len(buttons))
	primaryTaken := false
	for _, bt := range buttons {
		kind := "default"
		switch {
		case bt.Danger:
			kind = "danger"
		case !primaryTaken:
			kind = "primary"
			primaryTaken = true
		}
		cols = append(cols, map[string]any{
			"tag": "column", "width": "auto",
			"elements": []any{map[string]any{
				"tag":  "button",
				"type": kind,
				"size": "medium",
				"text": plainText(bt.Label),
				"behaviors": []any{map[string]any{
					"type":  "callback",
					"value": map[string]any{"v": bt.Value},
				}},
			}},
		})
	}
	return map[string]any{"tag": "column_set", "horizontal_spacing": "8px", "columns": cols}
}

func plainText(s string) map[string]any {
	return map[string]any{"tag": "plain_text", "content": s}
}
