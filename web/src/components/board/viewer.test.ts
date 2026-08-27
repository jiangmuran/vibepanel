import { describe, expect, it } from 'vitest'

import { rowSpan } from './Tile'
import { effectiveHeight, effectiveSpan, forViewport } from './viewer'

/**
 * One stored board opens on a phone and on a 4K wall.
 *
 * That property was worth having before the grid was widened and it is worth
 * more now: the owner composes for a television and the same link gets opened
 * on somebody's handset on the way to a meeting. The collapse used to be three
 * CSS media queries changing the column count; it is here instead, because the
 * grid is twelve columns wide at every size and a span of 7 in a narrower grid
 * is placed by rules nobody wants to reason about.
 */
describe('a board collapsing onto the screen it is opened on', () => {
  it('keeps the composed width on a wall', () => {
    expect(effectiveSpan(3, 3840)).toBe(3)
    expect(effectiveSpan(4, 1920)).toBe(4)
    expect(effectiveSpan(12, 1440)).toBe(12)
  })

  it('gives nothing less than half the grid on a laptop', () => {
    // The old two-column band. A quarter-width tile is half a laptop.
    expect(effectiveSpan(3, 900)).toBe(6)
    expect(effectiveSpan(2, 900)).toBe(6)
    // And something already wider is left alone rather than widened again.
    expect(effectiveSpan(8, 900)).toBe(8)
  })

  it('gives the whole width on a phone', () => {
    for (const span of [2, 3, 4, 6, 8, 9, 12]) {
      expect(effectiveSpan(span, 390)).toBe(12)
    }
  })

  it('flattens the hero on a phone, where the width has already paid for it', () => {
    // A tile three rows tall and twelve wide is three screens of one number.
    expect(effectiveHeight(3, 390)).toBe(1)
    expect(effectiveHeight(4, 900)).toBe(2)
    expect(effectiveHeight(3, 2560)).toBe(3)
  })

  it('survives a board that says something impossible', () => {
    // The server refuses these on the way in and drops them on the way out.
    // This is the third place the rule is applied, because it is the one
    // running on somebody else's machine: `grid-column: span NaN` is a tile
    // that swallows the board.
    expect(effectiveSpan(Number.NaN, 1920)).toBe(12)
    expect(effectiveSpan(0, 1920)).toBe(12)
    expect(effectiveSpan(-4, 1920)).toBe(12)
    expect(effectiveSpan(99, 1920)).toBe(12)
    expect(effectiveHeight(Number.NaN, 1920)).toBe(1)
    expect(effectiveHeight(99, 1920)).toBe(4)
    expect(effectiveHeight(undefined, 1920)).toBe(1)
  })

  it('leaves everything else about a widget alone', () => {
    const w = { kind: 'spendbars', span: 6, height: 2, by: 'day', days: 30, page: 1 }
    expect(forViewport(w, 2560)).toEqual(w)
    // Only the two numbers this screen decides.
    expect(forViewport(w, 390)).toEqual({ ...w, span: 12, height: 1 })
  })

  it('clamps the row span a tile actually asks the grid for', () => {
    // The last line of defence, and the one running on somebody else's
    // machine. `grid-row: span NaN` is a tile that swallows the board.
    expect(rowSpan(undefined)).toBe(1)
    expect(rowSpan(0)).toBe(1)
    expect(rowSpan(-3)).toBe(1)
    expect(rowSpan(Number.NaN)).toBe(1)
    expect(rowSpan(2.7)).toBe(2)
    expect(rowSpan(4)).toBe(4)
    expect(rowSpan(999)).toBe(4)
  })
})
