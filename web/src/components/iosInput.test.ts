import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { iosInputText, shouldBypassXtermKeydown } from './iosInput'

describe('iOS direct IME input', () => {
  it('bypasses xterm for a non-composition keyCode 229 event', () => {
    expect(shouldBypassXtermKeydown({ keyCode: 229, isComposing: false })).toBe(true)
  })

  it('keeps active composition inside xterm', () => {
    expect(shouldBypassXtermKeydown({ keyCode: 229, isComposing: true })).toBe(false)
  })

  it('does not change ordinary keyboard handling', () => {
    expect(shouldBypassXtermKeydown({ keyCode: 65, isComposing: false })).toBe(false)
  })

  it('forwards direct text and editing input events', () => {
    expect(iosInputText({ inputType: 'insertText', data: '，' })).toBe('，')
    expect(iosInputText({ inputType: 'deleteContentBackward', data: null })).toBe('\x7f')
    expect(iosInputText({ inputType: 'deleteContentForward', data: null })).toBe('\x1b[3~')
    expect(iosInputText({ inputType: 'insertLineBreak', data: null })).toBe('\r')
  })

  it('ignores input events that are not direct editing', () => {
    expect(iosInputText({ inputType: 'insertCompositionText', data: '中' })).toBeNull()
  })
})

describe('and the terminal installs the workaround', () => {
  const code = readFileSync(new URL('./Terminal.tsx', import.meta.url), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/^\s*\/\/.*$/gm, ' ')

  it('checks the predicate before xterm receives the keydown', () => {
    expect(code).toMatch(/shouldBypassXtermKeydown\(event\)/)
    expect(code).toMatch(/iosInputText\(event(?: as InputEvent)?\)/)
    expect(code).toMatch(/host\.addEventListener\('keydown', bypassIOSKeydown, true\)/)
    expect(code).toMatch(/host\.addEventListener\('input', forwardIOSInput, true\)/)
  })

  it('does not cancel the browser default input', () => {
    expect(code).not.toMatch(/bypassIOSKeydown[\s\S]{0,500}preventDefault\(\)/)
  })

  // The flag is what tells the input listener that this character is already
  // being handled here. Left armed -- a keyup iOS did not send -- the next
  // ordinary keystroke goes to the PTY twice, once from xterm's own keydown
  // and once from the input event after it. Any other key disarms it, so the
  // window is never wider than the keystroke that opened it.
  it('disarms on any other keydown, not only on keyup', () => {
    const bypass = code.slice(code.indexOf('const bypassIOSKeydown'), code.indexOf('const forwardIOSInput'))
    expect(bypass).toMatch(/shouldBypassXtermKeydown\(event\)\)\s*\{[\s\S]*forwardingIOSInput = false/)
  })
})
