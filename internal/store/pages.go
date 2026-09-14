package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// A share page is HTML the owner wrote, drawn on a share link in place of a
// board. See docs/share-pages.md for the design and internal/httpapi/sharepage.go
// for how one is served.
//
// This file stores them and decides nothing about what they may contain: the
// manifest, the file list and every limit are internal/pages' to check, before
// anything reaches here.

// SharePage is one page, with the version its links draw by default.
type SharePage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// SourceDir is where the draft lives on this machine: the directory an
	// agent is editing. Read by the Preview pane and by publish, never by a
	// link somebody was handed.
	SourceDir string `json:"sourceDir"`
	// PublishedVersion is 0 for a page that has never been published, which
	// no handed-out link may point at.
	PublishedVersion int   `json:"publishedVersion"`
	CreatedAt        int64 `json:"createdAt"`
	UpdatedAt        int64 `json:"updatedAt"`
}

// SharePageVersion is one immutable snapshot of a page.
type SharePageVersion struct {
	Version int `json:"version"`
	// Manifest is vibepanel.json as it was published, already validated.
	Manifest json.RawMessage `json:"manifest"`
	Note     string          `json:"note"`
	Bytes    int64           `json:"bytes"`
	Files    int             `json:"files"`
	// CommitSHA is the source directory's HEAD when it was published, and
	// Dirty says the working tree had changes beyond it. Both are provenance:
	// the files below are what the version is, whatever git says.
	CommitSHA string `json:"commitSha"`
	Dirty     bool   `json:"dirty"`
	// Candidate marks a version frozen for a trial on one screen and not yet
	// kept. It is hidden from the history until it is.
	Candidate bool  `json:"candidate"`
	CreatedAt int64 `json:"createdAt"`
}

// SharePageFile is one file of a version. Data is filled only by
// SharePageFileData, so a listing of a version does not read every byte of it.
type SharePageFile struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	SHA256      []byte `json:"-"`
	Size        int64  `json:"size"`
}

// NewSharePageFile is one file handed to AddSharePageVersion.
type NewSharePageFile struct {
	Path        string
	ContentType string
	Data        []byte
}

// NewSharePageVersion is what AddSharePageVersion stores.
type NewSharePageVersion struct {
	Manifest  json.RawMessage
	Note      string
	CommitSHA string
	Dirty     bool
	// Candidate is a trial version; Publish makes it the published one. They
	// are not both true.
	Candidate bool
	Publish   bool
	Files     []NewSharePageFile
}

// CreateSharePage records a page with no versions yet.
func (d *DB) CreateSharePage(ctx context.Context, id, userID, name, sourceDir string) (SharePage, error) {
	n := now()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO share_pages (id, user_id, name, source_dir, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, userID, name, sourceDir, n, n)
	if err != nil {
		return SharePage{}, fmt.Errorf("store: create share page: %w", err)
	}
	return SharePage{ID: id, Name: name, SourceDir: sourceDir, CreatedAt: n, UpdatedAt: n}, nil
}

const sharePageColumns = `id, name, source_dir, published_version, created_at, updated_at`

