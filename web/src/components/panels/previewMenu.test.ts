import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * The preview button offers both policies, and the safe one is not the second
 * thought.
 *
 * The server half is pinned in Go: `dirPreviewCSP` widens only when the link
 * says so, `connect-src`, the sandbox and `base-uri` do not move either way,
 * and three mutations go red. None of that reaches the two menu items, and no
 * browser check drives this button at all -- stated here rather than left to
 * be found, because a check that does not exist looks exactly like one that
 * passes.
 */
describe('the preview menu', () => {
  const code = readFileSync(new URL('./FileTree.tsx', import.meta.url), 'utf8')
    .replace(/^\s*\/\*[\s\S]*?\*\/\s*$/gm, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')

  it('offers both, and passes the flag through', () => {
    expect(code).toMatch(/allowExternal: external/)
    expect(code).toMatch(/const makePreview = \(opts: \{[^}]*allowExternal/)
    // Not `[^)]*`: the call carries `path.split('/').pop()` and the class
    // stops at that first bracket, which is how this matched nothing.
    expect(code).toMatch(/\.\.\.opts,/)
  })

  it('does not widen by default', () => {
    // The button itself must not make an external link. If the menu is ever
    // collapsed back into one press, this is what says which of the two it
    // collapsed to.
    expect(code).not.toMatch(/allowExternal: true\b/)

  })

  it('closes the menu before it makes the link', () => {
    // A menu left open over the next directory makes a link for a path that is
    // no longer on screen.
    expect(code).toMatch(/setPreviewMenu\(false\)\s*\n\s*void makePreview\(\{/)
  })
})

describe('the advanced form', () => {
  const code = readFileSync(new URL('./FileTree.tsx', import.meta.url), 'utf8')
    .replace(/^\s*\/\*[\s\S]*?\*\/\s*$/gm, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')

  it('passes all three choices through', () => {
    // Each one separately, because a form that collects a field and does not
    // send it is the failure that looks exactly like the feature working.
    expect(code).toMatch(/expiresIn: f\.expiresIn/)
    expect(code).toMatch(/address: f\.address\.trim\(\)/)
    expect(code).toMatch(/allowExternal: f\.allowExternal/)
  })

  it('offers a link that never expires', () => {
    // 「可以自定义可见范围、有效期之类的」. 0 is never, and it has to be an
    // option rather than the absence of one: the server already reads 0 that
    // way, so leaving it out of the list would be the UI withholding something
    // the API does.
    expect(code).toMatch(/<option value=\{0\}>/)
  })

  it('says what choosing an address means', () => {
    // The one thing about this form that a person can get wrong without
    // noticing: an address they picked is public, and nothing else about the
    // link changes to say so.
    expect(code).toMatch(/files\.previewAddressWhy/)
  })
})
