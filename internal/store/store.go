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
