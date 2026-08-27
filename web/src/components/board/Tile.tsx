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
export function Tile({
  label,
  span,
  children,
  testid,
  kind,
  plain,
}: {
  label?: string
  span: number
  children: ReactNode
  testid: string
  kind: string
  /** No surface and no border: for a widget that is one huge figure, where a
   *  box around it only makes the figure smaller. */
  plain?: boolean
}) {
  return (
    <section
      data-testid={testid}
      data-widget={kind}
      className={
        plain
          ? 'flex min-w-0 flex-col justify-center'
          : 'flex min-w-0 flex-col rounded-vp-lg border border-hairline bg-surface p-5'
      }
      style={{ gridColumn: `span ${span}` }}
    >
      {label && (
        <h2 className="mb-3 truncate text-vp-xl font-medium text-ink-3" data-testid="tile-label">
          {label}
        </h2>
      )}
      <div className="min-w-0 flex-1">{children}</div>
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
