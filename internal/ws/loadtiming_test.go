package ws

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a log sink the connection goroutine and the test can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestALoadTimingIsLoggedOnlyForAWatchedSessionWithPlausibleNumbers(t *testing.T) {
	was := debugTiming
	debugTiming = true
	t.Cleanup(func() { debugTiming = was })

	s := serveSession(t, "vp_ws_loadtiming", "true")
	var logs lockedBuffer
	s.handler.Log = slog.New(slog.NewTextHandler(&logs, nil))

	v := dialViewer(t, s, "timing-viewer")
	good := map[string]any{
		"bytes": 2048, "subscribedMs": 12, "firstByteMs": 30, "receivedMs": 400,
		"readyMs": 950, "reconnect": false, "hidden": false,
	}
	// Before subscribing: the session id comes from the client, and a
	// connection that is not watching it has nothing to report about it.
	v.send(map[string]any{"t": MsgLoadTiming, "sessionId": "s1", "timing": good})
	v.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24})
	v.await(MsgSubscribed)
	v.send(map[string]any{"t": MsgLoadTiming, "sessionId": "s1", "timing": map[string]any{
		"bytes": -5, "subscribedMs": 0, "firstByteMs": 0, "receivedMs": 0, "readyMs": 1,
	}})
	v.send(map[string]any{"t": MsgLoadTiming, "sessionId": "s1", "timing": good})

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "ready_ms=950") {
		if time.Now().After(deadline) {
			t.Fatalf("a plausible report from a watching connection was not logged:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Give anything else a moment to land before counting.
	time.Sleep(200 * time.Millisecond)
	if n := strings.Count(logs.String(), `msg="terminal load"`); n != 1 {
		t.Errorf("%d load reports logged, want exactly the one valid report from a watching "+
			"connection:\n%s", n, logs.String())
	}
}

func TestALoadTimingIsSilentUnlessTimingIsSwitchedOn(t *testing.T) {
	was := debugTiming
	debugTiming = false
	t.Cleanup(func() { debugTiming = was })

	s := serveSession(t, "vp_ws_loadtiming_off", "true")
	var logs lockedBuffer
	s.handler.Log = slog.New(slog.NewTextHandler(&logs, nil))
	v := dialViewer(t, s, "timing-off")
	v.send(map[string]any{"t": MsgSubscribe, "sessionId": "s1", "cols": 80, "rows": 24})
	v.await(MsgSubscribed)
	v.send(map[string]any{"t": MsgLoadTiming, "sessionId": "s1", "timing": map[string]any{
		"bytes": 1, "subscribedMs": 1, "firstByteMs": 1, "receivedMs": 1, "readyMs": 1,
	}})
	// A ping behind it: once the pong is back, the report has been handled.
	v.send(map[string]any{"t": MsgPing})
	v.await(MsgPong)
	if strings.Contains(logs.String(), "terminal load") {
		t.Errorf("a load report was logged with VIBEPANEL_DEBUG_TIMING unset:\n%s", logs.String())
	}
}
