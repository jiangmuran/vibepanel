package session

import "sync"

// DefaultRingSize is how much recent output each session keeps for replay.
//
// 2 MiB holds far more than a screen: it is scrollback for a browser that
// reconnects. Bigger buffers cost memory times the number of live sessions
// (this user runs ~15), and tmux already keeps the authoritative 20k-line
// history that the cold path reads with capture-pane.
const DefaultRingSize = 2 << 20

// RingBuffer keeps the last N bytes a session produced, so a browser that
// connects — or reconnects — sees what it missed instead of a blank terminal.
//
// It stores raw PTY bytes, not decoded text: replaying the exact byte stream is
// what makes colours, cursor position and alternate-screen state come back
// correctly. Anything that parsed and re-rendered would have to reimplement a
// terminal emulator to do it faithfully.
type RingBuffer struct {
	mu       sync.RWMutex
	buf      []byte
	size     int // bytes currently held
	start    int // index of the oldest byte
	total    int64
	overflow bool // true once the buffer has wrapped and dropped data
}

// NewRingBuffer returns a buffer holding at most capacity bytes.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = DefaultRingSize
	}
	return &RingBuffer{buf: make([]byte, capacity)}
}

// Write appends p, evicting the oldest bytes when full. It never fails and
// never blocks on a reader: a slow browser must not be able to stall the PTY
// pump, because that would stall the agent behind it.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	r.total += int64(n)
	capacity := len(r.buf)

	// A write at least as large as the whole buffer: keep only its tail.
	if n >= capacity {
		// Only a write that is strictly larger, or one landing on top of
		// existing bytes, actually loses data. Filling an empty buffer exactly
		// drops nothing, and flagging it would make Snapshot trim a prefix that
		// is real output.
		if n > capacity || r.size > 0 {
			r.overflow = true
		}
		copy(r.buf, p[n-capacity:])
		r.start = 0
		r.size = capacity
		return n, nil
	}

	// Write at the tail, wrapping around the end.
	end := (r.start + r.size) % capacity
	first := copy(r.buf[end:], p)
	if first < n {
		copy(r.buf, p[first:])
	}

	if r.size+n > capacity {
		// Overwrote the oldest bytes; advance start past what was lost.
		r.start = (r.start + (r.size + n - capacity)) % capacity
		r.size = capacity
		r.overflow = true
	} else {
		r.size += n
	}
	return n, nil
}

// Snapshot returns the buffered bytes in order, ready to replay into a fresh
// terminal.
//
// When the buffer has wrapped, the first bytes are whatever happened to survive
// eviction — very possibly the middle of an escape sequence. Replaying that
// makes the terminal print the tail of a CSI sequence as literal garbage on the
// first line. trimPartialEscape drops it.
func (r *RingBuffer) Snapshot() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]byte, r.size)
	capacity := len(r.buf)
	first := copy(out, r.buf[r.start:min(r.start+r.size, capacity)])
	if first < r.size {
		copy(out[first:], r.buf[:r.size-first])
	}
	if r.overflow {
		out = trimPartialEscape(out)
	}
	return out
}

// Len reports how many bytes are currently buffered.
func (r *RingBuffer) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.size
}

// Total reports how many bytes have ever been written.
func (r *RingBuffer) Total() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.total
}

// Overflowed reports whether any data has been dropped. Callers use it to
// decide whether the in-memory replay is complete or whether they should fall
// back to capture-pane for the full scrollback.
func (r *RingBuffer) Overflowed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.overflow
}

// Reset empties the buffer. Used when a session is respawned into the same
// slot, so the new process does not inherit the dead one's output.
func (r *RingBuffer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.start, r.size, r.overflow = 0, 0, false
}

// escapeScanLimit bounds how far trimPartialEscape looks. An escape sequence
// that long is not a real one, and scanning further would risk eating actual
// output.
const escapeScanLimit = 128

// trimPartialEscape drops a leading fragment of an escape sequence.
//
// Heuristic, and deliberately so: knowing where a truncated stream sits inside
// the grammar of ANSI escapes needs a full parser, and being wrong by a few
// bytes on one line of restored scrollback matters far less than carrying one.
//
// Known false positive: output that genuinely begins with digits followed by a
// letter ("89abcdef") is indistinguishable from a truncated CSI sequence, and
// gets trimmed. Accepted — it costs a few characters on the first line of a
// buffer that has already lost data, and only after an overflow.
func trimPartialEscape(b []byte) []byte {
	limit := min(len(b), escapeScanLimit)

	// Locate the first unambiguous landmark. ESC starts a fresh sequence, a
	// newline cannot occur inside one, and BEL terminates an OSC.
	esc, newline, bel := -1, -1, -1
	for i := 0; i < limit; i++ {
		switch b[i] {
		case 0x1b:
			if esc < 0 {
				esc = i
			}
		case '\n':
			if newline < 0 {
				newline = i
			}
		case 0x07:
			if bel < 0 {
				bel = i
			}
		}
	}

	// Nothing before the first ESC, so the data starts cleanly.
	if esc == 0 {
		return b
	}

	// A BEL reached before any ESC or newline means we are inside an OSC
	// sequence whose introducer was evicted; its payload is arbitrary text, so
	// the CSI scan below would cut in the wrong place.
	if bel >= 0 && (esc < 0 || bel < esc) && (newline < 0 || bel < newline) {
		return b[bel+1:]
	}

	// CSI fragment: parameter bytes followed by a final byte. Require at least
	// one parameter byte — otherwise every line of ordinary text starting with
	// a letter looks like a terminator and loses its first character.
	scanEnd := limit
	for _, stop := range []int{esc, newline} {
		if stop >= 0 && stop < scanEnd {
			scanEnd = stop
		}
	}
	for i := 1; i < scanEnd; i++ {
		c := b[i]
		if c >= 0x40 && c <= 0x7e && allParameterBytes(b[:i]) {
			return b[i+1:]
		}
	}
	return b
}

// allParameterBytes reports whether every byte could appear inside the
// parameter or intermediate section of an escape sequence.
func allParameterBytes(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x3f {
			return false
		}
	}
	return true
}
