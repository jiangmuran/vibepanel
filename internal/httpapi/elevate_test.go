package httpapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/selfupdate"
)

// The elevated upgrade is tested against what sudo actually says.
//
// testdata/sudo holds stderr, stdout and the exit code of sudo 1.9.15 and
// sudo-rs 0.2.8, recorded in containers as an unprivileged user with no
// terminal -- the way a systemd service runs it -- one file set per case, plus
// the lines this machine's sudo-rs 0.2.13 produced. Nothing below composes
// sudo's wording: the classifier reads the recordings, and the fake sudo the
// HTTP tests run replays them. scripts/sudo-check.sh runs the same variants
// against real sudo, which is where the recordings come from and what would
// notice them going stale.

// TestMain lets this test binary stand in for sudo. The server is pointed at
// os.Args[0] with VP_FAKE_SUDO set, so each `sudo ...` it runs lands in
// fakeSudo instead of in the tests.
func TestMain(m *testing.M) {
	if variant := os.Getenv("VP_FAKE_SUDO"); variant != "" {
		os.Exit(fakeSudo(variant))
	}
	os.Exit(m.Run())
}

// fakeRecord is what one run of the fake sudo saw, for the tests to check.
type fakeRecord struct {
	Argv          []string `json:"argv"`
	Env           []string `json:"env"`
	ReadPassword  bool     `json:"readPassword"`
	StdoutRegular bool     `json:"stdoutRegular"`
	StderrRegular bool     `json:"stderrRegular"`
}

// fakeSudo behaves as sudo would under one sudoers variant, by replaying the
// recording of that case. The variants and the recordings are the same matrix
// scripts/sudo-check.sh runs for real.
func fakeSudo(variant string) int {
	data := filepath.Join(os.Getenv("VP_FAKE_SUDO_DATA"), os.Getenv("VP_FAKE_SUDO_IMPL"))
	args := os.Args[1:]
	var nonInteractive, readStdin, list bool
	i := 0
flags:
	for ; i < len(args); i++ {
		switch args[i] {
		case "-k":
		case "-n":
			nonInteractive = true
		case "-S":
			readStdin = true
		case "-l":
			list = true
		case "-p":
			i++
		case "--":
			i++
			break flags
		default:
			break flags
		}
	}
	command := args[i:]

	typed := ""
	if readStdin {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		typed = strings.TrimSuffix(line, "\n")
	}
	regular := func(f *os.File) bool {
		fi, err := f.Stat()
		return err == nil && fi.Mode().IsRegular()
	}
	if logPath := os.Getenv("VP_FAKE_SUDO_LOG"); logPath != "" {
		rec, _ := json.Marshal(fakeRecord{
			Argv: os.Args[1:], Env: os.Environ(), ReadPassword: typed != "",
			StdoutRegular: regular(os.Stdout), StderrRegular: regular(os.Stderr),
		})
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, string(rec)) //nolint:errcheck // test log
			f.Close()                    //nolint:errcheck // test log
		}
	}

	replay := func(name string) int {
		b, err := os.ReadFile(filepath.Join(data, name+".stderr"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake sudo: no recording %s/%s: %v\n", data, name, err)
			return 99
		}
		os.Stderr.Write(b) //nolint:errcheck // replaying
		out, _ := os.ReadFile(filepath.Join(data, name+".stdout"))
		os.Stdout.Write(out) //nolint:errcheck // replaying
		code, _ := os.ReadFile(filepath.Join(data, name+".exit"))
		n, _ := strconv.Atoi(strings.TrimSpace(string(code)))
		return n
	}
	// runAfter replays sudo's own part of a recording that went on to run the
	// command -- the prompt, a lecture, parse warnings -- and then runs it.
	runAfter := func(name string) int {
		b, err := os.ReadFile(filepath.Join(data, name+".stderr"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake sudo: no recording %s/%s: %v\n", data, name, err)
			return 99
		}
		os.Stderr.Write(b)                              //nolint:errcheck // replaying
		cmd := exec.Command(command[0], command[1:]...) //nolint:gosec // test
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			return 98
		}
		return 0
	}
	isUpgrade := strings.Join(command, "\x1f") == os.Getenv("VP_FAKE_UPGRADE")
	// The passwords sudo would accept come from a file rather than the
	// environment, so that the environment the panel hands sudo can be checked
	// for them and the check means something.
	pws, _ := os.ReadFile(os.Getenv("VP_FAKE_PASSWORDS"))
	userPW, rootPW, _ := strings.Cut(strings.TrimSpace(string(pws)), "\n")
	right := typed != "" && typed == userPW
	classic := os.Getenv("VP_FAKE_SUDO_IMPL") == "classic"

	switch variant {
	case "hang":
		time.Sleep(time.Minute)
		return 1
	case "password":
		switch {
		case list:
			return replay("password-probe")
		case nonInteractive:
			return replay("password-none")
		case right:
			return runAfter("password-right")
		}
		return replay("password-wrong")
	case "nopasswd":
		if list {
			return replay("nopasswd-probe")
		}
		return runAfter("nopasswd-none")
	case "nopasswd-only":
		if !isUpgrade {
			return replay("password-none")
		}
		if list {
			return replay("nopasswd-only-probe")
		}
		return runAfter("nopasswd-only-none")
	case "only-upgrade":
		switch {
		case !isUpgrade:
			return replay("only-upgrade-true")
		case list:
			return replay("password-probe")
		case nonInteractive:
			return replay("password-none")
		case right:
			return runAfter("only-upgrade-right")
		}
		return replay("password-wrong")
	case "rootpw":
		switch {
		case list:
			return replay("password-probe")
		case nonInteractive:
			return replay("password-none")
		case typed != "" && typed == rootPW:
			return runAfter("rootpw-rootpw")
		}
		return replay("rootpw-userpw")
	case "notsudoer":
		switch {
		case nonInteractive:
			return replay("notsudoer-none")
		case right:
			return replay("notsudoer-right")
		}
		return replay("notsudoer-wrong")
	case "requiretty":
		if classic {
			return replay("requiretty-right")
		}
		if right {
			return runAfter("requiretty-right")
		}
		return replay("password-wrong")
	case "lecture":
		switch {
		case nonInteractive:
			return replay("password-none")
		case right && classic:
			return runAfter("lecture-right")
		case right:
			return runAfter("lecture-right")
		}
		return replay("lecture-wrong")
	}
	fmt.Fprintf(os.Stderr, "fake sudo: unknown variant %q\n", variant)
	return 97
}

