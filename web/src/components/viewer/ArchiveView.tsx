import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ChevronDown, ChevronRight, File, Folder, TriangleAlert } from 'lucide-react'

import { archiveSummary, type ArchiveListing } from '../../protocol/listings'
import { t } from '../../i18n'
import { safeBody } from '../text'
import { formatBytes } from '../panels/preview'
import { archiveMatches, buildTree, initialOpen, stamp, visibleRows, type Row } from './archive'

/** Rows per group the browser skips while it is off screen. */
const GROUP = 200

/**
 * An archive's table of contents: directories folded, each row its name, size
 * and time. Nothing in the archive is opened -- the server sent names and
 * numbers -- so a row is something to read, not to click into. The viewer's
 * find bar searches the names: the current match's directories open and it
 * is brought into view.
 */
export default function ArchiveView({
  listing,
  query,
  current,
  onCount,
}: {
  listing: ArchiveListing
  query: string
  current: number
  onCount: (n: number, capped: boolean) => void
}) {
  const root = useMemo(() => buildTree(listing.entries), [listing])
  const [open, setOpen] = useState<ReadonlySet<string>>(() => initialOpen(root))
  const found = useMemo(() => archiveMatches(root, query), [root, query])
  useEffect(() => {
    onCount(found.length, false)
  }, [found, onCount])

  // The current match's directories are opened when it changes; adjusted
  // while rendering, as the JSON tree does.
  const target = current >= 0 ? found[current] : undefined
  const [revealed, setRevealed] = useState<string | undefined>(undefined)
  if (target?.id !== revealed) {
    setRevealed(target?.id)
    if (target) {
      const need = target.parents.filter((p) => !open.has(p))
      if (need.length) setOpen((prev) => new Set([...prev, ...need]))
    }
  }
  const boxRef = useRef<HTMLDivElement | null>(null)
  useLayoutEffect(() => {
    if (!target) return
    boxRef.current?.querySelector(`[data-entry="${CSS.escape(target.id)}"]`)?.scrollIntoView({ block: 'center' })
  }, [target, open])

  const rows = useMemo(() => visibleRows(root, open), [root, open])
  const hits = useMemo(() => new Set(found.map((f) => f.id)), [found])
  const toggle = (id: string) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  if (rows.length === 0) {
    return (
      <p data-testid="archive-empty" className="flex flex-1 items-center justify-center px-6 py-12 text-vp-base text-ink-2">
        {t('packed.empty')}
      </p>
    )
  }
  const groups: Row[][] = []
  for (let i = 0; i < rows.length; i += GROUP) groups.push(rows.slice(i, i + GROUP))
  return (
    <div ref={boxRef} data-testid="viewer-archive" role="tree" aria-label={t('packed.label')} className="min-h-0 flex-1 overflow-auto py-1 text-vp-sm text-ink" tabIndex={0}>
      {groups.map((g, gi) => (
        <div key={gi} style={{ contentVisibility: 'auto', containIntrinsicSize: `auto ${g.length * 26}px` }}>
          {g.map((r) => (
            <EntryRow key={r.node.id} row={r} hit={hits.has(r.node.id)} isCurrent={r.node.id === target?.id} onToggle={toggle} />
          ))}
        </div>
      ))}
    </div>
  )
}

