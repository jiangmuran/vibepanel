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
    expect(code).toMatch(/makePreview\(external\)/)
    expect(code).toMatch(/const makePreview = \(allowExternal: boolean\)/)
    // Not `[^)]*`: the call carries `path.split('/').pop()` and the class
    // stops at that first bracket, which is how this matched nothing.
    expect(code).toMatch(/createPreview\([\s\S]*?allowExternal\)/)
  })

  it('does not widen by default', () => {
    // The button itself must not make an external link. If the menu is ever
    // collapsed back into one press, this is what says which of the two it
    // collapsed to.
    expect(code).not.toMatch(/makePreview\(true\)/)
    expect(code).not.toMatch(/createPreview\([\s\S]*?,\s*true\)/)
  })

  it('closes the menu before it makes the link', () => {
    // A menu left open over the next directory makes a link for a path that is
    // no longer on screen.
    expect(code).toMatch(/setPreviewMenu\(false\)\s*\n\s*void makePreview\(external\)/)
  })
})
