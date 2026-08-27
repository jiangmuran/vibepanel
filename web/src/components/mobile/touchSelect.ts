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
 * How many whole rows a drag of `dy` pixels covers, carrying the remainder.
 *
 * Carried, because a row is around eighteen pixels and a finger moves in ones
 * and twos: truncating each event on its own throws most of the movement away
 * and the terminal crawls behind the finger.
 */
export function dragRows(dy: number, rowHeight: number, carried: number) {
  if (!(rowHeight > 0)) return { rows: 0, carry: carried }
  const total = carried + dy / rowHeight
  const rows = Math.trunc(total)
  return { rows, carry: total - rows }
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
  let lastY = 0
  let carried = 0
  let scrolling = false

  // The screen, not the rows.
  //
  // `.xterm-rows` is the DOM renderer's grid of spans, and under the GPU
  // renderer -- which is what ships -- it is an empty element with no height at
  // all. Every measurement here divides by that height: the drag scrolled zero
  // rows and the selection could not find a cell, so on a phone the terminal
  // stopped scrolling and stopped being selectable the moment the renderer was
  // loaded. `.xterm-screen` is the element both renderers draw into and is the
  // same box.
  const rowsBox = () => {
    const screen = host.querySelector('.xterm-screen')
    return screen ? screen.getBoundingClientRect() : null
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
    lastY = t.clientY
    carried = 0
    scrolling = false
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
      if (!startPoint) return

      // Dragging is how a phone scrolls, and until this existed it did
      // nothing at all: xterm's scrollable element listens for wheel events,
      // which a touchscreen never sends. Measured with 269 lines of scrollback
      // present — wheel 269 → 268, drag 268 → 268, and the panel's own pgup
      // key 268 → 268, because it sends ESC[5~ and a shell ignores it. The
      // history was there and no gesture on a phone could reach it.
      const dx = Math.abs(t.clientX - startPoint.x)
      const dy = Math.abs(t.clientY - startPoint.y)
      if (!scrolling) {
        // Take the gesture only once it is clearly vertical and there is
        // something behind the screen to show. Claiming every drag would eat
        // the horizontal swipe that changes view, and preventing the default
        // on a terminal with no scrollback stops the page moving for nothing.
        if (dy <= SLOP_PX || dy <= dx || term.buffer.active.baseY === 0) return
        scrolling = true
      }

      const box = rowsBox()
      const rowHeight = box && term.rows > 0 ? box.height / term.rows : 0
      const step = dragRows(t.clientY - lastY, rowHeight, carried)
      carried = step.carry
      lastY = t.clientY
      if (step.rows !== 0) {
        // Down reveals what came before, which is which way every list on a
        // phone moves.
        term.scrollLines(-step.rows)
      }
      // Non-passive for this: without it the page scrolls under the finger as
      // well and the whole shell slides around.
      e.preventDefault()
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
    scrolling = false
    carried = 0
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
