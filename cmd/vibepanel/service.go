package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// The management command: everything a person does to a vibepanel install
// after the installer has run.
//
// Why it lives in the binary rather than in a script beside it.
//
// A script would have to be installed, found on PATH, upgraded in step with
// the binary, and kept honest about which service kind is present — four
// things that can drift, for a program whose whole distribution story is "one
// static file with no runtime to install first". The binary is already the
// admin CLI (serve, project, session, hook, doctor, version), it is already on
// PATH because the installer puts it in ~/.local/bin, and `vibepanel service
// upgrade` replacing the binary that is running it is safe: GNU and BSD
// `install` both unlink the destination before writing, so there is no ETXTBSY
// and no half-written file.
//
// What it removes is the thing the runbook kept having to explain: whether the
// command is `systemctl --user`, `sudo systemctl`, or `launchctl bootout
// gui/501/io.github.jiangmuran.vibepanel`. Those three answers differ per
// machine and are the reason people leave a panel broken rather than look it
// up.

// serviceCommands is what `vibepanel service` says it can do, and the only
// list of it. The same shape as config.Commands and for the same reason: the
// switch, the error for an unknown word and the help text were three copies
// once already.
const serviceCommands = `  status     is it running, and since when
  start      start it (and enable it at boot)
  stop       stop it; your sessions belong to tmux and survive
  restart    restart it; likewise
  logs       the last lines of its log (-f to follow, -n to choose)
  token      the one-time setup token, dug out of the log for you
  upgrade    fetch the latest release and re-run the installer over this one
  uninstall  stop it, remove the service and the binary; keeps your data`

type svcKind string

const (
	svcSystem svcKind = "system" // systemd system unit, /etc/systemd/system
	svcUser   svcKind = "user"   // systemd user unit, ~/.config/systemd/user
	svcAgent  svcKind = "agent"  // launchd LaunchAgent, ~/Library/LaunchAgents
	svcNone   svcKind = "none"
)

// macLabel is the launchd job label. Three things have to agree on it: this
// file, deploy/io.github.jiangmuran.vibepanel.plist, and deploy/install.sh.
const macLabel = "io.github.jiangmuran.vibepanel"

// installURL is the one-liner, and `upgrade` is the only thing that runs it.
const installURL = "https://raw.githubusercontent.com/jiangmuran/vibepanel/main/install.sh"

// svcTarget is everything the command lines are derived from. Built by
// detectService and never read from the environment below that, so the whole
// mapping is a pure function of it and can be tested without a service
// manager, a Mac, or root.
type svcTarget struct {
	Platform string // "linux" | "darwin"
	Kind     svcKind
	Unit     string   // the unit file or the plist
	Log      string   // darwin only: there is no journal
	Bin      string   // the installed binary, which uninstall removes
	Root     []string // how to become root, empty when we already are
	UID      string
}

// detectService works out what is installed by looking for the files the
// installer writes. Deliberately not by asking systemd or launchd: `systemctl
// --user is-active` answers for the logged-in user's manager whatever HOME
// says, and on a machine with two accounts that is somebody else's panel.
func detectService(platform, home, destdir, uid string) svcTarget {
	t := svcTarget{
		Platform: platform,
		Kind:     svcNone,
		Bin:      filepath.Join(home, ".local", "bin", "vibepanel"),
		UID:      uid,
	}
	if uid != "0" {
		t.Root = []string{"sudo"}
	}
	if platform == "darwin" {
		t.Unit = filepath.Join(home, "Library", "LaunchAgents", macLabel+".plist")
		t.Log = filepath.Join(home, "Library", "Logs", "vibepanel.log")
		if fileExists(t.Unit) {
			t.Kind = svcAgent
		}
		return t
	}
	// System first, because that is the order the installer resolves an
	// ambiguous re-run in, and the two are never meant to coexist.
	sys := systemUnitPath(destdir)
	usr := filepath.Join(home, ".config", "systemd", "user", "vibepanel.service")
	switch {
	case fileExists(sys):
		t.Kind, t.Unit = svcSystem, sys
		// The unit is the authority on where its own binary is.
		//
		// A system install puts it in /usr/local/bin and a user install in
		// ~/.local/bin, and older system installs put it in ~/.local/bin too --
		// so guessing gets `uninstall` removing the wrong file, or nothing.
		// ExecStart is a fact about this machine; the default under it is only
		// for a unit that cannot be read.
		t.Bin = filepath.Join(destdir, "usr", "local", "bin", "vibepanel")
		if destdir == "" {
			t.Bin = "/usr/local/bin/vibepanel"
		}
		if b := execStartBinary(sys); b != "" {
			t.Bin = b
		}
	case fileExists(usr):
		t.Kind, t.Unit = svcUser, usr
	default:
		t.Unit = usr
	}
	return t
}

