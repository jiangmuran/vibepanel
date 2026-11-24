package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/secret"
)

func TestShareLinkAccessAndAnalytics(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	u, err := db.CreateUser(ctx, "u1", "owner", "hash")
	if err != nil {
		t.Fatal(err)
	}

	// Default access mode is 'none'.
	l1, err := db.CreateShareLink(ctx, NewShareLink{
		ID: "l1", TokenHash: []byte("h1"), Prefix: "p1", Name: "Public",
		Detail: ShareCounts, Scope: ShareWhole, UserID: u.ID, PageID: "pg1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if l1.AccessMode != string(ShareAccessNone) || l1.PasswordEpoch != 1 || l1.PasswordViewable {
		t.Fatalf("unexpected default link: %+v", l1)
	}

	// Create a custom-password link.
	l2, err := db.CreateShareLink(ctx, NewShareLink{
		ID: "l2", TokenHash: []byte("h2"), Prefix: "p2", Name: "Private",
		Detail: ShareCounts, Scope: ShareWhole, UserID: u.ID, PageID: "pg1",
		AccessMode: ShareAccessCustom, PasswordHash: "argon-hash", PasswordEnc: []byte("sealed-pw"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if l2.AccessMode != string(ShareAccessCustom) || !l2.PasswordViewable {
		t.Fatalf("unexpected custom link: %+v", l2)
	}

	authRec, err := db.ShareLinkAuthByID(ctx, "l2")
	if err != nil {
		t.Fatal(err)
	}
	if authRec.PasswordHash != "argon-hash" || string(authRec.TokenHash) != "h2" {
		t.Fatalf("auth lookup mismatch: %+v", authRec)
	}
	enc, err := db.ShareLinkPasswordEnc(ctx, "l2")
	if err != nil || string(enc) != "sealed-pw" {
		t.Fatalf("password enc = %q %v", enc, err)
	}

	// Rotate bumps password_epoch.
	if err := db.RotateShareLink(ctx, "l2", []byte("h2-new"), []byte("tok-enc"), "p2n"); err != nil {
		t.Fatal(err)
	}
	afterRotate, err := db.ShareLinkByID(ctx, "l2")
	if err != nil {
		t.Fatal(err)
	}
	if afterRotate.PasswordEpoch != 2 {
		t.Fatalf("rotate did not bump password_epoch: got %d, want 2", afterRotate.PasswordEpoch)
	}

	// Apply shared password to all public links: touches l1, leaves l2 alone.
	n, err := db.ApplySharedPasswordToPublicLinks(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ApplySharedPasswordToPublicLinks = %d %v, want 1", n, err)
	}
	l1After, _ := db.ShareLinkByID(ctx, "l1")
	l2After, _ := db.ShareLinkByID(ctx, "l2")
	if l1After.AccessMode != string(ShareAccessShared) || l2After.AccessMode != string(ShareAccessCustom) {
		t.Fatalf("after batch apply: l1=%s l2=%s", l1After.AccessMode, l2After.AccessMode)
	}
	if cnt, err := db.CountShareLinksByAccessMode(ctx, ShareAccessShared); err != nil || cnt != 1 {
		t.Fatalf("count shared = %d %v, want 1", cnt, err)
	}

	// Record IP accesses and verify summaries, loopback detection, and trimming.
	if err := db.RecordShareAccess(ctx, "l1", "127.0.0.1", "visit", "Chrome · macOS", "", true); err != nil {
		t.Fatal(err)
	}
	onlyLoopback, err := db.HasOnlyLoopbackShareAccess(ctx, time.Now().Add(-time.Hour).Unix())
	if err != nil || !onlyLoopback {
		t.Fatalf("expected loopback-only warning when only 127.0.0.1 visited: %v %v", onlyLoopback, err)
	}

	if err := db.RecordShareAccess(ctx, "l1", "203.0.113.10", "unlock_fail", "Safari · iOS", "wrong password", true); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordShareAccess(ctx, "l1", "203.0.113.10", "unlock_ok", "Safari · iOS", "", true); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordShareAccess(ctx, "l1", "203.0.113.10", "visit", "Safari · iOS", "", true); err != nil {
		t.Fatal(err)
	}
	// Poll refreshes last_seen_at without adding an event or bumping visit_count.
	if err := db.RecordShareAccess(ctx, "l1", "203.0.113.10", "poll", "", "", false); err != nil {
		t.Fatal(err)
	}

	onlyLoopback, err = db.HasOnlyLoopbackShareAccess(ctx, time.Now().Add(-time.Hour).Unix())
	if err != nil || onlyLoopback {
		t.Fatalf("loopback-only should be false after real IP visited: %v %v", onlyLoopback, err)
	}

	sums, err := db.ShareAccessSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := sums["l1"]
	if s.UniqueIPs != 2 || s.VisitCount != 2 || s.UnlockFailCount != 1 {
		t.Fatalf("unexpected summary: %+v", s)
	}

	ips, err := db.ShareAccessIPs(ctx, "l1", 50)
	if err != nil || len(ips) != 2 {
		t.Fatalf("ips = %+v %v", ips, err)
	}
	var found bool
	for _, r := range ips {
		if r.IP == "203.0.113.10" {
			found = true
			if r.VisitCount != 1 || r.UnlockOKCount != 1 || r.UnlockFailCount != 1 || r.LastEvent != "visit" {
				t.Fatalf("unexpected ip row: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("missing 203.0.113.10 row")
	}

	events, err := db.ShareAccessEvents(ctx, "l1", 50)
	if err != nil || len(events) != 4 {
		t.Fatalf("events len = %d %v, want 4", len(events), err)
	}

	// Trim to maxRows = 2.
	if err := db.TrimShareAccessEvents(ctx, 0, 2); err != nil {
		t.Fatal(err)
	}
	events, err = db.ShareAccessEvents(ctx, "l1", 50)
	if err != nil || len(events) != 2 {
		t.Fatalf("trimmed events len = %d %v, want 2", len(events), err)
	}

	// EnsureShareUnlockSecret persists across calls and seals with box.
	box, err := secret.Open(filepath.Join(dir, secret.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	k1, err := db.EnsureShareUnlockSecret(ctx, box)
	if err != nil || len(k1) != 32 {
		t.Fatalf("EnsureShareUnlockSecret = %d %v", len(k1), err)
	}
	k2, err := db.EnsureShareUnlockSecret(ctx, box)
	if err != nil || string(k1) != string(k2) {
		t.Fatalf("EnsureShareUnlockSecret changed across calls")
	}
}
