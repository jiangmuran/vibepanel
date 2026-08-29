package config

import (
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/hooks"
)

// Every variable the panel puts inside a session is exempt from the
// "unrecognised setting" report.
//
// Two lists of the same four names, in packages that do not import each other,
// and they had drifted: VIBEPANEL_PROJECT_ID was in hooks.SessionEnv and not
// in the exemption. What that produced is a warning at every start for anybody
// who runs `vibepanel` from a terminal *inside the panel*, telling them one of
// their own settings was not recognised -- and the same stray variable failed
// two unrelated tests in this package, from an environment neither of them was
// about.
//
// The comparison runs this way round on purpose: a name added to SessionEnv
// and forgotten here is the failure. A name exempt here that SessionEnv does
// not set is only dead weight.
func TestEveryHookVariableIsExemptHere(t *testing.T) {
	env := hooks.SessionEnv("s-1", "p-1", "http://127.0.0.1:1/", "tok")
	if len(env) == 0 {
		t.Fatal("SessionEnv returned nothing; this test is comparing against an empty list")
	}
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			t.Errorf("SessionEnv entry %q is not K=V", kv)
			continue
		}
		if !strings.HasPrefix(key, envPrefix) {
			continue
		}
		if !hookSessionVar(key) {
			t.Errorf("%s is injected into every session and is reported as an unrecognised "+
				"setting; add it to hookSessionVar", key)
		}
	}
}
