/**
 * What a font says about itself: the specimen's text and sizes, and the name
 * read from its `name` table. Pure, so the reading is tested on bytes.
 */

/** What the specimen shows, besides the font's name and the file's. */
export const SPECIMEN = {
  upper: 'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
  lower: 'abcdefghijklmnopqrstuvwxyz',
  digits: '0123456789 !?&@#%()[]{}',
  pangram: 'The quick brown fox jumps over the lazy dog.',
}

/** The three sizes, in pixels. */
export const SPECIMEN_SIZES = [14, 24, 40] as const

const u16 = (b: Uint8Array, o: number) => (b[o] << 8) | b[o + 1]
const u32 = (b: Uint8Array, o: number) => ((b[o] << 24) >>> 0) + (b[o + 1] << 16) + (b[o + 2] << 8) + b[o + 3]

/** Where the `name` table is in a TrueType or OpenType file, or null. */
export function sfntNameTable(b: Uint8Array): Uint8Array | null {
  if (b.length < 12) return null
  const n = u16(b, 4)
  for (let i = 0; i < n; i++) {
    const at = 12 + 16 * i
    if (at + 16 > b.length) return null
    if (String.fromCharCode(b[at], b[at + 1], b[at + 2], b[at + 3]) !== 'name') continue
    const off = u32(b, at + 8)
    const len = u32(b, at + 12)
    if (off + len > b.length) return null
    return b.subarray(off, off + len)
  }
  return null
}

/**
 * Where the `name` table is in a WOFF file, and whether it is compressed:
 * zlib when its compressed length is less than its length, which
 * DecompressionStream('deflate') reads. WOFF2 compresses all of its tables as
 * one Brotli stream, which no browser API decompresses, so it has no name
 * here and the specimen uses the file's.
 */
export function woffNameTable(b: Uint8Array): { data: Uint8Array; compressed: boolean } | null {
  if (b.length < 44) return null
  const n = u16(b, 12)
  for (let i = 0; i < n; i++) {
    const at = 44 + 20 * i
    if (at + 20 > b.length) return null
    if (String.fromCharCode(b[at], b[at + 1], b[at + 2], b[at + 3]) !== 'name') continue
    const off = u32(b, at + 4)
    const comp = u32(b, at + 8)
    const orig = u32(b, at + 12)
    if (off + comp > b.length) return null
    return { data: b.subarray(off, off + comp), compressed: comp < orig }
  }
  return null
}

/**
 * The font's name from its `name` table: the full name (ID 4), or the family
 * (ID 1). Windows' UTF-16 records first, which is what nearly every font
 * carries; Mac Roman as a fallback for an old one, read as Latin-1, which it
 * is for the letters a name is made of.
 */
export function nameFromTable(t: Uint8Array): string | null {
  if (t.length < 6) return null
  const count = u16(t, 2)
  const strings = u16(t, 4)
  const found: { id: number; platform: number; text: string }[] = []
  for (let i = 0; i < count; i++) {
    const at = 6 + 12 * i
    if (at + 12 > t.length) break
    const platform = u16(t, at)
    const id = u16(t, at + 6)
    const len = u16(t, at + 8)
    const off = strings + u16(t, at + 10)
    if ((id !== 4 && id !== 1) || off + len > t.length) continue
    let text = ''
    if (platform === 3 || platform === 0) {
      for (let j = 0; j + 1 < len; j += 2) text += String.fromCharCode(u16(t, off + j))
    } else if (platform === 1) {
      for (let j = 0; j < len; j++) text += String.fromCharCode(t[off + j])
    } else continue
    if (text.trim()) found.push({ id, platform, text: text.trim() })
  }
  const pick = (id: number) =>
    found.find((f) => f.id === id && f.platform !== 1) ?? found.find((f) => f.id === id)
  return (pick(4) ?? pick(1))?.text ?? null
}
