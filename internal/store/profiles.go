package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jiangmuran/vibepanel/internal/session"
)

// Launch profiles: a name, an argv, and the environment to start it with.
//
// The panel could already be told what to run — one free-text argv per session
// — and could not be told anything about the environment it runs in. That is
// the half people actually need: the same agent pointed at Anthropic, at a
// company proxy and at a self-hosted gateway is three configurations that
// differ only in ANTHROPIC_BASE_URL, and retyping it is the thing the feature
// exists to stop.
//
// # Why environment variables rather than an apiHost field
//
// A dedicated "API host" column would have to know which variable each agent
// reads: ANTHROPIC_BASE_URL for claude, OPENAI_BASE_URL for codex, and for
// opencode nothing at all, because its endpoint is per-provider and lives in
// its own configuration file. That mapping is guesswork that goes stale with
// every release of somebody else's tool, and the day it is wrong the panel
// silently sets a variable nothing reads. Variables are what the agents
// document; the panel carries them and names none of them itself, except in
// the built-in catalogue below, where getting one wrong costs a prefilled
// field somebody can edit rather than a launch that quietly does nothing.
//
// # Why keys are allowed in here at all
//
// A base URL almost always arrives with a key, so the choice is not whether
// the panel touches credentials but where they end up. Refusing them would
// send people to the argv field — `env ANTHROPIC_AUTH_TOKEN=sk-... claude` —
// which is worse in three measurable ways: it lands in launch_command, which
// the restore dialog prints on screen; it lands in the process command line,
// where every other user on the machine can read it out of `ps`; and it is
// re-run verbatim on every restore. Measured: a variable passed to tmux with
// -e does not appear in `ps -eo args` at all, and one passed in the argv does.
//
// So they are allowed, and Secret below is what the panel does about it:
//
//   - the value is never sent back to a browser once stored, so the settings
//     page, a screenshot of it and an unlocked phone disclose the name and
//     nothing else;
//   - it reaches the process through tmux's -e, never through an argv;
//   - it never reaches the audit log, which records profile names only.
//
// What that does not do is encrypt it. It is plaintext in the SQLite file, and
// the settings page says so in one line rather than leaving the reader to
// assume otherwise. Encrypting it with a key stored beside the database is not
// encryption; it is obfuscation with a migration path to maintain, and it
// would let the same settings page make a promise the file cannot keep.

// LaunchEnvVar is one variable a profile sets.
type LaunchEnvVar struct {
	Name string `json:"name"`
	// Value is empty on the way out for a secret, whatever is stored.
	Value string `json:"value"`
	// Secret withholds the value from every read. Editing means replacing.
	Secret bool `json:"secret"`
	// HasValue reports that something is stored, which is the only thing a
	// browser can learn about a secret. Set on the way out; ignored on the way
	// in, where an empty secret value means "keep what is already there".
	HasValue bool `json:"hasValue"`
}

// LaunchProfile is a named way to start a session.
type LaunchProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Builtin marks a profile that lives in this file rather than in a row.
	// The frontend translates its name; a row's name is whatever was typed.
	Builtin bool `json:"builtin"`
	// Command is the argv, empty for the user's login shell.
	Command []string `json:"command"`
	// Env is the variables to start it with, in the order they are shown.
	Env       []LaunchEnvVar `json:"env"`
	CreatedAt int64          `json:"createdAt"`
	UpdatedAt int64          `json:"updatedAt"`
}

// The bounds. Every one of these is a refusal with a message rather than a
// truncation: a profile that silently kept half of what was typed is a session
// that starts against the wrong endpoint.
const (
	MaxLaunchProfiles    = 64
	MaxLaunchArgs        = 32
	MaxLaunchArgLen      = 4096
	MaxLaunchEnvVars     = 32
	MaxLaunchEnvNameLen  = 128
	MaxLaunchEnvValueLen = 8192
)

// BuiltinPrefix marks the ids that are not rows. A generated id is 16 hex
// characters, so no row can ever collide with one.
const BuiltinPrefix = "builtin:"

// BuiltinShell is the profile the panel used to have with no name: no command,
// no variables, the user's login shell. It is what the "new session" button did
// before there was anything to choose.
const BuiltinShell = BuiltinPrefix + "shell"

