package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// A share page's own data: docs/page-backend.md §2. The rules -- which keys,
// which types, how large -- are internal/pages'; this is where the values
// live, keyed by page, namespace ("live" or "draft") and key.

// The data namespaces.
const (
	PageDataLive  = "live"
	PageDataDraft = "draft"
)

// ValidPageDataNamespace reports whether ns is one of them.
func ValidPageDataNamespace(ns string) bool { return ns == PageDataLive || ns == PageDataDraft }

// PageDatum is one stored value, as JSON.
type PageDatum struct {
	Value     []byte
	UpdatedAt int64
	UpdatedBy string
}

// PageData reads every stored key of a page's namespace.
func (d *DB) PageData(ctx context.Context, pageID, ns string) (map[string]PageDatum, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT key, value, updated_at, updated_by FROM share_page_data WHERE page_id = ? AND ns = ?`, pageID, ns)
	if err != nil {
		return nil, fmt.Errorf("store: page data: %w", err)
	}
	defer rows.Close()
	out := map[string]PageDatum{}
	for rows.Next() {
		var key, value string
		var dt PageDatum
		if err := rows.Scan(&key, &value, &dt.UpdatedAt, &dt.UpdatedBy); err != nil {
			return nil, fmt.Errorf("store: scan page data: %w", err)
		}
		dt.Value = []byte(value)
		out[key] = dt
	}
	return out, rows.Err()
}

// PageDataWrite is one key's new value in a batch; a nil Value deletes it,
// which is how a key goes back to its default.
type PageDataWrite struct {
	Key   string
	Value []byte
}

// ErrPageDataTooLarge is a write that would take a namespace past its cap.
var ErrPageDataTooLarge = errors.New("store: page data would be larger than its limit")

// ErrPageDataTooManyKeys is a write that would store more keys than allowed.
var ErrPageDataTooManyKeys = errors.New("store: page data would have too many keys")

// WritePageData applies writes to one namespace in one transaction, or none of
// them: server code that fails half way through a call must not leave half of
// what it wrote. The caps are checked inside the same transaction against
// what the namespace holds afterwards.
func (d *DB) WritePageData(ctx context.Context, pageID, ns string, writes []PageDataWrite,
	by string, maxBytes, maxKeys int) error {
	if !ValidPageDataNamespace(ns) {
		return fmt.Errorf("store: unknown page data namespace %q", ns)
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin page data: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	n := now()
	for _, w := range writes {
		if w.Value == nil {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM share_page_data WHERE page_id = ? AND ns = ? AND key = ?`, pageID, ns, w.Key); err != nil {
				return fmt.Errorf("store: delete page data: %w", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO share_page_data (page_id, ns, key, value, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (page_id, ns, key) DO UPDATE SET
			    value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			pageID, ns, w.Key, string(w.Value), n, by); err != nil {
			if isForeignKey(err) {
				return ErrNotFound
			}
			return fmt.Errorf("store: write page data: %w", err)
		}
	}
	var total, keys int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(LENGTH(CAST(value AS BLOB)) + LENGTH(CAST(key AS BLOB))), 0), COUNT(*) FROM share_page_data WHERE page_id = ? AND ns = ?`,
		pageID, ns).Scan(&total, &keys); err != nil {
		return fmt.Errorf("store: size page data: %w", err)
	}
	if maxBytes > 0 && total > maxBytes {
		return ErrPageDataTooLarge
	}
	if maxKeys > 0 && keys > maxKeys {
		return ErrPageDataTooManyKeys
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit page data: %w", err)
	}
	return nil
}

// ClearPageData deletes every key of a namespace.
func (d *DB) ClearPageData(ctx context.Context, pageID, ns string) error {
	if _, err := d.sql.ExecContext(ctx,
		`DELETE FROM share_page_data WHERE page_id = ? AND ns = ?`, pageID, ns); err != nil {
		return fmt.Errorf("store: clear page data: %w", err)
	}
	return nil
}

func isForeignKey(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY")
}
