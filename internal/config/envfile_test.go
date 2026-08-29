package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const shipped = `# Environment for vibepanel. Copy to ~/.config/vibepanel.env and edit.
#
# Every setting also exists as a flag; the flag wins.

# Where to listen.
VIBEPANEL_ADDR=:18443

# The public hostname. Also the WebAuthn Relying Party ID.
#VIBEPANEL_DOMAIN=panel.example.com

# off | files | acme
#VIBEPANEL_TLS_MODE=acme

# Keep this dedicated.
#VIBEPANEL_TMUX_SOCKET=vibepanel
`

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vibepanel.env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The paragraphs survive. They are the only documentation the operator has.
//
// A writer that emitted K=V from a map would produce a working file with every
// explanation gone, and the person who opens it next has lost the thing that
// told them what VIBEPANEL_TRUSTED_PROXIES does and why leaving it unset
// matters. Half the variables in the shipped file exist only as commented
// examples under their own paragraph, which is the same point twice.
func TestPatchKeepsTheCommentsAndTheExamples(t *testing.T) {
	p := write(t, shipped)
	if err := PatchEnvFile(p, map[string]string{
		"VIBEPANEL_ADDR":   ":9443",
		"VIBEPANEL_DOMAIN": "panel.example.org",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	body := string(got)

	for _, want := range []string{
		"# Every setting also exists as a flag; the flag wins.",
		"# The public hostname. Also the WebAuthn Relying Party ID.",
		"# off | files | acme",
		"#VIBEPANEL_TLS_MODE=acme",
		"#VIBEPANEL_TMUX_SOCKET=vibepanel",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the file lost %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "VIBEPANEL_ADDR=:9443") {
		t.Errorf("the address was not changed:\n%s", body)
	}
	if strings.Contains(body, "VIBEPANEL_ADDR=:18443") {
		t.Errorf("the old address is still there:\n%s", body)
	}
	// Uncommented in place, so it stays under the paragraph that explains it.
	if !strings.Contains(body, "VIBEPANEL_DOMAIN=panel.example.org") {
		t.Errorf("the domain was not set:\n%s", body)
	}
	i := strings.Index(body, "# The public hostname")
	j := strings.Index(body, "VIBEPANEL_DOMAIN=panel.example.org")
	if i < 0 || j < i || j-i > 120 {
		t.Errorf("the domain was appended at the end instead of set in place:\n%s", body)
	}
}

// Emptying a value comments it out rather than deleting the line.
func TestPatchCommentsOutRatherThanDeletes(t *testing.T) {
	p := write(t, shipped)
	if err := PatchEnvFile(p, map[string]string{"VIBEPANEL_ADDR": ""}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), "#VIBEPANEL_ADDR=:18443") {
		t.Errorf("the line is gone rather than commented:\n%s", body)
	}
	live, err := ReadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := live["VIBEPANEL_ADDR"]; still {
		t.Error("a commented line is still being read as an assignment")
	}
}

// A key the file has never mentioned is appended, once, under a heading.
func TestPatchAppendsWhatIsNotThere(t *testing.T) {
	p := write(t, shipped)
	if err := PatchEnvFile(p, map[string]string{"VIBEPANEL_ALLOW_FROM": "10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(p)
	if n := strings.Count(string(body), "VIBEPANEL_ALLOW_FROM="); n != 1 {
		t.Errorf("appended %d times:\n%s", n, body)
	}
	// And again, to the same file: a second save must not stack a second copy.
	if err := PatchEnvFile(p, map[string]string{"VIBEPANEL_ALLOW_FROM": "10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(p)
	if n := strings.Count(string(body), "VIBEPANEL_ALLOW_FROM="); n != 1 {
		t.Errorf("a second save stacked another copy (%d):\n%s", n, body)
	}
}

// Only the listed keys, and the two that are deliberately not on the list.
//
// CLOUDFLARE_API_TOKEN is a credential and a page that shows it puts it in
// every screenshot of that page. VIBEPANEL_TMUX_SOCKET is red line 1: a panel
// pointed at another socket cannot see its own sessions, and the ones it was
// managing keep running with nothing attached.
func TestPatchRefusesWhatIsNotEditable(t *testing.T) {
	p := write(t, shipped)
	for _, k := range []string{"CLOUDFLARE_API_TOKEN", "VIBEPANEL_TMUX_SOCKET", "PATH"} {
		if err := PatchEnvFile(p, map[string]string{k: "x"}); err == nil {
			t.Errorf("%s was accepted", k)
		}
	}
	body, _ := os.ReadFile(p)
	if string(body) != shipped {
		t.Errorf("a refused write changed the file anyway:\n%s", body)
	}
}

// The original is copied before anything is written.
func TestPatchBacksUpFirst(t *testing.T) {
	p := write(t, shipped)
	if err := PatchEnvFile(p, map[string]string{"VIBEPANEL_ADDR": ":1"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name(), "vibepanel-backup") {
			b, _ := os.ReadFile(filepath.Join(filepath.Dir(p), e.Name()))
			if string(b) != shipped {
				t.Errorf("the backup is not the original:\n%s", b)
			}
			found = true
		}
	}
	if !found {
		t.Error("no backup was written")
	}
}

// Prose containing an `=` is not an assignment.
func TestReadIgnoresProseAndComments(t *testing.T) {
	p := write(t, "# set it to a=b if you like\nVIBEPANEL_ADDR=:1\nnot a key = value\n")
	got, err := ReadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["VIBEPANEL_ADDR"] != ":1" {
		t.Errorf("read %v", got)
	}
}

// A file that has never existed is an empty map and a write that creates it.
func TestPatchCreatesTheFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "vibepanel.env")
	got, err := ReadEnvFile(p)
	if err != nil || len(got) != 0 {
		t.Fatalf("read of a missing file: %v %v", got, err)
	}
	if err := PatchEnvFile(p, map[string]string{"VIBEPANEL_ADDR": ":2"}); err != nil {
		t.Fatal(err)
	}
	back, _ := ReadEnvFile(p)
	if back["VIBEPANEL_ADDR"] != ":2" {
		t.Errorf("read back %v", back)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// It can hold an ACME credential.
	if info.Mode().Perm() != 0o600 {
		t.Errorf("a new env file is mode %o, want 600", info.Mode().Perm())
	}
}
