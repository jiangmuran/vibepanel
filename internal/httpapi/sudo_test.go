package httpapi

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSudoSaysOneLine uses the bytes sudo-rs actually wrote.
//
// Copied from the report rather than composed: a typed password the machine
// refused, shown on the settings page as 「sudo: Authentication failed, try
// again.��sudo: Authentication required but not attempted」. The two diamonds
// are safeText doing its job on two newlines.
func TestSudoSaysOneLine(t *testing.T) {
	raw := "sudo: Authentication failed, try again.\n\nsudo: Authentication required but not attempted\n"
	got := sudoSays(raw, errors.New("exit status 1"))
	if got != "sudo: Authentication failed, try again." {
		t.Errorf("sudoSays = %q, want sudo's first line", got)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("sudoSays kept a line break, which the page draws as a black diamond: %q", got)
	}
	// Said nothing: the process error is still something to show.
	if got := sudoSays(" \n\n", errors.New("exit status 1")); got != "exit status 1" {
		t.Errorf("empty stderr gave %q, want the exit error", got)
	}
}

// TestEverySudoHereReadsThePasswordItself.
//
// Both commands in applyElevated carry `-k -S` and neither carries `-n`.
//
// The first version verified with `-v` and then ran the upgrade with `-n`,
// trusting sudo to carry a credential from one process to the next with no
// terminal attached. sudo-rs keys that cache by terminal or parent process,
// and there was no way to check it without the owner's password -- so the
// design stopped depending on it. `-k` is also what keeps the password out of
// the upgrade's stdin: with a live timestamp, `sudo -S` does not read stdin,
// and the line would be inherited by the command it runs.
//
// Read out of the source, because which flags a process was started with is
// not visible from a request, and running the real thing needs a password
// this suite does not have.
func TestEverySudoHereReadsThePasswordItself(t *testing.T) {
	src, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	i := strings.Index(code, "func (s *Server) applyElevated")
	if i < 0 {
		t.Fatal("applyElevated is gone; this test is checking nothing")
	}
	body := code[i:]
	if j := strings.Index(body, "\n}\n"); j >= 0 {
		body = body[:j]
	}

	calls := regexp.MustCompile(`exec\.CommandContext\([^,]+, sudo, ([^\n]*)\)`).FindAllStringSubmatch(body, -1)
	if len(calls) != 2 {
		t.Fatalf("found %d sudo commands in applyElevated, want the check and the upgrade", len(calls))
	}
	for _, c := range calls {
		args := c[1]
		if !strings.HasPrefix(args, `"-k", "-S"`) {
			t.Errorf("a sudo command does not start -k -S: %s", args)
		}
		if strings.Contains(args, `"-n"`) {
			t.Errorf("a sudo command leans on a cached credential with -n: %s", args)
		}
	}
	if n := strings.Count(body, "Stdin = strings.NewReader(password"); n != len(calls) {
		t.Errorf("%d sudo commands and %d of them given the password on stdin", len(calls), n)
	}
}
