/**
 * Neutralises the characters that make a name lie about what it is.
 *
 * Unicode has directional overrides - U+202E and its relatives - whose entire
 * job is to reverse the visual order of the text after them. In a list of
 * files next to a download button, that is not a curiosity. Measured in a real
 * browser, with the override written here as an escape:
 *
 *   logical  "report\u202Efdp.exe"  ->  visual  "reportexe.pdf"
 *   logical  "deploy \u202Egnp.hs"  ->  visual  "deploy sh.png"
 *
 * An executable and a shell script, each wearing the extension of something
 * inert, in a panel where the thing beside the name is a download link. Both
 * names reach the screen from outside: filenames come from whatever an agent
 * wrote to disk, and session titles come from `pane_title`, which any program
 * sets with a two-byte escape sequence.
 *
 * `<bdi>` does not fix this, and it is worth saying why, because it is the
 * obvious answer: isolation stops a string from affecting the direction of the
 * text *around* it, and this override is inside the string. Measured - the bdi
 * version reordered identically. The character has to go.
 *
 * Replaced rather than dropped: a name that renders as "reportfdp.exe" hides
 * that anything was there, and U+FFFD is the conventional way to say
 * "something unprintable was here". C0 controls go too; they render as nothing
 * at all, which is its own way of hiding a suffix.
 *
 * This is display only. The path sent back to the server is the real one, or
 * the file could not be opened.
 *
 * Written with escapes rather than the characters themselves, deliberately.
 * These bytes are invisible in an editor, in a diff and in a code review - the
 * same property this function exists to defeat.
 */
const DECEPTIVE =
  // C0 and DEL, then C1, then the bidi embeddings, overrides and isolates,
  // then the standalone marks. U+061C is the Arabic letter mark, which belongs
  // to the same family and is routinely forgotten.
  //
  // Then the invisibles that are not bidi at all and hide a difference rather
  // than a suffix: U+200B zero-width space, U+2060 word joiner, U+FEFF
  // (zero-width no-break space, the byte-order mark) and U+00AD soft hyphen.
  // Two sessions titled "deploy" and "dep<U+200B>loy" are the same three rows
  // of pixels, and picking the wrong one means typing into the wrong agent.
  // Titles come from `pane_title`, which any program sets.
  //
  // U+200C and U+200D are deliberately NOT here, and completing the range is
  // the tempting mistake. Measured:
  //
  //   "\u{1F468}\u200D\u{1F469}\u200D\u{1F467}" -> family emoji, three broken glyphs without it
  //   "\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645" -> Persian, mis-joined without it
  //
  // Both are load-bearing in ordinary text that people put in filenames, and
  // breaking a Persian name to defeat a lookalike is the worse trade.
  // The control characters are the subject here, not an accident: this is the
  // function whose job is removing them.
  // eslint-disable-next-line no-control-regex
  /[\u0000-\u001F\u007F-\u009F\u00AD\u202A-\u202E\u2066-\u2069\u200E\u200F\u200B\u2060\uFEFF\u061C]/g

export function safeText(s: string): string {
  return s.replace(DECEPTIVE, '\uFFFD')
}
