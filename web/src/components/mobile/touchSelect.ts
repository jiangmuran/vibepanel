import type { Terminal } from '@xterm/xterm'

/**
 * Selecting terminal text with a finger.
 *
 * The browser's own long-press selection cannot be relied on here: xterm sets
 * `user-select: none` on the terminal and handles pointer input itself, and
 * what a long press does over such an element differs between Android Chrome
 * and iOS Safari. It also cannot be tested — headless Chromium performs no
 * native touch text selection at all, so a check driving real touch events
 * would prove nothing either way.
 *
 * So the gesture is ours: press and hold to start, drag to extend, lift to
 * keep. It runs on ordinary touch events against xterm's own selection API,
 * which means it behaves the same on every phone and can be driven by a test.
 */

/** Movement that turns a press into a scroll rather than a selection. */
const SLOP_PX = 10
/** How long a finger must stay put. Long enough not to fire while scrolling. */
export const HOLD_MS = 450

export interface Cell {
  col: number
  row: number
}

/**
 * The run of cells between two points, in reading order, as xterm wants it:
 * a start cell and a length that may wrap across lines.
 *
 * Returned start is whichever cell comes first on screen, so dragging up or
 * left selects the same text as dragging down or right.
 */
export function selectionRun(anchor: Cell, focus: Cell, cols: number) {
  const a = anchor.row * cols + anchor.col
  const b = focus.row * cols + focus.col
  const [from, to] = a <= b ? [a, b] : [b, a]
  return {
    col: from % cols,
    row: Math.floor(from / cols),
    length: to - from + 1,
  }
}

/** Which cell a point on screen falls in, given the rows element's box. */
export function cellAt(
  point: { x: number; y: number },
  box: { left: number; top: number; width: number; height: number },
  cols: number,
  rows: number,
): Cell {
  // Divide the measured box rather than asking xterm for its cell size: in a
  // passive viewer the terminal is CSS-scaled, and the box is already in the
  // scaled coordinates the touch arrives in.
  const cw = box.width / cols
  const ch = box.height / rows
  const clamp = (v: number, hi: number) => Math.min(Math.max(v, 0), hi)
  return {
    col: clamp(Math.floor((point.x - box.left) / cw), cols - 1),
    row: clamp(Math.floor((point.y - box.top) / ch), rows - 1),
  }
}

/**
 * Attaches the gesture to a terminal. Returns a function that removes it.
 *
 * `host` is the element the terminal was opened into; the rows element inside
 * it is what gets measured, because that is the box the character grid
 * actually occupies.
 */
export function attachTouchSelection(host: HTMLElement, term: Terminal): () => void {
  let holdTimer: number | undefined
  let anchor: Cell | null = null
  let selecting = false
  let startPoint: { x: number; y: number } | null = null

  const rowsBox = () => {
    const rows = host.querySelector('.xterm-rows')
    return rows ? rows.getBoundingClientRect() : null
  }

  const cellFor = (touch: { clientX: number; clientY: number }): Cell | null => {
    const box = rowsBox()
    if (!box || box.width === 0 || box.height === 0) return null
    return cellAt({ x: touch.clientX, y: touch.clientY }, box, term.cols, term.rows)
  }

  const apply = (focus: Cell) => {
    if (!anchor) return
    const run = selectionRun(anchor, focus, term.cols)
    // Rows are viewport-relative; the selection API wants a buffer line, and
    // the two differ by however far the user has scrolled back.
    term.select(run.col, run.row + term.buffer.active.viewportY, run.length)
  }

  const cancelHold = () => {
    if (holdTimer !== undefined) {
      clearTimeout(holdTimer)
      holdTimer = undefined
    }
  }

  const onStart = (e: TouchEvent) => {
    if (e.touches.length !== 1) return
    const t = e.touches[0]
    startPoint = { x: t.clientX, y: t.clientY }
    // A tap outside an existing selection dismisses it, the way tapping away
    // from selected text does everywhere else.
    if (!selecting && term.hasSelection()) term.clearSelection()
    selecting = false
    cancelHold()
    holdTimer = window.setTimeout(() => {
      const cell = cellFor(t)
      if (!cell) return
      selecting = true
      anchor = cell
      term.clearSelection()
      apply(cell)
      // The only feedback a finger gets that the mode changed; without it the
      // first drag looks like a failed scroll.
      navigator.vibrate?.(8)
    }, HOLD_MS)
  }

  const onMove = (e: TouchEvent) => {
    const t = e.touches[0]
    if (!t) return
    if (!selecting) {
      // Still deciding. Enough movement means the finger is scrolling, and a
      // selection that starts mid-scroll is worse than none.
      if (
        startPoint &&
        Math.hypot(t.clientX - startPoint.x, t.clientY - startPoint.y) > SLOP_PX
      ) {
        cancelHold()
      }
      return
    }
    const cell = cellFor(t)
    if (!cell) return
    // Non-passive, so this can stop the page scrolling under the drag. The
    // listener is registered with {passive: false} for exactly this line.
    e.preventDefault()
    apply(cell)
  }

  const onEnd = () => {
    cancelHold()
    startPoint = null
    // selecting stays true until the next touch so that lifting the finger
    // leaves the selection on screen with the copy bar over it.
  }

  host.addEventListener('touchstart', onStart, { passive: true })
  host.addEventListener('touchmove', onMove, { passive: false })
  host.addEventListener('touchend', onEnd, { passive: true })
  host.addEventListener('touchcancel', onEnd, { passive: true })

  return () => {
    cancelHold()
    host.removeEventListener('touchstart', onStart)
    host.removeEventListener('touchmove', onMove)
    host.removeEventListener('touchend', onEnd)
    host.removeEventListener('touchcancel', onEnd)
  }
}
