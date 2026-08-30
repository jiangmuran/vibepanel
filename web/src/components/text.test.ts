import { describe, expect, it } from 'vitest'
import { safeText, safeBody } from './text'

// The characters under test are written as escapes throughout: a test whose
// subject is invisible in a diff cannot be reviewed.
describe('safeText', () => {
  it('neutralises the override that reverses a filename', () => {
    // Rendered in a real browser, the input shows as "reportexe.pdf".
    expect(safeText('report\u202Efdp.exe')).toBe('report\uFFFDfdp.exe')
  })

  it('covers the whole bidi family, not just the override', () => {
    const family = ['\u202A', '\u202B', '\u202C', '\u202D', '\u202E', '\u2066', '\u2067', '\u2068', '\u2069', '\u200E', '\u200F', '\u061C']
    for (const c of family) {
      expect(safeText(`a${c}b`)).toBe('a\uFFFDb')
    }
  })

  it('removes control characters, which render as nothing at all', () => {
    expect(safeText('a\u0001b\u0002c\u001Bd')).toBe('a\uFFFDb\uFFFDc\uFFFDd')
  })

  it('removes the invisibles that make two different names look identical', () => {
    // Not a suffix attack like the override above: these hide a *difference*.
    // "deploy" and "dep<ZWSP>loy" are the same pixels in a sidebar, and a
    // title is whatever a program passed to pane_title.
    expect(safeText('dep\u200Bloy')).not.toBe('deploy')
    expect(safeText('a\u2060b')).toBe('a\uFFFDb')
    expect(safeText('\uFEFFnotes.md')).toBe('\uFFFDnotes.md')
    expect(safeText('re\u00ADport.pdf')).toBe('re\uFFFDport.pdf')
  })

  it('removes C1 controls, which render as nothing just like C0', () => {
    // Reachable: a filename is any bytes but NUL and '/', and pane_title is
    // whatever a program sent. Only C0 and DEL were covered.
    expect(safeText('a\u0085b\u009Fc')).toBe('a\uFFFDb\uFFFDc')
  })

  it('keeps the two zero-width characters that ordinary text needs', () => {
    // The tempting mistake is to complete the range. U+200D joins an emoji
    // family into one glyph and U+200C is what makes Persian join correctly;
    // both appear in names people really use. Breaking those to defeat a
    // lookalike is the worse trade — see the comment beside DECEPTIVE.
    const family = '\u{1F468}\u200D\u{1F469}\u200D\u{1F467}'
    expect(safeText(family)).toBe(family)
    const persian = '\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645'
    expect(safeText(persian)).toBe(persian)
  })

  it('leaves ordinary names alone, including non-Latin ones', () => {
    for (const s of ['README.md', '重构认证流程', 'hotfix', 'a-b_c.1', 'naïve', 'Ω.txt']) {
      expect(safeText(s)).toBe(s)
    }
  })
})

describe('safeBody', () => {
  // The two characters a document is made of. safeText replaces both, which is
  // right for a filename and is what the markdown preview shipped applying to
  // file contents: every indented line and every wrapped paragraph came out
  // with a black diamond in it.
  it('keeps the tabs and newlines a file is made of', () => {
    expect(safeBody('a\tb')).toBe('a\tb')
    expect(safeBody('one\ntwo')).toBe('one\ntwo')
    expect(safeBody('\tif x {\n\t\treturn\n\t}')).toBe('\tif x {\n\t\treturn\n\t}')
  })

  it('still removes what safeText removes', () => {
    // A bidi override lies about the order of a paragraph exactly as it lies
    // about the order of a filename.
    expect(safeBody('report\u202Efdp.exe')).toBe('report\uFFFDfdp.exe')
    expect(safeBody('a\u0000b')).toBe('a\uFFFDb')
    expect(safeBody('a\u200Bb')).toBe('a\uFFFDb')
    // Vertical tab and form feed are not what a document is made of.
    expect(safeBody('a\u000Bb')).toBe('a\uFFFDb')
  })

  it('normalises CRLF instead of putting a box at the end of every line', () => {
    expect(safeBody('one\r\ntwo\r\n')).toBe('one\ntwo\n')
    // An old-Mac line ending is a line ending, not an overwrite.
    expect(safeBody('one\rtwo')).toBe('one\ntwo')
  })
})
