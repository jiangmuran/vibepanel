package usage

import (
	"database/sql"
	"fmt"
	"time"
)

// ─── opencode ─────────────────────────────────────────────────────────────

// dbFile is the ledger, inside the root the scanner probes.
const dbFile = "opencode.db"

/*
readOpencode aggregates opencode's database.

The third agent the panel starts, and for a while the one it could not count.
The first look here found only `migration` and `session_diff` under the root and
concluded there was no ledger; the ledger was a 2.2 GB SQLite file in the same
directory, holding 993 million tokens. Listing a directory is not reading it.

Why a query rather than a scan. The file is large and almost all of it is the
`part` table, which is tool output: 3,722 messages against 2.2 GB. Selecting the
assistant rows and their token objects takes 172 ms through this driver on the
machine this was written on, so the (size, mtime) cursor the other two readers
use works unchanged -- the database is one path, re-read whole when it moves.

What the columns are, taken from opencode's own aggregation rather than
inferred from a sample:

	total = input + output + reasoning + cache.read + cache.write

All five are disjoint. `input` is the uncached remainder -- measured, 109 fresh
against 8,192 cache read on the same message -- which is Anthropic's convention
and the opposite of Codex's, where the cached part is *inside* `input_tokens`
and has to be subtracted. Getting that backwards would not fail anywhere: it
would report a plausible number about a quarter too small.

`reasoning` is its own field here and is folded into Output, which keeps the
invariant the Codex reader is pinned to -- what the panel reports for a thread
is what the agent itself last wrote down. It is also the honest place for it:
reasoning tokens are the model producing, not the cost of asking.

	Two summations exist in opencode's own binary and one of them omits
	reasoning. The one followed here is the one that agrees with the `total`
	field stored on every message, which is the figure a person can check.

Columns are named one by one, and that is a rule rather than a style. This
database also holds `account.access_token`, `account.refresh_token` and
`credential.value`. A `SELECT *` here, or a later "just read the whole row and
pick fields in Go", puts somebody's OAuth tokens in the panel's memory for no
reason at all. Nothing below reads a table other than `message` and `session`.

Opened read-only, and it stays that way while opencode is running: `mode=ro`
plus `query_only`, so a bug here cannot write to a database the panel does not
own. `busy_timeout` is short because this runs on a background pass -- being
late is free, and waiting on somebody else's write is not worth it.
*/
func readOpencode(path string, loc *time.Location) (readResult, error) {
	var out readResult

	// Immutable is deliberately *not* set. It would skip the WAL, which is
	// where a running opencode's most recent work is -- 174 MB of it here --
	// and the reader would silently report an old answer as a current one.
	dsn := "file:" + path + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return out, err
	}
	defer db.Close() //nolint:errcheck // read-only

	rows, err := db.Query(`
		SELECT m.session_id,
		       json_extract(m.data, '$.time.created'),
		       json_extract(m.data, '$.modelID'),
		       json_extract(m.data, '$.tokens.input'),
		       json_extract(m.data, '$.tokens.output'),
		       json_extract(m.data, '$.tokens.reasoning'),
		       json_extract(m.data, '$.tokens.cache.read'),
		       json_extract(m.data, '$.tokens.cache.write'),
		       s.directory
		FROM message m
		LEFT JOIN session s ON s.id = m.session_id
		WHERE json_extract(m.data, '$.role') = 'assistant'`)
	if err != nil {
		// A schema this does not recognise is a Problem on the source, not a
		// failed pass: opencode may be older or newer than this reader, and
		// the other two agents' numbers must still arrive.
		return out, fmt.Errorf("reading %s: %w", dbFile, err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	agg := map[key]*Bucket{}
	for rows.Next() {
		var session string
		var created sql.NullInt64
		var model, dir sql.NullString
		var in, outTok, reasoning, cacheRead, cacheWrite sql.NullInt64
		if err := rows.Scan(&session, &created, &model, &in, &outTok, &reasoning,
			&cacheRead, &cacheWrite, &dir); err != nil {
			return out, err
		}
		// Milliseconds since the epoch, not RFC 3339 like the other two, so
		// localDay cannot be reused. Zero is not a timestamp: a row without
		// one cannot be put on a day, and guessing today for it would move
		// somebody's history onto the day the panel happened to read it.
		if !created.Valid || created.Int64 <= 0 {
			out.skipped++
			continue
		}
		day := time.UnixMilli(created.Int64).In(loc).Format(dayFormat)

		c := Counts{
			Input:  in.Int64,
			Output: outTok.Int64 + reasoning.Int64,

			CacheRead:  cacheRead.Int64,
			CacheWrite: cacheWrite.Int64,
		}
		if c.Total() == 0 {
			// An assistant message that spent nothing: a refusal, an aborted
			// turn. Not a request either -- there was no request.
			continue
		}
		c.Requests = 1
		add(agg, key{day, session, model.String}, dir.String, c)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.buckets = flatten(agg)
	return out, nil
}