// ─── the recordings, read for what they mean ────────────────────────────────

// TestSudoIsReadForWhatItMeant.
//
// Every refusal recorded, from both implementations, with the reason the page
// is given, whose password sudo wanted, and the one line shown. The failures
// this replaced: a user whose sudoers allowed only the upgrade was told sudo
// refused; classic sudo's lecture arrived as the answer to a wrong password;
// sudo-rs's complaints about sudoers settings did the same.
//
// Every recording in testdata/sudo must be in this table or in the list of
// runs that succeed, so one added later cannot sit there unread.
func TestSudoIsReadForWhatItMeant(t *testing.T) {
	type want struct{ reason, askedFor, says string }
	refusals := map[string]want{
		"classic/password-wrong":    {refusedWrongPassword, "u", "Sorry, try again."},
		"classic/password-none":     {refusedNeedPassword, "", "sudo: a password is required"},
		"classic/password-probe":    {refusedNeedPassword, "", "sudo: a password is required"},
		"classic/rootpw-userpw":     {refusedWrongPassword, "root", "Sorry, try again."},
		"classic/targetpw-userpw":   {refusedWrongPassword, "root", "Sorry, try again."},
		"classic/notsudoer-right":   {refusedNotAllowed, "u", "u is not in the sudoers file."},
		"classic/only-upgrade-true": {refusedNotAllowed, "u", "Sorry, user u is not allowed to execute '/usr/bin/true' as root on vphost."},
		"classic/requiretty-right":  {refusedNeedsTTY, "", "sudo: sorry, you must have a tty to run sudo"},
		"classic/lecture-wrong":     {refusedWrongPassword, "u", "Sorry, try again."},
		// Classic sudo cannot tell these apart from their neighbours, and says
		// so: a wrong password from somebody not in sudoers is a wrong
		// password, and -n from them is "a password is required". The page
		// asks, and the next answer is the real one.
		"classic/notsudoer-wrong": {refusedWrongPassword, "u", "Sorry, try again."},
		"classic/notsudoer-none":  {refusedNeedPassword, "", "sudo: a password is required"},

		"sudo-rs/password-wrong":    {refusedWrongPassword, "u", "sudo: Authentication failed, try again."},
		"sudo-rs/password-none":     {refusedNeedPassword, "", "sudo-rs: interactive authentication is required"},
		"sudo-rs/password-probe":    {refusedNeedPassword, "", "sudo-rs: interactive authentication is required"},
		"sudo-rs/rootpw-userpw":     {refusedWrongPassword, "root", "sudo: Authentication failed, try again."},
		"sudo-rs/targetpw-userpw":   {refusedWrongPassword, "root", "sudo: Authentication failed, try again."},
		"sudo-rs/notsudoer-right":   {refusedNotAllowed, "", "sudo-rs: I'm sorry u. I'm afraid I can't do that"},
		"sudo-rs/notsudoer-wrong":   {refusedNotAllowed, "", "sudo-rs: I'm sorry u. I'm afraid I can't do that"},
		"sudo-rs/notsudoer-none":    {refusedNotAllowed, "", "sudo-rs: I'm sorry u. I'm afraid I can't do that"},
		"sudo-rs/only-upgrade-true": {refusedNotAllowed, "", "sudo-rs: I'm sorry u. I'm afraid I can't do that"},
		"sudo-rs/lecture-wrong":     {refusedWrongPassword, "u", "sudo: Authentication failed, try again."},

		// no_new_privs, which the panel checks before running sudo at all.
		// sudo-rs's line reads like a broken install; it is not.
		"classic/nnp-none":     {refusedCannotElevate, "", `sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`},
		"classic/nnp-probe":    {refusedCannotElevate, "", `sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`},
		"classic/nnp-password": {refusedCannotElevate, "", `sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`},
		"sudo-rs/nnp-none":     {refusedCannotElevate, "", "sudo-rs: sudo must be owned by uid 0 and have the setuid bit set"},
		"sudo-rs/nnp-probe":    {refusedCannotElevate, "", "sudo-rs: sudo must be owned by uid 0 and have the setuid bit set"},
		"sudo-rs/nnp-password": {refusedCannotElevate, "", "sudo-rs: sudo must be owned by uid 0 and have the setuid bit set"},

		// This machine's, verbatim from the report: the refused password, then
		// sudo-rs describing the pipe it was handed.
		"sudo-rs-0.2.13/password-wrong": {refusedWrongPassword, "", "sudo: Authentication failed, try again."},
	}
	succeed := map[string]bool{
		"classic/password-right": true, "classic/nopasswd-none": true, "classic/nopasswd-probe": true,
		"classic/only-upgrade-right": true, "classic/nopasswd-only-probe": true, "classic/nopasswd-only-none": true,
		"classic/rootpw-rootpw": true, "classic/lecture-right": true,
		"sudo-rs/password-right": true, "sudo-rs/nopasswd-none": true, "sudo-rs/nopasswd-probe": true,
		"sudo-rs/only-upgrade-right": true, "sudo-rs/nopasswd-only-probe": true, "sudo-rs/nopasswd-only-none": true,
		"sudo-rs/rootpw-rootpw": true, "sudo-rs/lecture-right": true,
		// sudo-rs does not know requiretty, warns, and runs.
		"sudo-rs/requiretty-right":      true,
		"sudo-rs-0.2.13/nopasswd-none":  true,
		"sudo-rs-0.2.13/nopasswd-probe": true,
	}

	files, err := filepath.Glob("testdata/sudo/*/*.exit")
	if err != nil || len(files) == 0 {
		t.Fatalf("no recordings found (%v); this test is checking nothing", err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(f), "testdata/sudo/"), ".exit")
		code, _ := os.ReadFile(f)
		stderr, _ := os.ReadFile(strings.TrimSuffix(f, ".exit") + ".stderr")
		exit := strings.TrimSpace(string(code))

		if succeed[name] {
			if exit != "0" {
				t.Errorf("%s: listed as succeeding, but sudo exited %s", name, exit)
			}
			continue
		}
		w, ok := refusals[name]
		if !ok {
			t.Errorf("%s: a recording no test reads; add what it means to this table", name)
			continue
		}
		if exit == "0" {
			t.Errorf("%s: listed as a refusal, but sudo exited 0", name)
		}
		reason, askedFor := classifySudo(string(stderr))
		if reason != w.reason {
			t.Errorf("%s: reason = %q, want %q", name, reason, w.reason)
		}
		if askedFor != w.askedFor {
			t.Errorf("%s: askedFor = %q, want %q", name, askedFor, w.askedFor)
		}
		says := sudoSays(string(stderr), errors.New("exit status 1"))
		if says != w.says {
			t.Errorf("%s: shown as %q, want %q", name, says, w.says)
		}
		if strings.ContainsAny(says, "\r\n") {
			t.Errorf("%s: the line shown has a line break, which the page draws as a black diamond: %q", name, says)
		}
	}
	for name := range refusals {
		if _, err := os.Stat("testdata/sudo/" + name + ".exit"); err != nil {
			t.Errorf("%s: in the table, but there is no such recording", name)
		}
	}

	if got := sudoSays(" \n\n", errors.New("exit status 1")); got != "exit status 1" {
		t.Errorf("sudo said nothing: shown as %q, want the exit error", got)
	}
}

