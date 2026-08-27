import { readdirSync, readFileSync, statSync } from 'node:fs'
import { basename, join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * The browser's three dialogs are not this panel's.
 *
 * `window.confirm` and `window.prompt` were how the panel asked before killing
 * a session, removing a project, revoking a token and deleting a passkey. They
 * are wrong here in four separate ways, and every one of them was visible in
 * the product:
 *
 *   - They are the operating system's chrome, in the operating system's
 *     language. A panel translated line by line into Chinese asked the one
 *     question that destroys something in English, with an "OK" nobody in the
 *     dictionary chose.
 *   - They cannot mark the destructive answer. OK and Cancel are the same
 *     button twice, and OK is the one under the cursor.
 *   - They are a single string. The count of sessions about to be killed, the
 *     promise that the directory survives and the token prefix all had to be
 *     concatenated into one paragraph with a blank line in it.
 *   - On a phone installed to the home screen they arrive as a system sheet
 *     with the hostname above the question, which is the shape of a phishing
 *     prompt.
 *
 * They also block the event loop and are dismissed by the *browser*, which is
 * why the render check drove them with `page.once('dialog')` -- a listener that
 * silently never fires if the dialog stops being one. That has already cost
 * this project once, in the first-run check, and the comment there says so.
 *
 * A static rule rather than a note in AGENTS.md, because the next one arrives
 * as a one-line "quick confirm" in a component nobody is reviewing for this.
 */
const SRC = new URL('./', import.meta.url).pathname

function sources(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...sources(p))
    else if (name.endsWith('.ts') || name.endsWith('.tsx')) out.push(p)
  }
  return out
}

/** Prose about the old dialogs is not a use of them. */
function stripComments(s: string): string {
  return s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

const CALLS = [
  // Both spellings. `window.confirm(...)` and a bare `confirm(...)` are the
  // same function, and only the first is greppable by eye.
  /\bwindow\s*\.\s*(alert|confirm|prompt)\s*\(/g,
  /(?<![.\w$])(alert|confirm|prompt)\s*\(/g,
]

describe('the panel asks its own questions', () => {
  // Its own name, because the patterns above are written out below in the
  // failure message and this file would otherwise be the only offender.
  const files = sources(SRC).filter((f) => basename(f) !== 'no-raw-dialogs.test.ts')

  it('finds the sources at all', () => {
    // A scanner that walks the wrong directory reports no violations, which is
    // indistinguishable from a clean tree.
    expect(files.length).toBeGreaterThan(20)
  })

  it('calls none of the browser dialogs', () => {
    const bad: string[] = []
    for (const file of files) {
      const text = stripComments(readFileSync(file, 'utf8'))
      for (const pattern of CALLS) {
        for (const m of text.matchAll(pattern)) {
          bad.push(`${file.slice(SRC.length)}: ${m[0]}`)
        }
      }
    }
    expect(
      bad,
      'use askConfirm/askText from components/ask.ts for a question, and showToast ' +
        `from components/toasts.ts for something that only needs saying:\n${bad.join('\n')}`,
    ).toEqual([])
  })
})
