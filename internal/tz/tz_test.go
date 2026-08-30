package tz

import (
	"os"
	"testing"
	"time"
)

// The zone database has to be in the binary.
//
// CGO_ENABLED=0 means time.LoadLocation reads /usr/share/zoneinfo, which a
// distroless image, a scratch container and a stripped VM do not have. This
// test would pass on any developer machine whether or not `time/tzdata` is
// imported, so it hides the system database first -- which is the only way to
// tell the two apart without building a container.
func TestTheZoneDatabaseIsInTheBinary(t *testing.T) {
	// ZONEINFO is consulted before the system paths, and a path that is not a
	// zip file makes the file-system lookup fail. If tzdata is embedded, the
	// lookup still succeeds; if it is not, it cannot.
	t.Setenv("ZONEINFO", "/nonexistent/vibepanel-no-zoneinfo-here.zip")

	for _, name := range []string{"Asia/Shanghai", "America/Los_Angeles", "Europe/London", "UTC"} {
		loc, err := Load(name)
		if err != nil {
			t.Errorf("Load(%q) with no system zoneinfo: %v -- import _ \"time/tzdata\" has gone", name, err)
			continue
		}
		if loc.String() != name {
			t.Errorf("Load(%q) = %q", name, loc.String())
		}
	}
}

func TestEmptyMeansTheMachinesOwnZone(t *testing.T) {
	loc, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if loc != time.Local {
		t.Errorf(`Load("") = %v, want the local zone`, loc)
	}
	// Not UTC. At UTC+8 a UTC day boundary cuts the working evening in half,
	// and the question this feature answers is "how much did I spend today".
	if os.Getenv("TZ") == "" && loc == time.UTC && time.Local != time.UTC {
		t.Error(`Load("") fell back to UTC`)
	}
}

// A name that cannot be loaded is refused rather than stored and ignored.
// A setting that silently does something other than what the page shows is
// worse than one that will not save.
func TestAnUnknownZoneIsRefused(t *testing.T) {
	for _, bad := range []string{"Mars/Olympus", "Asia/Shangai", "+08:00", "CST-8", "../../etc/passwd"} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true", bad)
		}
	}
	for _, good := range []string{"", "UTC", "Asia/Shanghai", "America/New_York", "Local"} {
		if !Valid(good) {
			t.Errorf("Valid(%q) = false", good)
		}
	}
}
