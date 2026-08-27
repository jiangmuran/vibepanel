package store

import (
	"context"
	"database/sql"
	"fmt"
)

// MaxVncTargets bounds how many displays one panel may hold.
//
// The same reasoning as maxWebhooks: every row here is an address this process
// will open a TCP connection to when asked, so the list is a multiplier on how
// much a panel can be made to dial. Thirty-two is far past what anybody
// configures by hand for a machine they are sitting in front of.
const MaxVncTargets = 32

// VncTarget is one VNC display the panel knows how to reach.
//
// Panel-wide rather than per-project, and that is a judgement worth stating: a
// display is a fact about a machine, like the CPU meter, not about a
// repository. The same X server is where the browser under test in one project
// and the Electron app in another both draw, and filing it under one of them
// would mean adding it twice.
type VncTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	// ViewOnly drops input at the proxy. Stored, because the browser is not
	// where this can be decided -- see internal/vnc/proxy.go.
	ViewOnly bool `json:"viewOnly"`
	// Password is the VNC password, in the clear.
	//
	// `json:"-"` is the load-bearing part: this struct is what the API hands
	// back, and a password that has been typed once must not come back out of
	// the panel afterwards. HasPassword is what the settings page renders
	// instead, so the field that says "there is one" and the field that is one
	// are different fields and only one of them is ever marshalled.
	//
	// In the clear because there is nothing to encrypt it with. The panel has
	// no key that is not in the same 0600 file as the database, so a column
	// full of ciphertext next to its own key is theatre, and the honest
	// version is a sentence on screen saying what is stored. The protocol
	// makes the point moot anyway: a VNC password is eight bytes verified by
	// single-DES, so the database is not the weakest thing about it.
	Password    string `json:"-"`
	HasPassword bool   `json:"hasPassword"`
	CreatedAt   int64  `json:"createdAt"`
}

// ListVncTargets returns every display, oldest first.
func (d *DB) ListVncTargets(ctx context.Context) ([]VncTarget, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, name, host, port, view_only, password, created_at
		FROM vnc_targets ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list vnc targets: %w", err)
	}
	defer rows.Close()
	var out []VncTarget
	for rows.Next() {
		t, err := scanVncTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list vnc targets: %w", err)
	}
	return out, nil
}

// GetVncTarget returns one display, or ErrNotFound.
func (d *DB) GetVncTarget(ctx context.Context, id string) (VncTarget, error) {
	row := d.sql.QueryRowContext(ctx, `
		SELECT id, name, host, port, view_only, password, created_at
		FROM vnc_targets WHERE id = ?`, id)
	t, err := scanVncTarget(row)
	if err == sql.ErrNoRows {
		return VncTarget{}, ErrNotFound
	}
	if err != nil {
		return VncTarget{}, err
	}
	return t, nil
}

func scanVncTarget(s scanner) (VncTarget, error) {
	var t VncTarget
	var viewOnly int
	if err := s.Scan(&t.ID, &t.Name, &t.Host, &t.Port, &viewOnly, &t.Password, &t.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return VncTarget{}, err
		}
		return VncTarget{}, fmt.Errorf("store: read vnc target: %w", err)
	}
	t.ViewOnly = viewOnly != 0
	t.HasPassword = t.Password != ""
	return t, nil
}

// CreateVncTarget records a display.
func (d *DB) CreateVncTarget(ctx context.Context, t VncTarget) (VncTarget, error) {
	t.CreatedAt = now()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO vnc_targets (id, name, host, port, view_only, password, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Host, t.Port, boolInt(t.ViewOnly), t.Password, t.CreatedAt)
	if err != nil {
		return VncTarget{}, fmt.Errorf("store: create vnc target: %w", err)
	}
	t.HasPassword = t.Password != ""
	return t, nil
}

// UpdateVncTarget writes every field of an existing row.
//
// Whole-row rather than a set of optional columns, because the handler has
// already merged the change onto the row it read: keeping the merge in one
// place is what stops "the field the caller did not send" and "the field the
// caller cleared" from being decided in two.
func (d *DB) UpdateVncTarget(ctx context.Context, t VncTarget) (VncTarget, error) {
	res, err := d.sql.ExecContext(ctx, `
		UPDATE vnc_targets SET name = ?, host = ?, port = ?, view_only = ?, password = ?
		WHERE id = ?`,
		t.Name, t.Host, t.Port, boolInt(t.ViewOnly), t.Password, t.ID)
	if err != nil {
		return VncTarget{}, fmt.Errorf("store: update vnc target: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return VncTarget{}, ErrNotFound
	}
	t.HasPassword = t.Password != ""
	return t, nil
}

// DeleteVncTarget removes a display.
func (d *DB) DeleteVncTarget(ctx context.Context, id string) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM vnc_targets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete vnc target: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
