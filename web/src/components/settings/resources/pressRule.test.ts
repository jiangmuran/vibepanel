import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONFIRM_WINDOW_MS, ConfirmPress, pressStep } from './pressRule'
import type { PressPhase } from './pressRule'

describe('the press-twice rule', () => {
  it('arms on the first press and acts only on the second', () => {
    expect(pressStep('idle', 'press')).toEqual({ phase: 'armed', run: false })
    expect(pressStep('armed', 'press')).toEqual({ phase: 'running', run: true })
  })

  it('goes back to idle on a timeout or a cancel, and does nothing', () => {
    expect(pressStep('armed', 'timeout')).toEqual({ phase: 'idle', run: false })
    expect(pressStep('armed', 'cancel')).toEqual({ phase: 'idle', run: false })
  })

  it('ignores everything while it runs except its end', () => {
    for (const e of ['press', 'timeout', 'cancel'] as const) {
      expect(pressStep('running', e)).toEqual({ phase: 'running', run: false })
    }
    expect(pressStep('running', 'settled')).toEqual({ phase: 'idle', run: false })
  })

  it('does not act from idle on anything but a press', () => {
    for (const e of ['timeout', 'cancel', 'settled'] as const) {
      expect(pressStep('idle', e)).toEqual({ phase: 'idle', run: false })
    }
  })
})

describe('a button pressed twice', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  const make = (act: () => Promise<unknown>) => {
    const seen: PressPhase[] = []
    const c = new ConfirmPress(act, (p) => seen.push(p))
    return { c, seen }
  }

  it('runs the action on the second press inside the window', async () => {
    const act = vi.fn(() => Promise.resolve())
    const { c, seen } = make(act)
    c.press()
    expect(act).not.toHaveBeenCalled()
    vi.advanceTimersByTime(CONFIRM_WINDOW_MS - 1)
    c.press()
    expect(act).toHaveBeenCalledTimes(1)
    await vi.runAllTimersAsync()
    expect(seen).toEqual(['armed', 'running', 'idle'])
  })

  it('does not run when the window has passed, and the next press arms again', () => {
    const act = vi.fn(() => Promise.resolve())
    const { c } = make(act)
    c.press()
    vi.advanceTimersByTime(CONFIRM_WINDOW_MS)
    expect(c.phase).toBe('idle')
    c.press()
    expect(act).not.toHaveBeenCalled()
    expect(c.phase).toBe('armed')
  })

  it('is disarmed by a cancel, as Escape and a blur do', () => {
    const act = vi.fn(() => Promise.resolve())
    const { c } = make(act)
    c.press()
    c.cancel()
    c.press()
    expect(act).not.toHaveBeenCalled()
    expect(c.phase).toBe('armed')
  })

  it('sends one request however many times it is pressed while it runs', async () => {
    let finish: () => void = () => undefined
    const act = vi.fn(() => new Promise<void>((r) => (finish = r)))
    const { c } = make(act)
    c.press()
    c.press()
    c.press()
    c.press()
    expect(act).toHaveBeenCalledTimes(1)
    expect(c.phase).toBe('running')
    c.cancel()
    expect(c.phase).toBe('running')
    finish()
    await vi.runAllTimersAsync()
    expect(c.phase).toBe('idle')
  })

  it('is usable again after the action fails', async () => {
    const act = vi.fn(() => Promise.reject(new Error('no')))
    const { c } = make(act)
    c.press()
    c.press()
    await vi.runAllTimersAsync()
    expect(c.phase).toBe('idle')
  })

  it('does not call back after it is disposed', () => {
    const act = vi.fn(() => Promise.resolve())
    const { c, seen } = make(act)
    c.press()
    c.dispose()
    vi.advanceTimersByTime(CONFIRM_WINDOW_MS * 2)
    c.press()
    expect(seen).toEqual(['armed'])
    expect(act).not.toHaveBeenCalled()
  })
})
