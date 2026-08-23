// Package id generates the opaque identifiers used for projects and sessions.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New returns a 16-character random hex id.
//
// crypto/rand rather than a counter or a timestamp: ids appear in URLs and in
// the environment of every process a session spawns, so they must not let one
// session guess another's.
func New() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform we support; if it somehow
		// does, continuing with a predictable id would be worse than stopping.
		panic(fmt.Sprintf("id: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// TmuxName renders an id as a tmux session name.
//
// The prefix keeps panel sessions recognisable in `tmux ls` output, and the
// fixed length means no generated name is ever a prefix of another — which
// matters because tmux target matching is prefix-based by default.
func TmuxName(sessionID string) string { return "vp_" + sessionID }
