import { describe, expect, it } from 'vitest'

import { afterRefusal, initialElevate } from './updateElevate'

// The page's half of the elevated upgrade. The server's half -- what sudo said
// and what it means -- is tested against recordings and against real sudo in
// internal/httpapi/elevate_test.go; this is what the page does with the answer.
describe('the elevated update form', () => {
  it('asks for nothing when sudo needs nothing', () => {
    expect(initialElevate(true, 'jmr')).toEqual({ field: false, who: 'jmr', blocked: false })
    expect(initialElevate(false, 'jmr')).toEqual({ field: true, who: 'jmr', blocked: false })
    // An older panel does not send the flag; asking is the safe default.
    expect(initialElevate(undefined, 'jmr').field).toBe(true)
  })

  it('brings the field up when sudo turns out to want a password', () => {
    const next = afterRefusal(initialElevate(true, 'jmr'), 'needPassword', '', 'jmr')
    expect(next.state.field).toBe(true)
    expect(next.key).toBe('upd.needPassword')
  })

  it('names whose password sudo wanted, which may be root', () => {
    const next = afterRefusal(initialElevate(false, 'jmr'), 'wrongPassword', 'root', 'jmr')
    expect(next.state).toEqual({ field: true, who: 'root', blocked: false })
    expect(next.params).toEqual({ who: 'root' })
    // sudo-rs 0.2.13 does not always say whose; the account the check named stands.
    expect(afterRefusal(initialElevate(false, 'jmr'), 'wrongPassword', '', 'jmr').params).toEqual({ who: 'jmr' })
  })

  it('stops offering anything when typing cannot help', () => {
    for (const reason of ['notAllowed', 'needsTty', 'cannotElevate'] as const) {
      const next = afterRefusal(initialElevate(false, 'jmr'), reason, '', 'jmr')
      expect(next.state.blocked).toBe(true)
    }
    expect(afterRefusal(initialElevate(false, 'jmr'), 'notAllowed', '', 'jmr').params).toEqual({ user: 'jmr' })
  })

  it('leaves the form alone for a failure it has no sentence for', () => {
    const start = initialElevate(false, 'jmr')
    const next = afterRefusal(start, 'failed', '', 'jmr')
    expect(next.state).toBe(start)
    expect(next.key).toBe('upd.failed')
  })
})
