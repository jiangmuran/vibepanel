import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * One type scale and one radius scale, in the tokens, not in the components.
 *
 * Nine font sizes were in use -- 9, 10, 10.5, 11, 11.5, 12, 12.5, 13, 15 -- and
 * six radii. That is not a scale, it is nine decisions taken at nine different
 * moments. Nothing looks wrong at any one of them, which is exactly why it
 * accumulates: the difference between 11px and 11.5px is invisible on its own,
 * and the sum of eight such differences is what "unrefined" means.
 *
 * An arbitrary value in a component is how the tenth size arrives, so this
 * refuses them. `text-vp-sm` and the rest are defined in styles.css; if a new
 * step is genuinely needed it belongs there, where it is one decision that
 * applies everywhere rather than one that applies here.
 *
 * Sizes inside styles.css itself are not covered -- that is where they live.
 */
const SRC = new URL('./', import.meta.url).pathname

function sources(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...sources(p))
    else if (name.endsWith('.tsx')) out.push(p)
  }
  return out
}

describe('the design scale lives in the tokens', () => {
  const files = sources(SRC)

  it('finds the components at all', () => {
    expect(files.length).toBeGreaterThan(10)
  })

  it('has no arbitrary font size in a component', () => {
    const bad: string[] = []
    for (const file of files) {
      const text = readFileSync(file, 'utf8')
      for (const m of text.matchAll(/text-\[[0-9.]+(?:px|rem|em)\]/g)) {
        bad.push(`${file.slice(SRC.length)}: ${m[0]}`)
      }
    }
    expect(bad, `use text-vp-xs|sm|base|md|lg, or add a step to styles.css:\n${bad.join('\n')}`)
      .toEqual([])
  })

  // Tailwind's own text-xs/text-sm/... are a second scale sitting beside this
  // one, in the same class attribute, named so similarly that nobody notices.
  // One arrived within an hour of the scale being introduced.
  it("uses no Tailwind default text size", () => {
    const bad: string[] = []
    for (const file of files) {
      const text = readFileSync(file, 'utf8')
      for (const m of text.matchAll(/\btext-(?:xs|sm|base|lg|xl|[2-9]xl)\b/g)) {
        bad.push(`${file.slice(SRC.length)}: ${m[0]}`)
      }
    }
    expect(bad, `use text-vp-xs|sm|base|md|lg -- Tailwind's steps are a different ` +
      `scale wearing similar names:\n${bad.join('\n')}`).toEqual([])
  })

  it('has no arbitrary corner radius in a component', () => {
    const bad: string[] = []
    for (const file of files) {
      const text = readFileSync(file, 'utf8')
      for (const m of text.matchAll(/rounded-\[[^\]]+\]/g)) {
        bad.push(`${file.slice(SRC.length)}: ${m[0]}`)
      }
    }
    expect(bad, `use rounded-md | rounded-vp | rounded-vp-lg | rounded-full:\n${bad.join('\n')}`)
      .toEqual([])
  })

  it('uses only the four named radii', () => {
    const allowed = new Set(['rounded-md', 'rounded-vp', 'rounded-vp-lg', 'rounded-full'])
    const bad: string[] = []
    for (const file of files) {
      const text = readFileSync(file, 'utf8')
      for (const m of text.matchAll(/\brounded(?:-[a-z0-9]+)*\b/g)) {
        if (!allowed.has(m[0])) bad.push(`${file.slice(SRC.length)}: ${m[0]}`)
      }
    }
    expect(bad, `rounded, rounded-lg and friends are Tailwind's steps, not this ` +
      `project's; use one of the four:\n${bad.join('\n')}`).toEqual([])
  })
})
