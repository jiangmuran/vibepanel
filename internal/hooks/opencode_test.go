package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func opencodeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// The plugin goes where opencode looks, and nowhere else.
//
// ~/.config/opencode/plugin is auto-discovered -- every *.js and *.ts in it is
// loaded with no config entry. Verified against the real opencode on this
// machine: a file dropped there comes back from `opencode debug config` as
// plugin_origins with scope "global". Getting the directory wrong installs
// nothing and reports success.
func TestTheOpencodePluginGoesWhereOpencodeLooks(t *testing.T) {
	home := opencodeHome(t)
	path, err := OpencodePluginPath()
	if err != nil {
		t.Fatalf("OpencodePluginPath: %v", err)
	}
	want := filepath.Join(home, ".config", "opencode", "plugin", "vibepanel.js")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestInstallingAndRemovingTheOpencodePlugin(t *testing.T) {
	opencodeHome(t)
	if OpencodeInstalled() {
		t.Fatal("reported installed before anything was written")
	}
	if err := InstallOpencode(); err != nil {
		t.Fatalf("InstallOpencode: %v", err)
	}
	if !OpencodeInstalled() {
		t.Error("reported not installed immediately after installing")
	}
	path, _ := OpencodePluginPath()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// The three states, as bare literals, because this file cannot import the
	// enum -- red line 3. A reporter that sends anything else is refused by the
	// server and the session silently stays on the heuristic.
	for _, state := range []string{"working", "waiting", "done"} {
		if !strings.Contains(string(body), `'`+state+`'`) {
			t.Errorf("the installed plugin never reports %q", state)
		}
	}
	// And it must do nothing at all outside a panel session.
	if !strings.Contains(string(body), "VIBEPANEL_SESSION_ID") {
		t.Error("the plugin does not check for a session id, so it would run for every opencode")
	}

	if err := UninstallOpencode(); err != nil {
		t.Fatalf("UninstallOpencode: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the plugin is still there after uninstalling: %v", err)
	}
	// Removing what is not there is not an error: the settings page offers the
	// button whenever it believes the plugin is present, and a stale belief
	// must not turn into a failure somebody has to read.
	if err := UninstallOpencode(); err != nil {
		t.Errorf("uninstalling twice: %v", err)
	}
}

// An older reporter left by an older build is installed and wrong.
//
// "A file exists" is the check this project keeps finding to be a lie -- the
// settings page reading a configuration file rather than whether anything ever
// arrived. Byte equality is what makes "installed" mean "current".
func TestAnOldOpencodePluginIsNotReportedAsInstalled(t *testing.T) {
	opencodeHome(t)
	path, _ := OpencodePluginPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("export default async () => ({})\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if OpencodeInstalled() {
		t.Error("a plugin from an older build is reported as installed")
	}
	if err := InstallOpencode(); err != nil {
		t.Fatalf("InstallOpencode: %v", err)
	}
	if !OpencodeInstalled() {
		t.Error("installing over an old plugin did not replace it")
	}
}

// Nothing may be left behind in a directory where opencode loads every file.
//
// The other two installers back a file up before editing it. Doing that here
// would leave vibepanel.js.bak beside vibepanel.js -- and opencode
// auto-discovers *.js, so being careful would install a second copy of the
// reporter and double every state update.
func TestInstallingLeavesNoOtherFileInThePluginDirectory(t *testing.T) {
	opencodeHome(t)
	if err := InstallOpencode(); err != nil {
		t.Fatalf("InstallOpencode: %v", err)
	}
	// Install twice: the second one is where a backup would appear.
	if err := os.WriteFile(mustPath(t), []byte("// changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallOpencode(); err != nil {
		t.Fatalf("InstallOpencode again: %v", err)
	}
	dir := filepath.Dir(mustPath(t))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "vibepanel.js" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the plugin directory holds %v; opencode loads every one of them", names)
	}
}

func mustPath(t *testing.T) string {
	t.Helper()
	p, err := OpencodePluginPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
