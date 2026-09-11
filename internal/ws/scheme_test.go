package ws

import (
	"os"
	"strings"
	"testing"
)

// TestASchemeReportIsRememberedForTheNextAttach.
//
// internal/session proves that a remembered scheme reaches tmux when a session
// attaches -- TestTmuxIsToldWhichThemeTheBrowserIsOn asks tmux itself. What it
// cannot prove is that anything does the remembering. That is one line here,
// and deleting it leaves every other test in the tree green while a panel
// restart puts every session back to "light": Reconcile attaches them all
// before any browser has spoken, and the six clients read off the live panel
// when this was found all said exactly that.
//
// Read out of the source because Manager does not expose what it remembered,
// and growing an accessor for one test is a public method nothing else calls.
func TestASchemeReportIsRememberedForTheNextAttach(t *testing.T) {
	src, err := os.ReadFile("conn.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	i := strings.Index(code, "func (c *Conn) applyScheme(")
	if i < 0 {
		t.Fatal("applyScheme is gone; this test is checking nothing")
	}
	body := code[i:]
	if j := strings.Index(body, "\n}\n"); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "c.h.Manager.RememberScheme(*dark)") {
		t.Error("a browser's scheme is no longer remembered for sessions that attach later")
	}
}
