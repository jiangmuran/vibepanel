import { describe, expect, it } from 'vitest'
import { safeText } from './text'

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

  it('leaves ordinary names alone, including non-Latin ones', () => {
    for (const s of ['README.md', '重构认证流程', 'hotfix', 'a-b_c.1']) {
      expect(safeText(s)).toBe(s)
    }
  })
})
