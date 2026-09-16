package store

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/jiangmuran/vibepanel/internal/secret"
)

// The hook token authorizes writes to every session's state, which makes it
// exactly the kind of value a copied database must not hand over. It lives
// sealed under the panel's secrets key, and the plaintext row it migrated
// from goes away.
func TestTheHookTokenIsSealedAtRest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	box, err := secret.Open(filepath.Join(dir, secret.KeyFile))
	if err != nil {
		t.Fatalf("secret.Open: %v", err)
	}
	db, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tok, err := db.HookToken(ctx, box)
	if err != nil {
		t.Fatalf("HookToken: %v", err)
	}
	legacy, _ := db.GetSetting(ctx, "hook_token", "")
	if legacy != "" {
		t.Fatalf("a plaintext hook_token row survived sealing: %q", legacy)
	}
	enc, err := db.GetSetting(ctx, "hook_token_sealed", "")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if enc == "" {
		t.Fatal("no sealed hook token row")
	}

	db.Close()
	again, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	got, err := again.HookToken(ctx, box)
	if err != nil {
		t.Fatalf("HookToken after reopen: %v", err)
	}
	if got != tok {
		t.Errorf("HookToken after reopen = %q, want the value sealed before", got)
	}
}

// Migration seals the value that is already there. Running sessions hold the
// token in their environment; generating a fresh one on migrate would reject
// every report from every session started before the change, silently,
// because the reporter swallows its own failures.
func TestHookTokenMigrationKeepsTheValueAndDropsThePlaintext(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	box, err := secret.Open(filepath.Join(dir, secret.KeyFile))
	if err != nil {
		t.Fatalf("secret.Open: %v", err)
	}
	db, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.SetSetting(ctx, "hook_token", "seeded-value"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	got, err := db.HookToken(ctx, box)
	if err != nil {
		t.Fatalf("HookToken: %v", err)
	}
	if got != "seeded-value" {
		t.Errorf("HookToken after migration = %q, want the value sessions hold", got)
	}
	legacy, _ := db.GetSetting(ctx, "hook_token", "")
	if legacy != "" {
		t.Errorf("the plaintext row survived the migration")
	}
	enc, _ := db.GetSetting(ctx, "hook_token_sealed", "")
	if enc == "" {
		t.Fatalf("no sealed row after migration")
	}
	out, err := box.Unseal(mustOpenSealed(t, enc), "hook-token")
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if string(out) != "seeded-value" {
		t.Errorf("sealed value = %q, want seeded-value", out)
	}
}

func mustOpenSealed(t *testing.T, enc string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	return raw
}

// PeekHookToken is what `doctor` reads, and a diagnostic must not mint a
// credential as a side effect of diagnosing.
func TestPeekHookTokenCreatesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	box, err := secret.Open(filepath.Join(dir, secret.KeyFile))
	if err != nil {
		t.Fatalf("secret.Open: %v", err)
	}
	db, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	got, err := db.PeekHookToken(ctx, box)
	if err != nil {
		t.Fatalf("PeekHookToken: %v", err)
	}
	if got != "" {
		t.Errorf("PeekHookToken on a fresh database = %q, want empty", got)
	}
	for _, key := range []string{"hook_token", "hook_token_sealed"} {
		if v, _ := db.GetSetting(ctx, key, ""); v != "" {
			t.Errorf("%s = %q after a peek; a peek must not create the token", key, v)
		}
	}
}