// ─── the whole path, over HTTP, against each variant ────────────────────────

const (
	fakeUserPW = "user-pw-9f2c"
	fakeRootPW = "root-pw-47b1"
)

type sudoRig struct {
	ts  *httptest.Server
	srv *Server
	ran string
	log string
}

// newSudoRig is a signed-in panel that cannot write its own binary, whose
// sudo is the fake above behaving as `variant` of `impl`.
func newSudoRig(t *testing.T, impl, variant string) *sudoRig {
	t.Helper()
	ts, srv := newTestServer(t)
	srv.installable = func() error { return fmt.Errorf("%w: /usr/local/bin", selfupdate.ErrNotWritable) }
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	srv.sudo = self

	dir := t.TempDir()
	rig := &sudoRig{ts: ts, srv: srv, ran: filepath.Join(dir, "ran"), log: filepath.Join(dir, "sudo.log")}
	// The upgrade prints first, like `service upgrade`, and leaves a mark.
	srv.upgradeCommand = []string{"/bin/sh", "-c", `echo "vibepanel: handing over to the installer"; echo ran >> "$VP_FAKE_RAN"`}

	data, err := filepath.Abs("testdata/sudo")
	if err != nil {
		t.Fatal(err)
	}
	// Set after the server exists, so nothing it started carries them.
	t.Setenv("VP_FAKE_SUDO", variant)
	t.Setenv("VP_FAKE_SUDO_IMPL", impl)
	t.Setenv("VP_FAKE_SUDO_DATA", data)
	t.Setenv("VP_FAKE_SUDO_LOG", rig.log)
	t.Setenv("VP_FAKE_RAN", rig.ran)
	pwFile := filepath.Join(dir, "passwords")
	if err := os.WriteFile(pwFile, []byte(fakeUserPW+"\n"+fakeRootPW+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VP_FAKE_PASSWORDS", pwFile)
	t.Setenv("VP_FAKE_UPGRADE", strings.Join(srv.upgradeCommand, "\x1f"))
	return rig
}

type applyAnswer struct {
	code     int
	Error    string `json:"error"`
	Reason   string `json:"reason"`
	AskedFor string `json:"askedFor"`
	Elevated bool   `json:"elevated"`
}

func (r *sudoRig) apply(t *testing.T, password string) applyAnswer {
	t.Helper()
	body := `{}`
	if password != "" {
		body = `{"password":` + strconv.Quote(password) + `}`
	}
	code, raw := send(t, r.ts, http.MethodPost, "/api/update", body)
	var a applyAnswer
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("apply answered %d with something that is not JSON: %s", code, raw)
	}
	a.code = code
	for _, pw := range []string{fakeUserPW, fakeRootPW} {
		if strings.Contains(raw, pw) {
			t.Errorf("the answer carries a password: %s", raw)
		}
	}
	return a
}

// ranCount is how many times the upgrade command has run, waiting briefly for
// one that was started a moment ago.
func (r *sudoRig) ranCount(t *testing.T, want int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(r.ran)
		n := strings.Count(string(b), "ran\n")
		if n >= want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (r *sudoRig) records(t *testing.T) []fakeRecord {
	t.Helper()
	b, err := os.ReadFile(r.log)
	if err != nil {
		return nil
	}
	var out []fakeRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec fakeRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("fake sudo log: %v: %s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// checkRecords holds for every sudo this panel ran: the password reached it on
// stdin and nowhere else, only the upgrade command was ever run, `-k` always
// kept a timestamp from deciding, and the output went to files that outlive
// the panel restarting, not pipes that would SIGPIPE the installer.
func (r *sudoRig) checkRecords(t *testing.T) {
	t.Helper()
	upgrade := strings.Join(r.srv.upgradeCommand, "\x1f")
	for _, rec := range r.records(t) {
		argv := strings.Join(rec.Argv, "\x1f")
		for _, pw := range []string{fakeUserPW, fakeRootPW} {
			if strings.Contains(argv, pw) {
				t.Errorf("a password is on sudo's command line: %q", rec.Argv)
			}
			for _, kv := range rec.Env {
				if strings.Contains(kv, pw) {
					t.Errorf("a password is in sudo's environment: %s", kv)
				}
			}
		}
		if !strings.HasSuffix(argv, "--\x1f"+upgrade) {
			t.Errorf("sudo was asked to run something other than the upgrade: %q", rec.Argv)
		}
		if len(rec.Argv) == 0 || rec.Argv[0] != "-k" {
			t.Errorf("sudo ran without -k, so a cached credential could decide: %q", rec.Argv)
		}
		list := strings.Contains(argv, "\x1f-l\x1f")
		if !list && (!rec.StdoutRegular || !rec.StderrRegular) {
			t.Errorf("the upgrade's output is not a file (stdout %v, stderr %v); a pipe dies with the panel and kills the installer",
				rec.StdoutRegular, rec.StderrRegular)
		}
	}
}

// Subtest names are short on purpose: the test server's tmux socket path carries
// the full test name, and past about a hundred bytes tmux cannot bind it.
func TestElevatedUpgrade(t *testing.T) {
	for _, impl := range []string{"classic", "sudo-rs"} {
		t.Run(impl, func(t *testing.T) {
			t.Run("password", func(t *testing.T) {
				r := newSudoRig(t, impl, "password")
				if r.srv.sudoRunsWithoutPassword(t.Context(), r.srv.sudo) {
					t.Error("the check says no password is needed under a rule that needs one")
				}
				if a := r.apply(t, ""); a.code != http.StatusForbidden || a.Reason != refusedNeedPassword {
					t.Errorf("nothing typed: %d %q %q, want 403 %s", a.code, a.Reason, a.Error, refusedNeedPassword)
				}
				if a := r.apply(t, "not-it"); a.code != http.StatusForbidden || a.Reason != refusedWrongPassword || a.AskedFor != "u" {
					t.Errorf("a wrong password: %d %q for %q (%q), want 403 %s for u", a.code, a.Reason, a.AskedFor, a.Error, refusedWrongPassword)
				}
				if n := r.ranCount(t, 0); n != 0 {
					t.Fatalf("the upgrade ran %d times before sudo let it", n)
				}
				if a := r.apply(t, fakeUserPW); a.code != http.StatusOK || !a.Elevated {
					t.Errorf("the right password: %d %q, want 200 elevated", a.code, a.Error)
				}
				if n := r.ranCount(t, 1); n != 1 {
					t.Errorf("the upgrade ran %d times, want once", n)
				}
				recs := r.records(t)
				sawTyped := false
				for _, rec := range recs {
					if rec.ReadPassword {
						sawTyped = true
						if !strings.Contains(strings.Join(rec.Argv, " "), "-S -p <<vp:%p>> --") {
							t.Errorf("a password was sent without -S and the prompt that names whose it is: %q", rec.Argv)
						}
					}
				}
				if !sawTyped {
					t.Error("no sudo read a password from stdin")
				}
				r.checkRecords(t)
			})

			// The regression this rewrite exists for: the rule allows the
			// upgrade and nothing else, and the old check ran `true` first.
			t.Run("only-upgrade", func(t *testing.T) {
				r := newSudoRig(t, impl, "only-upgrade")
				if a := r.apply(t, fakeUserPW); a.code != http.StatusOK {
					t.Errorf("the right password: %d %q %q, want 200", a.code, a.Reason, a.Error)
				}
				if n := r.ranCount(t, 1); n != 1 {
					t.Errorf("the upgrade ran %d times, want once", n)
				}
				r.checkRecords(t)
			})

			for _, variant := range []string{"nopasswd", "nopasswd-only"} {
				t.Run(variant, func(t *testing.T) {
					r := newSudoRig(t, impl, variant)
					if !r.srv.sudoRunsWithoutPassword(t.Context(), r.srv.sudo) {
						t.Error("the check asks for a password sudo would not read")
					}
					if a := r.apply(t, ""); a.code != http.StatusOK || !a.Elevated {
						t.Errorf("nothing typed: %d %q %q, want 200 elevated", a.code, a.Reason, a.Error)
					}
					if n := r.ranCount(t, 1); n != 1 {
						t.Errorf("the upgrade ran %d times, want once", n)
					}
					for _, rec := range r.records(t) {
						if rec.ReadPassword {
							t.Errorf("sudo was handed a password nobody typed: %q", rec.Argv)
						}
					}
					r.checkRecords(t)
				})
			}

			t.Run("rootpw", func(t *testing.T) {
				r := newSudoRig(t, impl, "rootpw")
				if a := r.apply(t, fakeUserPW); a.code != http.StatusForbidden || a.Reason != refusedWrongPassword || a.AskedFor != "root" {
					t.Errorf("the user's password: %d %q for %q, want 403 %s for root", a.code, a.Reason, a.AskedFor, refusedWrongPassword)
				}
				if a := r.apply(t, fakeRootPW); a.code != http.StatusOK {
					t.Errorf("root's password: %d %q %q, want 200", a.code, a.Reason, a.Error)
				}
				if n := r.ranCount(t, 1); n != 1 {
					t.Errorf("the upgrade ran %d times, want once", n)
				}
				r.checkRecords(t)
			})

			t.Run("notsudoer", func(t *testing.T) {
				r := newSudoRig(t, impl, "notsudoer")
				if a := r.apply(t, fakeUserPW); a.code != http.StatusForbidden || a.Reason != refusedNotAllowed {
					t.Errorf("the right password: %d %q %q, want 403 %s", a.code, a.Reason, a.Error, refusedNotAllowed)
				}
				if n := r.ranCount(t, 0); n != 0 {
					t.Errorf("the upgrade ran %d times for an account sudo refuses", n)
				}
				r.checkRecords(t)
			})

			t.Run("requiretty", func(t *testing.T) {
				r := newSudoRig(t, impl, "requiretty")
				a := r.apply(t, fakeUserPW)
				if impl == "classic" {
					if a.code != http.StatusForbidden || a.Reason != refusedNeedsTTY {
						t.Errorf("classic sudo with requiretty: %d %q %q, want 403 %s", a.code, a.Reason, a.Error, refusedNeedsTTY)
					}
					return
				}
				// sudo-rs does not know the setting, warns about it, and runs.
				if a.code != http.StatusOK {
					t.Errorf("sudo-rs with requiretty: %d %q %q, want 200", a.code, a.Reason, a.Error)
				}
				if b := r.apply(t, "not-it"); b.Reason != refusedWrongPassword || strings.Contains(b.Error, "requiretty") {
					t.Errorf("a wrong password under sudo-rs's parse warnings: %q shown as %q", b.Reason, b.Error)
				}
			})

			t.Run("lecture", func(t *testing.T) {
				r := newSudoRig(t, impl, "lecture")
				a := r.apply(t, "not-it")
				if a.Reason != refusedWrongPassword || strings.Contains(a.Error, "lecture") || strings.Contains(a.Error, "#1)") {
					t.Errorf("a wrong password under the lecture: %q shown as %q", a.Reason, a.Error)
				}
				if b := r.apply(t, fakeUserPW); b.code != http.StatusOK {
					t.Errorf("the right password under the lecture: %d %q", b.code, b.Error)
				}
			})
		})
	}
}

// A sudo that neither starts the command nor refuses is killed, and the page
// is told, instead of a request hanging with a password in memory.
func TestASudoThatNeverAnswersIsGivenUpOn(t *testing.T) {
	r := newSudoRig(t, "classic", "hang")
	old := elevatedStartWait
	elevatedStartWait = 300 * time.Millisecond
	t.Cleanup(func() { elevatedStartWait = old })

	start := time.Now()
	a := r.apply(t, fakeUserPW)
	if a.code != http.StatusGatewayTimeout {
		t.Errorf("a sudo that never answered: %d %q, want 504", a.code, a.Error)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("gave up after %v; the wait is not what bounds it", took)
	}
}

// A panel under no_new_privs is not offered sudo, and sudo is never run: no
// sudoers rule can get past the flag. The page is told why, and the shell
// command -- whose installer rewrites the unit -- is what is left.
func TestAProcessThatCannotGainPrivilegesIsNotOfferedSudo(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v99.0.0","html_url":"https://example.invalid","body":"","assets":[{"name":%q,"browser_download_url":"https://example.invalid/a"}]}`,
			selfupdate.AssetName("v99.0.0"))
	}))
	t.Cleanup(gh.Close)

	r := newSudoRig(t, "classic", "nopasswd")
	r.srv.Updater.API = gh.URL
	old := cannotElevate
	cannotElevate = func() bool { return true }
	t.Cleanup(func() { cannotElevate = old })

	code, raw := send(t, r.ts, http.MethodGet, "/api/update", "")
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil || code != http.StatusOK {
		t.Fatalf("check: %d %s", code, raw)
	}
	if out["elevate"] == true || out["cannotElevate"] != true {
		t.Errorf("under no_new_privs the check offers sudo: %s", raw)
	}
	if a := r.apply(t, ""); a.code != http.StatusConflict || a.Reason != refusedCannotElevate || !strings.Contains(a.Error, "service upgrade") {
		t.Errorf("apply under no_new_privs: %d %q %q, want 409 %s naming the shell command", a.code, a.Reason, a.Error, refusedCannotElevate)
	}
	if recs := r.records(t); len(recs) != 0 {
		t.Errorf("sudo was run %d times under a flag that stops it every time", len(recs))
	}
}

