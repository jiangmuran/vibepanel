import { readFileSync } from 'node:fs'
import { inflateSync } from 'node:zlib'
import { describe, expect, it } from 'vitest'

import { fontFormat } from './fontkind'
import { nameFromTable, sfntNameTable, woffNameTable } from './fontname'

// The fixtures preview-check opens, written by make.py with fontTools.
const fixture = (ext: string) =>
  new Uint8Array(readFileSync(new URL(`../../../scripts/fixtures/preview/files/fonts/fixture.${ext}`, import.meta.url)))

describe('fontFormat', () => {
  it('knows the four formats by their first bytes', () => {
    expect(fontFormat(fixture('ttf'))).toBe('TrueType')
    expect(fontFormat(fixture('otf'))).toBe('OpenType')
    expect(fontFormat(fixture('woff'))).toBe('WOFF')
    expect(fontFormat(fixture('woff2'))).toBe('WOFF2')
    expect(fontFormat(new TextEncoder().encode('true'))).toBe('TrueType')
  })

  it('is null for everything else, a collection included', () => {
    expect(fontFormat(new Uint8Array([0x74, 0x74, 0x63, 0x66, 0, 1, 0, 0]))).toBeNull()
    expect(fontFormat(new TextEncoder().encode('%PDF'))).toBeNull()
    expect(fontFormat(new Uint8Array([0, 1, 0]))).toBeNull()
  })
})

describe('the name a font gives itself', () => {
  it('is read from a TrueType and an OpenType file', () => {
    for (const ext of ['ttf', 'otf']) {
      const t = sfntNameTable(fixture(ext))
      expect(t, ext).not.toBeNull()
      expect(nameFromTable(t!), ext).toBe('Vibepanel Fixture Regular')
    }
  })

  it('is read from a WOFF file once its table is inflated', () => {
    const w = woffNameTable(fixture('woff'))
    expect(w).not.toBeNull()
    const table = w!.compressed ? new Uint8Array(inflateSync(w!.data)) : w!.data
    expect(nameFromTable(table)).toBe('Vibepanel Fixture Regular')
  })

  it('is null for a table that runs past the file, rather than read out of it', () => {
    const ttf = fixture('ttf')
    expect(sfntNameTable(ttf.subarray(0, 200))).toBeNull()
    expect(woffNameTable(fixture('woff').subarray(0, 60))).toBeNull()
    expect(nameFromTable(new Uint8Array([0, 0, 0, 1, 0, 18, 0, 3, 0, 1, 4, 9, 0, 4, 0, 200, 0, 0]))).toBeNull()
  })
})
