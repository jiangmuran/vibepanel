import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * Red line 5, as a test rather than a rule people remember.
 *
 * Theme blocks redefine tokens; they never define component styles. Putting a
 * component's colours inside a `[data-theme]` or `prefers-color-scheme` block
 * is the classic cause of white-on-white after a theme switch: the rule exists
 * in one theme and simply is not there in the other, so whatever it was
 * overriding comes back.
 *
 * Static, so it costs nothing and cannot be fooled by a screenshot taken in
 * the theme that happens to work.
 */
// node:fs, not Vite's `?raw`: vitest runs with css: false, so a CSS import —
// including a raw one — resolves to an empty stub and every assertion below
// passes against nothing. The "blocks exist" guard is what caught that.
const css = readFileSync(new URL('./styles.css', import.meta.url), 'utf8')

/** The file with comments removed, so their colons are not read as CSS. */
const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '')

/** Body of the block whose opening brace follows `from`, by brace counting. */
function blockAt(source: string, from: number): { body: string; end: number } {
  const open = source.indexOf('{', from)
  if (open < 0) return { body: '', end: from }
  let depth = 0
  for (let i = open; i < source.length; i++) {
    if (source[i] === '{') depth++
    else if (source[i] === '}') {
      depth--
      if (depth === 0) return { body: source.slice(open + 1, i), end: i }
    }
  }
  return { body: source.slice(open + 1), end: source.length }
}

/** Every region that exists to redefine tokens for a theme. */
function themeBlocks(): { label: string; body: string }[] {
  const out: { label: string; body: string }[] = []

  const dark = stripped.indexOf('@media (prefers-color-scheme: dark)')
  if (dark >= 0) {
    out.push({ label: 'prefers-color-scheme: dark', body: blockAt(stripped, dark).body })
  }

  // Any rule whose selector mentions the theme attribute.
  const attr = /\[data-theme=/g
  let m: RegExpExecArray | null
  while ((m = attr.exec(stripped)) !== null) {
    const { body, end } = blockAt(stripped, m.index)
    out.push({ label: `[data-theme] rule at ${m.index}`, body })
    attr.lastIndex = Math.max(attr.lastIndex, end)
  }
  return out
}

/** Property names declared anywhere in a block, nested rules included. */
function declaredProperties(body: string): string[] {
  const props: string[] = []
  const re = /([a-zA-Z-][a-zA-Z0-9-]*)\s*:/g
  let m: RegExpExecArray | null
  while ((m = re.exec(body)) !== null) {
    // A declaration's colon is followed by a value and then `;` or the end of
    // its block; a selector's colon (`a:hover`) is followed by `{`. Whichever
    // comes first decides.
    //
    // This started out matching line-first, which read as simpler and had two
    // holes that both point the wrong way: a component rule written on one
    // line inside a theme block was invisible, and a bare `a:hover {` on its
    // own line was reported as a declaration named "a". A check for a red line
    // has to be right about both directions, or the rule it guards is worth
    // less than it looks.
    const rest = body.slice(re.lastIndex)
    const brace = rest.indexOf('{')
    const term = rest.search(/[;}]/)
    if (term >= 0 && (brace < 0 || term < brace)) props.push(m[1])
  }
  return props
}

