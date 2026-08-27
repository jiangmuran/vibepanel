package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

	// LaunchCommand is the argv this session was created with, and it is the
	// only thing here that can be run again.
	//
	// Command above is #{pane_current_command}, rewritten by the poller every
	// couple of seconds with the name of whatever is running now. "node" is
	// what an agent looks like through it, "bash" is what a shell somebody
	// typed in looks like, and neither is something to exec. Written once, at
	// creation, and never touched afterwards.
	LaunchCommand []string `json:"launchCommand"`

	// LaunchRecorded distinguishes "created with no command" from "created
	// before the panel recorded commands at all".
	//
	// Both are an empty LaunchCommand and they mean opposite things: the first
	// is a login shell and restoring it is exact, the second is a session the
	// panel cannot honestly claim to be able to rebuild. Rows that predate
	// migration v9 are the second, and the restore dialog has to say so rather
	// than quietly starting a shell under an agent's name.
	LaunchRecorded bool `json:"launchRecorded"`

	// LaunchProfileID is the profile this session was started with, empty for
	// one started without a profile.
	//
	// The id rather than the resolved variables, because a restore needs the
	// environment as well as the argv and copying the variables here would put
	// every key in a second table, once per session, where nothing redacts
	// them. The cost is that a profile deleted afterwards leaves this pointing
	// at nothing -- which is a fact the restore dialog can state, and a copy
	// would have hidden by restoring an endpoint the user has since removed.
	LaunchProfileID string `json:"launchProfileId"`

	// RestoreOnBoot means: when the tmux session is found missing at startup,
	// bring this one back without asking.
	//
	// Off by default and deliberately opt-in per session. A panel that silently
	// launched two dozen agents on every boot would be a worse failure than the
	// one restore exists to fix.
	RestoreOnBoot bool `json:"restoreOnBoot"`

	// RestoredAt is when this session was last rebuilt from a database row,
	// zero if never.
	//
	// The process behind a restored session is new — the agent's conversation
	// did not survive the reboot and nothing can bring it back. The pane gets a
	// banner saying so, but a banner scrolls off; this is what lets the UI keep
	// saying it after it has.
	RestoredAt int64 `json:"restoredAt"`

	// ScrollbackAt is when the archived scrollback for this session was
	// captured, zero if there is none. Joined in rather than stored here; see
	// session_scrollback.
	ScrollbackAt int64 `json:"scrollbackAt"`
	// Not sent. It changes at most once a second per session, and the state
	// snapshot is broadcast to every viewer whenever it differs from the last
	// one — so a single session producing output made every tick a broadcast,
	// which is the thing that comparison exists to prevent. Measured with six
	// sessions printing: ten ticks out of ten, 85 KiB/min per viewer, and at
	// two dozen sessions around 20 MB an hour on a phone.
	//
	// Nothing read it. It was declared in wire.ts and used nowhere, and the
	// ordering it drives is applied in SQL, so the array arrives already in
	// the right order. A display of "last active" should carry a value chosen
	// for display — bucketed, so it changes when the words would.
	LastOutputAt int64 `json:"-"`

	// Exited means the pane's process is gone and tmux is showing its last
	// screen. Orthogonal to State: the task may have been finished, abandoned
	// or killed, and only the exit status hints at which.
	Exited bool `json:"exited"`
	// ExitStatus is the wait status, meaningful only while Exited.
	//
	// ExitStatusVanished is the one value that is not a wait status: it marks a
	// session whose tmux session disappeared, where nothing was around to
	// observe how it ended.
	ExitStatus int `json:"exitStatus"`

	CreatedAt  int64  `json:"createdAt"`
	ArchivedAt *int64 `json:"archivedAt"`

	// ParentID is set for a scratch terminal opened under a main session.
	//
	// Bottom terminals are ordinary sessions with a parent rather than their
	// own kind of thing, so they get attaching, replay, state detection and
	// naming without a parallel implementation of each.
	ParentID *string `json:"parentSessionId"`
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

	var parent any
	if s.ParentID != nil {
		parent = *s.ParentID
	}

	// Always recorded from here on, including the empty argv. A caller that
	// creates a session at all knows what it asked for, so "not recorded" is
	// reserved for rows written before this column existed — and a restore
	// that cannot tell the two apart has to guess at a shell for a session
	// that was running an agent.
	launch, err := json.Marshal(emptyIfNil(s.LaunchCommand))
	if err != nil {
		return Session{}, fmt.Errorf("store: encode launch command: %w", err)
	}
	s.LaunchRecorded = true

	_, err = d.sql.ExecContext(ctx, `
		INSERT INTO sessions
			(id, project_id, tmux_name, title, title_source, state, state_source,
			 state_changed_at, cwd, command, cols, rows, created_at, parent_session_id,
			 launch_command, launch_profile_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.ProjectID, s.TmuxName, s.Title, s.TitleSource, s.State, s.StateSource,
		s.StateChangedAt, s.CWD, s.Command, s.Cols, s.Rows, s.CreatedAt, parent,
		string(launch), s.LaunchProfileID)
	if err != nil {
		return Session{}, fmt.Errorf("store: insert session: %w", err)
	}
	return s, nil
}

// emptyIfNil keeps a nil argv from marshalling as `null`.
//
// json.Marshal([]string(nil)) is "null", which reads back as nil and is
// indistinguishable from the column never having been written. The whole point
// of launch_command is telling those two apart.
func emptyIfNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// sessionColumns is qualified because every read joins the scrollback archive.
//
// The join is a primary-key lookup per row and buys the one fact the restore UI
// cannot do without: whether there is anything to put back, and how old it is.
// The blob itself is deliberately not in this list — see ScrollbackAt.
const sessionColumns = `s.id, s.project_id, s.tmux_name, s.title, s.title_source, s.state,
	s.state_source, s.state_changed_at, s.pinned, s.sort_index, s.cwd, s.command, s.cols,
	s.rows, s.last_output_at, s.created_at, s.archived_at, s.parent_session_id, s.exited,
	s.exit_status, s.launch_command, s.launch_profile_id, s.restore_on_boot, s.restored_at,
	COALESCE(sb.captured_at, 0)`

// sessionFrom is the FROM clause every session read shares.
const sessionFrom = `FROM sessions s LEFT JOIN session_scrollback sb ON sb.session_id = s.id`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var sortIdx sql.NullInt64
	var archived sql.NullInt64
	var parent sql.NullString
	var launch string
	err := sc.Scan(&s.ID, &s.ProjectID, &s.TmuxName, &s.Title, &s.TitleSource,
		&s.State, &s.StateSource, &s.StateChangedAt, &s.Pinned, &sortIdx,
		&s.CWD, &s.Command, &s.Cols, &s.Rows, &s.LastOutputAt, &s.CreatedAt, &archived,
		&parent, &s.Exited, &s.ExitStatus, &launch, &s.LaunchProfileID, &s.RestoreOnBoot,
		&s.RestoredAt, &s.ScrollbackAt)
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
	if parent.Valid {
		s.ParentID = &parent.String
	}
	// A row written before v9 has '' here, which is not JSON and must not be
	// reported as an empty argv: an empty argv is a login shell, and a session
	// that was running an agent restarted as a shell under the agent's name is
	// exactly the silent wrongness restore exists to avoid. Anything that fails
	// to parse is treated the same way — unrecorded — rather than surfaced as
	// an error that would take the whole sidebar down with it.
	if launch != "" {
		var argv []string
		if json.Unmarshal([]byte(launch), &argv) == nil {
			s.LaunchCommand = emptyIfNil(argv)
			s.LaunchRecorded = true
		}
	}
	if s.LaunchCommand == nil {
		s.LaunchCommand = []string{}
	}
	return s, nil
}

// ListChildSessions returns the scratch terminals under a session, oldest
// first.
//
// Ordered by creation rather than by state: these are tabs in a strip, and a
// tab strip that reorders itself while you are using it is hostile.
func (d *DB) ListChildSessions(ctx context.Context, parentID string) ([]Session, error) {
	rows, err := d.sql.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s %s
		WHERE s.parent_session_id = ?
		ORDER BY s.sort_index ASC, s.created_at ASC`, sessionColumns, sessionFrom), parentID)
	if err != nil {
		return nil, fmt.Errorf("store: list child sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan child session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
	return d.listSessions(ctx, "WHERE s.project_id = ?", []any{projectID})
}

func (d *DB) listSessions(ctx context.Context, where string, args []any) ([]Session, error) {
	q := fmt.Sprintf(`
		SELECT %s %s
		%s
		ORDER BY s.pinned DESC,
		         CASE s.state WHEN 'waiting' THEN 0 WHEN 'working' THEN 1 WHEN 'done' THEN 2 ELSE 3 END,
		         CASE WHEN s.sort_index IS NULL THEN 1 ELSE 0 END,
		         s.sort_index ASC,
		         s.last_output_at DESC,
		         s.created_at DESC`, sessionColumns, sessionFrom, where)

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
		fmt.Sprintf(`SELECT %s %s WHERE s.id = ?`, sessionColumns, sessionFrom), id)
	s, err := scanSession(row)
	if err == sql.ErrNoRows {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: get session: %w", err)
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

	// A session changing state is the project being active.
	//
	// Projects are ordered by last_active_at and the column was written in
	// exactly one place: creating a session. So "most active first" meant "most
	// recently given a new session first" — a project whose agents had been
	// working all day sat below one where a session was created in the morning
	// and never touched again. The consequence that matters is the ordering
	// nobody would think to question: a project that has a session waiting for
	// a human does not come to the top, which is the one thing this list is for.
	//
	// State changes rather than output. Output would mean a write per chunk;
	// state changes are already debounced by only being written when the state
	// actually differs, and they are the events a person cares about.
	if _, terr := d.sql.ExecContext(ctx, `
		UPDATE projects SET last_active_at = ?
		WHERE id = (SELECT project_id FROM sessions WHERE id = ?)`, now(), id); terr != nil {
		return fmt.Errorf("store: touch project for session state: %w", terr)
	}
	return nil
}

// SetSessionTitle renames a session and records where the name came from.
//
// An automatic update is ignored once the user has renamed the tab by hand:
// having a title you chose silently replaced by whatever the shell last set is
// exactly the behaviour this project exists to fix.
func (d *DB) SetSessionTitle(ctx context.Context, id, title string, src TitleSource) error {
	// Both sources are bounded. The automatic one is whatever the pane put in
	// its title, which is whatever an agent printed; the manual one is a text
	// field, and a field with no maximum is one somebody eventually pastes a
	// file into.
	title = session.TruncateTitle(title)
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
//
// Deliberately does not touch last_output_at. That column means "when this
// session last produced output", and a poller that stamps it every tick makes
// it mean "when we last looked" — which breaks the sidebar's activity ordering
// and defeats any attempt to detect that a session has gone quiet.
func (d *DB) UpdateSessionRuntime(ctx context.Context, id, cwd, command string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE sessions SET cwd = ?, command = ? WHERE id = ?`, cwd, command, id)
	if err != nil {
		return fmt.Errorf("store: update session runtime: %w", err)
	}
	return nil
}

// ExitStatusVanished marks a session whose tmux session is simply gone.
//
// A real wait status is never negative, so this cannot collide with one. It is
// distinct from exiting cleanly because it is not the same fact: a clean exit
// was watched happening, this one was noticed afterwards.
const ExitStatusVanished = -1

// SetSessionExit records whether a session's process is still there.
//
// Written by the poller from #{pane_dead}, and cleared again on respawn, so a
// session restarted from the panel stops claiming to be dead without waiting
// for the next tick.
func (d *DB) SetSessionExit(ctx context.Context, id string, exited bool, status int) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE sessions SET exited = ?, exit_status = ? WHERE id = ?`, exited, status, id)
	if err != nil {
		return fmt.Errorf("store: set session exit: %w", err)
	}
	return nil
}

// TouchSessionOutput records that a session produced output at the given time.
//
// Called from the PTY pump's signal path, not from the poller: only the pump
// knows when bytes actually arrived.
func (d *DB) TouchSessionOutput(ctx context.Context, id string, at int64) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE sessions SET last_output_at = ? WHERE id = ?`, at, id)
	if err != nil {
		return fmt.Errorf("store: touch session output: %w", err)
	}
	return nil
}

// SetSessionRestoreOnBoot decides whether a startup that finds this session's
// tmux session missing should rebuild it without asking.
func (d *DB) SetSessionRestoreOnBoot(ctx context.Context, id string, on bool) error {
	return d.exec1(ctx, `UPDATE sessions SET restore_on_boot = ? WHERE id = ?`, on, id)
}

// MarkSessionRestored records that a session was rebuilt from its row.
//
// Kept rather than cleared on the next restart: it is a fact about the pane
// somebody is looking at — the scrollback above the banner belongs to a process
// that no longer exists — and it stays true until the session is killed.
func (d *DB) MarkSessionRestored(ctx context.Context, id string, at int64) error {
	return d.exec1(ctx, `UPDATE sessions SET restored_at = ? WHERE id = ?`, at, id)
}

// DeleteSession removes a session row.
func (d *DB) DeleteSession(ctx context.Context, id string) error {
	return d.exec1(ctx, `DELETE FROM sessions WHERE id = ?`, id)
}
