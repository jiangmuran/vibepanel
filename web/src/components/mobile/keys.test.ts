import { describe, expect, it } from 'vitest'

import { KEY_SEQUENCES, withAlt, withCtrl } from './keys'

describe('key sequences', () => {
  it('encodes the keys a phone cannot produce', () => {
    // These are the encodings terminals actually expect; a wrong one is a key
    // that silently does nothing, which is very hard to notice on a phone.
    expect(KEY_SEQUENCES.escape).toBe('\x1b')
    expect(KEY_SEQUENCES.tab).toBe('\t')
    expect(KEY_SEQUENCES.enter).toBe('\r')
    expect(KEY_SEQUENCES.up).toBe('\x1b[A')
    expect(KEY_SEQUENCES.down).toBe('\x1b[B')
    expect(KEY_SEQUENCES.right).toBe('\x1b[C')
    expect(KEY_SEQUENCES.left).toBe('\x1b[D')
    expect(KEY_SEQUENCES.pageUp).toBe('\x1b[5~')
    expect(KEY_SEQUENCES.backspace).toBe('\x7f')
  })
})

describe('withCtrl', () => {
  it('clears the top three bits', () => {
    expect(withCtrl('c')).toBe('\x03')
    expect(withCtrl('C')).toBe('\x03')
    expect(withCtrl('d')).toBe('\x04')
    expect(withCtrl('a')).toBe('\x01')
    // Ctrl-[ is Escape, which is the same accident that makes Escape usable at
    // all on keyboards that lack the key.
    expect(withCtrl('[')).toBe('\x1b')
    expect(withCtrl(' ')).toBe('\x00')
  })

  it('leaves characters it cannot modify alone', () => {
    // Better a plain digit than a wrong control byte.
    expect(withCtrl('1')).toBe('1')
    expect(withCtrl('')).toBe('')
  })
})

describe('withAlt', () => {
  it('prefixes an escape', () => {
    expect(withAlt('b')).toBe('\x1bb')
    expect(withAlt('.')).toBe('\x1b.')
  })
})
