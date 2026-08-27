package vnc

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// maxClientMessage bounds one message from the browser.
//
// Only reached in view-only mode, which is the only mode that parses this
// direction. The length that can be large is ClientCutText's, and it is a u32
// the browser chooses; without a bound a paste claiming four gigabytes is an
// allocation this process makes on request. Four mebibytes is a clipboard
// nobody has.
const maxClientMessage = 4 << 20

// ErrClientMessage means the browser sent something this proxy cannot measure.
var ErrClientMessage = errors.New("vnc: unreadable client message")

// Proxy copies RFB between the browser and the server until either end stops.
//
// After Handshake there is nothing to understand in this direction: RFB from
// ClientInit onwards is the same in every version, and the encodings that make
// a desktop legible over a phone connection -- Tight, ZRLE, JPEG -- are
// negotiated between the two ends without the panel having an opinion. That is
// the point of being a proxy rather than a client: the panel does not decode a
// single pixel, and a new encoding shipped in a VNC server works here the day
// it ships.
//
// The one exception is viewOnly, which cannot be anywhere else. See
// filterClient.
func Proxy(upstream, browser io.ReadWriter, viewOnly bool) error {
	errs := make(chan error, 2)
	go func() {
		_, err := io.Copy(browser, upstream)
		errs <- err
	}()
	go func() {
		if viewOnly {
			errs <- filterClient(upstream, browser)
			return
		}
		_, err := io.Copy(upstream, browser)
		errs <- err
	}()
	// The first end to stop ends the connection. The caller closes both
	// sockets, which unblocks the other goroutine -- without that it would sit
	// in a Read on a socket nobody will ever write to again, for the life of
	// the process.
	err := <-errs
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// filterClient copies browser-to-server messages, dropping the ones that are
// input.
//
// View-only is enforced here and not in the browser, and the difference is the
// same one red line 8 is about. noVNC has a viewOnly property; setting it is a
// line of JavaScript that anybody with the devtools open can unset, and more
// to the point it is a promise made by the thing being restrained rather than
// by the thing doing the restraining. The stored row says view-only, the
// server reads the row, and the keystroke never reaches the display.
//
// The cost is that this direction has to be framed, which means this function
// knows the length of every client message. Getting one length wrong
// desynchronises the stream, so an unmeasurable message ends the connection
// rather than being skipped: a proxy that has lost its place must not go on
// forwarding bytes it can no longer classify.
func filterClient(dst io.Writer, src io.Reader) error {
	br := bufio.NewReaderSize(src, 16<<10)
	for {
		head, err := br.Peek(1)
		if err != nil {
			return err
		}
		n, input, err := clientMessageSpan(br, head[0])
		if err != nil {
			return err
		}
		if n > maxClientMessage {
			return fmt.Errorf("%w: type %d claims %d bytes", ErrClientMessage, head[0], n)
		}
		msg := make([]byte, n)
		if _, err := io.ReadFull(br, msg); err != nil {
			return err
		}
		if input {
			continue
		}
		if _, err := dst.Write(msg); err != nil {
			return err
		}
	}
}

// clientMessageSpan reports how many bytes the message at the head of br
// occupies, and whether it carries input.
//
// "Input" is wider than key and pointer events on purpose. A clipboard paste
// types into the remote machine as surely as a keystroke does; a resize
// rearranges somebody else's screen; xvp is a power button. None of them
// belongs on a connection whose stored row says the person is watching.
func clientMessageSpan(br *bufio.Reader, typ byte) (int, bool, error) {
	peek := func(n int) ([]byte, error) {
		b, err := br.Peek(n)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	switch typ {
	case 0: // SetPixelFormat
		return 20, false, nil
	case 2: // SetEncodings
		h, err := peek(4)
		if err != nil {
			return 0, false, err
		}
		return 4 + 4*int(binary.BigEndian.Uint16(h[2:4])), false, nil
	case 3: // FramebufferUpdateRequest
		return 10, false, nil
	case 4: // KeyEvent
		return 8, true, nil
	case 5: // PointerEvent
		return 6, true, nil
	case 6: // ClientCutText
		h, err := peek(8)
		if err != nil {
			return 0, false, err
		}
		// A negative length is the extended clipboard extension saying the
		// payload is compressed; the magnitude is still the byte count.
		n := int32(binary.BigEndian.Uint32(h[4:8]))
		if n < 0 {
			n = -n
		}
		return 8 + int(n), true, nil
	case 150: // EnableContinuousUpdates
		return 10, false, nil
	case 248: // ClientFence
		h, err := peek(9)
		if err != nil {
			return 0, false, err
		}
		// Forwarded, not dropped. A fence is a synchronisation reply the
		// server asked for; swallowing it leaves a server waiting for an
		// answer that is never coming, which looks exactly like the frozen
		// display this feature has a separate indicator for.
		return 9 + int(h[8]), false, nil
	case 250: // xvpOp -- shut down, reboot, reset
		return 4, true, nil
	case 251: // SetDesktopSize
		h, err := peek(7)
		if err != nil {
			return 0, false, err
		}
		return 8 + 16*int(h[6]), true, nil
	case 255: // QEMU client message
		h, err := peek(2)
		if err != nil {
			return 0, false, err
		}
		if h[1] != 0 { // 0 is the extended key event; nothing else is defined
			return 0, false, fmt.Errorf("%w: QEMU sub-message %d", ErrClientMessage, h[1])
		}
		return 12, true, nil
	}
	return 0, false, fmt.Errorf("%w: type %d", ErrClientMessage, typ)
}
