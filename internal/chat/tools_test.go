package chat

import "testing"

func TestAgentForPrefersTheForegroundProcess(t *testing.T) {
	cases := []struct {
		launch  []string
		current string
		want    string
	}{
		{[]string{"claude"}, "claude", "claude"},
		{[]string{"/usr/local/bin/codex", "--full-auto"}, "node", "codex"},
		{nil, "claude", "claude"},
		{[]string{"bash"}, "claude", "claude"},
		{[]string{"codex"}, "bash", "codex"}, // dropped to a shell inside; still Codex's session
		{nil, "bash", "shell"},
		{[]string{"zsh"}, "vim", "shell"},
		{[]string{"kimi"}, "node", "kimi"},
		{[]string{"zcode"}, "", "zcode"},
	}
	for _, c := range cases {
		if got := AgentFor(c.launch, c.current); got != c.want {
			t.Errorf("AgentFor(%v, %q) = %q, want %q", c.launch, c.current, got, c.want)
		}
	}
}

func TestToolProfilesRefuseKeysThatAreText(t *testing.T) {
	raw := `{"codex":{"approve":["y"],"deny":["n"],"interrupt":["C-c"],"submit":["Enter"]},
	         "evil":{"approve":["rm -rf /"],"deny":["n"],"interrupt":["Escape"],"submit":["Enter"]},
	         "claude":{"approve":["1","Enter"],"deny":["Escape"],"interrupt":["Escape"],"submit":["Enter"]}}`
	p := ParseTools(raw)
	if _, ok := p["evil"]; ok {
		t.Fatal("a profile with a space in a key was accepted")
	}
	if p["codex"].Interrupt[0] != "C-c" {
		t.Fatalf("codex interrupt not read: %+v", p["codex"])
	}
	if len(p["claude"].Approve) != 2 || p["claude"].Approve[0] != "1" {
		t.Fatalf("claude approve not read: %+v", p["claude"])
	}
	// Defaults fill what the row does not mention.
	if _, ok := p["opencode"]; !ok {
		t.Fatal("opencode default missing")
	}
	if _, ok := p["shell"]; !ok || len(p["shell"].Approve) != 0 {
		t.Fatalf("shell profile: %+v", p["shell"])
	}
	if ParseTools("{bad")["codex"].Approve[0] != "y" {
		t.Fatal("an unparseable row did not fall back to defaults")
	}
}
