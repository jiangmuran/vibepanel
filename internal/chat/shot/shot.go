package shot

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// Options shape the picture. The zero value of each field means its default.
type Options struct {
	// Scale is pixels per font pixel; 2 is what reads on a phone, 1 is a
	// thumbnail.
	Scale int
	// MaxCols and MaxRows cap the grid. Rows are taken from the bottom,
	// because that is where the prompt and the newest output are.
	MaxCols int
	MaxRows int
	// Dark selects the palette; the light one is for a chat app that
	// shows pictures on white.
	Dark bool
}

const (
	defaultScale   = 2
	defaultMaxCols = 200
	defaultMaxRows = 60
	// minCols and minRows keep an empty screen a picture rather than an
	// error: the chat app shows a small dark rectangle, which is the true
	// answer to "what does it look like".
	minCols = 20
	minRows = 2
	// pad is the border, in cells, so text does not touch the edge of the
	// image where a chat app's rounded corners clip it.
	pad = 1
)

// Render is the chat.Shooter the bridge is given: the dark theme at scale 2.
func Render(ansi string) ([]byte, error) {
	return RenderOptions(ansi, Options{Dark: true})
}

// RenderOptions renders with explicit options; a zero Scale, MaxCols or
// MaxRows takes the default.
func RenderOptions(ansi string, o Options) ([]byte, error) {
	if o.Scale <= 0 {
		o.Scale = defaultScale
	}
	if o.MaxCols <= 0 {
		o.MaxCols = defaultMaxCols
	}
	if o.MaxRows <= 0 {
		o.MaxRows = defaultMaxRows
	}
	glyphs, err := font()
	if err != nil {
		return nil, err
	}
	t := &lightTheme
	if o.Dark {
		t = &darkTheme
	}
	rows, cols := layout(parse(ansi, o.MaxCols), o.MaxRows)
	img := draw(rows, cols, t, glyphs, o.Scale)
	var buf bytes.Buffer
	// BestSpeed: the picture is flat colour and compresses to a few
	// percent at any level, and the default level is what put a 200x60
	// screen over the budget.
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("shot: %w", err)
	}
	return buf.Bytes(), nil
}

// layout trims the blank rows at the bottom, keeps the last maxRows of what
// is left, and measures the widest row. Trailing blank rows are the unused
// bottom of a pane, not content; leading ones are kept because a program
// that put its text at the bottom meant it to be there. The column cap is
// parse's, which never builds a row wider than it, so the widest row is
// already inside it.
func layout(rows [][]cell, maxRows int) ([][]cell, int) {
	for len(rows) > 0 && rowWidth(rows[len(rows)-1]) == 0 {
		rows = rows[:len(rows)-1]
	}
	if len(rows) > maxRows {
		rows = rows[len(rows)-maxRows:]
	}
	cols := 0
	for i, r := range rows {
		w := rowWidth(r)
		rows[i] = r[:w]
		if w > cols {
			cols = w
		}
	}
	if cols < minCols {
		cols = minCols
	}
	for len(rows) < minRows {
		rows = append(rows, nil)
	}
	return rows, cols
}

// rowWidth is one past the last visible cell, so trailing spaces in the
// default colour do not widen the picture.
func rowWidth(r []cell) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i].visible() {
			if r[i].wide {
				return i + 2
			}
			return i + 1
		}
	}
	return 0
}

// draw paints at font resolution into an index buffer and scales up at the
// end. Indexes rather than pixels because a screen almost never has more
// than 256 distinct colours, and a paletted PNG is a quarter of the bytes
// to filter and deflate, which is most of the time a screenshot takes.
func draw(rows [][]cell, cols int, t *theme, glyphs map[rune]glyph, scale int) image.Image {
	const cw, ch = 8, glyphRows
	w, h := (cols+2*pad)*cw, (len(rows)+2*pad)*ch
	pal := newPalette(t.bg)
	buf := make([]uint16, w*h) // zero is the background
	for y, row := range rows {
		for x := 0; x < len(row) && x < cols; x++ {
			c := row[x]
			fg, bg := t.cellColours(c)
			ox, oy := (x+pad)*cw, (y+pad)*ch
			if bg != t.bg {
				bi := pal.index(bg)
				for dy := 0; dy < ch; dy++ {
					line := buf[(oy+dy)*w+ox:]
					for dx := 0; dx < cw; dx++ {
						line[dx] = bi
					}
				}
			}
			if c.r == 0 {
				continue
			}
			fi := pal.index(fg)
			g, ok := glyphs[c.r]
			if !ok {
				drawMissing(buf, w, ox, oy, c.wide, fi)
			} else {
				drawGlyph(buf, w, ox, oy, g, c.wide, fi)
			}
			if c.over != 0 {
				if g, ok := glyphs[c.over]; ok {
					drawGlyph(buf, w, ox, oy, g, c.wide, fi)
				}
			}
			if c.attrs&attrUnderline != 0 {
				n := cw
				if c.wide {
					n = 2 * cw
				}
				line := buf[(oy+ch-1)*w+ox:]
				for dx := 0; dx < n; dx++ {
					line[dx] = fi
				}
			}
		}
	}
	return pal.image(buf, w, h, scale)
}

