import { CircleCheck, OctagonAlert, TriangleAlert } from 'lucide-react'

import type { ResourcesView } from '../../protocol/wire'
import { pct } from './tone'

type Level = ResourcesView['level']

/**
 * How a memory level looks, in one place for the question across the console
 * and the resources page. A shape per level as well as a colour (red line 4).
 */
export function LevelIcon({ level, size }: { level: Level; size: number }) {
  if (level === 'critical') return <OctagonAlert size={size} className="shrink-0" aria-hidden="true" />
  if (level === 'warn') return <TriangleAlert size={size} className="shrink-0" aria-hidden="true" />
  return <CircleCheck size={size} className="shrink-0" aria-hidden="true" />
}


/**
 * Held memory and cache as one bar: held solid in the tone, cache faint and
 * neutral, because cache is what the kernel can take back and is never the
 * alarming part.
 */
export function HeldBar({
  held,
  current,
  scale,
  tone,
  height = 'h-2',
  children,
}: {
  held: number
  current: number
  scale: number
  tone: string
  height?: string
  children?: React.ReactNode
}) {
  const heldPct = pct(held, scale)
  const cachePct = Math.max(0, pct(current, scale) - heldPct)
  return (
    <div className={`vp-bar relative ${height}`}>
      <span className="absolute inset-y-0 left-0" style={{ width: `${heldPct}%`, background: tone }} />
      <span
        className="absolute inset-y-0 bg-ink-3"
        style={{ left: `${heldPct}%`, width: `${cachePct}%`, opacity: 0.35 }}
      />
      {children}
    </div>
  )
}
