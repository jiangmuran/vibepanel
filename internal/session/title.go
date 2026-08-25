package session

import "unicode/utf8"

// MaxTitleRunes bounds how much of a session title is kept.
//
// A title is whatever the application put in `OSC 0` or `OSC 2`, which means it
// is whatever an agent printed — and nothing was bounding it. Measured against
// the real binary: a pane emitting a 200,000-character title left tmux holding
// all 200,001 bytes of it, the database row holding 200,000, and the state
// snapshot growing from 705 bytes to 200,710.
//
// The snapshot is the part that matters. It is rebuilt every two seconds,
// compared, and broadcast to every connected viewer whenever it changed. A
// couple of dozen sessions doing this is megabytes pushed at every browser
// watching, including a phone on mobile data.
//
// None of it needs malice. `cat` a file with an escape sequence in it and this
// is what happens, which is a thing agents do to files they did not write.
//
// 256 is far past anything the sidebar shows and far short of anything that
// costs. Runes rather than bytes so that truncation cannot split a character
// and leave invalid UTF-8 in the database.
const MaxTitleRunes = 256

// TruncateTitle bounds a title, marking it when it had to cut.
//
// The marker is not decoration: a title that arrives exactly at the limit and
// one that was cut short read identically without it, and the difference is
// whether what you are looking at is the whole name.
func TruncateTitle(title string) string {
	if utf8.RuneCountInString(title) <= MaxTitleRunes {
		return title
	}
	n := 0
	for i := range title {
		if n == MaxTitleRunes-1 {
			return title[:i] + "…"
		}
		n++
	}
	return title
}