// drawGlyph copies a bitmap: 8 pixels per row for a narrow glyph, 16 for a
// wide one. A narrow glyph in a wide cell is drawn at the left; Unifont has
// a few CJK punctuation marks that way and they read fine. The other
// mismatch, a 16-pixel bitmap for a rune tmux laid out in one cell (some
// arrows and symbols), is squeezed to 8 by folding column pairs, because
// drawing it at full width paints over the character to its right.
func drawGlyph(buf []uint16, w, ox, oy int, g glyph, wide bool, fi uint16) {
	bpr := len(g) / glyphRows
	for dy := 0; dy < glyphRows; dy++ {
		line := buf[(oy+dy)*w+ox:]
		var bits uint16
		for b := 0; b < bpr; b++ {
			bits = bits<<8 | uint16(g[dy*bpr+b])
		}
		if bpr == 2 && !wide {
			var folded uint16
			for i := 0; i < 8; i++ {
				if bits&(0xc000>>(2*i)) != 0 {
					folded |= 0x80 >> i
				}
			}
			bits, bpr = folded, 1
		}
		n := 8 * bpr
		for bit := 0; bit < n; bit++ {
			if bits&(1<<(n-1-bit)) != 0 {
				line[bit] = fi
			}
		}
	}
}

// drawMissing is the one-pixel box for a code point the font lacks, in the
// foreground colour so it is visible on any background: a person sees that
// something was printed there, which a blank cell would deny.
func drawMissing(buf []uint16, w, ox, oy int, wide bool, fi uint16) {
	cw := 8
	if wide {
		cw = 16
	}
	for dy := 1; dy < glyphRows-1; dy++ {
		line := buf[(oy+dy)*w+ox:]
		if dy == 1 || dy == glyphRows-2 {
			for dx := 1; dx < cw-1; dx++ {
				line[dx] = fi
			}
		} else {
			line[1] = fi
			line[cw-2] = fi
		}
	}
}

// palette is every colour the picture uses, in first-seen order, with the
// background at 0.
type palette struct {
	colours []rgb
	at      map[rgb]uint16
}

func newPalette(bg rgb) *palette {
	p := &palette{at: make(map[rgb]uint16, 32)}
	p.index(bg)
	return p
}

func (p *palette) index(c rgb) uint16 {
	if i, ok := p.at[c]; ok {
		return i
	}
	i := uint16(len(p.colours))
	p.colours = append(p.colours, c)
	p.at[c] = i
	return i
}

// image scales the index buffer up. Paletted when the palette fits, which
// is the common case; RGBA when a truecolour gradient blew past 256.
func (p *palette) image(buf []uint16, w, h, scale int) image.Image {
	W, H := w*scale, h*scale
	if len(p.colours) <= 256 {
		cp := make(color.Palette, len(p.colours))
		for i, c := range p.colours {
			cp[i] = color.RGBA{c.r, c.g, c.b, 0xff}
		}
		img := image.NewPaletted(image.Rect(0, 0, W, H), cp)
		for y := 0; y < h; y++ {
			src := buf[y*w : (y+1)*w]
			dst := img.Pix[y*scale*img.Stride : y*scale*img.Stride+W]
			for x, v := range src {
				for s := 0; s < scale; s++ {
					dst[x*scale+s] = uint8(v)
				}
			}
			for s := 1; s < scale; s++ {
				copy(img.Pix[(y*scale+s)*img.Stride:], dst)
			}
		}
		return img
	}
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < h; y++ {
		src := buf[y*w : (y+1)*w]
		dst := img.Pix[y*scale*img.Stride : y*scale*img.Stride+4*W]
		for x, v := range src {
			c := p.colours[v]
			for s := 0; s < scale; s++ {
				o := (x*scale + s) * 4
				dst[o], dst[o+1], dst[o+2], dst[o+3] = c.r, c.g, c.b, 0xff
			}
		}
		for s := 1; s < scale; s++ {
			copy(img.Pix[(y*scale+s)*img.Stride:], dst)
		}
	}
	return img
}