// runtimeGOOS is runtime.GOOS, with the same VIBEPANEL_PLATFORM override
// deploy/install.sh documents: the launchd half of this file is otherwise only
// reachable from a Mac, and a mapping nobody can run is a mapping nobody
// checks.
func runtimeGOOS() string {
	if p := os.Getenv("VIBEPANEL_PLATFORM"); p != "" {
		return strings.ToLower(p)
	}
	return runtime.GOOS
}

// systemUnitPath is where the systemd *system* unit lives, under an optional
// DESTDIR.
//
// The "/" is the whole function. `filepath.Join("", "etc", ...)` is
// "etc/systemd/system/vibepanel.service" -- a relative path, resolved against
// whatever directory the person is standing in. DESTDIR is empty in every real
// install and set in every test, so the branch that ships was the only one
// nothing ran, and on a machine with a working system install `vibepanel
// service token` reported no service installed and named the user path
// instead. The panel was serving on 8443 while it said so.
func systemUnitPath(destdir string) string {
	if destdir == "" {
		destdir = "/"
	}
	return filepath.Join(destdir, "etc", "systemd", "system", "vibepanel.service")
}

// execStartBinary is the program a unit file runs, or "" if it cannot be read.
//
// The first field of the first ExecStart, with a leading `-`, `@`, `+`, `!` --
// systemd's prefixes -- stripped. Anything it does not understand returns
// empty and the caller keeps its default, because a wrong path here is a
// `service uninstall` deleting a file nobody asked about.
func execStartBinary(unit string) string {
	b, err := os.ReadFile(unit)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "ExecStart="))
		v = strings.TrimLeft(v, "-@+!:")
		if v == "" {
			return ""
		}
		first := strings.Fields(v)
		if len(first) == 0 || !filepath.IsAbs(first[0]) {
			return ""
		}
		return first[0]
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// step is one thing to do. Either a command to run or a file to remove, so
// uninstall — which is both — is still a list a person can be shown before it
// happens.
type step struct {
	Argv   []string
	Remove string
	// Ignore marks a step whose failure is not a failure: stopping a service
	// that is not running, or removing a file that is already gone. Without
	// this, `uninstall` on a half-installed machine stops at the first step and
	// leaves the rest of it behind.
	Ignore bool
}

func (s step) String() string {
	if s.Remove != "" {
		return "rm -f " + s.Remove
	}
	return strings.Join(s.Argv, " ")
}

type svcOpts struct {
	Lines  int
	Follow bool
}

// root prefixes argv with sudo when the target needs it. Only the system unit
// does: a user unit and a LaunchAgent are both entirely inside $HOME, and
// asking for a password to read your own logs is how people stop reading them.
func (t svcTarget) root(argv ...string) []string {
	if t.Kind != svcSystem {
		return argv
	}
	return append(append([]string{}, t.Root...), argv...)
}

func (t svcTarget) guiTarget() string { return "gui/" + t.UID + "/" + macLabel }

