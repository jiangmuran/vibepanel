package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func agyPath(t *testing.T) string {
	t.Helper()
	p, err := AgyHooksPath()
	if err != nil {
		t.Fatalf("AgyHooksPath: %v", err)
	}
	return p
}

func readAgyDoc(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(agyPath(t))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("the written hooks.json does not parse: %v", err)
	}
	return doc
}

func writeAgyDoc(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(agyPath(t)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agyPath(t), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgyPathIsTheMachineWideHooksFile(t *testing.T) {
	home := withFakeHome(t)
	if want := filepath.Join(home, ".gemini", "config", "hooks.json"); agyPath(t) != want {
		t.Errorf("AgyHooksPath = %q, want %q", agyPath(t), want)
	}
}

func TestInstallAgyCreatesTheFileWhenThereIsNone(t *testing.T) {
	withFakeHome(t)
	st, err := InstallAgy(codexScript)
	if err != nil {
		t.Fatalf("InstallAgy: %v", err)
	}
	if !st.AgyInstalled {
		t.Error("installed and then reported not installed")
	}
	if len(st.AgyEvents) != len(agyEvents) {
		t.Errorf("AgyEvents = %v, want %d events", st.AgyEvents, len(agyEvents))
	}
	block, _ := readAgyDoc(t)[agyHookName].(map[string]any)
	for _, e := range agyEvents {
		if _, ok := block[e.event]; !ok {
			t.Errorf("the hook is missing %s", e.event)
		}
	}
}

// agy is strict about the two shapes: a grouped event with bare handlers, or a
// flat one wrapped in a matcher group, is a hook that never runs and says
// nothing. The docs name which is which, and this pins it.
func TestAgyEventsAreGroupedOrFlatAsAgyExpectsThem(t *testing.T) {
	withFakeHome(t)
	if _, err := InstallAgy(codexScript); err != nil {
		t.Fatal(err)
	}
	block, _ := readAgyDoc(t)[agyHookName].(map[string]any)
	grouped := map[string]bool{"PreToolUse": true, "PostToolUse": true}
	for _, e := range agyEvents {
		if e.grouped != grouped[e.event] {
			t.Errorf("%s: grouped = %v, agy's docs say %v", e.event, e.grouped, grouped[e.event])
		}
		list, _ := block[e.event].([]any)
		first, _ := list[0].(map[string]any)
		_, hasInner := first["hooks"]
		if hasInner != e.grouped {
			t.Errorf("%s is written with a matcher group = %v, want %v", e.event, hasInner, e.grouped)
		}
		if e.grouped && first["matcher"] != "*" {
			t.Errorf("%s matcher = %v, want *", e.event, first["matcher"])
		}
	}
}

// Everything in the file that is not the panel's named hook is somebody's.
func TestAgyKeepsTheUsersOwnHooksAndRestoresThemOnUninstall(t *testing.T) {
	withFakeHome(t)
	own := `{"lint":{"PostToolUse":[{"matcher":"run_command","hooks":[{"type":"command","command":"./lint.sh"}]}]}}`
	writeAgyDoc(t, own)
	if _, err := InstallAgy(codexScript); err != nil {
		t.Fatal(err)
	}
	doc := readAgyDoc(t)
	if _, ok := doc["lint"]; !ok {
		t.Fatal("the install dropped the user's own hook")
	}
	st, err := UninstallAgy(codexScript)
	if err != nil {
		t.Fatal(err)
	}
	if st.AgyInstalled || len(st.AgyEvents) != 0 {
		t.Errorf("still installed after uninstall: %v", st.AgyEvents)
	}
	doc = readAgyDoc(t)
	if _, ok := doc[agyHookName]; ok {
		t.Error("our block is still in the file")
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(own), &want)
	if !reflect.DeepEqual(doc, want) {
		t.Errorf("the file is not what it was before: %v", doc)
	}
}

func TestInstallAgyTwiceWritesAndBacksUpNothing(t *testing.T) {
	withFakeHome(t)
	if _, err := InstallAgy(codexScript); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(agyPath(t))
	if _, err := InstallAgy(codexScript); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(agyPath(t))
	if string(before) != string(after) {
		t.Error("a second install changed the file")
	}
	backups, _ := filepath.Glob(agyPath(t) + ".vibepanel-backup-*")
	if len(backups) != 0 {
		t.Errorf("a first install over nothing and a no-op second one left %d backup(s)", len(backups))
	}
}

// A stale block from an older build, or one pointing at another data
// directory's script, is replaced and not appended to.
func TestInstallAgyReplacesAStaleBlock(t *testing.T) {
	withFakeHome(t)
	writeAgyDoc(t, `{"`+agyHookName+`":{"Stop":[{"type":"command","command":"/old/hooks/vibepanel-report.sh done"}]}}`)
	if _, err := InstallAgy(codexScript); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(agyPath(t))
	if strings.Contains(string(b), "/old/hooks") {
		t.Errorf("the stale command survived:\n%s", b)
	}
}

// A file that is not there is left not there: an uninstall that creates one
// has edited somebody's machine to say the panel was here.
func TestUninstallAgyCreatesNothing(t *testing.T) {
	withFakeHome(t)
	if _, err := UninstallAgy(codexScript); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agyPath(t)); !os.IsNotExist(err) {
		t.Errorf("uninstall left a file behind: %v", err)
	}
}

// A hook of somebody else's that shares the name is not ours to delete.
func TestUninstallAgyLeavesAForeignHookOfTheSameName(t *testing.T) {
	withFakeHome(t)
	writeAgyDoc(t, `{"`+agyHookName+`":{"Stop":[{"type":"command","command":"./mine.sh"}]}}`)
	if _, err := UninstallAgy(codexScript); err != nil {
		t.Fatal(err)
	}
	if _, ok := readAgyDoc(t)[agyHookName]; !ok {
		t.Error("deleted a hook that does not call this panel's script")
	}
}

func TestAgyIsReportedWithNoClaudeSettingsFile(t *testing.T) {
	withFakeHome(t)
	if _, err := InstallAgy(codexScript); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(codexScript)
	if err != nil {
		t.Fatal(err)
	}
	if st.AgyPath == "" || !st.AgyInstalled || st.AgyEvents == nil {
		t.Errorf("the early return for a missing Claude file dropped agy: %+v", st)
	}
}

func TestAgySnippetCarriesEveryEventTheInstallerWrites(t *testing.T) {
	snippet := AgyHooks(codexScript)
	for _, e := range agyEvents {
		if !strings.Contains(snippet, `"`+e.event+`"`) {
			t.Errorf("the snippet promises no %s", e.event)
		}
	}
	if !strings.Contains(snippet, codexScript) {
		t.Error("the snippet does not carry the reporter script")
	}
}

func TestAgyHookEventsIsNeverNil(t *testing.T) {
	withFakeHome(t)
	if got := agyHookEvents(agyPath(t)); got == nil {
		t.Error("nil would marshal as null and the settings page would die on .map")
	}
}
