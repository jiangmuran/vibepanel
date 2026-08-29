package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// supervisorName is whatever would start this process again, or "".
//
// Asked because the button has to be honest: on a machine where nothing
// supervises the panel, "restart" is "stop", and the person who pressed it is
// in a browser tab that is about to stop working with no way to get it back.
//
// The parent is the decisive half, and the first version of this did not have
// it. `INVOCATION_ID` is set by systemd for every unit it starts -- and it is
// inherited, like any other variable, by everything those units go on to
// spawn. So a panel started by hand from a terminal that is itself inside a
// systemd user session sees it and concludes it is a service. Pressing the
// button then stopped the panel for good, which is precisely the case this
// function exists to refuse; it was found by pressing it.
//
// A unit's main process is reparented to pid 1 -- systemd on Linux, launchd on
// a Mac -- and a process started from a shell is not. The variable then only
// says *which* of the two, which is all it is good for.
//
// Being pid 1 is not enough either: that is a container, and whether anything
// restarts a container is the runtime's policy and not something visible from
// in here. Refusing is the safe direction, and `docker compose` says
// `restart: unless-stopped` where somebody can read it.
func supervisorName() string {
	ppid := os.Getppid()
	return supervisorFrom(ppid, parentComm(ppid),
		os.Getenv("INVOCATION_ID"), os.Getenv("XPC_SERVICE_NAME"))
}

// parentComm is the parent process's name, or "" where that cannot be read.
//
// /proc is Linux, and so is the case it answers. Everything else falls back to
// the pid test, which is the right answer on a Mac.
func parentComm(ppid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", ppid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// supervisorFrom is the decision, separated from where the answers come from.
//
// A test cannot give itself a different parent, and the parent is the half
// that matters, so the arguments arrive as arguments.
//
// Two shapes of parent, and the second one cost a round of this. A systemd
// *system* unit and a launchd job are reparented to pid 1. A systemd **user**
// unit is not: its parent is that account's `systemd --user`, an ordinary
// process with an ordinary pid -- and the user unit is the install this
// project recommends wherever there is no root. Testing for pid 1 alone
// refused to restart the most common installation there is, and answered 409
// to a panel that systemd would have brought straight back.
//
// The environment variables still only say *which* supervisor. They are
// inherited by everything a unit spawns, so on their own they are true of a
// shell inside a user session and of everything started from it -- which is
// how the first version of this managed to stop a hand-started panel for good.
func supervisorFrom(ppid int, parent, invocationID, xpcName string) string {
	byInit := ppid == 1
	// `systemd`, not a path: /proc/<pid>/comm is the bare name, and the user
	// manager and pid 1 are the same binary under the same name.
	byManager := parent == "systemd"
	if !byInit && !byManager {
		return ""
	}
	if invocationID != "" {
		return "systemd"
	}
	if xpcName != "" && xpcName != "0" && !strings.HasPrefix(xpcName, "com.apple.Terminal") {
		return "launchd"
	}
	return ""
}

// handleRestart stops the panel and lets the supervisor bring it back.
//
// The sessions are the reason this is a button at all rather than advice to go
// and find a terminal. tmux owns every process; the panel is a client. So a
// restart costs the connection and nothing else, and that is the property the
// whole architecture is built on -- `KillMode=process` in the unit, and
// restart-check in the test suite, exist to keep it true.
//
// Refused rather than attempted when nothing would restart it. A `go build &&
// ./vibepanel` on somebody's laptop is the common case for that, and it is
// also the case where the button is most tempting.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	who := supervisorName()
	if who == "" || s.Restart == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":     false,
			"reason": "unsupervised",
		})
		return
	}

	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "panel.restarted", u.Username, s.clientIP(r), who)
	}

	// Answered before the process goes anywhere. The browser needs the reply
	// to know to start polling for the panel coming back; a connection dropped
	// mid-request looks the same as the panel having crashed.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":         true,
		"supervisor": who,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Non-blocking: the channel is buffered by one and a second click while
	// the first restart is already shutting down is the same request.
	select {
	case s.Restart <- struct{}{}:
	default:
	}
}
