package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// A plugin access token is the owner's credential for one plugin's door on
// the panel's port (/api/plugin-http/{id}/, docs/plugins.md §5). Minted on
// the card with a name -- "the glasses", "the spare phone" -- revoked one at
// a time, its last use shown. Its own table: an API token is the owner, a
// plugin token is a process, and this reaches one plugin's mount and nothing
// else. currentUser consults none of the three plugin tables.

type PluginAccessToken struct {
	ID         string `json:"id"`
	PluginID   string `json:"pluginId"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	RevokedAt  int64  `json:"revokedAt"`
}

func (d *DB) CreatePluginAccessToken(ctx context.Context, id, pluginID, name string, tokenHash []byte) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO plugin_access_tokens (id, plugin_id, name, token_hash, created_at, last_used_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, 0, 0)`, id, pluginID, name, tokenHash, now())
	if err != nil {
		if isForeignKey(err) {
			return ErrNotFound
		}
		return fmt.Errorf("store: create plugin access token: %w", err)
	}
	return nil
}

func (d *DB) ListPluginAccessTokens(ctx context.Context, pluginID string) ([]PluginAccessToken, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, plugin_id, name, created_at, last_used_at, revoked_at
		FROM plugin_access_tokens WHERE plugin_id = ? ORDER BY created_at, id`, pluginID)
	if err != nil {
		return nil, fmt.Errorf("store: list plugin access tokens: %w", err)
	}
	defer rows.Close()
	out := []PluginAccessToken{}
	for rows.Next() {
		var t PluginAccessToken
		if err := rows.Scan(&t.ID, &t.PluginID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
			return nil, fmt.Errorf("store: list plugin access tokens: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PluginAccessTokenByHash resolves a presented token: live, for a plugin
// that is enabled or in dev mode.
func (d *DB) PluginAccessTokenByHash(ctx context.Context, tokenHash []byte) (PluginAccessToken, error) {
	var t PluginAccessToken
	err := d.sql.QueryRowContext(ctx, `
		SELECT t.id, t.plugin_id, t.name, t.created_at, t.last_used_at, t.revoked_at
		FROM plugin_access_tokens t
		JOIN plugins p ON p.id = t.plugin_id AND (p.enabled = 1 OR p.dev = 1)
		WHERE t.token_hash = ? AND t.revoked_at = 0`, tokenHash).
		Scan(&t.ID, &t.PluginID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginAccessToken{}, ErrNotFound
	}
	if err != nil {
		return PluginAccessToken{}, fmt.Errorf("store: plugin access token: %w", err)
	}
	return t, nil
}

func (d *DB) TouchPluginAccessToken(ctx context.Context, id string) error {
	if _, err := d.sql.ExecContext(ctx, `UPDATE plugin_access_tokens SET last_used_at = ? WHERE id = ?`, now(), id); err != nil {
		return fmt.Errorf("store: touch plugin access token: %w", err)
	}
	return nil
}

// RevokePluginAccessToken ends a token; the row stays so the card can say
// when it was revoked and the audit line has a name to use.
func (d *DB) RevokePluginAccessToken(ctx context.Context, pluginID, id string) error {
	res, err := d.sql.ExecContext(ctx, `UPDATE plugin_access_tokens SET revoked_at = ? WHERE plugin_id = ? AND id = ? AND revoked_at = 0`,
		now(), pluginID, id)
	if err != nil {
		return fmt.Errorf("store: revoke plugin access token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
