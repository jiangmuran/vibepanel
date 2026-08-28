import { describe, expect, it } from 'vitest'

import type { ShareWidget } from '../../protocol/wire'
import { gapAt, insertWidget, moveWidget, removeWidget, snapHeight, snapSpan } from './edit'
import { clampDensity, rows } from './density'

const w = (kind: string): ShareWidget => ({ kind, span: 3 })

describe('moving a widget', () => {
  const list = [w('a'), w('b'), w('c')]

  it('moves right by one, which the naive splice gets wrong', () => {
    // Dropping "a" into the gap after "b" is gap 2. Removing "a" first shifts
    // that gap down to 1, and a version that does not adjust puts "a" back
    // where it started -- the bug this whole file is a function for.
    expect(moveWidget(list, 0, 2).map((x) => x.kind)).toEqual(['b', 'a', 'c'])
  })

  it('moves left by one', () => {
    expect(moveWidget(list, 2, 0).map((x) => x.kind)).toEqual(['c', 'a', 'b'])
  })

  it('returns the same array when the drop changes nothing', () => {
    // Identity, not equality: this is what stops a drag that went nowhere from
    // rewriting the board and saving it.
    expect(moveWidget(list, 1, 1)).toBe(list)
    expect(moveWidget(list, 1, 2)).toBe(list)
  })

  it('refuses an index that is not there', () => {
    expect(moveWidget(list, 5, 0)).toBe(list)
    expect(moveWidget(list, 0, 9)).toBe(list)
    expect(moveWidget(list, -1, 0)).toBe(list)
  })

  it('drops at the end', () => {
    expect(moveWidget(list, 0, 3).map((x) => x.kind)).toEqual(['b', 'c', 'a'])
  })
})

describe('adding and removing', () => {
  it('inserts into a gap', () => {
    expect(insertWidget([w('a'), w('b')], w('x'), 1).map((k) => k.kind)).toEqual(['a', 'x', 'b'])
  })

  it('clamps an insert past the end rather than dropping it', () => {
    expect(insertWidget([w('a')], w('x'), 9).map((k) => k.kind)).toEqual(['a', 'x'])
  })

  it('removes by index and leaves a bad index alone', () => {
    const list = [w('a'), w('b')]
    expect(removeWidget(list, 0).map((k) => k.kind)).toEqual(['b'])
    expect(removeWidget(list, 7)).toBe(list)
  })
})

describe('snapping a resize', () => {
  const steps = [2, 3, 4, 6, 8, 9, 12]

  it('lands on an offered width and never between two', () => {
    // 7/12 is not offered; a drag that reaches it must round to one that is,
    // or the catalogue's list of widths is a lie.
    expect(steps).not.toContain(snapSpan(7 / 12, steps, 12) === 7 ? 7 : -1)
    expect(steps).toContain(snapSpan(7 / 12, steps, 12))
    expect(snapSpan(0.5, steps, 12)).toBe(6)
    expect(snapSpan(0.01, steps, 12)).toBe(2)
    expect(snapSpan(1, steps, 12)).toBe(12)
  })

  it('survives a drag outside the box', () => {
    expect(snapSpan(-3, steps, 12)).toBe(2)
    expect(snapSpan(9, steps, 12)).toBe(12)
  })

  it('falls back to the full width when the server offered nothing', () => {
    expect(snapSpan(0.5, [], 12)).toBe(12)
  })

  it('snaps a height to whole rows within the bound', () => {
    expect(snapHeight(210, 100, 4)).toBe(2)
    expect(snapHeight(9000, 100, 4)).toBe(4)
    expect(snapHeight(-40, 100, 4)).toBe(1)
    // A row height of zero is a canvas that has not been laid out yet.
    expect(snapHeight(100, 0, 4)).toBe(1)
  })
})

describe('where a drop lands', () => {
  // Three rows of two, as a twelve-column board flows them.
  //
  // Three and not two: with two rows, a version that takes the *last* row it
  // walked past rather than the last one at or above the pointer still lands on
  // the right answer, because the trailing check catches it. The third row is
  // what makes "the row the pointer is in" mean something.
  const slots = [
    { index: 0, left: 0, top: 0, right: 100, bottom: 100 },
    { index: 1, left: 100, top: 0, right: 200, bottom: 100 },
    { index: 2, left: 0, top: 100, right: 100, bottom: 200 },
    { index: 3, left: 100, top: 100, right: 200, bottom: 200 },
    { index: 4, left: 0, top: 200, right: 100, bottom: 300 },
    { index: 5, left: 100, top: 200, right: 200, bottom: 300 },
  ]

  it('takes the left half of a widget as the gap before it', () => {
    expect(gapAt(slots, 10, 50)).toBe(0)
    expect(gapAt(slots, 110, 50)).toBe(1)
  })

  it('takes the right half as the gap after it', () => {
    expect(gapAt(slots, 90, 50)).toBe(1)
    expect(gapAt(slots, 190, 50)).toBe(2)
  })

  it('stays in the row the pointer is in', () => {
    // The failure this exists for: nearest-centre over every rectangle puts a
    // pointer in the blank to the right of a short last row onto a widget two
    // rows up, because that one happens to be closer.
    expect(gapAt(slots, 10, 150)).toBe(2)
    expect(gapAt(slots, 190, 150)).toBe(4)
  })

  it('past the right edge of a row is the gap after that row', () => {
    expect(gapAt(slots, 900, 50)).toBe(2)
  })

  it('below everything is the last row', () => {
    expect(gapAt(slots, 10, 9000)).toBe(4)
  })

  it('an empty canvas is one gap', () => {
    expect(gapAt([], 40, 40)).toBe(0)
  })
})

describe('density', () => {
  it('is clamped on this side too, because the board came out of a database', () => {
    expect(clampDensity(0)).toBe(2)
    expect(clampDensity(undefined)).toBe(2)
    expect(clampDensity(99)).toBe(3)
    expect(clampDensity(1)).toBe(1)
  })

  it('grows a list rather than shrinking type', () => {
    expect(rows(1, 10)).toBeLessThan(rows(2, 10))
    expect(rows(3, 10)).toBeGreaterThan(rows(2, 10))
    expect(rows(1, 1)).toBeGreaterThanOrEqual(1)
  })
})
