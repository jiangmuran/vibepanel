import { describe, expect, it } from 'vitest'

import { getLang, setLang, t } from './i18n'

/**
 * The dictionary's job is that nothing is missing and nothing is stale.
 *
 * Both languages sit on one line precisely so a gap is impossible to introduce
 * by editing one file and forgetting the other — but a `satisfies` check only
 * proves the shape, not that somebody left `zh` equal to the English while they
 * "came back to it later".
 */
describe('i18n', () => {
  it('substitutes where the language puts the number, not where the caller does', () => {
    setLang('en')
    expect(t('todos.leftOf', { left: 3, total: 5 })).toBe('3 of 5 left')
    setLang('zh')
    // The same two facts, in the other order, from the same call.
    expect(t('todos.leftOf', { done: 2, total: 5 })).toBe('2 / 5 已完成')
  })

  it('switches, and remembers which it is', () => {
    setLang('en')
    expect(getLang()).toBe('en')
    expect(t('app.projects')).toBe('Projects')
    setLang('zh')
    expect(getLang()).toBe('zh')
    expect(t('app.projects')).toBe('项目')
  })

  it('leaves no placeholder unfilled in either language', () => {
    // A key whose zh has {n} and whose en does not is the drift that shows a
    // literal "{n}" to exactly one set of users.
    for (const lang of ['zh', 'en'] as const) {
      setLang(lang)
      for (const [key, params] of [
        ['todos.leftOf', { left: 1, total: 2, done: 1 }],
        ['monitor.cores', { n: 16 }],
        ['monitor.free', { size: '1 GiB' }],
        ['dir.truncated', { shown: 10, total: 99 }],
      ] as const) {
        const out = t(key, params)
        expect(out, `${key} in ${lang}`).not.toMatch(/\{[a-z]+\}/i)
      }
    }
  })
})
