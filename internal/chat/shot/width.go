package shot

// runeWidth is the number of cells a rune occupies, which must agree with
// what tmux thought when it laid the line out, or every character after a
// Chinese one on the same row is drawn one cell to the left of where the
// program put it and boxes drawn with line characters come apart. The
// tables are the East Asian Wide and Fullwidth blocks plus the combining
// marks a shell is likely to emit; a full wcwidth is a thousand ranges for
// scripts that a coding agent's terminal does not show.
func runeWidth(r rune) int {
	switch {
	case r >= 0x0300 && r <= 0x036F, // combining diacritics
		r >= 0x200B && r <= 0x200F, // zero-width space, joiners, marks
		r >= 0xFE00 && r <= 0xFE0F, // variation selectors
		r >= 0x20D0 && r <= 0x20FF: // combining marks for symbols
		return 0
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E,   // CJK radicals, punctuation
		r >= 0x3041 && r <= 0x33FF,   // kana, bopomofo, compatibility
		r >= 0x3400 && r <= 0x4DBF,   // CJK extension A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF,   // Yi
		r >= 0xAC00 && r <= 0xD7A3,   // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF,   // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE4F,   // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60,   // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,   // fullwidth signs
		r >= 0x1F300 && r <= 0x1FAFF, // emoji
		r >= 0x20000 && r <= 0x3FFFD: // CJK extensions B and up
		return 2
	}
	return 1
}
