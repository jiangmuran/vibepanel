package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jiangmuran/vibepanel/internal/session"
)

// TitleSource records whether a session's title tracks the application or was
// chosen by the user.
type TitleSource string

const (
	// TitleAuto follows #{pane_title}, which the application sets via OSC 0/2.
	TitleAuto TitleSource = "auto"
	// TitleManual means the user renamed the tab; automatic updates must stop.
	TitleManual TitleSource = "manual"
)

// Session is one coding task backed by one tmux session.
type Session struct {
	ID             string         `json:"id"`
	ProjectID      string         `json:"projectId"`
	TmuxName       string         `json:"tmuxName"`
	Title          string         `json:"title"`
	TitleSource    TitleSource    `json:"titleSource"`
	State          session.State  `json:"state"`
	StateSource    session.Source `json:"stateSource"`
	StateChangedAt int64          `json:"stateChangedAt"`
	Pinned         bool           `json:"pinned"`
	SortIndex      *int           `json:"sortIndex"`
	CWD            string         `json:"cwd"`
	Command        string         `json:"command"`
	Cols           int            `json:"cols"`
	Rows           int            `json:"rows"`
	LastOutputAt   int64          `json:"lastOutputAt"`
	CreatedAt      int64          `json:"createdAt"`
	ArchivedAt     *int64         `json:"archivedAt"`
}

// CreateSession inserts a session row. The tmux session itself is created by
// the caller; this only records it.
func (d *DB) CreateSession(ctx context.Context, s Session) (Session, error) {
	if s.Cols <= 0 {
		s.Cols = 120
	}
	if s.Rows <= 0 {
		s.Rows = 32
	}
	if s.State == "" {
		s.State = session.StateWorking
	}
	if s.StateSource == "" {
		s.StateSource = session.SourceHeuristic
	}
	if s.TitleSource == "" {
		s.TitleSource = TitleAuto
	}
	s.CreatedAt = now()
	s.StateChangedAt = s.CreatedAt

	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO sessions
			(id, project_id, tmux_name, title, title_source, state, state_source,
			 state_changed_at, cwd, command, cols, rows, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ProjectID, s.TmuxName, s.Title, s.TitleSource, s.State, s.StateSource,
		s.StateChangedAt, s.CWD, s.Command, s.Cols, s.Rows, s.CreatedAt)
	if err != nil {
		return Session{}, fmt.Errorf("store: insert session: %w", err)
	}
	return s, nil
}

const sessionColumns = `id, project_id, tmux_name, title, title_source, state, state_source,
	state_changed_at, pinned, sort_index, cwd, command, cols, rows,
	last_output_at, created_at, archived_at`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var sortIdx sql.NullInt64
	var archived sql.NullInt64
	err := sc.Scan(&s.ID, &s.ProjectID, &s.TmuxName, &s.Title, &s.TitleSource,
		&s.State, &s.StateSource, &s.StateChangedAt, &s.Pinned, &sortIdx,
		&s.CWD, &s.Command, &s.Cols, &s.Rows, &s.LastOutputAt, &s.CreatedAt, &archived)
	if err != nil {
		return Session{}, err
	}
	if sortIdx.Valid {
		v := int(sortIdx.Int64)
		s.SortIndex = &v
	}
	if archived.Valid {
		s.ArchivedAt = &archived.Int64
	}
	return s, nil
}

// ListSessions returns every session in display order.
//
// Order: pinned first, then by state urgency (waiting before working before
// done), then manual position, then most recent output. The state ranking is
// expressed as a CASE rather than a join so it cannot disagree with
// session.State.SortWeight — the test asserts the two match.
func (d *DB) ListSessions(ctx context.Context) ([]Session, error) {
	return d.listSessions(ctx, "", nil)
}

// ListProjectSessions returns the sessions of one project in display order.
func (d *DB) ListProjectSessions(ctx context.Context, projectID string) ([]Session, error) {
	return d.listSessions(ctx, "WHERE project_id = ?", []any{projectID})
}

