import { describe, expect, it } from 'vitest'

import { HEX_ROW, hexLines, hexRow } from './hex'

const bytes = (...b: number[]) => new Uint8Array(b)

describe('hexRow', () => {
  it('lays a row out the way hexdump -C does', () => {
    const elf = bytes(0x7f, 0x45, 0x4c, 0x46, 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0)
    expect(hexRow(elf, 0)).toBe('00000000  7f 45 4c 46 02 01 01 00  00 00 00 00 00 00 00 00  |.ELF............|')
  })

  it('never puts a byte that is not printable ASCII into the text', () => {
    const all = new Uint8Array(256).map((_, i) => i)
    const ascii = hexLines(all).map((l) => l.slice(l.indexOf('|') + 1, -1)).join('')
    // eslint-disable-next-line no-control-regex
    expect(ascii).not.toMatch(/[\u0000-\u001f\u007f-￿]/)
    expect(ascii.slice(0x41, 0x44)).toBe('ABC')
  })

  it('keeps the ASCII column in its place on a short last row', () => {
    const lines = hexLines(new Uint8Array(HEX_ROW + 3))
    expect(lines).toHaveLength(2)
    expect(lines[1].indexOf('|')).toBe(lines[0].indexOf('|'))
    expect(lines[1].startsWith('00000010  ')).toBe(true)
  })

  it('is nothing for nothing', () => {
    expect(hexLines(new Uint8Array(0))).toEqual([])
  })
})
