import { describe, expect, it } from 'vitest'

import { pieces } from './pieces'
import type { Tok } from './tokens'

const toks: Tok[] = [
  { k: 'keyword', v: 'const' },
  { k: 'plain', v: ' x = ' },
  { k: 'string', v: "'abc'" },
]
const text = "const x = 'abc'"

describe('pieces', () => {
  it('is the tokens when nothing matches', () => {
    expect(pieces(text, toks, [], [])).toEqual(toks.map((t) => ({ ...t, match: -1 })))
  })

  it('keeps each token\'s kind inside a match that crosses tokens', () => {
    // "st x = 'a": from inside the keyword to inside the string.
    const m = [{ line: 0, start: 3, end: 12 }]
    expect(pieces(text, toks, [0], m)).toEqual([
      { k: 'keyword', v: 'con', match: -1 },
      { k: 'keyword', v: 'st', match: 0 },
      { k: 'plain', v: ' x = ', match: 0 },
      { k: 'string', v: "'a", match: 0 },
      { k: 'string', v: "bc'", match: -1 },
    ])
  })

  it('puts back exactly the line', () => {
    const m = [
      { line: 0, start: 0, end: 2 },
      { line: 0, start: 11, end: 13 },
    ]
    expect(pieces(text, toks, [0, 1], m).map((p) => p.v).join('')).toBe(text)
  })

  it('works on a plain line, with no tokens at all', () => {
    const m = [{ line: 0, start: 2, end: 4 }]
    expect(pieces('abcdef', undefined, [0], m)).toEqual([
      { k: 'plain', v: 'ab', match: -1 },
      { k: 'plain', v: 'cd', match: 0 },
      { k: 'plain', v: 'ef', match: -1 },
    ])
  })

  it('keeps two matches that touch apart', () => {
    const m = [
      { line: 0, start: 0, end: 2 },
      { line: 0, start: 2, end: 4 },
    ]
    expect(pieces('aaaa', undefined, [0, 1], m).map((p) => p.match)).toEqual([0, 1])
  })
})
