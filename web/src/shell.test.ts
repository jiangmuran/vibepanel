import { describe, expect, it } from 'vitest'

import { shellQuote } from './shell'

// eslint-disable-next-line no-control-regex
const ANY_CONTROL = /[\u0000-\u001F\u007F]/

describe('shellQuote', () => {
  it('leaves a plain name alone', () => {
    expect(shellQuote('/home/u/projects/a-file_1.txt')).toBe('/home/u/projects/a-file_1.txt')
  })

  it('quotes a name with a space', () => {
    expect(shellQuote('/tmp/two words.png')).toBe("'/tmp/two words.png'")
  })

  it('closes and reopens for an embedded single quote', () => {
    expect(shellQuote("/tmp/it's.png")).toBe("'/tmp/it'\\''s.png'")
  })

  it('sends no control byte for a name containing a newline', () => {
    // The whole point. What reaches the PTY has no 0x0A in it, so the line
    // editor cannot read one as Enter and leave the user at a PS2 prompt they
    // cannot explain.
    const quoted = shellQuote('/tmp/a\nb.png')
    expect(quoted).not.toMatch(ANY_CONTROL)
    expect(quoted).toBe("$'/tmp/a\\x0ab.png'")
  })

  it('escapes every control character, not only the newline', () => {
    const quoted = shellQuote('/tmp/a\tb\rc\x07d.png')
    expect(quoted).not.toMatch(ANY_CONTROL)
    expect(quoted).toContain('\\x09')
    expect(quoted).toContain('\\x0d')
    expect(quoted).toContain('\\x07')
  })

  it('escapes a backslash before introducing its own', () => {
    // Without that ordering a backslash already in the name and the one
    // starting an escape are indistinguishable to the shell, and the path the
    // user presses enter on is not the file that was uploaded.
    expect(shellQuote('/tmp/a\\b\nc')).toBe("$'/tmp/a\\\\b\\x0ac'")
  })

  it('escapes a single quote inside the ANSI-C form', () => {
    const quoted = shellQuote("/tmp/it's\na.png")
    expect(quoted.startsWith("$'")).toBe(true)
    expect(quoted.endsWith("'")).toBe(true)
    expect(quoted).toContain("\\'")
    expect(quoted).not.toMatch(ANY_CONTROL)
  })

  it('never lets a control character through, whatever else is in the name', () => {
    for (const code of [...Array(0x20).keys(), 0x7f]) {
      const quoted = shellQuote('/tmp/x' + String.fromCharCode(code) + 'y')
      expect(quoted).not.toMatch(ANY_CONTROL)
    }
  })
})
