package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/shareskill"
)

func TestPagePublishWithLinkAndPageLinkCLI(t *testing.T) {
	p := startCLIPanel(t)

	// Configure a dedicated share origin so we prove CLI links honor it.
	if err := p.db.SetSetting(t.Context(), "sharing.origin", "https://share.example.com"); err != nil {
		t.Fatal(err)
	}

	pageDir := filepath.Join(t.TempDir(), "page-preview")
	if out, err := runCLI(t, "page", "init", "--name", "preview", pageDir); err != nil {
		t.Fatalf("page init: %v\n%s", err, out)
	}
	// Make a standalone preview page with no live snapshot sections.
	if err := os.WriteFile(filepath.Join(pageDir, "vibepanel.json"), []byte(`{"sdk":1,"name":"preview","sections":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pageDir, "index.html"), []byte(`<!doctype html><title>Preview</title><h1>Ready</h1>`), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err := runCLI(t, "page", "check", pageDir); err != nil {
		t.Fatalf("page check: %v\n%s", err, out)
	}

	// Publish + mint link in one command.
	out, err := runCLI(t, "page", "publish", "--link", "--note", "initial", "--json", pageDir)
	if err != nil {
		t.Fatalf("page publish --link: %v\n%s", err, out)
	}
	var pub struct {
		Version int    `json:"version"`
		URL     string `json:"url"`
		Link    struct {
			Created bool `json:"created"`
			Reused  bool `json:"reused"`
		} `json:"link"`
	}
	if err := json.Unmarshal([]byte(out), &pub); err != nil {
		t.Fatalf("decode publish output %q: %v", out, err)
	}
	if pub.Version != 1 || !pub.Link.Created || !strings.HasPrefix(pub.URL, "https://share.example.com/share/") {
		t.Fatalf("unexpected publish --link response: %+v", pub)
	}

	// `page link` reuses the exact same URL.
	linkOut, err := runCLI(t, "page", "link", pageDir)
	if err != nil {
		t.Fatalf("page link: %v\n%s", err, linkOut)
	}
	if strings.TrimSpace(linkOut) != pub.URL {
		t.Fatalf("page link returned %q, want %q", strings.TrimSpace(linkOut), pub.URL)
	}

	// `page link --new --expires 7d` mints a second distinct link with an expiry.
	newOut, err := runCLI(t, "page", "link", "--new", "--expires", "7d", "--json", pageDir)
	if err != nil {
		t.Fatalf("page link --new: %v\n%s", err, newOut)
	}
	var created struct {
		URL       string `json:"url"`
		Created   bool   `json:"created"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal([]byte(newOut), &created); err != nil {
		t.Fatalf("decode link --new output %q: %v", newOut, err)
	}
	if !created.Created || created.URL == pub.URL || created.ExpiresAt == 0 {
		t.Fatalf("unexpected link --new response: %+v", created)
	}
}

func TestPageSkillCLIInstallStatusRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("DSH_HOME", "")
	t.Setenv("PATH", "")

	// Seed Claude Code and DeepSeek Harness config directories so detection sees those two.
	for _, d := range []string{filepath.Join(home, ".claude"), filepath.Join(home, ".dsh")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	names, err := runCLI(t, "page", "skill", "status", "--detected-names")
	if err != nil {
		t.Fatalf("status --detected-names: %v\n%s", err, names)
	}
	if !strings.Contains(names, "Claude Code") || !strings.Contains(names, "DeepSeek Harness") || strings.Contains(names, "Codex") {
		t.Fatalf("unexpected detected names: %q", names)
	}

	out, err := runCLI(t, "page", "skill", "install", "--agent", "detected", "--json")
	if err != nil {
		t.Fatalf("install --agent detected: %v\n%s", err, out)
	}
	var st shareskill.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	byID := map[shareskill.Agent]shareskill.AgentStatus{}
	for _, a := range st.Agents {
		byID[a.ID] = a
	}
	if !byID[shareskill.AgentClaude].Installed || !byID[shareskill.AgentDsh].Installed {
		t.Fatalf("expected claude and dsh installed: %+v", byID)
	}
	if byID[shareskill.AgentCodex].Installed || byID[shareskill.AgentAgy].Installed {
		t.Fatalf("codex and agy should remain uninstalled: %+v", byID)
	}

	if _, err := runCLI(t, "page", "skill", "remove", "--agent", "all"); err != nil {
		t.Fatalf("remove --agent all: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", shareskill.SkillName)); !os.IsNotExist(err) {
		t.Fatalf("expected claude bundle removed, stat err = %v", err)
	}
}
