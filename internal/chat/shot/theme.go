package shot

type rgb struct{ r, g, b uint8 }

// theme is the palette a screenshot is drawn with. The sixteen ANSI colours
// are not the ones a terminal ships: they are picked to stay readable on
// the background at phone size, which is why "black" on the dark theme is a
// grey. A program that prints dim black text on a black background is
// unreadable in the terminal too, but there the person can select it; in a
// picture they cannot.
type theme struct {
	bg, fg rgb
	ansi   [16]rgb
}

var darkTheme = theme{
	bg: rgb{0x1a, 0x1b, 0x1e},
	fg: rgb{0xd6, 0xd6, 0xd6},
	ansi: [16]rgb{
		{0x4a, 0x4a, 0x4a}, {0xff, 0x6b, 0x6b}, {0x8b, 0xe2, 0x8b}, {0xf3, 0xd7, 0x74},
		{0x74, 0xa9, 0xff}, {0xd3, 0x8c, 0xff}, {0x6f, 0xd8, 0xe0}, {0xe6, 0xe6, 0xe6},
		{0x7a, 0x7a, 0x7a}, {0xff, 0x8f, 0x8f}, {0xa8, 0xf0, 0xa8}, {0xff, 0xe6, 0x99},
		{0x9c, 0xc2, 0xff}, {0xe3, 0xad, 0xff}, {0x98, 0xe8, 0xee}, {0xff, 0xff, 0xff},
	},
}

var lightTheme = theme{
	bg: rgb{0xfa, 0xfa, 0xfa},
	fg: rgb{0x24, 0x29, 0x2e},
	ansi: [16]rgb{
		{0x24, 0x29, 0x2e}, {0xc6, 0x28, 0x28}, {0x2e, 0x7d, 0x32}, {0xa6, 0x7c, 0x00},
		{0x1e, 0x63, 0xd6}, {0x8e, 0x24, 0xaa}, {0x0e, 0x7c, 0x86}, {0x9a, 0x9a, 0x9a},
		{0x5c, 0x5c, 0x5c}, {0xe5, 0x39, 0x35}, {0x43, 0xa0, 0x47}, {0xc9, 0x97, 0x00},
		{0x3f, 0x7f, 0xf0}, {0xab, 0x47, 0xbc}, {0x26, 0xa6, 0xb0}, {0xb8, 0xb8, 0xb8},
	},
}

// resolve turns a requested colour into pixels. bold is whether the cell is
// bold, which brightens the eight base colours and nothing else: a bold
// default-coloured line stays the default, since there is no heavier
// weight in a bitmap font to draw it with.
func (t *theme) resolve(c colour, def rgb, bold bool) rgb {
	switch c.kind {
	case colourRGB:
		return rgb{c.r, c.g, c.b}
	case colourIndexed:
		i := int(c.idx)
		if bold && i < 8 {
			i += 8
		}
		return index256(t, i)
	}
	return def
}

// index256 is the xterm layout: the theme's sixteen, a 6x6x6 cube, then
// twenty-four greys.
func index256(t *theme, i int) rgb {
	switch {
	case i < 16:
		return t.ansi[i]
	case i < 232:
		i -= 16
		return rgb{cubeLevel(i / 36), cubeLevel(i / 6 % 6), cubeLevel(i % 6)}
	default:
		v := uint8(8 + 10*(i-232))
		return rgb{v, v, v}
	}
}

func cubeLevel(n int) uint8 {
	if n == 0 {
		return 0
	}
	return uint8(55 + 40*n)
}

// cellColours is the pair a cell is drawn with, after every attribute.
// Reverse swaps before dim is applied so that dim reverse text fades the
// text, not the block behind it.
func (t *theme) cellColours(c cell) (fg, bg rgb) {
	fg = t.resolve(c.fg, t.fg, c.attrs&attrBold != 0)
	bg = t.resolve(c.bg, t.bg, false)
	if c.attrs&attrReverse != 0 {
		fg, bg = bg, fg
	}
	if c.attrs&attrDim != 0 {
		fg = rgb{
			uint8((int(fg.r) + int(bg.r)) / 2),
			uint8((int(fg.g) + int(bg.g)) / 2),
			uint8((int(fg.b) + int(bg.b)) / 2),
		}
	}
	return fg, bg
}
