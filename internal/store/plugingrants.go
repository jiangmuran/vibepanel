package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Plugin grants: the credential a plugin's frame holds. docs/plugins.md §5.
//
// Minted only for a signed-in session, and bound to it: the lookup joins on
// auth_sessions, so a grant whose session was signed out, expired or deleted
// by a password change resolves to nothing on its very next request. That
// join is the whole revocation mechanism, and it cannot be forgotten by a
// handler because no handler does it. The plugin has to be enabled, or in
// dev mode, which the join also says.

// PluginGrant is a live grant.
type PluginGrant struct {
	PluginID  string
	UserID    string
	ExpiresAt int64
}

// CreatePluginGrant records a grant for a plugin, minted from the session
// whose token hash is sessionHash.
func (d *DB) CreatePluginGrant(ctx context.Context, tokenHash []byte, pluginID, userID string,
	sessionHash []byte, ttl time.Duration) error {
	n := now()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO plugin_grants (token_hash, plugin_id, user_id, session_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		tokenHash, pluginID, userID, sessionHash, n, time.Now().Add(ttl).Unix())
	if err != nil {
		if isForeignKey(err) {
			return ErrNotFound
		}
		return fmt.Errorf("store: create plugin grant: %w", err)
	}
	return nil
}

// PluginGrantByToken resolves a presented grant: unexpired, its plugin still
// there and enabled or in dev mode, and the session it was minted from still
// live.
func (d *DB) PluginGrantByToken(ctx context.Context, tokenHash []byte) (PluginGrant, error) {
	var g PluginGrant
	n := now()
	err := d.sql.QueryRowContext(ctx, `
		SELECT g.plugin_id, g.user_id, g.expires_at
		FROM plugin_grants g
		JOIN auth_sessions s ON s.token_hash = g.session_hash AND s.expires_at > ? AND s.user_id = g.user_id
		JOIN plugins p ON p.id = g.plugin_id AND (p.enabled = 1 OR p.dev = 1)
		WHERE g.token_hash = ? AND g.expires_at > ?`, n, tokenHash, n).
		Scan(&g.PluginID, &g.UserID, &g.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginGrant{}, ErrNotFound
	}
	if err != nil {
		return PluginGrant{}, fmt.Errorf("store: plugin grant: %w", err)
	}
	return g, nil
}

// SweepPluginGrants deletes grants that can no longer resolve.
func (d *DB) SweepPluginGrants(ctx context.Context) error {
	n := now()
	_, err := d.sql.ExecContext(ctx, `
		DELETE FROM plugin_grants
		WHERE expires_at <= ?
		   OR session_hash NOT IN (SELECT token_hash FROM auth_sessions WHERE expires_at > ?)`, n, n)
	if err != nil {
		return fmt.Errorf("store: sweep plugin grants: %w", err)
	}
	return nil
}
