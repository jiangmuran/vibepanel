package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// User is an account that can sign in.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"createdAt"`
}

// CountUsers reports how many accounts exist. Zero means the panel has never
// been set up, which is the only state in which the setup endpoint works.
func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// CreateUser inserts an account.
func (d *DB) CreateUser(ctx context.Context, id, username, passwordHash string) (User, error) {
	u := User{ID: id, Username: username, PasswordHash: passwordHash, CreatedAt: now()}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.CreatedAt)
	if err != nil {
		return User{}, fmt.Errorf("store: create user: %w", err)
	}
	return u, nil
}

// UserByName looks an account up for sign-in.
func (d *DB) UserByName(ctx context.Context, username string) (User, error) {
	var u User
	err := d.sql.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: user by name: %w", err)
	}
	return u, nil
}

// UserByID looks an account up by id.
func (d *DB) UserByID(ctx context.Context, id string) (User, error) {
	var u User
	err := d.sql.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: user by id: %w", err)
	}
	return u, nil
}

// SetPasswordHash replaces an account's password.
func (d *DB) SetPasswordHash(ctx context.Context, userID, hash string) error {
	return d.exec1(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
}

// AuthSession is one signed-in browser.
type AuthSession struct {
	UserID     string `json:"userId"`
	CreatedAt  int64  `json:"createdAt"`
	ExpiresAt  int64  `json:"expiresAt"`
	LastSeenAt int64  `json:"lastSeenAt"`
	UserAgent  string `json:"userAgent"`
	IP         string `json:"ip"`
}

// CreateAuthSession records a sign-in.
//
// The token is stored hashed, never in the clear: a database that leaks should
// not hand over live sessions along with it.
func (d *DB) CreateAuthSession(ctx context.Context, tokenHash []byte, userID string, ttl time.Duration, userAgent, ip string) error {
	n := now()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO auth_sessions (token_hash, user_id, created_at, expires_at, last_seen_at, user_agent, ip)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, userID, n, time.Now().Add(ttl).Unix(), n, userAgent, ip)
	if err != nil {
		return fmt.Errorf("store: create auth session: %w", err)
	}
	return nil
}

// AuthSessionByToken returns a live session, or ErrNotFound if it is unknown
// or expired.
func (d *DB) AuthSessionByToken(ctx context.Context, tokenHash []byte) (AuthSession, error) {
	var s AuthSession
	err := d.sql.QueryRowContext(ctx, `
		SELECT user_id, created_at, expires_at, last_seen_at, user_agent, ip
		FROM auth_sessions WHERE token_hash = ? AND expires_at > ?`,
		tokenHash, now()).
		Scan(&s.UserID, &s.CreatedAt, &s.ExpiresAt, &s.LastSeenAt, &s.UserAgent, &s.IP)
	if err == sql.ErrNoRows {
		return AuthSession{}, ErrNotFound
	}
	if err != nil {
		return AuthSession{}, fmt.Errorf("store: auth session: %w", err)
	}
	return s, nil
}

// TouchAuthSession records that a session was used.
func (d *DB) TouchAuthSession(ctx context.Context, tokenHash []byte) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE auth_sessions SET last_seen_at = ? WHERE token_hash = ?`, now(), tokenHash)
	if err != nil {
		return fmt.Errorf("store: touch auth session: %w", err)
	}
	return nil
}

// DeleteAuthSession signs one browser out.
func (d *DB) DeleteAuthSession(ctx context.Context, tokenHash []byte) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("store: delete auth session: %w", err)
	}
	return nil
}

// DeleteUserAuthSessions signs every browser out for one account. Used when
// the password changes: the point of changing it is that whoever had the old
// one stops having access.
func (d *DB) DeleteUserAuthSessions(ctx context.Context, userID string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("store: delete user auth sessions: %w", err)
	}
	return nil
}

// PurgeExpiredAuthSessions removes sessions past their expiry.
func (d *DB) PurgeExpiredAuthSessions(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at <= ?`, now())
	if err != nil {
		return 0, fmt.Errorf("store: purge auth sessions: %w", err)
	}
	return res.RowsAffected()
}

// AuditEntry is one recorded security-relevant event.
type AuditEntry struct {
	At       int64  `json:"at"`
	Event    string `json:"event"`
	Username string `json:"username"`
	IP       string `json:"ip"`
	Detail   string `json:"detail"`
}

// Audit records an event.
func (d *DB) Audit(ctx context.Context, e AuditEntry) error {
	if e.At == 0 {
		e.At = now()
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO audit_log (at, event, username, ip, detail) VALUES (?, ?, ?, ?, ?)`,
		e.At, e.Event, e.Username, e.IP, e.Detail)
	if err != nil {
		return fmt.Errorf("store: audit: %w", err)
	}
	return nil
}

// RecentAudit returns the most recent entries, newest first.
func (d *DB) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT at, event, username, ip, detail FROM audit_log ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.At, &e.Event, &e.Username, &e.IP, &e.Detail); err != nil {
			return nil, fmt.Errorf("store: scan audit: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditKeep is how many audit rows are worth holding on to.
//
// The settings page shows fifty. This is three orders of magnitude more than
// anybody reads, and about two megabytes — chosen so that trimming never
// throws away something a person was going to look for, while the table still
// has a ceiling.
const AuditKeep = 50000

// TrimAuditLog drops all but the newest keep rows.
//
// Nothing removed an audit row before this existed. Every refused sign-in on a
// panel exposed to the internet added one, permanently, to the same disk the
// projects live on.
func (d *DB) TrimAuditLog(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	// By rowid rather than by a subquery over the whole table: id is the
	// primary key, so this reads one row to find the boundary and then deletes
	// a contiguous range below it.
	res, err := d.sql.ExecContext(ctx, `
		DELETE FROM audit_log WHERE id <= (
			SELECT id FROM audit_log ORDER BY id DESC LIMIT 1 OFFSET ?
		)`, keep)
	if err != nil {
		return 0, fmt.Errorf("store: trim audit log: %w", err)
	}
	return res.RowsAffected()
}
