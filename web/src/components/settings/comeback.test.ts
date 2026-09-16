import { describe, expect, it } from 'vitest'

import { COMEBACK_EVERY_MS, COMEBACK_FIRST_MS, COMEBACK_TRIES, waitForItToComeBack } from './comeback'

// A fake clock: timers are queued and run by hand, in order.
function clock() {
  const queue: { fn: () => void; ms: number }[] = []
  return {
    setTimeout: (fn: () => void, ms: number) => {
      queue.push({ fn, ms })
    },
    async run(n: number) {
      for (let i = 0; i < n; i++) {
        const next = queue.shift()
        if (!next) return
        next.fn()
        // Let the fetch promise settle before the next timer.
        await Promise.resolve()
        await Promise.resolve()
      }
    },
    delays: () => queue.map((q) => q.ms),
  }
}

describe('waiting for the panel to come back', () => {
  it('does not believe the old process answering, and waits for a gap', async () => {
    const c = clock()
    const answers: (null | { version: string; commit: string })[] = [
      { version: 'v1', commit: 'a' }, // still the old one
      { version: 'v1', commit: 'a' },
      null, // gone
      null,
      { version: 'v1', commit: 'a' }, // back, same build (a restart, not an upgrade)
    ]
    let back = 0
    let gaveUp = 0
    waitForItToComeBack({
      was: 'v1@a',
      onBack: () => back++,
      onGaveUp: () => gaveUp++,
      fetchHealth: () => Promise.resolve(answers.shift() ?? null),
      setTimeout: c.setTimeout,
    })
    expect(c.delays()).toEqual([COMEBACK_FIRST_MS])
    await c.run(4)
    expect(back).toBe(0)
    expect(c.delays()).toEqual([COMEBACK_EVERY_MS])
    await c.run(1)
    expect(back).toBe(1)
    expect(gaveUp).toBe(0)
    expect(c.delays()).toEqual([])
  })

  it('takes a different build as back even without seeing the gap', async () => {
    const c = clock()
    let back = 0
    waitForItToComeBack({
      was: 'v1@a',
      onBack: () => back++,
      onGaveUp: () => {},
      fetchHealth: () => Promise.resolve({ version: 'v2', commit: 'b' }),
      setTimeout: c.setTimeout,
    })
    await c.run(1)
    expect(back).toBe(1)
  })

  it('gives up rather than polling forever', async () => {
    const c = clock()
    let gaveUp = 0
    waitForItToComeBack({
      was: null,
      onBack: () => {},
      onGaveUp: () => gaveUp++,
      fetchHealth: () => Promise.resolve({ version: 'v1', commit: 'a' }),
      setTimeout: c.setTimeout,
    })
    await c.run(COMEBACK_TRIES + 5)
    expect(gaveUp).toBe(1)
    expect(c.delays()).toEqual([])
  })
})
