import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { fromFormControl, heightDrag } from './BoardEditor'

/**
 * The two ways the editor's own controls and its canvas got in each other's
 * way. Both are pointer/keyboard handlers, and vitest runs in node here (see
 * vitest.config.ts), so what is tested is the arithmetic and the predicate the
 * handlers were reduced to — plus, for the one line that cannot be reduced to
 * either, that the handler still starts with it.
 */

/** The canvas as styles.css draws it: `grid-auto-rows: minmax(8 * --vp-wall)`
 *  with `gap: calc(1.5 * --vp-wall)`, and --vp-wall is 13.33px at 1600 wide.
 *  A tile n rows tall covers n tracks and the n-1 gaps between them. */
const WALL = 40 / 3
const MAX_ROWS = 4
const TOP = 0
const tileAt = (rows: number) => ({
  top: TOP,
  height: rows * 8 * WALL + (rows - 1) * 1.5 * WALL,
})

/** A drag, move by move: the tile is re-measured every time, because the panel
 *  re-measures it every time, and that is what the bug was made of. */
function drag(ys: number[], startRows: number): number[] {
  const press = {
    x: 0,
    y: TOP + tileAt(startRows).height,
    index: 0,
    span: 6,
    height: startRows,
    row: tileAt(startRows).height / Math.max(1, startRows),
  }
  let rows = startRows
  return ys.map((y) => {
    rows = heightDrag(y, tileAt(rows), press, MAX_ROWS)
    return rows
  })
}

describe('dragging a tile taller', () => {
  it('does not flip under a pointer that is holding still', () => {
    // 165px down a one-row tile is the band where a unit measured from the
    // tile mid-drag and one measured at the press disagree. Six moves at the
    // same place used to give 2, 1, 2, 1, 2, 1.
    expect(drag([165, 165, 165, 165, 165, 165], 1)).toEqual([2, 2, 2, 2, 2, 2])
  })

  it('grows without going backwards', () => {
    expect(drag([120, 160, 200, 240, 280, 300, 300, 300], 1)).toEqual([1, 2, 2, 2, 3, 3, 3, 3])
  })

  it('shrinks all the way back to one row', () => {
    // A three-row tile dragged up used to stick at two: the unit stayed the
    // height of three rows however short the tile got.
    expect(drag([380, 340, 300, 260, 220, 180, 140, 120], 3)).toEqual([3, 3, 3, 2, 2, 2, 1, 1])
  })

  it('gives one pointer position one answer, wherever the drag has been', () => {
    // Down and back up again. Where the tile ends up must depend on where the
    // pointer is and not on how it got there, or a person aiming at two rows
    // gets two rows or three depending on which way they came.
    const there = drag([200, 280, 360, 280, 200], 1)
    expect(there[0]).toBe(there[4])
    expect(there[1]).toBe(there[3])
  })

  it('stays inside what the server allows', () => {
    expect(drag([4000], 1)).toEqual([MAX_ROWS])
    expect(drag([-4000], 3)).toEqual([1])
  })
})

/** Enough of an element for `closest`: the inspector's controls are the
 *  targets, and `closest` matches the element itself first. */
const elementLike = (tag: string) => ({
  closest: (sel: string) => (sel.includes(tag) ? { tag } : null),
})

describe('a key typed into the inspector', () => {
  it('belongs to the control, not to the board', () => {
    expect(fromFormControl(elementLike('input'))).toBe(true)
    expect(fromFormControl(elementLike('select'))).toBe(true)
    expect(fromFormControl(elementLike('textarea'))).toBe(true)
    expect(fromFormControl(elementLike('contenteditable'))).toBe(true)
  })

  it('leaves the canvas its own keys', () => {
    expect(fromFormControl(elementLike('section'))).toBe(false)
    expect(fromFormControl(null)).toBe(false)
  })

  // The guard itself is one line inside a React handler, and a handler needs a
  // browser. What can be checked here is that it is still the first thing the
  // handler does: below the first `preventDefault` it is too late, because the
  // select has already lost the arrow key that was meant for it.
  it('is refused before the board claims anything', () => {
    const src = readFileSync(new URL('./BoardEditor.tsx', import.meta.url), 'utf8')
    const body = src.slice(src.indexOf('const onKeyDown ='), src.indexOf('const onPreset ='))
    expect(body).not.toBe('')
    expect(body).toContain('fromFormControl')
    expect(body.indexOf('fromFormControl')).toBeLessThan(body.indexOf('preventDefault'))
  })
})
