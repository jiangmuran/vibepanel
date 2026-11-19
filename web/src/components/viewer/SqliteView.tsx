import { useMemo } from 'react'
import { Loader2 } from 'lucide-react'

import type { DBListing } from '../../protocol/listings'
import { t } from '../../i18n'
import { safeText } from '../text'
import { CsvTable } from './CsvTable'
import { tableRows } from './sqlite'

/**
 * A database: its tables as a strip, each with its row count, and the first
 * rows of the one chosen in the table body. ← and → step along the strip
 * (the viewer's keys); choosing one asks the server for its rows, and while
 * it does the rows shown are the last table's, marked as waiting.
 */
export default function SqliteView({
  db,
  shown,
  failed,
  pending,
  onChoose,
  query,
  current,
  onCount,
}: {
  db: DBListing
  /** The table chosen; db.table once its rows have arrived. */
  shown: string
  /** Why the chosen table's rows are not here, in words. */
  failed: string | null
  pending: boolean
  onChoose: (name: string) => void
  query: string
  current: number
  onCount: (n: number, capped: boolean) => void
}) {
  const body = useMemo(() => tableRows(db), [db])
  const titles = useMemo(() => db.columns.map((c) => c.type), [db])
  if (db.tables.length === 0) {
    return (
      <p data-testid="sqlite-empty" className="flex flex-1 items-center justify-center px-6 py-12 text-vp-base text-ink-2">
        {t('db.empty')}
      </p>
    )
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div role="tablist" aria-label={t('db.tables')} data-testid="sqlite-tables" className="flex shrink-0 gap-1 overflow-x-auto border-b border-hairline px-2 py-1.5">
        {db.tables.map((tb) => (
          <button
            key={tb.name}
            type="button"
            role="tab"
            aria-selected={tb.name === shown}
            onClick={() => tb.name !== shown && onChoose(tb.name)}
            data-testid="sqlite-table"
            data-name={tb.name}
            title={tb.columns.map((c) => (c.type ? `${c.name} ${c.type}` : c.name)).join('\n')}
            className="vp-tab shrink-0 text-vp-xs"
          >
            <span className="max-w-[16rem] truncate">{safeText(tb.name)}</span>
            <span className="tabular text-ink-3">
              {tb.kind === 'view' ? t('db.view') : tb.rows < 0 ? '?' : tb.rows.toLocaleString()}
            </span>
          </button>
        ))}
      </div>
      {failed !== null ? (
        <p data-testid="sqlite-failed" className="flex flex-1 items-center justify-center px-6 py-12 text-center text-vp-base text-ink-2">
          {safeText(failed)}
        </p>
      ) : (
        // `data-table` names the table these rows belong to, which is not
        // always the one selected in the strip: the strip flips on the click
        // and the rows arrive a fetch later. A check that read the body as
        // soon as the strip had flipped read the previous table's rows.
        <div className={`flex min-h-0 flex-1 flex-col ${pending ? 'opacity-60' : ''}`} aria-busy={pending} data-testid="sqlite-body" data-table={db.table}>
          {pending && (
            <p className="flex shrink-0 items-center gap-1.5 px-3 py-1 text-vp-xs text-ink-2">
              <Loader2 size={11} className="animate-spin" />
              {t('preview.loading')}
            </p>
          )}
          <CsvTable rows={body.rows} more={db.more} query={query} current={current} onCount={onCount} stand={body.stand} titles={titles} />
          {db.rows.length === 0 && (
            <p data-testid="sqlite-no-rows" className="shrink-0 px-3 py-2 text-vp-sm text-ink-2">
              {t('db.noRows')}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

/** How many tables, how many rows of the one shown, and what was left out. */
export function SqliteFooter({ db, copied, source, lines }: { db: DBListing; copied: boolean; source: boolean; lines: number }) {
  const chosen = db.tables.find((tb) => tb.name === db.table)
  // The server stops at a column bound and a text bound as well as at 200
  // rows; which one is read from the fields, not from its English note.
  const columnsCut = chosen !== undefined && db.columns.length < chosen.columns.length
  const bytesCut = db.note !== '' && !columnsCut && db.more && db.rows.length < 200
  return (
    <footer
      data-testid="viewer-footer"
      className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2"
    >
      <span data-testid="viewer-format">SQLite</span>
      <span aria-hidden="true">·</span>
      <span className="tabular" data-testid="sqlite-count">
        {t('db.count', { n: db.tables.length.toLocaleString() })}
      </span>
      {source ? (
        <>
          <span aria-hidden="true">·</span>
          <span className="tabular" data-testid="viewer-lines">
            {t('preview.lines', { n: lines.toLocaleString() })}
          </span>
        </>
      ) : (
        chosen && (
          <>
            <span aria-hidden="true">·</span>
            <span className="tabular" data-testid="sqlite-rows">
              {db.more
                ? chosen.rows >= 0
                  ? t('db.rowsOf', { shown: db.rows.length.toLocaleString(), n: chosen.rows.toLocaleString() })
                  : t('db.rowsFirst', { shown: db.rows.length.toLocaleString() })
                : t('csv.rows', { n: db.rows.length.toLocaleString() })}
            </span>
          </>
        )
      )}
      {(columnsCut || bytesCut) && (
        <span data-testid="sqlite-note" title={db.note} className="min-w-0 flex-1 text-right">
          {columnsCut
            ? t('db.columnsCut', { shown: db.columns.length.toLocaleString(), n: chosen.columns.length.toLocaleString() })
            : t('db.textCut')}
        </span>
      )}
      {copied && <span className="text-ink">{t('viewer.pathCopied')}</span>}
    </footer>
  )
}
