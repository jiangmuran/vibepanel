import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { STORAGE_KEY } from './components/theme'

/**
 * The theme is written to the DOM in two places and they have to agree.
 *
 * `index.html` sets `data-theme` from localStorage before first paint, because
 * reading it in React instead shows a frame of the wrong palette on every load.
 * `theme.ts` owns the key and the values.
 *
 * If the key drifts, the visible result is not a flash — it is the theme choice
 * silently not working. The inline script finds nothing, and nothing else
 * applies a stored choice: `applyTheme` runs only from the toggle's handler. So
 * the session follows the system preference while the toggle shows whatever was
 * chosen.
 */
const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8')

describe('the pre-paint theme script', () => {
  it('reads the key theme.ts writes', () => {
    expect(
      html.includes(`'${STORAGE_KEY}'`) || html.includes(`"${STORAGE_KEY}"`),
      `index.html does not mention ${STORAGE_KEY}; the stored theme would be ignored on load`,
    ).toBe(true)
  })

  it('honours only the values theme.ts can store', () => {
    // 'system' deliberately sets no attribute — the CSS treats its absence as
    // "follow prefers-color-scheme" — so the script must not write it.
    expect(html).toMatch(/'light'|"light"/)
    expect(html).toMatch(/'dark'|"dark"/)
    expect(html).not.toMatch(/dataset\.theme\s*=\s*['"]system['"]/)
  })

  it('runs before the app script', () => {
    // Below the module tag it would paint once in the wrong palette first,
    // which is the entire reason it is inline rather than imported.
    const inline = html.indexOf('localStorage.getItem')
    const module = html.indexOf('src/main.tsx')
    expect(inline).toBeGreaterThan(-1)
    expect(module).toBeGreaterThan(-1)
    expect(inline).toBeLessThan(module)
  })

  it('cannot throw where storage is unavailable', () => {
    // Private mode makes localStorage.getItem throw in some browsers, and an
    // exception here happens before anything has rendered: the page would be
    // blank rather than merely the wrong colour.
    const script = html.slice(html.indexOf('<script>'), html.indexOf('</script>'))
    expect(script).toMatch(/try\s*\{/)
    expect(script).toMatch(/catch/)
  })
})
