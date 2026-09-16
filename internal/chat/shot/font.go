package shot

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"sync"
)

// unifontGz is the upstream unifont-16.0.04.hex.gz, unaltered; see
// UNIFONT-LICENSE next to it and the package comment for why it is here.
//
//go:embed unifont.hex.gz
var unifontGz []byte

// glyph is one bitmap, one row per 16 rows: 16 bytes for an 8-pixel-wide
// glyph, 32 bytes (two per row, high byte first) for a 16-pixel-wide one.
// Width is implied by the length, which is how the .hex format says it too.
type glyph []byte

const (
	glyphRows   = 16
	narrowBytes = glyphRows     // 8 px wide
	wideBytes   = glyphRows * 2 // 16 px wide
)

var (
	fontOnce sync.Once
	fontMap  map[rune]glyph
	fontErr  error
)

// font decompresses and parses the embedded file once. The first
// screenshot pays for it (about 40 ms); every later one finds the map.
func font() (map[rune]glyph, error) {
	fontOnce.Do(func() { fontMap, fontErr = parseHexGz(unifontGz) })
	return fontMap, fontErr
}

func parseHexGz(gz []byte) (map[rune]glyph, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("unifont: %w", err)
	}
	defer zr.Close()
	// One backing array for every bitmap, rather than a slice per glyph:
	// 57 000 small allocations are what made the first call take a
	// noticeable fraction of a second under -race.
	backing := make([]byte, 0, 1<<21)
	m := make(map[rune]glyph, 60000)
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		line := sc.Bytes()
		colon := bytes.IndexByte(line, ':')
		if colon < 4 || colon > 6 {
			continue
		}
		cp, ok := hexValue(line[:colon])
		if !ok {
			continue
		}
		hex := line[colon+1:]
		n := len(hex) / 2
		if (n != narrowBytes && n != wideBytes) || len(hex)%2 != 0 {
			continue
		}
		start := len(backing)
		for i := 0; i < n; i++ {
			v, ok := hexValue(hex[2*i : 2*i+2])
			if !ok {
				backing = backing[:start]
				n = 0
				break
			}
			backing = append(backing, byte(v))
		}
		if n == 0 {
			continue
		}
		m[rune(cp)] = backing[start : start+n : start+n]
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("unifont: %w", err)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("unifont: no glyphs in the embedded file")
	}
	return m, nil
}

// hexValue is strconv.ParseUint without the allocation of a string, on the
// hot path of a 3.7 MB parse.
func hexValue(b []byte) (uint32, bool) {
	var v uint32
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		case c >= 'A' && c <= 'F':
			v |= uint32(c-'A') + 10
		case c >= 'a' && c <= 'f':
			v |= uint32(c-'a') + 10
		default:
			return 0, false
		}
	}
	return v, true
}
