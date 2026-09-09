import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { isBrowserCopy, isBrowserPaste, type KeyPress } from './clipboardKeys'

/**
 * The rules about ctrl+V and ctrl+C, without a browser.
 *
 * What a browser run checks is that the clipboard arrives; `render-check.mjs`
 * presses both keys at a real session and reads the result off the screen.
 * What it cannot do cheaply is the *other* half -- every combination these
 * must keep their hands off, because each one is a keystroke that silently
 * stops reaching the pty if a condition is widened.
 */
function press(over: Partial<KeyPress> = {}): KeyPress {
  return { type: 'keydown', key: 'v', ctrlKey: true, metaKey: false, altKey: false, ...over }
}

describe('ctrl+V belongs to the browser', () => {
  it('is claimed with either paste modifier', () => {
    expect(isBrowserPaste(press())).toBe(true)
    expect(isBrowserPaste(press({ ctrlKey: false, metaKey: true }))).toBe(true)
  })

  it('is claimed with shift too, which is the other paste people press', () => {
    // Uppercase because shift is down. xterm already leaves ctrl+shift+V alone
    // -- its ctrl branch requires !shiftKey -- so this changes nothing today;
    // it is here so that both keys people paste with keep going to the same
    // place if that ever moves.
    expect(isBrowserPaste(press({ key: 'V' }))).toBe(true)
  })

  it('is not claimed on the way back up', () => {
    // xterm hands its handler keyup as well, and answering false there skips
    // the refocus and cursor update it does on that path.
    expect(isBrowserPaste(press({ type: 'keyup' }))).toBe(false)
    expect(isBrowserPaste(press({ type: 'keypress' }))).toBe(false)
  })

  it('leaves AltGr alone', () => {
    // AltGr is ctrl+alt on Windows and Linux. A layout that types a character
    // there would have it swallowed by a rule that only looked at ctrl, and
    // the symptom is one key on one keyboard doing nothing at all.
    expect(isBrowserPaste(press({ altKey: true }))).toBe(false)
    expect(isBrowserPaste(press({ ctrlKey: false, altKey: true }))).toBe(false)
  })

  it('leaves every other control character alone', () => {
    // ^C, ^D, ^Z, ^L, ^R: the keys an agent and a shell are driven with. A
    // rule that claimed any of them would look like a dead terminal.
    for (const key of ['c', 'd', 'z', 'l', 'r', 'u', 'w']) {
      expect(isBrowserPaste(press({ key }))).toBe(false)
    }
  })

  it('leaves a plain v alone', () => {
    // Typing the letter, which is most of what this key is ever used for.
    expect(isBrowserPaste(press({ ctrlKey: false }))).toBe(false)
  })
})

describe('ctrl+C is the chord, and never the whole condition', () => {
  const copy = (over: Partial<KeyPress> = {}): KeyPress => press({ key: 'c', ...over })

  it('is claimed with either copy modifier', () => {
    expect(isBrowserCopy(copy())).toBe(true)
    expect(isBrowserCopy(copy({ ctrlKey: false, metaKey: true }))).toBe(true)
    expect(isBrowserCopy(copy({ key: 'C' }))).toBe(true)
  })

  it('is not claimed on the way back up', () => {
    expect(isBrowserCopy(copy({ type: 'keyup' }))).toBe(false)
  })

  it('leaves AltGr alone', () => {
    expect(isBrowserCopy(copy({ altKey: true }))).toBe(false)
  })

  it('leaves the other control characters alone', () => {
    for (const key of ['v', 'd', 'z', 'l', 'r', 'u', 'w']) {
      expect(isBrowserCopy(copy({ key }))).toBe(false)
    }
  })

  it('leaves a plain c alone', () => {
    expect(isBrowserCopy(copy({ ctrlKey: false }))).toBe(false)
  })

  it('says nothing about the selection, which is the caller’s half', () => {
    // The predicate answers true for a ctrl+C pressed with nothing selected
    // too -- and that press must still interrupt. Deciding here would mean
    // this file needed a terminal; deciding in the caller is what the wiring
    // block below pins.
    expect(isBrowserCopy(copy())).toBe(true)
  })
})

describe('and the terminal actually hands the key back', () => {
  /*
   * The predicate above is provable in node; the wiring is not, and only the
   * wiring is the bug. `attachCustomKeyEventHandler` can be deleted outright
   * with every assertion in this file still green -- the rule exists, is
   * correct, and nothing consults it. Measured: removing that one line left
   * 546 tests passing, and the only thing that went red was `render-check`,
   * which takes twenty minutes and is not the gate anybody runs before
   * committing.
   *
   * That is the fifth time in this suite. A decision extracted into a pure
   * function so it can be tested, and a call site nothing looks at: the
   * extraction is what makes it testable and also what makes it skippable.
   */
  const code = readFileSync(new URL('./Terminal.tsx', import.meta.url), 'utf8')
    // Only comments that own their line. The `[\s\S]*?` form finds the `/*`
    // inside `accept="image/*"` and eats the rest of the file with it; that
    // one cut TouchBar.tsx to a fifth of itself and took three suites down
    // with it, silently, because a shorter string still matches less.
    .replace(/^\s*\/\*[\s\S]*?\*\/\s*$/gm, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')

  it('registers the handler with xterm', () => {
    expect(code).toMatch(/attachCustomKeyEventHandler/)
  })

  it('returns false for a paste, which is what leaves the event uncancelled', () => {
    // The direction is the whole fix. `if (isBrowserPaste(e)) return true`
    // compiles, type-checks, reads almost the same, and restores the bug:
    // true means xterm processes the key and sends \x16.
    expect(code).toMatch(/if\s*\(isBrowserPaste\(e\)\)\s*return false/)
  })

  it('takes ctrl+C only when something is selected', () => {
    // Without the second half this is a terminal that cannot be interrupted.
    // ^C is how a runaway agent is stopped, and the person pressing it is in a
    // hurry; a copy that ate it would look like the panel had frozen.
    expect(code).toMatch(/isBrowserCopy\(e\)\s*&&\s*term\.hasSelection\(\)/)
  })

  it('falls through to the interrupt when there was nothing to copy', () => {
    // hasSelection() can be true for a selection whose text is empty. Copying
    // nothing and swallowing the key is the same failure as above, arrived at
    // from one step further in.
    expect(code).toMatch(/if\s*\(!copySelection\(\)\)\s*return true/)
  })

  it('clears the selection in the same gesture', () => {
    // So the next ctrl+C interrupts. Left in place, a selection on screen
    // swallows every press after the first: the same text copied again and
    // again while the agent it came from keeps running.
    expect(code).toMatch(/term\.clearSelection\(\)/)
  })
})
