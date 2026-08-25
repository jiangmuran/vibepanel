/**
 * Returns every named control that a script can see and a person cannot.
 *
 * Playwright's `isVisible()` is a script's notion of visible: a bounding box
 * with a size, and `visibility` not `hidden`. An element at `opacity: 0`
 * satisfies both. So does one whose parent is.
 *
 * Which means a check that asserts an affordance is present can pass against a
 * build where it is completely transparent. Measured: adding `opacity-0` to the
 * "take control" pill — the only way out of a passive viewer's scaled grid, and
 * something the render check has a dedicated FAIL for when it is missing — left
 * the whole run green.
 *
 * That is the same shape as `overflow: hidden` in overflow.mjs, and the same
 * lesson: a measurement a script can make is not automatically a measurement
 * about what somebody sees.
 *
 * Opacity is multiplied down the ancestor chain, because that is how it
 * composes and because a faded container is the more likely accident.
 *
 * `.vp-reveal` is excluded, and it is the only exclusion because it is the only
 * intentional use of this in the panel: a row control that appears on hover,
 * and only where hovering exists — styles.css turns it off below the hover
 * media query for exactly the reason this scan exists. A separate check already
 * asserts those are opaque on a touch screen.
 */
export function findFadedControls(target, minOpacity = 0.05) {
  return target.evaluate((min) => {
    const out = []
    for (const el of document.querySelectorAll('[data-testid]')) {
      if (el.closest('.vp-reveal')) continue
      const cs = getComputedStyle(el)
      if (cs.display === 'none' || cs.visibility === 'hidden') continue
      const box = el.getBoundingClientRect()
      if (box.width === 0 || box.height === 0) continue

      let opacity = 1
      for (let a = el; a && a !== document.documentElement; a = a.parentElement) {
        opacity *= Number(getComputedStyle(a).opacity)
        if (opacity < min) break
      }
      if (opacity < min) {
        out.push(`[${el.getAttribute('data-testid')}] at opacity ${opacity.toFixed(2)}`)
      }
    }
    return out.slice(0, 8)
  }, minOpacity)
}
