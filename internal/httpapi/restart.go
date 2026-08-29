package httpapi

import (
	"net/http"
	"os"
	"strings"
)

// supervisorName is whatever would start this process again, or "".
//
// Asked because the button has to be honest: on a machine where nothing
// supervises the panel, "restart" is "stop", and the person clicking it is in
// a browser tab that is about to stop working with no way to get it back.
//
// INVOCATION_ID is set by systemd for every unit it starts and by nothing
// else. XPC_SERVICE_NAME is launchd's, and is the literal "0" for a process
// started from a shell -- so the value matters, not just its presence.
func supervisorName() string {
	if os.Getenv("INVOCATION_ID") != "" {
		return "systemd"
	}
	if n := os.Getenv("XPC_SERVICE_NAME"); n != "" && n != "0" && !strings.HasPrefix(n, "com.apple.Terminal") {
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
