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

// Only the listed keys, and the two that are on no list for different reasons.
//
// VIBEPANEL_TMUX_SOCKET is red line 1: a panel pointed at another socket
// cannot see its own sessions, and the ones it was managing keep running with
// nothing attached. It is refused outright.
//
// CLOUDFLARE_API_TOKEN used to be refused too, on the grounds that a page
// showing it puts an ACME credential in every screenshot. That reason is
// right, and refusing the *write* was the wrong answer to it: it also meant
// the one TLS mode the panel recommends could not be configured from the
// panel, so somebody rotating a token had to go and find the file. It is
// write-only now -- settable, and never in a response -- so what this test
// pins is that it stays out of EditableEnv, which is the list the settings
// page renders values from.
func TestPatchRefusesWhatIsNotEditable(t *testing.T) {
	p := write(t, shipped)
	for _, k := range []string{"VIBEPANEL_TMUX_SOCKET", "PATH"} {
		if err := PatchEnvFile(p, map[string]string{k: "x"}); err == nil {
			t.Errorf("%s was accepted", k)
		}
	}
	body, _ := os.ReadFile(p)
	if string(body) != shipped {
		t.Errorf("a refused write changed the file anyway:\n%s", body)
	}
}

// The key allowlist is only an allowlist while a value is one line.
//
// It was not: the value went into the file verbatim, so
// `VIBEPANEL_DOMAIN=panel.example.com\nVIBEPANEL_TMUX_SOCKET=default` wrote
// both assignments and ReadEnvFile handed the second one back. That is red
// line 1 reached through the settings page -- the panel restarts on somebody
// else's socket, sees their weeks-old sessions and loses its own -- and the
// same shape sets VIBEPANEL_ADDR or VIBEPANEL_ALLOW_FROM. The refusal is here
// rather than in the handler because this is what owns the file format.
func TestPatchRefusesALineBreakInAValue(t *testing.T) {
	for _, v := range []string{
		"panel.example.com\nVIBEPANEL_TMUX_SOCKET=default",
		"panel.example.com\rVIBEPANEL_TMUX_SOCKET=default",
	} {
		p := write(t, shipped)
		if err := PatchEnvFile(p, map[string]string{"VIBEPANEL_DOMAIN": v}); err == nil {
			t.Errorf("%q was accepted", v)
		}
		body, _ := os.ReadFile(p)
		if string(body) != shipped {
			t.Errorf("a refused write changed the file anyway:\n%s", body)
		}
		got, err := ReadEnvFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if s := got["VIBEPANEL_TMUX_SOCKET"]; s != "" {
			t.Errorf("the tmux socket was set to %q from a text box", s)
		}
	}
}

func TestTheAcmeTokenIsWritableAndNeverListed(t *testing.T) {
	p := write(t, shipped)
	if err := PatchEnvFile(p, map[string]string{"CLOUDFLARE_API_TOKEN": "cf-token"}); err != nil {
		t.Fatalf("a write-only setting could not be written: %v", err)
	}
	body, _ := os.ReadFile(p)
	if !strings.Contains(string(body), "CLOUDFLARE_API_TOKEN=cf-token") {
		t.Errorf("the token is not in the file:\n%s", body)
	}

	// Not in the list the settings page renders values from. Adding it there
	// is one line and would put an ACME credential in every screenshot of that
	// page; TestTheAcmeTokenIsNeverInTheResponse is the other half of this.
	for _, k := range EditableEnv {
		if k == "CLOUDFLARE_API_TOKEN" {
			t.Error("CLOUDFLARE_API_TOKEN is in EditableEnv; its value will be sent to the browser")
		}
	}
	if !Secret("CLOUDFLARE_API_TOKEN") {
		t.Error("Secret() no longer knows the token is write-only")
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
