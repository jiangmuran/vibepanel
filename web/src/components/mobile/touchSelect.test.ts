import { describe, expect, it } from 'vitest'
import { cellAt, selectionRun } from './touchSelect'

describe('selectionRun', () => {
  it('reads left to right on one line', () => {
    expect(selectionRun({ col: 2, row: 0 }, { col: 6, row: 0 }, 80)).toEqual({
      col: 2,
      row: 0,
      length: 5,
    })
  })

  it('gives the same run when dragged backwards', () => {
    const forward = selectionRun({ col: 2, row: 1 }, { col: 6, row: 3 }, 80)
    const backward = selectionRun({ col: 6, row: 3 }, { col: 2, row: 1 }, 80)
    expect(backward).toEqual(forward)
  })

  it('wraps across lines by counting cells, not characters', () => {
    // Two full lines and one cell: dragging down a column must not select a
    // rectangle, which is what a naive implementation does.
    expect(selectionRun({ col: 0, row: 0 }, { col: 0, row: 2 }, 10)).toEqual({
      col: 0,
      row: 0,
      length: 21,
    })
  })
})

describe('cellAt', () => {
  const box = { left: 100, top: 50, width: 800, height: 400 }

  it('maps a point to the cell under it', () => {
    // 80 cols over 800px is 10px a cell; 20 rows over 400px is 20px a row.
    expect(cellAt({ x: 105, y: 55 }, box, 80, 20)).toEqual({ col: 0, row: 0 })
    expect(cellAt({ x: 145, y: 95 }, box, 80, 20)).toEqual({ col: 4, row: 2 })
  })

  it('clamps a finger that leaves the terminal', () => {
    // Dragging off the edge is how people select to the end of a line, so it
    // has to land on the last cell rather than an index past the buffer.
    expect(cellAt({ x: 5000, y: 5000 }, box, 80, 20)).toEqual({ col: 79, row: 19 })
    expect(cellAt({ x: -50, y: -50 }, box, 80, 20)).toEqual({ col: 0, row: 0 })
  })
})
