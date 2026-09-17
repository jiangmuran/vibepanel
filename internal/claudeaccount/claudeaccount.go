// Package claudeaccount gives Claude Code a second login without giving it a
// second everything else.
//
// Claude Code's documented way to run as another account is
// CLAUDE_CONFIG_DIR, and it moves far more than the login. Measured on 2.1.274,
// the directory it names replaces ~/.claude whole, and ~/.claude.json moves
// into it as well: settings.json and therefore the panel's hooks, CLAUDE.md,
// skills, agents, commands, plugins, every transcript `--resume` and the token
// accounting read, the prompt history behind the up arrow, user-scope MCP
// servers and which projects have been trusted. A profile that set it by hand
// started an agent that reported nothing to the panel, could not resume
// anything and asked to trust every repository again -- and the settings page
// still said hooks were installed, because it reads ~/.claude.
//
// # What an account directory is
//
// <DataDir>/claude-accounts/<id>, holding:
//
//   - its own: the login (.credentials.json on Linux; a Keychain entry on
//     macOS), .claude.json, and whatever Claude Code creates that is not on
//     the list below -- backups/, cache/, sessions/, daemon/, jobs/,
//     remote-settings.json, telemetry.
//   - symlinks into ~/.claude for everything on Shared.
//
// Each of those was measured rather than assumed, against a real claude in a
// throwaway tmux with a throwaway main directory:
//
//   - A write to a symlinked file lands in the target and the link survives,
//     for settings.json (`/model`, which saves there) and .claude.json alike.
//     Claude Code resolves the link rather than renaming over it.
//   - A symlinked directory works: transcripts written under the account
//     arrive in ~/.claude/projects, and a second account's `claude --resume`
//     lists and opens them.
//   - A symlink to a file that does not exist yet is followed and the file is
//     created at the target (settings.json and history.jsonl). A directory is
//     not, which is why Reconcile creates missing directories in ~/.claude and
//     leaves missing files as dangling links.
//   - Hooks in the shared settings.json fire with CLAUDE_CONFIG_DIR set to the
//     account, and a user skill in the shared skills/ is offered.
//
// # Why a list of what is shared, and not a list of what is private
//
// sessions/ holds a live session's peer key, and daemon/ and jobs/ belong to
// background agents. Sharing either would let one account's session talk to,
// or run under, another's login. Neither existed a few releases ago. The next
// thing like them will appear in a release nobody here reads, and a list of
// private entries would share it by default and say nothing. A list of shared
// entries fails the other way: something new is private until it is added
// here, which costs a habit rather than a login.
//
// # Why not CLAUDE_SECURESTORAGE_CONFIG_DIR
//
// It exists (2.1.274) and, set alone, moves only where the login is read from,
// which would share everything with no links at all. It is not documented, and
// .claude.json would still be one file: the oauthAccount written at /login --
// email, organisation -- would be whichever account logged in last, for every
// session.
package claudeaccount

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Entry is one name in ~/.claude an account may link to.
type Entry struct {
	Name string
	Dir  bool
	// History marks what an isolated account keeps to itself: the
	// conversations, what was typed, and what either of those points at.
	History bool
}

// Shared is every entry an account links to ~/.claude.
//
// Grouped by why. Changing this list changes what one login can see of
// another's, so an addition says which group it is in.
var Shared = []Entry{
	// Configuration somebody wrote. The panel's hooks are in settings.json.
	{Name: "settings.json"},
	{Name: "CLAUDE.md"},
	{Name: "keybindings.json"},
	{Name: "agents", Dir: true},
	{Name: "commands", Dir: true},
	{Name: "skills", Dir: true},
	{Name: "output-styles", Dir: true},
	{Name: "plugins", Dir: true},
	// Written by other programs for Claude Code to find: the IDE extensions
	// write a lock file per window here, and a session that cannot see it
	// cannot connect to the editor it was opened from.
	{Name: "ide", Dir: true},
	{Name: "shell-snapshots", Dir: true},

	// Conversations and what refers to them. projects/ also holds each
	// project's auto memory. paste-cache/ is where a large paste in
	// history.jsonl actually is, and file-history/, todos/, tasks/ and
	// session-env/ are keyed by a session id that `--resume` carries across.
	{Name: "projects", Dir: true, History: true},
	{Name: "history.jsonl", History: true},
	{Name: "paste-cache", Dir: true, History: true},
	{Name: "file-history", Dir: true, History: true},
	{Name: "todos", Dir: true, History: true},
	{Name: "tasks", Dir: true, History: true},
	{Name: "plans", Dir: true, History: true},
	{Name: "session-env", Dir: true, History: true},
}

