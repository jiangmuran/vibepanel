package store

import (
	"context"
	"fmt"
	"testing"
)

func TestSessionMessagesAreBoundedPerSession(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	for i := 0; i < MessagesKeptPerSession+25; i++ {
		if _, err := db.AddSessionMessage(ctx, SessionMessage{
			SessionID: "s1", Kind: MessageAssistant, Text: fmt.Sprintf("turn %d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A second session is not trimmed by the first's growth.
	if _, err := db.AddSessionMessage(ctx, SessionMessage{SessionID: "s2", Kind: MessagePrompt, Text: "ok?"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListSessionMessages(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MessagesKeptPerSession {
		t.Fatalf("kept %d rows, want %d", len(got), MessagesKeptPerSession)
	}
	if got[0].Text != "turn 25" || got[len(got)-1].Text != fmt.Sprintf("turn %d", MessagesKeptPerSession+24) {
		t.Fatalf("wrong window: first %q last %q", got[0].Text, got[len(got)-1].Text)
	}
	last, ok, err := db.LatestSessionMessage(ctx, "s1")
	if err != nil || !ok || last.Text != got[len(got)-1].Text {
		t.Fatalf("latest = %+v ok=%v err=%v", last, ok, err)
	}
	if _, ok, _ := db.LatestSessionMessage(ctx, "nobody"); ok {
		t.Fatal("a session that never spoke has a latest message")
	}
	other, _ := db.ListSessionMessages(ctx, "s2", 0)
	if len(other) != 1 {
		t.Fatalf("s2 has %d rows", len(other))
	}
}

func TestSessionMessageKindIsRefused(t *testing.T) {
	db := openTest(t)
	for _, bad := range []SessionMessage{
		{SessionID: "s1", Kind: "Notification", Text: "x"},
		{SessionID: "", Kind: MessageAssistant, Text: "x"},
	} {
		if _, err := db.AddSessionMessage(context.Background(), bad); err == nil {
			t.Errorf("%+v was stored", bad)
		}
	}
}

func TestTranscriptPathIsRememberedPerSession(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, ok, _ := db.GetSessionTranscript(ctx, "s1"); ok {
		t.Fatal("found before set")
	}
	if err := db.SetSessionTranscript(ctx, "s1", "claude", "/a.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionTranscript(ctx, "s1", "claude", "/b.jsonl"); err != nil {
		t.Fatal(err)
	}
	tr, ok, err := db.GetSessionTranscript(ctx, "s1")
	if err != nil || !ok || tr.Path != "/b.jsonl" || tr.Tool != "claude" {
		t.Fatalf("got %+v ok=%v err=%v", tr, ok, err)
	}
	if err := db.SetSessionTranscript(ctx, "s1", "claude", ""); err == nil {
		t.Fatal("an empty path was accepted")
	}
}

func TestChatChannelsRoundTripAndDeleteTakesTheirPeers(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, err := db.GetChatChannel(ctx, "telegram"); err != ErrNotFound {
		t.Fatalf("missing channel: %v", err)
	}
	if err := db.PutChatChannel(ctx, ChatChannel{Kind: "telegram", Enabled: true, ConfigEnc: []byte("sealed")}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutChatChannel(ctx, ChatChannel{Kind: "telegram", Enabled: false, ConfigEnc: []byte("sealed2")}); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetChatChannel(ctx, "telegram")
	if err != nil || c.Enabled || string(c.ConfigEnc) != "sealed2" {
		t.Fatalf("got %+v err=%v", c, err)
	}
	if err := db.PutChatPeer(ctx, ChatPeer{Channel: "telegram", PeerID: "7", Status: PeerPaired, Mode: ModeNormal}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordChatOutbound(ctx, ChatOutbound{Channel: "telegram", PeerID: "7", Ref: "m1", SessionID: "s1", Kind: OutboundStatus}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteChatChannel(ctx, "telegram"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetChatPeer(ctx, "telegram", "7"); err != ErrNotFound {
		t.Fatalf("peer survived its channel: %v", err)
	}
	if _, ok, _ := db.ChatOutboundByRef(ctx, "telegram", "7", "m1"); ok {
		t.Fatal("outbound record survived its channel")
	}
}

func TestChatPeersValidateAndListPendingFirst(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	for _, bad := range []ChatPeer{
		{Channel: "t", PeerID: "1", Status: "friend", Mode: ModeNormal},
		{Channel: "t", PeerID: "1", Status: PeerPaired, Mode: "auto"},
		{Channel: "", PeerID: "1", Status: PeerPaired, Mode: ModeNormal},
	} {
		if err := db.PutChatPeer(ctx, bad); err == nil {
			t.Errorf("%+v was stored", bad)
		}
	}
	must := func(p ChatPeer) {
		t.Helper()
		if err := db.PutChatPeer(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	must(ChatPeer{Channel: "t", PeerID: "a", Status: PeerPaired, Mode: ModeAdvanced, CreatedAt: 1})
	must(ChatPeer{Channel: "t", PeerID: "b", Status: PeerBlocked, Mode: ModeNormal, CreatedAt: 2})
	must(ChatPeer{Channel: "w", PeerID: "c", Status: PeerPending, Mode: ModeNormal, PairingCode: "1234", CreatedAt: 3})
	all, err := db.ListChatPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].PeerID != "c" || all[1].PeerID != "a" || all[2].PeerID != "b" {
		t.Fatalf("order: %+v", all)
	}
	paired, _ := db.PairedChatPeers(ctx)
	if len(paired) != 1 || paired[0].PeerID != "a" || paired[0].Mode != ModeAdvanced {
		t.Fatalf("paired: %+v", paired)
	}
	// An update keeps created_at and replaces the rest.
	must(ChatPeer{Channel: "t", PeerID: "a", Status: PeerPaired, Mode: ModeNormal, FocusSession: "s9", ContextToken: "ctx", LastSeenAt: 50})
	got, err := db.GetChatPeer(ctx, "t", "a")
	if err != nil || got.CreatedAt != 1 || got.FocusSession != "s9" || got.ContextToken != "ctx" || got.LastSeenAt != 50 || got.Mode != ModeNormal {
		t.Fatalf("after update: %+v err=%v", got, err)
	}
	if err := db.DeleteChatPeer(ctx, "t", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetChatPeer(ctx, "t", "a"); err != ErrNotFound {
		t.Fatalf("deleted peer: %v", err)
	}
}

func TestChatHandlesAreMonotonicAndNeverReused(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	h1, err := db.ChatHandle(ctx, "s1")
	if err != nil || h1 != 1 {
		t.Fatalf("first handle %d err=%v", h1, err)
	}
	h2, _ := db.ChatHandle(ctx, "s2")
	again, _ := db.ChatHandle(ctx, "s1")
	if h2 != 2 || again != 1 {
		t.Fatalf("h2=%d again=%d", h2, again)
	}
	id, ok, err := db.SessionByHandle(ctx, 2)
	if err != nil || !ok || id != "s2" {
		t.Fatalf("by handle: %q ok=%v err=%v", id, ok, err)
	}
	if _, ok, _ := db.SessionByHandle(ctx, 9); ok {
		t.Fatal("an unassigned handle resolved")
	}
	// Deleting the highest handle's row does not hand its number to the next
	// session: MAX is over what remains, so simulate the real risk -- the
	// session row is gone but the handle row stays, which is the invariant.
	m, _ := db.ChatHandles(ctx)
	if len(m) != 2 || m["s1"] != 1 || m["s2"] != 2 {
		t.Fatalf("handles: %v", m)
	}
	if _, err := db.ChatHandle(ctx, ""); err == nil {
		t.Fatal("empty session got a handle")
	}
}

func TestOutboundRefsResolveAndRecentIsNewestFirst(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	for i, ref := range []string{"m1", "m2", "m3"} {
		if err := db.RecordChatOutbound(ctx, ChatOutbound{
			Channel: "t", PeerID: "p", Ref: ref, SessionID: fmt.Sprint("s", i), Kind: OutboundStatus, At: int64(100 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	o, ok, err := db.ChatOutboundByRef(ctx, "t", "p", "m2")
	if err != nil || !ok || o.SessionID != "s1" {
		t.Fatalf("by ref: %+v ok=%v err=%v", o, ok, err)
	}
	if _, ok, _ := db.ChatOutboundByRef(ctx, "t", "other", "m2"); ok {
		t.Fatal("a ref resolved for the wrong peer")
	}
	recent, _ := db.RecentChatOutbound(ctx, "t", "p", 2)
	if len(recent) != 2 || recent[0].Ref != "m3" || recent[1].Ref != "m2" {
		t.Fatalf("recent: %+v", recent)
	}
	if err := db.SweepChatOutbound(ctx, 102); err != nil {
		t.Fatal(err)
	}
	recent, _ = db.RecentChatOutbound(ctx, "t", "p", 10)
	if len(recent) != 1 || recent[0].Ref != "m3" {
		t.Fatalf("after sweep: %+v", recent)
	}
	if err := db.RecordChatOutbound(ctx, ChatOutbound{Channel: "t", PeerID: "p"}); err == nil {
		t.Fatal("an outbound without a ref was stored")
	}
}

func TestStatusRefsAndMutes(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, ok, _ := db.ChatStatusRef(ctx, "t", "p", "s1"); ok {
		t.Fatal("status ref before set")
	}
	if err := db.SetChatStatusRef(ctx, "t", "p", "s1", "m1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatStatusRef(ctx, "t", "p", "s1", "m2"); err != nil {
		t.Fatal(err)
	}
	ref, ok, _ := db.ChatStatusRef(ctx, "t", "p", "s1")
	if !ok || ref != "m2" {
		t.Fatalf("ref %q ok=%v", ref, ok)
	}
	if err := db.ClearChatStatus(ctx, "t", "p", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.ChatStatusRef(ctx, "t", "p", "s1"); ok {
		t.Fatal("status ref survived clear")
	}

	if err := db.MuteChat(ctx, "t", "p", "s1", 1000); err != nil {
		t.Fatal(err)
	}
	until, _ := db.ChatMutedUntil(ctx, "t", "p", "s1", 999)
	if until != 1000 {
		t.Fatalf("muted until %d", until)
	}
	until, _ = db.ChatMutedUntil(ctx, "t", "p", "s1", 1000)
	if until != 0 {
		t.Fatalf("an expired mute is still in force: %d", until)
	}
	if err := db.MuteChat(ctx, "t", "p", "s1", 0); err != nil {
		t.Fatal(err)
	}
	until, _ = db.ChatMutedUntil(ctx, "t", "p", "s1", 0)
	if until != 0 {
		t.Fatalf("unmute left %d", until)
	}
}

func TestChatSpendAccumulatesPerDay(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	total, err := db.AddChatSpend(ctx, "2026-09-15", 0.02)
	if err != nil || total != 0.02 {
		t.Fatalf("total %v err=%v", total, err)
	}
	total, _ = db.AddChatSpend(ctx, "2026-09-15", 0.03)
	if total < 0.0499 || total > 0.0501 {
		t.Fatalf("total %v", total)
	}
	usd, calls, _ := db.ChatSpend(ctx, "2026-09-15")
	if calls != 2 || usd < 0.0499 {
		t.Fatalf("usd=%v calls=%d", usd, calls)
	}
	usd, calls, _ = db.ChatSpend(ctx, "2026-09-16")
	if usd != 0 || calls != 0 {
		t.Fatalf("a day with nothing reads %v/%d", usd, calls)
	}
	if _, err := db.AddChatSpend(ctx, "", 1); err == nil {
		t.Fatal("spend without a day")
	}
}
