import { describe, expect, it } from 'vitest'

import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'

import { mouseButtonCode, sgrMouseReport, wantsReport } from './mouseReport'

describe('sgrMouseReport', () => {
  it('is one-based on the wire, M for press and m for release', () => {
    expect(sgrMouseReport('down', 0, 39, 9)).toBe('\x1b[<0;40;10M')
    expect(sgrMouseReport('up', 0, 39, 9)).toBe('\x1b[<0;40;10m')
  })

  it('adds 32 for motion and the modifier bits xterm would', () => {
    expect(sgrMouseReport('move', 0, 0, 0)).toBe('\x1b[<32;1;1M')
    expect(mouseButtonCode(2, { shift: true, alt: true, ctrl: true }, false)).toBe(2 + 4 + 8 + 16)
  })

  it('reports no button as 3', () => {
    expect(mouseButtonCode(-1, {}, true)).toBe(35)
    expect(mouseButtonCode(4, {}, false)).toBe(3)
  })
})

describe('wantsReport', () => {
  it('follows the tracking mode', () => {
    expect(wantsReport('x10', 'down', false)).toBe(true)
    expect(wantsReport('x10', 'up', false)).toBe(false)
    expect(wantsReport('vt200', 'up', false)).toBe(true)
    expect(wantsReport('vt200', 'move', true)).toBe(false)
    expect(wantsReport('drag', 'move', true)).toBe(true)
    expect(wantsReport('drag', 'move', false)).toBe(false)
    expect(wantsReport('any', 'move', false)).toBe(true)
    expect(wantsReport('none', 'down', false)).toBe(false)
  })
})

/**
 * Terminal.tsx reads the active mouse encoding from xterm's core, because the
 * public `modes` has the tracking mode and not the encoding. Pinned against
 * the installed bundle the way imeInput.test.ts pins its fields: if an upgrade
 * renames either, the scaled viewer's clicks go quietly back to being wrong,
 * and this is what says so.
 */
describe('the xterm internals the scaled-viewer reports rely on', () => {
  it('still exist in the installed bundle', () => {
    const require = createRequire(import.meta.url)
    const bundle = readFileSync(require.resolve('@xterm/xterm/lib/xterm.js'), 'utf8')
    expect(bundle).toContain('coreMouseService')
    expect(bundle).toContain('activeEncoding')
    expect(bundle).toContain('"SGR"')
  })
})
