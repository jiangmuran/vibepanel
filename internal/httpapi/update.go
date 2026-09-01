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
	// Whether pressing the button could work, answered here rather than
	// discovered after the download. A system install cannot replace its own
	// binary, and a page that offers to do it anyway is a page that lies.
	out := map[string]any{
		"version": rel.Version, "newer": rel.Newer, "current": rel.Current,
		"url": rel.URL, "notes": rel.Notes, "asset": rel.Asset,
	}
	if err := s.canInstall(); err != nil {
		out["byHand"] = updateByHand(err)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateApply downloads, verifies and installs, then asks the service
// manager to restart the unit.
//
// The version to install is not taken from the request. A client that could
// name it could name any release, including one that is older -- and the
// interesting case is not a mistake, it is somebody who has a session cookie
// and would like this panel to run something else.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	// First, before the network and before the download.
	//
	// A system install puts the binary under /usr/local/bin owned by root and
	// runs the panel as the user, so the swap cannot work -- and it used to
	// find that out after fetching seven megabytes, reporting the raw
	// `permission denied` on a temp file nobody had heard of. Whether this
	// process can replace its own binary does not depend on what GitHub says,
	// so it is not worth a round trip to find out.
	if err := s.canInstall(); err != nil {
		writeErr(w, http.StatusConflict, updateByHand(err))
		return
	}

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
	return restartCommandFor(supervisorName())
}

// restartCommandFor turns "who supervises this" into the command that asks it.
//
// The question is asked once, by supervisorName, and this used to ask it again
// from `INVOCATION_ID` alone -- which restart.go was fixed twice for. That
// variable is inherited by everything a unit spawns, so a panel built and
// started by hand from a pane of a vibepanel session answered "systemd" here
// while handleRestart answered 409 unsupervised for the same process: the page
// said the panel was restarting and it never was, and on a machine that does
// have a unit, `systemctl --user restart vibepanel` went and restarted a
// different, production panel that nobody had updated.
func restartCommandFor(supervisor string) (*exec.Cmd, error) {
	if supervisor != "systemd" {
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

// canInstall is Installable, or whatever a test put in its place.
func (s *Server) canInstall() error {
	if s.installable != nil {
		return s.installable()
	}
	return selfupdate.Installable()
}

// updateByHand turns "cannot write there" into the command that can.
//
// One sentence and one command. The panel deliberately does not try to become
// root: a web console that can escalate is a different program with a
// different threat model, and the whole point of the system unit dropping to
// User= is that this process is not privileged.
func updateByHand(err error) string {
	if !errors.Is(err, selfupdate.ErrNotWritable) {
		return err.Error()
	}
	self, _ := os.Executable()
	if self == "" {
		self = "vibepanel"
	}
	return "this panel cannot replace its own binary: " + err.Error() +
		". Update it from a shell instead: sudo " + self + " service upgrade"
}
