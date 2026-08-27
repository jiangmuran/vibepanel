package store

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openTemp(t *testing.T) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "vibepanel.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, ctx
}

// A variable name arriving from a browser form ends up in a process's
// environment, and tmux refuses none of these itself: measured against 3.6, an
// empty name creates an entry and says nothing, and a newline is stored
// verbatim. Every row here is a session that would look configured and not be.
func TestWhatIsRefusedInAVariable(t *testing.T) {
	for _, tc := range []struct {
		why  string
		v    LaunchEnvVar
		want string // a fragment of the message, or "" for accepted
	}{
		{"an empty name", LaunchEnvVar{Name: "", Value: "x"}, "needs a name"},
		{"a name that is only spaces", LaunchEnvVar{Name: "   ", Value: "x"}, "needs a name"},
		{"a leading digit", LaunchEnvVar{Name: "1FOO", Value: "x"}, "not a variable name"},
		{"a dash", LaunchEnvVar{Name: "A-B", Value: "x"}, "not a variable name"},
		{"a newline in the name", LaunchEnvVar{Name: "A\nB", Value: "x"}, "not a variable name"},
		{"an equals in the name", LaunchEnvVar{Name: "A=B", Value: "x"}, "not a variable name"},
		{"a space in the name", LaunchEnvVar{Name: "A B", Value: "x"}, "not a variable name"},
		{"the panel's own URL", LaunchEnvVar{Name: "VIBEPANEL_URL", Value: "http://evil"}, "panel's own"},
		{"the panel's own token", LaunchEnvVar{Name: "VIBEPANEL_TOKEN", Value: "x"}, "panel's own"},
		{"anything in the panel's namespace", LaunchEnvVar{Name: "VIBEPANEL_ANYTHING", Value: "x"}, "panel's own"},
		{"a newline in the value", LaunchEnvVar{Name: "URL", Value: "a\nb"}, "line break"},
		{"a carriage return in the value", LaunchEnvVar{Name: "URL", Value: "a\rb"}, "line break"},
		{"a null in the value", LaunchEnvVar{Name: "URL", Value: "a\x00b"}, "line break"},
		{"a value that is too long", LaunchEnvVar{Name: "URL", Value: strings.Repeat("x", MaxLaunchEnvValueLen+1)}, "too long"},
		{"a name that is too long", LaunchEnvVar{Name: "A" + strings.Repeat("B", MaxLaunchEnvNameLen), Value: "x"}, "too long"},

		{"an ordinary base URL", LaunchEnvVar{Name: "ANTHROPIC_BASE_URL", Value: "https://gw/v1"}, ""},
		{"an underscore first", LaunchEnvVar{Name: "_PRIVATE", Value: "x"}, ""},
		{"a value with spaces and equals", LaunchEnvVar{Name: "OPTS", Value: "--a=b --c d"}, ""},
		{"an empty value", LaunchEnvVar{Name: "ANTHROPIC_BASE_URL", Value: ""}, ""},
		// Deliberately accepted. Refusing it would look like a boundary and be
		// none: everybody who can save a profile can also create a session
		// running an arbitrary argv, so the shortest path to loading a library
		// is to type the command that loads it. A rule that stops nothing while
		// implying it stops something is what the next person builds on.
		{"LD_PRELOAD", LaunchEnvVar{Name: "LD_PRELOAD", Value: "/tmp/x.so"}, ""},
		{"PATH", LaunchEnvVar{Name: "PATH", Value: "/opt/bin"}, ""},
		// The prefix is the panel's, and a variable that merely starts with the
		// same letters is not.
		{"a name that only looks like the panel's", LaunchEnvVar{Name: "VIBEPANELISH", Value: "x"}, ""},
	} {
		t.Run(tc.why, func(t *testing.T) {
			_, err := ValidateLaunchEnvVar(tc.v)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("%s was refused: %v", tc.why, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s was accepted; it reaches a process's environment", tc.why)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q does not mention %q, so nobody can tell what to fix",
					err, tc.want)
			}
		})
	}
}

