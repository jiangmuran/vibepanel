package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Credential is one registered passkey.
//
// Data holds the marshalled webauthn.Credential — the authoritative copy. The
// scalar fields beside it exist for indexing and for showing the user a list
// they can recognise, not as a second source of truth.
type Credential struct {
	ID           string `json:"id"`
	UserID       string `json:"-"`
	CredentialID []byte `json:"-"`
	UserHandle   []byte `json:"-"`
	Data         []byte `json:"-"`
	Name         string `json:"name"`
	SignCount    uint32 `json:"-"`
	CreatedAt    int64  `json:"createdAt"`
	LastUsedAt   *int64 `json:"lastUsedAt"`
}

// CreateCredential registers a passkey.
func (d *DB) CreateCredential(ctx context.Context, c Credential) error {
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO credentials
			(id, user_id, credential_id, public_key, sign_count, transports, name, created_at, data, user_handle)
		VALUES (?, ?, ?, x'', ?, '', ?, ?, ?, ?)`,
		c.ID, c.UserID, c.CredentialID, c.SignCount, c.Name, c.CreatedAt, c.Data, c.UserHandle)
	if err != nil {
		return fmt.Errorf("store: create credential: %w", err)
	}
	return nil
}

// ListCredentials returns a user's passkeys, oldest first.
func (d *DB) ListCredentials(ctx context.Context, userID string) ([]Credential, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, user_id, credential_id, user_handle, data, name, sign_count, created_at, last_used_at
		FROM credentials WHERE user_id = ? ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list credentials: %w", err)
	}
	defer rows.Close()
	return scanCredentials(rows)
}

// CredentialsByUserHandle returns the passkeys registered against a WebAuthn
// user handle. Used for passwordless sign-in, where the browser supplies the
// handle and no username is typed.
func (d *DB) CredentialsByUserHandle(ctx context.Context, handle []byte) ([]Credential, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, user_id, credential_id, user_handle, data, name, sign_count, created_at, last_used_at
		FROM credentials WHERE user_handle = ? ORDER BY created_at ASC`, handle)
	if err != nil {
		return nil, fmt.Errorf("store: credentials by handle: %w", err)
	}
	defer rows.Close()
	return scanCredentials(rows)
}

func scanCredentials(rows *sql.Rows) ([]Credential, error) {
	var out []Credential
	for rows.Next() {
		var c Credential
		var lastUsed sql.NullInt64
		if err := rows.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.UserHandle, &c.Data,
			&c.Name, &c.SignCount, &c.CreatedAt, &lastUsed); err != nil {
			return nil, fmt.Errorf("store: scan credential: %w", err)
		}
		if lastUsed.Valid {
			c.LastUsedAt = &lastUsed.Int64
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCredentialUse records a sign-in with a passkey.
//
// The sign count is the authenticator's own monotonic counter. It going
// backwards is the documented signal that a credential has been cloned, which
// the caller checks before calling this.
func (d *DB) UpdateCredentialUse(ctx context.Context, credentialID []byte, signCount uint32, data []byte) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE credentials SET sign_count = ?, data = ?, last_used_at = ? WHERE credential_id = ?`,
		signCount, data, now(), credentialID)
	if err != nil {
		return fmt.Errorf("store: update credential use: %w", err)
	}
	return nil
}

// RenameCredential changes a passkey's label.
func (d *DB) RenameCredential(ctx context.Context, id, userID, name string) error {
	return d.exec1(ctx,
		`UPDATE credentials SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
}

// DeleteCredential removes a passkey.
//
// Scoped to the owner: an id is not a capability, and one user must not be
// able to remove another's key by guessing at it.
func (d *DB) DeleteCredential(ctx context.Context, id, userID string) error {
	return d.exec1(ctx, `DELETE FROM credentials WHERE id = ? AND user_id = ?`, id, userID)
}

// CountCredentials reports how many passkeys a user has.
func (d *DB) CountCredentials(ctx context.Context, userID string) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM credentials WHERE user_id = ?`, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count credentials: %w", err)
	}
	return n, nil
}
