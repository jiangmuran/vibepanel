/**
 * The wireframe one widget kind is drawn as when it is not being drawn for
 * real.
 *
 * Two callers, and they are the two ends of the same gesture: the palette
 * entry you press, and — until the first preview lands — the tile on the
 * canvas you dropped it onto. One picture for both, so what you are moving
 * looks like what you picked up.
 *
 * **Schematic, not live.** A palette of thirty-seven live widgets is
 * thirty-seven renders of the whole payload on every poll, and a gallery of
 * two dozen live boards is worse. What a picture has to carry here is *shape*:
 * how wide the thing is by default, and whether it is a figure, a chart, a
 * list or a rule. That is exactly what a wireframe says and it costs nothing.
 * The real thing is one drag away on the canvas, drawn from the real payload.
 *
 * Its own file rather than an export out of BoardPalette, because the canvas
 * reaching into the palette to borrow a helper reads as a dependency between
 * two halves of the editor that do not have one.
 */

/**
 * What a kind's picture looks like, by what it says rather than by what it
 * reads.
 *
 * A `switch` with a default, like the renderer's, and for the same reason: the
 * catalogue is served by the server, so a build of this frontend can be handed
 * a kind it has never heard of. It gets the neutral picture rather than none,
 * because a palette entry with no picture reads as broken.
 */
type Shape = 'figure' | 'chart' | 'series' | 'list' | 'grid' | 'meter' | 'rule' | 'text'

function shapeOf(kind: string): Shape {
  switch (kind) {
    case 'bignumber':
    case 'attention':
    case 'output':
    case 'odometer':
    case 'spendtotals':
    case 'spendrate':
    case 'spendcompare':
    case 'kinds':
    case 'prs':
    case 'states':
    case 'clock':
    case 'datetime':
    case 'uptime':
    case 'health':
      return 'figure'
    case 'machinearea':
    case 'tokenburn':
    case 'sparkline':
      return 'chart'
    case 'spendbars':
    case 'spendstack':
    case 'codechurn':
    case 'spentmade':
    case 'flow':
    case 'waits':
      return 'series'
    case 'sessionlist':
    case 'cputop':
    case 'exits':
    case 'busiest':
    case 'todos':
    case 'projects':
    case 'timeline':
    case 'spendsplit':
    case 'repoprojects':
    case 'feed':
      return 'list'
    case 'sessiongrid':
    case 'spendheatmap':
      return 'grid'
    case 'machine':
    case 'gauge':
    case 'statebar':
    case 'nowstrip':
      return 'meter'
    case 'rule':
    case 'spacer':
      return 'rule'
    case 'caption':
    case 'heading':
    case 'remark':
      return 'text'
    default:
      return 'figure'
  }
}

/**
 * The wireframe for one kind, in a unit box the caller stretches.
 *
 * `className` because the two callers want different boxes: a palette entry is
 * a 28px strip above a name, and a ghost tile on the canvas is the whole
 * height of a wall tile. `preserveAspectRatio="none"` is what makes one
 * drawing serve both.
 */
export function Sketch({ kind, className = 'h-7 w-full' }: { kind: string; className?: string }) {
  const shape = shapeOf(kind)
  const ink = 'var(--vp-ink-3)'
  const accent = 'var(--vp-accent)'
  return (
    <svg
      viewBox="0 0 60 28"
      preserveAspectRatio="none"
      className={className}
      aria-hidden="true"
      data-testid="palette-sketch"
      data-shape={shape}
    >
      {shape === 'figure' && (
        <>
          <rect x="4" y="5" width="26" height="12" rx="2" fill={accent} opacity="0.7" />
          <rect x="4" y="20" width="18" height="3" rx="1.5" fill={ink} opacity="0.5" />
        </>
      )}
      {shape === 'chart' && (
        <polyline
          points="4,22 14,14 24,17 34,8 44,12 56,5"
          fill="none"
          stroke={accent}
          strokeWidth="2.5"
          strokeLinejoin="round"
        />
      )}
      {shape === 'series' &&
        [6, 14, 22, 30, 38, 46].map((x, i) => (
          <rect
            key={x}
            x={x}
            y={24 - [8, 14, 6, 18, 11, 20][i]}
            width="6"
            height={[8, 14, 6, 18, 11, 20][i]}
            rx="1"
            fill={accent}
            opacity="0.75"
          />
        ))}
      {shape === 'list' &&
        [4, 11, 18].map((y, i) => (
          <g key={y}>
            <rect x="4" y={y} width="16" height="4" rx="2" fill={ink} opacity="0.5" />
            <rect
              x="24"
              y={y}
              width={[30, 20, 12][i]}
              height="4"
              rx="2"
              fill={accent}
              opacity="0.7"
            />
          </g>
        ))}
      {shape === 'grid' &&
        [0, 1, 2, 3].flatMap((c) =>
          [0, 1].map((r) => (
            <rect
              key={`${c}-${r}`}
              x={4 + c * 14}
              y={5 + r * 11}
              width="11"
              height="8"
              rx="1.5"
              fill={accent}
              opacity={0.35 + ((c + r) % 3) * 0.2}
            />
          )),
        )}
      {shape === 'meter' && (
        <>
          <rect x="4" y="8" width="52" height="5" rx="2.5" fill={ink} opacity="0.35" />
          <rect x="4" y="8" width="34" height="5" rx="2.5" fill={accent} />
          <rect x="4" y="18" width="52" height="5" rx="2.5" fill={ink} opacity="0.35" />
          <rect x="4" y="18" width="18" height="5" rx="2.5" fill={accent} opacity="0.7" />
        </>
      )}
      {shape === 'rule' && <rect x="4" y="13" width="52" height="2" rx="1" fill={ink} opacity="0.5" />}
      {shape === 'text' && (
        <>
          <rect x="4" y="7" width="40" height="5" rx="2.5" fill={ink} opacity="0.6" />
          <rect x="4" y="16" width="26" height="4" rx="2" fill={ink} opacity="0.35" />
        </>
      )}
    </svg>
  )
}
