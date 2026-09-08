package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrPreviewNameTaken is a chosen preview address that already exists.
//
// A distinct error and not ErrNotFound's opposite number, because the person
// who sees it has to be told to pick another word rather than that something
// went wrong. The uniqueness is the database's -- token_hash is UNIQUE -- so
// two people choosing the same address at the same moment cannot both win.
var ErrPreviewNameTaken = errors.New("store: that preview address is taken")

// PreviewLink is a directory served as a page.
//
// 「在文件管理里可以将任何一个目录作为 Python 的 simple server」: a link, made
// while signed in, that renders the HTML sitting in a directory with its
// relative assets working, so a page an agent has just written is one click
// away rather than a download and a local server.
//
// Root is an absolute directory rather than a project id, for two reasons. The
// thing worth previewing is usually a build output that sits beside a project
// rather than inside one; and a link that outlived its project would be a link
// serving a directory nobody is looking after.
type PreviewLink struct {
	ID string `json:"id"`
	// Prefix is the first few characters of the token, so a row can be
	// recognised in a list without the token being readable from it.
	Prefix string `json:"prefix"`
	Name   string `json:"name"`
	Root   string `json:"root"`
	// AllowExternal lets the served page load scripts, styles, fonts and
	// images from other origins. Off unless asked for; see the migration that
	// added the column for what it costs.
	AllowExternal bool  `json:"allowExternal"`
	CreatedAt     int64 `json:"createdAt"`
	ExpiresAt     int64 `json:"expiresAt"`
	LastUsedAt    int64 `json:"lastUsedAt"`
}

// CreatePreviewLink stores a new link. The token itself is never stored; the
// caller keeps the only readable copy and hands it over once.
func (d *DB) CreatePreviewLink(
	ctx context.Context, id string, tokenHash []byte, prefix, name, root, userID string,
	expiresAt int64, allowExternal bool,
) (PreviewLink, error) {
	p := PreviewLink{
		ID: id, Prefix: prefix, Name: name, Root: root,
		AllowExternal: allowExternal, CreatedAt: now(), ExpiresAt: expiresAt,
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO preview_links
		  (id, token_hash, prefix, name, root, user_id, created_at, expires_at, allow_external)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tokenHash, prefix, name, root, userID, p.CreatedAt, expiresAt, allowExternal)
	if err != nil {
		// modernc's driver spells it in the message; there is no code to
		// compare against without importing the driver here, which this
		// package does not do anywhere else.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return PreviewLink{}, ErrPreviewNameTaken
		}
		return PreviewLink{}, fmt.Errorf("store: create preview link: %w", err)
	}
	return p, nil
}

// PreviewLinkByToken resolves a token to the directory it may serve.
//
// The expiry is in the WHERE clause, not left to the caller. An expiry a
// handler has to remember to compare is an expiry the next handler will not
// compare, and the point of offering one is that nobody has to come back.
func (d *DB) PreviewLinkByToken(ctx context.Context, tokenHash []byte) (PreviewLink, error) {
	var p PreviewLink
	err := d.sql.QueryRowContext(ctx, `
		SELECT id, prefix, name, root, created_at, expires_at, last_used_at, allow_external
		FROM preview_links
		WHERE token_hash = ? AND (expires_at = 0 OR expires_at > ?)`, tokenHash, now()).
		Scan(&p.ID, &p.Prefix, &p.Name, &p.Root, &p.CreatedAt, &p.ExpiresAt, &p.LastUsedAt,
			&p.AllowExternal)
	if errors.Is(err, sql.ErrNoRows) {
		return PreviewLink{}, ErrNotFound
	}
	if err != nil {
		return PreviewLink{}, fmt.Errorf("store: preview link lookup: %w", err)
	}
	return p, nil
}

// ListPreviewLinks returns a user's links, newest first, expired ones included.
//
// Expired rows are returned on purpose: "this link has expired" is exactly what
// the page showing them has to be able to say, and a listing that hides them
// leaves somebody wondering where their link went.
func (d *DB) ListPreviewLinks(ctx context.Context, userID string) ([]PreviewLink, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, prefix, name, root, created_at, expires_at, last_used_at, allow_external
		FROM preview_links WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list preview links: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	out := []PreviewLink{}
	for rows.Next() {
		var p PreviewLink
		if err := rows.Scan(&p.ID, &p.Prefix, &p.Name, &p.Root,
			&p.CreatedAt, &p.ExpiresAt, &p.LastUsedAt, &p.AllowExternal); err != nil {
			return nil, fmt.Errorf("store: scan preview link: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePreviewLink revokes one, and only for the account that made it.
//
// The user id is in the WHERE clause rather than checked by the handler: an
// ownership check a handler performs is one the next handler forgets.
func (d *DB) DeletePreviewLink(ctx context.Context, id, userID string) error {
	res, err := d.sql.ExecContext(ctx,
		`DELETE FROM preview_links WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete preview link: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete preview link: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchPreviewLink records that a link was used, for the listing.
func (d *DB) TouchPreviewLink(ctx context.Context, id string) error {
	if _, err := d.sql.ExecContext(ctx,
		`UPDATE preview_links SET last_used_at = ? WHERE id = ?`, now(), id); err != nil {
		return fmt.Errorf("store: touch preview link: %w", err)
	}
	return nil
}
