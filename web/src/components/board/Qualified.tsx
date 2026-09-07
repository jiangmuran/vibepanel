/**
 * A line made of a subject and a qualifier, where the qualifier is what gives
 * way.
 *
 * Board tiles are full of `A · B`: "Code, over time · Lines changed",
 * "42,617,061,632 · 155,819 requests", "152K · per minute". Written as one
 * string with `truncate` on it, the browser cuts wherever the box ends — which
 * on a narrow tile means cutting the subject. `make board-check` found
 * twenty-six of them across the presets, and the ones that matter are the
 * numbers:
 *
 *     "42,617,061,632 · 155,819 Reque"   truncated by 245px
 *     "152K · per minute"                truncated by 69px
 *
 * A number cut in the middle is not a shortened number, it is a different one.
 * Nothing in the rendering says it was cut, so 42,617,061,632 reads as whatever
 * survived.
 *
 * So the two parts are two elements. The subject does not shrink and the
 * qualifier does: when the room runs out the qualifier is trimmed and then
 * disappears, and the subject is left whole. That is the right order for every
 * one of these, because the subject is the figure or the name and the qualifier
 * is what it is a figure of — a reader who loses "per minute" still has "152K",
 * and one who loses "152K" has nothing.
 *
 * The separator belongs to the qualifier so that it goes with it. A line ending
 * in a dangling "·" is worse than one that just stops.
 */
export function Qualified({
  subject,
  qualifier,
  className = '',
  testid,
}: {
  subject: React.ReactNode
  qualifier: React.ReactNode
  /** Classes for the line as a whole: the type size, the colour, the margin. */
  className?: string
  testid?: string
}) {
  return (
    <span className={`flex min-w-0 items-baseline ${className}`} data-testid={testid}>
      {/* `shrink-0` and no truncation. If the subject alone does not fit, it
          overflows the tile's own `overflow-hidden` and is cut there -- which
          is the same outcome as before for a case that has not been observed,
          and the price of guaranteeing it is never cut in the cases that
          have. */}
      <span className="shrink-0">{subject}</span>
      <span className="min-w-0 truncate">
        {' · '}
        {qualifier}
      </span>
    </span>
  )
}
