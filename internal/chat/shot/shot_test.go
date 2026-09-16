package shot

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// cellPx is the pixel rectangle of a cell at the default scale, inside the
// one-cell padding, so a test can say "cell (0,0)" and mean the top-left
// character rather than the border.
func cellPx(col, row int) image.Rectangle {
	const cw, ch = 8 * defaultScale, glyphRows * defaultScale
	return image.Rect((col+pad)*cw, (row+pad)*ch, (col+pad+1)*cw, (row+pad+1)*ch)
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

func render(t *testing.T, s string) image.Image {
	t.Helper()
	b, err := Render(s)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return decode(t, b)
}

func toRGB(c color.Color) rgb {
	r, g, b, _ := c.RGBA()
	return rgb{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
}

// count is how many pixels in r are exactly c.
func count(img image.Image, r image.Rectangle, c rgb) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if toRGB(img.At(x, y)) == c {
				n++
			}
		}
	}
	return n
}

// sameCell reports whether cell (col,row) of a is pixel for pixel cell
// (col,row) of b. Counting colours is not enough for attributes that move
// pixels around: a cell with the swap missing still has "some red" and
// "some background" in it.
func sameCell(a, b image.Image, col, row int) bool {
	r := cellPx(col, row)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if toRGB(a.At(x, y)) != toRGB(b.At(x, y)) {
				return false
			}
		}
	}
	return true
}

func dims(cols, rows int) (int, int) {
	return (cols + 2*pad) * 8 * defaultScale, (rows + 2*pad) * glyphRows * defaultScale
}

func TestFontLoadsWithBothWidths(t *testing.T) {
	m, err := font()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(m['A']); got != narrowBytes {
		t.Errorf("A: %d bytes, want %d", got, narrowBytes)
	}
	if got := len(m['中']); got != wideBytes {
		t.Errorf("中: %d bytes, want %d", got, wideBytes)
	}
	if _, ok := m['�']; !ok {
		t.Error("no replacement glyph, invalid UTF-8 would be blank")
	}
	if len(m) < 50000 {
		t.Errorf("%d glyphs, the BMP build has about 57 000", len(m))
	}
}

func TestDimensions(t *testing.T) {
	cases := []struct {
		in         string
		cols, rows int
	}{
		{"", minCols, minRows},
		{"hello", minCols, minRows},
		{strings.Repeat("x", 30) + "\n\n\n", 30, minRows},
		{"a\nb\nc\nd", minCols, 4},
		{strings.Repeat("中", 15), 30, minRows},
	}
	for _, c := range cases {
		img := render(t, c.in)
		w, h := dims(c.cols, c.rows)
		if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
			t.Errorf("%q: %dx%d, want %dx%d", c.in, img.Bounds().Dx(), img.Bounds().Dy(), w, h)
		}
	}
}

func TestGlyphAndBlankCell(t *testing.T) {
	img := render(t, "A")
	if _, ok := img.(*image.Paletted); !ok {
		// A picture with a handful of colours must go out as an indexed
		// PNG: it is a quarter of the bytes to encode and to send.
		t.Errorf("a two-colour screen should be a paletted PNG, got %T", img)
	}
	if n := count(img, cellPx(0, 0), darkTheme.fg); n == 0 {
		t.Error("no foreground pixels where A is")
	}
	if n := count(img, cellPx(1, 0), darkTheme.fg); n != 0 {
		t.Errorf("%d foreground pixels in the blank cell after A", n)
	}
	bg := cellPx(1, 0)
	if n := count(img, bg, darkTheme.bg); n != bg.Dx()*bg.Dy() {
		t.Errorf("blank cell is not all background: %d of %d", n, bg.Dx()*bg.Dy())
	}
}

func TestWideCharacterFillsTwoCells(t *testing.T) {
	img := render(t, "中A")
	// 中 has strokes on both halves of its 16 columns.
	left, right := count(img, cellPx(0, 0), darkTheme.fg), count(img, cellPx(1, 0), darkTheme.fg)
	if left == 0 || right == 0 {
		t.Errorf("中 should span two cells: left %d, right %d", left, right)
	}
	if n := count(img, cellPx(2, 0), darkTheme.fg); n == 0 {
		t.Error("A after 中 should be in the third cell")
	}
	if n := count(img, cellPx(3, 0), darkTheme.fg); n != 0 {
		t.Error("nothing should be in the fourth cell")
	}
}

func TestWideCharacterInTheLastColumnIsDropped(t *testing.T) {
	// Twenty-one columns: twenty narrow then a wide one that does not fit
	// in a 22-column cap by one cell. tmux would wrap; the capture has
	// already wrapped, so the only wide-in-last-column case left is the
	// cap, and it drops the character rather than drawing half of it.
	in := strings.Repeat("x", 21) + "中"
	b, err := RenderOptions(in, Options{MaxCols: 22, Dark: true})
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, b)
	if n := count(img, cellPx(21, 0), darkTheme.fg); n != 0 {
		t.Errorf("half a 中 drawn in the last column: %d px", n)
	}
	if n := count(img, cellPx(20, 0), darkTheme.fg); n == 0 {
		t.Error("the x before it should still be drawn")
	}
}

