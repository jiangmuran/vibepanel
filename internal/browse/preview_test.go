package browse

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// The kind is decided from the bytes, and the cases that matter are the ones
// where the name and the content disagree -- which in a project directory an
// agent writes into is most of them.
func TestSniffMagic(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		kind Kind
		mime string
	}{
		{"png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), KindImage, "image/png"},
		{"jpeg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), KindImage, "image/jpeg"},
		{"gif87", []byte("GIF87a\x01\x00"), KindImage, "image/gif"},
		{"gif89", []byte("GIF89a\x01\x00"), KindImage, "image/gif"},
		{"webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 "), KindImage, "image/webp"},
		{"avif", []byte("\x00\x00\x00\x20ftypavifmif1"), KindImage, "image/avif"},
		{"pdf", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3"), KindPDF, "application/pdf"},
		{"source", []byte("package main\n"), KindBinary, ""},
		// The whole reason SVG is not on the list: it would be rendered in an
		// <object> on the panel's origin, and it is a document with script in
		// it. Read as text instead, which is the more useful preview anyway.
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>`), KindBinary, ""},
		// "BM" is how a bitmap starts and also how a great many sentences do.
		{"looks like bmp", []byte("BMW pricing notes\n"), KindBinary, ""},
		// RIFF without the WEBP brand is a wav, which nothing here can draw.
		{"riff but not webp", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), KindBinary, ""},
		{"empty", nil, KindBinary, ""},
		// Shorter than the offsets the container formats are read at. The
		// bounds check is the point; without it this panics.
		{"three bytes", []byte("RIF"), KindBinary, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, mime := SniffMagic(c.head)
			if kind != c.kind || mime != c.mime {
				t.Errorf("SniffMagic = %q/%q, want %q/%q", kind, mime, c.kind, c.mime)
			}
		})
	}
}

func TestIsText(t *testing.T) {
	if !IsText([]byte("hello\n\tworld — with an em dash\n")) {
		t.Error("ordinary UTF-8 prose was called binary")
	}
	if !IsText(nil) {
		t.Error("an empty file has nothing binary in it")
	}
	// git's heuristic, and the one that carries the weight: a zero byte is
	// what every compiled artefact has and nothing anybody reads does.
	if IsText([]byte("ELF\x00\x01\x02")) {
		t.Error("a NUL byte was called text")
	}
	// A lone 0x80 is not a valid UTF-8 sequence, and the panel decodes what it
	// gets as UTF-8 -- so this would arrive as a screen of U+FFFD that reads
	// as the panel being broken.
	if IsText([]byte("caf\xe9 latte")) {
		t.Error("Latin-1 was called text; it decodes as replacement characters")
	}
}

func TestClipTextStopsAtAWholeLine(t *testing.T) {
	// The byte budget lands in the middle of a line essentially every time,
	// and a preview that ends mid-line invites the reader to think the file
	// does.
	got, truncated := ClipText([]byte("one\ntwo\nthr"), true, 0)
	if !truncated {
		t.Error("truncated = false after the caller said there was more")
	}
	if string(got) != "one\ntwo\n" {
		t.Errorf("got %q, want the two whole lines", got)
	}
}

func TestClipTextKeepsACompleteFileWhole(t *testing.T) {
	in := []byte("one\ntwo\nthree\n")
	got, truncated := ClipText(in, false, 4000)
	if truncated || !bytes.Equal(got, in) {
		t.Errorf("got %q/%v, want the file unchanged and not truncated", got, truncated)
	}
	// Exactly the line cap, ending in a newline: there is nothing after the
	// last one, so nothing was cut.
	four := []byte("1\n2\n3\n4\n")
	got, truncated = ClipText(four, false, 4)
	if truncated || !bytes.Equal(got, four) {
		t.Errorf("got %q/%v for exactly the cap, want it whole", got, truncated)
	}
}

func TestClipTextBoundsTheLineCount(t *testing.T) {
	// A quarter of a megabyte of "a\n" is a quarter of a million line boxes in
	// a 280px column. The byte budget does not bound the work; this does.
	in := []byte(strings.Repeat("a\n", 10_000))
	got, truncated := ClipText(in, false, 4000)
	if !truncated {
		t.Fatal("truncated = false on a file well past the line cap")
	}
	if n := bytes.Count(got, []byte("\n")); n != 4000 {
		t.Errorf("kept %d lines, want 4000", n)
	}
}

func TestClipTextNeverCutsACharacterInHalf(t *testing.T) {
	// One enormous line is what a minified bundle is: there is no newline to
	// cut back to, so the only thing to do is stop on a character boundary.
	// Without it the clipped buffer fails IsText, and the file is reported as
	// binary because of where the reader stopped -- a lie about the file.
	line := []byte(strings.Repeat("字", 40))
	cut := line[:len(line)-1]
	got, truncated := ClipText(cut, true, 0)
	if !truncated {
		t.Error("truncated = false")
	}
	if !utf8.Valid(got) {
		t.Errorf("got %q, which is not valid UTF-8", got)
	}
	if !IsText(got) {
		t.Error("a clipped run of CJK was reported as binary")
	}
	if len(got) != len(line)-3 {
		t.Errorf("trimmed %d bytes, want the whole partial character", len(cut)-len(got))
	}
	// A real U+FFFD decodes as RuneError too and is an ordinary character
	// somebody may have typed; it must survive.
	kept, _ := ClipText([]byte("a�"), true, 0)
	if string(kept) != "a�" {
		t.Errorf("got %q, want a genuine replacement character kept", kept)
	}
}
