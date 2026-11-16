import { useEffect, useLayoutEffect, useMemo, useRef } from 'react'

import { safeBody } from '../text'
import { t } from '../../i18n'

/** Rows per group the browser skips while it is off screen. */
const GROUP = 200

/**
 * Delimited text as a table: the first row is the header and stays at the top,
 * rows are numbered and the numbers stay at the left. The viewer's find bar
 * searches the cells: `query` and `current` come in, the count goes back out.
 *
 * Ten thousand rows is a lot of cells for a browser to lay out, so the body is
 * groups of rows under `content-visibility`, the line body's blocks for a
 * table.
 */
export function CsvTable({
  rows,
  more,
  query,
  current,
  onCount,
}: {
  rows: readonly string[][]
  more: boolean
  query: string
  current: number
  onCount: (n: number, capped: boolean) => void
}) {
  const [head, ...body] = rows
  const width = Math.max(0, ...rows.map((r) => r.length))
  // Cells that hold the query, in reading order, as [row, column] in `body`.
  // Smart case, as everywhere else in the viewer.
  const found = useMemo(() => {
    const out: [number, number][] = []
    if (query === '') return out
    const exact = query !== query.toLowerCase()
    const needle = exact ? query : query.toLowerCase()
    const all = rows
    all.forEach((r, ri) =>
      r.forEach((c, ci) => {
        if ((exact ? c : c.toLowerCase()).includes(needle)) out.push([ri, ci])
      }),
    )
    return out
  }, [rows, query])
  useEffect(() => {
    onCount(found.length, false)
  }, [found, onCount])

  const at = current >= 0 ? found[current] : undefined
  const key = (r: number, c: number) => `${r}:${c}`
  const hits = useMemo(() => new Set(found.map(([r, c]) => key(r, c))), [found])
  const boxRef = useRef<HTMLDivElement | null>(null)
  useLayoutEffect(() => {
    if (!at) return
    boxRef.current?.querySelector(`[data-cell="${key(at[0], at[1])}"]`)?.scrollIntoView({ block: 'center', inline: 'nearest' })
  }, [at])

  const cellClass = (r: number, c: number) => {
    if (at && at[0] === r && at[1] === c) {
      return 'bg-[var(--vp-find-current)] outline outline-2 -outline-offset-2 outline-[var(--vp-find-ring)]'
    }
    return hits.has(key(r, c)) ? 'bg-[var(--vp-find)]' : ''
  }
  const groups: string[][][] = []
  for (let i = 0; i < body.length; i += GROUP) groups.push(body.slice(i, i + GROUP))

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div ref={boxRef} data-testid="viewer-table" className="min-h-0 flex-1 overflow-auto" tabIndex={0}>
        <table className="border-separate border-spacing-0 font-mono text-vp-sm text-ink">
          <thead className="sticky top-0 z-10 bg-surface-2">
            <tr>
              <th className="sticky left-0 z-10 border-b border-r border-hairline bg-surface-2 px-2 py-1" aria-label={t('csv.row')} />
              {Array.from({ length: width }, (_, c) => (
                <th
                  key={c}
                  data-cell={key(0, c)}
                  data-match={hits.has(key(0, c)) ? 'true' : undefined}
                  data-current={at && at[0] === 0 && at[1] === c ? 'true' : undefined}
                  className={`border-b border-r border-hairline px-3 py-1 text-left font-semibold whitespace-pre-wrap ${cellClass(0, c)}`}
                >
                  {safeBody(head?.[c] ?? '')}
                </th>
              ))}
            </tr>
          </thead>
          {groups.map((g, gi) => (
            <tbody key={gi} style={{ contentVisibility: 'auto', containIntrinsicSize: `auto ${g.length * 26}px` }}>
              {g.map((row, i) => {
                const r = gi * GROUP + i + 1
                return (
                  <tr key={r}>
                    <td className="tabular sticky left-0 border-b border-r border-hairline bg-surface px-2 py-1 text-right text-ink-3 select-none">
                      {r}
                    </td>
                    {Array.from({ length: width }, (_, c) => (
                      <td
                        key={c}
                        data-cell={key(r, c)}
                        data-match={hits.has(key(r, c)) ? 'true' : undefined}
                        data-current={at && at[0] === r && at[1] === c ? 'true' : undefined}
                        className={`max-w-[32rem] border-b border-r border-hairline px-3 py-1 align-top whitespace-pre-wrap [overflow-wrap:anywhere] ${cellClass(r, c)}`}
                      >
                        {safeBody(row[c] ?? '')}
                      </td>
                    ))}
                  </tr>
                )
              })}
            </tbody>
          ))}
        </table>
      </div>
      {more && (
        <p data-testid="csv-more" className="shrink-0 border-t border-hairline px-3 py-1 text-vp-xs text-ink-2">
          {t('csv.more', { n: body.length.toLocaleString() })}
        </p>
      )}
    </div>
  )
}
