import { describe, expect, it } from 'vitest'

import { LoadTimer, formatLoadBytes, loadPercents } from './terminalLoad'

describe('loadPercents', () => {
  it('fills the bar with what has been parsed, not what has arrived', () => {
    // The failure this replaces: everything arrived, a fifth parsed, and the
    // bar said 68%.
    expect(loadPercents({ total: 1000, received: 1000, parsed: 200 })).toEqual({ parsed: 20, received: 100 })
  })

  it('never reads 100 while the overlay is still up', () => {
    expect(loadPercents({ total: 1000, received: 1000, parsed: 1000 }).parsed).toBe(99)
  })

  it('treats an empty snapshot as all but done', () => {
    expect(loadPercents({ total: 0, received: 0, parsed: 0 })).toEqual({ parsed: 99, received: 100 })
  })
})

describe('formatLoadBytes', () => {
  it('uses MiB for a full ring and KiB below one', () => {
    expect(formatLoadBytes(1 << 20, 2 << 20)).toBe('1.0 / 2.0 MiB')
    expect(formatLoadBytes(10 * 1024, 300 * 1024)).toBe('10 / 300 KiB')
  })
})

describe('LoadTimer', () => {
  it('records each mark once, relative to the start', () => {
    let t = 1000
    const timer = new LoadTimer(() => t)
    timer.begin(false)
    t = 1100
    timer.markSubscribed()
    t = 1150
    timer.markByte(false)
    t = 1300
    timer.markByte(false)
    t = 1900
    timer.markByte(true)
    t = 2400
    expect(timer.finish(2048, false)).toEqual({
      bytes: 2048,
      subscribedMs: 100,
      firstByteMs: 150,
      receivedMs: 900,
      readyMs: 1400,
      reconnect: false,
      resumed: false,
      hidden: false,
    })
  })

  it('starts clean on a reconnect, and says a mark never happened rather than inventing one', () => {
    let t = 0
    const timer = new LoadTimer(() => t)
    timer.begin(false)
    timer.markSubscribed()
    t = 5000
    timer.begin(true)
    t = 5040
    const r = timer.finish(0, true)
    expect(r).toMatchObject({ subscribedMs: -1, firstByteMs: -1, receivedMs: -1, readyMs: 40, reconnect: true })
  })
})
