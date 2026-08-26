import { describe, expect, it } from 'vitest'

import { reorder } from './useDragList'

/**
 * The downward-drag arm of a classic off-by-one, which nothing took.
 *
 * `overIndex` counts positions in the *original* list -- it is where the gap
 * is, so it means "put this before the item currently at that index". Removing
 * the dragged row first shifts everything below it up by one, which is what
 * `overIndex > from ? overIndex - 1 : overIndex` corrects for. render-check
 * only ever drags the second project above the first, which takes the other
 * arm, so the branch carrying the correction was untested in a gesture people
 * use constantly, and its failure is silent: a project dropped one position
 * from where it was aimed reads as having aimed badly.
 */
describe('reorder', () => {
  const ids = ['a', 'b', 'c', 'd']

  it('drops a downward drag where it was aimed', () => {
    // Aimed at the gap between c and d.
    expect(reorder(ids, 'a', 3)).toEqual(['b', 'c', 'a', 'd'])
  })

  it('drops a downward drag at the end of the list', () => {
    // indexForY returns ids.length when the pointer is past every midpoint.
    expect(reorder(ids, 'a', 4)).toEqual(['b', 'c', 'd', 'a'])
  })

  it('drops an upward drag where it was aimed', () => {
    expect(reorder(ids, 'c', 1)).toEqual(['a', 'c', 'b', 'd'])
  })

  it('moves to the very top', () => {
    expect(reorder(ids, 'd', 0)).toEqual(['d', 'a', 'b', 'c'])
  })

  it('is a no-op when the row is dropped on itself', () => {
    expect(reorder(ids, 'b', 1)).toBeNull()
  })

  it('is a no-op when the row is dropped just below itself', () => {
    // The gap under b is index 2, which after the correction is b's own
    // position again. Committing that would make every viewer redraw for
    // nothing.
    expect(reorder(ids, 'b', 2)).toBeNull()
  })

  it('is a no-op for an id that is not in the list', () => {
    // The list can change under a drag: another viewer removes a project
    // while this one is holding it.
    expect(reorder(ids, 'gone', 2)).toBeNull()
  })

  it('never loses or duplicates a row, wherever it is dropped', () => {
    for (const id of ids) {
      for (let over = 0; over <= ids.length; over++) {
        const next = reorder(ids, id, over)
        if (!next) continue
        expect([...next].sort()).toEqual([...ids].sort())
      }
    }
  })
})
