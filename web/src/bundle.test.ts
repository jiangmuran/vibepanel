import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * What the panel's first paint carries, and what it does not.
 *
 * The file viewer is its own chunk, fetched the first time a file is opened
 * (viewer/lazy.tsx), so that it -- and the tokenisers later batches bring with
 * it -- costs nothing to somebody who never opens a file. One static import of
 * it anywhere in the main graph puts all of it back into the first paint, and
 * nothing else would notice: every check still passes, just slower.
 *
 * Read from the committed bundle, which `head-check` keeps equal to the
 * source. A number raised here is a decision about the first paint; raise it
 * in the same commit as the thing that grew it, and say why.
 */
const DIST = new URL('../../internal/webui/dist/assets/', import.meta.url).pathname

function chunk(prefix: string): { name: string; bytes: number; text: string } | null {
  const name = readdirSync(DIST).find((f) => f.startsWith(prefix) && f.endsWith('.js'))
  if (!name) return null
  const path = join(DIST, name)
  return { name, bytes: statSync(path).size, text: readFileSync(path, 'utf8') }
}

describe('the bundle', () => {
  const main = chunk('index-')
  const viewer = chunk('FileViewer-')

  it('has the viewer as a chunk of its own', () => {
    expect(main, 'no main chunk in the committed bundle').not.toBeNull()
    expect(viewer, 'the viewer is not a separate chunk; something imports it statically').not.toBeNull()
  })

  it('keeps the viewer out of the first paint', () => {
    // A string only the viewer's find bar draws.
    expect(main!.text).not.toContain('viewer-findbar')
    expect(viewer!.text).toContain('viewer-findbar')
  })

  it('stays inside its sizes', () => {
    expect(main!.bytes, `${main!.name}`).toBeLessThanOrEqual(1_400_000)
    expect(viewer!.bytes, `${viewer!.name}`).toBeLessThanOrEqual(48_000)
  })
})