// The catalogue, in the order it is offered.
//
// Code rather than rows seeded by the migration, and the reason is the panel is
// bilingual. A seeded row's name is a string in whatever language the person
// installing happened to be using, frozen at install time; a built-in's name is
// a dictionary key with both languages on one line, and a Go test fails if one
// is missing. Two smaller reasons point the same way: a release can correct a
// built-in that turned out to be wrong, and a built-in cannot be deleted into a
// state where a fresh panel offers nothing.
//
// The variables carry names and no values, which is the whole point of them: an
// empty value is not passed to the process (see EnvPairs), so using a built-in
// directly runs the agent exactly as a bare terminal would, and duplicating one
// in the settings page gives you a form with the right variable names already
// spelled correctly. Getting a name wrong here costs a prefilled field somebody
// edits; guessing one for opencode would cost more than that, so opencode has
// none — its endpoint is chosen per provider in its own configuration, and
// there is no single variable to name.
var builtinProfiles = []LaunchProfile{
	{ID: BuiltinShell, Name: "Shell"},
	{ID: BuiltinPrefix + "claude", Name: "Claude Code", Command: []string{"claude"}, Env: []LaunchEnvVar{
		{Name: "ANTHROPIC_BASE_URL"},
		{Name: "ANTHROPIC_AUTH_TOKEN", Secret: true},
	}},
	{ID: BuiltinPrefix + "codex", Name: "Codex", Command: []string{"codex"}, Env: []LaunchEnvVar{
		{Name: "OPENAI_BASE_URL"},
		{Name: "OPENAI_API_KEY", Secret: true},
	}},
	{ID: BuiltinPrefix + "opencode", Name: "opencode", Command: []string{"opencode"}},
}

// BuiltinLaunchProfiles returns the catalogue.
//
// Copied on the way out, because a caller that appended to Command or Env would
// be editing the catalogue for the life of the process.
func BuiltinLaunchProfiles() []LaunchProfile {
	out := make([]LaunchProfile, 0, len(builtinProfiles))
	for _, p := range builtinProfiles {
		p.Builtin = true
		// make, not append-to-nil. `append([]string(nil))` of an empty source
		// is still nil, and nil marshals as `null` -- so the Shell profile,
		// which has neither a command nor an environment, arrived in the
		// browser as {"command": null, "env": null} and the settings page died
		// on `p.env.map(...)` before it drew anything. docs/api.md's first
		// convention is that arrays are always arrays; this was the one place
		// that broke it, and it took the whole dialog with it.
		p.Command = append(make([]string, 0, len(p.Command)), p.Command...)
		p.Env = append(make([]LaunchEnvVar, 0, len(p.Env)), p.Env...)
		out = append(out, p)
	}
	return out
}

// IsBuiltinLaunchProfile reports whether id names one.
func IsBuiltinLaunchProfile(id string) bool {
	for _, p := range builtinProfiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

// envNameOK is the POSIX rule for a name a shell can export, and it is the
// only one applied to names.
//
// Measured against tmux 3.6 rather than assumed, because tmux refuses none of
// this: `-e '=x'` creates an entry with an empty name and says nothing, `-e
// 'JUSTNAME'` with no '=' is accepted and silently sets nothing, and a name
// containing a newline is stored verbatim. All three produce a session that
// looks configured and is not, which is the failure mode this whole project
// keeps running into.
var envNameOK = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnvPrefix is the panel's own namespace, and a profile may not write
// into it.
//
// This is the one guard here that is a security boundary rather than a
// usability one. VIBEPANEL_URL is where a hook posts and VIBEPANEL_TOKEN is
// what it authenticates with, so a profile setting either would point every
// state report a session makes at an address of somebody else's choosing, with
// the panel's own hook token attached.
//
// Enforced twice on purpose. Here, so it cannot be saved; and by ordering in
// LaunchEnv, so a row that predates this rule -- or one edited into the
// database by hand -- still cannot win. Measured: with two -e flags naming the
// same variable, tmux takes the last one.
const reservedEnvPrefix = "VIBEPANEL_"

// ValidateLaunchEnvVar checks one variable and returns the cleaned version.
//
// What is deliberately *not* refused: LD_PRELOAD, LD_LIBRARY_PATH, PATH and
// every other variable that changes what a command resolves to or loads.
// Refusing them would look like a security measure and be none: everyone who
// can reach this endpoint can already create a session running an arbitrary
// argv, so the shortest path to loading a library is to type the command that
// loads it. A rule that stops nothing while implying it stops something is
// worse than no rule, because the next person builds on the implication.
func ValidateLaunchEnvVar(v LaunchEnvVar) (LaunchEnvVar, error) {
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return v, fmt.Errorf("a variable needs a name")
	}
	if len(v.Name) > MaxLaunchEnvNameLen {
		return v, fmt.Errorf("variable name is too long")
	}
	if !envNameOK.MatchString(v.Name) {
		return v, fmt.Errorf("%q is not a variable name: letters, digits and underscore, "+
			"not starting with a digit", v.Name)
	}
	if strings.HasPrefix(v.Name, reservedEnvPrefix) {
		return v, fmt.Errorf("%s is the panel's own; a session that reports its state "+
			"somewhere else is worse than one that cannot report at all", v.Name)
	}
	if len(v.Value) > MaxLaunchEnvValueLen {
		return v, fmt.Errorf("the value of %s is too long", v.Name)
	}
	// A newline, a carriage return or a NUL in a value. exec refuses the NUL
	// outright, and the other two are accepted by tmux and stored verbatim --
	// after which `show-environment` emits a value spanning two lines, which is
	// what SessionEnvValue parses to find out where a live session reports to.
	// Nothing that belongs in a base URL or a key contains one.
	if strings.ContainsAny(v.Value, "\n\r\x00") {
		return v, fmt.Errorf("the value of %s cannot contain a line break", v.Name)
	}
	return v, nil
}

