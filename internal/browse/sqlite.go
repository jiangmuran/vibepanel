package browse

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	// The driver the store already links; nothing new in the binary.
	_ "modernc.org/sqlite"
)

// A SQLite database's tables, for the viewer (docs/file-preview.md, "More
// kinds"). The file is somebody's -- an agent's scratch database, a test
// fixture, an app's data -- and it may be open in a process that is writing
// it right now, so it is opened read-only *and* immutable, every statement has
// a deadline, and the only SQL that runs is this file's own, with the one name
// a request supplies matched against the database's own list of tables and
// then quoted.

// sqliteMagic is the first sixteen bytes of every SQLite 3 database.
var sqliteMagic = []byte("SQLite format 3\x00")

// IsSQLite is the database half of SniffMedia.
func IsSQLite(head []byte) bool { return bytes.HasPrefix(head, sqliteMagic) }

const (
	// DBMaxRows is how many rows of the chosen table are sent.
	DBMaxRows = 200
	// dbMaxColumns bounds a row's width; SQLite allows two thousand.
	dbMaxColumns = 200
	// dbMaxText is how many characters of one text value are sent.
	dbMaxText = 1000
	// dbMaxBytes bounds the rows' text in one answer, so 200 rows of 200
	// columns of long text is not 160 MB.
	dbMaxBytes = 4 << 20
	// dbMaxTables bounds the list, each of which is counted.
	dbMaxTables = 1000
)

// dbStatementTimeout is each statement's deadline: a count over a table of a
// hundred million rows, or a view that recurses forever, is interrupted here.
// A variable so a test can wait less than the real two seconds.
var dbStatementTimeout = 2 * time.Second

// dbCountBudget is how long counting rows may take across all the tables;
// past it the rest are not counted.
var dbCountBudget = 5 * time.Second

// DBColumn is a column as the schema declares it.
type DBColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// DBTable is one table or view.
type DBTable struct {
	Name string `json:"name"`
	// Kind is table or view.
	Kind string `json:"kind"`
	// Rows is the row count, -1 when it was not counted: a view (counting
	// one runs it), a table whose count ran out of time, or one the driver
	// cannot read (a virtual table whose module is not here).
	Rows    int64      `json:"rows"`
	Columns []DBColumn `json:"columns"`
	// SQL is the statement that created it, for the viewer's Source.
	SQL string `json:"sql"`
}

// DBListing is a database's tables and the first rows of one of them.
type DBListing struct {
	Tables []DBTable `json:"tables"`
	// Table is the one whose rows follow; empty when there are no tables.
	Table   string     `json:"table"`
	Columns []DBColumn `json:"columns"`
	// Rows is up to DBMaxRows rows, each cell one of: null for NULL, a
	// number for an INTEGER that fits a double exactly or a REAL, a string
	// for TEXT, {"int": "…"} for an INTEGER past 2^53, {"real": "…"} for an
	// infinity, {"blob": n} for a BLOB of n bytes (never its bytes), and
	// {"text": "…", "more": true} for TEXT cut at dbMaxText characters.
	Rows [][]any `json:"rows"`
	// More is whether the table has rows past these.
	More bool   `json:"more"`
	Note string `json:"note,omitempty"`
}

// DBError is a database the panel could not read, or a request it refused,
// with a sentence. BadRequest separates the two: a table that is not in the
// database is the caller's, a damaged file is the file's.
type DBError struct {
	Reason     string
	BadRequest bool
}

func (e *DBError) Error() string { return e.Reason }

// cutSchema bounds one sqlite_schema CREATE statement. dbMaxText bounds what
// a cell of the answer carries and the schema is a cell like any other: a
// hostile database ships megabyte CREATE clauses, and the bound on rows was
// never the bound on the list.
func cutSchema(s string) string {
	if len(s) <= dbMaxText {
		return s
	}
	r := []rune(s)
	if len(r) <= dbMaxText {
		return s
	}
	return string(r[:dbMaxText])
}

