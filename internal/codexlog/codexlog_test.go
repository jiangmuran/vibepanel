package codexlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Lines shaped from real rollouts (codex-cli 0.153), with everything that says
// nothing about the turn trimmed.
const (
	started  = `{"timestamp":"2026-09-07T01:01:21.061Z","ordinal":2,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1788742867}}`
	item     = `{"timestamp":"2026-09-07T01:01:26.165Z","ordinal":18,"type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"Reasoning"}}}`
	tokens   = `{"timestamp":"2026-09-07T01:01:28.787Z","ordinal":24,"type":"event_msg","payload":{"type":"token_count","info":{}}}`
	complete = `{"timestamp":"2026-09-07T01:03:28.393Z","ordinal":143,"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"done"}}`
	aborted  = `{"timestamp":"2026-09-13T08:25:12.234Z","ordinal":3,"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t2","reason":"interrupted"}}`
	approval = `{"timestamp":"2026-09-07T01:02:00.000Z","type":"event_msg","payload":{"type":"exec_approval_request","call_id":"c1"}}`
	response = `{"timestamp":"2026-09-07T01:02:05.000Z","type":"response_item","payload":{"type":"message","content":"task_complete is just words here"}}`
)

func scanLines(lines ...string) Status {
	return Scan(strings.NewReader(strings.Join(lines, "\n")+"\n"), Status{})
}

func TestARolloutSaysWhatTheTurnIsDoing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"started", []string{started, item, tokens}, Working},
		{"complete", []string{started, item, complete}, Done},
		{"interrupted", []string{started, aborted}, Done},
		{"asking", []string{started, approval}, Waiting},
		{"asked and answered", []string{started, approval, item}, Working},
		{"a message that mentions an event is not the event", []string{started, response}, Working},
		{"nothing about a turn", []string{tokens}, ""},
	} {
		if got := scanLines(tc.lines...); got.State != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got.State, tc.want)
		}
	}
	if got := scanLines(started, complete); !got.At.Equal(time.Date(2026, 9, 7, 1, 3, 28, 393e6, time.UTC)) {
		t.Errorf("the time is the log's, got %v", got.At)
	}
}

// A line too long for a default scanner -- a compacted history is megabytes --
// must not end the scan before the turn's last event.
func TestAHugeLineDoesNotStopTheScan(t *testing.T) {
	big := `{"type":"compacted","payload":{"x":"` + strings.Repeat("a", 3<<20) + `"}}`
	if got := scanLines(started, big, complete); got.State != Done {
		t.Errorf("state after a 3 MiB line = %q", got.State)
	}
}

type fakeProc struct {
	children map[int][]int
	comm     map[int]string
	fds      map[int][]string
}

func (f fakeProc) Children(pid int) []int     { return f.children[pid] }
func (f fakeProc) Comm(pid int) string        { return f.comm[pid] }
func (f fakeProc) FDTargets(pid int) []string { return f.fds[pid] }

func TestTheRolloutIsTheOneThePanesCodexHoldsOpen(t *testing.T) {
	p := fakeProc{
		// pane shell 10 -> node wrapper 11 -> codex 12; an unrelated codex 99.
		children: map[int][]int{10: {11}, 11: {12}},
		comm:     map[int]string{10: "bash", 11: "node", 12: "codex", 99: "codex"},
		fds: map[int][]string{
			11: {"/home/u/.codex/sessions/2026/09/13/rollout-wrapper.jsonl"}, // not codex: ignored
			12: {"/dev/pts/3", "/home/u/.codex/logs_2.sqlite", "/home/u/.codex/sessions/2026/09/13/rollout-2026-09-13T17-10-52-abc.jsonl"},
			99: {"/home/u/.codex/sessions/2026/09/13/rollout-other.jsonl"},
		},
	}
	if got := FindRollout(p, 10); got != "/home/u/.codex/sessions/2026/09/13/rollout-2026-09-13T17-10-52-abc.jsonl" {
		t.Errorf("found %q", got)
	}
	if got := FindRollout(p, 50); got != "" {
		t.Errorf("a pane with no codex found %q", got)
	}
	if got := FindRollout(p, 0); got != "" {
		t.Errorf("pid 0 found %q", got)
	}
}

func TestTheWalkIsBounded(t *testing.T) {
	p := fakeProc{children: map[int][]int{}, comm: map[int]string{}, fds: map[int][]string{}}
	for i := 1; i < 1000; i++ {
		p.children[i] = []int{i + 1}
	}
	p.comm[500] = "codex"
	p.fds[500] = []string{"/x/sessions/a/rollout-deep.jsonl"}
	if got := FindRollout(p, 1); got != "" {
		t.Errorf("a codex 500 levels down was found: the walk is not bounded")
	}
}

func TestTheWatcherReadsOnlyWhatWasAppended(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions", "2026", "09", "13", "rollout-x.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
		f.Close() //nolint:errcheck
	}
	write(started + "\n" + item + "\n")
	w := &Watcher{Proc: fakeProc{comm: map[int]string{7: "codex"}, fds: map[int][]string{7: {path}}}}
	now := time.Now()
	if st, ok := w.State("s", 7, now); !ok || st.State != Working {
		t.Fatalf("first read = %+v %v", st, ok)
	}
	// Half a line: not read yet.
	write(complete[:40])
	if st, _ := w.State("s", 7, now.Add(time.Second)); st.State != Working {
		t.Errorf("a partial line changed the state to %q", st.State)
	}
	write(complete[40:] + "\n")
	if st, _ := w.State("s", 7, now.Add(2*time.Second)); st.State != Done {
		t.Errorf("after the line was finished = %q", st.State)
	}
	w.Retain(nil)
	if len(w.tail) != 0 {
		t.Error("Retain kept a session it was not given")
	}
}

// A rollout of hundreds of megabytes is not read from the top when first found.
func TestTheWatcherStartsNearTheEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions", "a", "rollout-big.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(started + "\n")
	for b.Len() < 2*initialTail {
		fmt.Fprintf(&b, "%s\n", tokens)
	}
	b.WriteString(complete + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{Proc: fakeProc{comm: map[int]string{7: "codex"}, fds: map[int][]string{7: {path}}}}
	st, ok := w.State("s", 7, time.Now())
	if !ok || st.State != Done {
		t.Errorf("state = %+v %v", st, ok)
	}
	if w.tail["s"].offset != int64(b.Len()) {
		t.Errorf("offset %d, want the end %d", w.tail["s"].offset, b.Len())
	}
}

func TestNoProcMeansNothingFound(t *testing.T) {
	w := &Watcher{Proc: procFS{root: filepath.Join(t.TempDir(), "no-proc")}}
	if _, ok := w.State("s", 1234, time.Now()); ok {
		t.Error("a machine with no /proc reported a rollout state")
	}
}
