package browse

import (
	"unicode/utf16"
	"unicode/utf8"
)

// Encodings the viewer decodes on the server, named as its footer names them
// and as `X-Preview-Encoding` carries them.
const (
	EncodingUTF16LE = "UTF-16 LE"
	EncodingUTF16BE = "UTF-16 BE"
	EncodingLatin1  = "Latin-1"
)

// Decode is the viewer's second look at a file IsText refused: text in
// another encoding, turned into UTF-8 so the browser draws it like any other
// text. It answers ok only when what comes out is something a person reads.
//
// Two encodings, each decided by something the bytes say rather than a guess:
//
//   - UTF-16 with a byte-order mark. The mark is the evidence; without it,
//     UTF-16 is indistinguishable from a binary with a NUL every other byte,
//     and that is what it is taken for.
//   - Latin-1, for bytes that hold no NUL and are not valid UTF-8 -- `café`
//     saved by an old editor. Every byte is a character in Latin-1, so the
//     evidence is the other way round: what decodes must hardly ever be a
//     control character, C0 or C1 (0x80 to 0x9F). Text has next to none of
//     either. Readable's line, three in ten, was the first rule here and was
//     not enough: random bytes with the NULs taken out are about 23%
//     controls, under it, and were drawn as accented noise. At most one in
//     fifty, so a stray form feed or bell in a real file does not turn it
//     back into bytes.
//
// `more` is whether the buffer stops before the file does: an odd last byte
// or half a surrogate pair at the end is where the read stopped, not a fault
// in the file, and is dropped.
func Decode(b []byte, more bool) (text []byte, encoding string, ok bool) {
	if len(b) >= 2 && (b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF) {
		le := b[0] == 0xFF
		body := b[2:]
		if len(body)%2 == 1 {
			if !more {
				return nil, "", false
			}
			body = body[:len(body)-1]
		}
		units := make([]uint16, len(body)/2)
		for i := range units {
			if le {
				units[i] = uint16(body[2*i]) | uint16(body[2*i+1])<<8
			} else {
				units[i] = uint16(body[2*i])<<8 | uint16(body[2*i+1])
			}
		}
		if more && len(units) > 0 && units[len(units)-1] >= 0xD800 && units[len(units)-1] < 0xDC00 {
			units = units[:len(units)-1]
		}
		out := make([]byte, 0, len(units)*3/2)
		for _, r := range utf16.Decode(units) {
			out = utf8.AppendRune(out, r)
		}
		if !Readable(out) {
			return nil, "", false
		}
		enc := EncodingUTF16BE
		if le {
			enc = EncodingUTF16LE
		}
		return out, enc, true
	}
	if !latin1Text(b, more) {
		return nil, "", false
	}
	out := make([]byte, 0, len(b)+len(b)/4)
	for _, c := range b {
		out = utf8.AppendRune(out, rune(c))
	}
	return out, EncodingLatin1, true
}

// latin1Text is whether bytes that are not UTF-8 read as Latin-1: no NUL,
// and no more than one in fifty of the first 8 KiB a control, C0 or C1,
// beyond the ones text is made of.
//
// UTF-8 is not Latin-1, including UTF-8 the read cut in the middle of a
// character: that is invalid only where it stops, and drawing it as Latin-1
// would turn every accent in the file into two characters.
func latin1Text(b []byte, more bool) bool {
	whole := b
	if more {
		whole = trimPartialRune(b)
	}
	if len(b) == 0 || utf8.Valid(whole) {
		return false
	}
	head := b
	if len(head) > 8<<10 {
		head = head[:8<<10]
	}
	n := 0
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	for _, c := range head {
		if (c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\f' && c != 0x1b) || (c >= 0x7F && c < 0xA0) {
			n++
		}
	}
	return n*50 <= len(head)
}
