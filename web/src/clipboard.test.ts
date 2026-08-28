import { describe, expect, it, vi, afterEach } from 'vitest'

import { copyText } from './clipboard'

/** Source with its comments removed, so quoting a mistake is not making one. */
const code = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')

/* The bug this file exists for.
 *
 * The terminal's copy-on-select read:
 *
 *     navigator.clipboard?.writeText(text).catch(fallback)
 *
 * and optional chaining short-circuits the *whole* chain. With no
 * `navigator.clipboard` -- which is every non-secure origin, and a panel on a
 * LAN address over plain http is one -- the expression is `undefined`, `.catch`
 * is never reached, and the fallback that offers the text behind a click never
 * runs. Selecting text put nothing on the clipboard and said nothing.
 *
 * So the cases that matter here are the ones where `navigator.clipboard` is
 * absent, not the ones where it refuses. */

const noClipboard = () => {
  Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
}
const withClipboard = (writeText: (s: string) => Promise<void>) => {
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
}

afterEach(() => {
  Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
  vi.restoreAllMocks()
})

describe('copying without a gesture', () => {
  it('reports failure rather than throwing where there is no clipboard API', async () => {
    noClipboard()
    await expect(copyText('hello')).resolves.toBe(false)
  })

  it('reports failure when the write is refused', async () => {
    withClipboard(() => Promise.reject(new Error('NotAllowedError')))
    await expect(copyText('hello')).resolves.toBe(false)
  })

  it('reports success when it worked', async () => {
    const seen: string[] = []
    withClipboard((s) => {
      seen.push(s)
      return Promise.resolve()
    })
    await expect(copyText('hello')).resolves.toBe(true)
    expect(seen).toEqual(['hello'])
  })
})

describe('the pattern that caused this', () => {
  // A source scan, because the failure was not in a function's behaviour but in
  // how one was called, and there is no DOM here to render a call site into.
  //
  //     navigator.clipboard?.writeText(text).catch(fallback)
  //
  // reads as "write it, and if that fails do this". It is not: optional
  // chaining short-circuits the whole chain, so with no clipboard API the
  // catch never runs. It survived review twice, in two files.
  it('is not written anywhere in the source', async () => {
    const { readdirSync, readFileSync, statSync } = await import('node:fs')
    const { join } = await import('node:path')

    const walk = (dir: string): string[] =>
      readdirSync(dir).flatMap((name) => {
        const p = join(dir, name)
        if (statSync(p).isDirectory()) return walk(p)
        return /\.tsx?$/.test(name) && !name.endsWith('.test.ts') ? [p] : []
      })

    const here = new URL('.', import.meta.url).pathname
    const files = walk(here)
    expect(files.length).toBeGreaterThan(20)

    const offenders = files.filter((f) => {
      // Comments stripped first. Terminal.tsx quotes the broken line to
      // explain what went wrong, which is worth keeping and is not a call.
      const body = code(readFileSync(f, 'utf8'))
      // `?.` on the clipboard at all: every reachable use goes through
      // clipboard.ts, which checks for the object before touching it.
      return /navigator\.clipboard\s*\?\./.test(body)
    })
    expect(offenders).toEqual([])
  })

  it('leaves navigator.clipboard to one module', async () => {
    const { readdirSync, readFileSync, statSync } = await import('node:fs')
    const { join } = await import('node:path')
    const walk = (dir: string): string[] =>
      readdirSync(dir).flatMap((name) => {
        const p = join(dir, name)
        if (statSync(p).isDirectory()) return walk(p)
        return /\.tsx?$/.test(name) && !name.endsWith('.test.ts') ? [p] : []
      })
    const here = new URL('.', import.meta.url).pathname
    const users = walk(here).filter((f) =>
      /navigator\.clipboard/.test(code(readFileSync(f, 'utf8'))),
    )
    // One module, and only one. There were five call sites with three
    // different answers between them, and the wrong answer was in four of
    // them -- including both "copy this token, you will not see it again"
    // buttons, where a silent failure costs the token.
    const allowed = ['clipboard.ts']
    const extra = users.filter((f) => !allowed.some((a) => f.endsWith(a)))
    expect(extra).toEqual([])
  })
})
