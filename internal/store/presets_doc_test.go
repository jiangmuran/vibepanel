package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs/features.md counts the presets, in two languages, and the count was
// wrong in both at once.
//
// The table has one row per screen and lists every board on it by name, and a
// paragraph above says how many there are altogether. Nothing connected any of
// that to presets.go, so a preset added or moved between screens left five
// numbers behind in prose that still read as though somebody had checked them.
//
// It went wrong in the direction that is hardest to catch: a documentation
// pass "corrected" thirty to twenty-nine and deleted a real preset from the
// laptop row to make its own arithmetic work. The docs were then internally
// consistent, confidently worded and describing a product that does not exist.
// A reader has no way to tell, and neither did the review.
//
// This counts the ` · ` separated names in each row rather than reading them,
// because the names in the table are prose translations of the ids and always
// will be. The count is the part that is checkable, and the count is the part
// that was wrong.
func TestTheFeatureDocCountsThePresetsCorrectly(t *testing.T) {
	byScreen := map[string]int{}
	for _, p := range Presets() {
		byScreen[p.Screen]++
	}

	// Spelled out, because the docs spell it out. The map covers the range the
	// catalogue plausibly moves through; a count outside it fails asking for
	// the map to be extended rather than passing on a word nobody wrote.
	enWord := map[int]string{28: "twenty-eight", 29: "twenty-nine", 30: "thirty", 31: "thirty-one", 32: "thirty-two", 33: "thirty-three", 34: "thirty-four", 35: "thirty-five"}
	zhWord := map[int]string{28: "二十八", 29: "二十九", 30: "三十", 31: "三十一", 32: "三十二", 33: "三十三", 34: "三十四", 35: "三十五"}
	n := len(Presets())
	if enWord[n] == "" || zhWord[n] == "" {
		t.Fatalf("%d presets is outside the spelled-out range; extend enWord and zhWord", n)
	}

	// Both languages, because the number is written out as a word in each and
	// a fix applied to one file is a fix applied to one file.
	for _, doc := range []struct {
		file  string
		total string
		rows  map[string]string // screen -> the row's leading cell
	}{
		{
			file:  "features.md",
			total: enWord[n] + " starting points",
			rows: map[string]string{
				screenPhone:  "a phone",
				screenLaptop: "a laptop",
				screenWall:   "a screen on a wall",
				screenBig:    "a 4K wall",
			},
		},
		{
			file:  "features.zh-CN.md",
			total: zhWord[n] + "个起手式",
			rows: map[string]string{
				screenPhone:  "手机",
				screenLaptop: "电脑",
				screenWall:   "墙上的屏",
				screenBig:    "4K",
			},
		},
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "docs", doc.file))
		if err != nil {
			t.Fatalf("%s: %v", doc.file, err)
		}
		text := string(raw)

		// Whitespace collapsed and case folded first: the count is a word in a
		// paragraph and where the line wraps is the formatter's business, not
		// a fact about the product. Matching the phrase and not the bare word
		// is the point -- "thirty" also appears in "over thirty days", and an
		// assertion that cannot tell those apart is decoration.
		flat := strings.ToLower(strings.Join(strings.Fields(text), " "))
		if !strings.Contains(flat, doc.total) {
			t.Errorf("%s: does not say %q; there are %d presets", doc.file, doc.total, n)
		}

		for screen, cell := range doc.rows {
			row := regexp.MustCompile(`(?m)^\| ` + regexp.QuoteMeta(cell) + `[^|]*\|([^|]+)\|`)
			m := row.FindStringSubmatch(text)
			if m == nil {
				t.Errorf("%s: no table row starting %q", doc.file, cell)
				continue
			}
			got := len(strings.Split(m[1], "·"))
			if got != byScreen[screen] {
				t.Errorf("%s: the %q row names %d boards, presets.go has %d for screen %q",
					doc.file, cell, got, byScreen[screen], screen)
			}
		}
	}
}
