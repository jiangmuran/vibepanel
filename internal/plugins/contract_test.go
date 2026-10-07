package plugins

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// contractFile is the committed list of every name a plugin may write down
// and expect to keep working within contract v1: slots, the classes in
// vibepanel-ui.css, the base tokens, the capabilities and the settings
// groups (docs/plugins.md §10). Contracts only grow: a name in the file that
// the source no longer has is a plugin somewhere drawing nothing, and a name
// in the source that the file does not have is a contract made by accident.
// Both are red. Adding a line here is the deliberate step.
//
// VP_WRITE_CONTRACT=1 rewrites the file from the source, for that step.
const contractFile = "testdata/contract-v1.txt"

func liveContract() map[string][]string {
	live := map[string][]string{
		"slot":  Slots(),
		"token": append([]string(nil), BaseTokens...),
		"group": append([]string(nil), SettingsGroups...),
	}
	for _, c := range Capabilities() {
		live["cap"] = append(live["cap"], c.Name)
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.(vp-[a-z0-9-]+)`).FindAllStringSubmatch(string(UI), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			live["class"] = append(live["class"], m[1])
		}
	}
	for k := range live {
		sort.Strings(live[k])
	}
	return live
}

func TestNothingAPluginCanNameWasRemoved(t *testing.T) {
	live := liveContract()
	if os.Getenv("VP_WRITE_CONTRACT") == "1" {
		var b strings.Builder
		b.WriteString("# Contract v1: every name a plugin may use. Only grows. docs/plugins.md §10.\n")
		b.WriteString("# Checked by TestNothingAPluginCanNameWasRemoved; VP_WRITE_CONTRACT=1 rewrites it.\n")
		for _, kind := range []string{"slot", "class", "token", "cap", "group"} {
			for _, name := range live[kind] {
				fmt.Fprintf(&b, "%s %s\n", kind, name)
			}
		}
		if err := os.WriteFile(contractFile, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(contractFile)
	if err != nil {
		t.Fatalf("%v (VP_WRITE_CONTRACT=1 writes it)", err)
	}
	defer f.Close()
	listed := map[string][]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, name, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("a line that is not `kind name`: %q", line)
		}
		listed[kind] = append(listed[kind], name)
	}
	if len(listed) < 5 {
		t.Fatalf("the list has %d kinds; the reader is reading nothing", len(listed))
	}
	for kind, names := range listed {
		for _, n := range names {
			if !slices.Contains(live[kind], n) {
				t.Errorf("%s %q is in the contract and no longer in the source: a plugin that names it now draws nothing", kind, n)
			}
		}
	}
	for kind, names := range live {
		for _, n := range names {
			if !slices.Contains(listed[kind], n) {
				t.Errorf("%s %q is in the source and not in %s: add the line (VP_WRITE_CONTRACT=1), out loud", kind, n, contractFile)
			}
		}
	}
}
