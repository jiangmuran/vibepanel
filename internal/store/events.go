package store

import (
	"context"
	"fmt"

	"github.com/jiangmuran/vibepanel/internal/session"
)

// The panel keeps state, not history, and that is why a wall of trends could
// not be built.
//
// `sessions` carries one `state_changed_at` and no record of what came before
// it, so every question with a shape — how did today go, is the queue getting
// longer, how long does something sit before somebody gets to it — had no data
// behind it at all. Every widget degraded to one current number, which is how a
// television ended up showing `0`, `0` and a request count.
//
// This is the smallest thing that fixes it: an append-only row per transition.
// Four decisions are load-bearing.
//
//   - **It is a flow log, not a stock log.** A row says "this session left
//     `waiting` for `working` at this time, having been waiting 412 seconds".
//     It does not say how many sessions were waiting at 14:00. Reconstructing a
//     stock from a flow needs a starting census and every event since, and the
//     first restart with a dropped write makes the reconstruction wrong in a way
//     nothing can detect. Flows are exactly true or absent, and "eleven things
//     finished this afternoon" is the sentence a wall wants anyway.
//
//   - **`for_seconds` is written at the transition, not derived later.** The
//     poller already holds the row it is about to overwrite, so the dwell is a
//     subtraction it can do for free. Deriving it afterwards is a self-join
//     against the previous event for the same session, which is the one query
//     shape this table would need an index it does not have.
//
//   - **No foreign key to `sessions`.** A deleted session must not delete
//     yesterday's afternoon. A cascade here would silently rewrite a chart that
//     has already been looked at, and the row holds no name — only ids that the
//     share surface renames under a per-link secret before anything leaves the
//     machine.
//
//   - **It is swept, not kept.** See EventRetentionDays.
//
// What is deliberately not here: titles, commands, paths, or the reason a state
// changed. The detector's `source` was considered and left out — it is a fact
// about how the panel guessed, which is a debugging question and not a trend.

// EventRetentionDays is how far back the log is kept.
//
// Thirty-one days, and the number follows from what draws it rather than from a
// feeling about disk. The widest window any widget offers over this table is
// thirty days, the same window shareSpendWindowDays uses, and one day of slack
// keeps the oldest bucket whole while a sweep is pending.
//
// A wall that has been up for a month should not be carrying a year of rows,
// and the arithmetic says why: two dozen sessions changing state a hundred
// times a day is 2,400 rows a day, so a year is nearly a million rows in the
// same file the panel writes session state to every two seconds. The token
// rollups next door hold a year because they are already aggregated to one row
// per day per model; this is not aggregated and must not pretend to be.
const EventRetentionDays = 31

// MaxEventFeed bounds how many rows the feed reads at once.
//
// A wall polls every two seconds forever, and this list is rendered into a tile
// that shows perhaps a dozen lines. The bound is on the work, not on the tile.
const MaxEventFeed = 40

// MaxEventBuckets bounds how many buckets one series may be cut into.
//
// A bucket count is arithmetic on two timestamps and a width, and every one of
// those comes from a widget's bounded settings — but the multiplication is
// where an unbounded series would appear, so it is clamped where it is used
// rather than trusted to stay small.
const MaxEventBuckets = 400

// SessionEvent is one transition, as it is stored and as the feed reads it.
type SessionEvent struct {
	At        int64
	SessionID string
	ProjectID string
	From      session.State
	To        session.State
	// ForSeconds is how long the session had been in From. Zero when the panel
	// had no earlier timestamp for it, which is what a session it has only just
	// met looks like.
	ForSeconds int64
}

// EventBucket is one interval of the flow series.
type EventBucket struct {
	// At is the start of the bucket, in unix seconds.
	At int64
	// Started, Waited and Finished are transitions *into* each state during
	// this bucket. A session that went working -> waiting -> working counts in
	// both Waited and Started, which is correct: both happened.
	Started  int
	Waited   int
	Finished int
	// WaitSeconds and WaitEnded are the dwell of every transition *out of*
	// waiting that landed in this bucket, and how many there were. The average
	// is taken on the far side so that an empty bucket is an empty bucket
	// rather than a zero-second wait.
	WaitSeconds int64
	WaitEnded   int
}

