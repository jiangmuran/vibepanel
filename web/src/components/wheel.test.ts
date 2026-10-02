import { describe, expect, it } from 'vitest'

import { MAX_REPORTS_PER_EVENT, wheelReports } from './wheel'

describe('wheelReports', () => {
  it('sends one report per row of travel, not one per event', () => {
    // A mouse notch of 100px over 15px rows is six rows and a bit.
    const plan = wheelReports(100, 0, 15, 33, 0)
    expect(plan.count).toBe(6)
    expect(plan.up).toBe(false)
    expect(plan.carry).toBeCloseTo(100 / 15 - 6)
  })

  it('reads direction from the sign', () => {
    expect(wheelReports(-100, 0, 15, 33, 0).up).toBe(true)
    expect(wheelReports(100, 0, 15, 33, 0).up).toBe(false)
  })

  it('carries trackpad fractions across events until they make a row', () => {
    // Forty events of 8px: 320px over 15px rows is 21 rows, not 40 and not 0.
    let carry = 0
    let total = 0
    for (let i = 0; i < 40; i++) {
      const plan = wheelReports(-8, 0, 15, 33, carry)
      total += plan.count
      carry = plan.carry
    }
    expect(total).toBe(21)
  })

  it('drops the carry on a change of direction', () => {
    const down = wheelReports(14, 0, 15, 33, 0)
    expect(down.count).toBe(0)
    expect(down.carry).toBeCloseTo(14 / 15)
    // Back up by one row exactly: the 14px owed downward must not count against it.
    const up = wheelReports(-15, 0, 15, 33, down.carry)
    expect(up.count).toBe(1)
    expect(up.up).toBe(true)
  })

  it('takes line and page deltas at their word', () => {
    expect(wheelReports(3, 1, 15, 33, 0).count).toBe(3)
    expect(wheelReports(1, 2, 15, 33, 0).count).toBe(33)
  })

  it('bounds one event', () => {
    expect(wheelReports(100000, 0, 15, 33, 0).count).toBe(MAX_REPORTS_PER_EVENT)
  })

  it('does nothing with a grid it cannot measure', () => {
    expect(wheelReports(100, 0, 0, 33, 0.5)).toEqual({ count: 0, up: false, carry: 0.5 })
    expect(wheelReports(100, 0, 15, 0, 0)).toMatchObject({ count: 0 })
  })
})
