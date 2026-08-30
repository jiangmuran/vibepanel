/**
 * Code, tokenised into spans. One tokeniser for every language.
 *
 * Deliberately simple, and the shape of the simplification is the point: this
 * finds comments, strings, numbers and a shared keyword set, and it does not
 * parse anything. A file preview is read to see *what a file is*, and the four
 * things above are what make that legible at a glance. Telling a type from a
 * function name needs a grammar per language, and a grammar per language needs
 * a dependency, which for this is a dependency to keep current forever.
 *
 * So: wrong in the ways a lexer with no grammar is wrong. `class` inside an
 * ordinary sentence in a `.txt` file is coloured as a keyword. That is a
 * cosmetic error in a preview, and it is the whole cost.
 *
 * The one thing it must not get wrong is the *order*: a `//` inside a string
 * is not a comment, and a quote inside a comment does not open a string. That
 * is why this is one pass over the source rather than a series of regexes over
 * the whole text, which is the version that produces a file where everything
 * after one apostrophe is a string.
 */

export type Tok = { k: Kind; v: string }
export type Kind = 'plain' | 'comment' | 'string' | 'number' | 'keyword'

/**
 * One keyword set, shared.
 *
 * The union of what the languages in front of this panel use, which is not the
 * same as being correct for any one of them: `func` is not a Python keyword and
 * `def` is not a Go one, and colouring either in the other's file is a wrong
 * colour on a word that is almost certainly a definition anyway.
 */
const KEYWORDS = new Set([
  // Shared shape
  'if', 'else', 'for', 'while', 'do', 'switch', 'case', 'default', 'break', 'continue',
  'return', 'goto', 'try', 'catch', 'finally', 'throw', 'raise', 'with', 'as', 'in', 'is',
  'and', 'or', 'not', 'new', 'delete', 'typeof', 'instanceof', 'await', 'async', 'yield',
  // Declarations
  'func', 'function', 'def', 'fn', 'class', 'struct', 'interface', 'enum', 'trait', 'impl',
  'type', 'var', 'let', 'const', 'static', 'public', 'private', 'protected', 'export',
  'import', 'from', 'package', 'module', 'use', 'require', 'include', 'namespace', 'extends',
  'implements', 'override', 'abstract', 'final', 'lambda', 'pass', 'global', 'nonlocal',
  // Values
  'true', 'false', 'null', 'nil', 'None', 'True', 'False', 'undefined', 'self', 'this',
  'super', 'void',
  // Go and Rust
  'go', 'defer', 'chan', 'select', 'range', 'map', 'make', 'mut', 'pub', 'match', 'where',
  // Shell
  'echo', 'fi', 'esac', 'then', 'elif', 'done', 'local', 'set', 'unset', 'source',
])

/** Line comment markers, by the languages that use each. */
function lineComment(lang: string): string[] {
  switch (lang) {
    case 'py': case 'python': case 'sh': case 'bash': case 'zsh': case 'yaml': case 'yml':
    case 'toml': case 'conf': case 'ini': case 'dockerfile': case 'makefile': case 'make':
    case 'ruby': case 'rb': case 'r':
      return ['#']
    case 'sql':
      return ['--']
    case 'lua':
      return ['--']
    default:
      // Both, so a `#!/bin/sh` at the top of an unlabelled file still reads as
      // a comment and `//` still works in the C-shaped majority.
      return ['//', '#']
  }
}

/** Whether the language has /* … *\/ blocks. */
function hasBlockComment(lang: string): boolean {
  switch (lang) {
    case 'py': case 'python': case 'sh': case 'bash': case 'zsh': case 'yaml': case 'yml':
    case 'toml': case 'ini': case 'conf': case 'makefile': case 'make':
      return false
    default:
      return true
  }
}

/**
 * Tokenise. Never throws and never drops a byte: the concatenation of every
 * token's text is the input, which is what makes it safe to render as spans --
 * a highlighter that loses a character is a preview that lies about the file.
 */
