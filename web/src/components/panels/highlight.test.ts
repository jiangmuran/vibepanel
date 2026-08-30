import { describe, expect, it } from 'vitest'
import { highlight, highlightLines, langOf, shouldHighlight } from './highlight'

/** The tokens as `kind:text`, which is easier to read than the objects. */
const shape = (code: string, lang = '') =>
  highlight(code, lang).map((t) => `${t.k}:${t.v}`)

describe('the one thing it must not lose', () => {
  // A highlighter that drops a byte is a preview that lies about the file, and
  // it is the only failure here that is not merely cosmetic.
  it('puts every byte in exactly one token', () => {
    for (const src of [
      'func main() { fmt.Println("hi") } // done',
      '# a comment\nx = 1\n',
      'const s = "he said \\"no\\"" /* and */ 0x1f\n',
      'unterminated "string\nnext line',
      '/* never closed\nand on\n',
      '',
      '\n\n\n',
      'émoji 🎉 and 中文',
      "it's a lone apostrophe and the rest of the file\nsecond line",
    ]) {
      expect(highlight(src).map((t) => t.v).join(''), JSON.stringify(src)).toBe(src)
    }
  })
})

describe('order', () => {
  // A `//` inside a string is not a comment. This is why it is one pass and
  // not a series of regexes over the whole text.
  it('does not find a comment inside a string', () => {
    expect(shape('x = "http://a" + 1')).toEqual([
      'plain:x = ', 'string:"http://a"', 'plain: + ', 'number:1',
    ])
  })

  it('does not find a string inside a comment', () => {
    expect(shape("// it's fine\nx", 'go')).toEqual(["comment:// it's fine", 'plain:\nx'])
  })

  // One stray apostrophe used to colour the rest of the file.
  it('stops an unterminated quote at the newline', () => {
    expect(shape("it's here\nplain again")).toEqual([
      'plain:it', "string:'s here", 'plain:\nplain again',
    ])
  })

  // Backticks do span lines, which is the difference.
  it('lets a backtick string span lines', () => {
    expect(shape('`a\nb`')).toEqual(['string:`a\nb`'])
  })
})

describe('what it colours', () => {
  it('finds keywords, numbers, strings and comments', () => {
    expect(shape('if (x == 42) return "a"; // why', 'ts')).toEqual([
      'keyword:if', 'plain: (x == ', 'number:42', 'plain:) ', 'keyword:return',
      'plain: ', 'string:"a"', 'plain:; ', 'comment:// why',
    ])
  })

  // Not every language's `#` is a comment, and not every language has blocks.
  it('takes the comment marker from the language', () => {
    expect(shape('# hi', 'python')).toEqual(['comment:# hi'])
    expect(shape('/* hi */', 'python')).toEqual(['plain:/* hi */'])
    expect(shape('/* hi */', 'go')).toEqual(['comment:/* hi */'])
    expect(shape('-- hi', 'sql')).toEqual(['comment:-- hi'])
  })

  it('does not read a number out of the middle of a name', () => {
    expect(shape('utf8mb4')).toEqual(['plain:utf8mb4'])
    expect(shape('x1 = 0xFF')).toEqual(['plain:x1 = ', 'number:0xFF'])
  })
})

describe('when to bother', () => {
  // A paragraph with `class` and `type` in it comes out speckled, which is
  // worse than plain.
  it('leaves prose alone', () => {
    for (const n of ['notes.txt', 'server.log', 'rows.csv', 'README.md']) {
      expect(shouldHighlight(n), n).toBe(false)
    }
    for (const n of ['main.go', 'App.tsx', 'Makefile', 'x.py']) {
      expect(shouldHighlight(n), n).toBe(true)
    }
  })

  it('names the language from the file', () => {
    expect(langOf('main.go')).toBe('go')
    expect(langOf('a/b/App.TSX')).toBe('tsx')
    expect(langOf('Makefile')).toBe('makefile')
    expect(langOf('deploy/Dockerfile')).toBe('dockerfile')
    expect(langOf('LICENSE')).toBe('')
  })
})

describe('split into lines', () => {
  // Highlighting each line on its own is the obvious implementation and it is
  // wrong in exactly the way the one-pass design exists to avoid.
  it('keeps a block comment a comment on every line of it', () => {
    const got = highlightLines('/* one\n   two */\nx = 1', 'go')
    expect(got.map((l) => l.map((t) => `${t.k}:${t.v}`))).toEqual([
      ['comment:/* one'],
      ['comment:   two */'],
      ['plain:x = ', 'number:1'],
    ])
  })

  it('keeps a backtick string a string on every line of it', () => {
    const got = highlightLines('a = `one\ntwo`\n', 'ts')
    expect(got.map((l) => l.map((t) => t.k))).toEqual([
      ['plain', 'string'],
      ['string'],
    ])
  })

  it('has one entry per line, empty ones included', () => {
    expect(highlightLines('a\n\nb').length).toBe(3)
    expect(highlightLines('a\n').length).toBe(1)
    expect(highlightLines('').length).toBe(1)
  })

  it('still loses no bytes', () => {
    const src = 'func f() {\n\t// hi\n\treturn "x"\n}\n'
    const back = highlightLines(src, 'go').map((l) => l.map((t) => t.v).join('')).join('\n')
    expect(back).toBe(src.replace(/\n$/, ''))
  })
})
