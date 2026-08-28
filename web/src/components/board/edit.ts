import type { ShareBoard, ShareWidget } from '../../protocol/wire'

/**
 * Rearranging a board, as arithmetic.
 *
 * Separate from the canvas that draws it for the reason panes.ts is separate
 * from the panel that draws it: the interesting failures here are off-by-ones
 * in a list — a widget dropped one slot from where it was aimed, a widget
 * dragged onto itself that ends up somewhere else — and they are a test each
 * when the arithmetic is a function and a browser check when it is inside a
 * pointer handler.
 *
 * A board is an ordered list flowed into a twelve-column grid with
 * `grid-auto-flow: dense`, so "where does this go" is one integer: an index.
 * There is no coordinate per widget per breakpoint, and this file is the reason
 * it stays that way — the whole placement vocabulary is order, span, height and
 * an explicit spacer.
 */

/** Pixels of movement before a press becomes a drag.
 *
 *  The same five the panel's tab drag uses. Two different drag feels in one
 *  product is the "assembled" complaint in another form. */
export const DRAG_THRESHOLD = 5

/**
 * Move the widget at `from` to the gap at `to`.
 *
 * `to` is a gap index, 0..length: gap 0 is before the first widget, gap
 * length is after the last. That is not the same as a widget index, and
 * conflating the two is what puts a widget dragged one step right back where it
 * started — removing it first shifts every gap after it down by one.
 *
 * Returns the same array when nothing moves, so a drag that went nowhere does
 * not rewrite the board and re-save it.
 */
export function moveWidget(widgets: ShareWidget[], from: number, to: number): ShareWidget[] {
  if (from < 0 || from >= widgets.length) return widgets
  if (to < 0 || to > widgets.length) return widgets
  const landing = to > from ? to - 1 : to
  if (landing === from) return widgets
  const next = [...widgets]
  const [row] = next.splice(from, 1)
  next.splice(landing, 0, row)
  return next
}

/** Put a new widget in the gap at `to`. */
export function insertWidget(widgets: ShareWidget[], w: ShareWidget, to: number): ShareWidget[] {
  const at = Math.max(0, Math.min(to, widgets.length))
  const next = [...widgets]
  next.splice(at, 0, w)
  return next
}

export function removeWidget(widgets: ShareWidget[], at: number): ShareWidget[] {
  if (at < 0 || at >= widgets.length) return widgets
  return widgets.filter((_, i) => i !== at)
}

/**
 * Which of the offered widths a drag of the right edge lands on.
 *
 * Snapped to the catalogue's steps rather than to twelfths, so dragging cannot
 * produce a width the editor's own select does not offer — 5/12 and 7/12 are
 * widths whose only use is stopping a row from lining up, and a drag that can
 * reach them makes the catalogue a lie.
 *
 * `steps` comes from the server. An empty list falls back to the full width,
 * which is the one span every kind accepts.
 */
export function snapSpan(fraction: number, steps: number[], maxSpan: number): number {
  if (steps.length === 0) return maxSpan
  const wanted = Math.max(0, Math.min(1, fraction)) * maxSpan
  let best = steps[0]
  for (const s of steps) {
    if (Math.abs(s - wanted) < Math.abs(best - wanted)) best = s
  }
  return best
}

/** How many rows tall a drag of the bottom edge lands on. */
export function snapHeight(pixels: number, rowPixels: number, maxRows: number): number {
  if (!Number.isFinite(rowPixels) || rowPixels <= 0) return 1
  const n = Math.round(pixels / rowPixels)
  return Math.max(1, Math.min(n, Math.max(1, maxRows)))
}

/** One widget's rectangle on the canvas, as the drop test reads them. */
export interface Slot {
  index: number
  left: number
  top: number
  right: number
  bottom: number
}

/**
 * Which gap a pointer at (x, y) is aiming at.
 *
 * Rows first, then the horizontal half of the widget under the pointer. The
 * naive version — nearest centre over every rectangle — puts a pointer in the
 * blank space to the right of a short last row onto whichever widget happens to
 * be geometrically closest, which on a wall board is usually one two rows up.
 *
 * Returns `widgets.length` for a pointer past the end, which is the gap after
 * the last widget and the answer a drop on empty canvas should give.
 */
export function gapAt(slots: Slot[], x: number, y: number): number {
  if (slots.length === 0) return 0
  // The row the pointer is in, or the last one it is below.
  let row: Slot[] = []
  let bestRow: Slot[] = []
  let rowTop = -Infinity
  const sorted = [...slots].sort((a, b) => a.top - b.top || a.left - b.left)
  for (const s of sorted) {
    if (s.top !== rowTop) {
      if (row.length > 0 && rowTop <= y) bestRow = row
      row = []
      rowTop = s.top
    }
    row.push(s)
  }
  if (row.length > 0 && rowTop <= y) bestRow = row
  if (bestRow.length === 0) bestRow = sorted.slice(0, 1)

  for (const s of bestRow) {
    if (x < (s.left + s.right) / 2) return s.index
    if (x <= s.right) return s.index + 1
  }
  // Past the right edge of the last widget in the row.
  return bestRow[bestRow.length - 1].index + 1
}

/**
 * A widget as the palette drops it: the kind's default span, and the first
 * value of every setting it takes.
 *
 * The first allowed value rather than none, because a one-number widget with no
 * number is refused by the server — and refusing on save something the editor
 * offered is the failure the whole catalogue mechanism exists to avoid.
 */
export function widgetFrom(spec: {
  kind: string
  span: number
  metrics: string[] | null
  bys: string[] | null
}): ShareWidget {
  const w: ShareWidget = { kind: spec.kind, span: spec.span }
  if (spec.metrics && spec.metrics.length > 0) w.metric = spec.metrics[0]
  if (spec.bys && spec.bys.length > 0) w.by = spec.bys[0]
  return w
}

/** The widgets of one page, with their indices in the whole board kept.
 *
 *  Kept rather than recomputed, because every edit addresses the board's list
 *  and the canvas only ever draws one page of it. An index into the page is the
 *  wrong index for every page but the first. */
export function pageWidgets(board: ShareBoard, page: number): { w: ShareWidget; index: number }[] {
  return board.widgets
    .map((w, index) => ({ w, index }))
    .filter((e) => (e.w.page ?? 0) === page)
}
