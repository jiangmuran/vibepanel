import { describe, expect, it } from 'vitest'

import { agoParts, formatAgo } from './ago'
import { setLang } from '../../i18n'

describe('how long ago something happened', () => {
  const now = 1_700_000_000

  it('climbs a unit at a time', () => {
    expect(agoParts(now - 30, now)).toEqual({ value: -30, unit: 'second' })
    expect(agoParts(now - 120, now)).toEqual({ value: -2, unit: 'minute' })
    expect(agoParts(now - 7200, now)).toEqual({ value: -2, unit: 'hour' })
    expect(agoParts(now - 3 * 86400, now)).toEqual({ value: -3, unit: 'day' })
    expect(agoParts(now - 60 * 86400, now)).toEqual({ value: -2, unit: 'month' })
    expect(agoParts(now - 800 * 86400, now)).toEqual({ value: -2, unit: 'year' })
  })

  it('clamps a commit from the future to now', () => {
    // Clocks differ across machines that share a repository, and "in 3 hours"
    // under a commit reads as the panel being broken rather than as the clock
    // being wrong.
    expect(agoParts(now + 10_000, now)).toEqual({ value: 0, unit: 'second' })
  })
})

describe('the same as a string', () => {
  const now = 1_700_000_000

  it('says nothing at all about something that never happened', () => {
    // A todo with no doneAt, a note the server has not written yet. Zero is
    // 1970, and "56 years ago" beside an unticked item is worse than a blank.
    expect(formatAgo(0, now)).toBe('')
    expect(formatAgo(-1, now)).toBe('')
    expect(formatAgo(Number.NaN, now)).toBe('')
  })

  it('speaks the language the panel is in', () => {
    setLang('en')
    expect(formatAgo(now - 3 * 86400, now)).toBe('3 days ago')
    setLang('zh')
    expect(formatAgo(now - 3 * 86400, now)).toBe('3天前')
  })

  it('has a word for yesterday rather than a count', () => {
    // numeric: 'auto'. "1 day ago" is what a hand-rolled table produces and it
    // is what nobody says in either language.
    setLang('en')
    expect(formatAgo(now - 86400, now)).toBe('yesterday')
  })
})
