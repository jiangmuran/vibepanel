package browse

import (
	"bytes"
	"unicode/utf8"
)

// Kind is what a viewer can do with a file, decided from its bytes rather than
// from its name.
//
// From the bytes because the name lies both ways in a project directory: a log
// an agent wrote as `output.dat` is text, `notes.txt` holding a truncated
// tarball is not, and half the files worth reading (Makefile, Dockerfile, a
// shell script with no suffix) have no extension to consult at all. A table of
// extensions would need an entry for every language anyone runs an agent on.
type Kind string

const (
	// KindBinary is the honest answer, not a failure: there is a file, the
	// panel can hand it to you, and it will not pretend to show it.
	KindBinary Kind = "binary"
	KindText   Kind = "text"
	KindImage  Kind = "image"
	KindPDF    Kind = "pdf"
)

// SniffMagic identifies the formats a browser can draw, from the leading bytes.
//
// Deliberately a short list, and SVG is deliberately not on it. An SVG is a
// document with scripting in it, and the two ways to show one at a useful size
// -- an <object> or an <iframe> -- run that script on the panel's own origin,
// against a file that arrived in the project from whatever the agent was told
// to do. The text of an SVG is the more useful preview anyway, and that is what
// it falls through to.
//
// BMP is also missing on purpose: its magic is the two bytes "BM", which is
// also how a great many text files begin, and a heuristic that turns a README
// into a broken image is worse than one that shows it as text.
func SniffMagic(head []byte) (Kind, string) {
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return KindImage, "image/png"
	case bytes.HasPrefix(head, []byte("\xff\xd8\xff")):
		return KindImage, "image/jpeg"
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return KindImage, "image/gif"
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return KindPDF, "application/pdf"
	}
	// The two container formats, whose marker is not at offset zero. WebP is a
	// RIFF chunk and AVIF is an ISO-BMFF brand, so both need the length field
	// in between skipped rather than matched.
	if len(head) >= 12 {
		if bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")) {
			return KindImage, "image/webp"
		}
		if bytes.Equal(head[4:8], []byte("ftyp")) &&
			(bytes.Equal(head[8:12], []byte("avif")) || bytes.Equal(head[8:12], []byte("avis"))) {
			return KindImage, "image/avif"
		}
	}
	return KindBinary, ""
}

// IsText reports whether a buffer can be shown as text.
//
// The NUL test is git's, and it is the one heuristic in this area that has
// earned its keep: nothing anybody wants to read has a zero byte in it, and
// almost every compiled artefact has one within a few hundred bytes.
//
// The UTF-8 test is ours, and it costs something worth naming: a README in
// CP1252 or GB18030 is refused rather than shown. That is the deliberate trade.
// The panel would otherwise render it through TextDecoder's UTF-8 path, which
// replaces every byte it cannot make sense of, and a screen of U+FFFD reads as
// the panel being broken. "No preview, here is the file" is a smaller lie.
//
// The caller must clip before asking, or a buffer cut in the middle of a
// multi-byte character is reported as binary for a reason that is entirely the
// reader's own doing.
func IsText(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	return utf8.Valid(b)
}

// ClipText bounds what a text preview shows, and says whether it bit.
//
// `more` is whether the caller stopped reading before the end of the file; the
// byte budget itself belongs to the caller, because the point of it is to not
// read the rest.
//
// Two bounds rather than one, because bytes alone do not bound the work. A
// quarter of a megabyte of "a\n" is a quarter of a million lines, and a
// wrapped monospace block of those in a 280px column is hundreds of megabytes
// of layout boxes -- the browser stops answering, on a file the reader chose
// by clicking one row of a list. maxLines is what makes the cost of a click
// predictable.
func ClipText(b []byte, more bool, maxLines int) ([]byte, bool) {
	truncated := more
	if truncated {
		// Back to the last complete line. A preview that ends mid-line invites
		// the reader to believe the file does, which is the one thing a
		// truncated view must not do -- and the byte budget lands in the
		// middle of a line essentially every time.
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			b = b[:i+1]
		} else {
			// One enormous line, which is what a minified bundle is. Nothing
			// to cut back to, so cut back only far enough that the last
			// character is whole.
			b = trimPartialRune(b)
		}
	}
	if maxLines > 0 {
		if i := indexNthByte(b, '\n', maxLines); i >= 0 && i+1 < len(b) {
			b = b[:i+1]
			truncated = true
		}
	}
	return b, truncated
}

// trimPartialRune drops a multi-byte character that the budget cut in half.
//
// Without it the clipped buffer fails IsText -- and the file would be reported
// as binary because of where the reader stopped, which is a lie about the file.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		// size > 1 keeps a real U+FFFD, which decodes as RuneError too and is
		// an ordinary character somebody may have written on purpose.
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// indexNthByte returns the offset of the nth occurrence of c, or -1.
func indexNthByte(b []byte, c byte, n int) int {
	off := 0
	for n > 0 {
		i := bytes.IndexByte(b[off:], c)
		if i < 0 {
			return -1
		}
		off += i + 1
		n--
	}
	return off - 1
}