// DirName is the directory under the data directory that holds accounts.
const DirName = "claude-accounts"

// Dir is where an account lives.
//
// Derived from the id and nothing else, and never moved. On macOS Claude Code
// names the Keychain entry for a login after the first eight hex digits of the
// SHA-256 of this path (read out of 2.1.274: `Claude Code-credentials-<hash>`),
// so a directory that moves is an account that has been logged out, with its
// token left in the Keychain under a name nothing will look up again.
func Dir(dataDir, id string) (string, error) {
	if !plainID(id) {
		return "", fmt.Errorf("claudeaccount: refusing to build a path from id %q", id)
	}
	if !filepath.IsAbs(dataDir) {
		// A relative CLAUDE_CONFIG_DIR is resolved against the pane's working
		// directory, which is a different project for every session -- the
		// same failure the ~ expansion in store.expandTilde was written for.
		abs, err := filepath.Abs(dataDir)
		if err != nil {
			return "", err
		}
		dataDir = abs
	}
	return filepath.Join(dataDir, DirName, id), nil
}

func plainID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Main is the configuration an account shares with: ~/.claude and
// ~/.claude.json under home.
//
// Not $CLAUDE_CONFIG_DIR from the panel's environment. The hooks installer
// writes ~/.claude/settings.json unconditionally (hooks.ClaudeSettingsPath),
// and an account that linked somewhere else would be linking away from the
// file the panel installs into.
type Main struct {
	Home string
}

func (m Main) dir() string  { return filepath.Join(m.Home, ".claude") }
func (m Main) json() string { return filepath.Join(m.Home, ".claude.json") }

// LinkState is what Reconcile found for one entry.
type LinkState string

const (
	// Linked: a symlink to the entry in ~/.claude.
	Linked LinkState = "linked"
	// Private: an isolated account's history entry, deliberately not linked.
	Private LinkState = "private"
	// Blocked: something else is there -- a real file or directory, or a link
	// somewhere else -- and it has been left alone. Claude Code wrote it
	// before the link existed, or a person put it there; either way it is
	// somebody's data, and replacing it with a link would delete it.
	Blocked LinkState = "blocked"
)

// Link is one entry's state.
type Link struct {
	Name  string    `json:"name"`
	State LinkState `json:"state"`
}

// Reconcile makes an account directory what this package says it is, and
// reports what it could not make so.
//
// Run before every launch rather than once at creation, for two reasons that
// both happened in testing: an entry added to Shared by a later release has to
// reach accounts that already exist, and a directory absent from ~/.claude
// when the account was made has to be linked once it is not.
//
// It never removes or replaces anything that is not a link it made. A Blocked
// entry is reported, not repaired.
func Reconcile(dir string, main Main, isolated bool) ([]Link, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("claudeaccount: create %s: %w", dir, err)
	}
	// MkdirAll leaves an existing directory's mode alone, and this one holds a
	// login.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("claudeaccount: chmod %s: %w", dir, err)
	}
	if err := os.MkdirAll(main.dir(), 0o755); err != nil {
		return nil, fmt.Errorf("claudeaccount: create %s: %w", main.dir(), err)
	}

	out := make([]Link, 0, len(Shared))
	for _, e := range Shared {
		at := filepath.Join(dir, e.Name)
		target := filepath.Join(main.dir(), e.Name)

		if isolated && e.History {
			// A link left over would be exactly the sharing this account was
			// made to avoid, and it can only be one of ours: nothing else
			// writes links into this directory. Removing a link removes the
			// link, never what it points at.
			if dest, err := os.Readlink(at); err == nil && dest == target {
				if err := os.Remove(at); err != nil {
					return nil, fmt.Errorf("claudeaccount: unlink %s: %w", at, err)
				}
			}
			out = append(out, Link{Name: e.Name, State: Private})
			continue
		}

		if e.Dir {
			// A link to a directory that does not exist is not followed when
			// Claude Code creates it: mkdir on a dangling link fails. So the
			// directory is made where it belongs.
			if err := os.MkdirAll(target, 0o755); err != nil {
				out = append(out, Link{Name: e.Name, State: Blocked})
				continue
			}
		}

		fi, err := os.Lstat(at)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Symlink(target, at); err != nil {
				return nil, fmt.Errorf("claudeaccount: link %s: %w", at, err)
			}
			out = append(out, Link{Name: e.Name, State: Linked})
		case err != nil:
			return nil, fmt.Errorf("claudeaccount: %s: %w", at, err)
		case fi.Mode()&fs.ModeSymlink != 0:
			dest, rerr := os.Readlink(at)
			if rerr == nil && dest == target {
				out = append(out, Link{Name: e.Name, State: Linked})
			} else {
				out = append(out, Link{Name: e.Name, State: Blocked})
			}
		default:
			out = append(out, Link{Name: e.Name, State: Blocked})
		}
	}
	return out, nil
}

