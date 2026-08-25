package ws

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The message names live here and again in the TypeScript, and nothing checked
// that the two agreed.
//
// The failure is silent in the worst direction: a message the server sends and
// the client's switch has no case for is discarded without an error anywhere.
// That is exactly how a viewer cut off for falling behind would stop
// recovering — the server says "dropped", the client hears nothing, and the
// terminal sits frozen looking like a network problem.
//
// One drift already existed when this was written: "panel" had been added to
// the client union and sent as a bare string from the server, with no constant
// beside its siblings.
func TestMessageTypesMatchTheClient(t *testing.T) {
	const path = "../../web/src/protocol/wire.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s, so the two ends were not compared: %v", path, err)
	}

	// Each union runs from the `t:` line to the closing brace of its interface.
	union := func(iface string) []string {
		t.Helper()
		start := strings.Index(string(src), "export interface "+iface)
		if start < 0 {
			t.Fatalf("%s not found in %s; this test is no longer comparing anything",
				iface, path)
		}
		end := strings.Index(string(src)[start:], "\n}")
		if end < 0 {
			t.Fatalf("could not find the end of %s", iface)
		}
		body := string(src)[start : start+end]
		// Only quotes that follow the `t:` or a `|` continuation, so an
		// apostrophe in a comment — "a project's note" sits inside one of these
		// interfaces already — cannot be read as a member name.
		var out []string
		for _, m := range regexp.MustCompile(`[|:]\s*'([a-zA-Z]+)'`).FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
		if len(out) == 0 {
			t.Fatalf("%s parsed to no members; the shape of the file changed", iface)
		}
		return out
	}

	compare := func(what string, want []string, got []string) {
		t.Helper()
		seen := map[string]bool{}
		for _, g := range got {
			seen[g] = true
		}
		for _, w := range want {
			if !seen[w] {
				t.Errorf("%s: the server sends %q and the client has no case for it, "+
					"so it would be discarded in silence", what, w)
			}
		}
		known := map[string]bool{}
		for _, w := range want {
			known[w] = true
		}
		for _, g := range got {
			if !known[g] {
				t.Errorf("%s: the client expects %q and nothing here sends it", what, g)
			}
		}
	}

	compare("server → client", AllServerMessages, union("ServerMessage"))
	compare("client → server", AllClientMessages, union("ClientMessage"))
}

// The binary frame layout is defined twice as well, and drifts louder but no
// more traceably: a wrong header length shifts every terminal byte, a wrong
// frame type makes replay look like live output, a wrong byte order routes
// frames to streams that do not exist. All of them present as "the terminal is
// broken" rather than as a constant.
func TestBinaryFrameLayoutMatchesTheClient(t *testing.T) {
	const path = "../../web/src/protocol/wire.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	text := string(src)

	declared := func(name string) uint64 {
		t.Helper()
		m := regexp.MustCompile(name + `\s*=\s*(0[xX][0-9a-fA-F]+|\d+)`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("%s is not declared in %s", name, path)
		}
		// Base 0 so 0x00 and 5 are both understood, rather than requiring the
		// TypeScript to be written the way this test expects.
		v, perr := strconv.ParseUint(m[1], 0, 64)
		if perr != nil {
			t.Fatalf("%s = %q: %v", name, m[1], perr)
		}
		return v
	}

	for _, tc := range []struct {
		ts   string
		want uint64
	}{
		{"FRAME_DATA", uint64(FrameData)},
		{"FRAME_REPLAY", uint64(FrameReplay)},
		{"BINARY_HEADER_LEN", uint64(binaryHeaderLen)},
	} {
		if got := declared(tc.ts); got != tc.want {
			t.Errorf("%s is %d in %s and %d here", tc.ts, got, path, tc.want)
		}
	}

	// The reference is written big-endian. DataView takes littleEndian as its
	// second argument, so `true` there would swap the byte order silently and
	// every frame would be delivered to a stream that does not exist.
	if !strings.Contains(text, "getUint32(1, false)") {
		t.Error("wire.ts does not read the stream reference as big-endian; " +
			"the server writes it with binary.BigEndian")
	}
	if !strings.Contains(text, "setUint32(1, ref, false)") {
		t.Error("wire.ts does not write the stream reference as big-endian")
	}
}

func TestFramesSurviveTheRoundTrip(t *testing.T) {
	// The codec carries every keystroke and every byte a session prints, and
	// nothing exercised it. The layout is compared against the TypeScript
	// above; this is the other half — that the bytes mean what the layout says.
	for _, payload := range [][]byte{
		nil,
		{},
		[]byte("y\r"),
		[]byte("\x1b[?1049h\x1b[2J"),
		bytes.Repeat([]byte("agent output "), 5000),
	} {
		for _, tc := range []struct {
			name   string
			encode func(uint32, []byte) []byte
			kind   byte
		}{
			{"data", EncodeData, FrameData},
			{"replay", EncodeReplay, FrameReplay},
		} {
			frame := tc.encode(0xDEADBEEF, payload)
			if frame[0] != tc.kind {
				t.Errorf("%s frame starts with %#x, want %#x", tc.name, frame[0], tc.kind)
			}
			ref, got, err := DecodeData(frame)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if ref != 0xDEADBEEF {
				t.Errorf("%s: ref = %#x", tc.name, ref)
			}
			if !bytes.Equal(got, payload) && !(len(got) == 0 && len(payload) == 0) {
				t.Errorf("%s: payload of %d bytes came back as %d", tc.name, len(payload), len(got))
			}
		}
	}
}

func TestReplayIsDistinguishableFromLiveOutput(t *testing.T) {
	// Not a detail. The replay buffer holds whatever the application sent,
	// including terminal capability queries, and a freshly created xterm
	// answers those as it parses them — at the shell prompt of whatever the
	// session was doing. The flag is the only thing that stops a page reload
	// typing "[?1;2c" into an agent.
	live := EncodeData(1, []byte("x"))
	replayed := EncodeReplay(1, []byte("x"))
	if live[0] == replayed[0] {
		t.Fatal("live output and replayed scrollback are the same frame type")
	}
	if _, _, err := DecodeData(replayed); err != nil {
		t.Fatalf("a replay frame does not decode: %v", err)
	}
}

func TestAMalformedFrameIsRefusedRatherThanRead(t *testing.T) {
	// A frame shorter than its header, read anyway, is a slice out of range on
	// the goroutine that carries a viewer's terminal.
	for _, frame := range [][]byte{nil, {}, {FrameData}, {FrameData, 0, 0, 0}} {
		if _, _, err := DecodeData(frame); !errors.Is(err, ErrShortFrame) {
			t.Errorf("a %d-byte frame gave %v, want ErrShortFrame", len(frame), err)
		}
	}
	if _, _, err := DecodeData([]byte{0x7f, 0, 0, 0, 1, 'x'}); err == nil {
		t.Error("an unknown frame type was accepted; its payload would be written to a terminal")
	}
}
