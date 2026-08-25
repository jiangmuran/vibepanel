/**
 * Returns every control a screen reader would announce as just "button".
 *
 * This panel is mostly icons: pin, kill, restart, collapse, new terminal, the
 * theme toggle, the state dot. An icon with no text and no label is announced
 * as nothing at all, and a row of them is a row of "button, button, button".
 *
 * Four ways to have a name, all of them accepted because all of them work:
 * aria-label, aria-labelledby, a title attribute, visible text, or a <title>
 * inside the svg — which is how the state dots do it, and which is also what
 * makes their shapes describable rather than merely different.
 */
export function findUnnamedControls(target) {
  return target.evaluate(() => {
    const named = (el) => {
      const aria = el.getAttribute('aria-label')
      if (aria && aria.trim()) return true
      if (el.getAttribute('aria-labelledby')) return true
      const title = el.getAttribute('title')
      if (title && title.trim()) return true
      if ((el.textContent ?? '').trim()) return true
      const svgTitle = el.querySelector('svg > title')
      return !!(svgTitle && (svgTitle.textContent ?? '').trim())
    }
    const out = []
    for (const el of document.querySelectorAll('button, a[href], [role="button"]')) {
      if (el.closest('.xterm')) continue
      if (el.offsetParent === null) continue
      if (named(el)) continue
      const cls = (el.className || '').toString().split(/\s+/).filter(Boolean).slice(0, 2).join('.')
      out.push(el.getAttribute('data-testid') ?? `${el.tagName.toLowerCase()}${cls ? '.' + cls : ''}`)
    }
    return [...new Set(out)].slice(0, 10)
  })
}
