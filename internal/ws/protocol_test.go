package ws

import (
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
