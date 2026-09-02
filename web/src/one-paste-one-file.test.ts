import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * One paste, one file.
 *
 * A pasted screenshot arrived twice, and there were two separate ways for it to
 * happen, because three handlers listened for the same gesture:
 *
 *   App.tsx        document, capture   uploads beside the session
 *   Terminal.tsx   its host, capture   uploaded beside the session, identically
 *   FileTree.tsx   window, bubble      uploads into the browsed directory
 *
 * With the terminal focused, the first two both fired: capture descends, so
 * document ran first and the event carried on down to the host. `preventDefault`
 * does not stop propagation, and both ended in the same `uploadInto`. With
 * *nothing* focused -- where focus sits after almost every click -- the first
 * and third both fired, because FileTree also claimed `e.target ===
 * document.body`, which is exactly the case App's handler was added for.
 *
 * Terminal's was deleted: it did what App's already did. The other two upload to
 * genuinely different places and both are wanted, so what they need is disjoint
 * claims rather than an ordering. `data-vp-paste-own` is that: FileTree marks
 * its panel with it and takes only pastes inside itself, and App stands down for
 * exactly the events that attribute covers.
 *
 * A source scan rather than a DOM test, and not only because vitest runs on
 * `node` here: the failure is structural. A fourth listener added anywhere
 * reintroduces it, and testing any surviving handler in isolation would never
 * notice -- each one is correct on its own. That is what made it survive review.
 */

const ROOT = new URL('.', import.meta.url).pathname

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) {
      sources(path, out)
      continue
    }
    if (!/\.tsx?$/.test(name) || /\.test\.tsx?$/.test(name)) continue
    out.push(path)
  }
  return out
}

/**
 * Source with its comments removed.
 *
 * Needed because this file's own explanation quotes the condition it forbids,
 * and a scan that reads comments would fail on the account of why it exists.
 */
function code(rel: string): string {
  return readFileSync(join(ROOT, rel), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')
}

describe('a pasted screenshot is uploaded once', () => {
  it('has exactly the two paste listeners, and no more', () => {
    const found: string[] = []
    for (const path of sources(ROOT)) {
      const src = readFileSync(path, 'utf8')
      // The registration, not the word. `onPaste={...}` on a React element is a
      // prop the component decides what to do with, and the mobile compose box
      // legitimately has one.
      for (const m of src.matchAll(/(\w+)\.addEventListener\(\s*'paste'/g)) {
        found.push(`${path.slice(ROOT.length)}: ${m[1]}`)
      }
    }
    expect(found.sort()).toEqual([
      'App.tsx: document',
      'components/panels/FileTree.tsx: window',
    ])
  })

  it('the file panel claims only what is inside it', () => {
    const src = code('components/panels/FileTree.tsx')
    // The exact condition that caused the double upload. A paste aimed at
    // nothing is the session's, and the panel must not answer for it.
    expect(src).not.toMatch(/target\s*===\s*document\.body/)
    expect(src).toContain('if (!root.contains(target)) return')
  })

  it('the two claims are made disjoint by the same attribute', () => {
    // FileTree marks itself; App stands down for anything inside a mark. Delete
    // either half and the panel's own pastes are uploaded twice again -- once
    // into the directory and once beside the session.
    expect(code('components/panels/FileTree.tsx')).toContain('data-vp-paste-own')
    expect(code('App.tsx')).toContain("closest('[data-vp-paste-own]')")
  })
})
