package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/selfupdate"
	"github.com/jiangmuran/vibepanel/internal/version"
)

func (s *Server) registerUpdateRoutes(r chi.Router) {
	r.Get("/update", s.handleUpdateCheck)
	r.Post("/update", s.handleUpdateApply)
}

// handleUpdateCheck asks GitHub what the newest release is.
//
// On demand only. A self-hosted panel that reaches out on its own schedule is
// a surprise, and this one has no telemetry of any kind; every request this
// makes happens because somebody pressed something.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rel, err := s.Updater.Latest(ctx, version.Version)
	if err != nil {
		// A panel with no route to GitHub is a normal state -- an air-gapped
		// box, a firewall, a repository that has never cut a release. Saying so
		// is the answer; 500 would read as the panel being broken.
		writeJSON(w, http.StatusOK, map[string]any{
			"current":     version.Version,
			"unreachable": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, rel)
}

// handleUpdateApply downloads, verifies and installs, then asks the service
// manager to restart the unit.
//
// The version to install is not taken from the request. A client that could
// name it could name any release, including one that is older -- and the
// interesting case is not a mistake, it is somebody who has a session cookie
// and would like this panel to run something else.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	rel, err := s.Updater.Latest(ctx, version.Version)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !rel.Newer {
		// Not an error and not a no-op to be silent about: somebody pressed a
		// button and is owed the reason nothing happened.
		writeErr(w, http.StatusConflict, "there is nothing newer than "+version.Version)
		return
	}
	bin, err := s.Updater.Fetch(ctx, rel)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, selfupdate.ErrChecksum) {
			// Not a network problem. Somebody should look at this.
			status = http.StatusUnprocessableEntity
		}
		writeErr(w, status, err.Error())
		return
	}
	backup, err := selfupdate.Install(bin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := ""
	if u, ok, err := s.currentUser(r); ok && err == nil {
		name = u.Username
	}
	s.audit(r.Context(), "update.installed", name, s.clientIP(r), rel.Version)

	restart, restartErr := restartCommand()
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": rel.Version,
		"previous":  backup,
		// Whether the panel can bring itself back. Running under systemd it
		// can; started by hand it cannot, and it must say so rather than
		// leaving somebody watching a page that will never reconnect.
		"restarting": restartErr == nil,
		"restartWhy": errText(restartErr),
	})

	if restartErr != nil {
		return
	}
	// After the response, and from a goroutine: the restart ends this process,
	// and a handler that never returns is a page that never gets its answer.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if out, err := restart.CombinedOutput(); err != nil {
			s.Log.Error("restart after update", "err", err, "out", string(out))
		}
	}()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// restartCommand works out how to ask for a restart, or says why it cannot.
//
// KillMode=process in both units is what makes this safe to do at all: systemd
// stops the panel and leaves the tmux server and every agent under it alone.
// Without that line this would be a button that kills everybody's work.
func restartCommand() (*exec.Cmd, error) {
	if _, ok := os.LookupEnv("INVOCATION_ID"); !ok {
		return nil, errors.New("not running under systemd, so the new binary starts the next time you start it yourself")
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return nil, errors.New("systemctl is not on PATH")
	}
	// A system unit runs as this user through User=, so euid alone cannot tell
	// the two apart. The user manager can: `--user` fails outright when there
	// is no session bus, which is exactly the system-unit case.
	if os.Getenv("XDG_RUNTIME_DIR") != "" {
		return exec.Command(systemctl, "--user", "restart", "vibepanel"), nil
	}
	if os.Geteuid() == 0 {
		return exec.Command(systemctl, "restart", "vibepanel"), nil
	}
	return nil, errors.New("this looks like a system unit, and restarting it needs root: run `sudo systemctl restart vibepanel`")
}
