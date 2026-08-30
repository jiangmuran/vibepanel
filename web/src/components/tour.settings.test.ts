import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { SETTINGS_SECTIONS, groupOf } from './settings/groups'

/**
 * Everything the first-run tour offers has a home in the settings dialog.
 *
 * The rule, in the words it was given in: 「这些都要放进设置里面」. A wizard is
 * the short version of the settings page, not a second place to configure the
 * same things — somebody who pressed "skip, and do not show again" on their
 * first day must still be able to find every one of them, and the tour is the
 * one surface that is designed to be seen once and never again.
 *
 * Two halves, because either alone passes while the rule is broken:
 *
 *   - every section a step links to has to exist, or the button opens the
 *     dialog on whatever `groupOf` falls back to;
 *   - every step that *does* something has to link somewhere, or it is an
 *     action that lives only in a dialog nobody can reopen.
 *
 * Static, over the source. The alternative is rendering seven steps in jsdom
 * and asserting on buttons, which tests React more than it tests the rule.
 */
const SRC = readFileSync(new URL('./Tour.tsx', import.meta.url), 'utf8')

/** A step is a function whose name the steps array names. */
function stepNames(): string[] {
  const m = /const steps = \[([^\]]+)\]/.exec(SRC)
  if (!m) throw new Error('Tour.tsx no longer has a `const steps = [...]`')
  return m[1].split(',').map((s) => s.trim()).filter(Boolean)
}

/** The body of one step function, so a step can be looked at on its own. */
function bodyOf(name: string): string {
  const start = SRC.indexOf(`function ${name}(`)
  if (start === -1) throw new Error(`Tour.tsx has no function ${name}`)
  const next = SRC.slice(start + 1).search(/\nfunction \w+\(/)
  return next === -1 ? SRC.slice(start) : SRC.slice(start, start + 1 + next)
}

describe('the tour and the settings dialog', () => {
  it('has steps to read at all', () => {
    // A regex that stops matching reports "every step is fine" in the same
    // words as a tour with nothing wrong with it.
    expect(stepNames().length).toBeGreaterThanOrEqual(5)
  })

  it('links only to sections that exist', () => {
    const linked = [...SRC.matchAll(/to="([a-z]+)"/g)].map((m) => m[1])
    expect(linked.length, 'no step links to settings at all').toBeGreaterThan(0)
    for (const section of linked) {
      expect(SETTINGS_SECTIONS as readonly string[], `Tour.tsx links to "${section}"`).toContain(
        section,
      )
      // And it resolves to a real group, which is what the dialog opens on.
      expect(groupOf(section as never), `"${section}" resolves to no group`).toBeTruthy()
    }
  })

  it('gives every step that acts a way into the settings page', () => {
    // A step "acts" if it has a button of its own that is not the tour's own
    // navigation. Those are the ones somebody has to be able to reach again.
    for (const name of stepNames()) {
      const body = bodyOf(name)
      // "Acts" means it has a control of its own -- any testid that is not the
      // handoff button and not the tour's own navigation. Keyed on the testid
      // rather than on `<button`, because a step's control may be a checkbox
      // or a select next.
      const own = [...body.matchAll(/data-testid=[{"`]*`?tour-([a-z-]+)/g)].map((m) => m[1])
      const acts = own.some((id) => !id.startsWith('to-'))
      if (!acts) continue
      expect(
        /data-tour-settings|<More /.test(body),
        `${name} does something and offers no way to reach it from settings. ` +
          'The tour is shown once; the settings page is where it lives afterwards.',
      ).toBe(true)
    }
  })
})
