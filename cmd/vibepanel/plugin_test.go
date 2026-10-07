package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `vibepanel plugin init` writes the same scaffold the New plugin button
// does, and what it wrote passes `vibepanel plugin check`: a directory an
// agent starts in is one the panel would install.
func TestPluginInitWritesADirectoryThatChecks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin-standup")
	if err := pluginInit([]string{"--template", "service", dir}); err != nil {
		t.Fatalf("init: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json")) //nolint:gosec // test dir
	if err != nil {
		t.Fatal(err)
	}
	// The id and the name come from the directory, less the plugin- prefix.
	if !strings.Contains(string(raw), `"id": "standup"`) || !strings.Contains(string(raw), `"en": "standup"`) {
		t.Errorf("plugin.json: %s", raw)
	}
	if err := pluginCheck([]string{dir}); err != nil {
		t.Errorf("check: %v", err)
	}
	// Never into a directory with files in it.
	if err := pluginInit([]string{dir}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("a second init over the first: %v", err)
	}
	// A name of its own, and an id made from it.
	dir2 := filepath.Join(t.TempDir(), "x")
	if err := pluginInit([]string{"--template", "theme", "--name", "Warm Paper", dir2}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir2, "plugin.json")) //nolint:gosec // test dir
	if !strings.Contains(string(raw), `"id": "warm-paper"`) {
		t.Errorf("plugin.json: %s", raw)
	}
}
