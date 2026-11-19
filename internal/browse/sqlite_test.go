package browse

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// makeDB writes a database with the given statements, closes it, and returns
// its path. Written through the same driver, the ordinary way, so the file is
// one a program would leave behind.
func makeDB(t *testing.T, dir, name string, stmts ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// One connection, so a BEGIN and what follows it are one transaction.
	db.SetMaxOpenConns(1)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func dirState(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		info, _ := e.Info()
		out = append(out, fmt.Sprintf("%s %v %d", e.Name(), info.ModTime(), info.Size()))
	}
	return out
}

func TestADatabaseIsListedWithItsFirstRows(t *testing.T) {
	dir := t.TempDir()
	path := makeDB(t, dir, "app.db",
		`CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT, photo BLOB, score REAL, big INTEGER, anything)`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 250)
		 INSERT INTO people (name, score) SELECT 'p' || i, i / 2.0 FROM n`,
		`UPDATE people SET photo = zeroblob(4096), big = 1152921504606846976, anything = NULL, name = printf('%.5000c', 'x'), score = 1e999 WHERE id = 1`,
		`CREATE TABLE empty (a TEXT)`,
		`CREATE VIEW grown AS SELECT name FROM people WHERE score > 100`)
	l, err := ReadDatabase(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tb := range l.Tables {
		names = append(names, tb.Name+":"+tb.Kind)
	}
	if strings.Join(names, " ") != "empty:table grown:view people:table" {
		t.Fatalf("tables %v", names)
	}
	people := l.Tables[2]
	if people.Rows != 250 || len(people.Columns) != 6 || people.Columns[1].Name != "name" || people.Columns[1].Type != "TEXT" ||
		!strings.HasPrefix(people.SQL, "CREATE TABLE people") {
		t.Errorf("people %+v", people)
	}
	if l.Tables[1].Rows != -1 {
		t.Errorf("a view was counted: %d", l.Tables[1].Rows)
	}
	// The first table by name, and it is empty.
	if l.Table != "empty" || len(l.Rows) != 0 || l.More {
		t.Errorf("first table %q, %d rows, more=%v", l.Table, len(l.Rows), l.More)
	}

	l, err = ReadDatabase(context.Background(), path, "people")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Rows) != DBMaxRows || !l.More {
		t.Fatalf("%d rows, more=%v", len(l.Rows), l.More)
	}
	b, _ := json.Marshal(l.Rows[0])
	want := `[1,{"text":"` + strings.Repeat("x", dbMaxText) + `","more":true},{"blob":4096},{"real":"+Inf"},{"int":"1152921504606846976"},null]`
	if string(b) != want {
		t.Errorf("row 1 is\n%s\nwant\n%s", b, want)
	}
	b, _ = json.Marshal(l.Rows[1])
	if string(b) != `[2,"p2",null,1,null,null]` {
		t.Errorf("row 2 is %s", b)
	}
}

// A trigger fires on a write, and nothing here writes: the database with one
// is listed like any other and is the same file afterwards, byte for byte.
func TestADatabaseWithATriggerIsReadAndNotChanged(t *testing.T) {
	dir := t.TempDir()
	path := makeDB(t, dir, "trig.db",
		`CREATE TABLE t (a TEXT)`, `CREATE TABLE log (msg TEXT)`,
		`CREATE TRIGGER t_ins AFTER INSERT ON t BEGIN INSERT INTO log VALUES ('fired'); END`,
		`INSERT INTO t VALUES ('one')`)
	before := fileSum(t, path)
	l, err := ReadDatabase(context.Background(), path, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Tables) != 2 || len(l.Rows) != 1 {
		t.Errorf("%+v", l)
	}
	if fileSum(t, path) != before {
		t.Error("reading the database changed it")
	}
}

// The one name a request supplies is matched against the database's own list
// before anything runs: a name that is not a table, an injection, a PRAGMA.
// Each is a 400 with a sentence, and the file is unchanged.
func TestOnlyATableTheDatabaseListsIsRead(t *testing.T) {
	dir := t.TempDir()
	path := makeDB(t, dir, "x.db", `CREATE TABLE t (a TEXT)`, `INSERT INTO t VALUES ('kept')`)
	before := fileSum(t, path)
	for _, name := range []string{
		"nosuch",
		`t"; DROP TABLE t; --`,
		"t; DELETE FROM t",
		"PRAGMA writable_schema=1",
		"sqlite_schema",
		"pragma_table_info('t')",
	} {
		_, err := ReadDatabase(context.Background(), path, name)
		var de *DBError
		if !errors.As(err, &de) || !de.BadRequest || !strings.Contains(de.Reason, "no table or view called") {
			t.Errorf("%q: %v, want a 400 naming the table", name, err)
		}
	}
	if fileSum(t, path) != before {
		t.Error("a refused name changed the database")
	}
}

// A table whose name has a double quote in it is a real table, and it is read
// -- because the name is quoted, not pasted. The list check alone would let
// it through and then the statement would break on it.
func TestATableNameIsQuotedNotPasted(t *testing.T) {
	dir := t.TempDir()
	path := makeDB(t, dir, "q.db", `CREATE TABLE "we""ird; x" ("col""umn" TEXT)`, `INSERT INTO "we""ird; x" VALUES ('found')`)
	l, err := ReadDatabase(context.Background(), path, `we"ird; x`)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Rows) != 1 || l.Rows[0][0] != "found" || l.Tables[0].Rows != 1 {
		t.Errorf("%+v", l)
	}
}

