package cgroup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestParseProcCgroupTakesTheUnifiedLine(t *testing.T) {
	// A hybrid host lists v1 hierarchies first; their paths name other trees.
	got, err := parseProcCgroup("12:memory:/user.slice\n1:name=systemd:/x\n0::/system.slice/vibepanel.service\n")
	if err != nil || got != "/system.slice/vibepanel.service" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseProcCgroup("1:name=systemd:/x\n"); err == nil {
		t.Fatal("a v1-only host must not read as a cgroup")
	}
}

func TestServiceManagerReadsThePathNotTheEnvironment(t *testing.T) {
	cases := map[string]Manager{
		"/system.slice/vibepanel.service":                                           System,
		"/user.slice/user-1000.slice/user@1000.service/app.slice/vibepanel.service": User,
	}
	for path, want := range cases {
		if got, ok := ServiceManager(path); !ok || got != want {
			t.Errorf("%s: got %q %v, want %q", path, got, ok, want)
		}
	}
	// A panel started by hand, including from inside one of its own sessions:
	// it is not a service and must not start moving processes.
	for _, path := range []string{
		"/system.slice/vibepanel-sessions.scope/pool/s-vp_ab",
		"/user.slice/user-1000.slice/session-3.scope",
		"/user.slice/user-1000.slice/user@1000.service",
		"/",
	} {
		if m, ok := ServiceManager(path); ok {
			t.Errorf("%s: read as a %q service", path, m)
		}
	}
}

func TestScopeNameIsPerSocketAccountAndAUnitName(t *testing.T) {
	if got := ScopeName(User, 1000, "vibepanel"); got != "vibepanel-sessions.scope" {
		t.Fatalf("user default: %q", got)
	}
	if got := ScopeName(System, 1000, "vibepanel"); got != "vibepanel-sessions-1000.scope" {
		t.Fatalf("system default: %q", got)
	}
	if ScopeName(System, 1000, "vibepanel") == ScopeName(System, 1001, "vibepanel") {
		t.Fatal("two accounts' system units shared a scope")
	}
	if ScopeName(User, 1, "vpcheck-1") == ScopeName(User, 1, "vpcheck-2") {
		t.Fatal("two sockets shared a scope")
	}
	if ScopeName(User, 1, "vp.a") == ScopeName(User, 1, "vp_a") {
		t.Fatal("a name changed to fit a unit name collided with one that did not need changing")
	}
	got := ScopeName(User, 1, "we ird/../x")
	if !regexp.MustCompile(`^vibepanel-sessions-we_ird____x-[0-9a-f]{8}\.scope$`).MatchString(got) {
		t.Fatalf("not a unit name: %q", got)
	}
}

func TestAScopeCgroupFromSystemdIsCheckedBeforeUse(t *testing.T) {
	ok := map[string]Manager{
		"/system.slice/vibepanel-sessions-1000.scope":                                      System,
		"/user.slice/user-1000.slice/user@1000.service/app.slice/vibepanel-sessions.scope": User,
	}
	for rel, m := range ok {
		name := rel[strings.LastIndex(rel, "/")+1:]
		if !plausibleScopeCgroup(m, name, rel) {
			t.Errorf("refused %s", rel)
		}
	}
	for _, rel := range []string{
		"/../../home/jmr",
		"/system.slice/../../home/jmr/vibepanel-sessions-1000.scope",
		"/system.slice/other.scope",
		"/user.slice/user-1000.slice/vibepanel-sessions-1000.scope",
		"system.slice/vibepanel-sessions-1000.scope",
	} {
		if plausibleScopeCgroup(System, "vibepanel-sessions-1000.scope", rel) {
			t.Errorf("accepted %q", rel)
		}
	}
}

func TestParsePressure(t *testing.T) {
	p := ParsePressure("some avg10=12.50 avg60=4.98 avg300=14.72 total=269904161\nfull avg10=40.25 avg60=4.86 avg300=14.24 total=255362451\n")
	if p.Some10 != 12.5 || p.Full10 != 40.25 {
		t.Fatalf("%+v", p)
	}
}

func TestFilesReadAndWrite(t *testing.T) {
	dir := t.TempDir()
	old := Mount
	Mount = dir
	t.Cleanup(func() { Mount = old })

	d := At("/scope")
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	if d.Rel() != "/scope" {
		t.Fatalf("rel %q", d.Rel())
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(string(d), name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("memory.max", "max\n")
	if v, err := d.Uint("memory.max"); err != nil || v != Unlimited {
		t.Fatalf("max read as %d %v", v, err)
	}
	if err := d.SetUint("memory.max", 1<<30); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Uint("memory.max"); v != 1<<30 {
		t.Fatalf("wrote %d", v)
	}
	write("cgroup.procs", "12\n34\n")
	if p, _ := d.Procs(); len(p) != 2 || p[0] != 12 || p[1] != 34 {
		t.Fatalf("procs %v", p)
	}
	write("cgroup.events", "populated 1\nfrozen 1\n")
	if !d.Frozen() {
		t.Fatal("frozen 1 read as thawed")
	}
	write("cgroup.events", "populated 1\nfrozen 0\n")
	if d.Frozen() {
		t.Fatal("frozen 0 read as frozen")
	}
}
