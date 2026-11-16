import { useEffect, useState } from 'react'

import { safeBody } from '../text'
import { grammarFor } from './grammars'
import { loadGrammar, tokenLines, type TokKind } from './tokens'

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
