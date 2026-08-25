import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

/**
 * The stale-socket sweeper's allowlist, against the sockets that are actually
 * built.
 *
 * `sweepStaleSockets` kills tmux servers left by harness runs that were
 * SIGKILLed, and it decides what is safe to touch from the socket's name — so
 * a prefix missing from that pattern is a check whose leaks nothing will ever
 * clean up. `vpfirstrun` was missing for as long as the sweeper existed.
 *
 * The premise of this project is that it does not disturb what is already
 * running on the machine. A test suite that quietly accumulates tmux servers
 * is that failure wearing a different hat, which is what the sweeper's own
 * documentation says.
 *
 * One direction only: every prefix in use must be covered. Extra entries are
 * retired harnesses whose sockets may still be on disk, and dropping one would
 * strand them.
 */
const root = fileURLToPath(new URL('../..', import.meta.url))
const sweeper = readFileSync(`${root}/web/scripts/lib/stale.mjs`, 'utf8')

function prefixesIn(dir: string): string[] {
  const found: string[] = []
  for (const name of readdirSync(dir)) {
    if (!/\.(mjs|sh)$/.test(name)) continue
    const src = readFileSync(`${dir}/${name}`, 'utf8')
    for (const m of src.matchAll(/\b(?:SOCKET|SOCK)\s*=\s*[`"']vp([a-z]+)-/g)) {
      found.push(m[1])
    }
  }
  return found
}

describe('the stale tmux socket sweeper', () => {
  const prefixes = [
    ...new Set([...prefixesIn(`${root}/web/scripts`), ...prefixesIn(`${root}/scripts`)]),
  ].sort()

  it('found the harnesses at all', () => {
    // If the socket names stop looking like `const SOCKET = \`vpx-...\`` this
    // test reads nothing and passes, which is the failure it is here to avoid.
    expect(prefixes.length).toBeGreaterThanOrEqual(6)
  })

  for (const prefix of prefixes) {
    it(`can sweep vp${prefix}-<pid>`, () => {
      const pattern = new RegExp(sweeper.match(/const HARNESS_SOCKET = \/(.+)\//)![1])
      expect(pattern.test(`vp${prefix}-12345`)).toBe(true)
    })
  }
})
