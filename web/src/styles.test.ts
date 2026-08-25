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

  it('declare only custom properties', () => {
    for (const block of themeBlocks()) {
      const offenders = declaredProperties(block.body).filter((p) => !p.startsWith('--'))
      expect(
        offenders,
        `${block.label} declares component styles (${offenders.join(', ')}); a rule that ` +
          'exists in one theme and not the other is how white-on-white happens',
      ).toEqual([])
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
