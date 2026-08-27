package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/config"
	"github.com/jiangmuran/vibepanel/internal/store"
	"github.com/jiangmuran/vibepanel/internal/tmux"
)

func TestEveryDocumentedCommandExists(t *testing.T) {
	// `vibepanel --help` said "Usage: vibepanel [flags]" and listed flags. It
	// never mentioned that commands exist, so somebody who installed the
	// release archive and asked the binary what it does was not told about
	// `doctor` — while the runbook opens by telling them to run it.
	//
	// Adding the list to the usage text made three copies of the same six
	// words: the switch, the error for an unknown command, and the help. This
	// compares the two that are left.
	documented := commandNames()
	if len(documented) == 0 {
		t.Fatal("config.Commands parsed to nothing; the help text changed shape " +
			"and this test is no longer comparing anything")
	}

	var dispatched []string
	for name := range commands {
		dispatched = append(dispatched, name)
	}
	sort.Strings(dispatched)
	sorted := append([]string(nil), documented...)
	sort.Strings(sorted)

	if strings.Join(sorted, " ") != strings.Join(dispatched, " ") {
		t.Errorf("--help offers %v and the binary answers to %v", sorted, dispatched)
	}

	for _, name := range documented {
		if commands[name] == nil {
			t.Errorf("--help offers %q and nothing handles it", name)
		}
	}
}

func TestTheHelpTextDescribesEachCommand(t *testing.T) {
	// A name on its own is a list of words to guess at. Each line has to say
	// what the command is for, or the help is only marginally better than the
	// error message it was copied from.
	for _, line := range strings.Split(config.Commands, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			t.Errorf("%q has a name and almost nothing else", strings.TrimSpace(line))
		}
	}
}

