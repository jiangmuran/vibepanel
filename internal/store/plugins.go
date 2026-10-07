package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrExists is a plugin id that is already taken.
var ErrExists = errors.New("store: already exists")

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Plugins: docs/plugins.md. This file stores them and decides nothing about
// what they may contain: the manifest, the files and every limit are
// internal/plugins' to check before anything reaches here, and what a plugin
// may do is the capability table's, read by internal/httpapi.

// Plugin is one installed, imported or drafted plugin.
type Plugin struct {
	// ID is the manifest's id, the namespace everything else is keyed by.
	ID string `json:"id"`
	// Enabled is the switch. InstalledVersion is the version that runs; 0
	// is a plugin that has not been through the install screen.
	Enabled          bool `json:"enabled"`
	InstalledVersion int  `json:"installedVersion"`
	// SourceDir is a draft directory an agent is editing, for dev mode;
	// Dev says the draft is what runs rather than the installed version.
	SourceDir string `json:"sourceDir"`
	Dev       bool   `json:"dev"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// PluginVersion is one immutable copy of a plugin.
type PluginVersion struct {
	Version   int             `json:"version"`
	Manifest  json.RawMessage `json:"manifest"`
	Note      string          `json:"note"`
	Bytes     int64           `json:"bytes"`
	Files     int             `json:"files"`
	Hash      string          `json:"hash"`
	CreatedAt int64           `json:"createdAt"`
}

// NewPluginFile is one file handed to AddPluginVersion.
type NewPluginFile struct {
	Path        string
	ContentType string
	Data        []byte
}

// NewPluginVersion is what AddPluginVersion stores.
type NewPluginVersion struct {
	Manifest json.RawMessage
	Note     string
	Hash     string
	Files    []NewPluginFile
}

// PluginFile is one file of a version, without its bytes.
type PluginFile struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

const pluginColumns = `id, enabled, installed_version, source_dir, dev, created_at, updated_at`

func scanPlugin(row scanner) (Plugin, error) {
	var p Plugin
	err := row.Scan(&p.ID, &p.Enabled, &p.InstalledVersion, &p.SourceDir, &p.Dev, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// CreatePlugin records a plugin with no versions yet. ErrExists when the
// id is taken: a second arrival of the same id is an upgrade, which the
// caller does through AddPluginVersion.
func (d *DB) CreatePlugin(ctx context.Context, id, userID, sourceDir string) (Plugin, error) {
	n := now()
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO plugins (id, user_id, source_dir, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, id, userID, sourceDir, n, n)
	if err != nil {
		if isUnique(err) {
			return Plugin{}, ErrExists
		}
		return Plugin{}, fmt.Errorf("store: create plugin: %w", err)
	}
	return Plugin{ID: id, SourceDir: sourceDir, CreatedAt: n, UpdatedAt: n}, nil
}

// ListPlugins returns every plugin, by id.
func (d *DB) ListPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+pluginColumns+` FROM plugins ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list plugins: %w", err)
	}
	defer rows.Close()
	out := []Plugin{}
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan plugin: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PluginByID reads one plugin.
func (d *DB) PluginByID(ctx context.Context, id string) (Plugin, error) {
	p, err := scanPlugin(d.sql.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM plugins WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Plugin{}, ErrNotFound
	}
	if err != nil {
		return Plugin{}, fmt.Errorf("store: plugin by id: %w", err)
	}
	return p, nil
}

// PluginBySourceDir finds the plugin whose draft lives in dir.
func (d *DB) PluginBySourceDir(ctx context.Context, dir string) (Plugin, error) {
	p, err := scanPlugin(d.sql.QueryRowContext(ctx,
		`SELECT `+pluginColumns+` FROM plugins WHERE source_dir = ? AND source_dir != '' LIMIT 1`, dir))
	if errors.Is(err, sql.ErrNoRows) {
		return Plugin{}, ErrNotFound
	}
	if err != nil {
		return Plugin{}, fmt.Errorf("store: plugin by source: %w", err)
	}
	return p, nil
}