// plan is the whole mapping, in one place, as a pure function.
func plan(t svcTarget, sub string, o svcOpts) ([]step, error) {
	lines := o.Lines
	if lines <= 0 {
		lines = 40
	}
	// Before the kind switch: upgrade is the one thing that works on a machine
	// with no service installed at all, and it is also the one thing that is
	// identical on every platform, because it hands over to the installer.
	if sub == "upgrade" {
		// The one-liner, run as the one-liner. --yes because the installer
		// keeps whichever service kind is already installed on a bare re-run,
		// so there is nothing left to ask, and because a prompt here would be
		// a prompt inside a pipeline.
		return []step{{Argv: []string{"sh", "-c",
			"curl -fsSL " + installURL + " | sh -s -- --yes"}}}, nil
	}
	switch t.Kind {
	case svcNone:
		return nil, fmt.Errorf("no vibepanel service is installed for this account.\n"+
			"       Looked for %s\n"+
			"       Install one:  curl -fsSL %s | sh", t.Unit, installURL)
	case svcAgent:
		switch sub {
		case "status":
			return []step{{Argv: []string{"launchctl", "print", t.guiTarget()}}}, nil
		case "start":
			// bootstrap loads the plist and, because RunAtLoad is set, starts
			// it. There is no separate "enable": a plist in LaunchAgents is
			// loaded at every login.
			return []step{{Argv: []string{"launchctl", "bootstrap", "gui/" + t.UID, t.Unit}}}, nil
		case "stop":
			return []step{{Argv: []string{"launchctl", "bootout", t.guiTarget()}}}, nil
		case "restart":
			// kickstart -k, not bootout followed by bootstrap: the pair has a
			// window in which the job does not exist, and anything that goes
			// wrong between them leaves the panel simply gone.
			return []step{{Argv: []string{"launchctl", "kickstart", "-k", t.guiTarget()}}}, nil
		case "logs":
			argv := []string{"tail", "-n", strconv.Itoa(lines)}
			if o.Follow {
				argv = append(argv, "-f")
			}
			return []step{{Argv: append(argv, t.Log)}}, nil
		case "uninstall":
			return []step{
				{Argv: []string{"launchctl", "bootout", t.guiTarget()}, Ignore: true},
				{Remove: t.Unit},
				{Remove: t.Bin},
			}, nil
		}
	case svcUser, svcSystem:
		unit := "vibepanel"
		sysctl := []string{"systemctl"}
		journal := []string{"journalctl"}
		if t.Kind == svcUser {
			sysctl = append(sysctl, "--user")
			journal = append(journal, "--user")
		}
		sc := func(args ...string) []string { return t.root(append(append([]string{}, sysctl...), args...)...) }
		switch sub {
		case "status":
			return []step{{Argv: sc("status", unit)}}, nil
		case "start":
			// enable --now, not start: "it is running" and "it comes back
			// after a reboot" are the same request from anyone typing this,
			// and a start that does not survive a reboot is the failure the
			// lingering paragraph in the installer exists for.
			return []step{{Argv: sc("enable", "--now", unit)}}, nil
		case "stop":
			return []step{{Argv: sc("stop", unit)}}, nil
		case "restart":
			return []step{{Argv: sc("restart", unit)}}, nil
		case "logs":
			argv := append(append([]string{}, journal...), "-u", unit, "-n", strconv.Itoa(lines))
			if o.Follow {
				argv = append(argv, "-f")
			}
			return []step{{Argv: t.root(argv...)}}, nil
		case "uninstall":
			// `rm` through root for the system unit, not os.Remove.
			//
			// /etc/systemd/system/vibepanel.service is root's, and so is
			// /usr/local/bin/vibepanel: os.Remove on either is "permission
			// denied" from a command that has already stopped the service, so
			// the panel is down and its files are still there. The user unit
			// stays an os.Remove -- both paths are the caller's own.
			if t.Kind == svcSystem {
				return []step{
					{Argv: sc("disable", "--now", unit), Ignore: true},
					{Argv: t.root("rm", "-f", t.Unit)},
					{Argv: sc("daemon-reload"), Ignore: true},
					{Argv: t.root("rm", "-f", t.Bin)},
				}, nil
			}
			return []step{
				{Argv: sc("disable", "--now", unit), Ignore: true},
				{Remove: t.Unit},
				{Argv: sc("daemon-reload"), Ignore: true},
				{Remove: t.Bin},
			}, nil
		}
	}
	return nil, fmt.Errorf("unknown: vibepanel service %s", sub)
}