// RecordSessionEvent appends one transition.
//
// Never called on the poller's own goroutine — see the recorder in
// internal/httpapi. A write to this table must not be able to delay the panel's
// idea of what a session is doing, which is the thing every other feature here
// is built on.
func (d *DB) RecordSessionEvent(ctx context.Context, ev SessionEvent) error {
	if !ev.To.Valid() || ev.SessionID == "" {
		// Refused rather than stored. Red line 6's reasoning applies one table
		// over: an unrecognised state in here is a row every future series
		// silently miscounts, and there is nothing on a wall to say so.
		return fmt.Errorf("store: not a state transition worth recording")
	}
	if ev.ForSeconds < 0 {
		// A clock that went backwards, or a row whose timestamp is in the
		// future. Zero rather than a negative dwell, which would subtract from
		// an average.
		ev.ForSeconds = 0
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO session_events (at, session_id, project_id, from_state, to_state, for_seconds)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ev.At, ev.SessionID, ev.ProjectID, string(ev.From), string(ev.To), ev.ForSeconds)
	if err != nil {
		return fmt.Errorf("store: record session event: %w", err)
	}
	return nil
}

// RecordSessionEvents appends a batch of transitions in one transaction.
//
// One transaction rather than one per row, and the reason is contention rather
// than speed. SQLite takes a single write lock for the whole database, and the
// pool here is four connections wide with a five-second busy timeout; the
// poller is already writing every two seconds and the panel saves notes,
// scrollback and audit rows on top of that. A burst of transitions -- two dozen
// sessions changing state in the same tick, which is what a restart looks like
// -- was N separate lock acquisitions competing with all of it, and it was
// visible: a note save came back `database is locked (5)` in a browser check
// that had never produced one before.
//
// A row that will not go in takes the batch with it. That is the right way
// round: the alternative is a partial afternoon in a chart with nothing saying
// which half is missing, and the caller already treats a lost batch as a gap.
func (d *DB) RecordSessionEvents(ctx context.Context, evs []SessionEvent) error {
	if len(evs) == 0 {
		return nil
	}
	if len(evs) == 1 {
		return d.RecordSessionEvent(ctx, evs[0])
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: record session events: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO session_events (at, session_id, project_id, from_state, to_state, for_seconds)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: record session events: %w", err)
	}
	defer stmt.Close()
	for _, ev := range evs {
		if !ev.To.Valid() || ev.SessionID == "" {
			// Skipped rather than failing the batch: one unrecognised state
			// must not lose the transitions queued beside it. The single-row
			// path refuses, because there it is the caller's own row.
			continue
		}
		if ev.ForSeconds < 0 {
			ev.ForSeconds = 0
		}
		if _, err := stmt.ExecContext(ctx, ev.At, ev.SessionID, ev.ProjectID,
			string(ev.From), string(ev.To), ev.ForSeconds); err != nil {
			return fmt.Errorf("store: record session events: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: record session events: %w", err)
	}
	return nil
}

// SweepSessionEvents drops everything older than `before`, and reports how many
// rows went.
func (d *DB) SweepSessionEvents(ctx context.Context, before int64) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM session_events WHERE at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("store: sweep session events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// EventScope narrows a query to what one share link is about.
//
// Two ids rather than a WHERE string built by the caller. The empty value of
// each means "do not narrow by this", and both being empty is the whole panel —
// which is why every caller has to have decided which it means before it gets
// here. resolveScope in internal/httpapi is that decision.
type EventScope struct {
	ProjectID string
	SessionID string
}

func (s EventScope) where() (string, []any) {
	switch {
	case s.SessionID != "":
		return " AND session_id = ?", []any{s.SessionID}
	case s.ProjectID != "":
		return " AND project_id = ?", []any{s.ProjectID}
	default:
		return "", nil
	}
}

// SessionEventSeries buckets the transitions between two times.
//
// `bucket` is the width in seconds. The bucketing is done in SQL rather than in
// Go because the alternative is reading every row of a month to add them up in
// a loop, on a wall that asks every couple of seconds.
//
// Buckets with nothing in them are filled in here rather than left out, because
// a chart drawn from only the non-empty buckets is a chart with no quiet
// afternoons in it — the gaps are the shape of the day.
func (d *DB) SessionEventSeries(ctx context.Context, since, until int64, bucket int,
	scope EventScope) ([]EventBucket, error) {
	if bucket <= 0 || until <= since {
		return []EventBucket{}, nil
	}
	n := int((until - since + int64(bucket) - 1) / int64(bucket))
	if n <= 0 {
		return []EventBucket{}, nil
	}
	if n > MaxEventBuckets {
		n = MaxEventBuckets
	}

	clause, args := scope.where()
	query := `SELECT (at - ?) / ? AS b,
	                 SUM(CASE WHEN to_state = 'working' THEN 1 ELSE 0 END),
	                 SUM(CASE WHEN to_state = 'waiting' THEN 1 ELSE 0 END),
	                 SUM(CASE WHEN to_state = 'done' THEN 1 ELSE 0 END),
	                 SUM(CASE WHEN from_state = 'waiting' THEN for_seconds ELSE 0 END),
	                 SUM(CASE WHEN from_state = 'waiting' THEN 1 ELSE 0 END)
	          FROM session_events
	          WHERE at >= ? AND at < ?` + clause + `
	          GROUP BY b`
	full := append([]any{since, bucket, since, until}, args...)

	rows, err := d.sql.QueryContext(ctx, query, full...)
	if err != nil {
		return nil, fmt.Errorf("store: session event series: %w", err)
	}
	defer rows.Close()

	out := make([]EventBucket, n)
	for i := range out {
		out[i].At = since + int64(i*bucket)
	}
	for rows.Next() {
		var b int64
		var started, waited, finished, waitSecs, waitEnded int64
		if err := rows.Scan(&b, &started, &waited, &finished, &waitSecs, &waitEnded); err != nil {
			return nil, fmt.Errorf("store: session event series: %w", err)
		}
		if b < 0 || b >= int64(n) {
			continue
		}
		out[b].Started = int(started)
		out[b].Waited = int(waited)
		out[b].Finished = int(finished)
		out[b].WaitSeconds = waitSecs
		out[b].WaitEnded = int(waitEnded)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: session event series: %w", err)
	}
	return out, nil
}

// RecentSessionEvents is the feed: what just happened, newest first.
func (d *DB) RecentSessionEvents(ctx context.Context, since int64, limit int,
	scope EventScope) ([]SessionEvent, error) {
	if limit <= 0 || limit > MaxEventFeed {
		limit = MaxEventFeed
	}
	clause, args := scope.where()
	query := `SELECT at, session_id, project_id, from_state, to_state, for_seconds
	          FROM session_events
	          WHERE at >= ?` + clause + `
	          ORDER BY at DESC, rowid DESC
	          LIMIT ?`
	full := append(append([]any{since}, args...), limit)

	rows, err := d.sql.QueryContext(ctx, query, full...)
	if err != nil {
		return nil, fmt.Errorf("store: recent session events: %w", err)
	}
	defer rows.Close()
	out := []SessionEvent{}
	for rows.Next() {
		var ev SessionEvent
		var from, to string
		if err := rows.Scan(&ev.At, &ev.SessionID, &ev.ProjectID, &from, &to, &ev.ForSeconds); err != nil {
			return nil, fmt.Errorf("store: recent session events: %w", err)
		}
		ev.From, ev.To = session.State(from), session.State(to)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: recent session events: %w", err)
	}
	return out, nil
}

// CountSessionEvents is the totals the single-figure widgets show: how many
// transitions into each state since a moment.
func (d *DB) CountSessionEvents(ctx context.Context, since int64, scope EventScope) (EventBucket, error) {
	clause, args := scope.where()
	query := `SELECT SUM(CASE WHEN to_state = 'working' THEN 1 ELSE 0 END),
	                 SUM(CASE WHEN to_state = 'waiting' THEN 1 ELSE 0 END),
	                 SUM(CASE WHEN to_state = 'done' THEN 1 ELSE 0 END),
	                 SUM(CASE WHEN from_state = 'waiting' THEN for_seconds ELSE 0 END),
	                 SUM(CASE WHEN from_state = 'waiting' THEN 1 ELSE 0 END)
	          FROM session_events
	          WHERE at >= ?` + clause
	full := append([]any{since}, args...)

	// Nullable on every column, because SUM over no rows is NULL rather than 0
	// and a scan into an int fails on it. That is the shape of a panel on its
	// first morning, which is exactly when a wall is being looked at.
	var started, waited, finished, waitSecs, waitEnded *int64
	err := d.sql.QueryRowContext(ctx, query, full...).
		Scan(&started, &waited, &finished, &waitSecs, &waitEnded)
	if err != nil {
		return EventBucket{}, fmt.Errorf("store: count session events: %w", err)
	}
	deref := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	return EventBucket{
		At: since, Started: int(deref(started)), Waited: int(deref(waited)),
		Finished: int(deref(finished)), WaitSeconds: deref(waitSecs),
		WaitEnded: int(deref(waitEnded)),
	}, nil
}
