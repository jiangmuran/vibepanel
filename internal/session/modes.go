package session

import (
	"bytes"
	"sort"
	"strconv"
)

// modeTracker follows the DEC private modes the pane has turned on, so a
// viewer that joins late can be told about them.
//
// tmux tells its client about a pane's modes once, when they change. The
// ring holds the last two megabytes of what the client was told, and a
// full-screen program redraws the whole screen on every wheel notch, so after
// an afternoon the `ESC[?1002h` it sent at startup has been evicted. A viewer
// subscribing then -- a new tab, a reload, a reconnect whose resume point is
// gone too -- gets the screen and not the modes: xterm does not know the pane
// wants the mouse, the wheel scrolls xterm's own scrollback, clicks select
// text, a paste arrives as typing. Measured: after exactly 2,097,152 bytes of
// redraws a fresh tab sent no mouse report at all.
//
// The tracker reads the stream the ring stores. A snapshot that does not
// continue the viewer's own stream is prefixed with ESC[?Nh for every mode
// still set; one that continues is not, because that viewer saw the original.
type modeTracker struct {
	// The last explicit setting of each mode seen: true for h, false for l.
	state map[int]bool
	// The tail of the last chunk from its final ESC, when a sequence was cut
	// by the read boundary. Bounded: a lone ESC followed by a page of text
	// is text, not a sequence, and is dropped.
	pending []byte
}

// Modes never replayed. The alternate screen, because tmux never sends it to
// this client (vibepanel.conf, smcup@) and a stray one would put the whole
// replay on a buffer with no scrollback. Synchronized output, because it
// brackets single frames and a leading 2026h would hold the replay back.
var modesNotReplayed = map[int]bool{47: true, 1047: true, 1048: true, 1049: true, 2026: true}

// Modes a terminal starts with on, so the state worth replaying is their
// reset: a hidden cursor (25) and wrap turned off (7). Everything else starts
// off and is replayed when set.
var modesDefaultOn = map[int]bool{7: true, 25: true}

const maxPendingMode = 64

func newModeTracker() *modeTracker { return &modeTracker{state: map[int]bool{}} }

// feed scans a chunk of pane output for CSI ? Pm h / l.
func (m *modeTracker) feed(chunk []byte) {
	b := chunk
	if len(m.pending) > 0 {
		b = append(m.pending, chunk...)
		m.pending = nil
	}
	for {
		i := bytes.IndexByte(b, 0x1b)
		if i < 0 {
			return
		}
		b = b[i:]
		n, params, final, complete := parsePrivateMode(b)
		if !complete {
			if len(b) <= maxPendingMode {
				m.pending = append([]byte(nil), b...)
			}
			return
		}
		if n == 0 {
			// An ESC that starts something else. Skip it.
			b = b[1:]
			continue
		}
		for _, p := range params {
			m.state[p] = final == 'h'
		}
		b = b[n:]
	}
}

// parsePrivateMode reads one CSI ? Pm h/l at the start of b. n is 0 when b
// starts with an ESC that is not one; complete is false when b ends before
// the sequence could be decided.
func parsePrivateMode(b []byte) (n int, params []int, final byte, complete bool) {
	if len(b) < 3 {
		// "\x1b" or "\x1b[" could still become one; anything else this short
		// cannot.
		return 0, nil, 0, !bytes.HasPrefix([]byte("\x1b[?"), b)
	}
	if b[1] != '[' || b[2] != '?' {
		return 0, nil, 0, true
	}
	i := 3
	num := -1
	for ; i < len(b); i++ {
		c := b[i]
		switch {
		case c >= '0' && c <= '9':
			if num < 0 {
				num = 0
			}
			num = num*10 + int(c-'0')
			if num > 99999 {
				return 0, nil, 0, true
			}
		case c == ';':
			if num >= 0 {
				params = append(params, num)
			}
			num = -1
		case c == 'h' || c == 'l':
			if num >= 0 {
				params = append(params, num)
			}
			return i + 1, params, c, true
		default:
			// Some other CSI with a ? prefix, or garbage.
			return 0, nil, 0, true
		}
	}
	return 0, nil, 0, false
}

// prefix is the sequence that puts a fresh terminal into the pane's current
// modes, lowest mode first: every mode whose last setting is not what a
// terminal starts with. Empty when nothing differs.
func (m *modeTracker) prefix() []byte {
	modes := make([]int, 0, len(m.state))
	for mode, on := range m.state {
		if !modesNotReplayed[mode] && on != modesDefaultOn[mode] {
			modes = append(modes, mode)
		}
	}
	if len(modes) == 0 {
		return nil
	}
	sort.Ints(modes)
	var out []byte
	for _, mode := range modes {
		out = append(out, "\x1b[?"...)
		out = strconv.AppendInt(out, int64(mode), 10)
		if m.state[mode] {
			out = append(out, 'h')
		} else {
			out = append(out, 'l')
		}
	}
	return out
}