export function highlight(code: string, lang = ''): Tok[] {
  const out: Tok[] = []
  const line = lineComment(lang)
  const block = hasBlockComment(lang)
  let plain = ''
  const flush = () => {
    if (plain !== '') {
      out.push({ k: 'plain', v: plain })
      plain = ''
    }
  }
  const push = (k: Kind, v: string) => {
    flush()
    out.push({ k, v })
  }

  let i = 0
  while (i < code.length) {
    const rest = code.slice(i)

    if (block && rest.startsWith('/*')) {
      const end = code.indexOf('*/', i + 2)
      const stop = end === -1 ? code.length : end + 2
      push('comment', code.slice(i, stop))
      i = stop
      continue
    }

    const marker = line.find((m) => rest.startsWith(m))
    if (marker !== undefined) {
      const nl = code.indexOf('\n', i)
      const stop = nl === -1 ? code.length : nl
      push('comment', code.slice(i, stop))
      i = stop
      continue
    }

    const q = code[i]
    if (q === '"' || q === "'" || q === '`') {
      let j = i + 1
      while (j < code.length) {
        if (code[j] === '\\') {
          j += 2
          continue
        }
        if (code[j] === q) {
          j++
          break
        }
        // A single-quoted string does not span lines in any language here, and
        // an unterminated one is common in a file being edited. Stopping at the
        // newline keeps one stray apostrophe from colouring the rest of the
        // file.
        if (code[j] === '\n' && q !== '`') break
        j++
      }
      push('string', code.slice(i, j))
      i = j
      continue
    }

    if (/[0-9]/.test(q) && !/[A-Za-z0-9_$]/.test(code[i - 1] ?? '')) {
      const m = /^(0[xXbBoO][0-9a-fA-F_]+|\d[\d_]*(?:\.\d[\d_]*)?(?:[eE][+-]?\d+)?)/.exec(rest)
      if (m) {
        push('number', m[0])
        i += m[0].length
        continue
      }
    }

    if (/[A-Za-z_$]/.test(q)) {
      const m = /^[A-Za-z_$][A-Za-z0-9_$]*/.exec(rest)
      if (m) {
        if (KEYWORDS.has(m[0])) {
          push('keyword', m[0])
        } else {
          plain += m[0]
        }
        i += m[0].length
        continue
      }
    }

    plain += q
    i++
  }
  flush()
  return out
}

/**
 * The language for a filename, as a hint for the tokeniser.
 *
 * Only used to choose comment markers, so it is a small map and an unknown
 * extension is not a problem -- the default handles the C-shaped majority.
 */
export function langOf(name: string): string {
  const base = name.toLowerCase()
  if (base === 'makefile' || base.endsWith('/makefile')) return 'makefile'
  if (base === 'dockerfile' || base.endsWith('/dockerfile')) return 'dockerfile'
  const dot = base.lastIndexOf('.')
  return dot <= 0 ? '' : base.slice(dot + 1)
}

/**
 * Whether a file is worth highlighting at all.
 *
 * Prose is not: a paragraph with `class` and `type` in it comes out speckled,
 * which is worse than plain. Everything else that previews as text gets it,
 * because guessing "is this code" from an extension list means a file type
 * nobody listed reads as plain forever.
 */
export function shouldHighlight(name: string): boolean {
  return !/\.(txt|log|csv|tsv|md|markdown|mdown|mkd)$/i.test(name)
}

/**
 * The same tokens, split into lines.
 *
 * Highlighting each line on its own is the obvious implementation and it is
 * wrong in exactly the way the one-pass design exists to avoid: a `/* … *\/`
 * spanning three lines is a comment on the first and code on the other two,
 * and a backtick string is worse. So the file is tokenised once and the tokens
 * are cut at the newlines afterwards.
 *
 * A token that spans lines becomes one token per line, each keeping its kind.
 * Every line is present, including empty ones, so the array's index is the
 * line number.
 */
export function highlightLines(code: string, lang = ''): Tok[][] {
  const lines: Tok[][] = [[]]
  for (const tok of highlight(code, lang)) {
    const parts = tok.v.split('\n')
    parts.forEach((part, i) => {
      if (i > 0) lines.push([])
      if (part !== '') lines[lines.length - 1].push({ k: tok.k, v: part })
    })
  }
  // A file ending in a newline has produced one empty line past the end.
  if (lines.length > 1 && lines[lines.length - 1].length === 0) lines.pop()
  return lines
}
