import { describe, expect, it } from 'vitest'
import { cellAt, claimsVerticalDrag, selectionRun, dragRows, scrollAction, wheelReport } from './touchSelect'

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

describe('dragRows', () => {
  it('carries the remainder instead of throwing it away', () => {
    // A row is around eighteen pixels and a finger moves in ones and twos.
    // Truncating each event on its own loses most of the movement, and the
    // terminal crawls a long way behind the finger.
    let carry = 0
    let scrolled = 0
    for (let i = 0; i < 18; i++) {
      const step = dragRows(2, 18, carry)
      carry = step.carry
      scrolled += step.rows
    }
    expect(scrolled).toBe(2)
  })

  it('has a sign: down covers rows the same way up does', () => {
    expect(dragRows(36, 18, 0).rows).toBe(2)
    expect(dragRows(-36, 18, 0).rows).toBe(-2)
  })

  it('survives a terminal that has not been measured yet', () => {
    // rowsBox() returns null before the terminal is laid out, and a division
    // by zero here would scroll the buffer to one end and stay there.
    expect(dragRows(100, 0, 0)).toEqual({ rows: 0, carry: 0 })
    expect(dragRows(100, Number.NaN, 0.5)).toEqual({ rows: 0, carry: 0.5 })
  })
})

describe('claimsVerticalDrag', () => {
  // The bug this exists to prevent: the condition also asked whether there was
  // scrollback, so over a full-screen agent the gesture went to the browser and
  // the browser reloaded the page. The function is not given the buffer, so it
  // cannot ask again.
  it('claims a clearly vertical drag', () => {
    expect(claimsVerticalDrag(2, 40, 8)).toBe(true)
  })

  it('leaves a horizontal drag alone, which is how views are switched', () => {
    expect(claimsVerticalDrag(60, 20, 8)).toBe(false)
    // Equal is not vertical: a diagonal belongs to whoever wants it more.
    expect(claimsVerticalDrag(30, 30, 8)).toBe(false)
  })

  it('leaves a tap alone', () => {
    expect(claimsVerticalDrag(0, 3, 8)).toBe(false)
    expect(claimsVerticalDrag(0, 8, 8)).toBe(false)
  })

  it('takes no argument that could describe the scrollback', () => {
    // The guard is the signature. If somebody adds a third meaning to this,
    // the arity changes and this fails -- which is the point, because the
    // failure it prevents is invisible: a gesture that quietly does nothing
    // until the page reloads.
    expect(claimsVerticalDrag.length).toBe(3)
  })
})

describe('wheelReport', () => {
  // A pane with mouse reporting on wants the wheel itself. Dragging on a phone
  // used to scroll xterm's own buffer instead, behind the application's back,
  // so a full-screen agent stayed put while the terminal slid up to whatever
  // was in the normal buffer before it started -- raw output from hours
  // earlier. Measured with tmux, which is what settled it:
  //
  //   claude  alt=1 sgr=1 any=1
  //   codex   alt=0 sgr=0 any=0
  //
  // and that is why it was only ever reported about Claude.
  it('encodes a wheel press in SGR form', () => {
    // 64 is wheel-up, 65 wheel-down, and both are presses with no release.
    expect(wheelReport(true, 0, 0)).toBe('\x1b[<64;1;1M')
    expect(wheelReport(false, 0, 0)).toBe('\x1b[<65;1;1M')
  })

  it('sends one-based coordinates', () => {
    // Cells are zero-based in the panel and one-based on the wire. An
    // off-by-one here puts the pointer in the wrong cell, which for a TUI that
    // scrolls the pane under the cursor is the wrong pane.
    expect(wheelReport(true, 11, 4)).toBe('\x1b[<64;12;5M')
  })

  it('is a press, not a drag report', () => {
    // 32 added to the button is motion; a wheel event that claims to be motion
    // makes an application think the pointer is being dragged across it.
    for (const s of [wheelReport(true, 0, 0), wheelReport(false, 3, 3)]) {
      expect(s).not.toMatch(/<(9[6-9]|1\d\d);/)
    }
  })
})

describe('scrollAction', () => {
  it('gives the wheel to an application that asked for it', () => {
    // Claude Code turns on SGR mouse reporting; Codex does not. Measured with
    // tmux on a live panel, which is what settled a bug reported three
    // different ways:
    //
    //   claude  alt=1 sgr=1 any=1
    //   codex   alt=0 sgr=0 any=0
    //
    // With reporting on, scrolling xterm's buffer slides the terminal behind
    // the application to whatever was in the normal buffer before it started.
    for (const mode of ['x10', 'vt200', 'drag', 'any']) {
      expect(scrollAction(mode, 0)).toBe('wheel')
      // Even with scrollback available: it still belongs to the application.
      expect(scrollAction(mode, 500)).toBe('wheel')
    }
  })

  it('scrolls the buffer when nobody is listening', () => {
    expect(scrollAction('none', 500)).toBe('buffer')
  })

  it('does nothing when there is nothing to scroll and nobody to tell', () => {
    // The gesture is still claimed by the caller. A drag over a full-screen
    // agent with no scrollback must do nothing visibly rather than hand the
    // browser a pull-to-refresh.
    expect(scrollAction('none', 0)).toBe('none')
  })
})
