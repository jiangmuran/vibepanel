package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Scrollback is one session's terminal history, kept so that a reboot does not
// take it.
//
// tmux holds 20,000 lines per session and holds them in memory: the history
// dies with the server, and the server dies with the machine. This is the only
// copy that survives a power cut.
type Scrollback struct {
	SessionID  string
	CapturedAt int64
	// Lines is how many lines the capture asked tmux for, which is the bound
	// that was applied — not how many came back.
	Lines int
	// Truncated means the byte cap bit and the front of the capture was cut
	// off. Surfaced rather than hidden: a restored screen missing its first
	// half should say so.
	Truncated bool
	Content   []byte
}

// PutScrollback replaces a session's archived scrollback.
//
// One row per session, overwritten in place. Keeping a history of histories
// would multiply the largest thing in the database by however many ticks have
// passed, to answer a question nobody asks: what somebody wants back is what
// was on screen when the machine went down, not what was on screen an hour
// before that.
func (d *DB) PutScrollback(ctx context.Context, sb Scrollback) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO session_scrollback (session_id, captured_at, lines, truncated, content)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			captured_at = excluded.captured_at,
			lines       = excluded.lines,
			truncated   = excluded.truncated,
			content     = excluded.content`,
		sb.SessionID, sb.CapturedAt, sb.Lines, sb.Truncated, sb.Content)
	if err != nil {
		return fmt.Errorf("store: put scrollback %s: %w", sb.SessionID, err)
	}
	return nil
}

// GetScrollback returns the archived scrollback for a session.
//
// ErrNotFound when there is none, which is an ordinary state: a session created
// since the last archive tick, or one that has never produced any output.
func (d *DB) GetScrollback(ctx context.Context, sessionID string) (Scrollback, error) {
	sb := Scrollback{SessionID: sessionID}
	err := d.sql.QueryRowContext(ctx, `
		SELECT captured_at, lines, truncated, content
		FROM session_scrollback WHERE session_id = ?`, sessionID).
		Scan(&sb.CapturedAt, &sb.Lines, &sb.Truncated, &sb.Content)
	if err == sql.ErrNoRows {
		return Scrollback{}, ErrNotFound
	}
	if err != nil {
		return Scrollback{}, fmt.Errorf("store: get scrollback %s: %w", sessionID, err)
	}
	return sb, nil
}

// DeleteScrollback drops a session's archive.
//
// Called once the archive has been handed back to a restored pane. The blob is
// the largest thing this database holds and its only purpose is being restored
// once; keeping it afterwards would mean a second restore replaying scrollback
// that is now two processes old.
func (d *DB) DeleteScrollback(ctx context.Context, sessionID string) error {
	_, err := d.sql.ExecContext(ctx,
		`DELETE FROM session_scrollback WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("store: delete scrollback %s: %w", sessionID, err)
	}
	return nil
}

// ScrollbackBytes reports what the archive is costing on disk, for `doctor` and
// the settings page.
func (d *DB) ScrollbackBytes(ctx context.Context) (rows int, bytes int64, err error) {
	err = d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(LENGTH(content)), 0) FROM session_scrollback`).
		Scan(&rows, &bytes)
	if err != nil {
		return 0, 0, fmt.Errorf("store: scrollback size: %w", err)
	}
	return rows, bytes, nil
}
