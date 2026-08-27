import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * One chrome button, defined once.
 *
 * `.vp-control` and the rest are in styles.css with the reasoning beside them.
 * This is the half that stylesheet cannot enforce: nothing stops the next
 * component from writing `vp-press rounded-md p-1 text-ink-2 transition-colors
 * duration-200 ease-vp hover:bg-surface-2 hover:text-ink` by hand again, and
 * that is precisely how the panel arrived at three control heights -- 20, 24
 * and 27 pixels -- sitting in the same rows as each other. Every one of them
 * was a reasonable line to write on its own; the sum of them is 「底下每一个控
 * 件都像是拼起来的」.
 *
 * The drift is invisible in review for the same reason it is invisible in the
 * product: nothing is wrong at any single site. It shows up only when two of
 * them are next to each other, which is a thing you see in a screenshot and
 * not in a diff.
 *
 * What is detected, and why in this shape:
 *
 *   - Square padding (`p-1`, not `px-`/`py-`) plus a radius plus a hover
 *     treatment is the fingerprint of an icon-only control. It is the only
 *     shape the drift ever took, and it is what `.vp-control` replaces.
 *   - `border` disqualifies. An outlined icon button is a different object: it
 *     is the square sibling of an outlined *text* button, sits at that
 *     button's height, and reads as broken beside it if it is stripped down to
 *     a bare 28px glyph. `share-open` and `token-refresh` are the two, and both
 *     stand next to a bordered text button.
 *   - The `.vp-tab` selected lift is checked separately. A set you choose from
 *     is a `.vp-segmented` of `.vp-tab`s; hand-rolling the lift is how the
 *     language picker and the spend range came to be two segmented controls
 *     that were not the panel's segmented control.
 *
 * Exceptions cost a line and a reason, keyed by the class list itself so that
 * editing an excused control re-opens the question, and the last assertion
 * fails when an excused class list is no longer written anywhere -- so the list
 * cannot quietly accumulate.
 */
const SRC = new URL('./', import.meta.url).pathname

/** The class list, verbatim, and why it is not `.vp-control`. */
const allowed: Record<string, string> = {
  // StateDot. A state glyph that happens to be pressable, not a button with an
  // icon in it: `-m-1` cancels the `p-1`, so the padding buys a target without
  // moving the dot. `.vp-control` would give it a 28px box with a min-width,
  // pushing every session name in the sidebar to the right.
  'vp-press -m-1 flex shrink-0 items-center justify-center rounded-md p-1 transition-colors duration-200 ease-vp hover:bg-surface-2':
    'a state indicator that is also pressable; its geometry is the glyph, not the target',
  // BottomTerminals. Closes the tab it lives inside, and a `.vp-tab` is itself
  // `--vp-control-h` tall -- a control the height of its own container bursts
  // it. `vp-tap` still gives a thumb its 44px.
  'vp-press vp-tap rounded-md p-0.5 vp-reveal hover:text-ink':
    'nested inside a .vp-tab of the same height',
}

function sources(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...sources(p))
    else if (name.endsWith('.tsx')) out.push(p)
  }
  return out
}

/**
 * Every className on an element, whether written as a string or as a template.
 *
 * Both forms, because the one that got away from the first pass of this sweep
 * was a template: the sidebar's add-project button carried the whole
 * hand-written list in a backtick with a conditional `ml-auto` after it, and a
 * scan of quoted strings alone does not see it. Interpolations are left in --
 * they are never a padding or a radius, and stripping them would need a parser.
 */
function classLists(text: string): { cls: string; index: number }[] {
  const out: { cls: string; index: number }[] = []
  for (const m of text.matchAll(/className=(?:"([^"]*)"|\{`([^`]*)`\})/g)) {
    out.push({ cls: (m[1] ?? m[2]).split(/\s+/).filter(Boolean).join(' '), index: m.index })
  }
  return out
}

const PADDING = /(?<![\w-])p-(?:0\.5|1|1\.5|2|2\.5|3)(?![\w.])/
const RADIUS = /\brounded-(?:md|vp|vp-lg|full)\b/
const HOVER = /hover:(?:bg-surface-2|bg-surface|text-ink)\b|\bvp-press\b/
const BORDER = /\bborder\b/

function looksLikeChromeButton(cls: string): boolean {
  if (cls.includes('vp-control') || cls.includes('vp-tab')) return false
  if (BORDER.test(cls)) return false
  return PADDING.test(cls) && RADIUS.test(cls) && HOVER.test(cls)
}

/** The lift `.vp-tab[aria-selected='true']` draws, written out by hand. */
const SELECTED_LIFT = /shadow-\[0_1px_2px_rgb\(0_0_0\/0\.12\)\]/g

function at(text: string, index: number): number {
  return text.slice(0, index).split('\n').length
}

describe('one chrome vocabulary', () => {
  const files = sources(SRC)

  it('finds the components at all', () => {
    // A scanner that walks the wrong directory reports nothing, which is
    // indistinguishable from a clean tree. The same guard as scale.test.ts.
    expect(files.length).toBeGreaterThan(10)
  })

  it('defines the primitives it is enforcing', () => {
    // If `.vp-control` is renamed or deleted, every converted button loses all
    // of its styling and not one assertion below fails: the class is simply
    // absent, and Tailwind never had an opinion about it. This is what notices.
    const css = readFileSync(new URL('./styles.css', import.meta.url), 'utf8')
    const missing = [
      '.vp-control',
      '.vp-tab',
      '.vp-chrome',
      '.vp-divider',
      '.vp-grip',
      '.vp-segmented',
    ].filter((sel) => !css.includes(`${sel} {`))
    expect(missing, `styles.css no longer defines:\n${missing.join('\n')}`).toEqual([])
  })

  it('hand-writes no chrome button', () => {
    const bad: string[] = []
    for (const file of files) {
      const text = readFileSync(file, 'utf8')
      for (const { cls, index } of classLists(text)) {
        if (!looksLikeChromeButton(cls) || cls in allowed) continue
        bad.push(`${file.slice(SRC.length)}:${at(text, index)}: ${cls}`)
      }
    }
    expect(
      bad,
      'use .vp-control from styles.css, and keep beside it only what is ' +
        'orthogonal to it -- ml-auto, -mr-1, vp-reveal, vp-tap, ' +
        'disabled:opacity-50. A padding or a hover colour of its own is the ' +
        `drift, not a variant:\n${bad.join('\n')}`,
    ).toEqual([])
  })

  it('hand-rolls no selected tab', () => {
    const bad: string[] = []
    for (const file of files) {
      const text = readFileSync(file, 'utf8')
      for (const m of text.matchAll(SELECTED_LIFT)) {
        bad.push(`${file.slice(SRC.length)}:${at(text, m.index)}: ${m[0]}`)
      }
    }
    expect(
      bad,
      'a set you choose from is a .vp-segmented of .vp-tabs, and the lift ' +
        `comes with the tab:\n${bad.join('\n')}`,
    ).toEqual([])
  })

  it('has no exception for a control that no longer needs one', () => {
    const seen = new Set<string>()
    for (const file of files) {
      for (const { cls } of classLists(readFileSync(file, 'utf8'))) seen.add(cls)
    }
    const stale = Object.keys(allowed).filter((cls) => !seen.has(cls))
    expect(
      stale,
      `excused but no longer written anywhere -- delete the exception:\n${stale.join('\n')}`,
    ).toEqual([])
  })
})
