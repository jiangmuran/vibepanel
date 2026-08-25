import { describe, expect, it } from 'vitest'

import { meterText, meterWidth } from './meter'

describe('meterText', () => {
  it('says nothing is known rather than saying zero', () => {
    // The CPU figure is a difference between two samples, so the first one has
    // nothing to compare against. It rendered as "0%" — a measurement nobody
    // had taken, claiming the machine was idle — beside a detail line that
    // said "sampling…". The two disagreed and the number is what people read.
    expect(meterText(null)).toBe('—')
    expect(meterWidth(null)).toBe(0)
  })

  it('is a real zero when the value is really zero', () => {
    expect(meterText(0)).toBe('0%')
  })

  it('rounds and clamps', () => {
    expect(meterText(26.4)).toBe('26%')
    expect(meterText(99.6)).toBe('100%')
    expect(meterText(-5)).toBe('0%')
    expect(meterText(150)).toBe('100%')
    expect(meterWidth(150)).toBe(100)
  })
})
