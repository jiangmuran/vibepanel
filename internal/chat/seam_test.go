package chat

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The seam is real only while the bridge imports no adapter. A single import
// of an IM package here would compile, work, and quietly make the bridge
// "the Telegram one": this is the pin.
func TestTheBridgeImportsNoAdapter(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(dep, "/internal/chat/") {
			t.Errorf("the bridge imports %s", dep)
		}
	}
}

// The states and kinds a rule may name are the panel's own enums, spelt
// once. A rule naming a state no session has would match nothing forever,
// with nothing on the page to say so.
func TestRulesValidateAgainstTheRealEnums(t *testing.T) {
	for _, st := range session.AllStates {
		r := Routes{Default: Rule{To: []string{"*"}, Match: Match{States: []string{string(st)}}}}
		if err := r.Validate(); err != nil {
			t.Errorf("state %q refused: %v", st, err)
		}
	}
	for _, k := range []string{store.MessageAssistant, store.MessagePrompt, store.MessageQuestion, store.MessageNotice, store.MessageUser} {
		r := Routes{Default: Rule{To: []string{"*"}, Match: Match{Kinds: []string{k}}}}
		if err := r.Validate(); err != nil {
			t.Errorf("kind %q refused: %v", k, err)
		}
	}
	for _, bad := range []Routes{
		{Default: Rule{To: []string{"*"}, Match: Match{States: []string{"asleep"}}}},
		{Default: Rule{To: []string{"*"}, Match: Match{Kinds: []string{"shout"}}}},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted %+v", bad.Default.Match)
		}
	}
}