// ValidateLaunchProfile checks a whole profile arriving from a browser.
//
// The caller is responsible for restoring withheld secret values first; see
// MergeLaunchSecrets.
func ValidateLaunchProfile(p LaunchProfile) (LaunchProfile, error) {
	p.Name = session.TruncateTitle(strings.TrimSpace(p.Name))
	if p.Name == "" {
		return p, fmt.Errorf("a profile needs a name")
	}
	if len(p.Command) > MaxLaunchArgs {
		return p, fmt.Errorf("that is more arguments than a command needs")
	}
	for i, a := range p.Command {
		if len(a) > MaxLaunchArgLen {
			return p, fmt.Errorf("argument %d is too long", i+1)
		}
		if strings.ContainsRune(a, 0) {
			return p, fmt.Errorf("argument %d contains a null byte", i+1)
		}
		if !utf8.ValidString(a) {
			return p, fmt.Errorf("argument %d is not valid text", i+1)
		}
	}
	// An empty first word with arguments after it. tmux would exec "" and the
	// pane would die instantly with a message nobody is watching for.
	if len(p.Command) > 0 && strings.TrimSpace(p.Command[0]) == "" {
		return p, fmt.Errorf("the command is empty")
	}
	if len(p.Env) > MaxLaunchEnvVars {
		return p, fmt.Errorf("that is more variables than a profile needs")
	}
	seen := map[string]bool{}
	env := make([]LaunchEnvVar, 0, len(p.Env))
	for _, v := range p.Env {
		clean, err := ValidateLaunchEnvVar(v)
		if err != nil {
			return p, err
		}
		// Last-wins is what tmux does, and it does it silently. Two rows named
		// the same thing in a form is a person who edited the wrong one and is
		// about to wonder why their change did nothing.
		if seen[clean.Name] {
			return p, fmt.Errorf("%s is listed twice", clean.Name)
		}
		seen[clean.Name] = true
		clean.HasValue = clean.Value != ""
		env = append(env, clean)
	}
	p.Env = env
	if p.Command == nil {
		p.Command = []string{}
	}
	return p, nil
}

// EnvPairs renders the variables to hand tmux, as "K=V".
//
// A variable with an empty value is left out rather than exported empty. The
// two are different to a process -- an agent that checks whether its base URL
// is *set* behaves differently from one whose base URL is the empty string --
// and of the two mistakes this can make, the common one is a half-filled form.
// A built-in profile is a list of variable names with nothing in them, and it
// has to run the agent exactly as a bare terminal would.
func (p LaunchProfile) EnvPairs() []string {
	out := make([]string, 0, len(p.Env))
	for _, v := range p.Env {
		if v.Value == "" {
			continue
		}
		out = append(out, v.Name+"="+v.Value)
	}
	return out
}