describe('theme blocks', () => {
  it('exist', () => {
    // If this fails the rest proves nothing: a parser that finds no blocks
    // finds no violations either.
    expect(themeBlocks().length).toBeGreaterThanOrEqual(2)
  })

  /*
   * One property that is not a token and belongs in a theme block anyway.
   *
   * `color-scheme` is the theme, told to the browser. It decides what the UA
   * paints where this stylesheet paints nothing: form controls, scrollbars, the
   * overscroll iOS rubber-bands into, and the safe area under a home-screen
   * PWA's home indicator. Leaving it out is not a missing nicety -- it is the
   * page and the browser disagreeing about which theme is on, and the panel
   * shipped with them disagreeing in all six combinations of system and forced
   * theme, measured.
   *
   * It cannot live outside these blocks: its whole content is which theme is
   * current, which is what these blocks are. And it cannot cause the failure
   * the rule guards against -- that is a *component* styled in one theme and
   * not the other, and this is a root-level declaration with no component to
   * drift from.
   *
   * Named rather than pattern-matched. An exception list with one entry costs a
   * line to extend and a reason to justify, which is the point.
   */
  const allowed = new Set(['color-scheme'])

  it('declare only custom properties', () => {
    for (const block of themeBlocks()) {
      const offenders = declaredProperties(block.body)
        .filter((p) => !p.startsWith('--'))
        .filter((p) => !allowed.has(p))
      expect(
        offenders,
        `${block.label} declares component styles (${offenders.join(', ')}); a rule that ` +
          'exists in one theme and not the other is how white-on-white happens',
      ).toEqual([])
    }
  })

  /*
   * And it has to be in every one of them.
   *
   * Three blocks cover four states, and the one that broke is the pair that
   * disagree: a panel forced dark on a device whose system is light. The page
   * followed [data-theme] and the browser followed prefers-color-scheme, so the
   * app was dark and everything around it was white -- reported from an iPad as
   * a white edge along the bottom of a black panel in PWA mode.
   *
   * Deleting it from any single block puts one of those four states back.
   */
  it('tell the browser which theme is on', () => {
    // The base :root as well, which themeBlocks() does not return -- it looks
    // for the *overrides*, and the light theme is the thing being overridden.
    // That block is the answer for the fourth state: a panel forced light on a
    // device whose system is dark, where the media query is excluded by its own
    // `:not([data-theme='light'])` and nothing else would say so.
    const base = stripped.match(/:root\s*\{[\s\S]*?\n\}/)?.[0] ?? ''
    expect(base, 'no :root block found, so the check below proves nothing').not.toBe('')
    expect(
      declaredProperties(base),
      'the base :root does not set color-scheme, so a panel forced light on a ' +
        'dark system leaves the browser painting dark around it',
    ).toContain('color-scheme')

    for (const block of themeBlocks()) {
      expect(
        declaredProperties(block.body),
        `${block.label} does not set color-scheme, so the browser paints the ` +
          'scrollbars, the form controls and a PWA safe area in the other theme',
      ).toContain('color-scheme')
    }
  })
})

describe('motion that collapses to nothing', () => {
  /*
   * The reduced-motion block is what makes every animation in this file
   * optional, so anything it fails to reset is an animation somebody who asked
   * for none still gets.
   *
   * The delay is the one that bites. A staggered list (`.vp-rows`) uses
   * `animation-delay` with a `backwards` fill, so the last row holds its
   * from-state -- opacity 0 -- for the whole delay whatever the duration is.
   * Collapsing only the duration left the eighth row of the monitor invisible
   * for 175ms to exactly the people who asked for it not to move.
   */
  const block = (() => {
    const at = stripped.indexOf('@media (prefers-reduced-motion: reduce)')
    return at < 0 ? '' : blockAt(stripped, at).body
  })()

  it('exists', () => {
    // Without this the two below pass by measuring an empty string.
    expect(block.length).toBeGreaterThan(20)
  })

  it('has something to collapse', () => {
    // A stagger with no delays is not a stagger, and the assertion below would
    // pass over a stylesheet that had lost it.
    const delays = [...stripped.matchAll(/animation-delay:\s*\d+ms/g)]
    expect(delays.length, 'no staggered animation left to protect').toBeGreaterThan(3)
  })

  it('collapses the delay as well as the duration', () => {
    for (const prop of ['animation-duration', 'animation-delay', 'transition-duration']) {
      expect(
        declaredProperties(block),
        `an animation's ${prop} survives prefers-reduced-motion`,
      ).toContain(prop)
    }
  })
})

describe('theme tokens', () => {
  const rootBody = (() => {
    // The bare `:root {` at the top, not `:root[data-theme=...]`.
    const at = /(^|\n):root\s*\{/.exec(stripped)
    return at ? blockAt(stripped, at.index).body : ''
  })()

  it('has a base definition for every token', () => {
    const base = new Set(declaredProperties(rootBody).filter((p) => p.startsWith('--')))
    expect(base.size).toBeGreaterThan(10)

    for (const block of themeBlocks()) {
      for (const prop of declaredProperties(block.body)) {
        if (!prop.startsWith('--')) continue
        expect(
          base.has(prop),
          `${prop} is defined in ${block.label} but has no value on bare :root, so it is ` +
            'undefined in the theme that block does not cover',
        ).toBe(true)
      }
    }
  })
})
