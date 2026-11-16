import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'

import { safeBody } from '../text'
import { t } from '../../i18n'
import { initialExpanded, revealing, treeMatches, visibleRows, type Row } from './json'

/** Rows per group the browser skips while it is off screen. */
const GROUP = 200

/**
 * A JSON document as a tree: objects and arrays open and close, keys and
 * values are coloured by kind with the code tokens, strings are shown as the
 * text they hold, long arrays fold past two hundred. The viewer's find bar
 * searches it: `query` and `current` come in, the count goes back out, and the
 * current match is opened and brought into view.
 */
export function JsonTree({
  value,
  query,
  current,
  onCount,
}: {
  value: unknown
  query: string
  current: number
  onCount: (n: number, capped: boolean) => void
}) {
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => initialExpanded(value))
  const found = useMemo(() => treeMatches(value, query), [value, query])
  useEffect(() => {
    onCount(found.paths.length, found.capped)
  }, [found, onCount])

  // The current match is opened when it changes: its ancestors, and the fold
  // of any long array it is beyond. Adjusted while rendering, guarded, which
  // is React's way of following a prop with state.
  const target = current >= 0 ? found.paths[current] : undefined
  const [revealed, setRevealed] = useState<string | undefined>(undefined)
  if (target !== revealed) {
    setRevealed(target)
    if (target !== undefined) {
      const need = revealing(target).filter((p) => !expanded.has(p))
      if (need.length) setExpanded((prev) => new Set([...prev, ...need]))
    }
  }
  useLayoutEffect(() => {
    if (target === undefined) return
    document.querySelector(`[data-json-path="${CSS.escape(target)}"]`)?.scrollIntoView({ block: 'center' })
  }, [target, expanded])

  const rows = useMemo(() => visibleRows(value, expanded), [value, expanded])
  const hits = useMemo(() => new Set(found.paths), [found])
  const boxRef = useRef<HTMLDivElement | null>(null)
  const toggle = (path: string) => {
    // The fold's button is replaced by the rows it stood for, and the focus
    // would fall to the page with it: the next key -- Ctrl+F -- reached
    // nothing. The tree keeps it.
    if (path.endsWith('#all')) boxRef.current?.focus()
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const groups: Row[][] = []
  for (let i = 0; i < rows.length; i += GROUP) groups.push(rows.slice(i, i + GROUP))
  return (
    <div ref={boxRef} data-testid="viewer-json" className="min-h-0 flex-1 overflow-auto py-2 font-mono text-vp-sm leading-5 text-ink" tabIndex={0}>
      {groups.map((g, gi) => (
        <div key={gi} style={{ contentVisibility: 'auto', containIntrinsicSize: `auto ${g.length * 20}px` }}>
          {g.map((r) => (
            <JsonRow key={r.path} row={r} hit={hits.has(r.path)} isCurrent={r.path === target} onToggle={toggle} />
          ))}
        </div>
      ))}
    </div>
  )
}

function JsonRow({ row, hit, isCurrent, onToggle }: { row: Row; hit: boolean; isCurrent: boolean; onToggle: (p: string) => void }) {
  // Room for the toggle, which sits a rem to the left of the row's text: at
  // 0.75rem the root's was cut off by the dialog's edge.
  const pad = { paddingLeft: `calc(${row.depth * 1.25}rem + 1.5rem)` }
  if (row.folded !== undefined) {
    return (
      <div style={pad} className="py-px">
        <button type="button" onClick={() => onToggle(row.path)} data-testid="json-more" className="text-ink-2 underline decoration-dotted hover:text-ink">
          {t('json.more', { n: row.folded.toLocaleString() })}
        </button>
      </div>
    )
  }
  const container = row.type === 'object' || row.type === 'array'
  // A match is tinted, and the current one outlined as well: colour is never
  // the only carrier (red line 4).
  const mark = isCurrent
    ? 'bg-[var(--vp-find-current)] outline outline-2 -outline-offset-2 outline-[var(--vp-find-ring)]'
    : hit
      ? 'bg-[var(--vp-find)]'
      : ''
  return (
    <div
      style={pad}
      data-json-path={row.path}
      data-match={hit ? 'true' : undefined}
      data-current={isCurrent ? 'true' : undefined}
      className={`flex items-start gap-1 rounded-md py-px pr-3 ${mark}`}
    >
      {container ? (
        <button
          type="button"
          onClick={() => onToggle(row.path)}
          aria-expanded={row.expanded}
          aria-label={row.expanded ? t('json.close') : t('json.open')}
          data-testid="json-toggle"
          className="-ml-4 mt-0.5 shrink-0 text-ink-3 hover:text-ink"
        >
          {row.expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </button>
      ) : null}
      <span className="min-w-0 whitespace-pre-wrap [overflow-wrap:anywhere]">
        {row.key !== null && (
          <>
            <span className={typeof row.key === 'number' ? 'text-ink-3' : 'text-[var(--vp-tok-property)]'}>
              {typeof row.key === 'number' ? row.key : safeBody(JSON.stringify(row.key))}
            </span>
            <span className="text-[var(--vp-tok-punct)]">: </span>
          </>
        )}
        <Value row={row} />
      </span>
    </div>
  )
}

function Value({ row }: { row: Row }) {
  switch (row.type) {
    case 'object':
      return row.expanded ? (
        <span className="text-[var(--vp-tok-punct)]">{'{'}</span>
      ) : (
        <span className="text-ink-3">{'{…}'} {t('json.keys', { n: row.size.toLocaleString() })}</span>
      )
    case 'array':
      return row.expanded ? (
        <span className="text-[var(--vp-tok-punct)]">[</span>
      ) : (
        <span className="text-ink-3">[…] {t('json.items', { n: row.size.toLocaleString() })}</span>
      )
    case 'string':
      // The text the string holds, quoted: escapes are already undone by the
      // parse, so a `\n` inside it is a line break here, as it is to whoever
      // reads the value.
      return <span className="text-[var(--vp-tok-string)]">&quot;{safeBody(row.value as string)}&quot;</span>
    case 'number':
    case 'boolean':
      return <span className="text-[var(--vp-tok-number)]">{String(row.value)}</span>
    case 'null':
      return <span className="text-[var(--vp-tok-keyword)]">null</span>
  }
}
