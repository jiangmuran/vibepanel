import { describe, expect, it } from 'vitest'

import {
  PROBE,
  PROBE_GRACE_MS,
  QUIET_MS,
  fresh,
  retryDelay,
  sawBytes,
  shouldRetry,
  stateForClose,
  tick,
  type Liveness,
} from './vnc'

describe('the liveness probe', () => {
  it('is a non-incremental request, which is the only kind a server must answer', () => {
    expect(PROBE[0]).toBe(3) // FramebufferUpdateRequest
    expect(PROBE[1]).toBe(0) // non-incremental
    expect(PROBE.length).toBe(10)
    // One pixel. A full-screen non-incremental request would repaint the whole
    // desktop every five quiet seconds, over somebody's phone connection.
    expect(Array.from(PROBE.slice(6))).toEqual([0, 1, 0, 1])
  })

  it('does not fire while bytes are arriving', () => {
    const l = fresh(0)
    const out = tick(l, QUIET_MS - 1)
    expect(out.probe).toBe(false)
    expect(out.stalled).toBe(false)
  })

  it('fires once after a quiet stretch, not on every tick', () => {
    let l: Liveness = fresh(0)
    const first = tick(l, QUIET_MS)
    expect(first.probe).toBe(true)
    l = first.next

    const second = tick(l, QUIET_MS + 100)
    expect(second.probe, 'a second probe while the first is outstanding').toBe(false)
    expect(second.stalled).toBe(false)
  })

  it('calls the display stalled only after the probe goes unanswered', () => {
    let l: Liveness = fresh(0)
    l = tick(l, QUIET_MS).next

    expect(tick(l, QUIET_MS + PROBE_GRACE_MS - 1).stalled).toBe(false)
    expect(tick(l, QUIET_MS + PROBE_GRACE_MS).stalled).toBe(true)
  })

  it('a quiet desktop that answers the probe is live, not stalled', () => {
    let l: Liveness = fresh(0)
    // Nothing for a while, so a probe goes out.
    l = tick(l, QUIET_MS).next
    // The answer arrives.
    l = sawBytes(l, QUIET_MS + 10)
    // And staying quiet afterwards is a quiet desktop, not a frozen one:
    // it earns a new probe rather than a stalled indicator.
    const out = tick(l, QUIET_MS + 10 + QUIET_MS)
    expect(out.stalled).toBe(false)
    expect(out.probe).toBe(true)
  })

  it('any inbound byte clears an outstanding probe', () => {
    let l: Liveness = fresh(0)
    l = tick(l, QUIET_MS).next
    expect(l.probeAt).toBeGreaterThan(0)
    l = sawBytes(l, QUIET_MS + 1)
    expect(l.probeAt).toBe(0)
  })
})

describe('what a close means', () => {
  it('separates an address the server refuses from a machine that is off', () => {
    // 1008 is what internal/httpapi/vnc.go closes with when the policy refuses
    // the address. It is not going to start working.
    expect(stateForClose(1008)).toBe('refused')
    expect(stateForClose(1014)).toBe('closed')
    expect(stateForClose(1006)).toBe('closed')
  })

  it('only retries the one that can come back', () => {
    expect(shouldRetry('closed')).toBe(true)
    expect(shouldRetry('refused')).toBe(false)
    expect(shouldRetry('live')).toBe(false)
  })

  it('backs off rather than hammering a machine that is switched off', () => {
    expect(retryDelay(1)).toBe(1_000)
    expect(retryDelay(2)).toBe(2_000)
    expect(retryDelay(3)).toBe(4_000)
    expect(retryDelay(99)).toBe(30_000)
    // A first attempt is never negative or zero-delayed into a tight loop.
    expect(retryDelay(0)).toBeGreaterThan(0)
  })
})
