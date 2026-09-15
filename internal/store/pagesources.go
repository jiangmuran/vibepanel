package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// A page's approved source hosts and its sealed secrets. docs/page-backend.md
// §4. Neither is part of a version or an export: they are the owner's
// decisions about this panel, not the page's.

// PageHosts lists the hosts approved for a page, sorted.
func (d *DB) PageHosts(ctx context.Context, pageID string) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT host FROM share_page_hosts WHERE page_id = ? ORDER BY host`, pageID)
	if err != nil {
		return nil, fmt.Errorf("store: page hosts: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("store: scan page host: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetPageHosts replaces a page's approved hosts.
func (d *DB) SetPageHosts(ctx context.Context, pageID string, hosts []string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin page hosts: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM share_page_hosts WHERE page_id = ?`, pageID); err != nil {
		return fmt.Errorf("store: clear page hosts: %w", err)
	}
	n := now()
	for _, h := range hosts {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO share_page_hosts (page_id, host, approved_at) VALUES (?, ?, ?)`, pageID, h, n); err != nil {
			if isForeignKey(err) {
				return ErrNotFound
			}
			return fmt.Errorf("store: add page host: %w", err)
		}
	}
	return tx.Commit()
}

// PageSecret is a stored secret's name and when it was set; never its value.
type PageSecret struct {
	Name  string `json:"name"`
	SetAt int64  `json:"setAt"`
}

// PageSecrets lists a page's secrets by name.
func (d *DB) PageSecrets(ctx context.Context, pageID string) ([]PageSecret, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT name, set_at FROM share_page_secrets WHERE page_id = ? ORDER BY name`, pageID)
	if err != nil {
		return nil, fmt.Errorf("store: page secrets: %w", err)
	}
	defer rows.Close()
	out := []PageSecret{}
	for rows.Next() {
		var s PageSecret
		if err := rows.Scan(&s.Name, &s.SetAt); err != nil {
			return nil, fmt.Errorf("store: scan page secret: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PageSecretSealed reads one secret's sealed value.
func (d *DB) PageSecretSealed(ctx context.Context, pageID, name string) ([]byte, error) {
	var enc []byte
	err := d.sql.QueryRowContext(ctx,
		`SELECT value_enc FROM share_page_secrets WHERE page_id = ? AND name = ?`, pageID, name).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: page secret: %w", err)
	}
	return enc, nil
}

// SetPageSecret stores a sealed secret.
func (d *DB) SetPageSecret(ctx context.Context, pageID, name string, sealed []byte) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO share_page_secrets (page_id, name, value_enc, set_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (page_id, name) DO UPDATE SET value_enc = excluded.value_enc, set_at = excluded.set_at`,
		pageID, name, sealed, now())
	if err != nil {
		if isForeignKey(err) {
			return ErrNotFound
		}
		return fmt.Errorf("store: set page secret: %w", err)
	}
	return nil
}

// DeletePageSecret removes a secret.
func (d *DB) DeletePageSecret(ctx context.Context, pageID, name string) error {
	res, err := d.sql.ExecContext(ctx,
		`DELETE FROM share_page_secrets WHERE page_id = ? AND name = ?`, pageID, name)
	if err != nil {
		return fmt.Errorf("store: delete page secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
