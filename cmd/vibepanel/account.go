package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/id"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// The first account, from the command line.
//
// It already existed one way: the panel prints a one-time setup token while no
// account exists, and the browser exchanges it for a username and a password.
// That path stays exactly as it was — this is a second door into the same
// room, for the person installing over ssh who would rather not copy a token
// out of a journal.
//
// Both doors lead through the same lock. The hash is auth.HashPassword
// (argon2id), the rules are auth.ValidateCredentials, and the row is
// store.CreateUser — the same three the HTTP handler uses. A second
// implementation of any of them is how a panel ends up with one account whose
// password is checked differently from the next.
//
// And it only ever creates the *first* account. `CountUsers() > 0` is a
// refusal, not an update. A subcommand that could also replace a password
// would be a password reset available to anybody who can run the binary —
// which, under the system unit, includes anyone who can write the unit's
// environment. Changing a password is a thing you do logged in.

const accountCommands = `  create     create the first account, if there is not one yet`

func cmdAccount(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "create":
		return cmdAccountCreate(args)
	case "":
		accountUsage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try: vibepanel account --help)", sub)
	}
}

func accountUsage(out *os.File) {
	fmt.Fprintf(out, "vibepanel account — the panel's own login, from the command line.\n\n")
	fmt.Fprintf(out, "Usage:\n  vibepanel account <command> [flags]\n\nCommands:\n%s\n\n", accountCommands)
	fmt.Fprintf(out, `Creating the first account:

  vibepanel account create --username me            # asks for the password
  vibepanel account create --username me --password-stdin < pw.txt
  vibepanel account create --username me --password-file /root/pw
  VP_PW=... vibepanel account create --username me --password-env VP_PW

Where the password may come from, and which of those is safe:

  a prompt          safest. Echo is turned off while you type it.
  --password-stdin  safe, and what a script should use. Nothing appears in
                    the process list or in your shell history.
  --password-file   safe if the file is yours and mode 0600; it is read once
                    and never written anywhere.
  --password-env    safe from other users on Linux (/proc/<pid>/environ is
                    readable only by the owner), but the value is inherited by
                    every child process, so prefer one of the two above.

There is deliberately no --password <value>. A password on a command line is
in your shell history and, for as long as the process runs, in `+"`ps`"+` output for
every other user on the machine.

This creates the first account and nothing else. If one already exists it
refuses: changing a password is done from Settings inside the panel.
`)
}

func cmdAccountCreate(args []string) error {
	fs := flag.NewFlagSet("vibepanel account create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		username = fs.String("username", "", "the account name")
		fromIn   = fs.Bool("password-stdin", false, "read the password from standard input")
		fromFile = fs.String("password-file", "", "read the password from this file")
		fromEnv  = fs.String("password-env", "", "read the password from this environment variable")
	)
	fs.Usage = func() { accountUsage(os.Stderr) }

	// Caught before the flag package can, because `flag` would report
	// "flag provided but not defined: -password" and leave the person to
	// discover the reason on their own — and the reason is the point.
	for _, a := range args {
		if a == "--password" || a == "-password" || strings.HasPrefix(a, "--password=") || strings.HasPrefix(a, "-password=") {
			return errors.New("there is no --password flag, on purpose: a password on a command line " +
				"is in your shell history and in `ps` output for every other user on this machine.\n" +
				"       Use --password-stdin, --password-file <path>, --password-env <VAR>, or leave it\n" +
				"       off entirely and be asked for it")
		}
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Every flag that is not the password, so the config file and the
	// environment still decide where the database is.
	cfg, err := config.Load(fs.Args(), os.Stderr)
	if err != nil {
		return err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	ctx := context.Background()
	// Not openApp: that calls EnsureServer, which would start a tmux server as
	// a side effect of creating a user account.
	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()

	n, err := db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return errors.New("this panel already has an account, and this command only ever creates the first one.\n" +
			"       Log in and change the password from Settings. If you have lost it, the\n" +
			"       recovery path is in docs/runbook.md — it is deliberately not a flag here")
	}

	name := strings.TrimSpace(*username)
	if name == "" {
		if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
			return errors.New("--username is required when there is nobody to ask")
		}
		fmt.Printf("username: ")
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			name = strings.TrimSpace(sc.Text())
		}
	}

	password, err := readPassword(*fromIn, *fromFile, *fromEnv)
	if err != nil {
		return err
	}
	if err := auth.ValidateCredentials(name, password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	user, err := db.CreateUser(ctx, id.New(), name, hash)
	if err != nil {
		return err
	}
	// Said explicitly, because the other consequence is invisible: with an
	// account in place the panel no longer prints a setup token at startup,
	// and somebody who went looking for one would find nothing and assume the
	// install had failed.
	fmt.Printf("created the account %q\n", user.Username)
	fmt.Printf("the panel will not print a setup token any more; log in with this instead.\n")
	return nil
}

