package vnc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

// The client messages this proxy has to be able to measure, built by name so a
// test reads as the protocol rather than as a hex dump.
func setPixelFormat() []byte { return append([]byte{0}, make([]byte, 19)...) }

func setEncodings(n int) []byte {
	out := []byte{2, 0, 0, 0}
	binary.BigEndian.PutUint16(out[2:], uint16(n))
	return append(out, make([]byte, 4*n)...)
}

func framebufferUpdateRequest() []byte { return append([]byte{3}, make([]byte, 9)...) }
func keyEvent() []byte                 { return append([]byte{4}, make([]byte, 7)...) }
func pointerEvent() []byte             { return append([]byte{5}, make([]byte, 5)...) }

func clientCutText(text string) []byte {
	out := make([]byte, 8)
	out[0] = 6
	binary.BigEndian.PutUint32(out[4:], uint32(len(text)))
	return append(out, []byte(text)...)
}

func extendedCutText(n int) []byte {
	out := make([]byte, 8)
	out[0] = 6
	binary.BigEndian.PutUint32(out[4:], uint32(int32(-n)))
	return append(out, make([]byte, n)...)
}

func clientFence(payload int) []byte {
	out := make([]byte, 9)
	out[0] = 248
	out[8] = byte(payload)
	return append(out, make([]byte, payload)...)
}

func setDesktopSize(screens int) []byte {
	out := make([]byte, 8)
	out[0] = 251
	out[6] = byte(screens)
	return append(out, make([]byte, 16*screens)...)
}

func qemuKeyEvent() []byte { return append([]byte{255, 0}, make([]byte, 10)...) }
func xvpOp() []byte        { return []byte{250, 0, 1, 2} }

func filtered(t *testing.T, stream []byte) ([]byte, error) {
	t.Helper()
	var out bytes.Buffer
	err := filterClient(&out, bytes.NewReader(stream))
	return out.Bytes(), err
}

// View-only is enforced at the proxy, not by the client.
//
// A noVNC `viewOnly` property is a promise made by the thing being restrained;
// this is the keystroke not reaching the display. Every message that types,
// clicks, pastes, resizes or powers off is on the dropped side.
func TestViewOnlyDropsEveryKindOfInput(t *testing.T) {
	var stream []byte
	for _, m := range [][]byte{
		setPixelFormat(),
		keyEvent(),
		setEncodings(3),
		pointerEvent(),
		framebufferUpdateRequest(),
		clientCutText("paste me into the agent's terminal"),
		setDesktopSize(2),
		qemuKeyEvent(),
		xvpOp(),
		clientFence(4),
		framebufferUpdateRequest(),
	} {
		stream = append(stream, m...)
	}

	got, err := filtered(t, stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("filterClient: %v", err)
	}

	want := bytes.Join([][]byte{
		setPixelFormat(),
		setEncodings(3),
		framebufferUpdateRequest(),
		clientFence(4),
		framebufferUpdateRequest(),
	}, nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("view-only forwarded %d bytes, want %d\n got %v\nwant %v",
			len(got), len(want), got, want)
	}
	// Named individually as well, because a length mistake in one of the
	// variable-length messages shifts everything after it and the byte
	// comparison above would then be failing for the wrong reason.
	for name, msg := range map[string][]byte{
		"a keystroke":    keyEvent(),
		"a pointer move": pointerEvent(),
		"a resize":       setDesktopSize(2),
		"a power button": xvpOp(),
	} {
		if bytes.Contains(got, msg) {
			t.Errorf("%s was forwarded to a view-only display", name)
		}
	}
	if bytes.Contains(got, []byte("paste me into the agent's terminal")) {
		t.Error("a clipboard paste was forwarded to a view-only display")
	}
}

// The extended clipboard sends a negative length whose magnitude is the byte
// count. Reading it as an unsigned number is a four-gigabyte allocation and a
// stream that never recovers.
func TestTheExtendedClipboardLengthIsReadAsSigned(t *testing.T) {
	stream := append(extendedCutText(16), framebufferUpdateRequest()...)
	got, err := filtered(t, stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("filterClient: %v", err)
	}
	if !bytes.Equal(got, framebufferUpdateRequest()) {
		t.Errorf("after an extended clipboard message the stream is out of step: %v", got)
	}
}

