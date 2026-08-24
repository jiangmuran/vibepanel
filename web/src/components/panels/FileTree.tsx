import { useEffect, useState } from 'react'
import { ChevronLeft, File, Folder } from 'lucide-react'

import { api } from '../../protocol/api'
import type { FileListing } from '../../protocol/wire'

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
  // The caller keys this component by project, so switching projects gives a
  // fresh instance starting at the root. Resetting state from an effect
  // instead would render one frame against the wrong project's path.
  const [path, setPath] = useState('')
  const [listing, setListing] = useState<FileListing | null>(null)
  const [error, setError] = useState<string | null>(null)

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
  }, [projectId, path])

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
            title="Up one level"
            className="rounded p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <ChevronLeft size={13} />
          </button>
        )}
        <span className="truncate text-[11px] text-ink-2" title={listing.path || '/'}>
          {listing.path || '/'}
        </span>
      </div>

      {listing.entries.length === 0 && (
        <p className="px-3 py-3 text-[12px] text-ink-2">Empty</p>
      )}

      {listing.entries.map((e) => (
        <div
          key={e.path}
          data-testid="file-entry"
          onClick={() => e.isDir && setPath(e.path)}
          className={`flex items-center gap-1.5 px-2 py-1 text-[12px] ${
            e.isDir ? 'cursor-pointer hover:bg-surface-2' : ''
          }`}
          title={e.path}
        >
          {e.isDir ? (
            <Folder size={12} className="shrink-0 text-ink-2" />
          ) : (
            <File size={12} className="shrink-0 text-ink-2" />
          )}
          <span className={`min-w-0 flex-1 truncate ${e.isDir ? 'text-ink' : 'text-ink-2'}`}>
            {e.name}
            {e.symlink && <span className="text-ink-2"> ↗</span>}
          </span>
          {!e.isDir && <span className="tabular shrink-0 text-[10.5px] text-ink-2">{bytes(e.size)}</span>}
        </div>
      ))}
    </div>
  )
}
