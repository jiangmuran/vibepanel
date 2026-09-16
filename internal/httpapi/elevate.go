package httpapi

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/jiangmuran/vibepanel/internal/selfupdate"
)

// ─── upgrading a root-owned binary through sudo ─────────────────────────────
//
// Everything below was decided against real sudo, not read out of a man page:
// sudo 1.9.15 (Ubuntu 24.04) and sudo-rs 0.2.8 (Ubuntu 25.10) in throwaway
// containers, as an unprivileged user with no terminal -- which is how a
// systemd service runs it -- across a password rule, NOPASSWD for everything,
// NOPASSWD for the upgrade command only, a password rule for the upgrade
// command only, rootpw, targetpw, requiretty, lecture=always and a user not in
// sudoers. scripts/sudo-check.sh runs the same matrix against this code.
//
// Three findings shaped it, and each one broke the version before:
//
//  1. Verify-then-run is wrong. The panel used to check the password with
//     `sudo -k -S -- true` and then run the upgrade. A rule that allows only
//     the upgrade refuses `true`, so a machine whose sudoers said "yes, this
//     one command" was told sudo had refused. Checking with `sudo -l <cmd>`
//     instead is worse: classic sudo authenticates `-l` as the invoking user
//     even under rootpw, so the check accepts a password the run then rejects,
//     after the page has said "upgrading". The only question sudo answers the
//     same way as running the command is running the command.
//
//  2. So it runs the command, once, and the first byte the command writes to
//     stdout is the signal that authentication passed. Everything sudo itself
//     says -- prompts, the lecture, sudoers parse warnings, refusals -- goes to
//     stderr, in every variant measured, so stdout is the command's alone.
//     `vibepanel service upgrade` prints a line before it touches the network
//     so that signal does not wait on a download.
//
//  3. Passwordless sudo needs no password. `sudo -k -n -l -- <cmd>` answers
//     whether this exact command runs without one, agreed with the real run
//     in every variant, reads nothing and runs nothing, so the check endpoint
//     uses it to decide whether to show a field at all.
//
//     `-k` is not decoration. Without it a credential cached by something
//     else answers for the rules: in both implementations, after one `sudo -v`
//     in the same session, `-n -l` said "no password needed" under a rule that
//     needs one, and `-k -n -l` said the truth. It was noticed because the
//     machine this was written on answered 0 either way -- which turned out to
//     be `(ALL) NOPASSWD: ALL` in its sudoers, and also the reason the first
//     version of this feature failed there: it validated with `sudo -v`, which
//     wants a password whenever any rule does, before running a command sudo
//     would have run without one.

// sudoPrompt is the prompt sudo prints before reading a password. `%p` is the
// account whose password it wants -- root under rootpw or targetpw, the user
// otherwise, measured in both implementations -- so a refusal can say whose it
// was instead of leaving somebody to type their own password three more times.
const sudoPrompt = "<<vp:%p>>"

// elevatedStartWait bounds how long sudo may take to either start the command
// or refuse. A refusal is a couple of seconds (classic sudo's failure delay);
// a start is immediate. Past this something is waiting on a thing that will
// not come, and the process is killed rather than left holding a password.
var elevatedStartWait = 60 * time.Second

// sudoBin is the sudo this panel would run, or "" when there is none.
func (s *Server) sudoBin() string {
	if s.sudo != "" {
		if _, err := os.Stat(s.sudo); err != nil {
			return ""
		}
		return s.sudo
	}
	p, err := exec.LookPath("sudo")
	if err != nil {
		return ""
	}
	return p
}

// upgradeArgv is the one command sudo is ever asked to run, exactly. A sudoers
// rule written for it -- `NOPASSWD: /usr/local/bin/vibepanel service upgrade`
// -- matches only these arguments, so nothing may be added to them.
func (s *Server) upgradeArgv() []string {
	if len(s.upgradeCommand) > 0 {
		return s.upgradeCommand
	}
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "vibepanel"
	}
	return []string{self, "service", "upgrade"}
}

