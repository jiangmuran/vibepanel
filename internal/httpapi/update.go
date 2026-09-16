package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/selfupdate"
	"github.com/jiangmuran/vibepanel/internal/version"
)

func (s *Server) registerUpdateRoutes(r chi.Router) {
	r.Get("/update", s.handleUpdateCheck)
	r.Get("/update/status", s.handleUpdateStatus)
	r.Put("/update/settings", s.handlePutUpdateSettings)
	r.Post("/update", s.handleUpdateApply)
}

// ─── what the panel knows about updates, and when it asks ───────────────────
//
// Two things live here between requests, and a page that reloads mid-update
// used to lose both.
//
// The **last check** -- what GitHub said, and when. It is kept in memory and
// written through to the settings table, so a panel that was restarted (by
// the update itself, most often) still knows what it last saw and does not
// show a badge that appears, vanishes and appears again across the restart.
// `Newer` is recomputed against the running version on every read rather than
// trusted from the record, which is what makes the record survive the upgrade
// it describes: after the restart the same record says "not newer".
//
// The **job** -- the one apply in progress. It runs on its own goroutine with
// its own context, because the first version ran on the request's, and a
// phone that put the tab to sleep during the download cancelled the download.
// A second tab, or the same tab reloaded, reads the job's progress from here.
// There is one at a time: a second press while one is running is the same
// request, answered 409.
//
// The check runs on its own only the way internal/git/warm.go refreshes a
// board: because a page asked (`GET /api/update/status`, which the app calls
// when it opens and every half hour after), the last answer is older than
// autoCheckEvery, and the setting is on. Nothing here has a ticker. A panel
// nobody has open does not ask, and a panel that is open asks GitHub four
// times a day at most, with a User-Agent that names the version and nothing
// else about the machine.

const (
	// updateAutoCheckKey is the settings row. Absent means on; "0" means off.
	// Absent-means-on is deliberate: the setting arrived after the feature, and
	// every panel already running is one where nobody has said no.
	updateAutoCheckKey = "update.autoCheck"
	// updateLastCheckKey holds the last check as JSON, see checkRecord.
	updateLastCheckKey = "update.lastCheck"

	// autoCheckEvery is how old the last answer has to be before a page that
	// asks gets a fresh one. Four times a day is well inside GitHub's
	// unauthenticated budget even from behind a busy NAT, and a release is
	// not something anybody needs to hear about within the hour.
	autoCheckEvery = 6 * time.Hour
	// autoRetryAfter is the same when the last check failed. Shorter, so a
	// wifi drop at the moment of the check does not hide a release for six
	// hours; long enough that an air-gapped panel is not asking every poll.
	autoRetryAfter = 30 * time.Minute
)

// checkRecord is one answer from GitHub, or one failure to get it.
type checkRecord struct {
	At      int64              `json:"at"`
	Release selfupdate.Release `json:"release"`
	Error   string             `json:"error,omitempty"`
	Kind    string             `json:"kind,omitempty"`
}

