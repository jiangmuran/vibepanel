import { createContext, useContext } from 'react'

/**
 * How much each widget says — which is not how large it is drawn.
 *
 * The two were one idea to begin with, keyed to viewing distance: a television
 * is read from three metres, therefore a television is sparse. That is wrong as
 * the only case, and the person it is for said so: 「也可以密一点啊 我可能坐在
 * 前面」. They sit in front of the same screen half the time and want it packed
 * when they do.
 *
 * So there are two axes and neither derives from the other:
 *
 *   scale    how large everything is drawn. A property of the viewport, settled
 *            in CSS with no stored value at all — see `.vp-wall` in styles.css.
 *            A 4K screen shows the same composition bigger, never more columns
 *            of smaller type.
 *   density  how much is on screen. A property of the *board*, stored on it,
 *            three steps. This.
 *
 * All four corners are real: dense and close, spare and across the room, and
 * both crosses.
 *
 * A widget with nothing more to say ignores this, which is why it is a hint and
 * not a mode. It must never decide whether a widget renders — that would be a
 * stored number choosing a code path, which is the one thing a board may not
 * do — only how much of what it already has it puts on screen.
 */

/** The steps. Mirrors store.MinDensity / MaxDensity / DefaultDensity. */
export const SPARE = 1
export const NORMAL = 2
export const DENSE = 3

/**
 * A context rather than a prop threaded through thirty-seven components.
 *
 * The prop version was written first and it is worse in the way that matters:
 * every widget would have to accept and forward a value most of them ignore, so
 * the ones that ignore it look identical to the ones that forgot it.
 */
const DensityContext = createContext<number>(NORMAL)

export const DensityProvider = DensityContext.Provider

/** This board's density, clamped. */
export function useDensity(): number {
  const raw = useContext(DensityContext)
  return clampDensity(raw)
}

/**
 * Clamped here as well as on the server, and for the same reason `rowSpan` is:
 * this is the copy that runs on somebody else's machine, and a board out of a
 * database is a value from outside this file.
 */
export function clampDensity(n: number | undefined): number {
  if (!Number.isFinite(n) || (n ?? 0) < SPARE) return NORMAL
  return Math.min(Math.floor(n!), DENSE)
}

/**
 * How many rows of a list to show at this density.
 *
 * One helper rather than a ternary in each widget, so that "denser" means the
 * same step everywhere on a board. A screen where one list grew and the one
 * beside it did not reads as a bug.
 */
export function rows(density: number, base: number): number {
  if (density <= SPARE) return Math.max(1, Math.round(base * 0.6))
  if (density >= DENSE) return Math.round(base * 1.8)
  return base
}

/** Whether this density has room for the qualifying line under a figure. */
export function showsDetail(density: number): boolean {
  return density > SPARE
}
