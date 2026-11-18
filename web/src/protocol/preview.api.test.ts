import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, FileChangedError } from './api'

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

describe('api.preview, the version a save is against', () => {
  const sha = `"${'ab'.repeat(32)}"`

  it('carries the ETag of a text sent whole, and only one of that shape', async () => {
    answer({ ETag: sha })
    let p = await api.preview('p', 'f.txt')
    expect(p.kind === 'text' && p.etag).toBe(sha)
    for (const bad of ['W/' + sha, 'abc', '"' + 'ab'.repeat(31) + '"', '']) {
      answer({ ETag: bad })
      p = await api.preview('p', 'f.txt')
      expect(p.kind === 'text' && p.etag, bad).toBeNull()
    }
  })

  it('sees a byte order mark the decode drops', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(new Uint8Array([0xef, 0xbb, 0xbf, 0x68, 0x69]), { status: 200, headers: { 'X-Preview-Kind': 'text' } })),
    )
    const p = await api.preview('p', 'f.txt')
    expect(p.kind === 'text' && p.text).toBe('hi')
    expect(p.kind === 'text' && p.bom).toBe(true)
    answer({})
    const q = await api.preview('p', 'f.txt')
    expect(q.kind === 'text' && q.bom).toBe(false)
  })
})

describe('api.saveFile', () => {
  const sha = (c: string) => `"${c.repeat(64)}"`
  const reply = (status: number, body: unknown = {}) => {
    const f = vi.fn(async (_url: string, _init?: RequestInit) =>
      new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }),
    )
    vi.stubGlobal('fetch', f)
    return f
  }

  it('puts the text against the version it was read at', async () => {
    const f = reply(200, { etag: sha('b'), size: 5 })
    const r = await api.saveFile('p1', 'a dir/f.txt', 'hello', sha('a'))
    expect(r).toEqual({ etag: sha('b'), size: 5 })
    const [url, init] = f.mock.calls[0]
    expect(url).toBe('/api/projects/p1/file?path=a%20dir%2Ff.txt')
    expect(init?.method).toBe('PUT')
    expect(new Headers(init?.headers).get('If-Match')).toBe(sha('a'))
    expect(init?.body).toBe('hello')
  })

  it('is FileChangedError on 412', async () => {
    reply(412, { error: 'the file was changed after you opened it' })
    await expect(api.saveFile('p', 'f', 'x', sha('a'))).rejects.toBeInstanceOf(FileChangedError)
  })

  it('says every other refusal in the viewer\'s words', async () => {
    for (const status of [400, 403, 404, 413, 415]) {
      reply(status, { error: 'an English sentence' })
      const e = await api.saveFile('p', 'f', 'x', sha('a')).catch((x: unknown) => x)
      expect(e, String(status)).toBeInstanceOf(Error)
      expect(e).not.toBeInstanceOf(FileChangedError)
      expect((e as Error).message, String(status)).not.toContain('English')
    }
  })

  it('refuses an answer without a version it can save against next', async () => {
    reply(200, { etag: 'nonsense', size: 1 })
    await expect(api.saveFile('p', 'f', 'x', sha('a'))).rejects.toThrow()
  })
})
