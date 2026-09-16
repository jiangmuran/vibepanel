package store

import "testing"

// idx_sessions_state was created with the sessions table and had no reader:
// nothing queries sessions by state. It was maintained on every state change
// -- the panel's hottest write -- for nothing, and the migration drops it from
// databases that already carry it, which this database is one of.
func TestTheSessionsTableCarriesNoStateIndex(t *testing.T) {
	db := openTest(t)
	var n int
	if err := db.sql.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_sessions_state'`,
	).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if n != 0 {
		t.Fatal("idx_sessions_state exists; it is maintained on every state change and read by nothing")
	}
}
