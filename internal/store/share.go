package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
// is accepted on the share GET routes -- the dashboard, the v1 snapshot and a
// share page's files, a list a test holds -- and is not a credential anywhere
// else.
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
	// Remark is a short label the owner writes for the people looking at the
	// screen -- "the one in meeting room three", "for the customer". Free text,
	// bounded at MaxRemark runes, and disclosed in both detail modes: see
	// internal/httpapi/share.go for why that is a decision rather than an
	// oversight.
	Remark string `json:"remark"`
	// Locked fixes the board: the dashboard stops offering its local
	// rearrangement and ignores one already stored.
	//
	// Not a permission and not a boundary. A viewer's rearrangement is in that
	// viewer's own browser and never reaches this row, so there is nothing
	// here for a server to enforce -- and a rearranged board discloses exactly
	// what an un-rearranged one does. This says "this board is fixed", which is
	// what somebody who hung a screen on a wall means by it.
	Locked bool `json:"locked"`
	// Viewers is how many screens had this link open a moment ago, filled in by
	// the settings page's own count rather than stored -- like ScopeName above,
	// and for a stronger reason: it is a fact about right now, and a column
	// would be a write on every poll of every link.
	Viewers int `json:"viewers"`
	// ViewportWidth and ViewportHeight are the largest live viewer's screen, in
	// CSS pixels, or 0 when nothing is looking or nothing said.
	//
	// The largest rather than the most recent, because the owner is composing
	// for a screen they cannot see: if a television and the phone somebody
	// checked it from are both on the link, the television is the one the board
	// is for. Reported by the viewer and believed only as far as a settings row
	// -- nothing the panel does depends on it.
	ViewportWidth  int `json:"viewportWidth"`
	ViewportHeight int `json:"viewportHeight"`

	// PageID is the share page this link draws, or '' for the board above.
	//
	// A choice of drawing, not of disclosure. A page reads the same redacted
	// snapshot a board does, narrowed by the same `detail` and `scope`; see
	// docs/share-pages.md.
	PageID string `json:"pageId"`
	// PinVersion holds the link on one version of its page instead of the
	// published one. With PinUntil set it is a trial, and it ends by itself.
	//
	// Resolved on read by ResolvePageVersion, the way expiry is resolved in a
	// WHERE clause: a trial that has to be ended by a timer is a trial a
	// restart leaves running, on a wall nobody who pressed the button is
	// standing in front of.
	PinVersion int   `json:"pinVersion"`
	PinUntil   int64 `json:"pinUntil"`
	// Params is the owner's values for the knobs the page declares, as stored.
	// What a page actually receives is these checked against the version it
	// is drawing, by internal/pages -- a page republished with a different
	// schema must not break a wall over a value that no longer fits.
	Params map[string]any `json:"params"`
	// Purpose is '' for a link somebody handed out and "preview" for the
	// short-lived ones the Preview pane mints. Never sent: a preview link is
	// not listed, cannot be edited, and says nothing a real link would not.
	Purpose string `json:"-"`
}

// SharePurposePreview marks a link minted by the Preview pane.
//
// It changes two things and no more: the link is left out of the settings
// list, and a page link of this purpose draws the page's draft from its
// source directory instead of a published version. What it discloses is
// decided the way every other link's is.
const SharePurposePreview = "preview"

// ResolvePageVersion is which version of its page a link draws right now.
//
// The only place that decides it. A trial whose PinUntil has passed resolves
// to the published version with no write, so nothing has to run for a wall to
// come back; a permanent pin (PinUntil 0) holds until somebody changes it.
func (s ShareLink) ResolvePageVersion(published int, now int64) int {
	if s.PinVersion > 0 && (s.PinUntil == 0 || s.PinUntil > now) {
		return s.PinVersion
	}
	return published
}

// MaxRemark is how much of a remark is kept, in runes.
//
// Runes rather than bytes, for the same reason MaxCaption is: this is cut and
// then rendered, and a byte slice through a multi-byte character renders the
// last one as U+FFFD. Longer than a caption because a remark carries a place
// and a purpose -- "会议室三的那块屏，给客户看的" -- and shorter than a name would be if
// it were a paragraph, because it is drawn under a heading on a wall.
const MaxRemark = 80

// TruncateRemark cuts a remark to MaxRemark runes.
//
// Exported and used by the one handler that stores one, so the bound is in the
// same package as the column and not repeated at a call site. A second copy of
// this number is how a validator and an editor come to disagree.
func TruncateRemark(s string) string { return truncateRunes(s, MaxRemark) }

// Deliberately no `func (s ShareLink) Expired(now int64) bool` here.
//
// It is the obvious helper and it would be the second place the expiry is
// decided. ShareLinkByToken's WHERE clause is the first, and the whole reason
// the comparison lives there is that an expiry a caller has to remember to
// check is an expiry the next caller will not check. A method makes forgetting
// possible again, and it reads as though somebody is meant to use it.