// LaunchEnv puts a profile's variables before the panel's own.
//
// The order is the guard, not a tidiness. Measured against tmux 3.6: given two
// -e flags naming one variable, the session gets the last. So the panel's
// VIBEPANEL_URL and VIBEPANEL_TOKEN go last and a profile cannot displace them,
// whatever is in the row -- which matters because rows outlive the validation
// that wrote them, and a database restored from a backup or edited by hand has
// never been through it at all.
//
// Here rather than at each of the three places that create a tmux session,
// because there are three and this project has already had one of them drift:
// `vibepanel session new` used to build its own environment and left out the
// hook token, producing sessions that looked configured and reported nothing.
func LaunchEnv(profile *LaunchProfile, panel []string) []string {
	if profile == nil {
		return panel
	}
	return append(profile.EnvPairs(), panel...)
}

// MergeLaunchSecrets carries stored secret values through an edit.
//
// A browser is never sent a secret value, so it cannot send one back, and a
// save would otherwise wipe every key in the profile the moment somebody
// renamed it. An incoming secret with an empty value means "keep what is
// stored"; anything else replaces it.
//
// Matched by name, which has one consequence worth knowing rather than hiding:
// renaming a secret variable and saving in the same edit clears its value,
// because the name that would have carried it forward is gone. The settings
// page says so next to the field.
func MergeLaunchSecrets(next LaunchProfile, prev LaunchProfile) LaunchProfile {
	stored := map[string]string{}
	for _, v := range prev.Env {
		if v.Secret && v.Value != "" {
			stored[v.Name] = v.Value
		}
	}
	for i, v := range next.Env {
		if v.Secret && v.Value == "" {
			next.Env[i].Value = stored[v.Name]
		}
	}
	return next
}

// RedactLaunchProfile is what leaves the server.
//
// Called on every read rather than at each handler, so that a route added later
// cannot be the one that forgets.
func RedactLaunchProfile(p LaunchProfile) LaunchProfile {
	env := make([]LaunchEnvVar, len(p.Env))
	copy(env, p.Env)
	for i := range env {
		env[i].HasValue = env[i].Value != ""
		if env[i].Secret {
			env[i].Value = ""
		}
	}
	p.Env = env
	return p
}

// ─── rows ─────────────────────────────────────────────────────────────────

// CreateLaunchProfile inserts a profile. The id is the caller's, as everywhere
// else here.
func (d *DB) CreateLaunchProfile(ctx context.Context, id string, p LaunchProfile) (LaunchProfile, error) {
	p.ID = id
	p.Builtin = false
	p.CreatedAt = now()
	p.UpdatedAt = p.CreatedAt
	cmd, env, err := encodeLaunch(p)
	if err != nil {
		return LaunchProfile{}, err
	}
	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO launch_profiles (id, name, command, env, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, cmd, env, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return LaunchProfile{}, fmt.Errorf("store: insert launch profile: %w", err)
	}
	return RedactLaunchProfile(p), nil
}

// UpdateLaunchProfile replaces a profile's name, command and variables.
func (d *DB) UpdateLaunchProfile(ctx context.Context, id string, p LaunchProfile) error {
	cmd, env, err := encodeLaunch(p)
	if err != nil {
		return err
	}
	return d.exec1(ctx,
		`UPDATE launch_profiles SET name = ?, command = ?, env = ?, updated_at = ? WHERE id = ?`,
		p.Name, cmd, env, now(), id)
}

// DeleteLaunchProfile removes one.
//
// Sessions keep pointing at it. No foreign key and no cascade, deliberately:
// launch_profile_id on a session is a record of how that session was started,
// and a deleted profile has to leave a session that can say "the profile it
// used is gone" rather than one that claims it never had one. See
// GetLaunchProfile's ErrNotFound, which is what the restore path reads.
func (d *DB) DeleteLaunchProfile(ctx context.Context, id string) error {
	return d.exec1(ctx, `DELETE FROM launch_profiles WHERE id = ?`, id)
}