func TestSGRColours(t *testing.T) {
	img := render(t, "\x1b[31mr\x1b[0mn")
	if count(img, cellPx(0, 0), darkTheme.ansi[1]) == 0 {
		t.Error("r is not red")
	}
	if count(img, cellPx(1, 0), darkTheme.ansi[1]) != 0 {
		t.Error("reset did not clear red")
	}
	if count(img, cellPx(1, 0), darkTheme.fg) == 0 {
		t.Error("n is not the default foreground")
	}

	img = render(t, "\x1b[1;34mb\x1b[22mn\x1b[94mB")
	if count(img, cellPx(0, 0), darkTheme.ansi[12]) == 0 {
		t.Error("bold blue should be bright blue")
	}
	if count(img, cellPx(1, 0), darkTheme.ansi[4]) == 0 {
		t.Error("22 should take the bold off and leave blue")
	}
	if count(img, cellPx(2, 0), darkTheme.ansi[12]) == 0 {
		t.Error("94 is bright blue")
	}

	img = render(t, "\x1b[42m \x1b[49m ")
	c := cellPx(0, 0)
	if n := count(img, c, darkTheme.ansi[2]); n != c.Dx()*c.Dy() {
		t.Errorf("green background fills %d of %d", n, c.Dx()*c.Dy())
	}
	if n := count(img, cellPx(1, 0), darkTheme.ansi[2]); n != 0 {
		t.Error("49 did not reset the background")
	}
}

func TestExtendedColours(t *testing.T) {
	img := render(t, "\x1b[38;2;10;20;30mX\x1b[48;2;200;100;50m ")
	if count(img, cellPx(0, 0), rgb{10, 20, 30}) == 0 {
		t.Error("truecolour foreground missed")
	}
	if count(img, cellPx(1, 0), rgb{200, 100, 50}) == 0 {
		t.Error("truecolour background missed")
	}
	img = render(t, "\x1b[38;5;196mX\x1b[38;5;244mY\x1b[38;5;9mZ\x1b[48;5;21m ")
	if count(img, cellPx(0, 0), rgb{255, 0, 0}) == 0 {
		t.Error("196 is cube red")
	}
	if count(img, cellPx(1, 0), rgb{128, 128, 128}) == 0 {
		t.Error("244 is grey 128")
	}
	if count(img, cellPx(2, 0), darkTheme.ansi[9]) == 0 {
		t.Error("9 is the theme's bright red")
	}
	if count(img, cellPx(3, 0), rgb{0, 0, 255}) == 0 {
		t.Error("21 is cube blue")
	}
}

func TestReverseSwaps(t *testing.T) {
	img := render(t, "\x1b[7;31mX\x1b[27mY")
	plain := render(t, "XY")
	c := cellPx(0, 0)
	// Every pixel that is ink in a plain X is background here, and every
	// pixel that is background there is red here: a full swap.
	for y := c.Min.Y; y < c.Max.Y; y++ {
		for x := c.Min.X; x < c.Max.X; x++ {
			got, ref := toRGB(img.At(x, y)), toRGB(plain.At(x, y))
			switch ref {
			case darkTheme.fg:
				if got != darkTheme.bg {
					t.Fatalf("(%d,%d): ink should be the background colour, is %v", x, y, got)
				}
			case darkTheme.bg:
				if got != darkTheme.ansi[1] {
					t.Fatalf("(%d,%d): block should be red, is %v", x, y, got)
				}
			}
		}
	}
	// 27 turns it off: red glyph on the default background, pixel for
	// pixel the plain Y with the ink recoloured.
	c = cellPx(1, 0)
	for y := c.Min.Y; y < c.Max.Y; y++ {
		for x := c.Min.X; x < c.Max.X; x++ {
			got, ref := toRGB(img.At(x, y)), toRGB(plain.At(x, y))
			if ref == darkTheme.fg && got != darkTheme.ansi[1] {
				t.Fatalf("(%d,%d): after 27 ink should be red, is %v", x, y, got)
			}
			if ref == darkTheme.bg && got != darkTheme.bg {
				t.Fatalf("(%d,%d): after 27 the background should be the default, is %v", x, y, got)
			}
		}
	}
}

