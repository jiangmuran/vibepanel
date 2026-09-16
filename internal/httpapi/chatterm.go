package httpapi

import (
	"context"

	"github.com/jiangmuran/vibepanel/internal/chat"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

// tmuxTerminal is the bridge's view of tmux: the four things a chat may do
// to a pane, and nothing else. The bridge never sees the tmux client itself,
// so the list of what a phone can do to a session is this file.
type tmuxTerminal struct{ c *tmux.Client }

// ChatTerminal wraps the panel's tmux client for the bridge.
func ChatTerminal(c *tmux.Client) chat.Terminal { return tmuxTerminal{c: c} }

func (t tmuxTerminal) Paste(ctx context.Context, name, text string) error {
	return t.c.Paste(ctx, name, text)
}

func (t tmuxTerminal) Keys(ctx context.Context, name string, keys ...string) error {
	return t.c.Keys(ctx, name, keys...)
}

func (t tmuxTerminal) Screen(ctx context.Context, name string, ansi bool) (string, error) {
	return t.c.Screen(ctx, name, ansi)
}

func (t tmuxTerminal) Fullscreen(ctx context.Context, name string) bool {
	return t.c.AlternateOn(ctx, name)
}
