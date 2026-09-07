import { describe, expect, it } from 'vitest'

import { isBrowserPaste, type KeyPress } from './pasteKey'

/**
 * The rule about ctrl+V, without a browser.
 *
 * What a browser run checks is that the paste arrives; `render-check.mjs` puts
 * text on the clipboard, presses the key and reads it off the screen. What it
 * cannot do cheaply is the *other* half -- every combination this must keep
 * its hands off, because each one of those is a keystroke that silently stops
 * reaching the pty if the condition is widened.
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
