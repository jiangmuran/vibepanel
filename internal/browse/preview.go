package browse

import (
	"bytes"
	"encoding/binary"
	"strings"
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
	KindVideo  Kind = "video"
	KindAudio  Kind = "audio"
	// KindArchive and KindSQLite are listed by their own routes, never
	// served: a table of contents and a database's tables, read on the
	// server (archive.go, sqlite.go).
	KindArchive Kind = "archive"
	KindSQLite  Kind = "sqlite"
)

// Kinds is every kind the preview route answers with, in the order the client
// lists them: TestThePreviewKindsMatchTheClient compares it with PreviewKind
// in web/src/protocol/wire.ts, so a kind added here is a kind the panel was
// told about.
var Kinds = []Kind{KindText, KindBinary, KindImage, KindPDF, KindVideo, KindAudio, KindArchive, KindSQLite}

// SniffMedia is SniffMagic and then the file viewer's further kinds: video,
// audio, two image formats SniffMagic leaves out, and the archives and
// databases the viewer lists. A separate function so
// SniffMagic keeps answering exactly what it did -- share pages decide what a
// bundle may hold with it (internal/pages), and a viewer's wider list is not
// a reason for a page to accept a video.
//
// Every answer is a media type from this function's own list, and the inline
// route serves nothing else as anything but octet-stream
// (TestTheInlineRouteNamesOnlyMediaTypes). An archive and a database answer
// with no media type at all: they are read by their listing routes, and the
// inline route serves them as the download it serves everything else as.
func SniffMedia(head []byte, size int64) (Kind, string) {
	if k, mime := SniffMagic(head); k != KindBinary {
		return k, mime
	}
	switch {
	case IsArchive(head):
		return KindArchive, ""
	case IsSQLite(head):
		return KindSQLite, ""
	// ICO: reserved zero, type 1 (icon), and a count that is not zero.
	case len(head) >= 6 && bytes.HasPrefix(head, []byte{0, 0, 1, 0}) && (head[4] != 0 || head[5] != 0):
		return KindImage, "image/x-icon"
	// BMP: "BM" alone is how plenty of text begins (SniffMagic's comment), so
	// the header's own file size must be the file's size as well.
	case len(head) >= 14 && bytes.HasPrefix(head, []byte("BM")) &&
		int64(binary.LittleEndian.Uint32(head[2:6])) == size:
		return KindImage, "image/bmp"
	case bytes.HasPrefix(head, []byte("fLaC")):
		return KindAudio, "audio/flac"
	case bytes.HasPrefix(head, []byte("ID3")):
		return KindAudio, "audio/mpeg"
	// An MPEG audio frame with no ID3 tag: eleven sync bits, Layer III -- what
	// an .mp3 is -- and a bitrate and a sample rate that are not the reserved
	// values. Any layer at all was the first rule, and a UTF-16 text file's
	// byte-order mark, FF FE, is a "Layer I frame" by it: the viewer opened
	// one as audio.
	case len(head) >= 3 && head[0] == 0xff && head[1]&0xe0 == 0xe0 && head[1]&0x06 == 0x02 &&
		head[2]>>4 != 0x0f && head[2]>>4 != 0 && (head[2]>>2)&0x03 != 0x03:
		return KindAudio, "audio/mpeg"
	case bytes.HasPrefix(head, []byte("\x1a\x45\xdf\xa3")):
		// EBML: WebM, or Matroska, which browsers play as WebM.
		return KindVideo, "video/webm"
	case bytes.HasPrefix(head, []byte("OggS")):
		// The first page names its codec: Opus or Vorbis is audio, Theora video.
		if bytes.Contains(head, []byte("theora")) {
			return KindVideo, "video/ogg"
		}
		return KindAudio, "audio/ogg"
	}
	if len(head) >= 12 {
		if bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WAVE")) {
			return KindAudio, "audio/wav"
		}
		if bytes.Equal(head[4:8], []byte("ftyp")) {
			switch string(head[8:12]) {
			case "M4A ", "M4B ":
				return KindAudio, "audio/mp4"
			case "qt  ":
				return KindVideo, "video/quicktime"
			default:
				// isom, mp41, mp42, avc1, iso2..iso6, dash, M4V: the MP4 family.
				return KindVideo, "video/mp4"
			}
		}
	}
	return KindBinary, ""
}