func (d *DB) listSessions(ctx context.Context, where string, args []any) ([]Session, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM sessions
		%s
		ORDER BY pinned DESC,
		         CASE state WHEN 'waiting' THEN 0 WHEN 'working' THEN 1 WHEN 'done' THEN 2 ELSE 3 END,
		         CASE WHEN sort_index IS NULL THEN 1 ELSE 0 END,
		         sort_index ASC,
		         last_output_at DESC,
		         created_at DESC`, sessionColumns, where)

	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSession returns one session by id.
func (d *DB) GetSession(ctx context.Context, id string) (Session, error) {
	row := d.sql.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM sessions WHERE id = ?`, sessionColumns), id)
	s, err := scanSession(row)
	if err == sql.ErrNoRows {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: get session: %w", err)
	}
	return s, nil
}

// GetSessionByTmuxName looks a session up by its tmux identity. Used when
// reconciling what the panel believes against what tmux actually has.
func (d *DB) GetSessionByTmuxName(ctx context.Context, tmuxName string) (Session, error) {
	row := d.sql.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM sessions WHERE tmux_name = ?`, sessionColumns), tmuxName)
	s, err := scanSession(row)
	if err == sql.ErrNoRows {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: get session by tmux name: %w", err)
	}
	return s, nil
}

// SetSessionState records a new state along with what decided it.
//
// A change is only written when the state actually differs, so that
// state_changed_at means "when it last changed" rather than "when it was last
// polled" — the sidebar shows that timestamp to the user.
func (d *DB) SetSessionState(ctx context.Context, id string, st session.State, src session.Source) error {
	if !st.Valid() {
		return fmt.Errorf("store: invalid state %q", st)
	}
	_, err := d.sql.ExecContext(ctx, `
		UPDATE sessions
		SET state = ?,
		    state_source = ?,
		    state_changed_at = CASE WHEN state != ? THEN ? ELSE state_changed_at END
		WHERE id = ?`, st, src, st, now(), id)
	if err != nil {
		return fmt.Errorf("store: set session state: %w", err)
	}
	return nil
}

// SetSessionTitle renames a session and records where the name came from.
//
// An automatic update is ignored once the user has renamed the tab by hand:
// having a title you chose silently replaced by whatever the shell last set is
// exactly the behaviour this project exists to fix.
func (d *DB) SetSessionTitle(ctx context.Context, id, title string, src TitleSource) error {
	if src == TitleAuto {
		_, err := d.sql.ExecContext(ctx,
			`UPDATE sessions SET title = ? WHERE id = ? AND title_source = 'auto'`, title, id)
		if err != nil {
			return fmt.Errorf("store: auto-title session: %w", err)
		}
		return nil
	}
	return d.exec1(ctx, `UPDATE sessions SET title = ?, title_source = ? WHERE id = ?`, title, src, id)
}

// SetSessionPinned pins a session to the top of its project.
func (d *DB) SetSessionPinned(ctx context.Context, id string, pinned bool) error {
	return d.exec1(ctx, `UPDATE sessions SET pinned = ? WHERE id = ?`, pinned, id)
}

// SetSessionSortIndex sets a manual position, or clears it with nil.
func (d *DB) SetSessionSortIndex(ctx context.Context, id string, idx *int) error {
	if idx == nil {
		return d.exec1(ctx, `UPDATE sessions SET sort_index = NULL WHERE id = ?`, id)
	}
	return d.exec1(ctx, `UPDATE sessions SET sort_index = ? WHERE id = ?`, *idx, id)
}

// SetSessionSize records the grid size agreed with the controlling viewer.
func (d *DB) SetSessionSize(ctx context.Context, id string, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("store: invalid size %dx%d", cols, rows)
	}
	return d.exec1(ctx, `UPDATE sessions SET cols = ?, rows = ? WHERE id = ?`, cols, rows, id)
}

// UpdateSessionRuntime records what tmux currently reports for a session.
func (d *DB) UpdateSessionRuntime(ctx context.Context, id, cwd, command string, lastOutputAt int64) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE sessions SET cwd = ?, command = ?, last_output_at = ? WHERE id = ?`,
		cwd, command, lastOutputAt, id)
	if err != nil {
		return fmt.Errorf("store: update session runtime: %w", err)
	}
	return nil
}

// DeleteSession removes a session row.
func (d *DB) DeleteSession(ctx context.Context, id string) error {
	return d.exec1(ctx, `DELETE FROM sessions WHERE id = ?`, id)
}