// A panel with no sudo takes no password and points at the command that works,
// named for the machine: doas where that is what there is, su where there is
// neither.
func TestWithoutSudoThePageOffersTheCommandThatExists(t *testing.T) {
	ts, srv := newTestServer(t)
	srv.installable = func() error { return fmt.Errorf("%w: /usr/local/bin", selfupdate.ErrNotWritable) }
	if a := (&sudoRig{ts: ts, srv: srv}).apply(t, "anything"); a.code != http.StatusConflict {
		t.Errorf("no sudo: %d %q, want 409", a.code, a.Error)
	}

	bin := t.TempDir()
	doas := filepath.Join(bin, "doas")
	if err := os.WriteFile(doas, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := privilegeHelper("/usr/local/bin/vibepanel"); got != "doas /usr/local/bin/vibepanel service upgrade" {
		t.Errorf("with doas and no sudo: %q", got)
	}
	t.Setenv("PATH", t.TempDir())
	if got := privilegeHelper("/usr/local/bin/vibepanel"); got != "su -c '/usr/local/bin/vibepanel service upgrade'" {
		t.Errorf("with neither: %q", got)
	}
}

// The check endpoint tells the page whether to show a field at all.
func TestTheCheckSaysWhetherSudoWantsAPassword(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v99.0.0","html_url":"https://example.invalid","body":"","assets":[{"name":%q,"browser_download_url":"https://example.invalid/a"}]}`,
			selfupdate.AssetName("v99.0.0"))
	}))
	t.Cleanup(gh.Close)

	// A panel that cannot write for a reason other than permissions -- a full
	// disk, a read-only mount -- is offered no field: nothing typed would help.
	t.Run("fulldisk", func(t *testing.T) {
		r := newSudoRig(t, "sudo-rs", "nopasswd")
		r.srv.installable = func() error { return errors.New("no space left on device") }
		r.srv.Updater.API = gh.URL
		code, raw := send(t, r.ts, http.MethodGet, "/api/update", "")
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil || code != http.StatusOK {
			t.Fatalf("check: %d %s", code, raw)
		}
		if out["elevate"] == true || out["elevateNoPassword"] == true {
			t.Errorf("a full disk was offered sudo: %s", raw)
		}
	})

	for _, tc := range []struct {
		variant  string
		noPasswd bool
	}{{"password", false}, {"nopasswd", true}, {"nopasswd-only", true}} {
		t.Run(tc.variant, func(t *testing.T) {
			r := newSudoRig(t, "sudo-rs", tc.variant)
			r.srv.Updater.API = gh.URL
			code, raw := send(t, r.ts, http.MethodGet, "/api/update", "")
			var out map[string]any
			if err := json.Unmarshal([]byte(raw), &out); err != nil || code != http.StatusOK {
				t.Fatalf("check: %d %s", code, raw)
			}
			if out["elevate"] != true {
				t.Errorf("elevate = %v, want true: %s", out["elevate"], raw)
			}
			if got, _ := out["elevateNoPassword"].(bool); got != tc.noPasswd {
				t.Errorf("elevateNoPassword = %v, want %v", got, tc.noPasswd)
			}
			if n := r.ranCount(t, 0); n != 0 {
				t.Errorf("checking ran the upgrade %d times", n)
			}
		})
	}
}