func TestCombiningMarkTakesNoCell(t *testing.T) {
	img := render(t, "a\u0301b")
	if count(img, cellPx(1, 0), darkTheme.fg) == 0 {
		t.Error("b should be in the second cell, right after a")
	}
	if count(img, cellPx(2, 0), darkTheme.fg) != 0 {
		t.Error("nothing should be in the third cell")
	}
	if sameCell(img, render(t, "ab"), 0, 0) {
		t.Error("the accent should be drawn over the a")
	}
	// After a wide character the mark belongs to the character, not to
	// the empty second half, which draws nothing and would lose it.
	img = render(t, "中\u0301")
	plain := render(t, "中")
	if sameCell(img, plain, 0, 0) && sameCell(img, plain, 1, 0) {
		t.Error("the accent on a wide character was lost")
	}
}

func TestWideBitmapInANarrowCellIsSqueezed(t *testing.T) {
	// U+279C is one cell to tmux and sixteen pixels to Unifont. Drawn at
	// full width it paints over the A; the A must be untouched.
	img := render(t, "\u279cA")
	if !sameCell(img, render(t, " A"), 1, 0) {
		t.Error("the arrow spilled into the cell of the A")
	}
	if count(img, cellPx(0, 0), darkTheme.fg) == 0 {
		t.Error("the arrow itself should still be drawn")
	}
}

func TestDimAndUnderline(t *testing.T) {
	img := render(t, "\x1b[2mX")
	f, b := darkTheme.fg, darkTheme.bg
	half := rgb{uint8((int(f.r) + int(b.r)) / 2), uint8((int(f.g) + int(b.g)) / 2), uint8((int(f.b) + int(b.b)) / 2)}
	if count(img, cellPx(0, 0), half) == 0 {
		t.Error("dim should halve the foreground toward the background")
	}
	if count(img, cellPx(0, 0), f) != 0 {
		t.Error("dim text should not contain the full foreground")
	}
	img = render(t, "\x1b[4m \x1b[24m ")
	c := cellPx(0, 0)
	bottom := image.Rect(c.Min.X, c.Max.Y-defaultScale, c.Max.X, c.Max.Y)
	if n := count(img, bottom, f); n != bottom.Dx()*bottom.Dy() {
		t.Errorf("underline: %d of %d bottom pixels", n, bottom.Dx()*bottom.Dy())
	}
	if count(img, cellPx(1, 0), f) != 0 {
		t.Error("24 did not take the underline off")
	}
}

func TestTrailingBlankRowsTrimmedAndBottomKept(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "row%d\n", i)
	}
	sb.WriteString("\n\n   \n")
	img := render(t, sb.String())
	if _, h := dims(0, 10); img.Bounds().Dy() != h {
		t.Errorf("trailing blank rows not trimmed: height %d, want %d", img.Bounds().Dy(), h)
	}

	b, err := RenderOptions(sb.String(), Options{MaxRows: 3, Dark: true})
	if err != nil {
		t.Fatal(err)
	}
	img = decode(t, b)
	if _, h := dims(0, 3); img.Bounds().Dy() != h {
		t.Fatalf("MaxRows: height %d, want %d", img.Bounds().Dy(), h)
	}
	// Rows 7, 8, 9 are kept; "row9" has a 9 in column 3 and "row0" has a
	// 0, which differ in pixels. Compare against a render of just "row9".
	want := render(t, "row9")
	for x := cellPx(3, 0).Min.X; x < cellPx(3, 0).Max.X; x++ {
		for y := cellPx(3, 0).Min.Y; y < cellPx(3, 0).Max.Y; y++ {
			if toRGB(img.At(x, y+2*glyphRows*defaultScale)) != toRGB(want.At(x, y)) {
				t.Fatal("the last row kept is not row9; the top was kept instead of the bottom")
			}
		}
	}
	// And the first row kept is row7, not row0.
	want = render(t, "row7")
	for x := cellPx(3, 0).Min.X; x < cellPx(3, 0).Max.X; x++ {
		for y := cellPx(3, 0).Min.Y; y < cellPx(3, 0).Max.Y; y++ {
			if toRGB(img.At(x, y)) != toRGB(want.At(x, y)) {
				t.Fatal("the first row kept is not row7")
			}
		}
	}
}

func TestMaxColsCap(t *testing.T) {
	b, err := RenderOptions(strings.Repeat("x", 500), Options{MaxCols: 40, Dark: true})
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, b)
	if w, _ := dims(40, 0); img.Bounds().Dx() != w {
		t.Errorf("width %d, want %d", img.Bounds().Dx(), w)
	}
	if count(img, cellPx(39, 0), darkTheme.fg) == 0 {
		t.Error("the last column inside the cap should be drawn")
	}
	b, err = RenderOptions(strings.Repeat("x", 500), Options{Dark: true})
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := dims(defaultMaxCols, 0); decode(t, b).Bounds().Dx() != w {
		t.Error("the default cap is 200 columns")
	}
}

