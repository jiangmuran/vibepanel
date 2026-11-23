// Package shareskill manages the optional vibepanel-share agent skill/plugin
// bundle for coding agents (Claude Code, Codex, Antigravity CLI, and DeepSeek
// Harness).
//
// Installing is always opt-in: each agent owns one dedicated subdirectory
// named after SkillName so install never edits a user's settings.json,
// config.toml, hooks.json, skills.json, or cordis.patch.yml, and uninstall
// deletes only that directory.
package shareskill

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// SkillName is the directory and slash-command name installed into each agent.
// Every installer owns only a leaf directory with this exact name so removing
// it can never touch a sibling skill or plugin.
const SkillName = "vibepanel-share"

//go:embed SKILL.md
var skillMarkdown []byte

//go:embed claude-plugin.json
var claudePluginJSON []byte

//go:embed agy-plugin.json
var agyPluginJSON []byte

//go:embed logo.svg
var agyLogoSVG []byte

// Agent identifies one coding-agent harness supported by this package.
type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
	AgentAgy    Agent = "agy"
	AgentDsh    Agent = "dsh"
)

// Agents is every supported harness, in the stable order shown in the UI and CLI.
var Agents = []Agent{AgentClaude, AgentCodex, AgentAgy, AgentDsh}

// AgentStatus describes one harness's install state on disk.
type AgentStatus struct {
	ID   Agent  `json:"id"`
	Name string `json:"name"`
	// Path is the directory or primary file written for this harness.
	Path string `json:"path"`
	// Kind is "plugin" for harnesses that load a manifest-backed bundle
	// (Claude Code, Antigravity) and "skill" for plain SKILL.md directories
	// (Codex, DeepSeek Harness).
	Kind string `json:"kind"`
	// Installed is true only when every file in the bundle is present and
	// byte-identical to this build's embedded copy.
	Installed bool `json:"installed"`
	// Stale is true when a file from an older build is on disk and differs
	// from this build's copy.
	Stale bool `json:"stale"`
	// Detected says the agent's binary or home configuration directory exists
	// on this machine. Advisory only: a user may still install for an agent
	// whose binary is outside the panel's PATH.
	Detected bool `json:"detected"`
}

// Status is what GET/POST/DELETE /api/settings/share-skill and the CLI return.
type Status struct {
	SkillName string        `json:"skillName"`
	Content   string        `json:"content"`
	Agents    []AgentStatus `json:"agents"`
}

// editMu serializes writes from concurrent settings clicks or CLI calls.
var editMu sync.Mutex

// ParseAgent validates a user-supplied agent id.
func ParseAgent(raw string) (Agent, bool) {
	a := Agent(strings.ToLower(strings.TrimSpace(raw)))
	if slices.Contains(Agents, a) {
		return a, true
	}
	return "", false
}

// Content returns the embedded SKILL.md text shown in the settings preview.
func Content() string {
	return string(skillMarkdown)
}

type targetSpec struct {
	id         Agent
	name       string
	kind       string
	parentDir  string // parent directory (must end in "skills" or "plugins")
	bundleDir  string // <parentDir>/<SkillName>
	primary    string // path shown in the UI
	files      map[string][]byte
	detectDirs []string
	detectBin  string
}

func targetFor(agent Agent) (targetSpec, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return targetSpec{}, fmt.Errorf("shareskill: home directory: %w", err)
	}
	switch agent {
	case AgentClaude:
		root := filepath.Join(home, ".claude")
		parent := filepath.Join(root, "skills")
		dir := filepath.Join(parent, SkillName)
		// Claude Code auto-loads ~/.claude/skills/<name>/ both as a user skill
		// and as a skills-dir plugin when .claude-plugin/plugin.json is present,
		// without modifying ~/.claude/settings.json.
		return targetSpec{
			id:        AgentClaude,
			name:      "Claude Code",
			kind:      "plugin",
			parentDir: parent,
			bundleDir: dir,
			primary:   dir,
			files: map[string][]byte{
				"SKILL.md":                   skillMarkdown,
				".claude-plugin/plugin.json": claudePluginJSON,
			},
			detectDirs: []string{root},
			detectBin:  "claude",
		}, nil

	case AgentCodex:
		root := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if root == "" {
			root = filepath.Join(home, ".codex")
		}
		parent := filepath.Join(root, "skills")
		dir := filepath.Join(parent, SkillName)
		return targetSpec{
			id:        AgentCodex,
			name:      "Codex",
			kind:      "skill",
			parentDir: parent,
			bundleDir: dir,
			primary:   filepath.Join(dir, "SKILL.md"),
			files: map[string][]byte{
				"SKILL.md": skillMarkdown,
			},
			detectDirs: []string{root},
			detectBin:  "codex",
		}, nil

	case AgentAgy:
		cfgRoot := filepath.Join(home, ".gemini", "config")
		parent := filepath.Join(cfgRoot, "plugins")
		dir := filepath.Join(parent, SkillName)
		// Installed as a native Antigravity plugin rather than under skills/ so
		// it auto-discovers regardless of include_only filters in skills.json,
		// appears in `agy plugin list`, and can be toggled natively by agy.
		return targetSpec{
			id:        AgentAgy,
			name:      "Antigravity (agy)",
			kind:      "plugin",
			parentDir: parent,
			bundleDir: dir,
			primary:   dir,
			files: map[string][]byte{
				"plugin.json":     agyPluginJSON,
				"assets/logo.svg": agyLogoSVG,
				filepath.Join("skills", SkillName, "SKILL.md"): skillMarkdown,
			},
			detectDirs: []string{
				filepath.Join(home, ".gemini", "antigravity-cli"),
				cfgRoot,
			},
			detectBin: "agy",
		}, nil

	case AgentDsh:
		root := strings.TrimSpace(os.Getenv("DSH_HOME"))
		if root == "" {
			root = filepath.Join(home, ".dsh")
		}
		parent := filepath.Join(root, "skills")
		dir := filepath.Join(parent, SkillName)
		// @deepseek-ai/dsh-skill-filesystem scans $DSH_HOME/skills (default
		// ~/.dsh/skills) at USER_DSH_RANK (400) and hot-reloads changes.
		return targetSpec{
			id:        AgentDsh,
			name:      "DeepSeek Harness (dsh)",
			kind:      "skill",
			parentDir: parent,
			bundleDir: dir,
			primary:   filepath.Join(dir, "SKILL.md"),
			files: map[string][]byte{
				"SKILL.md": skillMarkdown,
			},
			detectDirs: []string{root},
			detectBin:  "dsh",
		}, nil
	}
	return targetSpec{}, fmt.Errorf("shareskill: unknown agent %q", agent)
}