// Every statement has a deadline. A view that recurses forever and never
// yields a row is stopped, with a sentence, in about the deadline.
func TestAStatementIsStoppedAtItsDeadline(t *testing.T) {
	old := dbStatementTimeout
	dbStatementTimeout = 300 * time.Millisecond
	t.Cleanup(func() { dbStatementTimeout = old })
	dir := t.TempDir()
	path := makeDB(t, dir, "slow.db", `CREATE TABLE a (x)`,
		`CREATE VIEW forever AS WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT x FROM c WHERE x < 0`)
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := ReadDatabase(context.Background(), path, "forever")
		done <- err
	}()
	select {
	case err := <-done:
		var de *DBError
		if !errors.As(err, &de) || !strings.Contains(de.Reason, "longer than") {
			t.Errorf("%v, want stopped at the deadline", err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("took %v", d)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a statement ran with no deadline")
	}
}

// immutable=1: nothing appears beside the database. A read-only connection
// to a WAL database otherwise makes its -shm in the project directory.
func TestReadingADatabaseLeavesNoFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := makeDB(t, dir, "wal.db", `PRAGMA journal_mode=WAL`, `CREATE TABLE t (a)`, `INSERT INTO t VALUES (1)`)
	before := dirState(t, dir)
	if _, err := ReadDatabase(context.Background(), path, ""); err != nil {
		t.Fatal(err)
	}
	if after := dirState(t, dir); !slices.Equal(before, after) {
		t.Errorf("the directory was\n%q\nand is\n%q", before, after)
	}
}

// A file with the magic and nothing a database has after it, and one whose
// pages are noise: refused with a sentence, not a 400 -- the file is at fault,
// not the request.
func TestADamagedDatabaseIsRefusedWithASentence(t *testing.T) {
	dir := t.TempDir()
	good, _ := os.ReadFile(makeDB(t, dir, "good.db", `CREATE TABLE t (a)`, `INSERT INTO t VALUES (1)`))
	noise := append([]byte{}, good...)
	for i := 100; i < len(noise); i++ {
		noise[i] = byte(i * 7)
	}
	for name, b := range map[string][]byte{
		"header only": append([]byte("SQLite format 3\x00"), make([]byte, 2032)...),
		"noise pages": noise,
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ReadDatabase(context.Background(), p, "")
		var de *DBError
		if !errors.As(err, &de) || de.BadRequest || de.Reason == "" {
			t.Errorf("%s: %v, want a DBError about the file", name, err)
		}
	}
}

// The bounds on one answer: two hundred columns, four megabytes of text
// across the rows, a thousand tables, and a time for counting all of them.
func TestADatabaseAnswerIsBounded(t *testing.T) {
	dir := t.TempDir()
	cols := make([]string, dbMaxColumns+50)
	for i := range cols {
		cols[i] = fmt.Sprintf("c%d TEXT", i)
	}
	wide := makeDB(t, dir, "wide.db", `CREATE TABLE w (`+strings.Join(cols, ", ")+`)`, `INSERT INTO w (c0) VALUES ('x')`)
	l, err := ReadDatabase(context.Background(), wide, "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Columns) != dbMaxColumns || len(l.Rows[0]) != dbMaxColumns || !strings.Contains(l.Note, "columns") {
		t.Errorf("%d columns, %d cells, note %q", len(l.Columns), len(l.Rows[0]), l.Note)
	}

	// Thirty columns of a thousand characters is 30 KB a row; the text
	// bound arrives before the row bound does.
	text := make([]string, 30)
	set := make([]string, 30)
	for i := range text {
		text[i] = fmt.Sprintf("t%d TEXT", i)
		set[i] = fmt.Sprintf("printf('%%.%dc', 'y')", dbMaxText)
	}
	heavy := makeDB(t, dir, "heavy.db", `CREATE TABLE h (`+strings.Join(text, ", ")+`)`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 250)
		 INSERT INTO h SELECT `+strings.Join(set, ", ")+` FROM n`)
	l, err = ReadDatabase(context.Background(), heavy, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Rows) >= DBMaxRows || !l.More || !strings.Contains(l.Note, "MB") {
		t.Errorf("%d rows, more=%v, note %q", len(l.Rows), l.More, l.Note)
	}

	stmts := []string{"BEGIN"}
	for i := 0; i < dbMaxTables+5; i++ {
		stmts = append(stmts, fmt.Sprintf("CREATE TABLE t%04d (a)", i))
	}
	stmts = append(stmts, "COMMIT")
	many := makeDB(t, dir, "many.db", stmts...)
	old := dbCountBudget
	dbCountBudget = 0
	t.Cleanup(func() { dbCountBudget = old })
	l, err = ReadDatabase(context.Background(), many, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Tables) != dbMaxTables || !strings.Contains(l.Note, "tables") {
		t.Errorf("%d tables, note %q", len(l.Tables), l.Note)
	}
	// No time to count: every table says so rather than saying zero.
	for _, tb := range l.Tables {
		if tb.Rows != -1 {
			t.Fatalf("%s counted %d rows with no time to count", tb.Name, tb.Rows)
		}
	}
}