// updateJob is the one apply in progress, as the page reads it.
type updateJob struct {
	// Stage is one of downloading, installing, restarting, installed, failed.
	// `installed` is the end on a panel nothing supervises: the binary is in
	// place and it will run the next time somebody starts it.
	Stage     string `json:"stage"`
	Version   string `json:"version"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   int64  `json:"endedAt,omitempty"`
	// Done and Total are bytes of the archive, while downloading. Total is -1
	// when the server did not say.
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
	// Elevated means the upgrade went through sudo and is running as the
	// installer, outside this process: there is no progress to report and no
	// version to name, only the restart to wait for.
	Elevated bool `json:"elevated,omitempty"`
	// Previous is where the old binary was moved to, once installed.
	Previous   string `json:"previous,omitempty"`
	Restarting bool   `json:"restarting"`
	RestartWhy string `json:"restartWhy,omitempty"`
	// Error and Reason are set with Stage failed. Reason is one of checksum,
	// verify, network, install, restart; Error is the sentence underneath.
	Error  string `json:"error,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// active reports whether a job is still the reason to refuse another.
//
// A download or an install in this process is. A restart is, for a while:
// the process is about to go, and a second press in that window would start
// a second download that the restart cuts off. But not forever -- if the
// restart never happened the job stays at "restarting" with nobody to move
// it on, and the next press must be allowed to try again rather than answer
// "busy" until the process is replaced by hand.
//
// An elevated job never blocks. It runs outside this process, as the
// installer under sudo, and this process cannot see whether it is still
// going; refusing here would stand in front of sudo, which refuses a second
// concurrent run on its own and says so.
func (j *updateJob) active() bool {
	if j == nil || j.Elevated {
		return false
	}
	switch j.Stage {
	case "downloading", "installing":
		return true
	case "restarting":
		return time.Since(time.Unix(j.StartedAt, 0)) < restartGrace
	}
	return false
}

// restartGrace is how long a job at "restarting" still counts as running.
const restartGrace = 3 * time.Minute

// updateStatus is what both GETs answer.
type updateStatus struct {
	Current  string `json:"current"`
	Platform string `json:"platform"`
	// AutoCheck is the setting. Checking is whether a check is in flight now,
	// so a page that arrives during one can say so rather than "never".
	AutoCheck bool  `json:"autoCheck"`
	Checking  bool  `json:"checking"`
	CheckedAt int64 `json:"checkedAt,omitempty"`

	// The release, as selfupdate.Release: empty Version when the repository
	// has none, or when nothing has been checked yet.
	Version     string `json:"version,omitempty"`
	Newer       bool   `json:"newer"`
	URL         string `json:"url,omitempty"`
	Notes       string `json:"notes,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Asset       string `json:"asset,omitempty"`
	// Unreachable is why the last check got no answer, and UnreachableKind is
	// selfupdate.Kind of it. A panel with no route to GitHub is a normal state
	// -- an air-gapped box, a firewall -- and it arrives with 200.
	Unreachable     string `json:"unreachable,omitempty"`
	UnreachableKind string `json:"unreachableKind,omitempty"`

	// Whether pressing the button could work, answered before the download.
	// See handleUpdateCheck's first version for the seven-megabyte way of
	// finding out. Only set when there is something to install.
	ByHand            string `json:"byHand,omitempty"`
	Elevate           bool   `json:"elevate,omitempty"`
	ElevateNoPassword bool   `json:"elevateNoPassword,omitempty"`
	CannotElevate     bool   `json:"cannotElevate,omitempty"`
	ElevateAs         string `json:"elevateAs,omitempty"`

	Job *updateJob `json:"job,omitempty"`
}

// updateState is the process-wide record. The zero value works.
type updateState struct {
	mu     sync.Mutex
	loaded bool
	last   *checkRecord
	// inflight is non-nil while a check runs and closed when it ends, so a
	// second asker waits for the first answer rather than asking again.
	inflight chan struct{}
	job      *updateJob
}

// lastCheck is the record, read from the database the first time.
func (s *Server) lastCheck(ctx context.Context) *checkRecord {
	u := &s.updates
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.loaded {
		u.loaded = true
		if raw, err := s.DB.GetSetting(ctx, updateLastCheckKey, ""); err == nil && raw != "" {
			var rec checkRecord
			if json.Unmarshal([]byte(raw), &rec) == nil && rec.At > 0 {
				u.last = &rec
			}
		}
	}
	return u.last
}

// autoCheckOn reads the setting. Absent is on; see updateAutoCheckKey.
func (s *Server) autoCheckOn(ctx context.Context) bool {
	v, err := s.DB.GetSetting(ctx, updateAutoCheckKey, "1")
	return err != nil || v != "0"
}

// checkNow asks GitHub, once, however many callers arrive at the same time,
// and records the answer.
func (s *Server) checkNow(ctx context.Context) *checkRecord {
	u := &s.updates
	u.mu.Lock()
	if u.inflight != nil {
		// Somebody else is asking. Wait for their answer rather than adding
		// a request of our own to GitHub's count of this address.
		done := u.inflight
		u.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		return s.lastCheck(ctx)
	}
	done := make(chan struct{})
	u.inflight = done
	u.mu.Unlock()

	rec := s.askGitHub(ctx)

	u.mu.Lock()
	u.loaded = true
	u.last = &rec
	u.inflight = nil
	close(done)
	u.mu.Unlock()

	// Written through, and a failure to write is not a failure to check: the
	// answer is in memory, and the next restart asks again.
	if raw, err := json.Marshal(rec); err == nil {
		if err := s.DB.SetSetting(context.WithoutCancel(ctx), updateLastCheckKey, string(raw)); err != nil {
			s.Log.Warn("update check not recorded", "err", err)
		}
	}
	return &rec
}

func (s *Server) askGitHub(ctx context.Context) checkRecord {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rec := checkRecord{At: time.Now().Unix()}
	rel, err := s.Updater.Latest(ctx, version.Version)
	if err != nil {
		rec.Error = err.Error()
		rec.Kind = selfupdate.Kind(err)
		return rec
	}
	rec.Release = rel
	return rec
}

// maybeAutoCheck starts a check in the background when the page's question
// is older than the setting allows. Returns at once.
func (s *Server) maybeAutoCheck(ctx context.Context) {
	if !s.autoCheckOn(ctx) {
		return
	}
	last := s.lastCheck(ctx)
	if last != nil {
		wait := autoCheckEvery
		if last.Error != "" {
			wait = autoRetryAfter
		}
		if time.Since(time.Unix(last.At, 0)) < wait {
			return
		}
	}
	u := &s.updates
	u.mu.Lock()
	busy := u.inflight != nil
	u.mu.Unlock()
	if busy {
		return
	}
	// Its own context: the request that noticed the answer was old is not
	// the one that has to wait for the new one.
	go s.checkNow(context.Background())
}

// currentJob is a copy of the job, or nil.
func (s *Server) currentJob() *updateJob {
	u := &s.updates
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.job == nil {
		return nil
	}
	j := *u.job
	return &j
}

// editJob changes the job under the lock.
func (s *Server) editJob(f func(j *updateJob)) {
	u := &s.updates
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.job != nil {
		f(u.job)
	}
}

// statusView assembles the answer from the record and the job.
//
// `probe` asks for the installability answer even when there is nothing to
// install. The press of the button gets it, so the page can say up front how
// this panel would update; the background poll does not, because the answer
// runs sudo and a poll that runs sudo every minute is a journal full of it.
func (s *Server) statusView(ctx context.Context, rec *checkRecord, probe bool) updateStatus {
	u := &s.updates
	u.mu.Lock()
	checking := u.inflight != nil
	u.mu.Unlock()

	out := updateStatus{
		Current:   version.Version,
		Platform:  selfupdate.Platform(),
		AutoCheck: s.autoCheckOn(ctx),
		Checking:  checking,
		Job:       s.currentJob(),
	}
	if rec == nil {
		return out
	}
	out.CheckedAt = rec.At
	if rec.Error != "" {
		out.Unreachable = rec.Error
		out.UnreachableKind = rec.Kind
		return out
	}
	rel := rec.Release
	out.Version, out.URL, out.Notes, out.PublishedAt, out.Asset = rel.Version, rel.URL, rel.Notes, rel.PublishedAt, rel.Asset
	// Against what is running now, not what was running when the answer was
	// recorded. The record outlives the upgrade it announced.
	out.Newer = selfupdate.IsNewer(version.Version, rel.Version)
	if rel.Version != "" && (out.Newer || probe) {
		s.installability(ctx, &out)
	}
	return out
}

// installability answers whether pressing the button could work, and how.
//
// A system install cannot replace its own binary, and a page that offers to
// do it anyway is a page that lies. So: whether it can write; if not, the
// command that can; whether the page may offer to take a credential instead
// of only printing the command; and whether that credential is needed at all.
func (s *Server) installability(ctx context.Context, out *updateStatus) {
	err := s.canInstall()
	if err == nil {
		return
	}
	out.ByHand = updateByHand(err)
	// Whether the page may offer to ask for the credential instead of only
	// printing the command. Only that the helper exists: whether this
	// account may use it is that program's question, and it answers it a
	// moment later. Reading /etc/sudoers here to guess would be a second
	// implementation of something that already has a real one.
	sudo := s.sudoBin()
	elevate := errors.Is(err, selfupdate.ErrNotWritable) && sudo != ""
	// A field that no password can get past is not offered. The page says
	// why instead, and the shell command in byHand is the way through.
	if elevate && cannotElevate() {
		out.CannotElevate = true
		elevate = false
	}
	out.Elevate = elevate
	// And whether it needs anything typed. A NOPASSWD rule -- for
	// everything, or for the upgrade command alone -- answers yes to
	// `sudo -n -l` for exactly this command, and a page that asks such a
	// machine for a password is asking for something sudo will not read.
	if elevate {
		out.ElevateNoPassword = s.sudoRunsWithoutPassword(ctx, sudo)
	}
	// Whose password sudo will want, by name. The field said "this
	// account's password" and was read, reasonably, as the panel account
	// the person is signed into on this page -- and sudo refused it.
	if u, uerr := user.Current(); uerr == nil {
		out.ElevateAs = u.Username
	}
}

// handleUpdateCheck asks GitHub now, because somebody pressed the button.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	rec := s.checkNow(r.Context())
	writeJSON(w, http.StatusOK, s.statusView(r.Context(), rec, true))
}

// handleUpdateStatus answers from memory, and asks in the background when
// the answer is old enough and the setting says to.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	s.maybeAutoCheck(r.Context())
	writeJSON(w, http.StatusOK, s.statusView(r.Context(), s.lastCheck(r.Context()), false))
}

// handlePutUpdateSettings turns the automatic check on or off.
func (s *Server) handlePutUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoCheck bool `json:"autoCheck"`
	}
	if !decode(w, r, &req) {
		return
	}
	v := "1"
	if !req.AutoCheck {
		v = "0"
	}
	if err := s.DB.SetSetting(r.Context(), updateAutoCheckKey, v); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"autoCheck": req.AutoCheck})
}

// handleUpdateApply starts the download, verification and install, and
// answers as soon as it has started.
//
// The version to install is not taken from the request. A client that could
// name it could name any release, including one that is older -- and the
// interesting case is not a mistake, it is somebody who has a session cookie
// and would like this panel to run something else. What the request may
// carry is `expected`: the version the page showed when the button was
// pressed. If the newest release has changed since, the answer is 409 and the
// page checks again, because what was confirmed is not what would be
// installed.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	// The body carries a password only when one is needed, and it is read
	// before anything else so that it is in memory for the shortest time this
	// handler can arrange.
	var req struct {
		Password string `json:"password"`
		Expected string `json:"expected"`
	}
	if r.ContentLength > 0 {
		if !decode(w, r, &req) {
			return
		}
	}

	if job := s.currentJob(); job.active() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  "an update is already in progress",
			"reason": "busy",
			"job":    job,
		})
		return
	}

	// Whether this process can replace its own binary does not depend on
	// what GitHub says, so it is not worth a round trip to find out. A system
	// install puts the binary under /usr/local/bin owned by root and runs the
	// panel as the user, so the swap cannot work -- and it used to find that
	// out after fetching seven megabytes.
	if err := s.canInstall(); err != nil {
		// Nothing typed can help a panel that cannot write for a reason other
		// than permissions, or one with no sudo to hand it to, so a password is
		// not taken there. An empty one is not refused: passwordless sudo is
		// the common case on a machine somebody set up for this, and it runs
		// with -n. When that sudo does want a password the answer says so, and
		// the page asks.
		if !errors.Is(err, selfupdate.ErrNotWritable) || s.sudoBin() == "" {
			writeErr(w, http.StatusConflict, updateByHand(err))
			return
		}
		// Before sudo is run, not after it fails: under no_new_privs it fails
		// every time, and sudo-rs describes that as a broken sudo install.
		if cannotElevate() {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  updateByHand(err),
				"reason": refusedCannotElevate,
			})
			return
		}
		s.applyElevated(w, r, req.Password, req.Expected)
		return
	}
	if req.Password != "" {
		writeErr(w, http.StatusBadRequest,
			"this panel can replace its own binary; no password is needed and none was used")
		return
	}

	rec := s.checkNow(r.Context())
	if rec == nil || rec.Error != "" {
		msg := "GitHub could not be reached"
		if rec != nil {
			msg = rec.Error
		}
		writeErr(w, http.StatusBadGateway, msg)
		return
	}
	rel := rec.Release
	if !selfupdate.IsNewer(version.Version, rel.Version) {
		// Not an error and not a no-op to be silent about: somebody pressed a
		// button and is owed the reason nothing happened.
		writeErr(w, http.StatusConflict, "there is nothing newer than "+version.Version)
		return
	}
	if rel.Asset == "" {
		writeErr(w, http.StatusConflict, rel.Version+" has no archive for "+selfupdate.Platform())
		return
	}
	if req.Expected != "" && req.Expected != rel.Version {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "the newest release is now " + rel.Version + ", not " + req.Expected + " as shown",
			"reason": "changed",
		})
		return
	}

	name := ""
	if u, ok, err := s.currentUser(r); ok && err == nil {
		name = u.Username
	}
	job := &updateJob{Stage: "downloading", Version: rel.Version, StartedAt: time.Now().Unix(), Total: -1}
	u := &s.updates
	u.mu.Lock()
	if u.job.active() {
		// Two presses in the window between the check above and here.
		j := *u.job
		u.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "an update is already in progress", "reason": "busy", "job": &j,
		})
		return
	}
	u.job = job
	u.mu.Unlock()

	go s.runUpdate(rel, name, s.clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"job": s.currentJob()})
}

// runUpdate is the job: download, verify, swap, restart. On its own goroutine
// and its own context, see the notes at the top of the file.
func (s *Server) runUpdate(rel selfupdate.Release, who, ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	fail := func(reason string, err error) {
		s.Log.Error("update failed", "version", rel.Version, "reason", reason, "err", err)
		s.editJob(func(j *updateJob) {
			j.Stage, j.Reason, j.Error, j.EndedAt = "failed", reason, err.Error(), time.Now().Unix()
		})
	}

	bin, err := s.Updater.Download(ctx, rel, func(done, total int64) {
		s.editJob(func(j *updateJob) { j.Done, j.Total = done, total })
	})
	if err != nil {
		reason := "network"
		if errors.Is(err, selfupdate.ErrChecksum) {
			// Not a network problem. Somebody should look at this.
			reason = "checksum"
		}
		fail(reason, err)
		return
	}

	s.editJob(func(j *updateJob) { j.Stage = "installing" })
	backup, err := s.installBinary(bin, rel.Version)
	if err != nil {
		reason := "install"
		if errors.Is(err, selfupdate.ErrWillNotRun) {
			reason = "verify"
		}
		fail(reason, err)
		return
	}
	s.audit(ctx, "update.installed", who, ip, rel.Version)

	restart, restartErr := s.restartAfterUpdate()
	if restartErr != nil {
		// Installed, and the panel cannot bring itself back. Started by hand
		// it cannot, and it must say so rather than leaving somebody watching
		// a page that will never reconnect.
		s.editJob(func(j *updateJob) {
			j.Stage, j.Previous, j.Restarting, j.RestartWhy, j.EndedAt =
				"installed", backup, false, restartErr.Error(), time.Now().Unix()
		})
		return
	}
	s.editJob(func(j *updateJob) {
		j.Stage, j.Previous, j.Restarting = "restarting", backup, true
	})
	// A moment for the page to read that answer before the process goes: the
	// restart ends this process, and a job that never reached "restarting"
	// is a page that never knew why the socket closed.
	time.Sleep(1500 * time.Millisecond)
	if out, err := restart.CombinedOutput(); err != nil {
		s.Log.Error("restart after update", "err", err, "out", string(out))
		s.editJob(func(j *updateJob) {
			j.Stage, j.Restarting, j.EndedAt = "installed", false, time.Now().Unix()
			j.RestartWhy = strings.TrimSpace(err.Error() + ": " + string(out))
		})
	}
}

// installBinary is selfupdate.Install, or whatever a test put in its place:
// the real one replaces the running executable, which in a test is the test
// binary.
func (s *Server) installBinary(bin []byte, ver string) (string, error) {
	if s.install != nil {
		return s.install(bin, ver)
	}
	return selfupdate.Install(bin, ver)
}

// restartAfterUpdate is restartCommand, or a test's stand-in.
func (s *Server) restartAfterUpdate() (*exec.Cmd, error) {
	if s.restartCmd != nil {
		return s.restartCmd()
	}
	return restartCommand()
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
		". Update it from a shell instead: " + privilegeHelper(self)
}