func scanSharePage(row scanner) (SharePage, error) {
	var p SharePage
	err := row.Scan(&p.ID, &p.Name, &p.SourceDir, &p.PublishedVersion, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// SharePageOwner is the account a page belongs to, for a preview link the CLI
// mints on its behalf: a link row needs an owner, and the page's is the only
// one with any claim to it.
func (d *DB) SharePageOwner(ctx context.Context, id string) (string, error) {
	var owner string
	err := d.sql.QueryRowContext(ctx, `SELECT user_id FROM share_pages WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: share page owner: %w", err)
	}
	return owner, nil
}

// FirstUserID is the account `vibepanel page init` records a page against.
//
// The first account, because the CLI runs as the machine user and has nobody
// signed in to ask. A panel has one account in practice; one with several
// gets pages owned by the one that set it up, which is the account `vibepanel
// account create` made.
func (d *DB) FirstUserID(ctx context.Context) (string, error) {
	var uid string
	err := d.sql.QueryRowContext(ctx, `SELECT id FROM users ORDER BY created_at, id LIMIT 1`).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: first user: %w", err)
	}
	return uid, nil
}

// ListSharePages returns every page, most recently changed first.
func (d *DB) ListSharePages(ctx context.Context) ([]SharePage, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+sharePageColumns+` FROM share_pages ORDER BY updated_at DESC, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list share pages: %w", err)
	}
	defer rows.Close()
	out := []SharePage{}
	for rows.Next() {
		p, err := scanSharePage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan share page: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SharePageByID reads one page.
func (d *DB) SharePageByID(ctx context.Context, id string) (SharePage, error) {
	p, err := scanSharePage(d.sql.QueryRowContext(ctx,
		`SELECT `+sharePageColumns+` FROM share_pages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SharePage{}, ErrNotFound
	}
	if err != nil {
		return SharePage{}, fmt.Errorf("store: share page by id: %w", err)
	}
	return p, nil
}

// SharePageBySourceDir finds the page whose draft lives in dir, for the CLI
// running inside that directory and for the Preview pane opened on a project.
func (d *DB) SharePageBySourceDir(ctx context.Context, dir string) (SharePage, error) {
	p, err := scanSharePage(d.sql.QueryRowContext(ctx,
		`SELECT `+sharePageColumns+` FROM share_pages WHERE source_dir = ? AND source_dir != ''
		 ORDER BY updated_at DESC LIMIT 1`, dir))
	if errors.Is(err, sql.ErrNoRows) {
		return SharePage{}, ErrNotFound
	}
	if err != nil {
		return SharePage{}, fmt.Errorf("store: share page by source: %w", err)
	}
	return p, nil
}

// UpdateSharePage renames a page or moves its draft.
func (d *DB) UpdateSharePage(ctx context.Context, id, name, sourceDir string) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE share_pages SET name = ?, source_dir = ?, updated_at = ? WHERE id = ?`,
		name, sourceDir, now(), id)
	if err != nil {
		return fmt.Errorf("store: update share page: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSharePage removes a page, its versions, its preview links, and every
// blob nothing else references.
//
// The caller has already refused when a handed-out link draws it. The preview
// links go here, in the same transaction, because a preview link pointing at a
// page that no longer exists is a token that resolves to a row and draws
// nothing.
func (d *DB) DeleteSharePage(ctx context.Context, id string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete share page: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM share_links WHERE page_id = ? AND purpose = ?`, id, SharePurposePreview); err != nil {
		return fmt.Errorf("store: delete preview links: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM share_pages WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete share page: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := gcPageBlobs(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// AddSharePageVersion stores the next version of a page and returns its
// number.
//
// One transaction for the version row, its files and the blobs, so a publish
// that fails half way leaves no version with half its files -- a wall pointed
// at one would draw a page missing its stylesheet, with nobody there to say so.
//
// A new candidate replaces the page's older candidates that no link is pinned
// to: a trial nobody kept is not history, and "Try on a screen" pressed twenty
// times while iterating must not leave twenty versions behind.
func (d *DB) AddSharePageVersion(ctx context.Context, pageID string, in NewSharePageVersion) (int, error) {
	if in.Candidate && in.Publish {
		return 0, fmt.Errorf("store: a version is a candidate or published, not both")
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: add page version: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM share_pages WHERE id = ?`, pageID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("store: add page version: %w", err)
	}
	if exists == 0 {
		return 0, ErrNotFound
	}

	// The number first, the sweep after. A version number is what an open page
	// compares to decide whether to reload, so a number must never be handed
	// out twice: sweeping the newest candidate before counting would give its
	// number to different files, and a wall showing it would never notice.
	var version int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM share_page_versions WHERE page_id = ?`,
		pageID).Scan(&version); err != nil {
		return 0, fmt.Errorf("store: next page version: %w", err)
	}
	if in.Candidate {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM share_page_versions
			WHERE page_id = ? AND candidate = 1 AND version NOT IN (
			    SELECT pin_version FROM share_links WHERE page_id = ? AND pin_version > 0)`,
			pageID, pageID); err != nil {
			return 0, fmt.Errorf("store: drop old candidates: %w", err)
		}
	}

	var total int64
	for _, f := range in.Files {
		total += int64(len(f.Data))
	}
	n := now()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO share_page_versions
		    (page_id, version, manifest, note, bytes, files, commit_sha, dirty, candidate, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pageID, version, string(in.Manifest), in.Note, total, len(in.Files),
		in.CommitSHA, in.Dirty, in.Candidate, n); err != nil {
		return 0, fmt.Errorf("store: insert page version: %w", err)
	}
	for _, f := range in.Files {
		sum := sha256.Sum256(f.Data)
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO share_page_blobs (sha256, data) VALUES (?, ?)`,
			sum[:], f.Data); err != nil {
			return 0, fmt.Errorf("store: insert page blob: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO share_page_files (page_id, version, path, content_type, sha256)
			VALUES (?, ?, ?, ?, ?)`, pageID, version, f.Path, f.ContentType, sum[:]); err != nil {
			return 0, fmt.Errorf("store: insert page file %s: %w", f.Path, err)
		}
	}
	if in.Publish {
		if _, err := tx.ExecContext(ctx,
			`UPDATE share_pages SET published_version = ?, updated_at = ? WHERE id = ?`,
			version, n, pageID); err != nil {
			return 0, fmt.Errorf("store: publish page version: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE share_pages SET updated_at = ? WHERE id = ?`, n, pageID); err != nil {
			return 0, fmt.Errorf("store: touch share page: %w", err)
		}
	}
	if err := gcPageBlobs(ctx, tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: add page version: %w", err)
	}
	return version, nil
}

// PublishSharePageVersion makes an existing version the published one.
//
// Used for both "keep this trial" and "roll back to v6". A candidate that is
// published stops being a candidate, which is what makes it history.
func (d *DB) PublishSharePageVersion(ctx context.Context, pageID string, version int) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: publish page version: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit

	res, err := tx.ExecContext(ctx,
		`UPDATE share_page_versions SET candidate = 0 WHERE page_id = ? AND version = ?`,
		pageID, version)
	if err != nil {
		return fmt.Errorf("store: publish page version: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE share_pages SET published_version = ?, updated_at = ? WHERE id = ?`,
		version, now(), pageID); err != nil {
		return fmt.Errorf("store: publish page version: %w", err)
	}
	return tx.Commit()
}

const pageVersionColumns = `version, manifest, note, bytes, files, commit_sha, dirty, candidate, created_at`

func scanPageVersion(row scanner) (SharePageVersion, error) {
	var v SharePageVersion
	var manifest string
	err := row.Scan(&v.Version, &manifest, &v.Note, &v.Bytes, &v.Files, &v.CommitSHA,
		&v.Dirty, &v.Candidate, &v.CreatedAt)
	v.Manifest = json.RawMessage(manifest)
	return v, err
}

// ListSharePageVersions returns a page's versions, newest first, candidates
// included and marked.
func (d *DB) ListSharePageVersions(ctx context.Context, pageID string) ([]SharePageVersion, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+pageVersionColumns+` FROM share_page_versions WHERE page_id = ? ORDER BY version DESC`,
		pageID)
	if err != nil {
		return nil, fmt.Errorf("store: list page versions: %w", err)
	}
	defer rows.Close()
	out := []SharePageVersion{}
	for rows.Next() {
		v, err := scanPageVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan page version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SharePageVersionByNumber reads one version's row.
func (d *DB) SharePageVersionByNumber(ctx context.Context, pageID string, version int) (SharePageVersion, error) {
	v, err := scanPageVersion(d.sql.QueryRowContext(ctx,
		`SELECT `+pageVersionColumns+` FROM share_page_versions WHERE page_id = ? AND version = ?`,
		pageID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return SharePageVersion{}, ErrNotFound
	}
	if err != nil {
		return SharePageVersion{}, fmt.Errorf("store: page version: %w", err)
	}
	return v, nil
}

// SharePageFiles lists one version's files without their bytes.
func (d *DB) SharePageFiles(ctx context.Context, pageID string, version int) ([]SharePageFile, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT f.path, f.content_type, f.sha256, LENGTH(b.data)
		FROM share_page_files f JOIN share_page_blobs b ON b.sha256 = f.sha256
		WHERE f.page_id = ? AND f.version = ? ORDER BY f.path`, pageID, version)
	if err != nil {
		return nil, fmt.Errorf("store: list page files: %w", err)
	}
	defer rows.Close()
	out := []SharePageFile{}
	for rows.Next() {
		var f SharePageFile
		if err := rows.Scan(&f.Path, &f.ContentType, &f.SHA256, &f.Size); err != nil {
			return nil, fmt.Errorf("store: scan page file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SharePageFileData reads one file of one version.
func (d *DB) SharePageFileData(ctx context.Context, pageID string, version int, path string) (
	contentType string, data []byte, err error) {
	err = d.sql.QueryRowContext(ctx, `
		SELECT f.content_type, b.data
		FROM share_page_files f JOIN share_page_blobs b ON b.sha256 = f.sha256
		WHERE f.page_id = ? AND f.version = ? AND f.path = ?`, pageID, version, path).
		Scan(&contentType, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("store: page file: %w", err)
	}
	return contentType, data, nil
}

// gcPageBlobs deletes the blobs no version refers to any more.
func gcPageBlobs(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM share_page_blobs
		WHERE sha256 NOT IN (SELECT DISTINCT sha256 FROM share_page_files)`); err != nil {
		return fmt.Errorf("store: sweep page blobs: %w", err)
	}
	return nil
}
