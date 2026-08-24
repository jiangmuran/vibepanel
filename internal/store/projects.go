package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Project is a named directory that groups sessions.
type Project struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	SortIndex    *int   `json:"sortIndex"` // nil means "order me by activity"
	Pinned       bool   `json:"pinned"`
	LastActiveAt int64  `json:"lastActiveAt"`
	CreatedAt    int64  `json:"createdAt"`
}

// CreateProject inserts a project and its empty note row.
func (d *DB) CreateProject(ctx context.Context, id, name, path string) (Project, error) {
	p := Project{ID: id, Name: name, Path: path, CreatedAt: now(), LastActiveAt: now()}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeded

	_, err = tx.ExecContext(ctx,
		`INSERT INTO projects (id, name, path, last_active_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Path, p.LastActiveAt, p.CreatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("store: insert project: %w", err)
	}
	// Create the note row up front so the notes panel never has to distinguish
	// "no note yet" from "project missing".
	_, err = tx.ExecContext(ctx,
		`INSERT INTO notes (project_id, content_md, updated_at) VALUES (?, '', ?)`, p.ID, now())
	if err != nil {
		return Project{}, fmt.Errorf("store: insert note row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("store: commit: %w", err)
	}
	return p, nil
}

// ListProjects returns projects in display order: pinned first, then manually
// positioned rows, then the rest by most recent activity.
//
// The ordering lives in SQL rather than in Go so that every caller — REST,
// WebSocket snapshot, tests — sees the same sequence. Two implementations of
// "the sidebar order" would drift.
func (d *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, name, path, sort_index, pinned, last_active_at, created_at
		FROM projects
		ORDER BY pinned DESC,
		         CASE WHEN sort_index IS NULL THEN 1 ELSE 0 END,
		         sort_index ASC,
		         last_active_at DESC,
		         created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		var p Project
		var sortIdx sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &sortIdx, &p.Pinned, &p.LastActiveAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan project: %w", err)
		}
		if sortIdx.Valid {
			v := int(sortIdx.Int64)
			p.SortIndex = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject returns one project.
func (d *DB) GetProject(ctx context.Context, id string) (Project, error) {
	var p Project
	var sortIdx sql.NullInt64
	err := d.sql.QueryRowContext(ctx, `
		SELECT id, name, path, sort_index, pinned, last_active_at, created_at
		FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Path, &sortIdx, &p.Pinned, &p.LastActiveAt, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("store: get project: %w", err)
	}
	if sortIdx.Valid {
		v := int(sortIdx.Int64)
		p.SortIndex = &v
	}
	return p, nil
}

// RenameProject changes a project's display name.
func (d *DB) RenameProject(ctx context.Context, id, name string) error {
	return d.exec1(ctx, `UPDATE projects SET name = ? WHERE id = ?`, name, id)
}

// SetProjectPinned pins or unpins a project.
func (d *DB) SetProjectPinned(ctx context.Context, id string, pinned bool) error {
	return d.exec1(ctx, `UPDATE projects SET pinned = ? WHERE id = ?`, pinned, id)
}

// SetProjectSortIndex sets a manual position, or clears it with nil to return
// the project to automatic activity ordering.
func (d *DB) SetProjectSortIndex(ctx context.Context, id string, idx *int) error {
	if idx == nil {
		return d.exec1(ctx, `UPDATE projects SET sort_index = NULL WHERE id = ?`, id)
	}
	return d.exec1(ctx, `UPDATE projects SET sort_index = ? WHERE id = ?`, *idx, id)
}

// ReorderProjects writes an explicit order, replacing automatic ordering.
//
// Takes the whole list rather than one project's new position: a drag changes
// where every project below it sits, and sending those one at a time leaves the
// sidebar briefly showing an order that never existed if any request fails.
//
// Ids not present in the list are left on automatic ordering, so a client
// working from a stale list cannot silently drop a project someone else just
// added to the bottom.
func (d *DB) ReorderProjects(ctx context.Context, ids []string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin reorder: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeded

	for i, id := range ids {
		res, err := tx.ExecContext(ctx, `UPDATE projects SET sort_index = ? WHERE id = ?`, i, id)
		if err != nil {
			return fmt.Errorf("store: reorder project %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: reorder rows: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("store: reorder: %w: project %s", ErrNotFound, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit reorder: %w", err)
	}
	return nil
}

// ClearProjectOrder returns every project to automatic, most-active-first
// ordering.
func (d *DB) ClearProjectOrder(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE projects SET sort_index = NULL`)
	if err != nil {
		return fmt.Errorf("store: clear project order: %w", err)
	}
	return nil
}

// ProjectOrderIsManual reports whether any project carries an explicit
// position. The UI uses it to offer "sort by activity" only when that would
// actually change something.
func (d *DB) ProjectOrderIsManual(ctx context.Context) (bool, error) {
	var n int
	err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects WHERE sort_index IS NOT NULL`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: project order mode: %w", err)
	}
	return n > 0, nil
}

// TouchProject records activity, which drives automatic ordering.
func (d *DB) TouchProject(ctx context.Context, id string) error {
	_, err := d.sql.ExecContext(ctx, `UPDATE projects SET last_active_at = ? WHERE id = ?`, now(), id)
	if err != nil {
		return fmt.Errorf("store: touch project: %w", err)
	}
	return nil
}

// DeleteProject removes a project. Sessions, notes and todos cascade.
//
// It does not touch tmux: killing the underlying sessions is the caller's job,
// because a database transaction cannot be rolled back once a process is dead.
func (d *DB) DeleteProject(ctx context.Context, id string) error {
	return d.exec1(ctx, `DELETE FROM projects WHERE id = ?`, id)
}

// exec1 runs a statement that must affect exactly one row.
func (d *DB) exec1(ctx context.Context, query string, args ...any) error {
	res, err := d.sql.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: exec: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
