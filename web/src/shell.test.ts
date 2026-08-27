import { describe, expect, it } from 'vitest'

import { joinArgv, shellQuote, splitArgv } from './shell'

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

describe('splitArgv', () => {
  it('splits on whitespace', () => {
    expect(splitArgv('claude --model opus')).toEqual(['claude', '--model', 'opus'])
  })

  it('holds a quoted word together', () => {
    expect(splitArgv('claude -p "two words" x')).toEqual(['claude', '-p', 'two words', 'x'])
    expect(splitArgv("claude -p 'two words'")).toEqual(['claude', '-p', 'two words'])
  })

  it('keeps an empty quoted argument, which is a real one', () => {
    expect(splitArgv('foo "" bar')).toEqual(['foo', '', 'bar'])
  })

  it('collapses runs of whitespace and ignores the edges', () => {
    expect(splitArgv('  a \t b  ')).toEqual(['a', 'b'])
    expect(splitArgv('')).toEqual([])
    expect(splitArgv('   ')).toEqual([])
  })

  it('takes a backslash as an escape outside single quotes', () => {
    expect(splitArgv('a\\ b')).toEqual(['a b'])
    expect(splitArgv("'a\\ b'")).toEqual(['a\\ b'])
  })

  // The argv is exec'd directly by tmux, not run through a shell -- measured
  // against tmux 3.6, where a semicolon in an argument was printed rather than
  // acted on. Expanding anything here would be this function inventing a shell
  // that is not there, and every disagreement is a command that does something
  // other than it reads as.
  it('expands nothing', () => {
    expect(splitArgv('echo $HOME ~ *.go')).toEqual(['echo', '$HOME', '~', '*.go'])
    expect(splitArgv('a && b | c > d')).toEqual(['a', '&&', 'b', '|', 'c', '>', 'd'])
  })
})

describe('joinArgv', () => {
  it('round-trips what people type', () => {
    for (const argv of [
      ['claude'],
      ['claude', '--model', 'opus'],
      ['sh', '-c', 'echo two words'],
      ['a', '', 'b'],
      ['it', "isn't"],
      [],
    ]) {
      expect(splitArgv(joinArgv(argv)), joinArgv(argv)).toEqual(argv)
    }
  })

  // shellQuote returns the empty string unchanged, which is right for a path
  // and a word that vanishes here: opening the field and saving it would
  // otherwise change what runs.
  it('spells out the empty word', () => {
    expect(joinArgv(['a', '', 'b'])).toBe("a '' b")
  })
})
