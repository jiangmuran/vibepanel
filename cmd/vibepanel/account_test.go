package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/auth"
	"github.com/jiangmuran/vibepanel/internal/store"
)

func TestAccountCreateMakesTheFirstAccountAndRefusesTheSecond(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("a sufficiently long password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Through the environment, like every other subcommand in this binary: the
	// per-command flag sets deliberately do not redefine the global ones.
	t.Setenv("VIBEPANEL_DATA_DIR", dir)
	args := []string{"--username", "admin", "--password-file", pw}

	if err := cmdAccountCreate(args); err != nil {
		t.Fatalf("creating the first account: %v", err)
	}

	// Through the same argon2id path the browser uses, or the two doors into
	// the panel check the same password differently.
	db, err := store.Open(context.Background(), filepath.Join(dir, "vibepanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := db.UserByName(context.Background(), "admin")
	if err != nil {
		t.Fatalf("the account was not stored: %v", err)
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Errorf("the stored hash is %q; it must come from auth.HashPassword", u.PasswordHash)
	}
	if okPw, err := auth.VerifyPassword("a sufficiently long password", u.PasswordHash); err != nil || !okPw {
		t.Errorf("the panel cannot verify the password this command stored (err=%v)", err)
	}
	// The trailing newline of the file is not part of the password. A password
	// silently one character longer than the one that was supplied is a panel
	// nobody can log into, with nothing anywhere saying why.
	if okPw, _ := auth.VerifyPassword("a sufficiently long password\n", u.PasswordHash); okPw {
		t.Error("the file's trailing newline was stored as part of the password")
	}

	// The refusal. This is the whole safety property: it creates the first
	// account and is never a password reset.
	err = cmdAccountCreate(append([]string{}, args...))
	if err == nil {
		t.Fatal("it created a second account, which makes this a password reset for " +
			"anybody who can run the binary")
	}
	if !strings.Contains(err.Error(), "already has an account") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

func TestAccountCreateRejectsAPasswordOnTheCommandLine(t *testing.T) {
	// `ps` shows every other user on the machine the arguments of every
	// process, and the shell writes them to history. The flag does not exist,
	// and saying so is more use than "flag provided but not defined".
	for _, a := range [][]string{
		{"--username", "me", "--password", "hunter2hunter2"},
		{"--username", "me", "--password=hunter2hunter2"},
		{"--username", "me", "-password", "hunter2hunter2"},
	} {
		t.Setenv("VIBEPANEL_DATA_DIR", t.TempDir())
		err := cmdAccountCreate(a)
		if err == nil {
			t.Fatalf("%v was accepted", a)
		}
		if !strings.Contains(err.Error(), "shell history") {
			t.Errorf("%v: the refusal does not explain itself: %v", a, err)
		}
	}
}

func TestAccountCreateAppliesTheSameRulesAsTheWizard(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIBEPANEL_DATA_DIR", dir)
	err := cmdAccountCreate([]string{"--username", "me", "--password-file", pw})
	if err == nil {
		t.Fatal("a password shorter than the panel's minimum was accepted from the command line")
	}
	if !strings.Contains(err.Error(), "at least") {
		t.Errorf("the message is not the panel's own: %v", err)
	}
	// And the account really was not created, so a failed attempt does not
	// consume the one chance at the first account.
	db, derr := store.Open(context.Background(), filepath.Join(dir, "vibepanel.db"))
	if derr != nil {
		t.Fatal(derr)
	}
	defer db.Close()
	if n, _ := db.CountUsers(context.Background()); n != 0 {
		t.Errorf("%d accounts exist after a rejected password", n)
	}
}

func TestReadPasswordSourcesAreExclusive(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "pw")
	if err := os.WriteFile(f, []byte("from the file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VP_TEST_PW", "from the environment")

	if got, err := readPassword(false, f, ""); err != nil || got != "from the file" {
		t.Errorf("--password-file gave %q (%v)", got, err)
	}
	if got, err := readPassword(false, "", "VP_TEST_PW"); err != nil || got != "from the environment" {
		t.Errorf("--password-env gave %q (%v)", got, err)
	}
	// Two sources is a script that believes it set one password and set
	// another. Silently preferring either is the failure worth refusing over.
	if _, err := readPassword(false, f, "VP_TEST_PW"); err == nil {
		t.Error("two password sources were accepted; one of them was silently ignored")
	}
	if _, err := readPassword(false, "", "VP_NOT_SET_ANYWHERE"); err == nil {
		t.Error("an unset environment variable was treated as an empty password")
	}
}
