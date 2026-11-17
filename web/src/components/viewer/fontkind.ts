/**
 * A font file, by its first bytes: what the viewer needs to decide it is
 * one. Apart from fontname.ts, which only the specimen needs, so that the
 * name table's parser is in the specimen's chunk and not the viewer's.
 */

export type FontFormat = 'TrueType' | 'OpenType' | 'WOFF' | 'WOFF2'

/**
 * By the first four bytes, the way the server knows a picture. A font
 * collection (`ttcf`) is not here: FontFace takes one face, and which one a
 * collection would give is the browser's choice.
 */
export function fontFormat(b: Uint8Array): FontFormat | null {
  if (b.length < 4) return null
  const tag = String.fromCharCode(b[0], b[1], b[2], b[3])
  if (tag === '\x00\x01\x00\x00' || tag === 'true') return 'TrueType'
  if (tag === 'OTTO') return 'OpenType'
  if (tag === 'wOFF') return 'WOFF'
  if (tag === 'wOF2') return 'WOFF2'
  return null
}

/**
 * The largest font a specimen loads, whole: a CJK face is twenty megabytes.
 * Past it the file is its bytes, like any other binary.
 */
export const FONT_MAX_BYTES = 32 * 1024 * 1024