// Env is what a session is started with to run under the account.
func Env(dir string) []string {
	return []string{"CLAUDE_CONFIG_DIR=" + dir}
}

// Prepare is Reconcile followed by SyncJSON: everything a launch needs.
//
// A failure to merge .claude.json is not a failure to launch. The session
// starts with whatever the account already has, which at worst is a trust
// prompt it has answered before; refusing to start it would turn a habit into
// an outage.
func Prepare(dir string, main Main, isolated bool) ([]Link, error) {
	links, err := Reconcile(dir, main, isolated)
	if err != nil {
		return nil, err
	}
	_ = SyncJSON(dir, main)
	return links, nil
}

// ─── .claude.json ─────────────────────────────────────────────────────────

// SyncJSON copies the parts of ~/.claude.json that are habits into the
// account's own .claude.json, and nothing that is an account.
//
// The file cannot be a link. /login writes oauthAccount -- email,
// organisation, rate-limit tier -- into it, so one file would describe
// whichever account logged in last to every session of every account. It
// also holds userID, cached feature flags and API key approvals.
//
// So it is a copy, merged at every launch, and the merge only ever adds:
//
//   - an MCP server the account does not have, by name, at the top level and
//     per project;
//   - a project's trust, from false to true, and its allowed tools as a union;
//   - onboarding, once it is done.
//
// Display preferences are not copied, though they were at first. 2.1.274
// moves theme, editorMode, preferredNotifChannel and the rest out of
// .claude.json into settings.json on startup, which is a shared link: copied
// here, theme was deleted again by the first session and re-added by the next
// launch, a write per launch for nothing.
//
// Nothing is removed and nothing the account has is overwritten, which is
// what makes it safe against the one thing that cannot be prevented: a
// session of this account writing the file at the same moment. Claude Code
// re-reads the file before it writes (measured: a key added from outside while
// a session ran survived that session's exit), so the window is the time
// between this read and this rename, and losing it loses an addition the next
// launch makes again.
//
// One direction only. A server added with `claude mcp add` inside an account
// stays in that account, and a project trusted there is not trusted in
// ~/.claude.json: the panel does not write a file Claude Code's own sessions
// are writing all day, to save somebody pressing Enter once.
func SyncJSON(dir string, main Main) error {
	src, err := readObject(main.json())
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ".claude.json")
	dst, err := readObject(path)
	if err != nil {
		return err
	}
	if !mergeHabits(dst, src) {
		return nil
	}
	return writeObject(path, dst)
}

// projectOnceKeys are a project's decisions the account has not made yet.
var projectOnceKeys = []string{
	"enabledMcpjsonServers",
	"disabledMcpjsonServers",
	"hasClaudeMdExternalIncludesApproved",
	"hasClaudeMdExternalIncludesWarningShown",
}