func TestAProfileNeedsAName(t *testing.T) {
	if _, err := ValidateLaunchProfile(LaunchProfile{Name: "  "}); err == nil {
		t.Fatal("a nameless profile was accepted; the picker is a list of names")
	}
}

// Last-wins is what tmux does with two -e flags naming one variable, and it
// does it silently. Two rows with the same name in a form is somebody who
// edited the wrong one and is about to wonder why nothing changed.
func TestTwoVariablesWithOneNameAreRefused(t *testing.T) {
	_, err := ValidateLaunchProfile(LaunchProfile{Name: "p", Env: []LaunchEnvVar{
		{Name: "A", Value: "1"},
		{Name: "A", Value: "2"},
	}})
	if err == nil {
		t.Fatal("a duplicate variable name was accepted")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("message %q does not say which problem it is", err)
	}
}

func TestAnEmptyCommandWordIsRefused(t *testing.T) {
	if _, err := ValidateLaunchProfile(LaunchProfile{Name: "p", Command: []string{"", "--x"}}); err == nil {
		t.Fatal("a profile whose command is the empty string was accepted; " +
			"the pane exec's it and dies with nobody watching")
	}
}

func TestTheBoundsAreEnforced(t *testing.T) {
	many := make([]LaunchEnvVar, MaxLaunchEnvVars+1)
	for i := range many {
		many[i] = LaunchEnvVar{Name: "V" + strings.Repeat("X", i), Value: "1"}
	}
	if _, err := ValidateLaunchProfile(LaunchProfile{Name: "p", Env: many}); err == nil {
		t.Error("a profile with more than the cap on variables was accepted")
	}
	argv := make([]string, MaxLaunchArgs+1)
	for i := range argv {
		argv[i] = "x"
	}
	if _, err := ValidateLaunchProfile(LaunchProfile{Name: "p", Command: argv}); err == nil {
		t.Error("a profile with more than the cap on arguments was accepted")
	}
}

// An empty value is a half-filled form far more often than it is somebody
// wanting a variable set to nothing, and the two are different to a program:
// an agent that checks whether its base URL is *set* behaves differently from
// one whose base URL is "". A built-in is a list of names with nothing in them
// and has to run the agent exactly as a bare terminal would.
func TestAnEmptyValueIsNotPassedToTheProcess(t *testing.T) {
	p := LaunchProfile{Env: []LaunchEnvVar{
		{Name: "SET", Value: "yes"},
		{Name: "UNSET", Value: ""},
	}}
	got := p.EnvPairs()
	want := []string{"SET=yes"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnvPairs = %v, want %v", got, want)
	}
}

func TestABuiltinPassesNothing(t *testing.T) {
	for _, p := range BuiltinLaunchProfiles() {
		if got := p.EnvPairs(); len(got) != 0 {
			t.Errorf("built-in %s sets %v; using one directly must be the same as "+
				"running the agent from a bare terminal", p.ID, got)
		}
	}
}

// The ordering is the guard, not tidiness. Measured against tmux 3.6: given two
// -e flags naming one variable, the session gets the last. So a row that
// predates the VIBEPANEL_ rule -- or one edited into the database by hand --
// still cannot displace the panel's own.
func TestThePanelsOwnVariablesGoLast(t *testing.T) {
	p := LaunchProfile{Env: []LaunchEnvVar{{Name: "ANTHROPIC_BASE_URL", Value: "https://gw"}}}
	got := LaunchEnv(&p, []string{"VIBEPANEL_URL=http://127.0.0.1:8787", "VIBEPANEL_TOKEN=t"})
	if len(got) != 3 {
		t.Fatalf("LaunchEnv = %v", got)
	}
	if got[0] != "ANTHROPIC_BASE_URL=https://gw" {
		t.Errorf("the profile's variable is not first: %v", got)
	}
	for i, want := range []string{"VIBEPANEL_URL=", "VIBEPANEL_TOKEN="} {
		if !strings.HasPrefix(got[i+1], want) {
			t.Errorf("the panel's %s is not after the profile's: %v", want, got)
		}
	}
}

