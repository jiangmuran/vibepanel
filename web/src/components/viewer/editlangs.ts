/**
 * The languages the editor colours, by file name: the ten commonest grammars
 * CodeMirror has, each its own chunk, fetched the first time a file in it is
 * edited. Anything else is edited as plain text, which is still the text.
 *
 * Its own table rather than grammars.ts: that one names Prism's grammars,
 * about three hundred of them, and the editor has ten.
 */

import { lineEndings } from './text'

export type EditLang =
  | 'javascript'
  | 'jsx'
  | 'typescript'
  | 'tsx'
  | 'python'
  | 'go'
  | 'rust'
  | 'json'
  | 'markdown'
  | 'yaml'
  | 'html'
  | 'css'
  | 'cpp'

const BY_EXTENSION: Record<string, EditLang> = {
  js: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  jsx: 'jsx',
  ts: 'typescript',
  mts: 'typescript',
  cts: 'typescript',
  tsx: 'tsx',
  py: 'python',
  pyi: 'python',
  go: 'go',
  rs: 'rust',
  json: 'json',
  jsonc: 'json',
  ipynb: 'json',
  md: 'markdown',
  markdown: 'markdown',
  yaml: 'yaml',
  yml: 'yaml',
  html: 'html',
  htm: 'html',
  css: 'css',
  c: 'cpp',
  h: 'cpp',
  cc: 'cpp',
  cpp: 'cpp',
  cxx: 'cpp',
  hpp: 'cpp',
  hh: 'cpp',
}

export function editLanguage(name: string): EditLang | null {
  const base = (name.split('/').pop() ?? name).toLowerCase()
  const dot = base.lastIndexOf('.')
  if (dot < 0) return null
  const ext = base.slice(dot + 1)
  // Own keys only, for the reason grammars.ts gives: `a.constructor`.
  return Object.hasOwn(BY_EXTENSION, ext) ? BY_EXTENSION[ext] : null
}

/**
 * The line ending to write back: CodeMirror keeps lines and joins them with
 * one separator, so a CRLF file edited with the default would be saved as LF
 * everywhere. Null for a file that mixes them -- any one choice rewrites
 * lines nobody touched -- which is a file the editor does not open.
 */
export function lineSeparator(text: string): '\n' | '\r\n' | '\r' | null {
  switch (lineEndings(text)) {
    case 'mixed':
      return null
    case 'CRLF':
      return '\r\n'
    case 'CR':
      return '\r'
    default:
      return '\n'
  }
}
