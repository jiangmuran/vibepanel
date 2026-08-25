package main

import (
	"sort"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/config"
)

func TestEveryDocumentedCommandExists(t *testing.T) {
	// `vibepanel --help` said "Usage: vibepanel [flags]" and listed flags. It
	// never mentioned that commands exist, so somebody who installed the
	// release archive and asked the binary what it does was not told about
	// `doctor` — while the runbook opens by telling them to run it.
	//
	// Adding the list to the usage text made three copies of the same six
	// words: the switch, the error for an unknown command, and the help. This
	// compares the two that are left.
	documented := commandNames()
	if len(documented) == 0 {
		t.Fatal("config.Commands parsed to nothing; the help text changed shape " +
			"and this test is no longer comparing anything")
	}

	var dispatched []string
	for name := range commands {
		dispatched = append(dispatched, name)
	}
	sort.Strings(dispatched)
	sorted := append([]string(nil), documented...)
	sort.Strings(sorted)

	if strings.Join(sorted, " ") != strings.Join(dispatched, " ") {
		t.Errorf("--help offers %v and the binary answers to %v", sorted, dispatched)
	}

	for _, name := range documented {
		if commands[name] == nil {
			t.Errorf("--help offers %q and nothing handles it", name)
		}
	}
}

func TestTheHelpTextDescribesEachCommand(t *testing.T) {
	// A name on its own is a list of words to guess at. Each line has to say
	// what the command is for, or the help is only marginally better than the
	// error message it was copied from.
	for _, line := range strings.Split(config.Commands, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			t.Errorf("%q has a name and almost nothing else", strings.TrimSpace(line))
		}
	}
}