// CountLaunchProfiles is how many rows there are, for the cap on creating more.
func (d *DB) CountLaunchProfiles(ctx context.Context) (int, error) {
	var n int
	if err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM launch_profiles`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count launch profiles: %w", err)
	}
	return n, nil
}

// ListLaunchProfiles returns the built-in catalogue followed by the rows.
//
// Built-ins first and in catalogue order, rows after them by name: the picker
// is the most-used control in the panel and a list whose order moves under
// somebody's finger is worse than one that is slightly wrong. Nothing here is
// sorted by recency for the same reason.
//
// Values are redacted. There is no unredacted read of a profile outside this
// file; the launch path uses launchProfileFor below, which is the only caller
// that gets the values, and it hands them to tmux rather than to a response.
func (d *DB) ListLaunchProfiles(ctx context.Context) ([]LaunchProfile, error) {
	rows, err := d.listLaunchRows(ctx)
	if err != nil {
		return nil, err
	}
	out := BuiltinLaunchProfiles()
	for _, p := range rows {
		out = append(out, RedactLaunchProfile(p))
	}
	return out, nil
}

func (d *DB) listLaunchRows(ctx context.Context) ([]LaunchProfile, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, name, command, env, created_at, updated_at
		FROM launch_profiles ORDER BY name COLLATE NOCASE ASC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list launch profiles: %w", err)
	}
	defer rows.Close()

	out := []LaunchProfile{}
	for rows.Next() {
		p, err := scanLaunch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetLaunchProfile returns one profile with its secret values intact, or a
// built-in from the catalogue.
//
// The values are here because this is what the launch path needs; every path
// that answers a request goes through RedactLaunchProfile first.
func (d *DB) GetLaunchProfile(ctx context.Context, id string) (LaunchProfile, error) {
	for _, p := range BuiltinLaunchProfiles() {
		if p.ID == id {
			return p, nil
		}
	}
	row := d.sql.QueryRowContext(ctx, `
		SELECT id, name, command, env, created_at, updated_at
		FROM launch_profiles WHERE id = ?`, id)
	p, err := scanLaunch(row)
	if err != nil {
		return LaunchProfile{}, err
	}
	return p, nil
}

func scanLaunch(row scanner) (LaunchProfile, error) {
	var p LaunchProfile
	var cmd, env string
	err := row.Scan(&p.ID, &p.Name, &cmd, &env, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return LaunchProfile{}, ErrNotFound
	}
	if err != nil {
		return LaunchProfile{}, fmt.Errorf("store: scan launch profile: %w", err)
	}
	p.Command = decodeLaunchCommand(cmd)
	p.Env = decodeLaunchEnv(env)
	return p, nil
}

func encodeLaunch(p LaunchProfile) (cmd, env string, err error) {
	c, err := json.Marshal(emptyIfNil(p.Command))
	if err != nil {
		return "", "", fmt.Errorf("store: encode launch command: %w", err)
	}
	e, err := json.Marshal(emptyVars(p.Env))
	if err != nil {
		return "", "", fmt.Errorf("store: encode launch env: %w", err)
	}
	return string(c), string(e), nil
}

func emptyVars(v []LaunchEnvVar) []LaunchEnvVar {
	if v == nil {
		return []LaunchEnvVar{}
	}
	return v
}

func decodeLaunchCommand(s string) []string {
	var out []string
	if s == "" {
		return []string{}
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		// A column that does not parse is a profile the panel cannot honestly
		// run. Falling back to a login shell under an agent's name is the lie
		// the restore dialog exists to avoid telling, so this returns nothing
		// and the caller starts a shell it has not named anything else.
		return []string{}
	}
	return emptyIfNil(out)
}

// decodeLaunchEnv parses the column and drops anything the validator refuses.
//
// Nothing in the column is trusted on the way out, for the same reason a stored
// board is not: a row can arrive from a build that had different rules, from a
// restored backup, or from somebody with sqlite3 and an idea. Dropping rather
// than repairing, and dropping at read rather than at launch, so that what the
// settings page shows and what the process is given are the same list -- a
// variable filtered out only on the way to tmux would sit in the form looking
// like it worked.
func decodeLaunchEnv(s string) []LaunchEnvVar {
	if s == "" {
		return []LaunchEnvVar{}
	}
	var raw []LaunchEnvVar
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return []LaunchEnvVar{}
	}
	out := make([]LaunchEnvVar, 0, len(raw))
	seen := map[string]bool{}
	for _, v := range raw {
		clean, err := ValidateLaunchEnvVar(v)
		if err != nil || seen[clean.Name] {
			continue
		}
		seen[clean.Name] = true
		clean.HasValue = clean.Value != ""
		out = append(out, clean)
	}
	return out
}
