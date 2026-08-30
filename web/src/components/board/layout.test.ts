import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * The share settings page measures itself, not the browser window.
 *
 * Everything on that page lives inside a modal whose body is a fraction of the
 * window, and every layout rule on it was written with `sm:`/`lg:`, which ask
 * about the window. The two of them disagree by a factor of three, so the
 * rules fired in the wrong places at every size the panel has:
 *
 *   - `lg:grid-cols-[1fr_20rem]` on the board editor was true on any laptop,
 *     and split 540px of actual space into a 208px canvas beside a 320px
 *     palette — the thing being arranged smaller than the list of things to
 *     drag onto it.
 *   - the create form's `flex-wrap` row gave its two `flex-1` inputs to three
 *     `shrink-0` selects, so "What is it for" was a 90px box with four
 *     characters of its own placeholder in it.
 *   - each link's row was nine `shrink-0` fields in one line, which adds up to
 *     more than the body is wide, so the settings page scrolled sideways.
 *
 * None of that is visible in a unit test and all of it is visible in a
 * photograph, which is why `scripts/shots.mjs` photographs this surface now.
 * What *is* checkable here is the rule the fix rests on: inside this dialog,
 * a responsive variant must be a container query. A `sm:` that comes back is
 * the same bug wearing the same clothes.
 *
 * Static, and it reads the files rather than rendering them: vitest runs in
 * node here on purpose (see vitest.config.ts), and the failure being caught is
 * a class name, not a computed style.
 */
const HERE = new URL('../', import.meta.url).pathname

/** The four files that draw the sharing page, inside the settings modal. */
const INSIDE_THE_DIALOG = [
  'ShareLinks.tsx',
  'BoardEditor.tsx',
  'BoardPalette.tsx',
  'BoardCanvas.tsx',
]

/** The file with its comments removed, so a `lg:` being *discussed* in a
 *  comment — several of them say why it is wrong — is not read as one being
 *  used. */
function source(file: string): string {
  return readFileSync(HERE + file, 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\/\/.*$/gm, '')
}

/** Tailwind's viewport breakpoints, as they appear in a class list. */
const VIEWPORT = /(?:^|[\s'"`])(sm|md|lg|xl|2xl):/

describe('the share settings page measures its own container', () => {
  it('reads the files at all', () => {
    // A path that stops resolving reports "no viewport breakpoints anywhere"
    // in exactly the same words as a clean page.
    for (const file of INSIDE_THE_DIALOG) {
      expect(source(file).length, file).toBeGreaterThan(1000)
    }
  })

  it('uses no viewport breakpoint anywhere inside the dialog', () => {
    for (const file of INSIDE_THE_DIALOG) {
      const hit = VIEWPORT.exec(source(file))
      expect(hit?.[0], `${file} asks about the browser window: ${hit?.[0]}`).toBeUndefined()
    }
  })

  it('declares the containers those queries are measured against', () => {
    // Two roots: the page, and the editor — which is rendered twice, once at
    // the width of the whole page and once inside a link's edit panel, and
    // has to answer differently in the two places.
    expect(source('ShareLinks.tsx')).toContain('@container')
    expect(source('BoardEditor.tsx')).toContain('@container')
  })

  it('splits the editor into canvas and palette on the container, not the window', () => {
    // The rule that produced the 208px canvas. Named explicitly rather than
    // covered by the sweep above, because this is the one whose failure was
    // reported by a person looking at it.
    expect(source('BoardEditor.tsx')).toMatch(/@\w+:grid-cols-\[minmax\(0,1fr\)_20rem\]/)
  })
})
