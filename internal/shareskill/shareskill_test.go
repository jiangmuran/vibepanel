package shareskill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("DSH_HOME", "")
	t.Setenv("PATH", "")
	return home
}

func findAgent(t *testing.T, st Status, id Agent) AgentStatus {
	t.Helper()
	for _, a := range st.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("agent %q missing from status", id)
	return AgentStatus{}
}

func TestInstallInspectAndUninstallEveryAgent(t *testing.T) {
	home := setupHome(t)

	// Place sibling files in every agent's directory to prove neither install
	// nor uninstall ever touches them.
	siblings := []string{
		filepath.Join(home, ".claude", "skills", "other-skill", "SKILL.md"),
		filepath.Join(home, ".codex", "skills", "other-skill", "SKILL.md"),
		filepath.Join(home, ".gemini", "config", "plugins", "other-plugin", "plugin.json"),
		filepath.Join(home, ".dsh", "skills", "other-skill", "SKILL.md"),
	}
	for _, p := range siblings {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("keep me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	st, err := Inspect()
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if st.SkillName != SkillName {
		t.Fatalf("SkillName = %q, want %q", st.SkillName, SkillName)
	}
	for _, agent := range Agents {
		got := findAgent(t, st, agent)
		if got.Installed || got.Stale {
			t.Fatalf("agent %s should start uninstalled: %+v", agent, got)
		}
		if !got.Detected {
			t.Fatalf("agent %s should be detected when its config dir exists", agent)
		}
	}

	// Install all four.
	st, err = InstallMany(Agents)
	if err != nil {
		t.Fatalf("InstallMany: %v", err)
	}
	for _, agent := range Agents {
		got := findAgent(t, st, agent)
		if !got.Installed || got.Stale {
			t.Fatalf("agent %s should be installed and current: %+v", agent, got)
		}
	}

	// Verify exact expected layouts on disk.
	mustExist := []string{
		filepath.Join(home, ".claude", "skills", SkillName, "SKILL.md"),
		filepath.Join(home, ".claude", "skills", SkillName, ".claude-plugin", "plugin.json"),
		filepath.Join(home, ".codex", "skills", SkillName, "SKILL.md"),
		filepath.Join(home, ".gemini", "config", "plugins", SkillName, "plugin.json"),
		filepath.Join(home, ".gemini", "config", "plugins", SkillName, "assets", "logo.svg"),
		filepath.Join(home, ".gemini", "config", "plugins", SkillName, "skills", SkillName, "SKILL.md"),
		filepath.Join(home, ".dsh", "skills", SkillName, "SKILL.md"),
	}
	for _, p := range mustExist {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected installed file %s: %v", p, err)
		}
	}

	// Idempotent install must not rewrite files or move mtime.
	claudeSkill := filepath.Join(home, ".claude", "skills", SkillName, "SKILL.md")
	beforeInfo, err := os.Stat(claudeSkill)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(AgentClaude); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	afterInfo, err := os.Stat(claudeSkill)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatalf("idempotent install moved mtime: %v -> %v", beforeInfo.ModTime(), afterInfo.ModTime())
	}

	// Modifying one file marks that agent stale rather than installed.
	if err := os.WriteFile(claudeSkill, []byte("older copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = Inspect()
	if err != nil {
		t.Fatal(err)
	}
	gotClaude := findAgent(t, st, AgentClaude)
	if gotClaude.Installed || !gotClaude.Stale {
		t.Fatalf("modified copy should report stale=true installed=false: %+v", gotClaude)
	}
	// Re-installing brings it back to current.
	st, err = Install(AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	gotClaude = findAgent(t, st, AgentClaude)
	if !gotClaude.Installed || gotClaude.Stale {
		t.Fatalf("reinstall should restore current state: %+v", gotClaude)
	}

	// Uninstall all four and verify siblings are untouched.
	st, err = UninstallMany(Agents)
	if err != nil {
		t.Fatalf("UninstallMany: %v", err)
	}
	for _, agent := range Agents {
		got := findAgent(t, st, agent)
		if got.Installed || got.Stale {
			t.Fatalf("agent %s should be uninstalled: %+v", agent, got)
		}
	}
	for _, p := range siblings {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != "keep me\n" {
			t.Fatalf("sibling file %s was damaged: err=%v body=%q", p, err, string(b))
		}
	}
}

func TestEnvOverridesForCodexAndDsh(t *testing.T) {
	setupHome(t)
	customCodex := filepath.Join(t.TempDir(), "my-codex")
	customDsh := filepath.Join(t.TempDir(), "my-dsh")
	t.Setenv("CODEX_HOME", customCodex)
	t.Setenv("DSH_HOME", customDsh)

	st, err := InstallMany([]Agent{AgentCodex, AgentDsh})
	if err != nil {
		t.Fatalf("InstallMany: %v", err)
	}
	wantCodex := filepath.Join(customCodex, "skills", SkillName, "SKILL.md")
	wantDsh := filepath.Join(customDsh, "skills", SkillName, "SKILL.md")
	if got := findAgent(t, st, AgentCodex).Path; got != wantCodex {
		t.Fatalf("codex path = %q, want %q", got, wantCodex)
	}
	if got := findAgent(t, st, AgentDsh).Path; got != wantDsh {
		t.Fatalf("dsh path = %q, want %q", got, wantDsh)
	}
	if _, err := os.Stat(wantCodex); err != nil {
		t.Fatalf("codex skill missing at CODEX_HOME: %v", err)
	}
	if _, err := os.Stat(wantDsh); err != nil {
		t.Fatalf("dsh skill missing at DSH_HOME: %v", err)
	}
}

func TestEmbeddedManifestsAndFrontmatter(t *testing.T) {
	// SKILL.md must carry valid frontmatter with canonical kebab-case keys so
	// DeepSeek Harness's dsh-skill-filesystem parser never rejects it.
	raw := string(skillMarkdown)
	if !strings.HasPrefix(raw, "---\n") {
		t.Fatal("SKILL.md must begin with YAML frontmatter")
	}
	end := strings.Index(raw[4:], "\n---\n")
	if end < 0 {
		t.Fatal("SKILL.md frontmatter is not terminated")
	}
	fm := raw[4 : 4+end]
	if !strings.Contains(fm, "name: "+SkillName) {
		t.Fatalf("frontmatter missing name: %s", SkillName)
	}
	if !strings.Contains(fm, "description:") {
		t.Fatal("frontmatter missing description")
	}
	for _, legacy := range []string{"userInvocable:", "disableModelInvocation:", "modelInvocable:"} {
		if strings.Contains(fm, legacy) {
			t.Fatalf("frontmatter uses legacy camelCase key %q rejected by dsh-skill-filesystem", legacy)
		}
	}

	// Both plugin manifests must be valid JSON naming SkillName.
	for name, data := range map[string][]byte{
		"claude-plugin.json": claudePluginJSON,
		"agy-plugin.json":    agyPluginJSON,
	} {
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s invalid JSON: %v", name, err)
		}
		if doc["name"] != SkillName {
			t.Fatalf("%s name = %v, want %q", name, doc["name"], SkillName)
		}
	}
}

func TestRejectsUnknownAgent(t *testing.T) {
	setupHome(t)
	if _, ok := ParseAgent("unknown"); ok {
		t.Fatal("ParseAgent accepted unknown agent")
	}
	if _, err := Install(Agent("bogus")); err == nil {
		t.Fatal("Install accepted unknown agent")
	}
	if _, err := Uninstall(Agent("bogus")); err == nil {
		t.Fatal("Uninstall accepted unknown agent")
	}
}
