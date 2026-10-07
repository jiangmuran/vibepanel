package headless

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeClaude writes a shell script that behaves like `claude -p` according to
// its prompt (argv[2]):
//
//	ok     prints an init, two deltas, a tool call and a result
//	sleep  prints an init, starts a grandchild that records its pid, waits
//	fail   writes to stderr and exits 3 without a result
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > "$PWD/argv.txt"
case "$2" in
ok)
  echo '{"type":"system","subtype":"init","session_id":"s","model":"fake"}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"a"}},"parent_tool_use_id":null}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"b"}},"parent_tool_use_id":null}'
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"date"}}]},"parent_tool_use_id":null}'
  echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":false}]},"parent_tool_use_id":null}'
  printf '{"type":"result","subtype":"success","result":"ab","is_error":false,"total_cost_usd":0.01,"duration_ms":5,"num_turns":1}'
  ;;
sleep)
  echo '{"type":"system","subtype":"init","session_id":"s","model":"fake"}'
  sleep 300 &
  echo $! > "$PWD/child.pid"
  wait
  ;;
fail)
  echo 'not logged in' >&2
  exit 3
  ;;
esac
`
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func spec(bin, dir, prompt, session string) Spec {
	return Spec{
		Binary: bin, Args: []string{"-p", prompt}, Dir: dir, Env: []string{"PATH=" + os.Getenv("PATH")},
		Timeout: time.Minute, SessionID: session, ProjectID: "p1", Prompt: prompt, MaxConcurrent: 2,
	}
}

// wait reads a run to its end the way the SSE handler does.
func waitEnd(t *testing.T, r *Run) []Event {
	t.Helper()
	var all []Event
	var after int64
	deadline := time.After(20 * time.Second)
	for {
		evs, ch, done := r.Since(after)
		for _, e := range evs {
			all = append(all, e)
			after = e.Seq
		}
		if done {
			return all
		}
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("run did not end; events so far %v", all)
		}
	}
}

func types(evs []Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return strings.Join(out, ",")
}

func TestARunStreamsReplaysAndEnds(t *testing.T) {
	bin := fakeClaude(t)
	dir := t.TempDir()
	g := NewRegistry()
	r, err := g.Start(spec(bin, dir, "ok", "s1"))
	if err != nil {
		t.Fatal(err)
	}
	evs := waitEnd(t, r)
	if types(evs) != "init,text,text,tool,tool_done,result" {
		t.Fatalf("events %s", types(evs))
	}
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("seq %d at %d", e.Seq, i)
		}
	}
	if r.State() != StateDone {
		t.Fatalf("state %s", r.State())
	}
	// Replay from the middle: exactly what is after it, and the end.
	again, _, done := r.Since(3)
	if types(again) != "tool,tool_done,result" || !done {
		t.Fatalf("replay after 3: %s done=%v", types(again), done)
	}
	// Nothing after the terminal event, whatever arrives.
	r.add(Event{Type: "error", Fields: map[string]any{"message": "late"}})
	if all, _, _ := r.Since(0); len(all) != 6 {
		t.Fatalf("an event was added after the terminal one: %s", types(all))
	}
	if list := g.List("p1"); len(list) != 1 || list[0].State != StateDone || list[0].EndedAt == 0 {
		t.Fatalf("list %+v", list)
	}
	if l := g.List("other"); l == nil || len(l) != 0 {
		t.Fatalf("list for another project: %v", l)
	}
}

func TestAFailedRunEndsWithItsStderr(t *testing.T) {
	g := NewRegistry()
	r, err := g.Start(spec(fakeClaude(t), t.TempDir(), "fail", "s1"))
	if err != nil {
		t.Fatal(err)
	}
	evs := waitEnd(t, r)
	last := evs[len(evs)-1]
	if last.Type != "error" || !strings.Contains(last.Fields["message"].(string), "not logged in") {
		t.Fatalf("last event %v", last)
	}
	if r.State() != StateError {
		t.Fatalf("state %s", r.State())
	}
}

func TestStopKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	g := NewRegistry()
	r, err := g.Start(spec(fakeClaude(t), dir, "sleep", "s1"))
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "child.pid")
	var child int
	for i := 0; i < 200 && child == 0; i++ {
		b, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		time.Sleep(20 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("the fake never started its grandchild")
	}

	// Same session again while it runs: refused.
	if _, err := g.Start(spec(fakeClaude(t), dir, "ok", "s1")); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("same session: %v", err)
	}
	if !g.RunningSession("s1") {
		t.Fatal("RunningSession says no")
	}

	r.Stop()
	evs := waitEnd(t, r)
	if last := evs[len(evs)-1]; last.Type != "stopped" {
		t.Fatalf("last event %v", last)
	}
	if r.State() != StateStopped {
		t.Fatalf("state %s", r.State())
	}
	// The grandchild was in the group; it must be gone (or a zombie being
	// reaped), never still sleeping.
	gone := false
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(child, 0); err != nil {
			gone = true
			break
		}
		if b, _ := os.ReadFile("/proc/" + strconv.Itoa(child) + "/stat"); strings.Contains(string(b), ") Z ") {
			gone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gone {
		_ = syscall.Kill(child, syscall.SIGKILL)
		t.Fatal("stopping the run left its child running")
	}
	r.Stop() // a second stop on an ended run is a no-op
}

func TestTheCapCountsRunningRunsOnly(t *testing.T) {
	bin := fakeClaude(t)
	g := NewRegistry()
	sp := func(prompt, session string) Spec {
		s := spec(bin, t.TempDir(), prompt, session)
		s.MaxConcurrent = 1
		return s
	}
	done, err := g.Start(sp("ok", "a"))
	if err != nil {
		t.Fatal(err)
	}
	waitEnd(t, done)
	long, err := g.Start(sp("sleep", "b"))
	if err != nil {
		t.Fatalf("a finished run counted against the cap: %v", err)
	}
	if _, err := g.Start(sp("ok", "c")); !errors.Is(err, ErrTooMany) {
		t.Fatalf("over the cap: %v", err)
	}
	long.Stop()
	waitEnd(t, long)
}

func TestATimedOutRunEndsWithAnError(t *testing.T) {
	g := NewRegistry()
	s := spec(fakeClaude(t), t.TempDir(), "sleep", "s1")
	s.Timeout = 300 * time.Millisecond
	r, err := g.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	evs := waitEnd(t, r)
	last := evs[len(evs)-1]
	if last.Type != "error" || !strings.Contains(last.Fields["message"].(string), "timed out") {
		t.Fatalf("last %v", last)
	}
}

func TestFinishedRunsAgeOut(t *testing.T) {
	g := NewRegistry()
	r, err := g.Start(spec(fakeClaude(t), t.TempDir(), "ok", "s1"))
	if err != nil {
		t.Fatal(err)
	}
	waitEnd(t, r)
	g.now = func() time.Time { return time.Now().Add(31 * time.Minute) }
	if _, ok := g.Get(r.ID); ok {
		t.Fatal("a run finished 31 minutes ago is still kept")
	}
}

func TestTheBufferIsCapped(t *testing.T) {
	r := &Run{changed: make(chan struct{}), state: StateRunning}
	for i := 0; i < bufferCap+500; i++ {
		r.add(Event{Type: "text", Fields: map[string]any{"d": "x"}})
	}
	all, _, _ := r.Since(0)
	if len(all) > bufferCap {
		t.Fatalf("%d events buffered", len(all))
	}
	total := 0
	for _, e := range all {
		total += len(e.Fields["d"].(string))
	}
	if total != bufferCap+500 {
		t.Fatalf("text lost in compaction: %d of %d", total, bufferCap+500)
	}
	if all[len(all)-1].Seq != int64(bufferCap+500) {
		t.Fatal("the newest seq moved")
	}
}
