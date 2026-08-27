import { useEffect, useState } from 'react'
import { ChevronLeft, Download, File, Folder, RefreshCw } from 'lucide-react'
import { safeText } from '../text'

import { api } from '../../protocol/api'
import type { FileListing } from '../../protocol/wire'
import { t, useLang } from '../../i18n'

function bytes(n: number): string {
  if (n < 1024) return `${n}`
  const units = ['K', 'M', 'G', 'T']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)}${units[i]}`
}

/**
 * A one-directory-at-a-time browser rather than an expanding tree.
 *
 * A tree in a narrow column spends most of its width on indentation, and the
 * question this panel answers is "what is in here", not "show me the whole
 * repository at once".
 */
export function FileTree({ projectId }: { projectId: string }) {
  useLang()
  // The caller keys this component by project, so switching projects gives a
  // fresh instance starting at the root. Resetting state from an effect
  // instead would render one frame against the wrong project's path.
  const [path, setPath] = useState('')
  const [listing, setListing] = useState<FileListing | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Bumped to refetch the same directory. The listing is a snapshot of a
  // directory that agents are actively writing into, and without this the only
  // way to see a file one just produced is to leave the panel and come back.
  const [reloads, setReloads] = useState(0)

  useEffect(() => {
    // The documented shape for fetching in an effect: the state update happens
    // in a callback, and a flag stops a response that arrives after the
    // directory changed from overwriting the newer one.
    let ignore = false
    api
      .files(projectId, path)
      .then((l) => {
        if (ignore) return
        setListing(l)
        setError(null)
      })
      .catch((e: unknown) => {
        if (!ignore) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      ignore = true
    }
  }, [projectId, path, reloads])

  if (error) {
    return <p className="px-3 py-4 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>{error}</p>
  }
  if (!listing) {
    return <p className="px-3 py-4 text-[12px] text-ink-2">Reading…</p>
  }

  return (
    <div data-testid="file-tree" className="py-1">
      <div className="flex items-center gap-1 px-2 py-1">
        {listing.parent !== null && (
          <button
            type="button"
            onClick={() => setPath(listing.parent ?? '')}
            title={t('files.up')}
            className="rounded p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <ChevronLeft size={13} />
          </button>
        )}
        <span className="min-w-0 flex-1 truncate text-[11px] text-ink-2" title={listing.path || '/'}>
          {listing.path || '/'}
        </span>
        <button
          type="button"
          data-testid="file-refresh"
          onClick={() => setReloads((n) => n + 1)}
          title={t('files.reread')}
          className="shrink-0 rounded p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <RefreshCw size={12} />
        </button>
      </div>

      {listing.entries.length === 0 && (
        <p className="px-3 py-3 text-[12px] text-ink-2">{t('files.empty')}</p>
      )}

      {/* A partial listing has to say so. The server caps a directory at two
          thousand entries, and without this line the panel shows the first two
          thousand of a hundred thousand files as though that were the
          directory — a file browser that quietly stops is worse than one that
          admits its limit. */}
      {listing.truncated && (
        <p data-testid="file-truncated" className="px-3 py-2 text-[11px] text-ink-2">
          Showing {listing.entries.length.toLocaleString()} of{' '}
          {listing.total.toLocaleString()} items
        </p>
      )}

      {listing.entries.map((e) => (
        <div
          key={e.path}
          data-testid="file-entry"
          onClick={() => e.isDir && setPath(e.path)}
          className={`group flex items-center gap-2 rounded-md px-2 py-1.5 text-[12px] ${
            e.isDir ? 'cursor-pointer hover:bg-surface-2' : 'hover:bg-surface-2'
          }`}
          title={safeText(e.path)}
        >
          {e.isDir ? (
            <Folder size={12} className="shrink-0 text-ink-2" />
          ) : (
            <File size={12} className="shrink-0 text-ink-2" />
          )}
          <span className={`min-w-0 flex-1 truncate ${e.isDir ? 'text-ink' : 'text-ink-2'}`}>
            {/* A filename is whatever an agent wrote to disk, and a directional
                override in it renders the extension backwards right next to the
                download link. See safeText. */}
            {safeText(e.name)}
            {e.symlink && <span className="text-ink-2"> ↗</span>}
          </span>
          {/* Say why this one has nothing to click. Without it a file with no
              download among files that have one reads as the panel failing,
              rather than as the file being out of bounds. */}
          {e.escapes && (
            <span
              data-testid="file-escapes"
              title={t('files.escapeLink')}
              className="shrink-0 text-[10.5px] text-ink-2"
            >
              outside
            </span>
          )}
          {!e.isDir && <span className="tabular shrink-0 text-[10.5px] text-ink-2">{bytes(e.size)}</span>}
          {!e.isDir && e.readable && (
            // A link, not a fetch-and-blob: the browser's own download machinery
            // handles the progress, the resume and the save dialog, and a blob
            // would hold the whole file in memory first.
            <a
              href={api.downloadURL(projectId, e.path)}
              download={e.name}
              data-testid="file-download"
              onClick={(ev) => ev.stopPropagation()}
              title={`Download ${safeText(e.name)}`}
              className="vp-reveal shrink-0 rounded p-0.5 text-ink-2 hover:text-ink"
            >
              <Download size={12} />
            </a>
          )}
        </div>
      ))}
    </div>
  )
}
