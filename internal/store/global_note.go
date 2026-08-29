package store

import (
	"context"
	"database/sql"
	"fmt"
)

// The global note's row. There is one, and the table's CHECK says so.
const globalNoteID = 1

// GlobalNoteID is what a Note carries as its ProjectID when it is the global
// one.
//
// Not the empty string. The frontend keys panel state by project id and an
// empty key is indistinguishable from "no project selected", which is the
// state the global note is most often opened *from*.
const GlobalNoteID = "@global"

// GetGlobalNote returns the note that belongs to no project.
//
// A missing row is a blank note, not an error: nothing creates it up front,
// and the first read on a database that has never had one should open an empty
// editor rather than fail.
func (d *DB) GetGlobalNote(ctx context.Context) (Note, error) {
	n := Note{ProjectID: GlobalNoteID}
	err := d.sql.QueryRowContext(ctx,
		`SELECT content_md, updated_at, rev FROM global_note WHERE id = ?`, globalNoteID).
		Scan(&n.Content, &n.UpdatedAt, &n.Rev)
	if err == sql.ErrNoRows {
		return n, nil
	}
	if err != nil {
		return Note{}, fmt.Errorf("store: get global note: %w", err)
	}
	return n, nil
}

// SetGlobalNoteIfUnchanged is SetNoteIfUnchanged for the note with no project.
//
// The same revision check, and for a stronger reason: a project note is open
// in whoever is looking at that project, and this one is reachable from every
// screen, so two tabs holding it is the ordinary case rather than the unlucky
// one.
func (d *DB) SetGlobalNoteIfUnchanged(ctx context.Context, content string, baseRev int64) (Note, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return Note{}, fmt.Errorf("store: set global note: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current int64
	err = tx.QueryRowContext(ctx,
		`SELECT rev FROM global_note WHERE id = ?`, globalNoteID).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return Note{}, fmt.Errorf("store: set global note: %w", err)
	}
	if current != baseRev {
		return Note{}, ErrNoteStale
	}

	now := now()
	rev := baseRev + 1
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO global_note (id, content_md, updated_at, rev) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET content_md = excluded.content_md,
		                              updated_at = excluded.updated_at,
		                              rev        = excluded.rev`,
		globalNoteID, content, now, rev); err != nil {
		return Note{}, fmt.Errorf("store: set global note: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Note{}, fmt.Errorf("store: set global note: %w", err)
	}
	return Note{ProjectID: GlobalNoteID, Content: content, UpdatedAt: now, Rev: rev}, nil
}

// SetGlobalNote writes it unconditionally, still advancing the revision.
func (d *DB) SetGlobalNote(ctx context.Context, content string) (Note, error) {
	now := now()
	var rev int64
	err := d.sql.QueryRowContext(ctx, `
		INSERT INTO global_note (id, content_md, updated_at, rev) VALUES (?, ?, ?, 1)
		ON CONFLICT(id) DO UPDATE SET content_md = excluded.content_md,
		                              updated_at = excluded.updated_at,
		                              rev        = global_note.rev + 1
		RETURNING rev`, globalNoteID, content, now).Scan(&rev)
	if err != nil {
		return Note{}, fmt.Errorf("store: set global note: %w", err)
	}
	return Note{ProjectID: GlobalNoteID, Content: content, UpdatedAt: now, Rev: rev}, nil
}