// MediaTypes is every media type SniffMedia can answer with. The inline
// route's whole allow list: TestTheInlineRouteNamesOnlyMediaTypes walks it.
var MediaTypes = []string{
	"image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/x-icon", "image/bmp",
	"application/pdf",
	"audio/flac", "audio/mpeg", "audio/ogg", "audio/wav", "audio/mp4",
	"video/webm", "video/ogg", "video/quicktime", "video/mp4",
}

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

// Readable is IsText for the viewer: text, and not mostly control characters.
// A separate function so IsText answers what it always did -- share pages
// decide what a text asset may be with it.
func Readable(b []byte) bool {
	return IsText(b) && !controlHeavy(b)
}

// controlHeavy is the other half of "nothing anybody reads": a buffer whose
// opening is mostly C0 control characters. A file with no NUL in it is
// otherwise text however few of its bytes are printable, and the viewer drew
// nine megabytes of 0x07 as two megabytes of U+FFFD on one line. More than
// three in ten of the first 8 KiB, not counting the ones text is made of --
// tab, newline, carriage return, form feed, and escape for a terminal log's
// colours -- and it is shown as the bytes it is.
func controlHeavy(b []byte) bool {
	if len(b) > 8<<10 {
		b = b[:8<<10]
	}
	if len(b) == 0 {
		return false
	}
	n := 0
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\f' && c != 0x1b {
			n++
		}
	}
	return n*10 > len(b)*3
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

// Markup is a file the panel can offer to *draw* as a document rather than
// show as text.
//
// Deliberately the only place in this package that reads a filename, and the
// exception is worth arguing rather than hiding. Everywhere above, the name
// lies: a log called output.dat is text and notes.txt holding a tarball is not,
// so the bytes decide. Here the question is different. "Is this file meant to
// be a page" is a statement of *intent*, and intent is what an extension
// records -- an HTML fragment with no <html> in it is still an HTML file, and
// a .txt full of angle brackets is not one however much it parses.
//
// What makes reading the name acceptable is that nothing rests on it. The
// bytes are served into an iframe with an opaque origin, no scripts unless
// somebody asked for them, and a Content-Security-Policy that permits no
// network of any kind -- so guessing wrong renders something odd inside a box,
// not something dangerous next to the session cookie. See handleRenderPreview
// for the whole design.
type Markup string

// The three states. Empty means "show it as text", which is what everything
// that is not one of the two below stays.
const (
	MarkupNone Markup = ""
	MarkupHTML Markup = "html"
	MarkupSVG  Markup = "svg"
)

// SniffMarkup decides whether a file can be drawn, and as what.
//
// The media type comes back from here rather than from the file, and it is one
// of exactly two strings. That is the property worth keeping: whatever else
// changes, the panel can only ever be persuaded to serve a project's bytes as
// text/html or as image/svg+xml, both of which the isolation covers.
func SniffMarkup(name string, head []byte) (Markup, string) {
	if !IsText(head) {
		return MarkupNone, ""
	}
	dot := strings.LastIndexByte(name, '.')
	// `<= 0`, not `< 0`: a leading dot is not an extension. A file called
	// `.html` is a dotfile somebody's tooling wrote, not a page, and treating
	// it as one is the panel deciding to render a config file as a document.
	if dot <= 0 {
		return MarkupNone, ""
	}
	switch strings.ToLower(name[dot:]) {
	case ".html", ".htm", ".xhtml":
		// XHTML as text/html on purpose. Served as application/xhtml+xml a
		// single unclosed tag anywhere in the file is a yellow XML parse error
		// instead of a preview, and a preview that refuses malformed documents
		// is useless against files an agent is halfway through writing.
		return MarkupHTML, "text/html; charset=utf-8"
	case ".svg":
		return MarkupSVG, "image/svg+xml"
	}
	return MarkupNone, ""
}
