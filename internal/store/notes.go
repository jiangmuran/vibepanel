package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Note is a project's free-text scratchpad.
type Note struct {
	ProjectID string `json:"projectId"`
	Content   string `json:"content"`
	UpdatedAt int64  `json:"updatedAt"`
	// Rev advances on every write and is the token a client sends back to say
	// what its text was based on. A timestamp cannot do this job: updated_at
	// is unix seconds, and two people editing one note collide inside a second.
	Rev int64 `json:"rev"`
}

// GetNote returns a project's note, empty if it has never been written.
//
// A missing row is not an error: CreateProject makes one, but a project that
// predates this feature, or one whose row was lost, should still open a blank
// editor rather than an error message.
func (d *DB) GetNote(ctx context.Context, projectID string) (Note, error) {
	n := Note{ProjectID: projectID}
	err := d.sql.QueryRowContext(ctx,
		`SELECT content_md, updated_at, rev FROM notes WHERE project_id = ?`, projectID).
		Scan(&n.Content, &n.UpdatedAt, &n.Rev)
	if err == sql.ErrNoRows {
		return n, nil
	}
	if err != nil {
		return Note{}, fmt.Errorf("store: get note: %w", err)
	}
	return n, nil
}

// ErrNoteStale means the note changed since the caller last read it.
var ErrNoteStale = errors.New("store: note changed elsewhere")

// SetNoteIfUnchanged writes a note only if it still has the timestamp the
// caller loaded.
//
// Without this, two viewers editing the same note is silent data loss: the
// second save simply replaces the first, and the person whose paragraph
// vanished has no way to know it ever arrived. The note is the one place in
// the panel that holds the user's own writing, so last-writer-wins is not an
// acceptable default here.
//
// baseRev of 0 means "there was nothing when I loaded", which only matches an
// absent row: every stored note has a revision of at least 1.
//
// A revision rather than the timestamp, because updated_at is unix seconds and
// the collision this guards against — one person typing while another saves —
// happens well inside a second. A check that cannot see the case it exists for
// is worse than no check, because it reads as protection.
func (d *DB) SetNoteIfUnchanged(ctx context.Context, projectID, content string, baseRev int64) (Note, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return Note{}, fmt.Errorf("store: set note: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current int64
	err = tx.QueryRowContext(ctx,
		`SELECT rev FROM notes WHERE project_id = ?`, projectID).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return Note{}, fmt.Errorf("store: set note: %w", err)
	}
	if current != baseRev {
		return Note{}, ErrNoteStale
	}

	now := now()
	rev := baseRev + 1
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO notes (project_id, content_md, updated_at, rev) VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET content_md = excluded.content_md,
		                                      updated_at = excluded.updated_at,
		                                      rev = excluded.rev`,
		projectID, content, now, rev); err != nil {
		return Note{}, fmt.Errorf("store: set note: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Note{}, fmt.Errorf("store: set note: %w", err)
	}
	return Note{ProjectID: projectID, Content: content, UpdatedAt: now, Rev: rev}, nil
}

// SetNote writes a project's note unconditionally.
//
// Still advances the revision, so a client holding the previous one is
// correctly told it is stale rather than being allowed to write over this.
func (d *DB) SetNote(ctx context.Context, projectID, content string) (Note, error) {
	now := now()
	var rev int64
	err := d.sql.QueryRowContext(ctx, `
		INSERT INTO notes (project_id, content_md, updated_at, rev) VALUES (?, ?, ?, 1)
		ON CONFLICT(project_id) DO UPDATE SET content_md = excluded.content_md,
		                                      updated_at = excluded.updated_at,
		                                      rev = notes.rev + 1
		RETURNING rev`,
		projectID, content, now).Scan(&rev)
	if err != nil {
		return Note{}, fmt.Errorf("store: set note: %w", err)
	}
	return Note{ProjectID: projectID, Content: content, UpdatedAt: now, Rev: rev}, nil
}

// Todo is one item on a project's list.
type Todo struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Text      string `json:"text"`
	Done      bool   `json:"done"`
	SortIndex int    `json:"sortIndex"`
	CreatedAt int64  `json:"createdAt"`
	DoneAt    *int64 `json:"doneAt"`
}

// ListTodos returns a project's items: outstanding first, then completed.
//
// Completed items stay visible rather than disappearing. Seeing what you just
// finished is most of the value of ticking it off, and a list that empties
// itself gives no sense of progress.
func (d *DB) ListTodos(ctx context.Context, projectID string) ([]Todo, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, project_id, text, done, sort_index, created_at, done_at
		FROM todos WHERE project_id = ?
		ORDER BY done ASC, sort_index ASC, created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list todos: %w", err)
	}
	defer rows.Close()

	var out []Todo
	for rows.Next() {
		var t Todo
		var doneAt sql.NullInt64
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Text, &t.Done, &t.SortIndex,
			&t.CreatedAt, &doneAt); err != nil {
			return nil, fmt.Errorf("store: scan todo: %w", err)
		}
		if doneAt.Valid {
			t.DoneAt = &doneAt.Int64
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTodo appends an item to a project's list.
func (d *DB) CreateTodo(ctx context.Context, id, projectID, text string) (Todo, error) {
	// Append rather than prepend: a list you add to from the bottom keeps the
	// order you thought of things in.
	var next int
	err := d.sql.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sort_index) + 1, 0) FROM todos WHERE project_id = ?`, projectID).
		Scan(&next)
	if err != nil {
		return Todo{}, fmt.Errorf("store: next todo index: %w", err)
	}

	t := Todo{ID: id, ProjectID: projectID, Text: text, SortIndex: next, CreatedAt: now()}
	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO todos (id, project_id, text, sort_index, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Text, t.SortIndex, t.CreatedAt)
	if err != nil {
		return Todo{}, fmt.Errorf("store: insert todo: %w", err)
	}
	return t, nil
}

// SetTodoDone ticks or unticks an item.
func (d *DB) SetTodoDone(ctx context.Context, id string, done bool) error {
	var doneAt any
	if done {
		doneAt = now()
	}
	return d.exec1(ctx, `UPDATE todos SET done = ?, done_at = ? WHERE id = ?`, done, doneAt, id)
}

// SetTodoText edits an item.
func (d *DB) SetTodoText(ctx context.Context, id, text string) error {
	return d.exec1(ctx, `UPDATE todos SET text = ? WHERE id = ?`, text, id)
}

// DeleteTodo removes an item.
func (d *DB) DeleteTodo(ctx context.Context, id string) error {
	return d.exec1(ctx, `DELETE FROM todos WHERE id = ?`, id)
}

// GetTodo returns one item.
func (d *DB) GetTodo(ctx context.Context, id string) (Todo, error) {
	var t Todo
	var doneAt sql.NullInt64
	err := d.sql.QueryRowContext(ctx, `
		SELECT id, project_id, text, done, sort_index, created_at, done_at
		FROM todos WHERE id = ?`, id).
		Scan(&t.ID, &t.ProjectID, &t.Text, &t.Done, &t.SortIndex, &t.CreatedAt, &doneAt)
	if err == sql.ErrNoRows {
		return Todo{}, ErrNotFound
	}
	if err != nil {
		return Todo{}, fmt.Errorf("store: get todo: %w", err)
	}
	if doneAt.Valid {
		t.DoneAt = &doneAt.Int64
	}
	return t, nil
}