// NewShareLink is what CreateShareLink stores, as one value rather than a
// positional list that grew a column at a time until two strings beside each
// other could be swapped without the compiler noticing.
type NewShareLink struct {
	ID        string
	TokenHash []byte
	Prefix    string
	Name      string
	Detail    ShareDetail
	Board     Board
	Scope     ShareScope
	ScopeID   string
	UserID    string
	Remark    string
	Locked    bool
	ExpiresAt int64
	PageID    string
	Params    map[string]any
	Purpose   string
}

// CreateShareLink records a link. The token itself is never stored.
func (d *DB) CreateShareLink(ctx context.Context, in NewShareLink) (ShareLink, error) {
	encoded, err := EncodeBoard(in.Board)
	if err != nil {
		return ShareLink{}, err
	}
	params, err := encodeParams(in.Params)
	if err != nil {
		return ShareLink{}, err
	}
	remark := TruncateRemark(in.Remark)
	n := now()
	_, err = d.sql.ExecContext(ctx, `
		INSERT INTO share_links
			(id, token_hash, prefix, name, detail, board, scope, scope_id,
			 user_id, created_at, expires_at, remark, locked, page_id, params, purpose)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.TokenHash, in.Prefix, in.Name, string(in.Detail), encoded, string(in.Scope),
		in.ScopeID, in.UserID, n, in.ExpiresAt, remark, in.Locked, in.PageID, params, in.Purpose)
	if err != nil {
		return ShareLink{}, fmt.Errorf("store: create share link: %w", err)
	}
	return ShareLink{
		ID: in.ID, Prefix: in.Prefix, Name: in.Name, Detail: string(in.Detail), Board: in.Board,
		Scope: string(in.Scope), ScopeID: in.ScopeID, ExpiresAt: in.ExpiresAt, CreatedAt: n,
		Remark: remark, Locked: in.Locked, PageID: in.PageID, Params: decodeParams(params),
		Purpose: in.Purpose,
	}, nil
}

// shareLinkColumns is the one list every read of share_links selects, in the
// order scanShareLink reads them. token_hash is not in it, which is the whole
// reason there is exactly one list: no read path can hand a live credential
// back, and a second SELECT written by hand is where it would be added.
const shareLinkColumns = `id, prefix, name, detail, board, scope, scope_id,
	expires_at, created_at, last_used_at, remark, locked,
	page_id, pin_version, pin_until, params, purpose`

func scanShareLink(row scanner) (ShareLink, error) {
	var s ShareLink
	var board, params string
	if err := row.Scan(&s.ID, &s.Prefix, &s.Name, &s.Detail, &board, &s.Scope, &s.ScopeID,
		&s.ExpiresAt, &s.CreatedAt, &s.LastUsedAt, &s.Remark, &s.Locked,
		&s.PageID, &s.PinVersion, &s.PinUntil, &params, &s.Purpose); err != nil {
		return ShareLink{}, err
	}
	s.Board = DecodeBoard(board)
	s.Params = decodeParams(params)
	return s, nil
}

// encodeParams renders a link's parameter values for the column. Nil is an
// empty object, so the column never holds a JSON null somebody has to handle.
func encodeParams(p map[string]any) (string, error) {
	if len(p) == 0 {
		return "{}", nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("store: encode params: %w", err)
	}
	return string(raw), nil
}

// decodeParams reads the column and never fails, for DecodeBoard's reason: a
// wall must not answer 500 over a character in a text column. What a page is
// given is re-checked against its schema on the way out anyway.
func decodeParams(raw string) map[string]any {
	out := map[string]any{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// UpdateShareLink changes what an existing link is called, what it says, what
// it shows and whether that arrangement is fixed.
//
// Deliberately not the detail mode, and that is the interesting omission. The
// URL is already pasted into a television or an email by the time anybody edits
// it, so turning a counts link into a names link would widen what an address
// somebody else is holding discloses, without that person's knowledge and
// without a new link being handed out. Rearranging a board cannot disclose
// anything the link did not already carry; changing the mode can. So the mode
// is fixed at creation and a different one means a different link.
//
// A remark is on the near side of that line and it is worth saying why, since
// it is free text and the mode is about text. `name` is already sent in both
// modes and always was: what `detail` governs is whether the *panel's* words --
// session titles, project names, read out of its own database -- may leave the
// machine. A remark is not the panel's; it is a sentence the owner writes to
// whoever is standing in front of the screen, and one they can see the effect
// of. Suppressing it under `counts` would leave an owner labelling a wall with
// a label the wall never shows, which they would work around by putting it in
// `name` -- which is disclosed anyway.
func (d *DB) UpdateShareLink(ctx context.Context, id, name, remark string, board Board,
	locked bool) error {
	encoded, err := EncodeBoard(board)
	if err != nil {
		return err
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE share_links SET name = ?, board = ?, remark = ?, locked = ?
		 WHERE id = ? AND purpose = ''`,
		name, encoded, TruncateRemark(remark), locked, id)
	if err != nil {
		return fmt.Errorf("store: update share link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetShareLinkPage points a link at a page (or back at its board with ”),
// with a pin and the page's parameter values.
//
// Separate from UpdateShareLink because it is a separate decision in the
// settings page and a separate line in the audit trail, and because a link
// whose page changes should not also have to resend a board it is not drawing.
// Preview links are refused by the WHERE clause: they are minted per page and
// thrown away, and one that could be re-pointed is one that outlives its pane.
func (d *DB) SetShareLinkPage(ctx context.Context, id, pageID string, pinVersion int,
	pinUntil int64, params map[string]any) error {
	encoded, err := encodeParams(params)
	if err != nil {
		return err
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE share_links SET page_id = ?, pin_version = ?, pin_until = ?, params = ?
		 WHERE id = ? AND purpose = ''`,
		pageID, pinVersion, pinUntil, encoded, id)
	if err != nil {
		return fmt.Errorf("store: set share link page: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListShareLinks returns every link somebody handed out, newest first.
//
// Preview links are left out: they are the Preview pane's, they expire in
// minutes, and a settings list growing a row every time somebody opened a pane
// is a list nobody can find the real wall in.
func (d *DB) ListShareLinks(ctx context.Context) ([]ShareLink, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT `+shareLinkColumns+`
		FROM share_links WHERE purpose = '' ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list share links: %w", err)
	}
	defer rows.Close()
	out := []ShareLink{}
	for rows.Next() {
		s, err := scanShareLink(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan share link: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RenewShareLink moves a preview link's expiry, and only a preview link's.
//
// A real link's expiry is fixed at creation for the reason its detail is: the
// address is already in somebody's hands. A preview link is renewed while its
// pane stays open so the frame does not go dark in front of the person using
// it; the bound on how far is the caller's.
func (d *DB) RenewShareLink(ctx context.Context, id string, expiresAt int64) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE share_links SET expires_at = ? WHERE id = ? AND purpose = ?`,
		expiresAt, id, SharePurposePreview)
	if err != nil {
		return fmt.Errorf("store: renew share link: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SweepPreviewLinks deletes preview links that have expired.
//
// Called when a new one is minted, which is the only moment more of them can
// appear. An expired link already resolves to nothing; this is housekeeping so
// the table does not grow by one row per pane opened, forever.
func (d *DB) SweepPreviewLinks(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx,
		`DELETE FROM share_links WHERE purpose = ? AND expires_at > 0 AND expires_at <= ?`,
		SharePurposePreview, now())
	if err != nil {
		return fmt.Errorf("store: sweep preview links: %w", err)
	}
	return nil
}

// ShareLinkByToken resolves a presented token, or ErrNotFound if it is unknown
// or past its expiry.
//
// The expiry is in the WHERE clause rather than checked by the caller. An
// expiry a handler has to remember to compare is an expiry a second handler
// will not compare, and the whole point of offering one is that the person who
// set it does not have to come back and revoke it.
func (d *DB) ShareLinkByToken(ctx context.Context, tokenHash []byte) (ShareLink, error) {
	s, err := scanShareLink(d.sql.QueryRowContext(ctx, `
		SELECT `+shareLinkColumns+`
		FROM share_links
		WHERE token_hash = ? AND (expires_at = 0 OR expires_at > ?)`, tokenHash, now()))
	if errors.Is(err, sql.ErrNoRows) {
		return ShareLink{}, ErrNotFound
	}
	if err != nil {
		return ShareLink{}, fmt.Errorf("store: share link lookup: %w", err)
	}
	return s, nil
}

// ShareLinkByID reads one link the owner named, for the preview the editor
// draws.
//
// Deliberately a different lookup from ShareLinkByToken and not a shared
// helper with a flag. That one resolves a capability and has the expiry in its
// WHERE clause; this one is reached only from behind a signed-in session and
// must return an expired link, because "this link has expired" is exactly what
// the settings page has to be able to say about a row it is showing. One
// function answering both questions is one WHERE clause somebody has to
// remember which caller it is for.
//
// token_hash is not among the columns read, here as everywhere: there is no
// path from a signed-in session to a live share token either.
func (d *DB) ShareLinkByID(ctx context.Context, id string) (ShareLink, error) {
	s, err := scanShareLink(d.sql.QueryRowContext(ctx, `
		SELECT `+shareLinkColumns+`
		FROM share_links WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ShareLink{}, ErrNotFound
	}
	if err != nil {
		return ShareLink{}, fmt.Errorf("store: share link by id: %w", err)
	}
	return s, nil
}

// ShareLinksForPage lists the handed-out links drawing one page.
//
// For the two questions the settings page asks before it acts: "may this page
// be deleted" (not while a wall draws it) and "which screen should a trial go
// to".
func (d *DB) ShareLinksForPage(ctx context.Context, pageID string) ([]ShareLink, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT `+shareLinkColumns+`
		FROM share_links WHERE page_id = ? AND purpose = '' ORDER BY created_at DESC`, pageID)
	if err != nil {
		return nil, fmt.Errorf("store: share links for page: %w", err)
	}
	defer rows.Close()
	out := []ShareLink{}
	for rows.Next() {
		s, err := scanShareLink(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan share link: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
