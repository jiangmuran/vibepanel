package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ShareDetail is how much a read-only link is allowed to say.
//
// Two values, not a set of flags, because the question a person is answering
// when they make a link is a single one: is this going on a wall behind me.
// A flag per field reads as configurability and is really an invitation to get
// one of them wrong.
type ShareDetail string

const (
	// ShareCounts discloses shapes and numbers and no text at all: how many
	// projects, how many sessions, which state each is in, what each is
	// costing. Nothing that names a customer, a repository or a machine.
	ShareCounts ShareDetail = "counts"
	// ShareNames adds session titles and project names. Never paths, never
	// commands -- see internal/httpapi/share.go for why those two are not on
	// the near side of any line.
	ShareNames ShareDetail = "names"
)

// ValidShareDetail reports whether d came from this enum.
//
// Anything that reaches the database here decides what a link discloses for as
// long as it exists, so an unrecognised value is refused rather than
// defaulted: a default could only ever fall towards showing more or towards
// showing less, and both are the wrong answer to "I do not understand what you
// asked for".
func ValidShareDetail(d ShareDetail) bool {
	return d == ShareCounts || d == ShareNames
}

// ShareLink is a capability: a URL that opens the read-only dashboard.
//
// Same storage shape as APIToken, deliberately -- the hash, never the token,
// so a leaked backup does not hand over live links, and a prefix in the clear
// so the settings page can name the row you are about to revoke.
//
// The difference from an APIToken is what it can reach, and that difference is
// enforced by the router rather than by anything on this struct: a share token
// is accepted on exactly one GET route and is not a credential anywhere else.
// There is no field here that a handler could read to widen it.
type ShareLink struct {
	ID     string `json:"id"`
	Prefix string `json:"prefix"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	// ExpiresAt is unix seconds, or 0 for a link that does not expire.
	//
	// Zero rather than a nullable column: every caller has to handle "no
	// expiry" anyway, and a NULL meaning the same thing as 0 is one more way
	// for a query to be written wrong.
	ExpiresAt  int64 `json:"expiresAt"`
	CreatedAt  int64 `json:"createdAt"`
	LastUsedAt int64 `json:"lastUsedAt"`
}

// Deliberately no `func (s ShareLink) Expired(now int64) bool` here.
//
// It is the obvious helper and it would be the second place the expiry is
// decided. ShareLinkByToken's WHERE clause is the first, and the whole reason
// the comparison lives there is that an expiry a caller has to remember to
// check is an expiry the next caller will not check. A method makes forgetting
// possible again, and it reads as though somebody is meant to use it.

// CreateShareLink records a link. The token itself is never stored.
func (d *DB) CreateShareLink(ctx context.Context, id string, tokenHash []byte, prefix, name string,
	detail ShareDetail, userID string, expiresAt int64) (ShareLink, error) {
	n := now()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO share_links (id, token_hash, prefix, name, detail, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tokenHash, prefix, name, string(detail), userID, n, expiresAt)
	if err != nil {
		return ShareLink{}, fmt.Errorf("store: create share link: %w", err)
	}
	return ShareLink{
		ID: id, Prefix: prefix, Name: name, Detail: string(detail),
		ExpiresAt: expiresAt, CreatedAt: n,
	}, nil
}

// ListShareLinks returns every link, newest first. token_hash is not among the
// columns read, so there is no path from this call to a live credential.
func (d *DB) ListShareLinks(ctx context.Context) ([]ShareLink, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, prefix, name, detail, expires_at, created_at, last_used_at
		FROM share_links ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list share links: %w", err)
	}
	defer rows.Close()
	out := []ShareLink{}
	for rows.Next() {
		var s ShareLink
		if err := rows.Scan(&s.ID, &s.Prefix, &s.Name, &s.Detail,
			&s.ExpiresAt, &s.CreatedAt, &s.LastUsedAt); err != nil {
			return nil, fmt.Errorf("store: scan share link: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ShareLinkByToken resolves a presented token, or ErrNotFound if it is unknown
// or past its expiry.
//
// The expiry is in the WHERE clause rather than checked by the caller. An
// expiry a handler has to remember to compare is an expiry a second handler
// will not compare, and the whole point of offering one is that the person who
// set it does not have to come back and revoke it.
func (d *DB) ShareLinkByToken(ctx context.Context, tokenHash []byte) (ShareLink, error) {
	var s ShareLink
	err := d.sql.QueryRowContext(ctx, `
		SELECT id, prefix, name, detail, expires_at, created_at, last_used_at
		FROM share_links
		WHERE token_hash = ? AND (expires_at = 0 OR expires_at > ?)`, tokenHash, now()).
		Scan(&s.ID, &s.Prefix, &s.Name, &s.Detail, &s.ExpiresAt, &s.CreatedAt, &s.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ShareLink{}, ErrNotFound
	}
	if err != nil {
		return ShareLink{}, fmt.Errorf("store: share link lookup: %w", err)
	}
	return s, nil
}

// TouchShareLink records that a link was used.
//
// Deliberately not folded into ShareLinkByToken the way UserByAPIToken folds
// its stamp in. A share link is polled by a wall display every couple of
// seconds and an API token is used at human speed: stamping on every lookup is
// forty thousand writes a day, through one write lock, onto the disk the
// projects live on. The caller gates this behind a cooldown, and "last seen
// within the last minute" is all the settings page was ever going to say.
func (d *DB) TouchShareLink(ctx context.Context, id string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE share_links SET last_used_at = ? WHERE id = ?`, now(), id)
	if err != nil {
		return fmt.Errorf("store: touch share link: %w", err)
	}
	return nil
}

// DeleteShareLink revokes one link.
//
// Revocation takes effect on the next poll, and there is nothing else to
// invalidate: a share link has no session, no cookie and no socket, only a
// row. That is most of why the dashboard polls rather than holding a
// connection -- a socket authorised once and open for a week would need the
// revalidation machinery ws.Handler has, for a page that reads six numbers.
func (d *DB) DeleteShareLink(ctx context.Context, id string) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM share_links WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete share link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
