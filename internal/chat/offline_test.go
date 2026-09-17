package chat_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jiangmuran/vibepanel/internal/chat"
	_ "github.com/jiangmuran/vibepanel/internal/chat/feishu"
	_ "github.com/jiangmuran/vibepanel/internal/chat/telegram"
	_ "github.com/jiangmuran/vibepanel/internal/chat/weixin"
	"github.com/jiangmuran/vibepanel/internal/secret"
	"github.com/jiangmuran/vibepanel/internal/session"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// recorder is an HTTP transport that dials nothing and remembers every
// request anything tried to make.
type recorder struct {
	mu   sync.Mutex
	urls []string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.mu.Unlock()
	return nil, errors.New("offline")
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}

type noTerm struct{}

func (noTerm) Paste(context.Context, string, string) error          { return nil }
func (noTerm) Keys(context.Context, string, ...string) error        { return nil }
func (noTerm) Screen(context.Context, string, bool) (string, error) { return "", nil }
func (noTerm) Fullscreen(context.Context, string) bool              { return false }

// The promise the README makes: the chat code connects to nothing until a
// channel is switched on. Every real adapter this build ships is configured
// with credentials and left off, a person is paired, and a session goes
// waiting with something to say; not one request is made. Then one channel
// is switched on, and the same transport sees it try, which is what shows
// the recorder would have caught one.
func TestChatConnectsToNothingUntilAChannelIsSwitchedOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	box, err := secret.Open(filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	b := chat.New(chat.Deps{
		DB: db, Term: noTerm{}, Box: box, HTTP: &http.Client{Transport: rec},
		Coalesce: 50 * time.Millisecond, PublicURL: func() string { return "https://panel.test" },
	})
	b.Start(ctx)

	configs := map[string]map[string]string{
		"telegram": {"token": "123456:ABCDEF"},
		"feishu":   {"app_id": "cli_x", "app_secret": "s", "verification_token": "v"},
		"weixin":   {"bot_token": "t", "user_id": "u@im.bot", "bot_id": "b@im.bot"},
	}
	for kind, values := range configs {
		if _, ok := chat.FactoryFor(kind); !ok {
			t.Fatalf("this build has no %s adapter", kind)
		}
		if err := b.WriteChannel(ctx, kind, false, chat.ChannelConfig{Values: values}); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	_ = db.PutChatPeer(ctx, store.ChatPeer{Channel: "telegram", PeerID: "1", Status: store.PeerPaired, Mode: store.ModeNormal, ContextToken: "c"})
	if _, err := db.CreateProject(ctx, "p1", "demo", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	row, err := db.CreateSession(ctx, store.Session{ID: "s1", ProjectID: "p1", TmuxName: "vp_s1", Title: "fix", State: session.StateWaiting,
		LaunchCommand: []string{"claude"}, LaunchRecorded: true, Command: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.AddSessionMessage(ctx, store.SessionMessage{SessionID: "s1", Kind: store.MessagePrompt, Text: "Bash: ls"})
	b.SessionChanged(row, session.StateWaiting)
	_ = b.Healths(ctx)
	_, _, _, _ = b.Preview(ctx, "s1")
	time.Sleep(400 * time.Millisecond)
	if got := rec.seen(); len(got) != 0 {
		t.Fatalf("requests with every channel off: %q", got)
	}

	if err := b.WriteChannel(ctx, "telegram", true, chat.ChannelConfig{Values: configs["telegram"]}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.seen()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(rec.seen()) == 0 {
		t.Fatal("a channel switched on made no request, so the recorder proves nothing")
	}
}
