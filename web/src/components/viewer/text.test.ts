import { describe, expect, it } from 'vitest'

import {
  BLOCK_LINES,
  LONG_LINE,
  cutLines,
  MAX_MATCHES,
  blockOf,
  blocks,
  findMatches,
  firstMatchFrom,
  lineEndings,
  parseGoTo,
  splitLines,
  stepMatch,
} from './text'
import { countLines } from '../panels/preview'

describe('splitLines', () => {
  it('counts lines the way the footer does', () => {
    for (const t of ['', 'a', 'a\n', 'a\nb', 'a\nb\n', '\n', '\n\n', 'a\r\nb\r\n']) {
      expect(splitLines(t).length, JSON.stringify(t)).toBe(countLines(t.replace(/\r\n/g, '\n')))
    }
  })

  it('leaves no carriage return on a Windows line', () => {
    // Drawn, it is invisible; copied, it goes wherever the copy is pasted.
    expect(splitLines('a\r\nb\r\n')).toEqual(['a', 'b'])
  })

  it('splits a classic Mac file on its lone carriage returns', () => {
    expect(splitLines('a\rb\r')).toEqual(['a', 'b'])
  })

  it('keeps an empty line in the middle', () => {
    expect(splitLines('a\n\nb')).toEqual(['a', '', 'b'])
  })
})

describe('lineEndings', () => {
  it('names each kind', () => {
    expect(lineEndings('a\nb\n')).toBe('LF')
    expect(lineEndings('a\r\nb\r\n')).toBe('CRLF')
    expect(lineEndings('a\rb\r')).toBe('CR')
    expect(lineEndings('one line')).toBe('none')
  })

  it('says mixed rather than picking one', () => {
    expect(lineEndings('a\r\nb\n')).toBe('mixed')
    expect(lineEndings('a\rb\n')).toBe('mixed')
  })

  it('does not count a CRLF as a CR and an LF', () => {
    expect(lineEndings('\r\n'.repeat(1000))).toBe('CRLF')
  })
})

describe('blocks', () => {
  it('covers every line exactly once', () => {
    for (const n of [0, 1, BLOCK_LINES - 1, BLOCK_LINES, BLOCK_LINES + 1, 50_000]) {
      const bs = blocks(n)
      let next = 0
      for (const b of bs) {
        expect(b.start).toBe(next)
        expect(b.end - b.start).toBeGreaterThan(0)
        expect(b.end - b.start).toBeLessThanOrEqual(BLOCK_LINES)
        next = b.end
      }
      expect(next).toBe(n)
    }
  })

  it('puts a line in the block that holds it', () => {
    const bs = blocks(1000)
    for (const line of [0, 255, 256, 999]) {
      const b = bs[blockOf(line)]
      expect(line >= b.start && line < b.end).toBe(true)
    }
  })

  it('draws the largest file the server sends in a couple of hundred blocks', () => {
    // The number the browser has to consider before anything is laid out.
    expect(blocks(50_000).length).toBeLessThanOrEqual(200)
  })
})

describe('findMatches', () => {
  it('finds every occurrence, in order, without overlap', () => {
    const { matches } = findMatches(['aaaa', 'xaax'], 'aa')
    expect(matches).toEqual([
      { line: 0, start: 0, end: 2 },
      { line: 0, start: 2, end: 4 },
      { line: 1, start: 1, end: 3 },
    ])
  })

  it('ignores case until the query has a capital', () => {
    const lines = ['Error here', 'error there', 'ERROR']
    expect(findMatches(lines, 'error').matches.map((m) => m.line)).toEqual([0, 1, 2])
    expect(findMatches(lines, 'Error').matches.map((m) => m.line)).toEqual([0])
  })

  it('treats the query as text, not a pattern', () => {
    expect(findMatches(['a.c', 'abc'], 'a.c').matches.map((m) => m.line)).toEqual([0])
    expect(findMatches(['(x'], '(').matches).toHaveLength(1)
  })

  it('finds nothing for nothing', () => {
    expect(findMatches(['abc'], '').matches).toEqual([])
  })

  it('stops counting at the bound and says so', () => {
    const lines = Array.from({ length: 1000 }, () => 'a'.repeat(100))
    const { matches, capped } = findMatches(lines, 'a')
    expect(matches).toHaveLength(MAX_MATCHES)
    expect(capped).toBe(true)
    expect(findMatches(['aaa'], 'a').capped).toBe(false)
  })

  it('reports offsets into the line as drawn when lowercasing changes its length', () => {
    // 'İ'.toLowerCase() is two code units. Offsets taken from the lowered
    // string would point one character late for everything after it.
    const line = 'İx needle'
    const [m] = findMatches([line], 'needle').matches
    expect(line.slice(m.start, m.end)).toBe('needle')
  })
})

describe('stepping', () => {
  it('wraps both ways', () => {
    expect(stepMatch(2, 3, 1)).toBe(0)
    expect(stepMatch(0, 3, -1)).toBe(2)
    expect(stepMatch(-1, 3, 1)).toBe(0)
    expect(stepMatch(-1, 3, -1)).toBe(2)
    expect(stepMatch(0, 0, 1)).toBe(-1)
  })

  it('starts where the reader is', () => {
    const m = [0, 5, 5, 9].map((line) => ({ line, start: 0, end: 1 }))
    expect(firstMatchFrom(m, 0)).toBe(0)
    expect(firstMatchFrom(m, 3)).toBe(1)
    expect(firstMatchFrom(m, 9)).toBe(3)
    // Past the last match, round to the top rather than to nothing.
    expect(firstMatchFrom(m, 10)).toBe(0)
    expect(firstMatchFrom([], 3)).toBe(-1)
  })
})

describe('parseGoTo', () => {
  it('reads a 1-based line and answers 0-based', () => {
    expect(parseGoTo('1', 10)).toBe(0)
    expect(parseGoTo(' 7 ', 10)).toBe(6)
  })

  it('takes the line out of what a compiler prints', () => {
    expect(parseGoTo('src/a.ts:42', 100)).toBe(41)
    expect(parseGoTo('42:7', 100)).toBe(41)
    expect(parseGoTo('src/a.ts:42:7', 100)).toBe(41)
  })

  it('goes to the bottom for a line past it', () => {
    expect(parseGoTo('9999', 400)).toBe(399)
  })

  it('refuses what is not a line', () => {
    for (const s of ['', 'abc', '0', '-3', '1.5']) expect(parseGoTo(s, 10), s).toBeNull()
    expect(parseGoTo('3', 0)).toBeNull()
  })
})

describe('cutLines', () => {
  it('cuts only the lines past the bound, and leaves the rest alone', () => {
    const long = 'x'.repeat(LONG_LINE + 5)
    const out = cutLines(['a', long, 'b'])
    expect(out).toEqual(['a', 'x'.repeat(LONG_LINE), 'b'])
  })

  it('is the same array when nothing is long, so nothing downstream redraws', () => {
    const lines = ['a', 'b']
    expect(cutLines(lines)).toBe(lines)
  })
})
