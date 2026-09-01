import type { ReactNode } from 'react'

/**
 * The frame every widget sits in.
 *
 * One frame rather than each widget drawing its own, because the thing that
 * makes a wall of twenty numbers readable is that they line up. A tile is a
 * surface, a hairline, a heading and the content — and the heading is optional,
 * because the widgets that exist to be read from three metres (a single number,
 * a clock) are worse with a caption above them.
 */
/**
 * How many grid rows a tile takes.
 *
 * Clamped here as well as on the server. The server refuses a bad height on the
 * way in and drops the widget on the way out; this is the third place the same
 * rule is applied, because it is the one running on somebody else's machine and
 * `grid-row: span NaN` is a tile that swallows the whole board.
 *
 * Exported so the clamp has a test rather than only a comment.
 */
export function rowSpan(height?: number): number {
  if (!Number.isFinite(height) || (height ?? 0) <= 1) return 1
  return Math.min(Math.floor(height!), MAX_ROWS)
}

/** The server's own bound, restated. store.MaxRows. */
const MAX_ROWS = 4

export function Tile({
  label,
  span,
  height,
  children,
  testid,
  kind,
  plain,
}: {
  label?: string
  span: number
  /** How many grid rows tall, or absent for one.
   *
   *  Hierarchy on a wall is a size ratio, not a colour: the hero has to be
   *  several times the area of the texture around it, and a grid of equal
   *  tiles is a dashboard however carefully it is coloured. Height is the half
   *  of that ratio a span cannot express. */
  height?: number
  children: ReactNode
  testid: string
  kind: string
  /** No surface and no border: for a widget that is one huge figure, where a
   *  box around it only makes the figure smaller. */
  plain?: boolean
}) {
  const rows = rowSpan(height)
  return (
    <section
      data-testid={testid}
      data-widget={kind}
      data-height={rows}
      // `overflow-hidden` on the section as well as on the body below.
      //
      // The body's clip stops a widget escaping its own box; this stops a
      // *tile* escaping its grid cell, which is the same failure one level up.
      // On a fill board the row height is `minmax(0, 1fr)` and the section is
      // `container-type: size`, so a tile whose content wants more simply
      // painted over the tile beneath it -- and over its own heading, because
      // the body is centred and escapes from both ends at once.
      className={
        plain
          ? 'flex min-w-0 flex-col justify-center overflow-hidden'
          : 'flex min-w-0 flex-col overflow-hidden rounded-vp-lg border border-hairline bg-surface p-5'
      }
      style={{ gridColumn: `span ${span}`, gridRow: `span ${rows}` }}
    >
      {label && (
        <h2 className="mb-3 truncate text-vp-xl font-medium text-ink-3" data-testid="tile-label">
          {label}
        </h2>
      )}
      {/* Centred in the height it was given, not stacked at the top of it.
       *
       * A wall tile is as tall as its row, and a row on a television is
       * several hundred pixels. Three figures drawn at the top of one leave
       * three quarters of a card empty, and every card doing it makes a board
       * that is 90% full of grid read as a board that is 90% empty -- which is
       * what it looked like, and what was reported.
       *
       * Children that want the whole box still get it: a chart inside here is
       * `h-full`, and `min-h-0` keeps a list that overflows scrollable rather
       * than pushing the row past the viewport. */}
      {/* `overflow-hidden`, and it is the one line holding the grid together.
        *
        * `min-h-0` lets a child *scroll* if the child asked to; it does not
        * stop one that did not. On a fill board the section is
        * `container-type: size`, so its height is definite and anything taller
        * simply painted outside it -- and because this box is `justify-center`
        * it escaped from both ends at once, over the tile's own heading above
        * and over the tile below. Three widgets at a time were illegible on
        * the denser boards, and it read as tiles drawn on top of each other
        * rather than as one widget being too tall.
        *
        * A widget that legitimately has more rows than fit is the list's
        * problem to solve, and the lists scroll. */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col justify-center overflow-hidden">
        {children}
      </div>
    </section>
  )
}

/**
 * A horizontal bar with its own figure beside it.
 *
 * The figure is not optional, and that is red line 4 in its plainest form: a
 * bar carries a proportion by length and by hue, and neither survives a
 * photograph of a screen taken at an angle. The number does.
 */
export function Bar({
  label,
  value,
  fraction,
  tone,
  testid,
}: {
  label: ReactNode
  value: string
  fraction: number
  tone: string
  testid?: string
}) {
  const pct = Math.max(0, Math.min(100, fraction * 100))
  return (
    <div className="mb-3 last:mb-0" data-testid={testid}>
      <div className="mb-1 flex items-baseline justify-between gap-3">
        <span className="min-w-0 truncate text-vp-xl text-ink">{label}</span>
        <span className="tabular shrink-0 text-vp-xl text-ink-2">{value}</span>
      </div>
      <div className="h-2 overflow-hidden rounded-full" style={{ background: 'var(--vp-surface-2)' }}>
        <div
          className="h-full rounded-full transition-[width] duration-500 ease-vp"
          style={{ width: `${pct}%`, background: tone }}
        />
      </div>
    </div>
  )
}

/** What a widget shows when the thing it draws is not there. Two words. */
export function Empty({ text }: { text: string }) {
  return (
    <p className="text-vp-xl text-ink-3" data-testid="widget-empty">
      {text}
    </p>
  )
}
