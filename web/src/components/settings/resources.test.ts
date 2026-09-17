import { describe, expect, it } from 'vitest'

import { policyOnly, pollStillCurrent, wholeNumber } from './resourcesPolicy'

describe('the resources page', () => {
  it('draws a poll only if nothing was saved since it was sent', () => {
    // A poll in flight when a mode was chosen answered with the old mode and
    // put it back under the cursor.
    expect(pollStillCurrent(3, 3)).toBe(true)
    expect(pollStillCurrent(3, 4)).toBe(false)
  })

  it('sends a policy with exactly the keys the server takes', () => {
    // The server refuses unknown keys, and the custom form once sent the two
    // that belong to the numbers in force rather than to the policy.
    const sent = policyOnly({
      mode: 'custom',
      poolPercent: 60,
      askPercent: 80,
      autoAct: true,
      graceSeconds: 30,
      boostUntil: 123,
      ...({ dynamic: true, boosted: false } as object),
    })
    expect(Object.keys(sent).sort()).toEqual(['askPercent', 'autoAct', 'graceSeconds', 'mode', 'poolPercent'])
  })
})

describe('a custom number being typed', () => {
  it('is a whole number or nothing', () => {
    expect(wholeNumber('88')).toBe(88)
    expect(wholeNumber(' 20 ')).toBe(20)
    // An emptied field is not 0: that is how "015" got typed.
    expect(wholeNumber('')).toBeNull()
    expect(wholeNumber('-3')).toBeNull()
    expect(wholeNumber('60.5')).toBeNull()
    expect(wholeNumber('12345')).toBeNull()
  })
})
