package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jiangmuran/vibepanel/internal/session"
)

func TestTrimAuditLogKeepsTheNewest(t *testing.T) {
	// Nothing removed an audit row before this existed. A panel on a public
	// port collects one per refused sign-in for as long as it runs, on the same
	// disk as the projects.
	ctx := context.Background()
	db := openTest(t)

	for i := 0; i < 40; i++ {
		if err := db.Audit(ctx, AuditEntry{
			At: int64(1000 + i), Event: "login.failed", Username: "u",
			IP: "203.0.113.7", Detail: fmt.Sprintf("attempt-%d", i),
		}); err != nil {
			t.Fatalf("Audit %d: %v", i, err)
		}
	}

	n, err := db.TrimAuditLog(ctx, 10)
	if err != nil {
		t.Fatalf("TrimAuditLog: %v", err)
	}
	if n != 30 {
		t.Errorf("trimmed %d rows, want 30", n)
	}

	entries, err := db.RecentAudit(ctx, 100)
	if err != nil {
		t.Fatalf("RecentAudit: %v", err)
	}
	if len(entries) != 10 {
		t.Fatalf("%d rows left, want 10", len(entries))
	}
	// The newest are what a person looking at the settings page wants; trimming
	// the wrong end would leave a log that stops before the interesting part.
	if entries[0].Detail != "attempt-39" {
		t.Errorf("newest row is %q, want attempt-39", entries[0].Detail)
	}
	if entries[9].Detail != "attempt-30" {
		t.Errorf("oldest kept row is %q, want attempt-30", entries[9].Detail)
	}

	// A table smaller than the cap is left alone, and a nonsense cap does not
	// empty it.
	if n, err := db.TrimAuditLog(ctx, 1000); err != nil || n != 0 {
		t.Errorf("trimming below the row count: %d rows, %v", n, err)
	}
	if n, err := db.TrimAuditLog(ctx, 0); err != nil || n != 0 {
		t.Errorf("trimming to zero: %d rows, %v", n, err)
	}
}

func TestSessionTitleIsBoundedWhicheverWayItArrives(t *testing.T) {
	// The automatic title is whatever the pane put in OSC 0/2, which is
	// whatever an agent printed. Measured before this was bounded: a
	// 200,000-character title reached the row intact and took the state
	// snapshot — rebuilt every two seconds and broadcast to every viewer that
	// is connected — from 705 bytes to 200,710.
	ctx := context.Background()
	db := openTest(t)
	if _, err := db.CreateProject(ctx, "p", "P", "/tmp"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := db.CreateSession(ctx, Session{ID: "s", ProjectID: "p", TmuxName: "vp_s"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	long := strings.Repeat("A", 200000)
	for _, src := range []TitleSource{TitleAuto, TitleManual} {
		if err := db.SetSessionTitle(ctx, "s", long, src); err != nil {
			t.Fatalf("SetSessionTitle %v: %v", src, err)
		}
		got, err := db.GetSession(ctx, "s")
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if n := utf8.RuneCountInString(got.Title); n > session.MaxTitleRunes {
			t.Errorf("%v title stored %d runes, cap is %d", src, n, session.MaxTitleRunes)
		}
	}
}

func TestLastOutputIsNotOnTheWire(t *testing.T) {
	// The state snapshot is broadcast to every viewer whenever it differs from
	// the previous one, and last_output_at moves for any session that is
	// producing output. So one busy agent turned every two-second tick into a
	// broadcast — measured with six sessions printing: ten ticks out of ten,
	// 85 KiB/min per viewer, and around 20 MB an hour at two dozen sessions on
	// a phone. That comparison exists precisely to prevent this.
	//
	// The column stays: the sidebar's ordering is built on it, in SQL.
	a := Session{ID: "s", Title: "agent", LastOutputAt: 1000}
	b := a
	b.LastOutputAt = 999999
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(ja, jb) {
		t.Errorf("two sessions differing only in last output serialise differently:\n%s\n%s", ja, jb)
	}
	if bytes.Contains(ja, []byte("lastOutput")) {
		t.Errorf("last_output_at is on the wire: %s", ja)
	}
}
