import { readdirSync, readFileSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

import { CONTEXT_LINES, blockTokens, tokenLines, type Tok } from './tokens'
import { LANGUAGES, grammarFor, grammarForLanguage, loadGrammar, loadOrder, loaders } from './grammars'
import Prism from 'prismjs'

const components = createRequire(import.meta.url)('prismjs/components.json') as {
  languages: Record<string, { require?: string | string[] }>
}
const CORE = ['markup', 'css', 'clike', 'javascript']

const kinds = (line: Tok[]) => line.map((t) => `${t.k}:${t.v}`)

describe('the grammar table', () => {
  it('loads what each grammar extends before it, by Prism\'s own list', () => {
    for (const lang of Object.keys(loaders)) {
      const order = loadOrder(lang)
      const req = components.languages[lang]?.require ?? []
      for (const r of Array.isArray(req) ? req : [req]) {
        if (CORE.includes(r)) continue
        expect(order.indexOf(r), `${lang} needs ${r} first`).toBeGreaterThanOrEqual(0)
        expect(order.indexOf(r)).toBeLessThan(order.indexOf(lang))
      }
    }
  })

  it('registers every language it lists', async () => {
    for (const lang of LANGUAGES) {
      await loadGrammar(lang)
      expect(Prism.languages[lang], lang).toBeTruthy()
    }
    // Forty, as the design says: more is a bundle nobody asked for.
    expect(LANGUAGES.length).toBeGreaterThanOrEqual(40)
  })

  it('knows files by name before extension', () => {
    expect(grammarFor('Makefile')).toBe('makefile')
    expect(grammarFor('src/Dockerfile')).toBe('docker')
    expect(grammarFor('Dockerfile.dev')).toBe('docker')
    expect(grammarFor('a/b/c.tsx')).toBe('tsx')
    // A name that is a key of every object is not a grammar.
    expect(grammarFor('a.constructor')).toBeNull()
    expect(grammarFor('x.toString')).toBeNull()
    expect(grammarFor('__proto__')).toBeNull()
  })

  it('names a language by Prism\'s name or by its extension', () => {
    // A fence or a kernel says `python` as often as `py`.
    expect(grammarForLanguage('python')).toBe('python')
    expect(grammarForLanguage('py')).toBe('python')
    expect(grammarForLanguage(' Python ')).toBe('python')
    expect(grammarForLanguage('javascript')).toBe('javascript')
    expect(grammarForLanguage('ts')).toBe('typescript')
    expect(grammarForLanguage('')).toBeNull()
    expect(grammarForLanguage('no-such-language')).toBeNull()
    // Not a key that every object has.
    expect(grammarForLanguage('constructor')).toBeNull()
    expect(grammarFor('main.go')).toBe('go')
    expect(grammarFor('.zshrc')).toBe('bash')
    expect(grammarFor('page.html')).toBe('markup')
    expect(grammarFor('changes.patch')).toBe('diff')
  })

  it('leaves what nobody listed as plain text', () => {
    for (const n of ['notes.txt', 'build.log', 'x.unknownext', 'no-extension', '.hidden']) {
      expect(grammarFor(n), n).toBeNull()
    }
  })
})

describe('Prism', () => {
  it('is imported by tokens.ts and nothing else', () => {
    // Anything that imports Prism first loads it before prismSetup has run,
    // and Prism would then highlight the page on its own. In node Prism keeps
    // its settings on an object of its own, so `manual` itself is read in
    // the browser, by preview-check.
    const src = new URL('../../', import.meta.url).pathname
    const files: string[] = []
    const walk = (d: string) => {
      for (const n of readdirSync(d)) {
        const p = join(d, n)
        if (statSync(p).isDirectory()) walk(p)
        else if (/\.tsx?$/.test(n) && !/\.test\.tsx?$/.test(n)) files.push(p)
      }
    }
    walk(src)
    const importers = files
      .filter((f) => /from 'prismjs'|import 'prismjs'/.test(readFileSync(f, 'utf8')))
      .map((f) => f.slice(src.length))
    expect(importers).toEqual(['components/viewer/tokens.ts'])
  })
})

describe('tokenLines', () => {
  it('names what a reader tells apart', async () => {
    await loadGrammar('typescript')
    const [line] = tokenLines('const n: number = 1 // one', 'typescript')
    const by = Object.fromEntries(line.filter((t) => t.k !== 'plain').map((t) => [t.v.trim(), t.k]))
    expect(by.const).toBe('keyword')
    expect(by.number).toBe('type')
    expect(by['1']).toBe('number')
    expect(by['// one']).toBe('comment')
  })

  it('keeps a token that spans lines in its kind on every line', async () => {
    await loadGrammar('typescript')
    const lines = tokenLines('a /* one\ntwo\nthree */ b', 'typescript')
    expect(lines).toHaveLength(3)
    expect(lines[1]).toEqual([{ k: 'comment', v: 'two' }])
    expect(lines[2][0]).toEqual({ k: 'comment', v: 'three */' })
  })

  it('reads a quote inside a comment as the comment, and // inside a string as the string', async () => {
    await loadGrammar('typescript')
    const [a] = tokenLines("// it's", 'typescript')
    expect(kinds(a)).toEqual(["comment:// it's"])
    const [b] = tokenLines("'http://x'", 'typescript')
    expect(b.every((t) => t.k === 'string')).toBe(true)
  })

  it('reads a token by its alias when its own type is one nobody listed', async () => {
    // Python's docstring is `triple-quoted-string`, aliased `string`.
    await loadGrammar('python')
    const [line] = tokenLines('x = """doc"""', 'python')
    expect(line.find((t) => t.v.includes('doc'))?.k).toBe('string')
  })

  it('has every line, empty ones too, and none past the end', () => {
    expect(tokenLines('a\n\nb\n', 'nothing-loaded')).toHaveLength(3)
    expect(tokenLines('a\n\nb\n', 'nothing-loaded')[1]).toEqual([])
  })

  it('is text and only text, whatever the file says', async () => {
    await loadGrammar('markup')
    const lines = tokenLines('<script>alert(1)</script><img src=x onerror=alert(2)>', 'markup')
    const joined = lines.flat().map((t) => t.v).join('')
    expect(joined).toBe('<script>alert(1)</script><img src=x onerror=alert(2)>')
    for (const t of lines.flat()) expect(typeof t.v).toBe('string')
  })

  it('colours the diff signs and their lines', async () => {
    await loadGrammar('diff')
    const lines = tokenLines('-old\n+new\n same', 'diff')
    expect(lines[0].every((t) => t.k === 'deleted')).toBe(true)
    expect(lines[1].every((t) => t.k === 'inserted')).toBe(true)
  })
})

describe('blockTokens', () => {
  it('starts a block inside a comment opened above it', async () => {
    await loadGrammar('typescript')
    // The comment opens at line 100, so for the block at 256 it is 156 lines
    // up: inside the context, and nowhere inside the block itself.
    const lines = [
      ...Array.from({ length: 100 }, () => 'const before = 1'),
      '/*',
      ...Array.from({ length: 300 }, (_, i) => `still comment ${i}`),
      '*/',
      'const after = 1',
    ]
    const block = blockTokens(lines, 256, lines.length, 'typescript')
    expect(block[0].every((t) => t.k === 'comment'), JSON.stringify(block[0])).toBe(true)
    const last = block[block.length - 1]
    expect(last.find((t) => t.v === 'const')?.k).toBe('keyword')
  })

  it('is wrong, and only that far, past its context', async () => {
    // The bound, written down: a comment open for more than CONTEXT_LINES
    // before a block is code to that block until it closes.
    await loadGrammar('typescript')
    const n = CONTEXT_LINES + 300
    const lines = ['/*', ...Array.from({ length: n }, () => 'const inside = 1'), '*/']
    const block = blockTokens(lines, n - 10, n, 'typescript')
    expect(block[0].some((t) => t.k === 'keyword')).toBe(true)
  })

  it('returns exactly the lines asked for', async () => {
    await loadGrammar('typescript')
    const lines = ['a', '', '', 'b', '']
    expect(blockTokens(lines, 1, 5, 'typescript')).toHaveLength(4)
  })
})
