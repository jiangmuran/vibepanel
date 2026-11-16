/**
 * A binary's bytes as lines of text: offset, sixteen bytes in hex in two
 * groups of eight, and the same sixteen as ASCII -- the layout of `hexdump -C`
 * and `xxd`, which is what anybody who reads a binary already reads.
 *
 * Text rather than a component of its own, so the hex view is the viewer's
 * line body: find, go to line, copy and the blocks work on it as on a log.
 * A byte outside printable ASCII is a `.` on the right, the same as both
 * tools, and never the byte itself: a control character in a document is the
 * thing `safeBody` exists to keep out of the page, and here it would be every
 * other byte.
 */

export const HEX_ROW = 16

function hex2(b: number): string {
  return b.toString(16).padStart(2, '0')
}

/** One row: `00000000  7f 45 4c 46 02 01 01 00  00 00 00 00 00 00 00 00  |.ELF............|` */
export function hexRow(bytes: Uint8Array, offset: number): string {
  const row = bytes.subarray(offset, offset + HEX_ROW)
  let hexPart = ''
  let ascii = ''
  for (let i = 0; i < HEX_ROW; i++) {
    if (i === 8) hexPart += ' '
    if (i < row.length) {
      hexPart += `${hex2(row[i])} `
      ascii += row[i] >= 0x20 && row[i] < 0x7f ? String.fromCharCode(row[i]) : '.'
    } else {
      // A short last row keeps the ASCII column where every other row has it.
      hexPart += '   '
    }
  }
  return `${offset.toString(16).padStart(8, '0')}  ${hexPart} |${ascii}|`
}

export function hexLines(bytes: Uint8Array): string[] {
  const out: string[] = []
  for (let off = 0; off < bytes.length; off += HEX_ROW) out.push(hexRow(bytes, off))
  return out
}
