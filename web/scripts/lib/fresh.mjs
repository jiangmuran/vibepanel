// Refuse to measure a build that does not contain what is being measured.
//
// A mutation of `touchSelect.ts` left TypeScript unable to compile it. `make
// build` exited 2, `internal/webui/dist` was untouched, the binary kept the
// previous frontend, and the browser check reported PASS — for a change that
// was never in the build. It returned the same line numbers as the unmutated
// run, which is the only reason it was caught at all.
//
// Every browser check drives a prebuilt binary, and `npm run check:*` does not
// rebuild anything. So the failure mode is always available: edit, forget to
// build, measure yesterday.
//
// This turns it from a wrong answer into a loud one.
import { statSync, readdirSync } from 'node:fs'
import { join, extname } from 'node:path'

const SKIP = new Set(['node_modules', 'dist', '.git', 'testdata', 'shots'])

/** The newest mtime under a directory, ignoring build output. */
function newest(dir, exts) {
  let latest = { mtime: 0, path: null }
  const walk = (d) => {
    let entries
    try {
      entries = readdirSync(d, { withFileTypes: true })
    } catch {
      return // absent is not stale
    }
    for (const e of entries) {
      if (SKIP.has(e.name)) continue
      const p = join(d, e.name)
      if (e.isDirectory()) {
        walk(p)
        continue
      }
      if (exts && !exts.has(extname(e.name))) continue
      const m = statSync(p).mtimeMs
      if (m > latest.mtime) latest = { mtime: m, path: p }
    }
  }
  walk(dir)
  return latest
}

/**
 * Throws if the binary is older than the sources it is built from.
 *
 * `root` is the repository root. Checks two links in the chain separately, so
 * the message says which one is stale: the Go binary against Go sources and the
 * embedded frontend, and the embedded frontend against the frontend sources.
 */
export function assertFreshBuild(bin, root) {
  let binStat
  try {
    binStat = statSync(bin)
  } catch {
    throw new Error(`no binary at ${bin}; run \`make build\` first`)
  }

  const dist = join(root, 'internal', 'webui', 'dist')
  const web = newest(join(root, 'web', 'src'))
  const built = newest(dist)
  if (built.mtime && web.mtime > built.mtime) {
    throw new Error(
      `${web.path} is newer than the built frontend in internal/webui/dist. ` +
      'The binary embeds the previous one, so this check would measure a build ' +
      'that does not contain the change. Run `make build`.',
    )
  }

  const go = newest(join(root, 'internal'), new Set(['.go', '.conf']))
  const cmd = newest(join(root, 'cmd'), new Set(['.go']))
  const newestInput = [go, cmd, built].reduce((a, b) => (b.mtime > a.mtime ? b : a))
  if (newestInput.mtime > binStat.mtimeMs) {
    throw new Error(
      `${newestInput.path} is newer than ${bin}. This check would measure the ` +
      'previous build. Run `make build`.',
    )
  }
}