// Inspect reads the current on-disk state for every supported agent without
// modifying anything.
func Inspect() (Status, error) {
	out := Status{
		SkillName: SkillName,
		Content:   string(skillMarkdown),
		Agents:    make([]AgentStatus, 0, len(Agents)),
	}
	for _, agent := range Agents {
		spec, err := targetFor(agent)
		if err != nil {
			return Status{}, err
		}
		installed, stale := inspectSpec(spec)
		out.Agents = append(out.Agents, AgentStatus{
			ID:        spec.id,
			Name:      spec.name,
			Path:      spec.primary,
			Kind:      spec.kind,
			Installed: installed,
			Stale:     stale,
			Detected:  detectSpec(spec),
		})
	}
	return out, nil
}

func inspectSpec(spec targetSpec) (installed, stale bool) {
	allMatch := true
	anyExist := false
	for rel, want := range spec.files {
		p := filepath.Join(spec.bundleDir, rel)
		have, err := os.ReadFile(p)
		if err != nil {
			allMatch = false
			if !errors.Is(err, os.ErrNotExist) {
				anyExist = true
			}
			continue
		}
		anyExist = true
		if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(want)) {
			allMatch = false
		}
	}
	if allMatch {
		return true, false
	}
	if anyExist {
		return false, true
	}
	// Also treat an existing empty bundle directory left behind by a partial
	// copy as stale so the user can see and repair or remove it.
	if info, err := os.Stat(spec.bundleDir); err == nil && info.IsDir() {
		return false, true
	}
	return false, false
}

func detectSpec(spec targetSpec) bool {
	for _, dir := range spec.detectDirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return true
		}
	}
	if spec.detectBin != "" {
		if _, err := exec.LookPath(spec.detectBin); err == nil {
			return true
		}
	}
	return false
}

// Install writes the skill/plugin bundle for one agent and returns the refreshed
// status. Writing a bundle that is already current is a no-op so file mtimes do
// not churn.
func Install(agent Agent) (Status, error) {
	return InstallMany([]Agent{agent})
}

// InstallMany writes the skill/plugin bundle for each named agent in order.
func InstallMany(agents []Agent) (Status, error) {
	if len(agents) == 0 {
		return Inspect()
	}
	editMu.Lock()
	defer editMu.Unlock()
	for _, agent := range agents {
		spec, err := targetFor(agent)
		if err != nil {
			return Status{}, err
		}
		if err := writeSpec(spec); err != nil {
			return Status{}, err
		}
	}
	return Inspect()
}

// Uninstall removes the panel-owned skill/plugin directory for one agent and
// nothing else.
func Uninstall(agent Agent) (Status, error) {
	return UninstallMany([]Agent{agent})
}

// UninstallMany removes the panel-owned bundle for each named agent.
func UninstallMany(agents []Agent) (Status, error) {
	if len(agents) == 0 {
		return Inspect()
	}
	editMu.Lock()
	defer editMu.Unlock()
	for _, agent := range agents {
		spec, err := targetFor(agent)
		if err != nil {
			return Status{}, err
		}
		if err := removeSpec(spec); err != nil {
			return Status{}, err
		}
	}
	return Inspect()
}

func writeSpec(spec targetSpec) error {
	for rel, content := range spec.files {
		dst := filepath.Join(spec.bundleDir, rel)
		if current, err := os.ReadFile(dst); err == nil && bytes.Equal(current, content) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("shareskill: create %s: %w", filepath.Dir(dst), err)
		}
		if err := writeFileAtomic(dst, content, 0o644); err != nil {
			return fmt.Errorf("shareskill: write %s: %w", dst, err)
		}
	}
	return nil
}

func removeSpec(spec targetSpec) error {
	// Mechanical guard: never RemoveAll anything other than our own leaf
	// directory directly under skills/ or plugins/.
	clean := filepath.Clean(spec.bundleDir)
	parent := filepath.Clean(spec.parentDir)
	if filepath.Base(clean) != SkillName || filepath.Dir(clean) != parent {
		return fmt.Errorf("shareskill: refusing to remove unexpected path %s", clean)
	}
	if err := os.RemoveAll(clean); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("shareskill: remove %s: %w", clean, err)
	}
	return nil
}

func writeFileAtomic(dst string, data []byte, mode os.FileMode) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
