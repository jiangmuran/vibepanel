package main

import (
	"path/filepath"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
	"github.com/jiangmuran/vibepanel/internal/chat/assistant"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/httpapi"
)

// newAssistant is how serve builds the advanced mode's brain from what the
// Chat page stored: the harness, the launch profile's environment and the
// per-process tools token. The working directory is the panel's own, under
// the data directory, so the agent never runs inside a project.
func newAssistant(cfg config.Config, _ *httpapi.Server) httpapi.AssistantBuilder {
	return func(ac httpapi.AssistantConfig, env []string, toolsToken string) (chat.Assistant, error) {
		return assistant.New(assistant.Config{
			Harness:    ac.Harness,
			Model:      ac.Model,
			Env:        env,
			MaxTurns:   ac.MaxTurns,
			BudgetUSD:  ac.BudgetUSD,
			WorkDir:    filepath.Join(cfg.DataDir, "assistant"),
			PanelURL:   cfg.LoopbackURL(),
			ToolsToken: toolsToken,
			Timeout:    time.Duration(ac.TimeoutSeconds) * time.Second,
		})
	}
}
