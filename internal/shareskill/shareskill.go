// Package shareskill manages the optional vibepanel-share agent skill/plugin
// bundle for coding agents (Claude Code, Codex, Antigravity CLI, and DeepSeek
// Harness).
//
// Installing is always opt-in: each agent owns one dedicated subdirectory
// named after SkillName so install never edits a user's settings.json,
// config.toml, hooks.json, skills.json, or cordis.patch.yml, and uninstall
// deletes only that directory -- and only when the panel wrote it: a leaf
// carries an ownership marker, and one without it is somebody else's.
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

// ownerMarker is written first into every leaf this package creates. A
// directory named vibepanel-share that lacks it was put there by a person or
// another tool, and install-over or RemoveAll on it would destroy their work.
const ownerMarker = ".vibepanel-owned"

// ErrForeign is returned when the leaf exists but the panel did not write it
// (no marker, or a symlink), so install and remove both refuse.
var ErrForeign = errors.New("a directory the panel did not create is at this path")

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
	// Foreign is true when something is at Path that the panel did not write
	// (no ownership marker, or a symlink). It is neither Installed nor Stale,
	// and install and remove refuse it.
	Foreign bool `json:"foreign"`
	// Error is set when the target could not be resolved at all, for example
	// no home directory for an agent whose path lives under it.
	Error string `json:"error,omitempty"`
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
	bundleDir  string // <parentDir>/<SkillName>
	primary    string // path shown in the UI
	files      map[string][]byte
	detectDirs []string
	detectBin  string
}

func targetFor(agent Agent) (targetSpec, error) {
	// Only Claude and Antigravity live under HOME unconditionally; Codex and
	// dsh are independent of it when their variable is set, so a missing home
	// must not hide them.
	home, herr := os.UserHomeDir()
	needHome := func() error {
		if herr != nil {
			return fmt.Errorf("shareskill: home directory: %w", herr)
		}
		return nil
	}
	switch agent {
	case AgentClaude:
		if err := needHome(); err != nil {
			return targetSpec{}, err
		}
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
		root := absEnv("CODEX_HOME")
		if root == "" {
			if err := needHome(); err != nil {
				return targetSpec{}, err
			}
			root = filepath.Join(home, ".codex")
		}
		parent := filepath.Join(root, "skills")
		dir := filepath.Join(parent, SkillName)
		return targetSpec{
			id:        AgentCodex,
			name:      "Codex",
			kind:      "skill",
			bundleDir: dir,
			primary:   filepath.Join(dir, "SKILL.md"),
			files: map[string][]byte{
				"SKILL.md": skillMarkdown,
			},
			detectDirs: []string{root},
			detectBin:  "codex",
		}, nil

	case AgentAgy:
		if err := needHome(); err != nil {
			return targetSpec{}, err
		}
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
		root := absEnv("DSH_HOME")
		if root == "" {
			if err := needHome(); err != nil {
				return targetSpec{}, err
			}
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

// absEnv returns an environment variable only when it is an absolute path. A
// relative one would be resolved against the panel's working directory, which
// is wherever systemd or a shell happened to start it, and the bundle would
// land (or be removed) there; the agent itself would not look in that place.
func absEnv(name string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" || !filepath.IsAbs(v) {
		return ""
	}
	return filepath.Clean(v)
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
			// One agent without a path is that agent's problem, not a failure
			// of the whole listing.
			out.Agents = append(out.Agents, AgentStatus{ID: agent, Name: agentName(agent), Error: err.Error()})
			continue
		}
		if leafState(spec.bundleDir) == leafForeign {
			out.Agents = append(out.Agents, AgentStatus{
				ID: spec.id, Name: spec.name, Path: spec.primary, Kind: spec.kind,
				Detected: detectSpec(spec), Foreign: true,
			})
			continue
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

func agentName(a Agent) string {
	switch a {
	case AgentClaude:
		return "Claude Code"
	case AgentCodex:
		return "Codex"
	case AgentAgy:
		return "Antigravity (agy)"
	case AgentDsh:
		return "DeepSeek Harness (dsh)"
	}
	return string(a)
}

type leaf int

const (
	leafAbsent leaf = iota
	leafOwned
	leafForeign
)

// leafState says whether the bundle directory is absent, written by the panel
// or somebody else's. Lstat, not Stat: a symlink at the leaf could point
// anywhere, and RemoveAll or a write through it would act on the target.
func leafState(dir string) leaf {
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return leafAbsent
		}
		return leafForeign
	}
	if !info.IsDir() {
		return leafForeign
	}
	m, err := os.Lstat(filepath.Join(dir, ownerMarker))
	if err != nil || !m.Mode().IsRegular() {
		return leafForeign
	}
	return leafOwned
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
	if leafState(spec.bundleDir) == leafForeign {
		return fmt.Errorf("shareskill: %s: %w", spec.bundleDir, ErrForeign)
	}
	// The marker goes first so an install interrupted half way leaves a leaf
	// the panel can still repair or remove, not one it must treat as foreign.
	if err := os.MkdirAll(spec.bundleDir, 0o755); err != nil {
		return fmt.Errorf("shareskill: create %s: %w", spec.bundleDir, err)
	}
	marker := filepath.Join(spec.bundleDir, ownerMarker)
	if _, err := os.Lstat(marker); err != nil {
		if err := writeFileAtomic(marker, []byte("written by vibepanel; removing this file makes the panel treat the directory as not its own\n"), 0o644); err != nil {
			return fmt.Errorf("shareskill: write %s: %w", marker, err)
		}
	}
	for rel, content := range spec.files {
		dst := filepath.Join(spec.bundleDir, rel)
		if err := refuseSymlinkPath(spec.bundleDir, rel); err != nil {
			return err
		}
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

// refuseSymlinkPath fails when any component of rel under the leaf is a
// symlink: MkdirAll and a write would follow it out of the bundle.
func refuseSymlinkPath(leaf, rel string) error {
	p := leaf
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if err != nil {
			return nil // nothing there yet, nothing to follow
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("shareskill: %s: %w", p, ErrForeign)
		}
	}
	return nil
}

func removeSpec(spec targetSpec) error {
	switch leafState(spec.bundleDir) {
	case leafAbsent:
		return nil
	case leafForeign:
		return fmt.Errorf("shareskill: %s: %w", spec.bundleDir, ErrForeign)
	}
	if err := os.RemoveAll(spec.bundleDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("shareskill: remove %s: %w", spec.bundleDir, err)
	}
	return nil
}

// writeFileAtomic writes through a uniquely named temp file in the destination
// directory and renames it over dst. A fixed dst+".tmp" would follow a
// symlink someone left there, and two writers would share one name.
func writeFileAtomic(dst string, data []byte, mode os.FileMode) (err error) {
	f, err := os.CreateTemp(filepath.Dir(dst), ".vp-share-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
