import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from './api'

/**
 * What the preview call makes of a text answer's headers. A header is a
 * string anything on the way could have set, and the viewer's footer draws
 * the encoding, so only the names browse.Decode gives come through.
 */
const answer = (headers: Record<string, string>) =>
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response('text', { status: 200, headers: { 'X-Preview-Kind': 'text', ...headers } })),
  )

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('api.preview, text', () => {
  it('carries the encoding the server decoded from', async () => {
    for (const enc of ['UTF-16 LE', 'UTF-16 BE', 'Latin-1']) {
      answer({ 'X-Preview-Encoding': enc })
      const p = await api.preview('p', 'f.txt')
      expect(p.kind === 'text' && p.encoding, enc).toBe(enc)
    }
  })

  it('is null for UTF-8, and for a name the server does not give', async () => {
    answer({})
    let p = await api.preview('p', 'f.txt')
    expect(p.kind === 'text' && p.encoding).toBeNull()
    answer({ 'X-Preview-Encoding': 'Shift_JIS' })
    p = await api.preview('p', 'f.txt')
    expect(p.kind === 'text' && p.encoding).toBeNull()
  })
})
