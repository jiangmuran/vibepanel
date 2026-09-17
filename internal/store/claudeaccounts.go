package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/session"
)

// Claude accounts: a name for a Claude Code configuration directory the panel
// manages, so a profile can start `claude` logged in as somebody other than
// whoever ~/.claude belongs to.
//
// The row holds nothing but the name and one decision. No credential, no
// email and no path: the login is written by Claude Code itself into the
// account's directory, the email is asked of `claude auth status` when the
// settings page is open, and the path is derived from the id
// (claudeaccount.Dir). Nothing in this table is worth reading to anybody who
// has the database and not the home directory.

// MaxClaudeAccounts bounds the table. Each one is a login somebody completed
// by hand in a terminal; a list longer than this is not a list of accounts.
const MaxClaudeAccounts = 32

// ClaudeAccount is one row.
type ClaudeAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Isolated keeps this account's conversations, prompt history and memory
	// out of the ones ~/.claude has, and theirs out of it.
	//
	// Fixed when the account is created. Turning it on later would leave the
	// account holding links to a history it is now meant not to see, and
	// turning it off would have to choose between the account's own
	// conversations and the shared ones for a directory that can only be one
	// of them. Both are answers somebody should give on purpose, by making a
	// new account.
	Isolated  bool  `json:"isolated"`
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// ValidateClaudeAccountName trims and checks a name.
func ValidateClaudeAccountName(name string) (string, error) {
	name = session.TruncateTitle(strings.TrimSpace(name))
	if name == "" {
		return "", fmt.Errorf("an account needs a name")
	}
	return name, nil
}

// CreateClaudeAccount inserts one at the caller's id.
func (d *DB) CreateClaudeAccount(ctx context.Context, id string, a ClaudeAccount) (ClaudeAccount, error) {
	a.ID = id
	a.CreatedAt = now()
	a.UpdatedAt = a.CreatedAt
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO claude_accounts (id, name, isolated, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		a.ID, a.Name, a.Isolated, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return ClaudeAccount{}, fmt.Errorf("store: insert claude account: %w", err)
	}
	return a, nil
}

// RenameClaudeAccount changes the name and nothing else. There is nothing else
// to change: see Isolated.
func (d *DB) RenameClaudeAccount(ctx context.Context, id, name string) error {
	return d.exec1(ctx,
		`UPDATE claude_accounts SET name = ?, updated_at = ? WHERE id = ?`,
		name, now(), id)
}

// DeleteClaudeAccount removes the row. The directory is the caller's to remove
// first; see claudeaccount.Remove for the order that matters.
func (d *DB) DeleteClaudeAccount(ctx context.Context, id string) error {
	return d.exec1(ctx, `DELETE FROM claude_accounts WHERE id = ?`, id)
}

// GetClaudeAccount returns one, or ErrNotFound.
func (d *DB) GetClaudeAccount(ctx context.Context, id string) (ClaudeAccount, error) {
	row := d.sql.QueryRowContext(ctx, `
		SELECT id, name, isolated, created_at, updated_at
		FROM claude_accounts WHERE id = ?`, id)
	return scanClaudeAccount(row)
}

// ListClaudeAccounts returns them by name.
func (d *DB) ListClaudeAccounts(ctx context.Context) ([]ClaudeAccount, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, name, isolated, created_at, updated_at
		FROM claude_accounts ORDER BY name COLLATE NOCASE ASC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list claude accounts: %w", err)
	}
	defer rows.Close()
	out := []ClaudeAccount{}
	for rows.Next() {
		a, err := scanClaudeAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountClaudeAccounts is for the cap.
func (d *DB) CountClaudeAccounts(ctx context.Context) (int, error) {
	var n int
	if err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM claude_accounts`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count claude accounts: %w", err)
	}
	return n, nil
}

// ClaudeAccountProfiles names the profiles that start with an account.
//
// Deleting an account out from under a profile would leave a picker entry that
// refuses to start, so the delete is refused instead and this is the list the
// refusal gives.
func (d *DB) ClaudeAccountProfiles(ctx context.Context, accountID string) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT name FROM launch_profiles WHERE claude_account_id = ?
		ORDER BY name COLLATE NOCASE ASC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: profiles for claude account: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ClaudeAccountSessions returns the sessions that were started under an
// account, archived or not. The caller decides which of them are alive.
func (d *DB) ClaudeAccountSessions(ctx context.Context, accountID string) ([]Session, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+sessionColumns+` `+sessionFrom+` WHERE s.claude_account_id = ?`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: sessions for claude account: %w", err)
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanClaudeAccount(row scanner) (ClaudeAccount, error) {
	var a ClaudeAccount
	err := row.Scan(&a.ID, &a.Name, &a.Isolated, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return ClaudeAccount{}, ErrNotFound
	}
	if err != nil {
		return ClaudeAccount{}, fmt.Errorf("store: scan claude account: %w", err)
	}
	return a, nil
}
