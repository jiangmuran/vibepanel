/**
 * Returns every named control with something else on top of it.
 *
 * The third measurement in this directory that a script and a person disagree
 * about. `overflow: hidden` is still scrollable by a script; `opacity: 0` is
 * still visible to one; and a control can be present, opaque, correctly sized
 * and completely underneath something else.
 *
 * Playwright does notice this, but only for controls a check actually clicks,
 * and only by timing out: thirty seconds, then a `TimeoutError` that aborts the
 * rest of the run. The information is in the call log — it names the element
 * that "intercepts pointer events" — but you learn about one covered control
 * per run, and nothing at all about the ones no check clicks.
 *
 * This costs a millisecond and reports all of them.
 *
 * Only run it where nothing is deliberately covering the screen. A modal, a
 * drawer or a scrim covering what is behind it is the point of a modal, and a
 * scan that fires on every control behind an open dialog is a scan that gets
 * deleted.
 */
export function findCoveredControls(target) {
  return target.evaluate(() => {
    const out = []
    for (const el of document.querySelectorAll('[data-testid]')) {
      const box = el.getBoundingClientRect()
      if (box.width < 4 || box.height < 4) continue
      const cs = getComputedStyle(el)
      if (cs.display === 'none' || cs.visibility === 'hidden') continue
      // A control that does not take pointer events is not being covered, it
      // is decoration.
      if (cs.pointerEvents === 'none') continue

      const x = box.left + box.width / 2
      const y = box.top + box.height / 2
      if (x < 0 || y < 0 || x > innerWidth || y > innerHeight) continue

      // Scrolled out of a scroller is not covered.
      //
      // The fourth measurement in this directory that a script and a person
      // disagree about, and it arrived with the second scroll container: the
      // side panel's files tab is now a file list over a repository panel with
      // a divider between them, so a row scrolled past the bottom of the list
      // is still inside the *window* — its centre lands over the repository
      // panel, and elementFromPoint correctly reports that. Every row below
      // the fold was named as covered by whatever sits underneath.
      //
      // A person does not experience that as covered. They scroll to it. The
      // same lesson overflow.mjs states for its own case: an element inside a
      // scroller having its box outside that scroller is how a scroller works.
      //
      // Only the scroller's own box is checked, not every ancestor's — a
      // clipped ancestor that is *not* scrollable is content that is genuinely
      // unreachable, and reporting that is what this file is for.
      let clipped = false
      for (let a = el.parentElement; a && !clipped; a = a.parentElement) {
        const cs = getComputedStyle(a)
        const scrolls = /auto|scroll/.test(cs.overflowY) || /auto|scroll/.test(cs.overflowX)
        if (!scrolls) continue
        const r = a.getBoundingClientRect()
        clipped = x < r.left || x > r.right || y < r.top || y > r.bottom
      }
      if (clipped) continue

      const hit = document.elementFromPoint(x, y)
      if (!hit) continue
      // The control itself, something inside it, or something it sits inside:
      // all of those mean the point belongs to this control.
      if (hit === el || el.contains(hit) || hit.contains(el)) continue

      const name = (n) => {
        const id = n.getAttribute('data-testid')
        if (id) return `[${id}]`
        const cls = (n.className || '').toString().split(/\s+/).filter(Boolean).slice(0, 3).join('.')
        return n.tagName.toLowerCase() + (cls ? '.' + cls : '')
      }
      out.push(`${name(el)} is under ${name(hit)}`)
    }
    return out.slice(0, 8)
  })
}
