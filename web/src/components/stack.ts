/**
 * The divider inside a tab, and where its position lives.
 *
 * Two tabs are two panels stacked on top of each other — the file tree over
 * the repository, the note over the checklist — with a grip between them that
 * drags up and down. This is the arithmetic behind that grip, kept out of the
 * component so it can be tested without a DOM, which is the same split
 * panes.ts made for the same reason.
 *
 * It is deliberately *not* part of PaneLayout. A pane layout is a set of tabs
 * arranged into panes and its invariant is about tabs; this is one number per
 * stacked tab, it survives a tab being dragged into another pane, and folding
 * it into that structure would mean every operation in panes.ts had to carry a
 * value none of them have an opinion about.
 */

/**
 * Neither half may be dragged smaller than this share of the tab.
 *
 * Higher than PANE_MIN_RATIO's 0.1, and for a different reason: a pane dragged
 * to a tenth still shows its tab strip, which is how you get it back. A half of
 * a stacked tab has no strip of its own — the grip above it is the only way
 * back — so it has to keep enough height to be recognisable as a panel rather
 * than as a line.
 */
export const STACK_MIN_RATIO = 0.15

/** Where a stacked tab opens before anybody has dragged it. */
export const STACK_DEFAULT_RATIO = 0.6

/**
 * A share of the tab's height, or the nearest one that is allowed.
 *
 * NaN and Infinity resolve to the default rather than to a bound. A bound is a
 * position somebody dragged to; the default is the position nobody chose, and
 * a value that is not a number was not chosen.
 */
export function clampStackRatio(ratio: number): number {
  if (!Number.isFinite(ratio)) return STACK_DEFAULT_RATIO
  return Math.max(STACK_MIN_RATIO, Math.min(1 - STACK_MIN_RATIO, ratio))
}

/**
 * Where this tab's divider is remembered.
 *
 * Per tab, not per panel: the files tab and the notes tab are asking different
 * questions of the same column and there is no reason a person who wants the
 * repository large also wants their checklist large.
 *
 * Not bucketed by viewport the way the pane layout is. A ratio is already
 * relative to whatever height it lands in, so it carries across screens
 * correctly by construction — which is exactly the thing a pixel count could
 * not do and is why panes.ts buckets.
 */
export function stackStorageKey(id: string): string {
  return `vibepanel.stack.${id}`
}

/**
 * The remembered position, out of whatever localStorage handed back.
 *
 * Same suspicion as readLayout(): a string in a key a person can edit, which a
 * six-month-old build may have written. `Number('')` and `Number(null)` are
 * both 0, and 0 is a legal-looking ratio that collapses the top half
 * completely — so emptiness is checked before Number() gets hold of it, not
 * after.
 *
 * Everything else is clampStackRatio's problem, including `Number('half')`.
 * There was a second `Number.isFinite` check here and mutation testing found
 * it: removing it changed nothing, because the clamp already answers NaN with
 * the default. A guard whose removal no test notices is a guard that is not
 * doing anything, and two of them in a row is the reader having to work out
 * which one is load-bearing.
 */
export function readStackRatio(raw: string | null): number {
  if (raw === null || raw.trim() === '') return STACK_DEFAULT_RATIO
  return clampStackRatio(Number(raw))
}

/**
 * Where a pointer at `clientY` puts the divider, as a share of the box.
 *
 * A box with no height is a tab nothing has laid out yet — a display:none
 * ancestor, the frame before the first paint — and returns null so the caller
 * leaves the ratio where it is. Dividing by it would write NaN into the key
 * and take the tab's arrangement with it.
 */
export function stackRatioAt(clientY: number, top: number, height: number): number | null {
  if (!Number.isFinite(height) || height <= 0) return null
  return clampStackRatio((clientY - top) / height)
}
