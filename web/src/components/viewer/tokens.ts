import './prismSetup'
import Prism from 'prismjs'

import { loadGrammar } from './grammars'

// Prism's grammar modules extend a global `Prism`, which an ES module import
// does not create in a browser.
;(globalThis as unknown as { Prism: typeof Prism }).Prism = Prism

/**
 * The kinds a token is drawn as: one theme token each, `--vp-tok-<kind>`.
 *
 * Prism names hundreds of token types across its grammars; drawn one colour
 * each they are noise, and a reader tells code apart by a dozen. `plain` is
 * the ink.
 */
export type TokKind =
  | 'plain'
  | 'comment'
  | 'keyword'
  | 'string'
  | 'number'
  | 'function'
  | 'type'
  | 'property'
  | 'tag'
  | 'regex'
  | 'variable'
  | 'punct'
  | 'inserted'
  | 'deleted'

export interface Tok {
  k: TokKind
  v: string
}

const KIND: Record<string, TokKind> = {
  comment: 'comment', prolog: 'comment', doctype: 'comment', cdata: 'comment', shebang: 'comment',
  keyword: 'keyword', rule: 'keyword', atrule: 'keyword', important: 'keyword', directive: 'keyword',
  'control-flow': 'keyword', module: 'keyword', 'keyword-array': 'keyword',
  string: 'string', char: 'string', 'template-string': 'string', 'attr-value': 'string', url: 'string',
  'string-property': 'property',
  number: 'number', boolean: 'number', constant: 'number', unit: 'number', hexcode: 'number',
  function: 'function', 'function-variable': 'function', 'function-name': 'function', macro: 'function',
  'class-name': 'type', builtin: 'type', 'type-annotation': 'type', namespace: 'type', annotation: 'type',
  decorator: 'type', symbol: 'type',
  property: 'property', 'attr-name': 'property', key: 'property', selector: 'property', label: 'property',
  tag: 'tag', entity: 'tag',
  regex: 'regex',
  variable: 'variable', parameter: 'variable', interpolation: 'variable',
  punctuation: 'punct', operator: 'punct',
  inserted: 'inserted', 'inserted-sign': 'inserted', deleted: 'deleted', 'deleted-sign': 'deleted',
}

/** A Prism token's kind: its type, else the first alias that has one. */
function kindOf(t: Prism.Token, inherited: TokKind): TokKind {
  const direct = KIND[t.type]
  if (direct) return direct
  const aliases = Array.isArray(t.alias) ? t.alias : t.alias ? [t.alias] : []
  for (const a of aliases) if (KIND[a]) return KIND[a]
  return inherited
}

/**
 * Tokens, cut into lines.
 *
 * Prism's output is a tree -- a template string holds an interpolation that
 * holds code -- and it is flattened here, each leaf taking the nearest kind
 * above it that has one. Then cut at the newlines, because the body draws
 * rows: a token that spans lines becomes one per line, each keeping its kind.
 * Every line is present, empty ones included, so the index is the line.
 *
 * React elements are made from these by the body; nothing here or there ever
 * produces HTML. Prism's own `highlight` returns an HTML string, and that is
 * the one function of it this never calls (docs/file-preview.md, "Types").
 */
export function tokenLines(text: string, lang: string): Tok[][] {
  const grammar = Prism.languages[lang]
  const lines: Tok[][] = [[]]
  const push = (k: TokKind, v: string) => {
    const parts = v.split('\n')
    parts.forEach((part, i) => {
      if (i > 0) lines.push([])
      if (part === '') return
      const line = lines[lines.length - 1]
      const last = line[line.length - 1]
      // Adjacent pieces of one kind are one span, not several.
      if (last && last.k === k) last.v += part
      else line.push({ k, v: part })
    })
  }
  const walk = (stream: Prism.TokenStream, inherited: TokKind) => {
    if (typeof stream === 'string') {
      push(inherited, stream)
      return
    }
    if (Array.isArray(stream)) {
      for (const s of stream) walk(s, inherited)
      return
    }
    walk(stream.content, kindOf(stream, inherited))
  }
  if (grammar) walk(Prism.tokenize(text, grammar), 'plain')
  else push('plain', text)
  // A text ending in a newline has made one empty line past the end.
  if (lines.length > 1 && lines[lines.length - 1].length === 0 && text.endsWith('\n')) lines.pop()
  return lines
}

/**
 * Lines of context tokenised before a block.
 *
 * Prism keeps no state between calls, so a block tokenised on its own starts
 * in whatever state it starts in: the middle of a block comment, a template
 * string or a heredoc is code to it. Each block is tokenised with up to this
 * many lines before it and those lines' tokens are dropped. A construct open
 * for longer than this is coloured wrongly from the block's start until it
 * closes -- a cosmetic error, and a bounded one, against tokenising 50,000
 * lines before the first one is drawn.
 */
export const CONTEXT_LINES = 200

/** The tokens for lines [start, end), tokenised with their context. */
export function blockTokens(lines: readonly string[], start: number, end: number, lang: string): Tok[][] {
  const from = Math.max(0, start - CONTEXT_LINES)
  const all = tokenLines(lines.slice(from, end).join('\n'), lang)
  const out = all.slice(start - from, end - from)
  // Trailing empty lines produce no tokens and may be missing at the end.
  while (out.length < end - start) out.push([])
  return out
}

export { loadGrammar }
