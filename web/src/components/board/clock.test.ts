import { describe, expect, it } from 'vitest'

import { clockStep } from './wall'

/**
 * A truncated number is a wrong number.
 *
 * The wall clock was `text-vp-3xl truncate`, and on a tablet the tile is 264px
 * wide: "02:00 AM" overflowed by 55px and rendered as "02:00…", which reads as
 * a clock that has stopped rather than one that has been styled. Measured by
 * `make board-check` at ipad-landscape.
 *
 * The length is not the panel's to choose. `toLocaleTimeString` formats in the
 * reader's locale, and twelve-hour clocks are three characters longer than
 * twenty-four-hour ones -- so the same board is fine in Berlin and cut in
 * Chicago, which is the kind of difference nobody developing it will see.
 */
describe('the wall clock gives up size before it gives up digits', () => {
  it('keeps the largest step for a bare HH:MM', () => {
    expect(clockStep('14:00')).toBe('text-vp-3xl')
    expect(clockStep('09:45')).toBe('text-vp-3xl')
  })

  it('steps down for a twelve-hour clock, which is the reported case', () => {
    expect(clockStep('02:00 AM')).toBe('text-vp-2xl')
    expect(clockStep('12:55 PM')).toBe('text-vp-2xl')
  })

  it('steps down for anything longer, whatever produced it', () => {
    // Locales exist that append more than two letters. The rule is about the
    // width of the string, not about AM and PM.
    expect(clockStep('午前 02:00')).toBe('text-vp-2xl')
  })
})
