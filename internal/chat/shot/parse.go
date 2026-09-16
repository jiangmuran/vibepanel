package shot

import (
	"strings"
	"unicode/utf8"
)

// colourKind says how a cell's colour was asked for. The number is kept
// rather than resolved on the spot because bold brightens an indexed colour
// and reverse swaps with the theme's defaults, both of which need to know
// what was asked, not what it came to.
type colourKind uint8

const (
	colourDefault colourKind = iota
	colourIndexed            // 0–255
	colourRGB
)

type colour struct {
	kind    colourKind
	idx     uint8
	r, g, b uint8
}

const (
	attrBold uint8 = 1 << iota
	attrDim
	attrUnderline
	attrReverse
)

// cell is one column of one row after the escape sequences are gone.
type cell struct {
	r     rune
	fg    colour
	bg    colour
	attrs uint8
	// wide marks the first half of a two-column character; the second half
	// is a cell with r == 0 that draws only its background.
	wide bool
	// over is a combining mark drawn on top of r, one at most: a second
	// one lands on the same cell, and losing it is invisible.
	over rune
}

// visible is what keeps a row from being trimmed and what widens it: a
// glyph, or a background that is not the theme's, which a status bar of
// coloured spaces is, or an underline, which draws in a blank cell.
func (c cell) visible() bool {
	return (c.r != 0 && c.r != ' ') || c.bg.kind != colourDefault || c.attrs&(attrReverse|attrUnderline) != 0
}

type pen struct {
	fg, bg colour
	attrs  uint8
}

// parse turns the capture into rows of cells, one per line, with no cap or
// trim applied yet; that is layout's job. maxCols bounds a row so a line of
// junk cannot allocate without limit, and it is the "last column" past which
// a wide character is dropped rather than split.
func parse(ansi string, maxCols int) [][]cell {
	var rows [][]cell
	for _, line := range strings.Split(ansi, "\n") {
		rows = append(rows, parseLine(line, maxCols))
	}
	return rows
}

func parseLine(line string, maxCols int) []cell {
	var (
		row []cell
		p   pen
	)
	put := func(r rune, w int) {
		if w == 0 {
			if n := len(row); n > 0 && row[n-1].over == 0 {
				if n > 1 && row[n-1].r == 0 && row[n-2].wide {
					row[n-2].over = r
				} else {
					row[n-1].over = r
				}
			}
			return
		}
		if len(row)+w > maxCols {
			return
		}
		row = append(row, cell{r: r, fg: p.fg, bg: p.bg, attrs: p.attrs, wide: w == 2})
		if w == 2 {
			row = append(row, cell{fg: p.fg, bg: p.bg, attrs: p.attrs})
		}
	}
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == 0x1b:
			i += skipEscape(line[i:], &p)
		case c == '\t':
			// A blank cell for every column up to the next tab stop, in the
			// current background: that is what the terminal did with it.
			for n := 8 - len(row)%8; n > 0; n-- {
				put(' ', 1)
			}
			i++
		case c < 0x20 || c == 0x7f:
			i++
		default:
			r, size := utf8.DecodeRuneInString(line[i:])
			put(r, runeWidth(r))
			i += size
		}
	}
	return row
}

// skipEscape consumes one escape sequence starting at s[0] == ESC, applies
// it if it is SGR, and returns how many bytes it took. Anything else, cursor
// movement, OSC titles, charset switches, is dropped whole: the capture is
// already laid out, so the only sequence that still means something is the
// one that colours what follows.
func skipEscape(s string, p *pen) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		i := 2
		for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
			i++
		}
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i >= len(s) {
			return len(s)
		}
		if s[i] == 'm' {
			applySGR(s[2:i], p)
		}
		return i + 1
	case ']':
		// OSC: ends at BEL or at ST (ESC \). A title with no terminator
		// swallows the rest of the line, which is what a terminal does too.
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default:
		// ESC ( B and friends: intermediates then one final byte.
		i := 1
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) {
			i++
		}
		return i
	}
}

// applySGR is the parameter list between "ESC [" and "m". A private-mode
// or otherwise malformed list is skipped rather than half-applied.
func applySGR(params string, p *pen) {
	if params == "" {
		*p = pen{}
		return
	}
	var nums []int
	for _, f := range strings.Split(params, ";") {
		if f == "" {
			nums = append(nums, 0)
			continue
		}
		n := 0
		for _, c := range f {
			if c < '0' || c > '9' {
				return
			}
			n = n*10 + int(c-'0')
			if n > 1<<20 {
				return
			}
		}
		nums = append(nums, n)
	}
	for i := 0; i < len(nums); i++ {
		n := nums[i]
		switch {
		case n == 0:
			*p = pen{}
		case n == 1:
			p.attrs |= attrBold
		case n == 2:
			p.attrs |= attrDim
		case n == 4:
			p.attrs |= attrUnderline
		case n == 7:
			p.attrs |= attrReverse
		case n == 22:
			p.attrs &^= attrBold | attrDim
		case n == 24:
			p.attrs &^= attrUnderline
		case n == 27:
			p.attrs &^= attrReverse
		case n >= 30 && n <= 37:
			p.fg = colour{kind: colourIndexed, idx: uint8(n - 30)}
		case n == 39:
			p.fg = colour{}
		case n >= 40 && n <= 47:
			p.bg = colour{kind: colourIndexed, idx: uint8(n - 40)}
		case n == 49:
			p.bg = colour{}
		case n >= 90 && n <= 97:
			p.fg = colour{kind: colourIndexed, idx: uint8(n - 90 + 8)}
		case n >= 100 && n <= 107:
			p.bg = colour{kind: colourIndexed, idx: uint8(n - 100 + 8)}
		case n == 38 || n == 48:
			c, used, ok := extendedColour(nums[i+1:])
			if !ok {
				return
			}
			if n == 38 {
				p.fg = c
			} else {
				p.bg = c
			}
			i += used
		}
	}
}

// extendedColour reads "5;n" or "2;r;g;b" after a 38 or 48 and says how
// many parameters it consumed.
func extendedColour(rest []int) (colour, int, bool) {
	if len(rest) == 0 {
		return colour{}, 0, false
	}
	switch rest[0] {
	case 5:
		if len(rest) < 2 || rest[1] > 255 {
			return colour{}, 0, false
		}
		return colour{kind: colourIndexed, idx: uint8(rest[1])}, 2, true
	case 2:
		if len(rest) < 4 || rest[1] > 255 || rest[2] > 255 || rest[3] > 255 {
			return colour{}, 0, false
		}
		return colour{kind: colourRGB, r: uint8(rest[1]), g: uint8(rest[2]), b: uint8(rest[3])}, 4, true
	}
	return colour{}, 0, false
}
