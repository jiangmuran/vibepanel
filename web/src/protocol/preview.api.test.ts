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

describe('api.preview, the listed kinds', () => {
  it('answers an archive and a database with their kind and nothing else', async () => {
    for (const kind of ['archive', 'sqlite'] as const) {
      answer({ 'X-Preview-Kind': kind })
      expect(await api.preview('p', 'f')).toEqual({ kind })
    }
  })
})

/** One answer from a listing route, and the URL it was asked at. */
const listed = (status: number, body: unknown) => {
  const f = vi.fn(async (_url: string) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', f)
  return f
}

describe('api.archiveEntries', () => {
  it('asks the archive-entries route for the path, and reads the listing', async () => {
    const f = listed(200, { format: 'zip', entries: [{ path: 'a', size: 1, modified: 0, isDir: false }], truncated: false })
    const l = await api.archiveEntries('p1', 'dir/a b.zip')
    expect(f.mock.calls[0][0]).toBe('/api/projects/p1/archive-entries?path=dir%2Fa%20b.zip')
    expect(l.entries[0].path).toBe('a')
  })

  it('carries the server sentence for a damaged archive inside its own', async () => {
    listed(422, { error: 'zip: not a valid zip file' })
    const e = await api.archiveEntries('p', 'a.zip').catch((e: Error) => e)
    expect((e as Error).message).toContain('zip: not a valid zip file')
    // Not the server's English alone: a sentence of ours around it.
    expect((e as Error).message).not.toBe('zip: not a valid zip file')
  })

  it('refuses an answer that is not a listing', async () => {
    listed(200, { format: 'rar', entries: [] })
    await expect(api.archiveEntries('p', 'a.zip')).rejects.toThrow()
  })
})

describe('api.sqlite', () => {
  const body = { tables: [{ name: 'a b', kind: 'table', rows: 1, columns: [], sql: '' }], table: 'a b', columns: [], rows: [], more: false }

  it('asks for the first table without a table parameter, and for one by name', async () => {
    const f = listed(200, body)
    await api.sqlite('p1', 'x.db')
    expect(f.mock.calls[0][0]).toBe('/api/projects/p1/sqlite?path=x.db')
    await api.sqlite('p1', 'x.db', 'a&b')
    expect(f.mock.calls[1][0]).toBe('/api/projects/p1/sqlite?path=x.db&table=a%26b')
  })

  it('says which table is not there, and what could not be read', async () => {
    listed(400, { error: 'no such table' })
    await expect(api.sqlite('p', 'x.db', 'gone')).rejects.toThrow(/gone/)
    listed(422, { error: 'the database is damaged: malformed' })
    const e = await api.sqlite('p', 'x.db').catch((e: Error) => e)
    expect((e as Error).message).toContain('the database is damaged: malformed')
    expect((e as Error).message).not.toBe('the database is damaged: malformed')
  })
})
