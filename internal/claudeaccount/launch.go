package claudeaccount

import (
	"context"

	"github.com/jiangmuran/vibepanel/internal/store"
)

// PrepareByID looks an account up, prepares its directory and returns the
// variables that start a process under it.
//
// Here rather than in the HTTP layer because `vibepanel session new` starts
// sessions without one, and that path has drifted from the HTTP path before --
// it once built its own environment and left out the hook token. Both call
// this.
//
// A missing account is store.ErrNotFound, unwrapped, so each caller can say
// what it means where it is.
func PrepareByID(ctx context.Context, db *store.DB, dataDir string, main Main, accountID string) ([]string, error) {
	a, err := db.GetClaudeAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	dir, err := Dir(dataDir, a.ID)
	if err != nil {
		return nil, err
	}
	if _, err := Prepare(dir, main, a.Isolated); err != nil {
		return nil, err
	}
	return Env(dir), nil
}
