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

// ShareScope is how much of the panel a link is about.
//
// A second axis from ShareDetail, and they answer different questions.
// ShareDetail is "may this link use words"; ShareScope is "which rows is it
// about at all". A link scoped to one project is the one you send to somebody
// you are working with on that project, and it must not become a view of
// everything the moment somebody asks it differently — so the scope lives on
// the row and is applied by the handler, never read from a request.
type ShareScope string

const (
	// ShareWhole is every project, which is what a link was before scopes.
	ShareWhole ShareScope = ""
	// ShareProject is one project and the sessions in it.
	ShareProject ShareScope = "project"
	// ShareSession is one session.
	ShareSession ShareScope = "session"
)

// ValidShareScope reports whether s came from this enum.
func ValidShareScope(s ShareScope) bool {
	return s == ShareWhole || s == ShareProject || s == ShareSession
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
	// Board is what this link opens: which widgets, in which order, at which
	// widths. Decoded here rather than handed on as a string, so nothing above
	// this layer ever holds the raw column — see DecodeBoard for why the read
	// path drops what it does not recognise instead of failing.
	Board Board `json:"board"`
	// Scope is "", "project" or "session"; ScopeID is the panel's real id of
	// the one it is about.
	//
	// The real id, deliberately, and it is the one place a share row holds one.
	// It is never sent: the dashboard renames every id it discloses under the
	// link's own secret, and this is the input to that renaming rather than
	// something a client ever sees. A pseudonym here would have to be resolved
	// back to a row on every poll, which is a reverse lookup the whole scheme
	// exists to avoid needing.
	Scope   string `json:"scope"`
	ScopeID string `json:"-"`
	// ScopeName is what the scoped project or session is called, filled in by
	// the settings page's own query rather than stored. Empty on the dashboard
	// side, which never reads it.
	ScopeName string `json:"scopeName"`
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
	detail ShareDetail, board Board, scope ShareScope, scopeID, userID string,
	expiresAt int64) (ShareLink, error) {
	encoded, err := EncodeBoard(board)
	if err != nil {
		return ShareLink{}, err
	}
	n := now()
	_, err = d.sql.ExecContext(ctx, `
		INSERT INTO share_links
			(id, token_hash, prefix, name, detail, board, scope, scope_id,
			 user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tokenHash, prefix, name, string(detail), encoded, string(scope), scopeID,
		userID, n, expiresAt)
	if err != nil {
		return ShareLink{}, fmt.Errorf("store: create share link: %w", err)
	}
	return ShareLink{
		ID: id, Prefix: prefix, Name: name, Detail: string(detail), Board: board,
		Scope: string(scope), ScopeID: scopeID, ExpiresAt: expiresAt, CreatedAt: n,
	}, nil
}

// UpdateShareLink changes what an existing link is called and what it shows.
//
// Deliberately not the detail mode, and that is the interesting omission. The
// URL is already pasted into a television or an email by the time anybody edits
// it, so turning a counts link into a names link would widen what an address
// somebody else is holding discloses, without that person's knowledge and
// without a new link being handed out. Rearranging a board cannot disclose
// anything the link did not already carry; changing the mode can. So the mode
// is fixed at creation and a different one means a different link.
func (d *DB) UpdateShareLink(ctx context.Context, id, name string, board Board) error {
	encoded, err := EncodeBoard(board)
	if err != nil {
		return err
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE share_links SET name = ?, board = ? WHERE id = ?`, name, encoded, id)
	if err != nil {
		return fmt.Errorf("store: update share link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListShareLinks returns every link, newest first. token_hash is not among the
// columns read, so there is no path from this call to a live credential.
func (d *DB) ListShareLinks(ctx context.Context) ([]ShareLink, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, prefix, name, detail, board, scope, scope_id,
		       expires_at, created_at, last_used_at
		FROM share_links ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list share links: %w", err)
	}
	defer rows.Close()
	out := []ShareLink{}
	for rows.Next() {
		var s ShareLink
		var board string
		if err := rows.Scan(&s.ID, &s.Prefix, &s.Name, &s.Detail, &board, &s.Scope, &s.ScopeID,
			&s.ExpiresAt, &s.CreatedAt, &s.LastUsedAt); err != nil {
			return nil, fmt.Errorf("store: scan share link: %w", err)
		}
		s.Board = DecodeBoard(board)
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
	var board string
	err := d.sql.QueryRowContext(ctx, `
		SELECT id, prefix, name, detail, board, scope, scope_id,
		       expires_at, created_at, last_used_at
		FROM share_links
		WHERE token_hash = ? AND (expires_at = 0 OR expires_at > ?)`, tokenHash, now()).
		Scan(&s.ID, &s.Prefix, &s.Name, &s.Detail, &board, &s.Scope, &s.ScopeID,
			&s.ExpiresAt, &s.CreatedAt, &s.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ShareLink{}, ErrNotFound
	}
	if err != nil {
		return ShareLink{}, fmt.Errorf("store: share link lookup: %w", err)
	}
	s.Board = DecodeBoard(board)
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