// PluginOwner is the account a plugin belongs to.
func (d *DB) PluginOwner(ctx context.Context, id string) (string, error) {
	var owner string
	err := d.sql.QueryRowContext(ctx, `SELECT user_id FROM plugins WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: plugin owner: %w", err)
	}
	return owner, nil
}

// UpdatePluginSource sets a plugin's draft directory and dev switch.
func (d *DB) UpdatePluginSource(ctx context.Context, id, sourceDir string, dev bool) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE plugins SET source_dir = ?, dev = ?, updated_at = ? WHERE id = ?`, sourceDir, dev, now(), id)
	if err != nil {
		return fmt.Errorf("store: update plugin source: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPluginEnabled flips the switch.
func (d *DB) SetPluginEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE plugins SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, now(), id)
	if err != nil {
		return fmt.Errorf("store: set plugin enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// InstallPluginVersion makes a version the one that runs, and sets the
// switch. The version must exist: a row pointing at a version with no files
// is a plugin that draws nothing.
func (d *DB) InstallPluginVersion(ctx context.Context, id string, version int, enabled bool) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: install plugin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_versions WHERE plugin_id = ? AND version = ?`,
		id, version).Scan(&exists); err != nil {
		return fmt.Errorf("store: install plugin: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE plugins SET installed_version = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		version, enabled, now(), id); err != nil {
		return fmt.Errorf("store: install plugin: %w", err)
	}
	return tx.Commit()
}

// DeletePlugin removes a plugin, its versions, grants, settings and secrets,
// and every blob nothing else references.
func (d *DB) DeletePlugin(ctx context.Context, id string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete plugin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	res, err := tx.ExecContext(ctx, `DELETE FROM plugins WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete plugin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := gcPageBlobs(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// AddPluginVersion stores the next version of a plugin and returns its
// number. One transaction for the row, its files and the blobs, so a publish
// that fails half way leaves no version with half its files.
func (d *DB) AddPluginVersion(ctx context.Context, id string, in NewPluginVersion) (int, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: add plugin version: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugins WHERE id = ?`, id).Scan(&exists); err != nil {
		return 0, fmt.Errorf("store: add plugin version: %w", err)
	}
	if exists == 0 {
		return 0, ErrNotFound
	}
	var version int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM plugin_versions WHERE plugin_id = ?`, id).Scan(&version); err != nil {
		return 0, fmt.Errorf("store: next plugin version: %w", err)
	}
	var total int64
	for _, f := range in.Files {
		total += int64(len(f.Data))
	}
	n := now()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO plugin_versions (plugin_id, version, manifest, note, bytes, files, hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, version, string(in.Manifest), in.Note, total, len(in.Files), in.Hash, n); err != nil {
		return 0, fmt.Errorf("store: insert plugin version: %w", err)
	}
	for _, f := range in.Files {
		sum := sha256.Sum256(f.Data)
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO share_page_blobs (sha256, data) VALUES (?, ?)`, sum[:], f.Data); err != nil {
			return 0, fmt.Errorf("store: insert plugin blob: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO plugin_files (plugin_id, version, path, content_type, sha256)
			VALUES (?, ?, ?, ?, ?)`, id, version, f.Path, f.ContentType, sum[:]); err != nil {
			return 0, fmt.Errorf("store: insert plugin file %s: %w", f.Path, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plugins SET updated_at = ? WHERE id = ?`, n, id); err != nil {
		return 0, fmt.Errorf("store: touch plugin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: add plugin version: %w", err)
	}
	return version, nil
}

const pluginVersionColumns = `version, manifest, note, bytes, files, hash, created_at`

func scanPluginVersion(row scanner) (PluginVersion, error) {
	var v PluginVersion
	var manifest string
	err := row.Scan(&v.Version, &manifest, &v.Note, &v.Bytes, &v.Files, &v.Hash, &v.CreatedAt)
	v.Manifest = json.RawMessage(manifest)
	return v, err
}

// ListPluginVersions returns a plugin's versions, newest first.
func (d *DB) ListPluginVersions(ctx context.Context, id string) ([]PluginVersion, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+pluginVersionColumns+` FROM plugin_versions WHERE plugin_id = ? ORDER BY version DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list plugin versions: %w", err)
	}
	defer rows.Close()
	out := []PluginVersion{}
	for rows.Next() {
		v, err := scanPluginVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan plugin version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// PluginVersionByNumber reads one version's row.
func (d *DB) PluginVersionByNumber(ctx context.Context, id string, version int) (PluginVersion, error) {
	v, err := scanPluginVersion(d.sql.QueryRowContext(ctx,
		`SELECT `+pluginVersionColumns+` FROM plugin_versions WHERE plugin_id = ? AND version = ?`, id, version))
	if errors.Is(err, sql.ErrNoRows) {
		return PluginVersion{}, ErrNotFound
	}
	if err != nil {
		return PluginVersion{}, fmt.Errorf("store: plugin version: %w", err)
	}
	return v, nil
}

// PluginFiles lists one version's files without their bytes.
func (d *DB) PluginFiles(ctx context.Context, id string, version int) ([]PluginFile, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT f.path, f.content_type, LENGTH(b.data)
		FROM plugin_files f JOIN share_page_blobs b ON b.sha256 = f.sha256
		WHERE f.plugin_id = ? AND f.version = ? ORDER BY f.path`, id, version)
	if err != nil {
		return nil, fmt.Errorf("store: list plugin files: %w", err)
	}
	defer rows.Close()
	out := []PluginFile{}
	for rows.Next() {
		var f PluginFile
		if err := rows.Scan(&f.Path, &f.ContentType, &f.Size); err != nil {
			return nil, fmt.Errorf("store: scan plugin file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PluginFileData reads one file of one version.
func (d *DB) PluginFileData(ctx context.Context, id string, version int, path string) (
	contentType string, data []byte, err error) {
	err = d.sql.QueryRowContext(ctx, `
		SELECT f.content_type, b.data
		FROM plugin_files f JOIN share_page_blobs b ON b.sha256 = f.sha256
		WHERE f.plugin_id = ? AND f.version = ? AND f.path = ?`, id, version, path).
		Scan(&contentType, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("store: plugin file: %w", err)
	}
	return contentType, data, nil
}

// SetPluginCaps replaces the grant decision for a plugin: exactly these
// capabilities, nothing else. One transaction, so a change of grants is
// never half applied.
func (d *DB) SetPluginCaps(ctx context.Context, id string, caps []string, by string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set plugin caps: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugins WHERE id = ?`, id).Scan(&exists); err != nil {
		return fmt.Errorf("store: set plugin caps: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_caps WHERE plugin_id = ?`, id); err != nil {
		return fmt.Errorf("store: set plugin caps: %w", err)
	}
	n := now()
	for _, c := range caps {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO plugin_caps (plugin_id, cap, granted_at, granted_by) VALUES (?, ?, ?, ?)`,
			id, c, n, by); err != nil {
			return fmt.Errorf("store: set plugin caps: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plugins SET updated_at = ? WHERE id = ?`, n, id); err != nil {
		return fmt.Errorf("store: set plugin caps: %w", err)
	}
	return tx.Commit()
}

// PluginCap is one granted capability.
type PluginCap struct {
	Cap       string `json:"cap"`
	GrantedAt int64  `json:"grantedAt"`
	GrantedBy string `json:"grantedBy"`
}

// PluginCaps reads a plugin's grant decision.
func (d *DB) PluginCaps(ctx context.Context, id string) ([]PluginCap, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT cap, granted_at, granted_by FROM plugin_caps WHERE plugin_id = ? ORDER BY cap`, id)
	if err != nil {
		return nil, fmt.Errorf("store: plugin caps: %w", err)
	}
	defer rows.Close()
	out := []PluginCap{}
	for rows.Next() {
		var c PluginCap
		if err := rows.Scan(&c.Cap, &c.GrantedAt, &c.GrantedBy); err != nil {
			return nil, fmt.Errorf("store: scan plugin cap: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PluginSettings reads every stored setting of a plugin, raw.
func (d *DB) PluginSettings(ctx context.Context, id string) (map[string]json.RawMessage, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT key, value FROM plugin_settings WHERE plugin_id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("store: plugin settings: %w", err)
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: scan plugin setting: %w", err)
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

// SetPluginSetting writes one setting.
func (d *DB) SetPluginSetting(ctx context.Context, id, key string, value json.RawMessage) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO plugin_settings (plugin_id, key, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(plugin_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		id, key, string(value), now())
	if err != nil {
		if isForeignKey(err) {
			return ErrNotFound
		}
		return fmt.Errorf("store: set plugin setting: %w", err)
	}
	return nil
}

// DeletePluginSetting puts a setting back to its default.
func (d *DB) DeletePluginSetting(ctx context.Context, id, key string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM plugin_settings WHERE plugin_id = ? AND key = ?`, id, key)
	if err != nil {
		return fmt.Errorf("store: delete plugin setting: %w", err)
	}
	return nil
}

// SetPluginSecret stores a sealed secret.
func (d *DB) SetPluginSecret(ctx context.Context, id, name string, sealed []byte) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO plugin_secrets (plugin_id, name, value_enc, set_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(plugin_id, name) DO UPDATE SET value_enc = excluded.value_enc, set_at = excluded.set_at`,
		id, name, sealed, now())
	if err != nil {
		if isForeignKey(err) {
			return ErrNotFound
		}
		return fmt.Errorf("store: set plugin secret: %w", err)
	}
	return nil
}

// PluginSecret reads one sealed secret.
func (d *DB) PluginSecret(ctx context.Context, id, name string) ([]byte, error) {
	var sealed []byte
	err := d.sql.QueryRowContext(ctx,
		`SELECT value_enc FROM plugin_secrets WHERE plugin_id = ? AND name = ?`, id, name).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: plugin secret: %w", err)
	}
	return sealed, nil
}

// PluginSecretNames lists which secrets are set, never their values.
func (d *DB) PluginSecretNames(ctx context.Context, id string) (map[string]int64, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT name, set_at FROM plugin_secrets WHERE plugin_id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("store: plugin secrets: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var at int64
		if err := rows.Scan(&name, &at); err != nil {
			return nil, fmt.Errorf("store: scan plugin secret: %w", err)
		}
		out[name] = at
	}
	return out, rows.Err()
}

// DeletePluginSecret forgets a secret.
func (d *DB) DeletePluginSecret(ctx context.Context, id, name string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM plugin_secrets WHERE plugin_id = ? AND name = ?`, id, name)
	if err != nil {
		return fmt.Errorf("store: delete plugin secret: %w", err)
	}
	return nil
}
