package httpapi

import (
	"context"

	"github.com/jiangmuran/vibepanel/internal/chat"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// tmuxTerminal is the bridge's view of tmux: the four things a chat may do
// to a pane, and nothing else. The bridge never sees the tmux client itself,
// so the list of what a phone can do to a session is this file.
type tmuxTerminal struct {
	c *tmux.Client
	// sent, if set, hears the name of every pane keys were sent to. See Keys.
	sent func(tmuxName string)
}

// ChatTerminal wraps the panel's tmux client for the bridge.
func ChatTerminal(c *tmux.Client) chat.Terminal { return tmuxTerminal{c: c} }

// chatTerminal is ChatTerminal with the keys it sends reported to the
// detector as input.
func (s *Server) chatTerminal() chat.Terminal {
	return tmuxTerminal{c: s.Tmux, sent: s.handleInputByName}
}

// handleInputByName is HandleInput for a caller that holds a tmux name.
func (s *Server) handleInputByName(name string) {
	if s.Manager == nil {
		return
	}
	for _, id := range s.Manager.LiveIDs() {
		if l, ok := s.Manager.Get(id); ok && l.TmuxName == name {
			s.HandleKey(id)
			return
		}
	}
}

func (t tmuxTerminal) Paste(ctx context.Context, name, text string) error {
	return t.c.Paste(ctx, name, text)
}

// Keys is every way a phone answers a session: allow, deny, submit, stop. Each
// is the person acting on what the session showed, so each counts as a key
// pressed in a browser does -- which is what releases a permission prompt that
// was answered from a phone, rather than leaving it on "waiting" until the
// approved tool finishes. Only as a key, not as a line: Detector.Input means a
// new turn to the legacy Codex rule, and "allow" is not one.
func (t tmuxTerminal) Keys(ctx context.Context, name string, keys ...string) error {
	err := t.c.Keys(ctx, name, keys...)
	if err == nil && t.sent != nil {
		t.sent(name)
	}
	return err
}

func (t tmuxTerminal) Screen(ctx context.Context, name string, ansi bool) (string, error) {
	return t.c.Screen(ctx, name, ansi)
}

func (t tmuxTerminal) Fullscreen(ctx context.Context, name string) bool {
	return t.c.AlternateOn(ctx, name)
}
