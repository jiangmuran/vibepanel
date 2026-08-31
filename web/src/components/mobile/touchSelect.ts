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
/**
 * Whether a drag has become a scroll.
 *
 * Deliberately given no way to ask whether there is anything to scroll. That
 * used to be part of the same condition -- `&& term.buffer.active.baseY !== 0`
 * -- and it meant a full-screen agent, which has no scrollback because the
 * alternate screen is one screen, handed the gesture back to the browser,
 * which pulled to refresh over the top of it. Codex kept its output in the
 * normal buffer and scrolled fine, so the report arrived as "Claude cannot be
 * scrolled but Codex can", which is one line of code seen from outside.
 *
 * Claiming and doing nothing is the correct behaviour over a terminal with no
 * history: the page must not move under a finger that is trying to read.
 */
export function claimsVerticalDrag(dx: number, dy: number, slop: number): boolean {
  return dy > slop && dy > dx
}

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
/**
 * Wheel events, as the application asked to receive them.
 *
 * A pane with mouse reporting on wants the wheel itself: that is how a desktop
 * scrolls Claude Code, and it is why the same session reads normally there and
 * wrongly on a phone. Dragging on a phone called `term.scrollLines`, which
 * scrolls xterm's *own* buffer behind the application's back -- so a
 * full-screen agent stayed put while the terminal underneath it slid up to
 * whatever was in the normal buffer before the agent started. Raw output from
 * hours earlier, which is what「向上滑动会出现一大堆HTML」was.
 *
 * Measured with tmux rather than guessed at:
 *
 *	claude  alt=1 sgr=1 any=1    <- reporting on
 *	codex   alt=0 sgr=0 any=0    <- off
 *	bash    alt=0 sgr=0 any=0    <- off
 *
 * which is also why it was only ever reported about Claude.
 *
 * SGR encoding only. The panel does not implement the older X10 and UTF-8
 * schemes because it does not have to guess: `mouseTrackingMode` says whether
 * reporting is on at all, and every application that asks for it in this
 * decade asks for 1006 alongside. If one does not, the drag falls through to
 * scrolling the buffer, which is what it did before and is not worse.
 */
/**
 * Who a drag belongs to.
 *
 * `wheel` — the application asked for mouse reporting, so it wants the wheel
 * and scrolls its own view with it. That is what a desktop already does, and
 * doing anything else on a phone is why the same session read normally there
 * and wrongly here.
 *
 * `buffer` — nobody is listening, so this is the terminal's own scrollback.
 *
 * `none` — nothing to scroll and nobody to tell. The gesture is still claimed
 * by the caller, because a drag over a full-screen agent has to do nothing
 * visibly rather than let the browser reload the page.
 *
 * A function of two values so it can be tested: the branch it replaces could
 * be deleted without failing anything, which is how the wrong half shipped.
 */
export function scrollAction(mouseTracking: string, baseY: number): 'wheel' | 'buffer' | 'none' {
  if (mouseTracking !== 'none') return 'wheel'
  return baseY > 0 ? 'buffer' : 'none'
}

export function wheelReport(up: boolean, col: number, row: number): string {
  // 64 is wheel-up, 65 wheel-down; both are press events with no release.
  // Columns and rows are 1-based on the wire.
  return `\x1b[<${up ? 64 : 65};${col + 1};${row + 1}M`
}

export function attachTouchSelection(
  host: HTMLElement,
  term: Terminal,
  send?: (data: string) => void,
): () => void {
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
        // Clearly vertical, and that is the whole condition. Not "and there is
        // scrollback", which is what it used to say.
        //
        // A full-screen TUI has no scrollback -- the alternate screen is one
        // screen -- so `baseY === 0` while Claude Code is drawing, and the
        // gesture was handed back to the browser, which pulled to refresh over
        // the top of it. Codex leaves its output in the normal buffer, so it
        // had scrollback, so it scrolled: 「手机端还是无法滑动Claude code但是
        // codex正常 Claude滑动后会出现刷新的加载条」, one report describing both
        // halves of the same line.
        //
        // The horizontal swipe is still safe: `dy > dx` is what protects it,
        // and that was never the part doing the work here.
        if (!claimsVerticalDrag(dx, dy, SLOP_PX)) return
        scrolling = true
      }

      const box = rowsBox()
      const rowHeight = box && term.rows > 0 ? box.height / term.rows : 0
      const step = dragRows(t.clientY - lastY, rowHeight, carried)
      carried = step.carry
      lastY = t.clientY
      if (step.rows !== 0) {
        const action = scrollAction(term.modes.mouseTrackingMode, term.buffer.active.baseY)
        if (action === 'wheel' && send) {
          // The application's scroll, not the terminal's. Down reveals what
          // came before, which is wheel-up, which is which way every list on a
          // phone moves.
          const up = step.rows > 0
          const n = Math.min(Math.abs(step.rows), 10)
          const cell = cellFor(t)
          for (let i = 0; i < n; i++) {
            send(wheelReport(up, cell?.col ?? 0, cell?.row ?? 0))
          }
        } else if (action === 'buffer') {
          // Nobody is listening for the wheel, so this is the terminal's own
          // scrollback. Only when there is somewhere to go: the gesture is
          // claimed either way, because a drag over a full-screen agent has to
          // do nothing visibly rather than let the browser reload the page.
          term.scrollLines(-step.rows)
        }
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
