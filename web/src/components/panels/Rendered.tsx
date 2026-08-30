import type { JSX } from 'react'

import type { Tok } from './highlight'
import { highlightLines, langOf, shouldHighlight } from './highlight'
import type { Block, Inline } from './markdown'
import { parseMarkdown } from './markdown'
import { safeBody } from '../text'

/**
 * A markdown document, and highlighted code, as React elements.
 *
 * No `dangerouslySetInnerHTML` anywhere in this file, and that is the whole
 * design rather than a style preference -- see the comment at the top of
 * markdown.ts. Nothing here ever builds an HTML string, so there is nothing
 * for a document out of somebody's repository to be injected into.
 *
 * Everything still goes through `safeBody`, which is what every other name and
 * path in the panel gets minus the two characters a document is made of: a
 * bidi override or a stray control character in a README is a line that reads
 * as something other than what it says, whether or not it can execute.
 *
 * `safeText`, the one the file tree uses, is the wrong one here and shipped
 * that way. It replaces every C0 control, which for a name is right and for a
 * body means every tab in every indented line and every newline inside a
 * paragraph came out as a black diamond.
 */

function inline(kids: Inline[], keyBase = ''): JSX.Element[] {
  return kids.map((n, i) => {
    const k = `${keyBase}${i}`
    switch (n.t) {
      case 'text':
        return <span key={k}>{safeBody(n.v)}</span>
      case 'code':
        return (
          // The project's own radius and its own type scale, not Tailwind's
          // bare steps or an arbitrary size. scale.test.ts refuses both, and it
          // is right to: a design system with an escape hatch beside it is two
          // design systems.
          <code key={k} className="rounded-vp bg-surface-2 px-1 py-0.5 font-mono text-vp-sm text-ink">
            {safeBody(n.v)}
          </code>
        )
      case 'strong':
        return <strong key={k} className="font-semibold text-ink">{inline(n.kids, `${k}.`)}</strong>
      case 'em':
        return <em key={k}>{inline(n.kids, `${k}.`)}</em>
      case 'del':
        return <del key={k} className="text-ink-2">{inline(n.kids, `${k}.`)}</del>
      case 'link':
        return (
          // A new tab, and `noopener`: the target gets no handle on the window
          // this panel is in. The scheme was already narrowed to http, https
          // and mailto when it was parsed.
          <a
            key={k}
            href={n.href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-accent underline underline-offset-2"
          >
            {inline(n.kids, `${k}.`)}
          </a>
        )
    }
  })
}

const HEADING = ['text-vp-lg', 'text-vp-md', 'text-vp-base', 'text-vp-base', 'text-vp-sm', 'text-vp-sm']

function block(b: Block, key: string): JSX.Element {
  switch (b.t) {
    case 'h': {
      const Tag = `h${Math.min(6, b.level)}` as 'h1'
      return (
        <Tag
          key={key}
          className={`mt-4 mb-2 font-semibold text-ink first:mt-0 ${HEADING[b.level - 1] ?? 'text-vp-sm'}`}
        >
          {inline(b.kids, `${key}.`)}
        </Tag>
      )
    }
    case 'p':
      return (
        <p key={key} className="my-2 leading-relaxed text-ink-2">
          {inline(b.kids, `${key}.`)}
        </p>
      )
    case 'pre':
      return <Code key={key} code={b.code} lang={b.lang} />
    case 'quote':
      return (
        <blockquote key={key} className="my-2 border-l-2 border-hairline pl-3 text-ink-2">
          {b.kids.map((c, i) => block(c, `${key}.${i}`))}
        </blockquote>
      )
    case 'list': {
      const Tag = b.ordered ? 'ol' : 'ul'
      return (
        <Tag
          key={key}
          className={`my-2 pl-5 text-ink-2 ${b.ordered ? 'list-decimal' : 'list-disc'}`}
        >
          {b.items.map((item, i) => (
            // `[&>p]:my-0`: a list item's own paragraph does not need the gap
            // a standalone one does, and with it every bullet is double-spaced.
            <li key={`${key}.${i}`} className="my-0.5 [&>p]:my-0">
              {item.map((c, j) => block(c, `${key}.${i}.${j}`))}
            </li>
          ))}
        </Tag>
      )
    }
    case 'rule':
      return <hr key={key} className="my-4 border-hairline" />
    case 'table':
      return (
        // Its own scroller: a wide table must not decide the width of the
        // dialog, which is the rule the rest of the panel follows.
        <div key={key} className="my-3 overflow-x-auto rounded-vp border border-hairline">
          <table className="w-full border-collapse text-vp-sm">
            <thead>
              <tr>
                {b.head.map((cell, i) => (
                  <th
                    key={i}
                    className="border-b border-hairline px-2 py-1 text-left font-semibold text-ink"
                  >
                    {inline(cell, `${key}.h${i}.`)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {b.rows.map((row, r) => (
                <tr key={r}>
                  {row.map((cell, c) => (
                    <td key={c} className="border-b border-hairline px-2 py-1 align-top text-ink-2 last:border-0">
                      {inline(cell, `${key}.${r}.${c}.`)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )
  }
}

/** A markdown file. */
export function Markdown({ text }: { text: string }) {
  const blocks = parseMarkdown(text)
  return (
    <div data-testid="preview-markdown" className="px-4 py-3 text-vp-base">
      {blocks.map((b, i) => block(b, String(i)))}
    </div>
  )
}

/**
 * The colour for a token kind.
 *
 * A switch and not a lookup table, because a table's values are strings with
 * spaces in them and i18n.untranslated.test.ts reads those as English phrases
 * that should have come from the dictionary. It is right to, in general: that
 * rule caught six real ones. These are class names.
 */
function tone(k: Tok['k']): string {
  switch (k) {
    case 'comment':
      return 'text-ink-3 italic'
    case 'string':
      return 'text-state-done'
    case 'number':
      return 'text-state-waiting'
    case 'keyword':
      return 'text-accent'
    default:
      return ''
  }
}

/**
 * A code block, with line numbers.
 *
 * The numbers are a separate column rather than text in the same flow, so
 * selecting the code and copying it does not bring them along -- which is the
 * one thing that makes numbered code worse than unnumbered.
 */
export function Code({ code, lang, name }: { code: string; lang?: string; name?: string }) {
  const language = lang || (name ? langOf(name) : '')
  const plain = name !== undefined && !shouldHighlight(name)
  // Tokenised once and cut at the newlines, not line by line: a block comment
  // or a backtick string spanning lines is one token, and highlighting each
  // line on its own would end it at the first newline.
  const lines = plain
    ? code.replace(/\n$/, '').split('\n').map((l) => [{ k: 'plain' as const, v: l }])
    : highlightLines(code, language)

  return (
    <div
      data-testid="preview-code"
      className="my-2 overflow-x-auto rounded-vp border border-hairline bg-surface-2"
    >
      <table className="w-full border-collapse font-mono text-vp-sm leading-relaxed">
        <tbody>
          {lines.map((l, i) => (
            <tr key={i}>
              <td
                aria-hidden
                className="w-0 border-r border-hairline px-2 text-right align-top tabular text-ink-3 select-none"
              >
                {i + 1}
              </td>
              <td className="px-3 whitespace-pre-wrap break-words text-ink">
                {l.map((tok, j) => (
                  <span key={j} className={tone(tok.k)}>
                    {safeBody(tok.v)}
                  </span>
                ))}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
