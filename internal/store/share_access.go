package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jiangmuran/vibepanel/internal/secret"
)

// ShareAccessSummary is the per-link aggregate attached to listed share links.
type ShareAccessSummary struct {
	UniqueIPs       int
	VisitCount      int
	UnlockFailCount int
	LastAccessIP    string
}

// ShareAccessIP is one distinct visitor IP recorded against a share link.
type ShareAccessIP struct {
	IP              string `json:"ip"`
	FirstSeenAt     int64  `json:"firstSeenAt"`
	LastSeenAt      int64  `json:"lastSeenAt"`
	VisitCount      int    `json:"visitCount"`
	UnlockOKCount   int    `json:"unlockOkCount"`
	UnlockFailCount int    `json:"unlockFailCount"`
	LastUA          string `json:"lastUa"`
	LastEvent       string `json:"lastEvent"`
}

// ShareAccessEvent is one chronological access record for a share link.
type ShareAccessEvent struct {
	ID     int64  `json:"id"`
	At     int64  `json:"at"`
	IP     string `json:"ip"`
	Event  string `json:"event"`
	UA     string `json:"ua"`
	Detail string `json:"detail"`
}

// ShareLinkAuth holds the internal credential fields needed to verify a link's
// password and mint/validate unlock grants. Kept out of ShareLink so no list
// or settings route can serialize password hashes or token hashes by accident.
type ShareLinkAuth struct {
	Link         ShareLink
	TokenHash    []byte
	PasswordHash string
}

// ShareLinkAuthByID looks up an active (unexpired, handed-out) link along with
// its token hash and per-link password hash.
func (d *DB) ShareLinkAuthByID(ctx context.Context, id string) (ShareLinkAuth, error) {
	var out ShareLinkAuth
	var params string
	err := d.sql.QueryRowContext(ctx, `
		SELECT `+shareLinkColumns+`, token_hash, password_hash
		FROM share_links
		WHERE id = ? AND purpose = '' AND (expires_at = 0 OR expires_at > ?)`,
		id, now()).Scan(
		&out.Link.ID, &out.Link.Prefix, &out.Link.Name, &out.Link.Detail, &out.Link.Scope, &out.Link.ScopeID,
		&out.Link.ExpiresAt, &out.Link.CreatedAt, &out.Link.LastUsedAt, &out.Link.Remark, &out.Link.Locked,
		&out.Link.PageID, &out.Link.PinVersion, &out.Link.PinUntil, &params, &out.Link.Purpose, &out.Link.Interactive,
		&out.Link.Copyable, &out.Link.AccessMode, &out.Link.PasswordEpoch, &out.Link.PasswordViewable,
		&out.TokenHash, &out.PasswordHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ShareLinkAuth{}, ErrNotFound
	}
	if err != nil {
		return ShareLinkAuth{}, fmt.Errorf("store: share link auth by id: %w", err)
	}
	out.Link.Params = decodeParams(params)
	return out, nil
}

// ShareLinkPasswordEnc reads a custom-protected link's sealed password copy so
// the signed-in owner can view or copy it again.
func (d *DB) ShareLinkPasswordEnc(ctx context.Context, id string) ([]byte, error) {
	var mode string
	var enc []byte
	err := d.sql.QueryRowContext(ctx,
		`SELECT access_mode, password_enc FROM share_links WHERE id = ? AND purpose = ''`, id).Scan(&mode, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: share link password enc: %w", err)
	}
	if mode != string(ShareAccessCustom) {
		return nil, nil
	}
	return enc, nil
}

