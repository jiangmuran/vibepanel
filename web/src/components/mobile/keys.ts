/**
 * The byte sequences a terminal expects for keys a touchscreen has no way to
 * produce.
 *
 * Written out rather than derived, because the encodings are historical
 * accidents and a clever rule would be wrong for half of them.
 */
export const KEY_SEQUENCES = {
  escape: '\x1b',
  tab: '\t',
  // Shift+Tab is CSI Z, "cursor backward tabulation", and it is not something
  // a phone keyboard can produce at all. It earns a key here because the
  // agents this panel exists for bind it: Claude Code cycles its permission
  // mode with it, and a reader on a phone who cannot send it is locked out of
  // half the conversation.
  shiftTab: '\x1b[Z',
  enter: '\r',
  backspace: '\x7f',
  up: '\x1b[A',
  down: '\x1b[B',
  right: '\x1b[C',
  left: '\x1b[D',
  home: '\x1b[H',
  end: '\x1b[F',
  pageUp: '\x1b[5~',
  pageDown: '\x1b[6~',
  delete: '\x1b[3~',
} as const

export type KeyName = keyof typeof KEY_SEQUENCES

/**
 * Applies Ctrl to a character.
 *
 * Ctrl clears the top three bits, which is why Ctrl-C is 0x03 and Ctrl-[ is
 * Escape. Letters are folded to upper case first so ctrl+c and ctrl+C agree.
 */
export function withCtrl(ch: string): string {
  if (!ch) return ''
  const upper = ch[0].toUpperCase()
  const code = upper.charCodeAt(0)
  if (code >= 0x40 && code <= 0x5f) return String.fromCharCode(code & 0x1f)
  // Space is Ctrl-@, the null byte — used to set a mark in several editors.
  if (ch === ' ') return '\x00'
  return ch
}

/** Applies Alt, which terminals encode as an Escape prefix. */
export function withAlt(ch: string): string {
  return '\x1b' + ch
}
