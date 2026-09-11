import { describe, expect, it } from 'vitest'

import { TerminalReplay, type ReplayTerminal } from './terminalReplay'

class FakeTerminal implements ReplayTerminal {
  writes: Uint8Array[] = []
  callbacks: Array<() => void> = []
  resets = 0

  write(data: Uint8Array, callback: () => void): void {
    this.writes.push(data)
    this.callbacks.push(callback)
  }

  reset(): void {
    this.resets++
  }

  finish(): void {
    this.callbacks.shift()?.()
  }
}

function scheduler() {
  const pending: Array<() => void> = []
  return {
    schedule: (callback: () => void) => {
      pending.push(callback)
      return pending.length
    },
    cancel: () => {},
    flush: () => pending.shift()?.(),
  }
}

describe('TerminalReplay', () => {
  it('paints replay chunks one frame apart and keeps live bytes ordered', () => {
    const term = new FakeTerminal()
    const clock = scheduler()
    const queue = new TerminalReplay(term, clock.schedule, clock.cancel)

    queue.enqueue(new Uint8Array([1]), true)
    queue.enqueue(new Uint8Array([2]), true)
    queue.enqueue(new Uint8Array([3]), false)
    expect([...term.writes[0]]).toEqual([1])
    expect(queue.replaying).toBe(true)

    term.finish()
    expect(term.writes).toHaveLength(1)
    clock.flush()
    expect([...term.writes[1]]).toEqual([2])
    term.finish()
    expect([...term.writes[2]]).toEqual([3])
    term.finish()
    expect(queue.replaying).toBe(false)
  })

  it('resets once before the next replay and drops queued old bytes', () => {
    const term = new FakeTerminal()
    const clock = scheduler()
    const queue = new TerminalReplay(term, clock.schedule, clock.cancel)

    queue.enqueue(new Uint8Array([1]), true)
    queue.enqueue(new Uint8Array([2]), true)
    queue.restart()
    queue.enqueue(new Uint8Array([3]), true)
    expect([...term.writes[0]]).toEqual([1])
    term.finish()
    clock.flush()
    expect(term.resets).toBe(1)
    expect([...term.writes[1]]).toEqual([3])
  })

  it('does not write after disposal, including a late callback', () => {
    const term = new FakeTerminal()
    const clock = scheduler()
    const queue = new TerminalReplay(term, clock.schedule, clock.cancel)

    queue.enqueue(new Uint8Array([1]), true)
    queue.enqueue(new Uint8Array([2]), true)
    queue.dispose()
    term.finish()
    clock.flush()
    expect(term.writes).toHaveLength(1)
  })
})