// sudoEnv is the environment sudo runs in. LC_ALL=C because the refusals are
// matched by their English wording; a translated sudo would fall through to
// "failed" and lose the reason.
func sudoEnv() []string {
	return append(os.Environ(), "LC_ALL=C")
}

// sudoRunsWithoutPassword reports whether sudo would run the upgrade with no
// password at all. Reads nothing, runs nothing.
func (s *Server) sudoRunsWithoutPassword(ctx context.Context, sudo string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sudo, append([]string{"-k", "-n", "-l", "--"}, s.upgradeArgv()...)...)
	cmd.Env = sudoEnv()
	return cmd.Run() == nil
}

// Why sudo did not run the upgrade, as the page needs to know it. The page
// says its own sentence for each; `error` still carries what sudo said.
const (
	// The password was refused. `askedFor` says whose it had to be.
	refusedWrongPassword = "wrongPassword"
	// No password was sent, and this sudo wants one.
	refusedNeedPassword = "needPassword"
	// This account may not run the upgrade through sudo, whatever it types.
	refusedNotAllowed = "notAllowed"
	// requiretty: sudo will only run from a terminal, which a service is not.
	refusedNeedsTTY = "needsTty"
	// This process can never become root through sudo, whatever sudoers says.
	// See cannotElevate.
	refusedCannotElevate = "cannotElevate"
	// Anything else.
	refusedOther = "failed"
)

// cannotElevate reports whether this process has no_new_privs set, under which
// sudo cannot become root at all.
//
// A unit installed before NoNewPrivileges was taken out of the shipped units
// still sets it -- upgrading the binary does not rewrite the unit, and the
// runbook's `confinement` doctor line is about exactly this. Under it, sudo
// fails whatever sudoers says, and the two implementations do not say the same
// thing: sudo 1.9 names the flag, and sudo-rs 0.2.8 says `sudo must be owned by
// uid 0 and have the setuid bit set`, which reads as a broken sudo install.
// Asking the kernel is the answer that does not depend on either. The way out
// is the upgrade from a shell, whose installer rewrites the unit.
//
// A variable so a test can set the answer; the real one is read in a real
// no_new_privs process by scripts/sudo-check.sh.
var cannotElevate = func() bool {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "NoNewPrivs:"); ok {
			return strings.TrimSpace(v) == "1"
		}
	}
	return false
}

var askedForRe = regexp.MustCompile(`<<vp:([^>]*)>>`)

// classifySudo reads why sudo refused, from what it wrote to stderr.
//
// Order matters, and each case is text measured from one implementation or
// the other. A user not in sudoers who types the wrong password gets classic
// sudo's wrong-password lines, not the sudoers one, so "not allowed" can only
// be said when sudo said it. And sudo-rs 0.2.13 follows a refused password
// with "Authentication required but not attempted" -- the pipe running dry on
// its second attempt -- so the wrong-password case has to win over the
// needs-a-password one.
func classifySudo(stderr string) (reason, askedFor string) {
	if m := askedForRe.FindAllStringSubmatch(stderr, -1); len(m) > 0 {
		askedFor = m[len(m)-1][1]
	}
	has := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(stderr, p) {
				return true
			}
		}
		return false
	}
	switch {
	case has(`"no new privileges" flag is set`, "must be owned by uid 0 and have the setuid bit set"):
		return refusedCannotElevate, askedFor
	case has("must have a tty"):
		return refusedNeedsTTY, askedFor
	case has("is not in the sudoers file", "may not run sudo", "I'm afraid I can't do that", "is not allowed to execute"):
		return refusedNotAllowed, askedFor
	case has("incorrect password attempt", "Authentication failed", "incorrect authentication attempts", "Sorry, try again"):
		return refusedWrongPassword, askedFor
	case has("a password is required", "interactive authentication is required", "Authentication required but not attempted"):
		return refusedNeedPassword, askedFor
	}
	return refusedOther, askedFor
}

