package httpapi

import "strings"

// sudoSays is the one line of what sudo said that is worth showing somebody.
//
// sudo-rs answers a refused password with two lines: "Authentication failed,
// try again." and then, because stdin had nothing left for its second
// attempt, "Authentication required but not attempted". The page runs text
// from the server through safeText, which turns each newline into a black
// diamond -- which is exactly how it arrived on screen, 「try again.��sudo:
// Authentication required…」. The first line is the answer. The second is
// sudo describing the pipe it was handed.
//
// The password is never in here to strip. It went in on stdin with no prompt
// and no echo, so sudo has nothing of it to print.
func sudoSays(stderr string, err error) string {
	for _, line := range strings.Split(stderr, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	if err != nil {
		return err.Error()
	}
	return "sudo refused and said nothing"
}
