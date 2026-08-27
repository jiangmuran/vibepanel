import { describe, expect, it } from 'vitest'

import { STACKED_TABS, PANEL_TABS } from './chrome'
import {
  STACK_DEFAULT_RATIO,
  STACK_MIN_RATIO,
  clampStackRatio,
  readStackRatio,
  stackRatioAt,
  stackStorageKey,
} from './stack'

describe('the divider inside a stacked tab', () => {
  it('keeps both halves large enough to be panels', () => {
    // Neither half has a tab strip of its own, so a half dragged to nothing is
    // a half with no way back except the grip that is now on an edge.
    expect(clampStackRatio(0)).toBe(STACK_MIN_RATIO)
    expect(clampStackRatio(-3)).toBe(STACK_MIN_RATIO)
    expect(clampStackRatio(1)).toBeCloseTo(1 - STACK_MIN_RATIO, 9)
    expect(clampStackRatio(99)).toBeCloseTo(1 - STACK_MIN_RATIO, 9)
  })

  it('leaves a position somebody dragged to alone', () => {
    expect(clampStackRatio(0.5)).toBe(0.5)
    expect(clampStackRatio(0.8)).toBe(0.8)
  })

  it('resolves a value that is not a number to the default, not to a bound', () => {
    // A bound is a position somebody dragged to. NaN is not a position.
    for (const bad of [Number.NaN, Infinity, -Infinity]) {
      expect(clampStackRatio(bad), String(bad)).toBe(STACK_DEFAULT_RATIO)
    }
  })

  it('opens with both halves visible', () => {
    expect(STACK_DEFAULT_RATIO).toBeGreaterThan(STACK_MIN_RATIO)
    expect(STACK_DEFAULT_RATIO).toBeLessThan(1 - STACK_MIN_RATIO)
  })
})

describe('what came back from the key', () => {
  it('is the default when nothing was ever stored', () => {
    expect(readStackRatio(null)).toBe(STACK_DEFAULT_RATIO)
    expect(readStackRatio('')).toBe(STACK_DEFAULT_RATIO)
    expect(readStackRatio('   ')).toBe(STACK_DEFAULT_RATIO)
  })

  it('does not read an empty key as zero', () => {
    // `Number('')` is 0, and 0 is a legal-looking ratio that collapses the top
    // half completely — the same shape as dropTargetFrom's null check.
    expect(readStackRatio('')).not.toBe(STACK_MIN_RATIO)
  })

  it('refuses whatever is not a number', () => {
    for (const raw of ['half', '{', 'NaN', 'Infinity', '0.5.5']) {
      expect(readStackRatio(raw), raw).toBe(STACK_DEFAULT_RATIO)
    }
  })

  it('clamps a number that is out of range rather than dropping it', () => {
    // Out of range is still a position: somebody's build wrote 0.95 when the
    // floor was lower. The nearest allowed position is what they meant.
    expect(readStackRatio('0.95')).toBeCloseTo(1 - STACK_MIN_RATIO, 9)
    expect(readStackRatio('-1')).toBe(STACK_MIN_RATIO)
  })

  it('round-trips a stored position', () => {
    expect(readStackRatio(String(0.42))).toBeCloseTo(0.42, 9)
  })
})

describe('where a stacked tab remembers its divider', () => {
  it('gives each stacked tab its own key', () => {
    const keys = STACKED_TABS.map(stackStorageKey)
    expect(new Set(keys).size, `two tabs share a key: ${keys.join(', ')}`).toBe(keys.length)
  })

  it('is a localStorage key and nothing else', () => {
    // Nothing about a divider is sent to the server, for the same reason
    // nothing about a pane layout is.
    expect(stackStorageKey('files')).toMatch(/^vibepanel\./)
  })

  it('is only ever asked about a tab that exists', () => {
    for (const tab of STACKED_TABS) expect(PANEL_TABS).toContain(tab)
  })
})

describe('dragging the divider', () => {
  it('reads the pointer as a share of the box it is in', () => {
    expect(stackRatioAt(150, 100, 200)).toBeCloseTo(0.25, 9)
    expect(stackRatioAt(200, 100, 200)).toBeCloseTo(0.5, 9)
  })

  it('clamps rather than letting a drag off the edge collapse a half', () => {
    expect(stackRatioAt(90, 100, 200)).toBe(STACK_MIN_RATIO)
    expect(stackRatioAt(9000, 100, 200)).toBeCloseTo(1 - STACK_MIN_RATIO, 9)
  })

  it('answers nothing for a box that has not been laid out', () => {
    // The frame before the first paint, or a display:none ancestor. Dividing
    // by it writes NaN into the key and takes the arrangement with it.
    expect(stackRatioAt(150, 0, 0)).toBeNull()
    expect(stackRatioAt(150, 0, Number.NaN)).toBeNull()
    expect(stackRatioAt(150, 0, -10)).toBeNull()
  })
})