func TestNoProfileIsJustThePanelsOwn(t *testing.T) {
	panel := []string{"VIBEPANEL_URL=x"}
	if got := LaunchEnv(nil, panel); !reflect.DeepEqual(got, panel) {
		t.Errorf("LaunchEnv(nil) = %v, want %v", got, panel)
	}
}

// A browser is never sent a secret value, so it cannot send one back. Without
// carrying the stored value through, renaming a profile would wipe every key in
// it -- and the request that does it looks exactly like the one that does not.
func TestRenamingAProfileKeepsItsKey(t *testing.T) {
	prev := LaunchProfile{Name: "old", Env: []LaunchEnvVar{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sk-secret", Secret: true},
	}}
	// What a browser sends back: the name, and an empty value.
	next := LaunchProfile{Name: "new", Env: []LaunchEnvVar{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "", Secret: true, HasValue: true},
	}}
	merged := MergeLaunchSecrets(next, prev)
	if merged.Env[0].Value != "sk-secret" {
		t.Fatalf("the key was lost by a rename: %q", merged.Env[0].Value)
	}
}

func TestANewSecretValueReplacesTheStoredOne(t *testing.T) {
	prev := LaunchProfile{Env: []LaunchEnvVar{{Name: "K", Value: "old", Secret: true}}}
	next := LaunchProfile{Env: []LaunchEnvVar{{Name: "K", Value: "new", Secret: true}}}
	if got := MergeLaunchSecrets(next, prev).Env[0].Value; got != "new" {
		t.Errorf("value = %q, want the new one", got)
	}
}

// Documented rather than hidden: the carry-forward is by name, so renaming a
// secret variable and saving in one edit clears it. Pinned so that changing it
// is a decision rather than an accident.
func TestRenamingASecretVariableClearsIt(t *testing.T) {
	prev := LaunchProfile{Env: []LaunchEnvVar{{Name: "OLD_KEY", Value: "sk", Secret: true}}}
	next := LaunchProfile{Env: []LaunchEnvVar{{Name: "NEW_KEY", Value: "", Secret: true}}}
	if got := MergeLaunchSecrets(next, prev).Env[0].Value; got != "" {
		t.Errorf("value = %q; the name that would have carried it is gone", got)
	}
}

func TestRedactionWithholdsSecretsAndSaysSomethingIsThere(t *testing.T) {
	p := RedactLaunchProfile(LaunchProfile{Env: []LaunchEnvVar{
		{Name: "URL", Value: "https://gw"},
		{Name: "KEY", Value: "sk-secret", Secret: true},
		{Name: "EMPTY", Value: "", Secret: true},
	}})
	if p.Env[0].Value != "https://gw" || !p.Env[0].HasValue {
		t.Errorf("a plain variable was redacted: %+v", p.Env[0])
	}
	if p.Env[1].Value != "" {
		t.Errorf("a secret value left the server: %q", p.Env[1].Value)
	}
	if !p.Env[1].HasValue {
		t.Error("a stored secret says nothing is stored, so the form offers to set one")
	}
	if p.Env[2].HasValue {
		t.Error("an unset secret claims to have a value")
	}
}

// Redaction must not edit the profile the caller still holds, because the
// launch path and the response are the same struct at different moments.
func TestRedactionDoesNotEditWhatItWasGiven(t *testing.T) {
	p := LaunchProfile{Env: []LaunchEnvVar{{Name: "K", Value: "sk", Secret: true}}}
	_ = RedactLaunchProfile(p)
	if p.Env[0].Value != "sk" {
		t.Fatal("redaction wrote through to the original; the launch would set an empty key")
	}
}