// readPassword resolves exactly one source, or asks.
func readPassword(fromStdin bool, file, envVar string) (string, error) {
	chosen := 0
	for _, on := range []bool{fromStdin, file != "", envVar != ""} {
		if on {
			chosen++
		}
	}
	if chosen > 1 {
		// Silently preferring one would mean a script that thinks it set the
		// password from a file while the password came from somewhere else.
		return "", errors.New("choose one of --password-stdin, --password-file and --password-env")
	}

	switch {
	case fromStdin:
		// Everything up to EOF, with one trailing newline removed and nothing
		// else touched: a password may legitimately contain spaces, and
		// TrimSpace would silently change it into a different password that
		// then fails to log in with no explanation anywhere.
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading the password from stdin: %w", err)
		}
		return trimOneNewline(string(raw)), nil
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading the password file: %w", err)
		}
		return trimOneNewline(string(raw)), nil
	case envVar != "":
		v, ok := os.LookupEnv(envVar)
		if !ok {
			return "", fmt.Errorf("$%s is not set, so there is no password to read", envVar)
		}
		return v, nil
	}

	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return "", errors.New("no password was given and there is no terminal to ask at.\n" +
			"       Use --password-stdin, --password-file <path> or --password-env <VAR>")
	}
	first, err := promptPassword("password: ")
	if err != nil {
		return "", err
	}
	again, err := promptPassword("again: ")
	if err != nil {
		return "", err
	}
	if first != again {
		// Asked twice because there is no way back: the panel stores an
		// argon2id hash, so a mistyped password is not recoverable, it is a
		// panel nobody can log into.
		return "", errors.New("the two did not match; nothing was created")
	}
	return first, nil
}

// trimOneNewline removes the newline a file or a heredoc ends with, and
// nothing else. TrimSpace would be wrong: a password may legitimately begin or
// end with a space, and silently storing a different string than the one that
// was supplied produces a panel nobody can log into and no error anywhere.
func trimOneNewline(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

// promptPassword reads a line with the terminal's echo turned off.
//
// Through `stty` rather than golang.org/x/term, which would be a new direct
// dependency for one call. stty is POSIX and is on both platforms this
// installs on. If it is not there the password is read anyway and the person
// is told it will be visible — refusing to create an account because the echo
// could not be turned off would be worse than the exposure, and saying nothing
// would be worse than both.
func promptPassword(label string) (string, error) {
	restore := func() {}
	if _, err := exec.LookPath("stty"); err == nil {
		off := exec.Command("stty", "-echo")
		off.Stdin = os.Stdin
		if err := off.Run(); err == nil {
			restore = func() {
				on := exec.Command("stty", "echo")
				on.Stdin = os.Stdin
				_ = on.Run()
				fmt.Println()
			}
		} else {
			fmt.Println("(could not turn off echo; what you type will be visible)")
		}
	} else {
		fmt.Println("(no stty here, so what you type will be visible)")
	}
	defer restore()

	fmt.Print(label)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return "", errors.New("no password was typed")
	}
	return sc.Text(), nil
}