// mergeHabits applies the rules SyncJSON describes, and reports whether dst
// changed.
func mergeHabits(dst, src map[string]json.RawMessage) bool {
	changed := false

	if isTrue(src["hasCompletedOnboarding"]) && !isTrue(dst["hasCompletedOnboarding"]) {
		dst["hasCompletedOnboarding"] = json.RawMessage("true")
		changed = true
	}
	if addMissing(dst, src, "mcpServers") {
		changed = true
	}

	srcProjects := object(src["projects"])
	if len(srcProjects) == 0 {
		return changed
	}
	dstProjects := object(dst["projects"])
	if dstProjects == nil {
		dstProjects = map[string]json.RawMessage{}
	}
	projectsChanged := false
	for path, raw := range srcProjects {
		sp := object(raw)
		if sp == nil {
			continue
		}
		dp := object(dstProjects[path])
		if dp == nil {
			dp = map[string]json.RawMessage{}
		}
		pc := false
		if isTrue(sp["hasTrustDialogAccepted"]) && !isTrue(dp["hasTrustDialogAccepted"]) {
			dp["hasTrustDialogAccepted"] = json.RawMessage("true")
			pc = true
		}
		if unionStrings(dp, sp, "allowedTools") {
			pc = true
		}
		if addMissing(dp, sp, "mcpServers") {
			pc = true
		}
		for _, k := range projectOnceKeys {
			if v, ok := sp[k]; ok {
				if _, have := dp[k]; !have {
					dp[k] = v
					pc = true
				}
			}
		}
		if pc {
			b, err := json.Marshal(dp)
			if err != nil {
				continue
			}
			dstProjects[path] = b
			projectsChanged = true
		}
	}
	if projectsChanged {
		b, err := json.Marshal(dstProjects)
		if err == nil {
			dst["projects"] = b
			changed = true
		}
	}
	return changed
}

// addMissing copies the members of src[key] that dst[key] lacks, by name.
func addMissing(dst, src map[string]json.RawMessage, key string) bool {
	from := object(src[key])
	if len(from) == 0 {
		return false
	}
	to := object(dst[key])
	if to == nil {
		// Present but not an object is somebody's data in a shape this does
		// not understand, and it is not replaced.
		if _, have := dst[key]; have {
			return false
		}
		to = map[string]json.RawMessage{}
	}
	added := false
	for name, v := range from {
		if _, have := to[name]; !have {
			to[name] = v
			added = true
		}
	}
	if !added {
		return false
	}
	b, err := json.Marshal(to)
	if err != nil {
		return false
	}
	dst[key] = b
	return true
}

// unionStrings adds the strings of src[key] that dst[key] lacks, in order.
func unionStrings(dst, src map[string]json.RawMessage, key string) bool {
	var from []string
	if json.Unmarshal(src[key], &from) != nil || len(from) == 0 {
		return false
	}
	var to []string
	if raw, have := dst[key]; have {
		if json.Unmarshal(raw, &to) != nil {
			return false
		}
	}
	seen := make(map[string]bool, len(to))
	for _, s := range to {
		seen[s] = true
	}
	added := false
	for _, s := range from {
		if !seen[s] {
			to = append(to, s)
			seen[s] = true
			added = true
		}
	}
	if !added {
		return false
	}
	b, err := json.Marshal(to)
	if err != nil {
		return false
	}
	dst[key] = b
	return true
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func isTrue(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("true"))
}

// readObject reads a JSON object, and an absent file as an empty one.
//
// A file that exists and does not parse is an error rather than an empty
// object: SyncJSON writes the account's file back, and writing back "empty
// plus the habits" over a file somebody's Claude Code was halfway through
// writing would erase the account's login details.
func readObject(path string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claudeaccount: read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("claudeaccount: %s is not a JSON object: %w", path, err)
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, nil
}