var (
	sudoRsPromptRe = regexp.MustCompile(`\[sudo: <<vp:[^>]*>>\] Password: ?`)
	sudoersDiagRe  = regexp.MustCompile(`^\S+:\d+:\d+: `)
)

// sudoSays is the one line of what sudo said that is worth showing somebody.
//
// The first line that is sudo talking about this attempt, which takes some
// finding. The page runs server text through safeText, which draws each
// newline as a black diamond -- 「try again.��sudo: Authentication required…」
// is how two lines arrived -- so it has to be one. And ahead of the answer can
// sit: the prompt, glued to the next line by classic sudo and wrapped as
// `[sudo: …] Password: ` by sudo-rs; sudo-rs's complaints about sudoers
// settings it does not know, three lines each (the diagnostic, the line, a
// caret); and classic sudo's lecture.
//
// The password is never in here to strip. It went in on stdin with a prompt
// that does not echo, so sudo has nothing of it to print.
func sudoSays(stderr string, err error) string {
	text := sudoRsPromptRe.ReplaceAllString(stderr, "\n")
	text = askedForRe.ReplaceAllString(text, "\n")
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "":
			continue
		case sudoersDiagRe.MatchString(line):
			i += 2 // the offending line, and the caret under it
			continue
		case isLecture(line):
			continue
		}
		return line
	}
	if err != nil {
		return err.Error()
	}
	return "sudo refused and said nothing"
}

// lectureLines are classic sudo's default lecture, line by line, as sudo
// 1.9.15 prints it. Matched per line rather than as "a paragraph up to the
// blank line", because it has blank lines inside it: skipping to the first one
// left `#1) Respect the privacy of others.` as what sudo said.
var lectureLines = []string{
	"We trust you have received the usual lecture",
	"Administrator. It usually boils down to these three things",
	"#1) Respect the privacy of others.",
	"#2) Think before you type.",
	"#3) With great power comes great responsibility.",
	"For security reasons, the password you type will not be visible.",
}

func isLecture(line string) bool {
	for _, l := range lectureLines {
		if strings.HasPrefix(line, l) {
			return true
		}
	}
	return false
}