// A session's scratch terminals must die with it.
//
// The database cascades the child rows away, and tmux knows nothing about that
// cascade. Kill only the parent's tmux session and the children keep running on
// the panel's socket with no row left pointing at them: nothing in the UI can
// reach them, and they hold a pane until the machine restarts. The panel says
// so at startup — "tmux sessions on our socket with no database row" — which is
// a report, not a repair.
//
// This is the shape that has cost this project repeatedly: two paths doing the
// same thing, one of them updated. handleDeleteSession has listed the children
// and killed them for as long as it has existed, and said why in a comment.
func TestKillingASessionKillsItsScratchTerminals(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	ctx := context.Background()

	socket := "vibepanel-test-" + strconv.Itoa(os.Getpid()) + "-killtree"
	tm := tmux.New(socket, t.TempDir())
	// Point the suite at another tmux without editing anything:
	//	TEST_TMUX_BIN=/path/to/tmux go test ./...
	if bin := os.Getenv("TEST_TMUX_BIN"); bin != "" {
		tm.Bin = bin
	}
	t.Cleanup(func() {
		_ = tm.KillServer(ctx)
		_ = os.Remove(tm.SocketPath())
	})
	if err := tm.EnsureServer(ctx); err != nil {
		t.Fatalf("EnsureServer: %v", err)
	}

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	proj, err := db.CreateProject(ctx, "p_test", "test", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	mk := func(id, name string, parent *string) store.Session {
		t.Helper()
		if err := tm.Create(ctx, tmux.CreateOptions{
			Name: name, Dir: proj.Path,
			Command: []string{"sleep", "300"},
			Width:   80, Height: 24,
		}); err != nil {
			t.Fatalf("tmux create %s: %v", name, err)
		}
		row, err := db.CreateSession(ctx, store.Session{
			ID: id, ProjectID: proj.ID, TmuxName: name, Title: id, ParentID: parent,
		})
		if err != nil {
			t.Fatalf("create session row %s: %v", id, err)
		}
		return row
	}

	parent := mk("s_parent", "vp_parent", nil)
	mk("s_child_a", "vp_child_a", &parent.ID)
	mk("s_child_b", "vp_child_b", &parent.ID)

	if err := killSessionTree(ctx, db, tm, parent); err != nil {
		t.Fatalf("killSessionTree: %v", err)
	}

	for _, name := range []string{"vp_parent", "vp_child_a", "vp_child_b"} {
		alive, err := tm.Has(ctx, name)
		if err != nil {
			t.Fatalf("Has(%s): %v", name, err)
		}
		if alive {
			t.Errorf("%s is still running after its session was killed; "+
				"its row is about to cascade away and nothing will be able to reach it", name)
		}
	}
}

// Every line doctor can print has a row in the runbook.
//
// The runbook's table is the only place that says what a failure *means*, and
// it said "Eleven lines" over ten rows while doctor printed thirteen. Three
// checks had been added without it -- one of them in the same sitting that
// noticed the count was wrong. A diagnostic whose output is not explained
// anywhere is a diagnostic people read once and guess at.
//
// The same shape as wire.ts and the state enum: a definition with a mirror
// somewhere no compiler looks. Pinned the same way, by reading both.
func TestTheRunbookExplainsEveryDoctorLine(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	book, err := os.ReadFile("../../docs/runbook.md")
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}

	labels := map[string]bool{}
	// Printf'd lines: "[ok  ] hook url           ..." -- the label runs up to
	// the run of spaces that pads the column.
	for _, m := range regexp.MustCompile(`\[(?:ok {2}|FAIL|-- {2}|warn)\] ([a-z][a-z ]*?) {2,}`).
		FindAllStringSubmatch(string(src), -1) {
		labels[m[1]] = true
	}
	// And the two helpers, which pad the label themselves.
	for _, m := range regexp.MustCompile(`(?:fail|skip)\("([a-z][a-z ]*)"`).
		FindAllStringSubmatch(string(src), -1) {
		labels[m[1]] = true
	}
	if len(labels) < 10 {
		t.Fatalf("only found %d doctor labels in main.go (%v); the patterns above have "+
			"stopped matching and this test is checking nothing", len(labels), labels)
	}

	var missing []string
	for label := range labels {
		if !strings.Contains(string(book), "| `"+label+"` |") {
			missing = append(missing, label)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("doctor prints %v, and the runbook's table has no row for them. That table "+
			"is the only place saying what each line means.", missing)
	}
}

// `vibepanel session new --profile` takes a name as well as an id.
//
// The ids are opaque hex, so a flag that only took one would send everybody to
// the settings page with a mouse in order to run a command. What matters more
// is the miss: a name matching nothing has to be an error, because a session
// started against the default endpoint when a gateway profile was asked for is
// a substitution nobody notices until the bill.
func TestTheCLIResolvesAProfileByNameAndRefusesAMiss(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	made, err := db.CreateLaunchProfile(ctx, "prof1", store.LaunchProfile{
		Name: "My Gateway",
		Env: []store.LaunchEnvVar{
			{Name: "ANTHROPIC_BASE_URL", Value: "https://gw"},
			// A secret, because that is the variable a redacted listing loses.
			// The first version of this test used a plain one, and taking the
			// row straight out of ListLaunchProfiles -- which is what the
			// lookup by name reads -- passed it.
			{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sk-secret", Secret: true},
		},
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	if got, rerr := resolveProfile(ctx, db, made.ID); rerr != nil || got == nil || got.ID != made.ID {
		t.Fatalf("by id: %+v %v", got, rerr)
	}
	// Case-insensitively, because nobody retypes a name exactly.
	got, err := resolveProfile(ctx, db, "my gateway")
	if err != nil || got == nil {
		t.Fatalf("by name: %+v %v", got, err)
	}
	// And with the values the launch needs, which the listing does not carry.
	if len(got.Env) != 2 || got.Env[0].Value != "https://gw" {
		t.Fatalf("resolved by name without its values: %+v", got.Env)
	}
	if got.Env[1].Value != "sk-secret" {
		t.Fatalf("the key did not come back with the profile, so a session started "+
			"from the CLI would reach the gateway unauthenticated: %+v", got.Env[1])
	}

	if b, berr := resolveProfile(ctx, db, store.BuiltinShell); berr != nil || b == nil {
		t.Fatalf("a built-in is not resolvable: %+v %v", b, berr)
	}
	if _, merr := resolveProfile(ctx, db, "no such thing"); merr == nil {
		t.Fatal("a profile name that matches nothing was accepted as 'no profile'")
	}
	if none, nerr := resolveProfile(ctx, db, ""); nerr != nil || none != nil {
		t.Fatalf("no --profile should be no profile: %+v %v", none, nerr)
	}
}
