package browse

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestDecodeReadsUTF16ByItsMark(t *testing.T) {
	le := []byte{0xFF, 0xFE, 'h', 0, 'i', 0, '\n', 0, 0x2D, 0x4E} // "hi\n中"
	got, enc, ok := Decode(le, false)
	if !ok || enc != EncodingUTF16LE || string(got) != "hi\n中" {
		t.Fatalf("LE: %q %q %v", got, enc, ok)
	}
	be := []byte{0xFE, 0xFF, 0, 'h', 0, 'i', 0x4E, 0x2D}
	got, enc, ok = Decode(be, false)
	if !ok || enc != EncodingUTF16BE || string(got) != "hi中" {
		t.Fatalf("BE: %q %q %v", got, enc, ok)
	}
	// A pair outside the BMP: U+1F600.
	got, _, ok = Decode([]byte{0xFF, 0xFE, 0x3D, 0xD8, 0x00, 0xDE}, false)
	if !ok || string(got) != "\U0001F600" {
		t.Fatalf("pair: %q %v", got, ok)
	}
}

func TestDecodeDropsWhatTheReadCutInHalf(t *testing.T) {
	// An odd byte at the end of a file is a fault; at the end of a read it is
	// where the read stopped.
	odd := []byte{0xFF, 0xFE, 'a', 0, 'b'}
	if _, _, ok := Decode(odd, false); ok {
		t.Error("an odd UTF-16 file decoded")
	}
	if got, _, ok := Decode(odd, true); !ok || string(got) != "a" {
		t.Errorf("cut read: %q %v", got, ok)
	}
	// Half a surrogate pair at the end of a read.
	half := []byte{0xFF, 0xFE, 'a', 0, 0x3D, 0xD8}
	if got, _, ok := Decode(half, true); !ok || string(got) != "a" {
		t.Errorf("half a pair: %q %v", got, ok)
	}
}

func TestDecodeReadsLatin1(t *testing.T) {
	got, enc, ok := Decode([]byte("caf\xe9 na\xefve\n"), false)
	if !ok || enc != EncodingLatin1 || string(got) != "café naïve\n" {
		t.Fatalf("%q %q %v", got, enc, ok)
	}
}

func TestDecodeLeavesUTF8CutByTheReadAlone(t *testing.T) {
	// "é" is two bytes; the read stopped after the first.
	if got, enc, ok := Decode([]byte("caf\xc3"), true); ok {
		t.Errorf("UTF-8 cut mid-character decoded as %s: %q", enc, got)
	}
}

func TestDecodeRefusesWhatIsNotText(t *testing.T) {
	cases := map[string][]byte{
		// UTF-8 is not a second encoding: IsText already took it.
		"utf-8": []byte("café\n"),
		"nul":   []byte("caf\xe9\x00"),
		"empty": {},
		// C1 controls are not in Latin-1 text.
		"c1": bytes.Repeat([]byte{0x85, 0x90, 'a'}, 100),
		// UTF-16 whose characters are mostly controls.
		"utf-16 controls": append([]byte{0xFF, 0xFE}, bytes.Repeat([]byte{7, 0}, 100)...),
	}
	// Random bytes with the NULs taken out: a binary that would be "Latin-1"
	// if every byte that is a character counted.
	r := rand.New(rand.NewSource(3))
	noise := make([]byte, 4096)
	for i := range noise {
		for noise[i] == 0 {
			noise[i] = byte(r.Intn(256))
		}
	}
	cases["noise"] = noise
	for name, b := range cases {
		if got, enc, ok := Decode(b, false); ok {
			t.Errorf("%s decoded as %s: %q", name, enc, got[:min(len(got), 40)])
		}
	}
}