// applyElevated runs the upgrade through sudo, with the typed password on
// stdin or, when none was typed, with -n.
func (s *Server) applyElevated(w http.ResponseWriter, r *http.Request, password, expected string) {
	sudo := s.sudoBin()
	if sudo == "" {
		writeErr(w, http.StatusConflict, updateByHand(selfupdate.ErrNotWritable))
		return
	}

	// `-k` always: the rules decide, never a timestamp left by something else.
	// With a live timestamp `sudo -S` does not read stdin, and the line would
	// sit in the pipe for the command to inherit; and without a password a
	// timestamp would let this run where the check said it could not.
	//
	// `-n` when nothing was typed: sudo must fail rather than wait on a
	// terminal that is not there, and the page is then told a password is
	// needed.
	args := []string{"-k", "-n", "--"}
	if password != "" {
		args = []string{"-k", "-S", "-p", sudoPrompt, "--"}
	}

	// Files, not pipes. The upgrade ends by restarting this process, and a
	// pipe whose reader has died kills the next write with SIGPIPE -- the
	// installer's closing lines, or anything that runs after the restart. A
	// file outlives the panel, and is also the log when something goes wrong.
	stdout, err := os.CreateTemp("", "vibepanel-upgrade-*.out")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create the upgrade log: "+err.Error())
		return
	}
	stderr, err := os.CreateTemp("", "vibepanel-upgrade-*.err")
	if err != nil {
		stdout.Close()           //nolint:errcheck // abandoning it
		os.Remove(stdout.Name()) //nolint:errcheck // best effort
		writeErr(w, http.StatusInternalServerError, "could not create the upgrade log: "+err.Error())
		return
	}
	closeLogs := func(remove bool) {
		stdout.Close() //nolint:errcheck // written by the child
		stderr.Close() //nolint:errcheck // written by the child
		if remove {
			os.Remove(stdout.Name()) //nolint:errcheck // best effort
			os.Remove(stderr.Name()) //nolint:errcheck // best effort
		}
	}

	// Not tied to the request: the request ends when the page is answered, and
	// the upgrade has to go on past that.
	cmd := exec.Command(sudo, append(args, s.upgradeArgv()...)...) //nolint:gosec // fixed argv; the password is on stdin only
	cmd.Env = sudoEnv()
	if password != "" {
		cmd.Stdin = strings.NewReader(password + "\n")
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		closeLogs(true)
		writeErr(w, http.StatusInternalServerError, "could not start sudo: "+err.Error())
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	wrote := func() bool {
		fi, err := stdout.Stat()
		return err == nil && fi.Size() > 0
	}
	said := func() string {
		b, _ := os.ReadFile(stderr.Name())
		return string(b)
	}

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(elevatedStartWait)
	defer deadline.Stop()
	for {
		select {
		case runErr := <-done:
			if wrote() {
				if runErr == nil {
					closeLogs(true)
					s.elevatedStarted(w, r, expected)
					return
				}
				// It got past sudo and then failed, before this loop saw it
				// start. Not sudo's refusal, so not a reason the page knows.
				s.Log.Error("elevated upgrade", "err", runErr, "stdout", stdout.Name(), "stderr", stderr.Name())
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error":  "the upgrade started and failed: " + sudoSays(said(), runErr),
					"reason": refusedOther,
				})
				closeLogs(false)
				return
			}
			text := said()
			reason, askedFor := classifySudo(text)
			if runErr == nil {
				reason = refusedOther
			}
			// 403, not 401. The panel session is fine; it is this machine's
			// sudo that refused, and the frontend reads a 401 as "signed out".
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":    sudoSays(text, runErr),
				"reason":   reason,
				"askedFor": askedFor,
			})
			closeLogs(true)
			return
		case <-tick.C:
			if !wrote() {
				continue
			}
			s.elevatedStarted(w, r, expected)
			go func() {
				if err := <-done; err != nil {
					s.Log.Error("elevated upgrade", "err", err, "stdout", stdout.Name(), "stderr", stderr.Name())
					closeLogs(false)
					return
				}
				closeLogs(true)
			}()
			return
		case <-deadline.C:
			_ = cmd.Process.Kill()
			<-done
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{
				"error":  "sudo neither started the upgrade nor refused within a minute: " + sudoSays(said(), nil),
				"reason": refusedOther,
			})
			closeLogs(true)
			return
		}
	}
}

// elevatedStarted answers the page once sudo has let the upgrade run.
//
// The job is recorded as already restarting: the installer is running behind
// this and ends by restarting the unit, and there is nothing this process
// can see of its progress. A page that reloads meanwhile still finds out why
// the socket is about to close.
func (s *Server) elevatedStarted(w http.ResponseWriter, r *http.Request, expected string) {
	name := ""
	if u, ok, err := s.currentUser(r); ok && err == nil {
		name = u.Username
	}
	// The event, never the password. The audit log is read on a settings page,
	// printed into a journal and shipped to whatever collects journals.
	s.audit(r.Context(), "update.elevated", name, s.clientIP(r), "")
	job := &updateJob{
		Stage: "restarting", Version: expected, StartedAt: time.Now().Unix(),
		Total: -1, Elevated: true, Restarting: true,
	}
	s.updates.mu.Lock()
	s.updates.job = job
	s.updates.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"elevated":   true,
		"restarting": true,
		"job":        s.currentJob(),
	})
}

// privilegeHelper names the command a person should type to upgrade from a
// shell: sudo where there is one, doas on the systems that ship that instead
// (Alpine, the BSDs), and su where there is neither.
func privilegeHelper(self string) string {
	cmd := self + " service upgrade"
	if _, err := exec.LookPath("sudo"); err == nil {
		return "sudo " + cmd
	}
	if _, err := exec.LookPath("doas"); err == nil {
		return "doas " + cmd
	}
	return "su -c '" + cmd + "'"
}
