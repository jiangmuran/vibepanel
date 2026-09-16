package chat

import (
	"strings"
	"testing"
)

func TestEveryRenderingStartsWithTheGlyphAndHandle(t *testing.T) {
	c := &Card{Handle: 3, Title: "fix tmux <b>", Project: "vibepanel", State: "waiting",
		Glyph: "▲", StateText: "在等你", Body: "run *this*? `ok`", Footer: "2m", URL: "https://p/?session=x"}
	for name, r := range map[string]string{
		"plain":    RenderPlain(c),
		"html":     RenderHTML(c),
		"markdown": RenderMarkdown(c),
	} {
		if !strings.Contains(strings.SplitN(r, "\n", 2)[0], "[3]") || !strings.HasPrefix(strings.TrimLeft(r, "*"), "▲") {
			t.Errorf("%s does not lead with the glyph and handle:\n%s", name, r)
		}
		if !strings.Contains(r, "在等你") || !strings.Contains(r, "2m") {
			t.Errorf("%s lost the state or footer:\n%s", name, r)
		}
	}
	if !strings.Contains(RenderHTML(c), "&lt;b&gt;") {
		t.Error("html did not escape the title")
	}
	if !strings.Contains(RenderMarkdown(c), "\\*this\\*") {
		t.Error("markdown did not escape the body")
	}
	if !strings.Contains(RenderPlain(c), "fix tmux <b>") {
		t.Error("plain changed the title")
	}
}

func TestSplitPrefersLineBoundariesAndHandlesCJK(t *testing.T) {
	text := strings.Repeat("这是一行文字\n", 100)
	pieces := Split(text, 50)
	if len(pieces) < 10 {
		t.Fatalf("only %d pieces", len(pieces))
	}
	for _, p := range pieces {
		if n := len([]rune(p)); n > 50 {
			t.Fatalf("piece of %d runes", n)
		}
		if strings.Contains(p, "\n\n") || strings.HasPrefix(p, "\n") {
			t.Fatalf("piece with a stray newline: %q", p)
		}
	}
	if len(Split("short", 0)) != 1 || len(Split("short", 100)) != 1 {
		t.Fatal("short text was split")
	}
	// No newline anywhere: cut on the count.
	long := strings.Repeat("x", 120)
	if p := Split(long, 50); len(p) != 3 || len(p[0]) != 50 {
		t.Fatalf("hard split: %d pieces, first %d", len(p), len(p[0]))
	}
}

func TestEveryStringHasBothLanguages(t *testing.T) {
	for k, v := range strs {
		if strings.TrimSpace(v[0]) == "" || strings.TrimSpace(v[1]) == "" {
			t.Errorf("%s is missing a language", k)
		}
		if strings.Count(v[0], "%") != strings.Count(v[1], "%") {
			t.Errorf("%s: the two languages take different arguments", k)
		}
	}
	if msg("zh", "receipt", 3) != "→ [3] 已送入" || msg("en", "receipt", 3) != "→ [3] sent" {
		t.Fatal("msg does not render")
	}
	if msg("zh", "no-such-key") != "no-such-key" {
		t.Fatal("an unknown key should come back as itself")
	}
}
