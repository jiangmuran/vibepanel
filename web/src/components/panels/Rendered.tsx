import { useEffect, useState, type JSX } from 'react'

import { grammarFor } from '../viewer/grammars'
import { loadGrammar, tokenLines, type TokKind } from '../viewer/tokens'
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
 * The colour for a token kind: the same theme tokens the viewer's line body
 * draws with (viewer/TextBody.tsx), pinned at 4.5:1 by contrast.test.ts.
 *
 * A switch and not a lookup table, because a table's values are strings with
 * spaces in them and i18n.untranslated.test.ts reads those as English phrases
 * that should have come from the dictionary. These are class names.
 */
function tone(k: TokKind): string {
  switch (k) {
    case 'comment':
      return 'text-[var(--vp-tok-comment)]'
    case 'keyword':
      return 'text-[var(--vp-tok-keyword)]'
    case 'string':
      return 'text-[var(--vp-tok-string)]'
    case 'number':
      return 'text-[var(--vp-tok-number)]'
    case 'function':
      return 'text-[var(--vp-tok-function)]'
    case 'type':
      return 'text-[var(--vp-tok-type)]'
    case 'property':
      return 'text-[var(--vp-tok-property)]'
    case 'tag':
      return 'text-[var(--vp-tok-tag)]'
    case 'regex':
      return 'text-[var(--vp-tok-regex)]'
    case 'variable':
      return 'text-[var(--vp-tok-variable)]'
    case 'punct':
      return 'text-[var(--vp-tok-punct)]'
    case 'inserted':
      return 'text-[var(--vp-tok-inserted)]'
    case 'deleted':
      return 'text-[var(--vp-tok-deleted)]'
    default:
      return ''
  }
}

/**
 * A fenced code block in a document, with line numbers.
 *
 * The fence's language (```ts, ```python, ```sh) is read the way a file's
 * extension is, through the viewer's grammar table, and the grammar loads the
 * first time a block in it is drawn; until then, and for a language nobody
 * listed, the block is plain. The numbers are a separate column, so copying
 * the code does not bring them along.
 */
export function Code({ code, lang }: { code: string; lang?: string }) {
  const grammar = lang ? grammarFor(`x.${lang.toLowerCase()}`) ?? grammarFor(lang) : null
  const [loaded, setLoaded] = useState<string | null>(null)
  useEffect(() => {
    if (!grammar) return
    let live = true
    loadGrammar(grammar).then(
      () => live && setLoaded(grammar),
      () => {
        /* plain */
      },
    )
    return () => {
      live = false
    }
  }, [grammar])
  const body = code.replace(/\n$/, '')
  const lines =
    grammar && loaded === grammar
      ? tokenLines(body, grammar)
      : body.split('\n').map((l) => (l ? [{ k: 'plain' as const, v: l }] : []))

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
