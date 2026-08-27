package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// UsageStamp is what the last pass saw of one transcript file.
//
// Size and ModifiedAt together are the cursor: a file whose stat matches what
// is recorded here contributed everything it is going to, and the pass skips
// it without opening it. That gate is the whole of the incremental design —
// without it every pass would re-read all 2.16 GB, which on the machine this
// was written on is 3.09 seconds instead of 35 milliseconds.
type UsageStamp struct {
	Size       int64
	ModifiedAt int64
}

// UsageFile is one transcript's contribution, as it goes into the database.
type UsageFile struct {
	Path       string
	Tool       string
	Size       int64
	ModifiedAt int64
	Skipped    int
	Problem    string
	Rows       []UsageRow
}

// UsageRow is one (day, agent session, model) bucket.
type UsageRow struct {
	Day        string
	Tool       string
	Session    string
	CWD        string
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Requests   int64
}

// UsageStamps returns every transcript the last pass recorded.
func (d *DB) UsageStamps(ctx context.Context) (map[string]UsageStamp, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT path, size, modified_at FROM usage_files`)
	if err != nil {
		return nil, fmt.Errorf("store: list usage files: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := map[string]UsageStamp{}
	for rows.Next() {
		var p string
		var s UsageStamp
		if err := rows.Scan(&p, &s.Size, &s.ModifiedAt); err != nil {
			return nil, fmt.Errorf("store: scan usage file: %w", err)
		}
		out[p] = s
	}
	return out, rows.Err()
}

// ReplaceUsageFile writes one transcript's whole contribution, replacing
// whatever it contributed before.
//
// Delete-then-insert inside one transaction, which is what makes reading a
// file twice harmless. The alternative — upserting the rows that are there now
// — leaves behind any bucket that has since stopped existing, and buckets do
// stop existing: Claude Code rewrites a transcript when a session is resumed
// from a earlier point, and the branch that was abandoned goes with it.
func (d *DB) ReplaceUsageFile(ctx context.Context, f UsageFile) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin usage write: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a commit

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO usage_files (path, tool, size, modified_at, scanned_at, skipped, problem)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			tool        = excluded.tool,
			size        = excluded.size,
			modified_at = excluded.modified_at,
			scanned_at  = excluded.scanned_at,
			skipped     = excluded.skipped,
			problem     = excluded.problem`,
		f.Path, f.Tool, f.Size, f.ModifiedAt, now(), f.Skipped, f.Problem); err != nil {
		return fmt.Errorf("store: put usage file %s: %w", f.Path, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_daily WHERE path = ?`, f.Path); err != nil {
		return fmt.Errorf("store: clear usage rows for %s: %w", f.Path, err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_daily
			(path, day, tool, agent_session, cwd, model,
			 input, output, cache_read, cache_write, requests)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: prepare usage insert: %w", err)
	}
	defer stmt.Close() //nolint:errcheck // closed by the transaction anyway
	for _, r := range f.Rows {
		if _, err := stmt.ExecContext(ctx, f.Path, r.Day, f.Tool, r.Session, r.CWD, r.Model,
			r.Input, r.Output, r.CacheRead, r.CacheWrite, r.Requests); err != nil {
			return fmt.Errorf("store: insert usage row for %s: %w", f.Path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit usage %s: %w", f.Path, err)
	}
	return nil
}

// ForgetUsageFiles drops transcripts that are no longer on disk.
//
// Agents do delete their own history, and a file that is gone must take its
// numbers with it. Leaving them would make the totals a record of every
// transcript that ever existed, which is a different and unfalsifiable claim:
// nothing on disk would back it up any more.
func (d *DB) ForgetUsageFiles(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin usage forget: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a commit
	for _, p := range paths {
		// usage_daily has ON DELETE CASCADE, and foreign_keys is on for every
		// pooled connection through the DSN. Deleting the parent is enough.
		if _, err := tx.ExecContext(ctx, `DELETE FROM usage_files WHERE path = ?`, p); err != nil {
			return fmt.Errorf("store: forget usage file %s: %w", p, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit usage forget: %w", err)
	}
	return nil
}

// UsageFilter narrows a query. Every field is optional; the zero value asks
// for everything.
type UsageFilter struct {
	// From and To are inclusive YYYY-MM-DD in the server's local zone, the
	// same form the rows are keyed by.
	From, To string
	Tool     string
	// CWDPrefix keeps only work done inside a directory. Compared with substr
	// rather than LIKE because a project path may legitimately contain % or _,
	// and a LIKE pattern built from user data is a filter that quietly matches
	// the wrong directories.
	CWDPrefix string
}

func (f UsageFilter) where() (string, []any) {
	var clauses []string
	var args []any
	if f.From != "" {
		clauses = append(clauses, "day >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		clauses = append(clauses, "day <= ?")
		args = append(args, f.To)
	}
	if f.Tool != "" {
		clauses = append(clauses, "tool = ?")
		args = append(args, f.Tool)
	}
	if f.CWDPrefix != "" {
		p := strings.TrimSuffix(f.CWDPrefix, "/")
		under := p + "/"
		clauses = append(clauses, "(cwd = ? OR substr(cwd, 1, ?) = ?)")
		args = append(args, p, len(under), under)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// UsageTotals is one row of any grouped usage query.
type UsageTotals struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Requests   int64 `json:"requests"`
}

// Total is every token the rows were billed for, cache included.
func (u UsageTotals) Total() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// UsageDay is one calendar day's spend.
type UsageDay struct {
	Day string `json:"day"`
	UsageTotals
}

// UsageAgentSession is one agent session's spend, across every day it ran.
type UsageAgentSession struct {
	// Session is the *agent's* id, from its own transcript. It is not a
	// vibepanel session id and there is no mapping between them; see
	// internal/usage.
	Session   string `json:"session"`
	Tool      string `json:"tool"`
	CWD       string `json:"cwd"`
	Models    string `json:"models"`
	FirstDay  string `json:"firstDay"`
	LastDay   string `json:"lastDay"`
	DaysCount int    `json:"days"`
	UsageTotals
}

// UsageByDay returns a row per day that has any usage, oldest first.
func (d *DB) UsageByDay(ctx context.Context, f UsageFilter) ([]UsageDay, error) {
	where, args := f.where()
	rows, err := d.sql.QueryContext(ctx, `
		SELECT day,
		       SUM(input), SUM(output), SUM(cache_read), SUM(cache_write), SUM(requests)
		FROM usage_daily`+where+`
		GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: usage by day: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []UsageDay{}
	for rows.Next() {
		var u UsageDay
		if err := rows.Scan(&u.Day, &u.Input, &u.Output, &u.CacheRead, &u.CacheWrite,
			&u.Requests); err != nil {
			return nil, fmt.Errorf("store: scan usage day: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsageByMonth returns a row per calendar month, oldest first.
//
// substr rather than strftime, because `day` is a bucket label and not a date.
// It was written as a *local* calendar day by the reader; strftime is a date
// parse, and a parse can fail — SQLite answers NULL for anything it does not
// recognise, so a single unexpected value would appear as a month called
// "null" sitting in the middle of the chart rather than as itself. Slicing
// seven characters cannot fail and cannot reinterpret the zone.
func (d *DB) UsageByMonth(ctx context.Context, f UsageFilter) ([]UsageDay, error) {
	where, args := f.where()
	rows, err := d.sql.QueryContext(ctx, `
		SELECT substr(day, 1, 7) AS month,
		       SUM(input), SUM(output), SUM(cache_read), SUM(cache_write), SUM(requests)
		FROM usage_daily`+where+`
		GROUP BY month ORDER BY month`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: usage by month: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []UsageDay{}
	for rows.Next() {
		var u UsageDay
		if err := rows.Scan(&u.Day, &u.Input, &u.Output, &u.CacheRead, &u.CacheWrite,
			&u.Requests); err != nil {
			return nil, fmt.Errorf("store: scan usage month: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsageBySession returns a row per agent session, biggest first.
//
// Capped, because a year of history is thousands of sessions and the panel
// asks this question to answer "what did the expensive ones cost". The caller
// is told how many there were in total so it can say the list is partial
// rather than implying it is all of them -- the same reasoning as the file
// browser's truncation.
func (d *DB) UsageBySession(ctx context.Context, f UsageFilter, limit int) ([]UsageAgentSession, int, error) {
	where, args := f.where()
	var total int
	if err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (SELECT 1 FROM usage_daily`+where+
			` GROUP BY tool, agent_session)`, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count usage sessions: %w", err)
	}

	q := `
		SELECT agent_session, tool,
		       cwd,
		       group_concat(DISTINCT model),
		       MIN(day), MAX(day), COUNT(DISTINCT day),
		       SUM(input), SUM(output), SUM(cache_read), SUM(cache_write), SUM(requests)
		FROM usage_daily` + where + `
		GROUP BY tool, agent_session
		ORDER BY SUM(input) + SUM(output) + SUM(cache_read) + SUM(cache_write) DESC
		LIMIT ?`
	rows, err := d.sql.QueryContext(ctx, q, append(append([]any{}, args...), limit)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: usage by session: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []UsageAgentSession{}
	for rows.Next() {
		var s UsageAgentSession
		var models sql.NullString
		if err := rows.Scan(&s.Session, &s.Tool, &s.CWD, &models, &s.FirstDay, &s.LastDay,
			&s.DaysCount, &s.Input, &s.Output, &s.CacheRead, &s.CacheWrite,
			&s.Requests); err != nil {
			return nil, 0, fmt.Errorf("store: scan usage session: %w", err)
		}
		s.Models = models.String
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// UsageDirectory is one working directory's spend.
type UsageDirectory struct {
	CWD string `json:"cwd"`
	UsageTotals
}

// UsageByDirectory lists the working directories that have any usage, biggest
// first.
//
// Returned as directories rather than as projects because the two are not the
// same set, and the difference is the interesting part: an agent run in a
// directory the panel has never been told about still spent money, and folding
// it into "no project" in SQL would hide where it went. The caller matches
// these against the projects it knows and keeps the rest.
func (d *DB) UsageByDirectory(ctx context.Context, f UsageFilter) ([]UsageDirectory, error) {
	where, args := f.where()
	rows, err := d.sql.QueryContext(ctx, `
		SELECT cwd, SUM(input), SUM(output), SUM(cache_read), SUM(cache_write), SUM(requests)
		FROM usage_daily`+where+`
		GROUP BY cwd
		ORDER BY SUM(input) + SUM(output) + SUM(cache_read) + SUM(cache_write) DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: usage directories: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []UsageDirectory{}
	for rows.Next() {
		var u UsageDirectory
		if err := rows.Scan(&u.CWD, &u.Input, &u.Output, &u.CacheRead, &u.CacheWrite,
			&u.Requests); err != nil {
			return nil, fmt.Errorf("store: scan usage directory: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UsageModel is one model's spend.
type UsageModel struct {
	Model string `json:"model"`
	UsageTotals
}

// UsageByModel returns a row per model, biggest first.
//
// The column has been there since the rows were first written and nothing read
// it across the whole table until a board asked "which model is doing the
// work". A model name is the vendor's — claude-opus-4, gpt-5-codex — so unlike
// a cwd it names nothing of the user's, which is why a share link may carry it.
func (d *DB) UsageByModel(ctx context.Context, f UsageFilter) ([]UsageModel, error) {
	where, args := f.where()
	rows, err := d.sql.QueryContext(ctx, `
		SELECT model, SUM(input), SUM(output), SUM(cache_read), SUM(cache_write), SUM(requests)
		FROM usage_daily`+where+`
		GROUP BY model
		ORDER BY SUM(input) + SUM(output) + SUM(cache_read) + SUM(cache_write) DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: usage by model: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []UsageModel{}
	for rows.Next() {
		var m UsageModel
		if err := rows.Scan(&m.Model, &m.Input, &m.Output, &m.CacheRead, &m.CacheWrite,
			&m.Requests); err != nil {
			return nil, fmt.Errorf("store: scan usage model: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UsageToolTotals is what one agent contributed, and what could not be read
// while working it out.
type UsageToolTotals struct {
	Tool string `json:"tool"`
	UsageTotals
	// Files and Skipped come from usage_files rather than from the rows, so
	// that a tool whose transcripts were all unreadable reports as read-and-
	// empty rather than as absent.
	Files    int    `json:"files"`
	Skipped  int    `json:"skipped"`
	Problems int    `json:"problems"`
	Problem  string `json:"problem"`
}

// UsageByTool returns a row per agent, whether or not it spent anything.
func (d *DB) UsageByTool(ctx context.Context, f UsageFilter) (map[string]UsageToolTotals, error) {
	out := map[string]UsageToolTotals{}

	where, args := f.where()
	rows, err := d.sql.QueryContext(ctx, `
		SELECT tool, SUM(input), SUM(output), SUM(cache_read), SUM(cache_write), SUM(requests)
		FROM usage_daily`+where+`
		GROUP BY tool`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: usage by tool: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	for rows.Next() {
		var t UsageToolTotals
		if err := rows.Scan(&t.Tool, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite,
			&t.Requests); err != nil {
			return nil, fmt.Errorf("store: scan usage tool: %w", err)
		}
		out[t.Tool] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The file side is not filtered by day: "how many transcripts did the last
	// pass read" is a fact about the pass, not about the range on screen, and
	// scoping it to the range would report zero files for a range with no
	// traffic in it.
	fileRows, err := d.sql.QueryContext(ctx, `
		SELECT tool, COUNT(*), SUM(skipped), SUM(CASE WHEN problem <> '' THEN 1 ELSE 0 END),
		       COALESCE(MAX(problem), '')
		FROM usage_files GROUP BY tool`)
	if err != nil {
		return nil, fmt.Errorf("store: usage file stats: %w", err)
	}
	defer fileRows.Close() //nolint:errcheck // read-only
	for fileRows.Next() {
		var tool, problem string
		var files, skipped, problems int
		if err := fileRows.Scan(&tool, &files, &skipped, &problems, &problem); err != nil {
			return nil, fmt.Errorf("store: scan usage file stats: %w", err)
		}
		t := out[tool]
		t.Tool = tool
		t.Files, t.Skipped, t.Problems, t.Problem = files, skipped, problems, problem
		out[tool] = t
	}
	return out, fileRows.Err()
}
