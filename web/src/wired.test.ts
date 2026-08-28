import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

/**
 * Every module the panel ships is reachable from the panel.
 *
 * Two features shipped switched off by nothing. `notifyOnWaiting` was complete,
 * commented, described in both READMEs, and **called from nowhere** -- so the
 * settings toggle asked for notification permission, stored a boolean and
 * promised something that could never happen. `panels/VncView.tsx` was a whole
 * viewer left behind when its tab was retired, sitting next to a working proxy,
 * a `--vnc` flag and a settings page for displays -- which is what this test
 * finding it started: a viewer nothing imported turned out to be the only half
 * of that feature anybody would have noticed missing, and the rest of it has
 * since been taken out too.
 *
 * Neither is a compile error, neither is a lint error, and both survived
 * review. What found them was reading the README against the code, by hand.
 *
 * So: a file under src/ that nothing imports is either dead or a promise nobody
 * kept. Entry points, and files that exist to be loaded rather than imported,
 * are named below with a reason each.
 */
const ROOT = new URL('.', import.meta.url).pathname

const EXPECTED_UNIMPORTED = new Set([
  'main.tsx', // the entry point; index.html loads it by URL
  'vite-env.d.ts', // ambient types, referenced by tsc rather than imported
  'panes-harness.tsx', // panes-harness.html loads it; it ships nowhere
])

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) return walk(p)
    return /\.tsx?$/.test(name) ? [p] : []
  })
}

describe('every module', () => {
  const files = walk(ROOT)
  const source = files.filter((f) => !/\.test\.tsx?$/.test(f))
  const bodies = new Map(files.map((f) => [f, readFileSync(f, 'utf8')]))

  it('has enough files to be worth checking', () => {
    expect(source.length).toBeGreaterThan(40)
  })

  it('is imported by something, or is named as an entry point', () => {
    const orphans = source.filter((f) => {
      const rel = f.slice(ROOT.length)
      if (EXPECTED_UNIMPORTED.has(rel)) return false
      const base = rel.replace(/\.tsx?$/, '').split('/').pop()!
      // Relative depth varies, so what identifies an import is its tail.
      const re = new RegExp(`from '[^']*/${base}'|from '\\./${base}'`)
      return !files.some((other) => other !== f && re.test(bodies.get(other)!))
    })
    expect(orphans.map((f) => f.slice(ROOT.length))).toEqual([])
  })

  it('exports no function that nothing else mentions', () => {
    // Narrower than the file check, and the one that would have caught
    // notifyOnWaiting -- whose file *was* imported, for three other functions
    // beside it.
    //
    // "else" is the whole rule: a function used only inside its own module is
    // not dead, it just should not be exported. BigMeter was exactly that and
    // this reported it correctly -- dropping the keyword is the fix, deleting
    // the function is not.
    const unused: string[] = []
    for (const f of source) {
      const rel = f.slice(ROOT.length)
      if (EXPECTED_UNIMPORTED.has(rel)) continue
      for (const m of bodies.get(f)!.matchAll(/^export (?:async )?function (\w+)/gm)) {
        const name = m[1]
        const used = files.some(
          (other) => other !== f && new RegExp(`\\b${name}\\b`).test(bodies.get(other)!),
        )
        if (!used) unused.push(`${rel}: ${name}`)
      }
    }
    expect(unused).toEqual([])
  })
})
