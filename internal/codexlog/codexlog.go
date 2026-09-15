// Package codexlog reads what Codex writes about its own turns, for a Codex
// session that has no hook reporting its state.
//
// Codex appends every turn to a rollout file, ~/.codex/sessions/YYYY/MM/DD/
// rollout-*.jsonl, and holds that file open for as long as the session runs.
// So the file a pane's Codex is writing is found by walking the pane's process
// tree and reading which descriptors point at a rollout -- no guessing from
// the working directory or the start time, which two Codex sessions in one
// project would share.
//
// What the file says, measured on this machine's rollouts (codex-cli 0.153):
// `event_msg` lines whose payload type is `task_started` when a turn begins,
// `task_complete` when it ends, and `turn_aborted` when it is interrupted.
// Approval requests are `exec_approval_request`, `apply_patch_approval_request`
// and `request_permissions` in the binary; they were never seen in a rollout
// here (these sessions run without approvals), so they are read if present and
// nothing depends on them being there.
//
// Linux only in practice: /proc is where the descriptors are. Without it
// nothing is found and the session stays on the heuristic, as it was.
package codexlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// States the log can report. Strings rather than session.State so this package
// stays below internal/session; the caller converts, and the server validates.
const (
	Working = "working"
	Waiting = "waiting"
	Done    = "done"
)

// Proc is the part of /proc this package reads.
type Proc interface {
	// Children lists the direct children of pid.
	Children(pid int) []int
	// Comm is the process's command name.
	Comm(pid int) string
	// FDTargets lists what the process's open descriptors point at.
	FDTargets(pid int) []string
}

// SystemProc reads the real /proc.
func SystemProc() Proc { return procFS{root: "/proc"} }

type procFS struct{ root string }

func (p procFS) Children(pid int) []int {
	// /proc/<pid>/task/<tid>/children needs CONFIG_PROC_CHILDREN, which most
	// distribution kernels have. When it is missing, every process's stat is
	// read for its parent: a few hundred small reads, and only while a Codex
	// rollout is still being looked for.
	tasks, err := os.ReadDir(filepath.Join(p.root, strconv.Itoa(pid), "task"))
	if err == nil {
		var out []int
		found := false
		for _, t := range tasks {
			b, rerr := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "task", t.Name(), "children"))
			if rerr != nil {
				continue
			}
			found = true
			for _, f := range strings.Fields(string(b)) {
				if n, cerr := strconv.Atoi(f); cerr == nil {
					out = append(out, n)
				}
			}
		}
		if found {
			return out
		}
	}
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		child, cerr := strconv.Atoi(e.Name())
		if cerr != nil {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(p.root, e.Name(), "stat"))
		if rerr != nil {
			continue
		}
		// pid (comm) state ppid ...; comm may contain spaces and parentheses,
		// so the fields are read after the last ')'.
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		fields := strings.Fields(s[i+1:])
		if len(fields) >= 2 && fields[1] == strconv.Itoa(pid) {
			out = append(out, child)
		}
	}
	return out
}

func (p procFS) Comm(pid int) string {
	b, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (p procFS) FDTargets(pid int) []string {
	dir := filepath.Join(p.root, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if target, lerr := os.Readlink(filepath.Join(dir, e.Name())); lerr == nil {
			out = append(out, target)
		}
	}
	return out
}

var rolloutPath = regexp.MustCompile(`/sessions/.+/rollout-[^/]+\.jsonl$`)

// Bounds on the walk, so a pane running a build with a thousand processes
// under it costs a bounded amount of reading.
const (
	maxDepth = 6
	maxProcs = 64
)

// FindRollout returns the rollout file a Codex process under pid holds open,
// or "" when there is none.
func FindRollout(p Proc, pid int) string {
	if pid <= 0 {
		return ""
	}
	type item struct{ pid, depth int }
	queue := []item{{pid, 0}}
	seen := map[int]bool{}
	for len(queue) > 0 && len(seen) < maxProcs {
		it := queue[0]
		queue = queue[1:]
		if seen[it.pid] {
			continue
		}
		seen[it.pid] = true
		if strings.HasPrefix(p.Comm(it.pid), "codex") {
			for _, target := range p.FDTargets(it.pid) {
				if rolloutPath.MatchString(target) {
					return target
				}
			}
		}
		if it.depth < maxDepth {
			for _, c := range p.Children(it.pid) {
				queue = append(queue, item{c, it.depth + 1})
			}
		}
	}
	return ""
}

// Status is the newest thing a rollout said about the turn, and when.
type Status struct {
	State string
	At    time.Time
}

type rolloutLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type string `json:"type"`
	} `json:"payload"`
}