// writeObject replaces the file in one rename, 0600: it holds the account's
// oauthAccount once somebody has logged in.
func writeObject(path string, m map[string]json.RawMessage) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("claudeaccount: encode %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude.json.vibepanel-*")
	if err != nil {
		return fmt.Errorf("claudeaccount: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("claudeaccount: %w", err)
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("claudeaccount: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("claudeaccount: write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("claudeaccount: replace %s: %w", path, err)
	}
	return nil
}

// ─── asking Claude Code ───────────────────────────────────────────────────

// Status is what `claude auth status --json` says about an account.
//
// Asked of the CLI rather than read out of .claude.json and .credentials.json,
// because on macOS the login is not in a file at all, and because the CLI's
// JSON output is an interface its authors present and the files are not.
type Status struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email,omitempty"`
	OrgName          string `json:"orgName,omitempty"`
	SubscriptionType string `json:"subscriptionType,omitempty"`
	// ConfigDirectory is where the CLI says it looked. Anything other than
	// the account's own directory means the environment overrode it, which is
	// the answer to "why does this account say it is somebody else".
	ConfigDirectory string `json:"configDirectory,omitempty"`
}

// Runner starts a claude subcommand with an extra environment.
//
// A field rather than exec directly so tests do not need a Claude Code
// install, and so the caller can resolve the binary the way panes do
// (tmux.LaunchArgv): under a systemd unit `claude` is usually not on the
// panel's PATH.
type Runner func(ctx context.Context, env []string, args ...string) ([]byte, error)

// ExecRunner runs argv (with args appended) with env added to the panel's
// own environment.
func ExecRunner(resolve func([]string) []string) Runner {
	return func(ctx context.Context, env []string, args ...string) ([]byte, error) {
		argv := resolve(append([]string{"claude"}, args...))
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = strings.TrimSpace(stdout.String())
			}
			if len(msg) > 300 {
				msg = msg[:300]
			}
			if msg != "" {
				return stdout.Bytes(), fmt.Errorf("claude %s: %w: %s", strings.Join(args, " "), err, msg)
			}
			return stdout.Bytes(), fmt.Errorf("claude %s: %w", strings.Join(args, " "), err)
		}
		return stdout.Bytes(), nil
	}
}

// statusTimeout bounds one status read. `claude auth status` takes about a
// second on a warm machine; this is for the one that is not.
const statusTimeout = 20 * time.Second

// ReadStatus asks Claude Code whether the account is logged in.
//
// Variables that would stand in for the login are removed from the child's
// environment for this read. The panel's own environment is not the session's
// -- panes inherit the tmux server's, from a login shell -- and a key exported
// there would make every account read "logged in" by API key, which is a true
// statement about the panel's process and a false one about the account.
func ReadStatus(ctx context.Context, run Runner, dir string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	env := append(Env(dir),
		"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=",
		"CLAUDE_SECURESTORAGE_CONFIG_DIR=")
	out, err := run(ctx, env, "auth", "status", "--json")
	// A logged-out account exits non-zero on some builds and still prints the
	// document, so the document is read first.
	var st Status
	if jerr := json.Unmarshal(bytes.TrimSpace(out), &st); jerr == nil && st.AuthMethod != "" {
		return st, nil
	}
	if err != nil {
		return Status{}, err
	}
	return Status{}, fmt.Errorf("claude auth status: unexpected output")
}

// Remove logs the account out and deletes its directory.
//
// Logout first, because of macOS: the login is a Keychain entry whose name is
// derived from this directory's path, and once the directory is gone nothing
// will ever look that entry up again. On Linux the login is a file inside the
// directory and logout is redundant, and harmless.
//
// A logout that fails is returned alongside a successful removal rather than
// stopping it. The person asked for the account to be gone; refusing because
// `claude` is not installed any more would leave them unable to remove it at
// all. The caller says what the error means.
//
// os.RemoveAll unlinks symlinks rather than following them -- the account's
// links into ~/.claude go, and ~/.claude stays. That is the property this
// function stands on, and TestRemoveLeavesTheSharedFilesAlone is what checks
// it.
func Remove(ctx context.Context, run Runner, dir string) (logoutErr, err error) {
	if _, serr := os.Lstat(dir); errors.Is(serr, fs.ErrNotExist) {
		return nil, nil
	}
	if run != nil {
		lctx, cancel := context.WithTimeout(ctx, statusTimeout)
		_, logoutErr = run(lctx, Env(dir), "auth", "logout")
		cancel()
	}
	if err := os.RemoveAll(dir); err != nil {
		return logoutErr, fmt.Errorf("claudeaccount: remove %s: %w", dir, err)
	}
	return logoutErr, nil
}