func TestABuiltinCannotBeChangedByACaller(t *testing.T) {
	first := BuiltinLaunchProfiles()
	first[1].Command[0] = "not-claude"
	first[1].Env[0].Name = "NOT_A_REAL_VARIABLE"
	second := BuiltinLaunchProfiles()
	if second[1].Command[0] == "not-claude" || second[1].Env[0].Name == "NOT_A_REAL_VARIABLE" {
		t.Fatal("the catalogue is shared; one caller's edit is everybody's profile " +
			"for the life of the process")
	}
}

// Nothing in the column is trusted on the way out. A row can arrive from a
// build with different rules, from a restored backup, or from somebody with
// sqlite3 and an idea -- and the one that matters is the last, because
// VIBEPANEL_TOKEN in a row would hand the panel's hook token to whatever
// address the same row set VIBEPANEL_URL to.
func TestAHandEditedRowIsSanitisedOnTheWayOut(t *testing.T) {
	db, ctx := openTemp(t)
	if _, err := db.CreateLaunchProfile(ctx, "p1", LaunchProfile{Name: "p"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := db.SQL().ExecContext(ctx, `UPDATE launch_profiles SET env = ? WHERE id = ?`,
		`[{"name":"VIBEPANEL_TOKEN","value":"stolen"},{"name":"","value":"x"},`+
			`{"name":"A-B","value":"x"},{"name":"OK","value":"1"}]`, "p1")
	if err != nil {
		t.Fatalf("hand-edit: %v", err)
	}
	got, err := db.GetLaunchProfile(ctx, "p1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Env) != 1 || got.Env[0].Name != "OK" {
		t.Fatalf("env = %+v; the refused rows survived a round trip through the database", got.Env)
	}
}

func TestACorruptColumnIsNotAnError(t *testing.T) {
	db, ctx := openTemp(t)
	if _, err := db.CreateLaunchProfile(ctx, "p1", LaunchProfile{Name: "p"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`UPDATE launch_profiles SET env = 'not json', command = '{' WHERE id = ?`, "p1"); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	got, err := db.GetLaunchProfile(ctx, "p1")
	if err != nil {
		t.Fatalf("a corrupt column took the whole read down: %v", err)
	}
	if len(got.Env) != 0 || len(got.Command) != 0 {
		t.Errorf("got %+v, want an empty profile", got)
	}
}

func TestTheListPutsBuiltinsFirstAndRowsByName(t *testing.T) {
	db, ctx := openTemp(t)
	// Names chosen so that the collation decides: under SQLite's default binary
	// comparison 'Z' (90) sorts before 'b' (98), so "Zulu" would come second.
	// The first fixture here was "zeta"/"Alpha"/"middle", which gives the same
	// order either way -- the assertion passed without testing anything.
	for _, name := range []string{"beta", "Alpha", "Zulu"} {
		if _, err := db.CreateLaunchProfile(ctx, "id-"+name, LaunchProfile{Name: name}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	list, err := db.ListLaunchProfiles(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	builtins := BuiltinLaunchProfiles()
	if len(list) != len(builtins)+3 {
		t.Fatalf("list has %d entries, want %d", len(list), len(builtins)+3)
	}
	for i, b := range builtins {
		if list[i].ID != b.ID || !list[i].Builtin {
			t.Fatalf("entry %d is %+v, want the built-in %s", i, list[i], b.ID)
		}
	}
	var names []string
	for _, p := range list[len(builtins):] {
		if p.Builtin {
			t.Errorf("%s is a row and says it is built in", p.ID)
		}
		names = append(names, p.Name)
	}
	// Case-insensitive, because "Alpha" filed after "zeta" is a picker whose
	// order nobody can predict.
	if !reflect.DeepEqual(names, []string{"Alpha", "beta", "Zulu"}) {
		t.Errorf("rows are ordered %v", names)
	}
}

func TestTheListNeverCarriesASecretValue(t *testing.T) {
	db, ctx := openTemp(t)
	_, err := db.CreateLaunchProfile(ctx, "p1", LaunchProfile{Name: "p", Env: []LaunchEnvVar{
		{Name: "KEY", Value: "sk-secret", Secret: true},
	}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	list, err := db.ListLaunchProfiles(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range list {
		for _, v := range p.Env {
			if v.Secret && v.Value != "" {
				t.Fatalf("a secret value is in the list: %s=%q", v.Name, v.Value)
			}
		}
	}
}

// The launch path is the one caller that needs the values, and it hands them to
// tmux rather than to a response.
func TestGetKeepsTheValuesTheLaunchNeeds(t *testing.T) {
	db, ctx := openTemp(t)
	_, err := db.CreateLaunchProfile(ctx, "p1", LaunchProfile{Name: "p", Env: []LaunchEnvVar{
		{Name: "KEY", Value: "sk-secret", Secret: true},
	}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := db.GetLaunchProfile(ctx, "p1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Env[0].Value != "sk-secret" {
		t.Fatalf("the launch path cannot see the value it has to set: %+v", got.Env[0])
	}
}

func TestCreateDoesNotReturnTheSecretItWasGiven(t *testing.T) {
	db, ctx := openTemp(t)
	rec, err := db.CreateLaunchProfile(ctx, "p1", LaunchProfile{Name: "p", Env: []LaunchEnvVar{
		{Name: "KEY", Value: "sk-secret", Secret: true},
	}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.Env[0].Value != "" {
		t.Fatalf("the response to a create echoed the key back: %q", rec.Env[0].Value)
	}
}

func TestABuiltinIsReadableByIDAndIsNotARow(t *testing.T) {
	db, ctx := openTemp(t)
	p, err := db.GetLaunchProfile(ctx, BuiltinShell)
	if err != nil {
		t.Fatalf("the shell built-in cannot be resolved: %v", err)
	}
	if !p.Builtin || len(p.Command) != 0 {
		t.Errorf("got %+v, want the login-shell built-in", p)
	}
	if !IsBuiltinLaunchProfile(BuiltinShell) || IsBuiltinLaunchProfile("p1") {
		t.Error("IsBuiltinLaunchProfile does not agree with the catalogue")
	}
}

func TestAMissingProfileIsNotFound(t *testing.T) {
	db, ctx := openTemp(t)
	if _, err := db.GetLaunchProfile(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := db.UpdateLaunchProfile(ctx, "nope", LaunchProfile{Name: "x"}); err != ErrNotFound {
		t.Fatalf("update err = %v, want ErrNotFound", err)
	}
	if err := db.DeleteLaunchProfile(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("delete err = %v, want ErrNotFound", err)
	}
}

// Migration v13. The number is a literal for the same reason the others are:
// migrations are append-only, so len(migrations)-1 moves under the test and
// starts checking whatever was added last.
func TestMigrationV13AddsProfilesAndTheColumnThatRestoresThem(t *testing.T) {
	db, ctx := openTemp(t)
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO launch_profiles (id, name, created_at, updated_at) VALUES ('x','n',1,1)`); err != nil {
		t.Fatalf("the table is not there or its defaults do not cover command and env: %v", err)
	}
	var cmd, env string
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT command, env FROM launch_profiles WHERE id = 'x'`).Scan(&cmd, &env); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if cmd != "" || env != "" {
		t.Errorf("defaults are %q/%q, want empty -- a JSON literal baked in here would "+
			"be today's idea of a default frozen into rows written years from now", cmd, env)
	}

	p, err := db.CreateProject(ctx, "p1", "n", t.TempDir())
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	s, err := db.CreateSession(ctx, Session{
		ID: "s1", ProjectID: p.ID, TmuxName: "vp_s1", LaunchProfileID: "prof-1"})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if s.LaunchProfileID != "prof-1" {
		t.Fatalf("create returned %q", s.LaunchProfileID)
	}
	got, err := db.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// The one field a restore reads to rebuild the environment. Without it the
	// session comes back pointed at the default endpoint, which looks like it
	// worked.
	if got.LaunchProfileID != "prof-1" {
		t.Errorf("launchProfileId = %q; a restore would lose the profile's environment",
			got.LaunchProfileID)
	}
}