// SetShareLinkAccess updates a handed-out link's password protection mode and
// bumps password_epoch so every existing unlock cookie and grant for this link
// stops working immediately.
//
// When mode is ShareAccessCustom and keepExistingPassword is true, the stored
// password_hash and password_enc are preserved (used when keeping a custom
// password while revoking sessions); otherwise the supplied hash and sealed
// copy replace them.
func (d *DB) SetShareLinkAccess(ctx context.Context, id string, mode ShareAccessMode,
	passwordHash string, passwordEnc []byte, keepExistingPassword bool) error {
	if !ValidShareAccessMode(mode) {
		return fmt.Errorf("store: invalid share access mode %q", mode)
	}
	var res sql.Result
	var err error
	if mode == ShareAccessCustom && keepExistingPassword {
		res, err = d.sql.ExecContext(ctx, `
			UPDATE share_links
			SET access_mode = ?, password_epoch = password_epoch + 1
			WHERE id = ? AND purpose = '' AND password_hash != ''`,
			string(mode), id)
	} else {
		if mode != ShareAccessCustom {
			passwordHash = ""
			passwordEnc = nil
		}
		res, err = d.sql.ExecContext(ctx, `
			UPDATE share_links
			SET access_mode = ?, password_hash = ?, password_enc = ?, password_epoch = password_epoch + 1
			WHERE id = ? AND purpose = ''`,
			string(mode), passwordHash, nonNilBytes(passwordEnc), id)
	}
	if err != nil {
		return fmt.Errorf("store: set share link access: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpShareLinkPasswordEpoch increments a link's password_epoch so all
// currently unlocked cookies and grants for that link must re-authenticate.
func (d *DB) BumpShareLinkPasswordEpoch(ctx context.Context, id string) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE share_links SET password_epoch = password_epoch + 1 WHERE id = ? AND purpose = ''`, id)
	if err != nil {
		return fmt.Errorf("store: bump share link password epoch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplySharedPasswordToPublicLinks switches every handed-out public link
// (access_mode = 'none') onto the panel-wide shared password in one statement.
func (d *DB) ApplySharedPasswordToPublicLinks(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `
		UPDATE share_links
		SET access_mode = 'shared', password_hash = '', password_enc = x'', password_epoch = password_epoch + 1
		WHERE purpose = '' AND access_mode = 'none'`)
	if err != nil {
		return 0, fmt.Errorf("store: apply shared password to public links: %w", err)
	}
	return res.RowsAffected()
}

// CountShareLinksByAccessMode counts handed-out links with the given mode.
func (d *DB) CountShareLinksByAccessMode(ctx context.Context, mode ShareAccessMode) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM share_links WHERE purpose = '' AND access_mode = ?`, string(mode)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count share links by access mode: %w", err)
	}
	return n, nil
}

const (
	shareUnlockSecretSetting       = "share_unlock_secret"
	shareUnlockSecretSealedSetting = "share_unlock_secret_sealed"
	shareUnlockSecretSealContext   = "share-unlock-secret"
)

// EnsureShareUnlockSecret returns the 32-byte HMAC key used to sign share
// unlock cookies and short-lived sandbox grants, creating and persisting it on
// first use. Sealed under box when available, matching HookToken.
func (d *DB) EnsureShareUnlockSecret(ctx context.Context, box *secret.Box) ([]byte, error) {
	if box != nil {
		enc, err := d.GetSetting(ctx, shareUnlockSecretSealedSetting, "")
		if err != nil {
			return nil, err
		}
		if enc != "" {
			if raw, derr := base64.StdEncoding.DecodeString(enc); derr == nil {
				if out, uerr := box.Unseal(raw, shareUnlockSecretSealContext); uerr == nil && len(out) == 32 {
					return out, nil
				}
			}
		}
	}
	legacy, err := d.GetSetting(ctx, shareUnlockSecretSetting, "")
	if err != nil {
		return nil, err
	}
	if legacy != "" {
		if raw, derr := base64.StdEncoding.DecodeString(legacy); derr == nil && len(raw) == 32 {
			if box != nil {
				_ = d.storeShareUnlockSecret(ctx, box, raw)
			}
			return raw, nil
		}
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("store: generate share unlock secret: %w", err)
	}
	if err := d.storeShareUnlockSecret(ctx, box, key); err != nil {
		return nil, err
	}
	return key, nil
}

func (d *DB) storeShareUnlockSecret(ctx context.Context, box *secret.Box, key []byte) error {
	if box == nil {
		return d.SetSetting(ctx, shareUnlockSecretSetting, base64.StdEncoding.EncodeToString(key))
	}
	sealed := base64.StdEncoding.EncodeToString(box.Seal(key, shareUnlockSecretSealContext))
	if err := d.SetSetting(ctx, shareUnlockSecretSealedSetting, sealed); err != nil {
		return err
	}
	_, err := d.sql.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, shareUnlockSecretSetting)
	if err != nil {
		return fmt.Errorf("store: remove plaintext share unlock secret: %w", err)
	}
	return nil
}

