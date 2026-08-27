// Package store owns the SQLite database: schema, migrations and typed access.
//
// SQLite via modernc.org/sqlite (a pure-Go translation, no cgo) because the
// whole distribution story is CGO_ENABLED=0 static binaries that cross-compile
// to arm64 without a toolchain. Linking the C library would trade that away for
// nothing this workload needs.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jiangmuran/vibepanel/internal/id"
)

//go:embed schema.sql
var schemaSQL string

// migrations are applied in order to bring a database up to date. Index 0 is
// the initial schema; each later entry is one step.
//
// Additive steps only, and never an edit to an earlier one: a released binary
// has to be able to open a database written by an older release, and a
// migration that has already run somewhere cannot be changed.
var migrations = []func(tx *sql.Tx) error{
	func(tx *sql.Tx) error {
		_, err := tx.Exec(schemaSQL)
		return err
	},
	// v2: bottom terminals become ordinary sessions with a parent.
	//
	// They were their own table, which would have meant a parallel
	// implementation of everything sessions already have — attaching, replay,
	// state detection, naming. A nullable parent column gets all of it for
	// free, and "a terminal is a session" is one fewer concept.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE sessions ADD COLUMN parent_session_id TEXT
			     REFERENCES sessions(id) ON DELETE CASCADE`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id)`,
			`DROP TABLE IF EXISTS bottom_terminals`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},
	// v3: browser sessions and an audit trail.
	//
	// Login sessions are their own table rather than a signed cookie so they
	// can be revoked: a panel that hands out a terminal on the public internet
	// needs a way to say "not that device any more" without changing the
	// password everywhere.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS auth_sessions (
			     token_hash   BLOB PRIMARY KEY,
			     user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			     created_at   INTEGER NOT NULL,
			     expires_at   INTEGER NOT NULL,
			     last_seen_at INTEGER NOT NULL,
			     user_agent   TEXT NOT NULL DEFAULT '',
			     ip           TEXT NOT NULL DEFAULT ''
			 )`,
			`CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_auth_sessions_expiry ON auth_sessions(expires_at)`,
			`CREATE TABLE IF NOT EXISTS audit_log (
			     id       INTEGER PRIMARY KEY AUTOINCREMENT,
			     at       INTEGER NOT NULL,
			     event    TEXT NOT NULL,
			     username TEXT NOT NULL DEFAULT '',
			     ip       TEXT NOT NULL DEFAULT '',
			     detail   TEXT NOT NULL DEFAULT ''
			 )`,
			`CREATE INDEX IF NOT EXISTS idx_audit_at ON audit_log(at DESC)`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},
	// v4: store the whole WebAuthn credential.
	//
	// The v1 table decomposed it into columns, which loses the fields the
	// library adds over time (attestation type, backup state, the transport
	// list, flags). Keeping the marshalled credential means an upgrade of the
	// library does not silently drop something a browser relies on; the
	// individual columns stay for indexing and for the settings page.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE credentials ADD COLUMN data BLOB NOT NULL DEFAULT x''`,
			`ALTER TABLE credentials ADD COLUMN user_handle BLOB NOT NULL DEFAULT x''`,
			`CREATE INDEX IF NOT EXISTS idx_credentials_handle ON credentials(user_handle)`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},
	// v5: a session's process can be gone while the session is still there.
	//
	// tmux keeps a dead pane on screen (remain-on-exit), and the panel used to
	// read that as "done" — the same thing it says about an agent that finished
	// the job. A crash and a success looked identical in the sidebar, which is
	// the one comparison the sidebar exists to make.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE sessions ADD COLUMN exited INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sessions ADD COLUMN exit_status INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},
	// v6: notes get a revision counter.
	//
	// The first attempt at "refuse a write that lands on somebody else's" used
	// updated_at, which is unix seconds — and two people editing the same note
	// collide inside one second by definition. A counter cannot be blind that
	// way. Starting at 1 for existing rows so that a client which has never
	// heard of revisions still fails the check rather than passing it with a
	// zero.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE notes ADD COLUMN rev INTEGER NOT NULL DEFAULT 0`,
			`UPDATE notes SET rev = 1`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},

	// v7: the two audit events that did not share a prefix with their pair.
	//
	// Eleven of the thirteen are dot-separated -- login / login.failed,
	// setup.completed / setup.rejected, passkey.registered / passkey.removed --
	// and `password_changed` / `password_change_refused` were not, so the one
	// pair a reader most wants together did not group under `GROUP BY event`,
	// which is the query the runbook hands the operator, and did not share a
	// prefix for a fail2ban rule to match on.
	//
	// The rows already written are migrated rather than left, because a history
	// spelled two ways is worse than either spelling: the group-by that the
	// rename exists to fix would still return two rows for one thing. Anyone
	// with a fail2ban rule on the old spelling has to update it, and the README
	// does not advertise one.
	func(tx *sql.Tx) error {
		for from, to := range map[string]string{
			"password_changed":        "password.changed",
			"password_change_refused": "password.change_refused",
		} {
			if _, err := tx.Exec(`UPDATE audit_log SET event = ? WHERE event = ?`, to, from); err != nil {
				return fmt.Errorf("rename audit event %s: %w", from, err)
			}
		}
		return nil
	},

	// v8: API tokens, so a program can drive the panel.
	//
	// The session cookie is a browser's credential: it expires, it is bound to
	// SameSite=Strict, and getting one means posting a password to a login
	// endpoint and keeping a jar. An agent asked to "open a session in the
	// billing project and tell me when it stops" should not have to do any of
	// that, and should not have to be given the password either -- a token can
	// be revoked without changing what you type.
	//
	// Same storage shape as an auth session: the hash, never the token. The
	// prefix is stored in the clear so the settings page can show you which one
	// you are about to revoke without being able to reconstruct it.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS api_tokens (
			     id          TEXT PRIMARY KEY,
			     token_hash  BLOB NOT NULL UNIQUE,
			     prefix      TEXT NOT NULL,
			     name        TEXT NOT NULL,
			     user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			     created_at  INTEGER NOT NULL,
			     last_used_at INTEGER NOT NULL DEFAULT 0
			 )`,
			`CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id)`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},

	// v9: enough to rebuild a session the machine took with it.
	//
	// tmux outliving the *panel* is the premise of the project. tmux does not
	// outlive the *machine*: a reboot takes the server and every session in it,
	// and what was left was a sidebar full of rows marked GONE with no way
	// back. Every column needed to recreate one was already here except the
	// one that matters most.
	//
	// launch_command is that one. `command` holds #{pane_current_command} --
	// the name of whatever was running last, "node" for an agent and "bash" for
	// a shell somebody used -- which the poller overwrites every two seconds. It
	// is a label, not an argv, and handleRestartSession already says so in as
	// many words and falls back to a login shell because of it. This is the argv
	// the session was created with, JSON, and it is never written again after
	// creation. Empty string means "not recorded" -- every row that predates
	// this migration, and nothing else; `[]` is the recorded fact that a session
	// was asked for with no command, which is a login shell and is exactly
	// reproducible. The two have to be distinguishable, because one of them is
	// something the UI must admit to and the other is not.
	//
	// restore_on_boot is the opt-in. Rebuilding two dozen agents unasked on
	// every boot is a worse failure than the one this fixes, so the default is
	// off and the panel offers rather than acts.
	//
	// restored_at is what stops a restored screen reading as a live one. The
	// pane carries a banner of its own, but a banner scrolls; this is the fact
	// the sidebar and the header can keep showing.
	//
	// The scrollback goes in its own table, not in a column here. `sessions` is
	// read whole by ListSessions on every poll tick and on every state
	// broadcast -- at two dozen sessions that is a couple of dozen rows twice a
	// second -- and a few hundred kilobytes of blob per row would be dragged
	// through all of it. A separate table is read only by the thing that
	// restores.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE sessions ADD COLUMN launch_command TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sessions ADD COLUMN restore_on_boot INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sessions ADD COLUMN restored_at INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE IF NOT EXISTS session_scrollback (
			     session_id  TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
			     captured_at INTEGER NOT NULL,
			     lines       INTEGER NOT NULL DEFAULT 0,
			     truncated   INTEGER NOT NULL DEFAULT 0,
			     content     BLOB NOT NULL
			 )`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},

	// v10: read-only share links, for a dashboard on a second screen.
	//
	// A separate table from api_tokens rather than a `scope` column on it, and
	// that is the whole security design in one decision. A scope column is a
	// flag a handler reads, which means every handler that forgets to read it
	// is a hole, and the handler that forgets is the one added next year. Two
	// tables means the question "can this credential reach the terminal" is
	// answered by which table the lookup went to, and the lookup is written
	// once, in the middleware for one route.
	//
	// Same storage as api_tokens otherwise: the hash, never the token, plus a
	// prefix in the clear so a row can be named on the way to being revoked.
	//
	// expires_at is 0 for "never", not NULL. Every caller handles the
	// never-expires case anyway, and a NULL that means the same thing as 0 is
	// one more way to write the comparison wrong.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS share_links (
			     id          TEXT PRIMARY KEY,
			     token_hash  BLOB NOT NULL UNIQUE,
			     prefix      TEXT NOT NULL,
			     name        TEXT NOT NULL,
			     -- 'counts' or 'names'; see store.ShareDetail.
			     detail      TEXT NOT NULL DEFAULT 'counts',
			     user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			     created_at  INTEGER NOT NULL,
			     expires_at  INTEGER NOT NULL DEFAULT 0,
			     last_used_at INTEGER NOT NULL DEFAULT 0
			 )`,
			`CREATE INDEX IF NOT EXISTS idx_share_links_user ON share_links(user_id)`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},

	// v11: what the agents recorded about their own token spend.
	//
	// Two tables because they answer two different questions and rot at
	// different rates.
	//
	// usage_files is the cursor. A pass compares (size, modified_at) against
	// what is on disk and reads nothing when they match — which is the whole
	// of the incremental design, because on the machine this was written on
	// the transcripts are 2.16 GB across 568 files and all but a handful of
	// them have not changed since the last pass.
	//
	// The cursor is a whole file, not a byte offset, and that is deliberate.
	// A byte offset is the obvious answer and it is wrong for Claude Code: a
	// resumed session replays its entire history back into the same transcript,
	// so records already counted reappear later in the file. Deduplicating
	// across a resume boundary needs the keys from before the offset, and there
	// are 67,339 of them here — more state than re-reading costs. Measured, a
	// full cold pass over all 2.16 GB is 3.09 s; a pass where nothing changed is
	// 35 ms, and one where a single 395 MB transcript grew is 539 ms. The
	// measurement lives in internal/usage's TestIngestPassCost.
	//
	// usage_daily is the fact table, already rolled up to (day, agent session,
	// model). A row per API request is the obvious shape and would be about
	// 650,000 rows and 40 MB a year — larger than everything else in this
	// database together — to answer questions that are all per-day anyway.
	//
	// path is the foreign key rather than an id, so replacing a file's
	// contribution is one DELETE and then inserts, inside a transaction. A
	// re-read of a whole file replaces the whole of what it said, which is what
	// makes reading it twice harmless.
	//
	// project_id is absent on purpose. The cwd is stored raw and matched
	// against projects at query time: projects are created, renamed and deleted
	// long after the transcripts were written, and baking the answer in at
	// ingest would mean a project added today never sees the history that
	// belongs to it.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS usage_files (
			     path        TEXT PRIMARY KEY,
			     tool        TEXT NOT NULL,
			     size        INTEGER NOT NULL,
			     modified_at INTEGER NOT NULL,
			     scanned_at  INTEGER NOT NULL,
			     skipped     INTEGER NOT NULL DEFAULT 0,
			     problem     TEXT NOT NULL DEFAULT ''
			 )`,
			`CREATE TABLE IF NOT EXISTS usage_daily (
			     path          TEXT NOT NULL REFERENCES usage_files(path) ON DELETE CASCADE,
			     day           TEXT NOT NULL,
			     tool          TEXT NOT NULL,
			     agent_session TEXT NOT NULL,
			     cwd           TEXT NOT NULL,
			     model         TEXT NOT NULL,
			     input         INTEGER NOT NULL DEFAULT 0,
			     output        INTEGER NOT NULL DEFAULT 0,
			     cache_read    INTEGER NOT NULL DEFAULT 0,
			     cache_write   INTEGER NOT NULL DEFAULT 0,
			     requests      INTEGER NOT NULL DEFAULT 0,
			     PRIMARY KEY (path, day, agent_session, model)
			 )`,
			// The heatmap and the day table both scan by day across every
			// file, which is the one access pattern that would otherwise be a
			// full table scan.
			`CREATE INDEX IF NOT EXISTS idx_usage_daily_day ON usage_daily(day)`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},

	// v12: a share link carries the board it opens.
	//
	// One TEXT column holding JSON rather than a widgets table, and the reason
	// is what a board is: a document that is written whole, read whole, and
	// never queried across. Nothing asks "which links show a heatmap"; every
	// read is "give me this link's board". A table would buy joins nobody
	// performs and cost an ordering column, a cascade and a transaction on
	// every edit.
	//
	// Defaulted to the empty string rather than to a JSON literal, so that
	// every link written before this migration is "no board recorded" — which
	// store.DecodeBoard turns into the default board, the arrangement the
	// dashboard had before boards existed. Baking a literal in here would fix
	// today's default into rows written years ago.
	//
	// Nothing in the column is trusted on the way out. It is validated field by
	// field against the widget registry on every read, because the row is the
	// one part of a board that a future build, a hand-edited database or a
	// half-finished write can change without anyone looking.
	//
	// scope and scope_id arrive in the same step because they are the same
	// idea: a link is about the whole panel, one project, or one session.
	// scope_id holds the panel's real id and is the only real id a share row
	// keeps -- it is the input to the per-link renaming rather than anything a
	// client ever sees. Empty scope means the whole panel, which is what every
	// link written before this migration was.
	//
	// Deliberately no foreign key. A project deleted out from under a scoped
	// link must leave the link resolving to nothing -- an empty dashboard --
	// rather than either cascading the row away or, far worse, leaving a scope
	// that matches no project and falling back to showing everything. The
	// handler decides that, and a test pins it.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`ALTER TABLE share_links ADD COLUMN board TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE share_links ADD COLUMN scope TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE share_links ADD COLUMN scope_id TEXT NOT NULL DEFAULT ''`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},
	// v13: the VNC displays the panel is allowed to reach.
	//
	// A table rather than a row in `settings`, which is where the webhook list
	// lives. The difference is that a webhook is only ever read as a whole
	// list by the code that fires all of them, while a display is fetched by
	// id on every socket open -- and that id is the only thing the browser
	// ever supplies about where a TCP connection goes. A row keyed by id is
	// what makes "the address is not in the request" a property of the schema
	// instead of a habit of the handler.
	//
	// `password` is a column in the clear. There is no key on this machine
	// that is not in the same 0600 directory as the database, and the protocol
	// verifies it with single-DES over eight bytes, so encrypting the column
	// would protect nothing and imply otherwise. See store.VncTarget.
	func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS vnc_targets (
			     id         TEXT PRIMARY KEY,
			     name       TEXT NOT NULL DEFAULT '',
			     host       TEXT NOT NULL,
			     port       INTEGER NOT NULL,
			     view_only  INTEGER NOT NULL DEFAULT 0,
			     password   TEXT NOT NULL DEFAULT '',
			     created_at INTEGER NOT NULL
			 )`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	},
}

// schemaVersion is the version this build writes.
var schemaVersion = len(migrations)

// DB wraps the connection pool with the queries the panel needs.
type DB struct {
	sql *sql.DB
}

// Open connects to the database at path, applying the schema if needed.
func Open(ctx context.Context, path string) (*DB, error) {
	// Pragmas go in the DSN so they apply to every pooled connection.
	// busy_timeout in particular: without it a concurrent writer fails
	// immediately with SQLITE_BUSY instead of waiting, which shows up as
	// random save failures under two browser tabs.
	dsn := path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// SQLite takes a single write lock for the whole database. More than one
	// writer connection buys nothing and turns lock contention into errors, so
	// the pool is deliberately kept small.
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(0)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}

	db := &DB{sql: sqlDB}
	if err := db.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Close releases the pool.
func (d *DB) Close() error { return d.sql.Close() }

// SQL exposes the underlying handle for packages that need their own queries.
func (d *DB) SQL() *sql.DB { return d.sql }

// migrate brings the database up to schemaVersion, one step at a time.
func (d *DB) migrate(ctx context.Context) error {
	var current int
	if err := d.sql.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	if current > schemaVersion {
		// Refuse rather than guess. An older binary opening a newer database
		// and silently ignoring columns it does not know about is how data gets
		// quietly dropped on a rollback.
		return fmt.Errorf("store: database is version %d but this build only knows %d; "+
			"upgrade vibepanel or restore an older copy of the database",
			current, schemaVersion)
	}

	for v := current; v < schemaVersion; v++ {
		// Each step in its own transaction, and user_version moves inside it:
		// a migration that fails half way must leave the database on the
		// version it actually is, not the one it was heading for.
		tx, err := d.sql.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", v+1, err)
		}
		if err := migrations[v](tx); err != nil {
			tx.Rollback() //nolint:errcheck // the error below is the useful one
			return fmt.Errorf("store: migration %d: %w", v+1, err)
		}
		// PRAGMA does not accept a bound parameter.
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("store: set user_version after migration %d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", v+1, err)
		}
	}
	return nil
}

// Version reports the schema version currently on disk.
func (d *DB) Version(ctx context.Context) (int, error) {
	var v int
	err := d.sql.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// now is the timestamp helper used across the package. Seconds, not
// milliseconds: every consumer is a human-facing "when did this last do
// something", and integer seconds keep the JSON small and the comparisons
// obvious.
func now() int64 { return time.Now().Unix() }

// GetSetting reads a settings row, returning def when absent.
func (d *DB) GetSetting(ctx context.Context, key, def string) (string, error) {
	var v string
	err := d.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return def, nil
	}
	if err != nil {
		return "", fmt.Errorf("store: get setting %s: %w", key, err)
	}
	return v, nil
}

// SetSetting writes a settings row.
func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: set setting %s: %w", key, err)
	}
	return nil
}

// HookToken returns the shared secret that authenticates state reports,
// creating it on first use.
//
// Here rather than on the API server because the admin CLI needs the same
// value when it creates a session: without it the hook inside that session has
// nothing to authenticate with and every report is rejected, silently, because
// the hook script suppresses its own errors by design.
//
// 32 hex characters from crypto/rand. It travels in an Authorization header on
// loopback and is written into the user's agent config, so it wants to be
// unguessable but does not need to be long.
func (d *DB) HookToken(ctx context.Context) (string, error) {
	existing, err := d.GetSetting(ctx, "hook_token", "")
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}
	token := id.New() + id.New()
	if err := d.SetSetting(ctx, "hook_token", token); err != nil {
		return "", err
	}
	return token, nil
}

// CheckWritable reports whether the database will actually accept a write.
//
// Opening a database and reading from it says nothing about writing to it. A
// panel whose disk has filled reads perfectly well: `doctor` reported every
// check ok and exited 0 against a database that could not take a single row,
// which is the failure the runbook sends people to `doctor` to find.
//
// Inside a transaction that is rolled back, so a diagnostic leaves nothing
// behind. The write still has to reach the write-ahead log to be rolled back,
// which is what makes it a real test rather than a lock acquisition.
func (d *DB) CheckWritable(ctx context.Context) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin write check: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // the rollback is the point
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES ('doctor.write_check', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		fmt.Sprint(now())); err != nil {
		return fmt.Errorf("store: write check: %w", err)
	}
	return nil
}