// ReadDatabase lists the database at path and reads the first rows of table,
// or of the first table when table is empty.
func ReadDatabase(ctx context.Context, path, table string) (*DBListing, error) {
	// mode=ro: nothing is written. immutable=1: the file is read as it is on
	// disk, with no locks taken and no -wal or -shm made beside it -- a
	// read-only connection to a WAL database otherwise creates its -shm in the
	// project directory, and taking a lock on a file another process is
	// writing is a stall for that process. The cost is named in the API
	// documentation: rows still in another process's -wal are not seen.
	// _query_only and _defensive on top, and trusted_schema off, so a view or
	// a trigger in the file runs no function the schema could abuse.
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?mode=ro&immutable=1&_query_only=1&_defensive=1&_pragma=trusted_schema(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, &DBError{Reason: "the database could not be opened: " + err.Error()}
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	l := &DBListing{Tables: []DBTable{}, Columns: []DBColumn{}, Rows: [][]any{}}
	err = query(ctx, db, func(rows *sql.Rows) error {
		for rows.Next() {
			var t DBTable
			var stmt sql.NullString
			if err := rows.Scan(&t.Kind, &t.Name, &stmt); err != nil {
				return err
			}
			t.SQL, t.Rows, t.Columns = cutSchema(stmt.String), -1, []DBColumn{}
			if len(l.Tables) == dbMaxTables {
				l.Note = fmt.Sprintf("Only the first %d tables are listed.", dbMaxTables)
				break
			}
			l.Tables = append(l.Tables, t)
		}
		return nil
	}, `SELECT type, name, sql FROM sqlite_schema
	    WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
	    ORDER BY name`)
	if err != nil {
		return nil, dbFailure(err)
	}

	countUntil := time.Now().Add(dbCountBudget)
	for i := range l.Tables {
		t := &l.Tables[i]
		_ = query(ctx, db, func(rows *sql.Rows) error {
			for rows.Next() {
				var c DBColumn
				if err := rows.Scan(&c.Name, &c.Type); err != nil {
					return err
				}
				t.Columns = append(t.Columns, c)
			}
			return nil
		}, `SELECT name, type FROM pragma_table_info(?)`, t.Name)
		if t.Kind != "table" || time.Now().After(countUntil) {
			continue
		}
		_ = query(ctx, db, func(rows *sql.Rows) error {
			if rows.Next() {
				return rows.Scan(&t.Rows)
			}
			return nil
		}, `SELECT count(*) FROM `+quoteIdent(t.Name))
	}

	if table == "" {
		if len(l.Tables) == 0 {
			return l, nil
		}
		table = l.Tables[0].Name
	}
	var chosen *DBTable
	for i := range l.Tables {
		if l.Tables[i].Name == table {
			chosen = &l.Tables[i]
		}
	}
	// The name is one the database listed, or nothing runs. It is quoted
	// below as well; the two are separate guards, and a name with a quote in
	// it is a real table somebody can make.
	if chosen == nil {
		return nil, &DBError{Reason: fmt.Sprintf("there is no table or view called %q in this database", table), BadRequest: true}
	}
	l.Table = chosen.Name
	l.Columns = chosen.Columns
	if len(l.Columns) > dbMaxColumns {
		l.Columns = l.Columns[:dbMaxColumns]
		l.Note = fmt.Sprintf("Only the first %d of %d columns are shown.", dbMaxColumns, len(chosen.Columns))
	}
	if len(l.Columns) == 0 {
		return l, nil
	}
	// Two expressions a column: its type, and its value with a blob replaced
	// by its length and a text cut short, so what crosses from the driver is
	// bounded by this statement and not by the file.
	cells := make([]string, 0, 2*len(l.Columns))
	for _, c := range l.Columns {
		q := quoteIdent(c.Name)
		cells = append(cells, "typeof("+q+")",
			fmt.Sprintf("CASE typeof(%[1]s) WHEN 'blob' THEN length(%[1]s) WHEN 'text' THEN substr(%[1]s, 1, %[2]d) ELSE %[1]s END", q, dbMaxText+1))
	}
	stmt := "SELECT " + strings.Join(cells, ", ") + " FROM " + quoteIdent(l.Table) + " LIMIT " + strconv.Itoa(DBMaxRows+1)
	spent := 0
	err = query(ctx, db, func(rows *sql.Rows) error {
		vals := make([]any, 2*len(l.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if len(l.Rows) == DBMaxRows || spent > dbMaxBytes {
				l.More = true
				if spent > dbMaxBytes {
					l.Note = fmt.Sprintf("Only the first %d rows are shown: their text reached %d MB.", len(l.Rows), dbMaxBytes>>20)
				}
				return nil
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			row := make([]any, len(l.Columns))
			for i := range row {
				kind, _ := vals[2*i].(string)
				var n int
				row[i], n = dbCell(kind, vals[2*i+1])
				spent += n
			}
			l.Rows = append(l.Rows, row)
		}
		return nil
	}, stmt)
	if err != nil {
		return nil, dbFailure(err)
	}
	return l, nil
}

// query runs one statement under its own deadline and hands over the rows.
func query(ctx context.Context, db *sql.DB, each func(*sql.Rows) error, stmt string, args ...any) error {
	ctx, cancel := context.WithTimeout(ctx, dbStatementTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return timedOut(ctx, err)
	}
	defer rows.Close()
	if err := each(rows); err != nil {
		return timedOut(ctx, err)
	}
	return timedOut(ctx, rows.Err())
}

var errDBTimeout = errors.New("timeout")

func timedOut(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return errDBTimeout
	}
	return err
}

func dbFailure(err error) error {
	if errors.Is(err, errDBTimeout) {
		return &DBError{Reason: fmt.Sprintf("reading the database took longer than %v and was stopped", dbStatementTimeout)}
	}
	msg := err.Error()
	if strings.Contains(msg, "malformed") || strings.Contains(msg, "not a database") {
		return &DBError{Reason: "the database is damaged: " + msg}
	}
	return &DBError{Reason: "the database could not be read: " + msg}
}

// quoteIdent is an SQL identifier, quoted: inside double quotes, with each
// double quote doubled. Never a name pasted into a statement as it came.
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// dbText is a text value cut short, in the order the documentation gives.
type dbText struct {
	Text string `json:"text"`
	More bool   `json:"more"`
}

// dbCell is one value as the listing sends it, and how many bytes of text it
// carries.
func dbCell(kind string, v any) (any, int) {
	switch kind {
	case "null":
		return nil, 0
	case "blob":
		n, _ := v.(int64)
		return map[string]int64{"blob": n}, 0
	case "integer":
		n, _ := v.(int64)
		if n > 1<<53 || n < -(1<<53) {
			return map[string]string{"int": strconv.FormatInt(n, 10)}, 0
		}
		return n, 0
	case "real":
		f, _ := v.(float64)
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return map[string]string{"real": strconv.FormatFloat(f, 'g', -1, 64)}, 0
		}
		return f, 0
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case []byte:
		s = string(t)
	}
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) > dbMaxText {
		r := []rune(s)
		s = string(r[:dbMaxText])
		return dbText{Text: s, More: true}, len(s)
	}
	return s, len(s)
}
