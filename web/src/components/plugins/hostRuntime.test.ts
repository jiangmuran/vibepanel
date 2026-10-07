import { afterEach, describe, expect, it } from 'vitest'

import { HOST_VERSION, hostFor, publishHostState, resetHost, safeMode } from './hostRuntime'
import type { PanelState } from '../../protocol/wire'

/**
 * The host object a rung-4 module is given (docs/plugins.md §7), without a
 * DOM: the parts that are a contract -- the version, the state subscription,
 * the prefixed dictionary -- and the one switch that must work before any
 * module has loaded.
 */
afterEach(resetHost)

describe('safe mode', () => {
  it('is ?safe=1 on the address and nothing else', () => {
    expect(safeMode('?safe=1')).toBe(true)
    expect(safeMode('?page=x&safe=1')).toBe(true)
    expect(safeMode('?safe=1&x=y')).toBe(true)
    expect(safeMode('?safe=10')).toBe(false)
    expect(safeMode('?unsafe=1')).toBe(false)
    expect(safeMode('')).toBe(false)
  })
})

describe('the host object', () => {
  it('carries the contract version and the plugin it was made for', () => {
    const h = hostFor('standup', 'Stand-up')
    expect(h.v).toBe(HOST_VERSION)
    expect(h.plugin).toBe('standup')
  })

  it('hands a subscriber the current state at once and every state after', () => {
    const h = hostFor('standup', 'Stand-up')
    const seen: number[] = []
    const state = (n: number) => ({ sessions: new Array(n), projects: [] }) as unknown as PanelState
    publishHostState(state(1))
    const off = h.state.subscribe((s) => seen.push(s.sessions.length))
    publishHostState(state(2))
    off()
    publishHostState(state(3))
    expect(seen).toEqual([1, 2])
    expect(h.state.current?.sessions.length).toBe(3)
  })

  it('prefixes dictionary keys with the plugin id and refuses a collision', () => {
    const h = hostFor('standup', 'Stand-up')
    h.i18n.add({ title: { zh: '站会', en: 'Stand-up' } })
    expect(h.i18n.t('title')).toBe('Stand-up')
    expect(h.i18n.t('standup.title')).toBe('Stand-up')
    // The panel's own keys are not reachable through a plugin's prefix, and
    // a plugin cannot redefine one of its own either.
    expect(() => h.i18n.add({ title: { zh: 'x', en: 'y' } })).toThrow(/taken/)
    expect(h.i18n.t('nope')).toBe('standup.nope')
  })

  it('does not let a throwing subscriber stop the others', () => {
    const h = hostFor('a', 'A')
    const g = hostFor('b', 'B')
    let got = 0
    h.state.subscribe(() => {
      throw new Error('boom')
    })
    g.state.subscribe(() => {
      got++
    })
    publishHostState({ sessions: [], projects: [] } as unknown as PanelState)
    expect(got).toBe(1)
  })
})