func TestTabs(t *testing.T) {
	img := render(t, "a\tb\n\t\tc")
	if count(img, cellPx(0, 0), darkTheme.fg) == 0 || count(img, cellPx(8, 0), darkTheme.fg) == 0 {
		t.Error("a at 0 and b at 8")
	}
	for x := 1; x < 8; x++ {
		if count(img, cellPx(x, 0), darkTheme.fg) != 0 {
			t.Errorf("column %d between a and b is not blank", x)
		}
	}
	if count(img, cellPx(16, 1), darkTheme.fg) == 0 {
		t.Error("c after two tabs at 16")
	}
}

func TestOtherSequencesAreIgnored(t *testing.T) {
	// A title, a cursor move, a charset switch, a private mode, and a
	// stray control byte, none of which may leave a glyph or eat text.
	in := "\x1b]0;title\x07A\x1b[3;4HB\x1b(BC\x1b[?25lD\x01E\x1b]2;st\x1b\\F"
	img := render(t, in)
	for i := 0; i < 6; i++ {
		if count(img, cellPx(i, 0), darkTheme.fg) == 0 {
			t.Errorf("letter %d missing", i)
		}
	}
	if count(img, cellPx(6, 0), darkTheme.fg) != 0 {
		t.Error("something drawn after F")
	}
	plain := render(t, "ABCDEF")
	for x := 0; x < cellPx(6, 0).Min.X; x++ {
		for y := cellPx(0, 0).Min.Y; y < cellPx(0, 0).Max.Y; y++ {
			if toRGB(img.At(x, y)) != toRGB(plain.At(x, y)) {
				t.Fatalf("pixel (%d,%d) differs from a plain ABCDEF", x, y)
			}
		}
	}
}

func TestDoesNotPanic(t *testing.T) {
	inputs := []string{
		"",
		"\n",
		"\x1b",
		"\x1b[",
		"\x1b[38;5",
		"\x1b[38;2;1;2m",
		"\x1b[99999999999999999999m",
		"\x1b]0;never terminated",
		"\xff\xfe\xc0 bad \xe4\xb8",
		strings.Repeat("\x1b[31;1;4;7m中", 300),
		strings.Repeat("\n", 500) + "x",
		"́́á́", // combining marks first, then doubled
		strings.Repeat("\t", 100),
	}
	for _, in := range inputs {
		if _, err := Render(in); err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	img := render(t, "\xff")
	if count(img, cellPx(0, 0), darkTheme.fg) == 0 {
		t.Error("an invalid byte should draw the replacement glyph, not nothing")
	}
}

func TestMissingGlyphIsABox(t *testing.T) {
	img := render(t, "\U0001F600") // emoji is in the plane-1 file, not embedded
	if n := count(img, cellPx(0, 0), darkTheme.fg); n == 0 {
		t.Error("a missing glyph should draw something")
	}
	if n := count(img, cellPx(1, 0), darkTheme.fg); n == 0 {
		t.Error("a missing wide glyph's box should span both cells")
	}
}

func TestLightTheme(t *testing.T) {
	b, err := RenderOptions("A", Options{Dark: false})
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, b)
	if count(img, cellPx(0, 0), lightTheme.fg) == 0 {
		t.Error("light theme foreground missing")
	}
	if count(img, cellPx(1, 0), lightTheme.bg) == 0 {
		t.Error("light theme background missing")
	}
}

func TestTruecolourBeyondAPalette(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "\x1b[48;2;%d;%d;%dm ", i%256, (i*7)%256, (i*13)%256)
		if i%50 == 49 {
			sb.WriteString("\x1b[0m\n")
		}
	}
	img := render(t, sb.String())
	if _, ok := img.(*image.Paletted); ok {
		t.Error("300 colours cannot be paletted")
	}
	if count(img, cellPx(0, 0), rgb{0, 0, 0}) == 0 {
		t.Error("first cell should be black")
	}
	if count(img, cellPx(1, 0), rgb{1, 7, 13}) == 0 {
		t.Error("second cell colour missed")
	}
}

func bigScreen() string {
	var sb strings.Builder
	for y := 0; y < 60; y++ {
		for x := 0; x < 165; x++ { // 198 cells: one in five is wide
			switch (x + y) % 5 {
			case 0:
				fmt.Fprintf(&sb, "\x1b[%dm%c", 31+(x%7), 'a'+rune(x%26))
			case 1:
				sb.WriteString("中")
			case 2:
				fmt.Fprintf(&sb, "\x1b[48;5;%dm \x1b[0m", (x+y)%256)
			default:
				sb.WriteByte('a' + byte(y%26))
			}
		}
		sb.WriteString("\x1b[0m\n")
	}
	return sb.String()
}

func BenchmarkRender200x60(b *testing.B) {
	in := bigScreen()
	if _, err := Render(in); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Render(in); err != nil {
			b.Fatal(err)
		}
	}
}