// RecordShareAccess updates the per-IP aggregate row for a share link and,
// when recordEvent is true, appends a chronological event row.
//
// Supported event kinds:
//   - "visit": increments visit_count
//   - "unlock_ok": increments unlock_ok_count
//   - "unlock_fail": increments unlock_fail_count
//   - "action": updates last_seen_at / last_event without bumping visit_count
//   - "poll": updates last_seen_at (and last_ua if non-empty) without writing an event
func (d *DB) RecordShareAccess(ctx context.Context, linkID, ip, event, ua, detail string, recordEvent bool) error {
	if linkID == "" || ip == "" {
		return nil
	}
	ts := now()
	var dVisit, dOK, dFail int
	switch event {
	case "visit":
		dVisit = 1
	case "unlock_ok":
		dOK = 1
	case "unlock_fail":
		dFail = 1
	}
	ua = truncateRunes(strings.TrimSpace(ua), 80)
	detail = truncateRunes(strings.TrimSpace(detail), 160)

	// Keep the previous meaningful last_event when a background poll refreshes
	// last_seen_at so a link's IP table does not turn every row into "poll".
	eventForRow := event
	if event == "poll" {
		eventForRow = ""
	}
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO share_access_ips
			(link_id, ip, first_seen_at, last_seen_at, visit_count, unlock_ok_count, unlock_fail_count, last_ua, last_event)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(link_id, ip) DO UPDATE SET
			last_seen_at      = excluded.last_seen_at,
			visit_count       = share_access_ips.visit_count + excluded.visit_count,
			unlock_ok_count   = share_access_ips.unlock_ok_count + excluded.unlock_ok_count,
			unlock_fail_count = share_access_ips.unlock_fail_count + excluded.unlock_fail_count,
			last_ua           = CASE WHEN excluded.last_ua != '' THEN excluded.last_ua ELSE share_access_ips.last_ua END,
			last_event        = CASE WHEN excluded.last_event != '' THEN excluded.last_event ELSE share_access_ips.last_event END`,
		linkID, ip, ts, ts, dVisit, dOK, dFail, ua, eventForRow)
	if err != nil {
		return fmt.Errorf("store: record share access ip: %w", err)
	}
	if recordEvent && event != "poll" {
		if _, err := d.sql.ExecContext(ctx, `
			INSERT INTO share_access_events (link_id, at, ip, event, ua, detail)
			VALUES (?, ?, ?, ?, ?, ?)`,
			linkID, ts, ip, event, ua, detail); err != nil {
			return fmt.Errorf("store: record share access event: %w", err)
		}
	}
	return nil
}

// TrimShareAccessEvents bounds share_access_events by age and total row count
// so long-running links and brute-force probes cannot fill the disk.
func (d *DB) TrimShareAccessEvents(ctx context.Context, maxAgeSeconds int64, maxRows int) error {
	if maxAgeSeconds > 0 {
		if _, err := d.sql.ExecContext(ctx,
			`DELETE FROM share_access_events WHERE at < ?`, now()-maxAgeSeconds); err != nil {
			return fmt.Errorf("store: trim share access events by age: %w", err)
		}
	}
	if maxRows > 0 {
		if _, err := d.sql.ExecContext(ctx, `
			DELETE FROM share_access_events
			WHERE id <= (
				SELECT id FROM share_access_events
				ORDER BY id DESC
				LIMIT 1 OFFSET ?
			)`, maxRows); err != nil {
			return fmt.Errorf("store: trim share access events by count: %w", err)
		}
	}
	return nil
}

// ShareAccessSummaries returns aggregate IP and visit metrics for every link
// that has recorded access.
func (d *DB) ShareAccessSummaries(ctx context.Context) (map[string]ShareAccessSummary, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT
			a.link_id,
			COUNT(*) AS unique_ips,
			COALESCE(SUM(a.visit_count), 0) AS visit_count,
			COALESCE(SUM(a.unlock_fail_count), 0) AS unlock_fail_count,
			COALESCE((
				SELECT b.ip FROM share_access_ips b
				WHERE b.link_id = a.link_id
				ORDER BY b.last_seen_at DESC, b.ip ASC
				LIMIT 1
			), '') AS last_ip
		FROM share_access_ips a
		GROUP BY a.link_id`)
	if err != nil {
		return nil, fmt.Errorf("store: share access summaries: %w", err)
	}
	defer rows.Close()
	out := map[string]ShareAccessSummary{}
	for rows.Next() {
		var linkID string
		var s ShareAccessSummary
		if err := rows.Scan(&linkID, &s.UniqueIPs, &s.VisitCount, &s.UnlockFailCount, &s.LastAccessIP); err != nil {
			return nil, fmt.Errorf("store: scan share access summary: %w", err)
		}
		out[linkID] = s
	}
	return out, rows.Err()
}