// A message this proxy cannot measure ends the connection.
//
// Skipping it is the tempting alternative and it is wrong: once the length is
// unknown the parser has lost its place, and everything after that is bytes
// being classified by a proxy that no longer knows where a message starts.
func TestAnUnmeasurableMessageEndsTheConnection(t *testing.T) {
	stream := append([]byte{99, 0, 0, 0}, framebufferUpdateRequest()...)
	got, err := filtered(t, stream)
	if !errors.Is(err, ErrClientMessage) {
		t.Fatalf("filterClient gave %v, want ErrClientMessage", err)
	}
	if len(got) != 0 {
		t.Errorf("%d bytes were forwarded past a message the proxy could not read", len(got))
	}
}

func TestAnUnknownQemuSubMessageEndsTheConnection(t *testing.T) {
	stream := []byte{255, 7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := filtered(t, stream); !errors.Is(err, ErrClientMessage) {
		t.Fatalf("filterClient gave %v, want ErrClientMessage", err)
	}
}

// A length the browser chooses must not be an allocation this process makes on
// request.
func TestAnAbsurdClipboardLengthIsRefused(t *testing.T) {
	out := make([]byte, 8)
	out[0] = 6
	binary.BigEndian.PutUint32(out[4:], uint32(maxClientMessage+1))
	if _, err := filtered(t, out); !errors.Is(err, ErrClientMessage) {
		t.Fatalf("filterClient gave %v, want ErrClientMessage", err)
	}
}

// Without view-only the panel is a pipe, and input has to arrive unchanged.
func TestAWritableDisplayPassesInputThrough(t *testing.T) {
	upstreamA, upstreamB := netPipe(t)
	browserA, browserB := netPipe(t)

	done := make(chan error, 1)
	go func() { done <- Proxy(upstreamA, browserA, false) }()

	if _, err := browserB.Write(keyEvent()); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got [8]byte
	if _, err := io.ReadFull(upstreamB, got[:]); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got[:], keyEvent()) {
		t.Errorf("the keystroke arrived as %v", got)
	}
	browserB.Close()
	upstreamB.Close()
	<-done
}

// And with view-only, the same keystroke does not arrive at all -- proven
// through the real Proxy rather than only through filterClient, because
// "viewOnly" reaching the filter is itself a wire somebody can cut.
func TestAViewOnlyDisplayNeverReceivesTheKeystroke(t *testing.T) {
	upstreamA, upstreamB := netPipe(t)
	browserA, browserB := netPipe(t)

	done := make(chan error, 1)
	go func() { done <- Proxy(upstreamA, browserA, true) }()

	// A keystroke, then something that is allowed. Only the second may arrive,
	// and reading it is what proves the first was dropped rather than merely
	// delayed.
	if _, err := browserB.Write(append(keyEvent(), framebufferUpdateRequest()...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got [10]byte
	if _, err := io.ReadFull(upstreamB, got[:]); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got[:], framebufferUpdateRequest()) {
		t.Errorf("the first message through to a view-only display was %v, want the update "+
			"request; the keystroke was forwarded", got)
	}
	browserB.Close()
	upstreamB.Close()
	<-done
}

// Server-to-browser is a copy in both modes: view-only restrains the person at
// the browser, not the display.
func TestTheDisplayReachesTheBrowserInViewOnly(t *testing.T) {
	upstreamA, upstreamB := netPipe(t)
	browserA, browserB := netPipe(t)

	done := make(chan error, 1)
	go func() { done <- Proxy(upstreamA, browserA, true) }()

	if _, err := upstreamB.Write([]byte("FRAMEBUFFER")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got [11]byte
	if _, err := io.ReadFull(browserB, got[:]); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got[:]) != "FRAMEBUFFER" {
		t.Errorf("the browser received %q", got)
	}
	browserB.Close()
	upstreamB.Close()
	<-done
}

// netPipe is net.Pipe with the ends closed when the test finishes, so a
// goroutine left blocked in a Read does not outlive the test that made it.
func netPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() {
		a.Close()
		b.Close()
	})
	return a, b
}
