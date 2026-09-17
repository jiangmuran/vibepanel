// Package claudelog reads the one thing about a Claude Code session that its
// hooks never report: that the person pressed Escape.
//
// Claude Code fires no hook on an interrupt. Measured on 2.1.273 with every hook
// event logged, in a throwaway tmux: Escape during an answer, and Escape on a
// question or a permission dialog, each produced nothing -- no Stop, and not
// the idle notification a minute later either, waited for past two minutes.
// The session went on reading working, or waiting, until its next prompt.
// Escape during a running tool was not captured (the model would not run a
// long foreground command); the hook schema has PostToolUseFailure with
// is_interrupt for it, which hooks.Read reads, and this covers it regardless.
//
// The transcript does say it, the moment it happens, as a user entry whose text
// is `[Request interrupted by user]` or `[Request interrupted by user for tool
// use]`. The path comes from the hooks themselves (every document carries
// `transcript_path`), so nothing here guesses where the file is.
package claudelog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
)

// marker is the start of the text Claude Code writes for both kinds of
// interrupt.
var marker = []byte(`[Request interrupted by user`)

type entry struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Scan returns the time of the last interrupt written in r, or last if there is
// none.
//
// Lines without the marker are skipped before they are decoded: a transcript is
// mostly tool output, some of it megabytes a line, and this runs every poll.
func Scan(r io.Reader, last time.Time) time.Time {
	sc := bufio.NewScanner(r)
	// As long as the longest read the Watcher hands over, so a line that fits
	// in a read fits in the scanner. A longer line ends the scan; the Watcher
	// steps over those itself.
	sc.Buffer(make([]byte, 64*1024), maxRead)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, marker) {
			continue
		}
		var e entry
		if json.Unmarshal(line, &e) != nil || e.Type != "user" || e.IsSidechain {
			continue
		}
		if !interrupt(e.Message.Content) {
			continue
		}
		// The last one written, not the latest stamped: a line stamped in the
		// future -- a clock stepped back, or a line somebody wrote there --
		// would otherwise hide every real interrupt after it.
		if at, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
			last = at
		}
	}
	return last
}

// interrupt reports whether a user entry's content is the interrupt marker
// itself, as opposed to a prompt or a tool result that quotes it -- somebody
// pasting this package's comment into a session, say.
func interrupt(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return bytes.HasPrefix([]byte(text), marker)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) != 1 {
		return false
	}
	return blocks[0].Type == "text" && bytes.HasPrefix([]byte(blocks[0].Text), marker)
}

const (
	// initialTail is how much of a transcript is read the first time it is
	// seen. The interrupt that matters is the latest, and a long session's
	// transcript is tens of megabytes.
	initialTail = 256 << 10
	// maxRead bounds one read of what was appended.
	maxRead = 4 << 20
)

// Watcher follows one transcript per session, reading only what was appended
// since the last call. Safe for concurrent use; meant to be called from the
// poller.
type Watcher struct {
	mu   sync.Mutex
	tail map[string]*tail
}

type tail struct {
	path   string
	offset int64
	last   time.Time
}

// Interrupted returns when the session's transcript last recorded an
// interrupt, and false if it never has or the file cannot be read.
func (w *Watcher) Interrupted(sessionID, path string) (time.Time, bool) {
	if path == "" {
		return time.Time{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tail == nil {
		w.tail = map[string]*tail{}
	}
	t := w.tail[sessionID]
	// /clear and --resume start a new transcript; what the old one said is
	// about a conversation that is over.
	if t == nil || t.path != path {
		t = &tail{path: path, offset: -1}
		w.tail[sessionID] = t
	}
	// Opened without blocking and without taking a controlling terminal. The
	// path arrived in a hook document: a FIFO blocks open(2) until something
	// writes to it, and a terminal device can become the panel's controlling
	// terminal. This runs on the poller, which is what keeps every session's
	// state current. Nothing checks the file is regular: what is read is
	// ReadAt, which a FIFO or a terminal refuses, and a device's size is zero.
	// A Stat before the Open was the first version, and a window in which a
	// FIFO could be swapped in.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return t.last, !t.last.IsZero()
	}
	defer f.Close() //nolint:errcheck // read-only
	info, err := f.Stat()
	if err != nil {
		return t.last, !t.last.IsZero()
	}
	// First sight, a truncated file, or a transcript nobody looked at while
	// its session sat at done and that has since grown past a whole tail: start
	// from the tail. An interrupt older than that is older than the report it
	// would have to beat.
	if t.offset < 0 || info.Size() < t.offset || info.Size()-t.offset > initialTail {
		t.offset = max(0, info.Size()-initialTail)
		if t.offset > 0 && !startsLine(f, t.offset) {
			// Past the partial line the tail starts in the middle of, reading
			// no further than the tail itself.
			skipped, _ := bufio.NewReader(io.LimitReader(io.NewSectionReader(f, t.offset, initialTail), initialTail)).ReadBytes('\n')
			t.offset += int64(len(skipped))
		}
	}
	if info.Size() > t.offset {
		n := min(info.Size()-t.offset, maxRead)
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, t.offset); err == nil || err == io.EOF {
			// Only whole lines: the agent may be halfway through writing one.
			if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
				t.last = Scan(bytes.NewReader(buf[:i+1]), t.last)
				t.offset += int64(i + 1)
			} else if n == maxRead {
				// One line longer than a whole read. Nothing in it is an
				// interrupt worth the memory; step over it rather than stall
				// here forever.
				t.offset += n
			}
		}
	}
	return t.last, !t.last.IsZero()
}

// startsLine reports whether offset is the first byte of a line.
func startsLine(f *os.File, offset int64) bool {
	b := make([]byte, 1)
	_, err := f.ReadAt(b, offset-1)
	return err == nil && b[0] == '\n'
}

// Retain drops the transcripts of every session not in ids.
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
