package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every editable key appears in the env file that ships.
//
// Two lists again, in two languages: EditableEnv here, and the paragraphs in
// deploy/vibepanel.env that say what each variable does. A key offered in the
// settings page and absent from the file is one somebody cannot look up, and a
// variable in the file that the page silently will not edit is one they will
// try to edit there and give up on.
//
// The settings page also carries a format hint per key, taken from that same
// file's examples. This does not check those strings -- they are in
// TypeScript -- but it does check the thing they were copied from is still
// there.
func TestEveryEditableKeyIsInTheShippedEnvFile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "vibepanel.env"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, k := range EditableEnv {
		if !strings.Contains(body, k) {
			t.Errorf("%s is editable from the settings page and is not in deploy/vibepanel.env, "+
				"so there is nowhere to read what it does", k)
		}
	}
	// And the two that are deliberately not editable are still described
	// there, because they are still settings -- they are just not settings a
	// text box in a browser should change.
	for _, k := range []string{"VIBEPANEL_TMUX_SOCKET", "CLOUDFLARE_API_TOKEN"} {
		if !strings.Contains(body, k) {
			t.Errorf("%s is no longer documented in deploy/vibepanel.env", k)
		}
	}
}
