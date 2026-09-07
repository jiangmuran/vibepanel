import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * The wizard asks before it locks the browser out, and the asking is wired.
 *
 * The server half of this is pinned in Go: `origin_test.go` posts a setup the
 * way a Host-dropping proxy delivers it and checks the 409, the ratification,
 * and that the value comes from the Origin header rather than the body. None
 * of that reaches the part a person actually touches.
 *
 * What is *not* covered anywhere, stated rather than left to be discovered:
 * no browser check drives this dialog. Reaching it needs Host and Origin to
 * disagree, a browser will not let a page set Host, and a real proxy in the
 * harness is a larger piece of work than the dialog. So this reads the source,
 * which catches the wiring going missing and cannot catch it looking wrong.
 */
describe('the setup wizard, when the panel would refuse this browser', () => {
  const code = readFileSync(new URL('./AuthGate.tsx', import.meta.url), 'utf8')
    .replace(/^\s*\/\*[\s\S]*?\*\/\s*$/gm, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')

  it('catches the refusal instead of showing it as an error', () => {
    // As a plain error this is a red line of prose under a form, saying the
    // panel answers to a name the reader did not type, with no way to act on
    // it -- and setup has not happened, so there is nothing else to try.
    expect(code).toMatch(/err instanceof OriginNotTrustedError/)
    expect(code).toMatch(/setAskOrigin\(err\)/)
  })

  it('shows both names', () => {
    // The decision is a comparison. One of the two on screen is not a
    // decision, it is a demand.
    expect(code).toMatch(/askOrigin\.origin/)
    expect(code).toMatch(/askOrigin\.answersTo/)
  })

  it('only ratifies when the button is pressed', () => {
    // runSetup(true) must be reachable from the dialog and from nowhere else.
    // The first submit is runSetup(false): a wizard that trusted on the way
    // past would make the dialog a decoration and the question rhetorical.
    expect(code).toMatch(/onClick=\{\(\) => void runSetup\(true\)\}/)
    expect(code).toMatch(/await runSetup\(false\)/)
    expect(code.match(/runSetup\(true\)/g) ?? []).toHaveLength(1)
  })

  it('keeps the form filled while it asks', () => {
    // The token is a long random string pasted from a terminal. Clearing it
    // to ask a yes/no question is how a person ends up going back to the
    // server log for something they already had.
    expect(code).not.toMatch(/setAskOrigin\([^)]*\)[\s\S]{0,120}setToken\(''\)/)
  })
})
