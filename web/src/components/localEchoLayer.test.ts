import { afterEach, describe, expect, it, vi } from 'vitest'

import { LOCAL_ECHO_KEY, localEchoOn, setLocalEcho } from './localEchoLayer'

function memoryStorage(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init))
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
    raw: m,
  }
}

afterEach(() => vi.unstubAllGlobals())

describe('the local echo switch', () => {
  it('writes what the terminal reads on its next keystroke', () => {
    const s = memoryStorage()
    vi.stubGlobal('localStorage', s)
    expect(localEchoOn()).toBe(true)
    setLocalEcho(false)
    expect(s.raw.get(LOCAL_ECHO_KEY)).toBe('off')
    expect(localEchoOn()).toBe(false)
    setLocalEcho(true)
    expect(s.raw.has(LOCAL_ECHO_KEY)).toBe(false)
    expect(localEchoOn()).toBe(true)
  })

  it('leaves the debug value alone when turned on', () => {
    // 'debug' is on, with logging; the settings saying "on" must not quietly
    // take the logging away from somebody chasing a reason.
    const s = memoryStorage({ [LOCAL_ECHO_KEY]: 'debug' })
    vi.stubGlobal('localStorage', s)
    expect(localEchoOn()).toBe(true)
    setLocalEcho(true)
    expect(s.raw.get(LOCAL_ECHO_KEY)).toBe('debug')
  })

  it('stays on where storage refuses', () => {
    const no = () => {
      throw new Error('denied')
    }
    vi.stubGlobal('localStorage', { getItem: no, setItem: no, removeItem: no })
    expect(() => setLocalEcho(false)).not.toThrow()
    expect(localEchoOn()).toBe(true)
  })
})
