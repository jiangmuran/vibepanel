import { readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * Two modules in one directory whose names differ only in case.
 *
 * `components/Toasts.tsx` sat beside `components/toasts.ts`, and on a Mac's
 * default disk, which is case-insensitive, `make build` stopped at `tsc -b`.
 * tsc resolves `./Toasts` by trying `Toasts.ts` before `Toasts.tsx`, the disk
 * answers yes because `toasts.ts` is there, and both importers of the
 * component got the store instead. CI is Linux, where there is no `Toasts.ts`
 * to find, so nothing there could see it.
 *
 * A static rule rather than a note, as with no-raw-dialogs:
 * the next one arrives as a new file beside an old one, and the machine that
 * would notice is not the one running CI.
 *
 * Files only. There is no index module under src, so a directory beside a
 * file of the same name is not something an import can resolve to.
 */
const SRC = new URL('./', import.meta.url).pathname

function modules(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...modules(p))
    else if (/\.tsx?$/.test(name)) out.push(p.replace(/\.tsx?$/, ''))
  }
  return out
}

describe('module names on a case-insensitive disk', () => {
  const names = modules(SRC)

  it('finds the sources at all', () => {
    // A walk of the wrong directory finds no clashes, which reads as a clean tree.
    expect(names.length).toBeGreaterThan(20)
  })

  it('has no two modules in one directory that differ only in case', () => {
    const seen = new Map<string, string>()
    const clashes: string[] = []
    for (const n of names) {
      const other = seen.get(n.toLowerCase())
      if (other !== undefined && other !== n) {
        clashes.push(`${other.slice(SRC.length)} and ${n.slice(SRC.length)}`)
      }
      seen.set(n.toLowerCase(), n)
    }
    expect(clashes).toEqual([])
  })
})
