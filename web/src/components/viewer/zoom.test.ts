import { describe, expect, it } from 'vitest'

import { MAX_ZOOM, MIN_ZOOM, clampZoom, percent, zoomAbout } from './zoom'

describe('zoom', () => {
  it('steps in and back out to where it started', () => {
    const z = zoomAbout(zoomAbout(1, 1), -1)
    expect(z).toBeCloseTo(1, 10)
  })

  it('takes several steps for a fast wheel', () => {
    expect(zoomAbout(1, 1, 2)).toBeCloseTo(1.5625, 10)
  })

  it('stops at both ends', () => {
    expect(zoomAbout(MAX_ZOOM, 1)).toBe(MAX_ZOOM)
    expect(zoomAbout(MIN_ZOOM, -1)).toBe(MIN_ZOOM)
    expect(clampZoom(1000)).toBe(MAX_ZOOM)
    expect(clampZoom(0)).toBe(MIN_ZOOM)
    expect(clampZoom(Number.NaN)).toBe(1)
  })

  it('says a scale as a percentage', () => {
    expect(percent(1)).toBe('100%')
    expect(percent(0.25)).toBe('25%')
  })
})
