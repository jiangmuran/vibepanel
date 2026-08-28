/**
 * A small filled trend line, drawn from numbers already in hand.
 *
 * Shared because there is one of this shape in the product and it was about to
 * become two: the share board's charts had it, and the panel's own spend block
 * needed one. Two copies of a chart drift the way charts drift -- one gains a
 * baseline, the other a different stroke width -- and then the panel and the
 * wall showing the same series stop looking like the same product.
 *
 * No chart library, no fetch and no URL below this line, which is the property
 * the board already had and the reason its charts can render inside a share
 * page at all.
 */

/** How these are drawn: a unit box, stretched by CSS. */
const VIEW = { w: 100, h: 100 }

/**
 * A polyline through a series, in the unit box.
 *
 * `max` is passed rather than derived so that two charts drawn side by side can
 * share a scale when they should and not when they should not. A series shorter
 * than two points has no line -- one reading is a dot, and a dot drawn as a flat
 * line across a chart reads as "nothing is happening" rather than "nothing has
 * been measured yet".
 */
function sparkPoints(values: number[], max: number): string {
  if (values.length < 2) return ''
  const top = max > 0 ? max : 1
  const step = VIEW.w / (values.length - 1)
  return values
    .map((v, i) => `${(i * step).toFixed(2)},${(VIEW.h - (v / top) * VIEW.h).toFixed(2)}`)
    .join(' ')
}

/**
 * A filled area under a line.
 *
 * `vector-effect: non-scaling-stroke` on the stroke, because the box is
 * stretched to its container with `preserveAspectRatio="none"` -- without it a
 * chart in a wide box has a hairline top edge and a fat one in a narrow box,
 * from the same markup.
 *
 * Returns null rather than an empty box when there is no line, so each caller
 * decides what "not enough readings yet" looks like where it sits.
 */
export function Spark({
  values,
  max,
  tone,
  testid,
}: {
  values: number[]
  max: number
  tone: string
  testid: string
}) {
  const line = sparkPoints(values, max)
  if (!line) return null
  return (
    <svg
      viewBox={`0 0 ${VIEW.w} ${VIEW.h}`}
      preserveAspectRatio="none"
      className="h-full w-full"
      role="img"
      data-testid={testid}
      data-points={values.length}
    >
      <polygon points={`0,${VIEW.h} ${line} ${VIEW.w},${VIEW.h}`} fill={tone} opacity="0.18" />
      <polyline
        points={line}
        fill="none"
        stroke={tone}
        strokeWidth="2"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  )
}
