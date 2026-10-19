import { afterEach, describe, expect, it, vi } from 'vitest'

import { RENDERER_KEY, rendererPreference, setRendererPreference } from './renderer'

function memoryStorage() {
  const m = new Map<string, string>()
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
    raw: m,
  }
}

afterEach(() => vi.unstubAllGlobals())

describe('the renderer switch', () => {
  it('is what the next terminal reads', () => {
    // The settings page writing a key the terminal does not read is the
    // failure this replaced: a comment promising a switch nobody could reach.
    const s = memoryStorage()
    vi.stubGlobal('localStorage', s)
    expect(rendererPreference()).toBe('auto')
    setRendererPreference('dom')
    expect(s.raw.get(RENDERER_KEY)).toBe('dom')
    expect(rendererPreference()).toBe('dom')
    setRendererPreference('auto')
    expect(s.raw.has(RENDERER_KEY)).toBe(false)
    expect(rendererPreference()).toBe('auto')
  })

  it('stays on auto where storage refuses', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('denied')
      },
      setItem: () => {
        throw new Error('denied')
      },
      removeItem: () => {
        throw new Error('denied')
      },
    })
    expect(() => setRendererPreference('dom')).not.toThrow()
    expect(rendererPreference()).toBe('auto')
  })
})
