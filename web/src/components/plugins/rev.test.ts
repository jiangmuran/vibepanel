import { afterEach, describe, expect, it } from 'vitest'

import { notePluginsRev, onPluginsRev, resetPluginsRev } from './rev'

describe('the plugins revision', () => {
  afterEach(resetPluginsRev)

  it('treats the first revision as a baseline, fires on a change, and not on a repeat', () => {
    let fired = 0
    onPluginsRev(() => fired++)
    notePluginsRev(3, null)
    expect(fired).toBe(0)
    notePluginsRev(3, null)
    expect(fired).toBe(0)
    notePluginsRev(4, null)
    expect(fired).toBe(1)
    notePluginsRev(4, null)
    expect(fired).toBe(1)
  })

  it('re-requests the theme sheet through the link already in the page', () => {
    // vitest runs in node here; the holder is the one method the swap uses.
    let href = '/plugin-themes.css'
    const asked: string[] = []
    const doc = {
      querySelector(selector: string) {
        asked.push(selector)
        return { setAttribute: (_: string, v: string) => (href = v) }
      },
    }
    notePluginsRev(1, doc)
    expect(href).toBe('/plugin-themes.css')
    notePluginsRev(2, doc)
    expect(href).toBe('/plugin-themes.css?v=2')
    expect(asked[0]).toContain('plugin-themes.css')
  })

  it('ignores what is not a number and lets one listener throw without stopping the rest', () => {
    const seen: string[] = []
    onPluginsRev(() => {
      throw new Error('x')
    })
    onPluginsRev(() => seen.push('ok'))
    notePluginsRev(1, null)
    notePluginsRev(Number.NaN, null)
    notePluginsRev(2, null)
    expect(seen).toEqual(['ok'])
  })
})
