/**
 * A span of one number, as a line.
 *
 * Drawn as an SVG path rather than through a chart library, because it is a
 * few dozen points in a box a few pixels tall and a dependency for that is a
 * dependency to keep updated forever.
 *
 * Fixed to 0-100 rather than scaled to what has been seen. A CPU that has sat
 * between 3% and 5% would otherwise draw a dramatic mountain range, which is a
 * chart that lies about the thing it is for -- the question is whether the
 * machine is under pressure, and a flat line near the floor is the correct
 * answer to it. A series with no natural ceiling, like network throughput,
 * scales itself against its own recent peak before it ever reaches here; this
 * component always draws what it is given as 0-100.
 *
 * One reading is a dot and no reading is nothing: a single point makes no line,
 * and drawing a flat one across the whole width would claim history that does
 * not exist yet.
 *
 * Shared by the strip (56×16, the corner of the eye) and the full monitor
 * (bigger, once there is a whole panel to spend on it) rather than kept as two
 * copies of the same path math at two sizes.
 */
export function Trend({
  values,
  tone,
  width = 56,
  height = 16,
}: {
  values: number[]
  tone: string
  width?: number
  height?: number
}) {
  if (values.length < 2) {
    return <span className="min-w-0 flex-1" style={{ height }} aria-hidden />
  }
  // Stretched to the width rather than plotted against a fixed axis. Both were
  // drawn and looked at: growing in from the left is more honest about how
  // much history there is, and for the first stretch after every page load it
  // is a few short marks in the corner of the eye -- which is worse at the
  // only job this has. Nobody reads the x axis of a line this short; they read
  // whether it is climbing.
  const step = width / (values.length - 1)
  const y = (v: number) => height - (Math.min(100, Math.max(0, v)) / 100) * (height - 1) - 0.5
  const line = values.map((v, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(1)},${y(v).toFixed(1)}`).join(' ')
  return (
    <svg
      className="min-w-0 flex-1"
      style={{ height }}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      aria-hidden
      focusable="false"
    >
      {/* The area under it, faint, so a low line still reads as a line rather
          than as a stray rule across an empty box. */}
      <path d={`${line} L${width},${height} L0,${height} Z`} fill={tone} opacity="0.15" />
      <path d={line} fill="none" stroke={tone} strokeWidth="1" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

/**
 * How many readings a history buffer holds for a two-minute-ish span at a
 * given sampling interval, shared so the strip and the full monitor agree on
 * what "recent" means even though they poll at different rates.
 */
export function historyLength(sampleMs: number): number {
  return Math.round(120_000 / sampleMs)
}
