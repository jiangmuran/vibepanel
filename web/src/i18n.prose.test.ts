import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

/* The panel is a product. It is not a report to whoever built it.
 *
 * This has been said twice, in the same words both times: 「把他妈类似这种描述
 * 从产品里删掉 这些话是说给我听的 没必要写进产品」. The strings that drew it
 * were not wrong — they were arguments. A sentence that explains *why* the
 * panel behaves as it does, or defends a design against an objection nobody in
 * front of the screen has raised, belongs in a comment or in docs/.
 *
 * There is no way to test for "this is an argument", so this tests length,
 * which is the shape every one of them had. The budget is generous enough that
 * saying the thing fits and defending it does not.
 *
 * Placeholders do not count. `wh.body` is a form label listing the five
 * substitutions a webhook body may use; it is data, not prose, and measuring
 * it as prose makes the budget punish the one string that has to be exhaustive.
 *
 * The exception list is the point of the mechanism rather than a hole in it:
 * going over the budget is allowed, but it costs a line here and a reason,
 * which is exactly the moment to notice you are writing a paragraph.
 */
const ZH_MAX = 38
const EN_MAX = 105

/* The first-run tour gets its own budget, and it is still a budget.
 *
 * The rule above is about the panel arguing with somebody who is trying to use
 * it -- a status line that defends a design, a notice that explains itself.
 * The tour is the one surface where explaining *is* the content: it was asked
 * for as 「新手教程」, and a tour written in forty-character fragments is a
 * worse tour, not a more disciplined one.
 *
 * Wide enough for two clauses and no wider. A step that will not fit in these
 * is a step that is doing two things and should be two steps.
 */
const TOUR_ZH_MAX = 60
const TOUR_EN_MAX = 155

const allowed: Record<string, string> = {
  // Names the exact command to run, which is the whole value of the string.
  'set.tmuxConfigStale': 'carries the command the reader has to run',
  // Says what will and will not be restored. Getting this wrong loses work.
  'restore.body': 'what a restore does and does not bring back',
}

describe('the dictionary', () => {
  const src = readFileSync(new URL('./i18n.ts', import.meta.url), 'utf8')
  const prose = (s: string) => s.replace(/\{[\w]+\}/g, '')
  const entries = [
    ...src.matchAll(/'([\w.]+)':\s*\{\s*\n?\s*zh:\s*'((?:[^'\\]|\\.)*)',\s*\n?\s*en:\s*'((?:[^'\\]|\\.)*)'/g),
  ]

  it('has entries to check', () => {
    expect(entries.length).toBeGreaterThan(300)
  })

  it('says the thing and stops', () => {
    const over: string[] = []
    for (const [, key, zh, en] of entries) {
      if (key in allowed) continue
      const tour = key.startsWith('tour.')
      const zMax = tour ? TOUR_ZH_MAX : ZH_MAX
      const eMax = tour ? TOUR_EN_MAX : EN_MAX
      const z = [...prose(zh)].length
      const e = prose(en).length
      if (z > zMax) over.push(`${key} zh is ${z} > ${zMax}: ${zh}`)
      if (e > eMax) over.push(`${key} en is ${e} > ${eMax}: ${en}`)
    }
    expect(over).toEqual([])
  })

  it('has no exception for a string that no longer needs one', () => {
    const keys = new Set(entries.map((e) => e[1]))
    const byKey = new Map(entries.map((e) => [e[1], e]))
    const stale: string[] = []
    for (const key of Object.keys(allowed)) {
      if (!keys.has(key)) {
        stale.push(`${key} is excused but no longer exists`)
        continue
      }
      const [, , zh, en] = byKey.get(key)!
      if ([...prose(zh)].length <= ZH_MAX && prose(en).length <= EN_MAX) {
        stale.push(`${key} is excused but now fits — remove the exception`)
      }
    }
    expect(stale).toEqual([])
  })
})
