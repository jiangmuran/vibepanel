// Package shot renders a captured tmux pane to a PNG.
//
// The input is what "tmux capture-pane -p -e -J" prints: one line per row,
// SGR escape sequences inline, nothing else of the terminal's state. The
// output is a picture a chat app will show inline on a phone, which is the
// only reason this exists: a person away from the keyboard asking "what does
// it look like right now" gets a full-screen program back as an image,
// because a TUI pasted as text is unreadable and a screenshot is not.
//
// # The font, and why the binary is 900 KB larger
//
// GNU Unifont is embedded as a gzipped .hex file, 936 KB, and it is the
// only thing embedded. It is a bitmap font, so drawing it is copying bits
// and needs no rasteriser, no freetype and no golang.org/x/image, which
// keeps CGO_ENABLED=0 and the dependency list where they are. It is also the
// only bitmap font with a glyph for every code point in the Basic
// Multilingual Plane, and that is the deciding reason: the sessions this
// panel runs are talked to in Chinese as often as in English, and a
// screenshot in which every line of the agent's answer is a row of tofu
// boxes is worse than no screenshot at all, because it looks like the
// session is broken. A Latin-only bitmap font would cost 20 KB and be
// exactly that failure. The plane-1 file with emoji is another 1 MB and is
// not embedded; an emoji renders as the small "missing glyph" box, which is
// a legible gap rather than a broken row.
package shot
