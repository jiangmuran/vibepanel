import { describe, expect, it } from 'vitest'

import { PREVIEW_MAX_BYTES, countLines, formatBytes } from './preview'

describe('the line count under a text preview', () => {
  it('agrees with wc -l about a trailing newline', () => {
    expect(countLines('a\nb\nc\n')).toBe(3)
    expect(countLines('a\nb\nc')).toBe(3)
    expect(countLines('one line')).toBe(1)
    expect(countLines('')).toBe(0)
    // A file that is one empty line is one line, not none.
    expect(countLines('\n')).toBe(1)
  })
})

describe('a size in the shortest form that is still true', () => {
  it('climbs a unit at a time', () => {
    expect(formatBytes(0)).toBe('0')
    expect(formatBytes(1023)).toBe('1023')
    expect(formatBytes(1024)).toBe('1.0K')
    expect(formatBytes(PREVIEW_MAX_BYTES)).toBe('8.0M')
  })
})