// ─── the same matrix, against real sudo ─────────────────────────────────────

// TestElevatedUpgradeAgainstRealSudo is the matrix above with nothing faked.
//
// Skipped unless VP_REAL_SUDO names a sudoers variant. scripts/sudo-check.sh
// runs it inside containers -- Ubuntu 24.04 for sudo 1.9, 25.10 for sudo-rs --
// as an unprivileged user with no terminal, once per variant, with sudoers
// arranged for that variant. /opt/vp/upgrade there is a root-owned script that
// prints the banner and appends "ran" to /opt/vp/ran when it runs as root.
//
// This is what would notice the recordings the fast tests replay going stale:
// a sudo release rewording a refusal, or changing which password -l wants.
func TestElevatedUpgradeAgainstRealSudo(t *testing.T) {
	variant := os.Getenv("VP_REAL_SUDO")
	if variant == "" {
		t.Skip("run by scripts/sudo-check.sh, inside a container whose sudoers is arranged for it")
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		t.Fatalf("no sudo in this container: %v", err)
	}
	version, _ := exec.Command(sudo, "--version").CombinedOutput()
	rs := strings.Contains(string(version), "sudo-rs")
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	userPW, rootPW := os.Getenv("VP_REAL_SUDO_USER_PW"), os.Getenv("VP_REAL_SUDO_ROOT_PW")

	ts, srv := newTestServer(t)
	srv.installable = func() error { return fmt.Errorf("%w: /usr/local/bin", selfupdate.ErrNotWritable) }
	srv.sudo = sudo
	srv.upgradeCommand = []string{"/opt/vp/upgrade"}
	r := &sudoRig{ts: ts, srv: srv, ran: "/opt/vp/ran"}

	base := r.ranCount(t, 0)
	ran := func(n int) {
		t.Helper()
		if got := r.ranCount(t, base+n) - base; got != n {
			t.Errorf("the upgrade ran as root %d times, want %d", got, n)
		}
		if b, _ := os.ReadFile(r.ran); strings.Contains(string(b), "not-root") {
			t.Errorf("the upgrade ran, but not as root")
		}
	}
	answer := func(pw string) applyAnswer {
		t.Helper()
		a := r.apply(t, pw)
		for _, secret := range []string{userPW, rootPW} {
			if secret != "" && strings.Contains(a.Error, secret) {
				t.Errorf("the answer carries a password: %q", a.Error)
			}
		}
		return a
	}
	refused := func(pw, reason, askedFor string) {
		t.Helper()
		a := answer(pw)
		if a.code != http.StatusForbidden || a.Reason != reason || (askedFor != "" && a.AskedFor != askedFor) {
			t.Errorf("%s: %d %q for %q (%q), want 403 %s for %q", variant, a.code, a.Reason, a.AskedFor, a.Error, reason, askedFor)
		}
	}
	accepted := func(pw string) {
		t.Helper()
		if a := answer(pw); a.code != http.StatusOK || !a.Elevated {
			t.Errorf("%s: %d %q %q, want 200 elevated", variant, a.code, a.Reason, a.Error)
		}
	}
	probe := func(want bool) {
		t.Helper()
		if got := srv.sudoRunsWithoutPassword(t.Context(), sudo); got != want {
			t.Errorf("%s: the check says a password is not needed = %v, want %v", variant, got, want)
		}
	}

	switch variant {
	case "password":
		probe(false)
		refused("", refusedNeedPassword, "")
		refused("not-it", refusedWrongPassword, me.Username)
		ran(0)
		accepted(userPW)
		ran(1)
	case "only-upgrade":
		probe(false)
		accepted(userPW)
		ran(1)
	case "nopasswd", "nopasswd-only":
		probe(true)
		accepted("")
		ran(1)
	case "rootpw", "targetpw":
		probe(false)
		refused(userPW, refusedWrongPassword, "root")
		ran(0)
		accepted(rootPW)
		ran(1)
	case "notsudoer":
		probe(false)
		refused(userPW, refusedNotAllowed, "")
		ran(0)
	case "requiretty":
		if rs {
			// sudo-rs does not know the setting, warns, and runs.
			accepted(userPW)
			ran(1)
			return
		}
		refused(userPW, refusedNeedsTTY, "")
		ran(0)
	case "nnp":
		// The script starts this test with setpriv --no-new-privs, the way an
		// old unit's NoNewPrivileges=yes starts the panel.
		if !cannotElevate() {
			t.Fatal("started under no_new_privs, and the kernel's answer was not read as such")
		}
		a := answer("")
		if a.code != http.StatusConflict || a.Reason != refusedCannotElevate {
			t.Errorf("nnp: %d %q %q, want 409 %s", a.code, a.Reason, a.Error, refusedCannotElevate)
		}
		ran(0)
	case "lecture":
		a := answer("not-it")
		if a.Reason != refusedWrongPassword || strings.Contains(a.Error, "lecture") || strings.Contains(a.Error, "#1)") {
			t.Errorf("a wrong password under the lecture: %q shown as %q", a.Reason, a.Error)
		}
		accepted(userPW)
		ran(1)
	default:
		t.Fatalf("unknown variant %q", variant)
	}
}
