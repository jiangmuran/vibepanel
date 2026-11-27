package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// A notice is a session speaking up on its own (docs/cli.md, `vibepanel me
// notify`): "I need a decision", with the choices as buttons. Unlike a state
// change it is delivered whatever the session's state and whatever the
// routes say about states -- the agent asked, explicitly -- but it still
// goes only to paired contacts, honours a contact's mute, and in quiet
// hours only an `ask` gets through, as a prompt does. A pressed choice is
// typed into the session like any reply, so the agent reads the answer
// from where it reads everything.

// Notice limits. Telegram's callback data is 64 bytes, and a choice rides
// in it beside the session id: twelve characters is three words, or four
// in Chinese, which is what a button should say.
const (
	NoticeMaxText    = 1000
	NoticeMaxChoices = 4
	NoticeMaxChoice  = 12
)

type Notice struct {
	// Level is "info", "warn" or "ask". Ask is not held by quiet hours.
	Level   string
	Text    string
	Choices []string
}

// CleanNotice bounds a notice: the text, the number and length of choices,
// and the level's vocabulary.
func CleanNotice(n Notice) Notice {
	switch n.Level {
	case "warn", "ask":
	default:
		n.Level = "info"
	}
	n.Text = strings.TrimSpace(n.Text)
	if r := []rune(n.Text); len(r) > NoticeMaxText {
		n.Text = string(r[:NoticeMaxText])
	}
	clean := []string{}
	for _, c := range n.Choices {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if r := []rune(c); len(r) > NoticeMaxChoice {
			c = string(r[:NoticeMaxChoice])
		}
		clean = append(clean, c)
		if len(clean) == NoticeMaxChoices {
			break
		}
	}
	n.Choices = clean
	if len(n.Choices) > 0 {
		n.Level = "ask"
	}
	return n
}

// Notify sends a notice to every paired contact the routes name and
// returns how many got it. A held notice (quiet hours, not an ask) is
// nobody; the web side has it regardless.
func (b *Bridge) Notify(ctx context.Context, row store.Session, n Notice) int {
	n = CleanNotice(n)
	if n.Text == "" || row.ArchivedAt != nil {
		return 0
	}
	raw, _ := b.d.DB.GetSetting(ctx, RoutesKey, "")
	d := ParseRoutes(raw).Decide(Change{SessionID: row.ID, ProjectID: row.ProjectID, State: string(row.State), Kind: store.MessageNotice}, b.d.Now().In(b.d.Zone()))
	if d.Hold && n.Level != "ask" {
		b.d.Audit(ctx, "chat.held", fmt.Sprintf("notice from %s held by quiet hours", row.Title))
		return 0
	}
	peers, err := b.d.DB.PairedChatPeers(ctx)
	if err != nil {
		return 0
	}
	handle, err := b.Handle(ctx, row.ID)
	if err != nil {
		return 0
	}
	project := ""
	if p, perr := b.d.DB.GetProject(ctx, row.ProjectID); perr == nil {
		project = p.Name
	}
	lang := b.language()
	card := &Card{Handle: handle, Title: row.Title, Project: project, State: string(row.State),
		Glyph: noticeGlyph(n.Level), StateText: noticeWord(lang, n.Level), Body: n.Text}
	sent := 0
	now := b.d.Now().Unix()
	for _, p := range peers {
		ch, ok := b.channel(p.Channel)
		if !ok {
			continue
		}
		// The routes' recipients when a rule names some; every contact when
		// the agent asked and no rule says who.
		if d.Send && !Destined(d.To, p.Channel, p.PeerID) {
			continue
		}
		if until, _ := b.d.DB.ChatMutedUntil(ctx, p.Channel, p.PeerID, row.ID, now); until > 0 {
			continue
		}
		if !ch.caps.Proactive && p.ContextToken == "" {
			b.undelivered(ctx, ch, p, row.ID, string(row.State), ErrNeedsHello)
			continue
		}
		out := Outbound{Card: card}
		if ch.caps.Buttons {
			for i, c := range n.Choices {
				// reply:<session>:0:<text> -- the verb, the session the button
				// belongs to, no bound message, and the words to type.
				// A channel with a callback budget gets the value trimmed to
				// it, words side only, at rune boundaries: Telegram rejects
				// the whole callback_data past 64 bytes and the failure is a
				// notice undelivered, not a shorter button -- and a cut
				// mid-rune would type mojibake into the session on press.
				value := "reply:" + row.ID + ":0:" + c
				if ch.caps.CallbackBytes > 0 && len(value) > ch.caps.CallbackBytes {
					for c != "" && len(value) > ch.caps.CallbackBytes {
						r := []rune(c)
						c = string(r[:len(r)-1])
						value = "reply:" + row.ID + ":0:" + c
					}
				}
				out.Buttons = append(out.Buttons, Button{Label: c, Value: value, Danger: i > 0 && n.Level == "warn"})
			}
		} else if len(n.Choices) > 0 {
			shown := *card
			shown.Hint = msg(lang, "hintPrompt", handle, handle)
			out.Card = &shown
		}
		ref, err := b.send(ctx, ch, p, out)
		if err != nil {
			b.undelivered(ctx, ch, p, row.ID, string(row.State), err)
			continue
		}
		_ = b.d.DB.RecordChatOutbound(ctx, store.ChatOutbound{Channel: p.Channel, PeerID: p.PeerID, Ref: ref, SessionID: row.ID, Kind: store.OutboundRequest})
		sent++
	}
	b.d.Audit(ctx, "chat.notice", fmt.Sprintf("[%d] %s: %s → %d contact(s)", handle, row.Title, preview(n.Text), sent))
	return sent
}

func noticeGlyph(level string) string {
	switch level {
	case "ask":
		return "❓"
	case "warn":
		return "⚠️"
	}
	return "💬"
}

func noticeWord(lang, level string) string {
	switch level {
	case "ask":
		return pick(lang, "在问你", "asks you")
	case "warn":
		return pick(lang, "提醒", "warns")
	}
	return pick(lang, "说", "says")
}

// replyChoice is the pressed choice of a notice: the words typed into the
// session, with the same gate and the same submit keys a typed reply gets.
func (b *Bridge) replyChoice(ctx context.Context, p store.ChatPeer, sessionID, text, lang string) said {
	text = strings.TrimSpace(text)
	if text == "" {
		return said{}
	}
	row, h, err := b.sessionAndHandle(ctx, sessionID)
	if err != nil {
		return plain(msg(lang, "gone", h))
	}
	return b.typeInto(ctx, p, row, h, HowButton, text, lang)
}