// tokenPattern is what auth.NewToken produces: 32 random bytes as raw
// base64url, so 43 characters of [A-Za-z0-9_-].
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{40,}$`)

// scrapeToken pulls the one-time setup token out of a log.
//
// The token exists only in memory and only while there is no account, so there
// is nothing in the database to read it back from — printing it to the log is
// deliberately the whole handover. Every install path ends by telling people to
// go and find it in `journalctl` or a file, which is the step that gets
// mistyped.
//
// The last one wins: a panel that has been restarted several times before
// anybody claimed it has printed several, and only the newest is live.
func scrapeToken(log string) string {
	sc := bufio.NewScanner(strings.NewReader(log))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	found := ""
	// A countdown rather than "the next line". The panel prints a blank line
	// between the sentence and the token, and under journalctl that blank line
	// is not blank -- it still carries the timestamp, the host and
	// `vibepanel[1234]:`, so its last field is the unit prefix. Looking only at
	// the line immediately after the marker found nothing on exactly the
	// platform this command exists for.
	left := 0
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "one-time setup token") {
			left = 3
			continue
		}
		if left == 0 {
			continue
		}
		left--
		// journalctl prefixes every line with a timestamp, the host and the
		// unit, so the token is the last field rather than the whole line.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if cand := fields[len(fields)-1]; tokenPattern.MatchString(cand) {
			found = cand
			left = 0
		}
	}
	return found
}

func serviceUsage(out *os.File) {
	fmt.Fprintf(out, "vibepanel service — one command for a running panel, whichever way it runs.\n\n")
	fmt.Fprintf(out, "Usage:\n  vibepanel service <command> [flags]\n\nCommands:\n%s\n\n", serviceCommands)
	fmt.Fprintf(out, "Flags:\n")
	fmt.Fprintf(out, "  -n <lines>   how many log lines (logs, token; default 40)\n")
	fmt.Fprintf(out, "  -f           follow the log (logs)\n")
	fmt.Fprintf(out, "  --yes        do not ask (uninstall)\n")
	fmt.Fprintf(out, "  --dry-run    print the commands instead of running them\n\n")
	fmt.Fprintf(out, "It works out for itself whether this machine runs the panel as a systemd\n")
	fmt.Fprintf(out, "system unit, a systemd user unit or a macOS LaunchAgent, and issues the\n")
	fmt.Fprintf(out, "right commands for that one. --dry-run shows you which.\n")
}

func cmdService(args []string) error {
	fs := flag.NewFlagSet("vibepanel service", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		lines  = fs.Int("n", 40, "how many log lines")
		follow = fs.Bool("f", false, "follow the log")
		yes    = fs.Bool("yes", false, "do not ask")
		dry    = fs.Bool("dry-run", false, "print the commands instead of running them")
	)
	fs.Usage = func() { serviceUsage(os.Stderr) }

	// Both orders. `vibepanel service logs -f` is what people type, and
	// `vibepanel service --dry-run status` is what a script types; Go's flag
	// package stops at the first non-flag, so neither works without this and
	// the failure is silent -- the subcommand is never seen and the usage text
	// is printed as though nothing had been asked for.
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if sub == "" && fs.NArg() > 0 {
		sub = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
	}
	if sub == "" {
		serviceUsage(os.Stdout)
		return nil
	}

	uid := strconv.Itoa(os.Getuid())
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no home directory, and every path this command uses is under one: %w", err)
	}
	// VIBEPANEL_DESTDIR is the same test-only override deploy/install.sh
	// documents: it puts a prefix in front of the system unit path so the
	// system-unit branch can be exercised without writing to /etc.
	t := detectService(runtimeGOOS(), home, os.Getenv("VIBEPANEL_DESTDIR"), uid)

	switch sub {
	case "token":
		return serviceToken(t, *lines, *dry)
	case "upgrade":
		// Planned against a target with no kind, because upgrade is the one
		// thing that works on a machine with no service installed at all.
	case "status", "start", "stop", "restart", "logs", "uninstall":
	default:
		return fmt.Errorf("unknown command %q (try: vibepanel service --help)", sub)
	}

	steps, err := plan(t, sub, svcOpts{Lines: *lines, Follow: *follow})
	if err != nil {
		return err
	}

	if sub == "uninstall" {
		fmt.Printf("this will:\n")
		for _, s := range steps {
			fmt.Printf("  %s\n", s)
		}
		// Said before the confirmation, because "did I just lose my sessions"
		// is the question, and the answer is no on both counts: the tmux
		// server is not a child of the panel, and nothing here touches the
		// database.
		fmt.Printf("\nYour data directory is not touched, and neither is the tmux server —\n")
		fmt.Printf("every running session survives this. Removing the data too, if you\n")
		fmt.Printf("really mean it, is a separate `rm -rf` you type yourself.\n\n")
		if !*yes && !*dry {
			if !askYes("remove it?") {
				fmt.Println("nothing was changed.")
				return nil
			}
		}
	}

	for _, s := range steps {
		if *dry {
			fmt.Println(s)
			continue
		}
		if s.Remove != "" {
			if err := os.Remove(s.Remove); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing %s: %w", s.Remove, err)
			}
			fmt.Println("removed", s.Remove)
			continue
		}
		cmd := exec.Command(s.Argv[0], s.Argv[1:]...) //nolint:gosec // argv comes from plan(), not from input
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := cmd.Run(); err != nil && !s.Ignore {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	if sub == "uninstall" && !*dry {
		fmt.Printf("\ngone. Your sessions are still running in tmux, and your data is where\n")
		fmt.Printf("it was — `vibepanel doctor` from a new install will find it again.\n")
	}
	return nil
}

// serviceToken runs the log command and reads the token out of what comes back.
func serviceToken(t svcTarget, lines int, dry bool) error {
	if lines < 200 {
		// A start prints a dozen lines after the token — the TLS warnings, the
		// listener — and a restart or two puts the interesting one well out of
		// the default window. The number people would have to know to pass is
		// exactly the kind of thing this command exists to stop them needing.
		lines = 200
	}
	steps, err := plan(t, "logs", svcOpts{Lines: lines})
	if err != nil {
		return err
	}
	if dry {
		for _, s := range steps {
			fmt.Println(s)
		}
		return nil
	}
	var buf bytes.Buffer
	cmd := exec.Command(steps[0].Argv[0], steps[0].Argv[1:]...) //nolint:gosec // argv comes from plan()
	cmd.Stdout, cmd.Stderr = &buf, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", steps[0], err)
	}
	token := scrapeToken(buf.String())
	if token == "" {
		// Three different situations, and the difference matters enough to say
		// all three rather than guess: a claimed panel has no token at all, a
		// panel that has been up for weeks has scrolled past it, and a panel
		// that never started never printed one.
		return fmt.Errorf("no setup token in the last %d lines of the log.\n"+
			"       A token exists only while the panel has no account yet, so either\n"+
			"       somebody has already claimed this panel (log in normally), or it\n"+
			"       has printed one further back (vibepanel service logs -n 2000), or\n"+
			"       it never started (vibepanel service status)", lines)
	}
	fmt.Println(token)
	return nil
}

// askYes is the same shape as the installer's prompt, and asks only when there
// is somebody to answer: a question written into a log file waits forever.
func askYes(q string) bool {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return false
	}
	fmt.Printf("%s [y/N] ", q)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	a := strings.TrimSpace(strings.ToLower(sc.Text()))
	return a == "y" || a == "yes"
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