// ShareAccessIPs lists distinct IPs recorded for one share link, newest first.
func (d *DB) ShareAccessIPs(ctx context.Context, linkID string, limit int) ([]ShareAccessIP, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := d.sql.QueryContext(ctx, `
		SELECT ip, first_seen_at, last_seen_at, visit_count, unlock_ok_count, unlock_fail_count, last_ua, last_event
		FROM share_access_ips
		WHERE link_id = ?
		ORDER BY last_seen_at DESC, visit_count DESC
		LIMIT ?`, linkID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: share access ips: %w", err)
	}
	defer rows.Close()
	out := []ShareAccessIP{}
	for rows.Next() {
		var r ShareAccessIP
		if err := rows.Scan(&r.IP, &r.FirstSeenAt, &r.LastSeenAt, &r.VisitCount,
			&r.UnlockOKCount, &r.UnlockFailCount, &r.LastUA, &r.LastEvent); err != nil {
			return nil, fmt.Errorf("store: scan share access ip: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ShareAccessEvents lists recent chronological access events for one share
// link, newest first.
func (d *DB) ShareAccessEvents(ctx context.Context, linkID string, limit int) ([]ShareAccessEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, at, ip, event, ua, detail
		FROM share_access_events
		WHERE link_id = ?
		ORDER BY at DESC, id DESC
		LIMIT ?`, linkID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: share access events: %w", err)
	}
	defer rows.Close()
	out := []ShareAccessEvent{}
	for rows.Next() {
		var e ShareAccessEvent
		if err := rows.Scan(&e.ID, &e.At, &e.IP, &e.Event, &e.UA, &e.Detail); err != nil {
			return nil, fmt.Errorf("store: scan share access event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HasOnlyLoopbackShareAccess reports whether recent share access has been
// recorded exclusively from loopback addresses (127.0.0.1 / ::1), which is
// the signature of a reverse proxy that is not forwarding client IPs.
func (d *DB) HasOnlyLoopbackShareAccess(ctx context.Context, since int64) (bool, error) {
	var total, nonLoopback int
	err := d.sql.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN ip NOT IN ('127.0.0.1', '::1', '') THEN 1 ELSE 0 END), 0)
		FROM share_access_ips
		WHERE last_seen_at >= ?`, since).Scan(&total, &nonLoopback)
	if err != nil {
		return false, fmt.Errorf("store: check loopback share access: %w", err)
	}
	return total > 0 && nonLoopback == 0, nil
}