function EntryRow({ row, hit, isCurrent, onToggle }: { row: Row; hit: boolean; isCurrent: boolean; onToggle: (id: string) => void }) {
  const n = row.node
  // A match is tinted, and the current one outlined as well: colour is never
  // the only carrier (red line 4).
  const mark = isCurrent
    ? 'bg-[var(--vp-find-current)] outline outline-2 -outline-offset-2 outline-[var(--vp-find-ring)]'
    : hit
      ? 'bg-[var(--vp-find)]'
      : ''
  const when = stamp(n.modified, n.wall)
  const name = (
    <>
      {n.dir ? <Folder size={13} className="shrink-0 text-ink-3" /> : <File size={13} className="shrink-0 text-ink-3" />}
      <span className={`min-w-0 [overflow-wrap:anywhere] ${n.unsafe ? 'font-mono' : ''}`}>{safeBody(n.name)}</span>
      {n.unsafe && (
        // Shape and words, not a colour: a name an extractor could be led
        // out of its directory by.
        <span data-testid="archive-unsafe" title={t('packed.unsafeHelp')} className="inline-flex shrink-0 items-center gap-0.5 rounded-md border border-hairline px-1 text-vp-xs text-ink-2">
          <TriangleAlert size={11} />
          {t('packed.unsafe')}
        </span>
      )}
    </>
  )
  return (
    <div
      role="treeitem"
      aria-level={row.depth + 1}
      aria-expanded={n.dir && !n.unsafe ? row.open : undefined}
      data-entry={n.id}
      data-match={hit ? 'true' : undefined}
      data-current={isCurrent ? 'true' : undefined}
      className={`grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 rounded-md px-3 py-0.5 @md:grid-cols-[minmax(0,1fr)_6rem_9rem] ${mark}`}
    >
      <div className="flex min-w-0 items-start gap-1.5" style={{ paddingLeft: `${row.depth * 1.25}rem` }}>
        {n.dir && !n.unsafe ? (
          <button
            type="button"
            onClick={() => onToggle(n.id)}
            data-testid="archive-toggle"
            aria-label={row.open ? t('json.close') : t('json.open')}
            className="vp-press flex min-w-0 items-start gap-1.5 rounded-md text-left hover:text-ink"
          >
            <span className="mt-0.5 shrink-0 text-ink-3">{row.open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}</span>
            {name}
          </button>
        ) : (
          <>
            <span className="w-3 shrink-0" />
            {name}
          </>
        )}
      </div>
      <span className="tabular text-right text-vp-xs text-ink-2">
        {n.dir && !n.unsafe ? t('json.items', { n: n.count.toLocaleString() }) : n.size < 0 ? '—' : formatBytes(n.size)}
      </span>
      <span className="tabular hidden text-right text-vp-xs text-ink-3 @md:block">{when ?? '—'}</span>
    </div>
  )
}

const FORMAT_NAMES: Record<ArchiveListing['format'], string> = { zip: 'ZIP', tar: 'tar', 'tar.gz': 'tar.gz', gzip: 'gzip' }

/**
 * What the archive holds and what the listing left out. The server's note is
 * English and says which bound it was; the footer says what that means here
 * from the fields, in the panel's language.
 */
export function ArchiveFooter({ listing, copied }: { listing: ArchiveListing; copied: boolean }) {
  const sum = archiveSummary(listing.entries)
  return (
    <footer
      data-testid="viewer-footer"
      className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-t border-hairline px-3 py-1.5 text-vp-xs text-ink-2"
    >
      <span data-testid="viewer-format">{FORMAT_NAMES[listing.format]}</span>
      <span aria-hidden="true">·</span>
      <span className="tabular" data-testid="archive-count">
        {t('packed.count', { files: sum.files.toLocaleString(), dirs: sum.dirs.toLocaleString() })}
      </span>
      {sum.sized && sum.files > 0 && (
        <>
          <span aria-hidden="true">·</span>
          <span className="tabular">{t('packed.unpacked', { size: formatBytes(sum.bytes) })}</span>
        </>
      )}
      {listing.truncated ? (
        <span data-testid="preview-truncated" title={listing.note || undefined} className="min-w-0 flex-1 text-right">
          {t('packed.cut', { n: listing.entries.length.toLocaleString() })}
        </span>
      ) : listing.format === 'gzip' ? (
        <span data-testid="archive-note" className="min-w-0 flex-1 text-right">
          {t('packed.gzip')}
        </span>
      ) : (
        listing.note !== '' && (
          <span data-testid="archive-note" title={listing.note} className="min-w-0 flex-1 text-right">
            {t('packed.damaged', { n: listing.entries.length.toLocaleString() })}
          </span>
        )
      )}
      {copied && <span className="text-ink">{t('viewer.pathCopied')}</span>}
    </footer>
  )
}
