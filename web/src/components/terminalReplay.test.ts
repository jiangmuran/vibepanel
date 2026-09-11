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

/**
 * requestAnimationFrame, run by hand.
 *
 * Cancelling really removes the callback. The first version of this ignored
 * cancel, so a frame that should never have run ran anyway, and neither of the
 * two cancels in TerminalReplay could be seen to do anything: both could be
 * deleted with every test still green.
 */
function frames() {
  let next = 1
  const pending = new Map<number, () => void>()
  return {
    schedule: (callback: () => void) => {
      const id = next++
      pending.set(id, callback)
      return id
    },
    cancel: (id: number) => {
      pending.delete(id)
    },
    flush: () => {
      const first = pending.entries().next()
      if (first.done) return
      const [id, callback] = first.value
      pending.delete(id)
      callback()
    },
    pending: () => pending.size,
  }
}

function setup() {
  const term = new FakeTerminal()
  const clock = frames()
  const queue = new TerminalReplay(term, clock.schedule, clock.cancel)
  return { term, clock, queue }
}

const bytes = (n: number) => new Uint8Array([n])

describe('TerminalReplay', () => {
  it('paints replay chunks one frame apart and keeps live bytes ordered', () => {
    const { term, clock, queue } = setup()

    queue.enqueue(bytes(1), true)
    queue.enqueue(bytes(2), true)
    queue.enqueue(bytes(3), false)
    expect([...term.writes[0]]).toEqual([1])
    expect(queue.replaying).toBe(true)

    term.finish()
    expect(term.writes).toHaveLength(1)
    // Between two chunks nothing is parsing, and the snapshot is not over.
    expect(queue.replaying).toBe(true)
    clock.flush()
    expect([...term.writes[1]]).toEqual([2])
    term.finish()
    expect([...term.writes[2]]).toEqual([3])
    term.finish()
    expect(queue.replaying).toBe(false)
  })

  it('resets once before the next replay and drops queued old bytes', () => {
    const { term, clock, queue } = setup()

    queue.enqueue(bytes(1), true)
    queue.enqueue(bytes(2), true)
    queue.restart()
    queue.enqueue(bytes(3), true)
    queue.enqueue(bytes(4), true)
    expect([...term.writes[0]]).toEqual([1])
    term.finish()
    clock.flush()
    expect(term.resets).toBe(1)
    expect([...term.writes[1]]).toEqual([3])

    // Once per snapshot, not once per chunk: a reset before the second chunk
    // would wipe the first one off the screen.
    term.finish()
    clock.flush()
    expect([...term.writes[2]]).toEqual([4])
    expect(term.resets).toBe(1)
  })

  it('starts a new snapshot at once when the restart lands between two frames', () => {
    const { term, clock, queue } = setup()

    queue.enqueue(bytes(1), true)
    queue.enqueue(bytes(2), true)
    term.finish()
    expect(clock.pending()).toBe(1)

    queue.restart()
    queue.enqueue(bytes(3), true)
    expect(term.writes.map((w) => [...w])).toEqual([[1], [3]])
    expect(term.resets).toBe(1)
    expect(clock.pending()).toBe(0)
  })

  it('leaves nothing scheduled when disposed between two frames', () => {
    const { term, clock, queue } = setup()

    queue.enqueue(bytes(1), true)
    queue.enqueue(bytes(2), true)
    term.finish()
    queue.dispose()
    expect(clock.pending()).toBe(0)
    expect(term.writes).toHaveLength(1)
  })

  it('schedules nothing from a parse that finishes after disposal', () => {
    const { term, clock, queue } = setup()

    queue.enqueue(bytes(1), true)
    queue.enqueue(bytes(2), true)
    queue.dispose()
    term.finish()
    expect(clock.pending()).toBe(0)
    expect(term.writes).toHaveLength(1)
  })

  it('writes nothing that arrives after disposal', () => {
    const { term, queue } = setup()

    queue.dispose()
    queue.enqueue(bytes(1), false)
    queue.enqueue(bytes(2), true)
    expect(term.writes).toHaveLength(0)
  })
})
