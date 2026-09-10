import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * The tab strip spans the row it is the only content of, on every device.
 *
 * This shipped once gated on `@media (pointer: coarse)`, and the report came
 * back with the same screenshot it started from: two icons in the corner of a
 * bar. 「右边咋还是这样」. A tablet with a keyboard, a stylus device and
 * anything in desktop-site mode all report `fine`, so the rule was there and
 * simply never applied to the person asking for it.
 *
 * Measured after: the track went from 56px wide with 26px tabs to 190px wide
 * with 93px tabs under a mouse, and coarse was unchanged. The pointer type was
 * never the question -- two icons hugging the left of a bar look wrong at any
 * size, which is what was asked about.
 */
describe('the panel tab strip', () => {
  const css = readFileSync(new URL('../styles.css', import.meta.url), 'utf8')

  // Comments talk about `pointer: coarse` at length, and the rule they explain
  // must not be inside one. Stripping them is the difference between reading
  // the stylesheet and reading the prose about it.
  const rules = css.replace(/\/\*[\s\S]*?\*\//g, ' ')

  it('grows, and is not conditional on anything', () => {
    const at = rules.indexOf('.vp-segmented-fill')
    expect(at, '.vp-segmented-fill is gone; nothing makes the strip span its row').toBeGreaterThan(-1)
    expect(rules.slice(at, at + 120)).toMatch(/flex-grow:\s*1/)

    // Nothing in front of it may be an unclosed media query. Counting braces
    // from the top is crude and it is the property that matters: a rule inside
    // any `@media` is a rule that some device does not get, and this one is
    // about balance rather than about hardware.
    const before = rules.slice(0, at)
    const depth = (before.match(/\{/g) ?? []).length - (before.match(/\}/g) ?? []).length
    expect(depth, '.vp-segmented-fill is nested inside a block, most likely a media query').toBe(0)
  })

  it('keeps the touch height where it belongs', () => {
    // The other half really is about fingers, so it stays gated. Checked so
    // that "remove the gate" is not read as "remove every gate".
    const coarse = rules.slice(rules.indexOf('@media (pointer: coarse)'))
    expect(coarse).toMatch(/\.vp-segmented\s*>\s*\.vp-tab\s*\{[^}]*min-height/)
  })
})
