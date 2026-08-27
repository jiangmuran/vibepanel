package hooks

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// opencodePlugin is the reporter opencode loads.
//
//go:embed opencode-plugin.js
var opencodePlugin []byte

// opencodePluginName is the file this panel owns in opencode's plugin
// directory. Owning exactly one filename is what lets uninstall be a delete
// with no chance of taking somebody else's plugin with it.
const opencodePluginName = "vibepanel.js"

// OpencodePluginPath is where the reporter goes.
//
// opencode auto-discovers every *.js and *.ts in this directory: no config
// entry, no edit to a file somebody else owns. Verified rather than assumed --
// `opencode debug config` reports a file dropped here as
// `plugin_origins: [{ scope: "global" }]`.
//
// That makes this the cleanest of the three installers. Claude Code needs a
// JSON merge and Codex needs a line inserted above the first TOML table, both
// into documents full of the user's own settings; this one writes a file that
// did not exist and deletes it again.
func OpencodePluginPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("hooks: home directory: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "plugin", opencodePluginName), nil
}

// OpencodeInstalled reports whether the file in place is ours and current.
//
// Byte equality against what is embedded, not "a file exists". An older
// reporter left by an older build is installed and wrong, and a page that says
// "installed" about it is the failure this project keeps finding: the settings
// page reading a configuration file rather than whether anything ever arrived.
func OpencodeInstalled() bool {
	path, err := OpencodePluginPath()
	if err != nil {
		return false
	}
	have, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(opencodePlugin))
}

// InstallOpencode writes the reporter into opencode's plugin directory.
//
// Writes nothing when the file is already exactly right, for the same reason
// the other two installers do not: the settings page calls this whenever
// somebody presses the button, and rewriting a file to make no change to it
// still moves its mtime.
func InstallOpencode() error {
	path, err := OpencodePluginPath()
	if err != nil {
		return err
	}
	if OpencodeInstalled() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("hooks: create %s: %w", filepath.Dir(path), err)
	}
	// No backup, deliberately, and this is the one installer where that is
	// right: the file is named after this panel and is written only by this
	// panel. Backing it up would leave vibepanel.js.bak in a directory where
	// opencode auto-loads every *.js -- installing a second copy of the
	// reporter by trying to be careful.
	if err := os.WriteFile(path, opencodePlugin, 0o600); err != nil {
		return fmt.Errorf("hooks: write %s: %w", path, err)
	}
	return nil
}

// UninstallOpencode removes the reporter and nothing else.
func UninstallOpencode() error {
	path, err := OpencodePluginPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hooks: remove %s: %w", path, err)
	}
	return nil
}