// Scan reads rollout lines and returns the status after the last one that says
// anything about the turn, starting from cur.
func Scan(r io.Reader, cur Status) Status {
	sc := bufio.NewScanner(r)
	// Codex's longest lines are several megabytes (a whole compacted history);
	// they say nothing about the turn and are skipped rather than failing the
	// scan.
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"event_msg"`)) {
			continue
		}
		var l rolloutLine
		if json.Unmarshal(line, &l) != nil || l.Type != "event_msg" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, l.Timestamp)
		if err != nil {
			continue
		}
		switch t := l.Payload.Type; {
		case t == "task_started":
			cur = Status{Working, at}
		case t == "task_complete" || t == "turn_aborted":
			cur = Status{Done, at}
		case strings.HasSuffix(t, "approval_request") || t == "request_permissions" || t == "request_user_input":
			cur = Status{Waiting, at}
		case cur.State == Waiting && (t == "item_completed" || strings.HasPrefix(t, "exec_command") ||
			strings.HasPrefix(t, "patch_apply")):
			// The question was answered and the tool is running.
			cur = Status{Working, at}
		}
	}
	return cur
}

// How much of a rollout is read when it is first found. Enough to hold the
// last turn's start; a rollout can be hundreds of megabytes.
const (
	initialTail = 512 << 10
	maxRead     = 1 << 20
	reresolve   = 15 * time.Second
)

// Watcher follows the rollout of each Codex session. Safe for concurrent use;
// meant to be called from the poller, never from a request.
type Watcher struct {
	Proc Proc

	mu   sync.Mutex
	tail map[string]*tail
}

type tail struct {
	pid        int
	path       string
	offset     int64
	status     Status
	resolvedAt time.Time
}

// State returns what the session's rollout last said, if its Codex could be
// found. It reads only what was appended since the previous call.
func (w *Watcher) State(sessionID string, panePID int, now time.Time) (Status, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tail == nil {
		w.tail = map[string]*tail{}
	}
	t := w.tail[sessionID]
	if t == nil || t.pid != panePID {
		t = &tail{pid: panePID}
		w.tail[sessionID] = t
	}
	if t.path == "" || now.Sub(t.resolvedAt) >= reresolve {
		proc := w.Proc
		if proc == nil {
			proc = SystemProc()
		}
		path := FindRollout(proc, panePID)
		t.resolvedAt = now
		if path != t.path {
			t.path, t.offset, t.status = path, -1, Status{}
		}
	}
	if t.path == "" {
		return Status{}, false
	}
	f, err := os.Open(t.path)
	if err != nil {
		t.path = ""
		return Status{}, false
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil {
		return t.status, t.status.State != ""
	}
	if t.offset < 0 || info.Size() < t.offset {
		t.offset = max(0, info.Size()-initialTail)
		if t.offset > 0 {
			// Start on a line boundary rather than in the middle of one.
			if _, err := f.Seek(t.offset, io.SeekStart); err == nil {
				br := bufio.NewReader(f)
				skipped, _ := br.ReadBytes('\n')
				t.offset += int64(len(skipped))
			}
		}
	}
	if info.Size() > t.offset {
		n := min(info.Size()-t.offset, maxRead)
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, t.offset); err == nil || err == io.EOF {
			// Only whole lines: a line still being written is read next time.
			if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
				t.status = Scan(bytes.NewReader(buf[:i+1]), t.status)
				t.offset += int64(i + 1)
			}
		}
	}
	return t.status, t.status.State != ""
}

// Retain forgets every session not in ids.
func (w *Watcher) Retain(ids []string) {
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for id := range w.tail {
		if !keep[id] {
			delete(w.tail, id)
		}
	}
}
