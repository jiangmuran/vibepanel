import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * Free text that arrived from somewhere else goes through safeText, everywhere.
 *
 * `safeText` replaces the characters that make a string lie about what it is:
 * the bidi overrides, whose entire job is to reverse the visual order of what
 * follows them, and the zero-width invisibles that make two different strings
 * the same three rows of pixels. The reasoning is in components/text.ts and it
 * is not repeated here.
 *
 * What is repeated here is the *enforcement*, because the rule is one that gets
 * followed carefully in the file where it was written and forgotten in the next
 * one. Every field below is free text somebody typed and the panel renders:
 *
 *   - `remark` — the owner's label for a screen, added for a wall display, and
 *     the newest of these. It is drawn in the dashboard header, in a widget of
 *     its own, and in a settings row.
 *   - `text` — a board's caption and section heading.
 *   - `name`, `title`, `scopeName` — the link's name, session titles from
 *     `pane_title` (which any program sets with two bytes) and project names.
 *
 * A static scan rather than a rendering test, because the tests here run
 * without a browser on purpose — see vitest.config.ts — and because the next
 * one arrives as a one-line `{link.remark}` in a component nobody is reviewing
 * for this.
 */
const SRC = new URL('./', import.meta.url).pathname

/** The fields that carry text from outside into the DOM. */
const FIELDS = ['remark', 'text', 'scopeName']

function sources(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...sources(p))
    else if (name.endsWith('.tsx')) out.push(p)
  }
  return out
}

describe('text from outside the panel', () => {
  const files = sources(SRC)

  it('has files to check', () => {
    expect(files.length).toBeGreaterThan(10)
  })

  it('is never rendered without safeText', () => {
    const bad: string[] = []
    for (const file of files) {
      const src = readFileSync(file, 'utf8')
      // Every JSX expression container on one line. Crude on purpose: a
      // cleverer parser is a second thing to be wrong, and the shape being
      // caught — `{safeText(x)}` versus `{x}` — is visible in one line of text.
      for (const line of src.split('\n')) {
        for (const field of FIELDS) {
          const re = new RegExp(`\\{[^{}]*\\.${field}\\b[^{}]*\\}`, 'g')
          for (const match of line.matchAll(re)) {
            const expr = match[0]
            // A prop being passed down, a comparison, or a spread is not a
            // render. What this is looking for is a value reaching the DOM.
            if (/=\s*$/.test(line.slice(0, match.index))) continue
            if (expr.includes('safeText(')) continue
            if (/[=!]==?|\?\?|&&|\|\||\.\.\./.test(expr)) continue
            bad.push(`${file.slice(SRC.length)}: ${expr.trim()}`)
          }
        }
      }
    }
    expect(
      bad,
      'free text reaching the DOM without safeText; a bidi override in one of these ' +
        'reverses everything after it on the screen:\n' + bad.join('\n'),
    ).toEqual([])
  })
})
