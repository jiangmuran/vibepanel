package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Admin grants: the credential an admin page holds. docs/page-backend.md §3.
//
// Minted only for a signed-in session, and bound to it: the lookup joins on
// auth_sessions, so a grant whose session was signed out, expired or deleted
// by a password change resolves to nothing on its very next request. That
// join is the whole revocation mechanism, and it cannot be forgotten by a
// handler because no handler does it.

// PageAdminGrant is a live grant.
type PageAdminGrant struct {
	PageID    string
	UserID    string
	Draft     bool
	ExpiresAt int64
}

// CreatePageAdminGrant records a grant for a page, minted from the session
// whose token hash is sessionHash.
func (d *DB) CreatePageAdminGrant(ctx context.Context, tokenHash []byte, pageID, userID string,
	sessionHash []byte, draft bool, ttl time.Duration) error {
	n := now()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO page_admin_grants (token_hash, page_id, user_id, session_hash, draft, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, pageID, userID, sessionHash, draft, n, time.Now().Add(ttl).Unix())
	if err != nil {
		if isForeignKey(err) {
			return ErrNotFound
		}
		return fmt.Errorf("store: create admin grant: %w", err)
	}
	return nil
}

// PageAdminGrantByToken resolves a presented grant: unexpired, its page still
// there, and the session it was minted from still live.
func (d *DB) PageAdminGrantByToken(ctx context.Context, tokenHash []byte) (PageAdminGrant, error) {
	var g PageAdminGrant
	n := now()
	err := d.sql.QueryRowContext(ctx, `
		SELECT g.page_id, g.user_id, g.draft, g.expires_at
		FROM page_admin_grants g
		JOIN auth_sessions s ON s.token_hash = g.session_hash AND s.expires_at > ? AND s.user_id = g.user_id
		JOIN share_pages p ON p.id = g.page_id
		WHERE g.token_hash = ? AND g.expires_at > ?`, n, tokenHash, n).
		Scan(&g.PageID, &g.UserID, &g.Draft, &g.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PageAdminGrant{}, ErrNotFound
	}
	if err != nil {
		return PageAdminGrant{}, fmt.Errorf("store: admin grant: %w", err)
	}
	return g, nil
}

// SweepPageAdminGrants deletes grants that can no longer resolve.
func (d *DB) SweepPageAdminGrants(ctx context.Context) error {
	n := now()
	_, err := d.sql.ExecContext(ctx, `
		DELETE FROM page_admin_grants
		WHERE expires_at <= ?
		   OR session_hash NOT IN (SELECT token_hash FROM auth_sessions WHERE expires_at > ?)`, n, n)
	if err != nil {
		return fmt.Errorf("store: sweep admin grants: %w", err)
	}
	return nil
}
