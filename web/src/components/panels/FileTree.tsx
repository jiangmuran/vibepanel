import { useCallback, useEffect, useRef, useState } from 'react'
import { ChevronLeft, Download, File, Folder, RefreshCw, Upload } from 'lucide-react'
import { safeText } from '../text'

import { api } from '../../protocol/api'
import type { FileEntry, FileListing } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { filesFrom, uploadFiles } from '../upload'
import { FilePreview } from './FilePreview'
import { formatBytes } from './preview'

/**
 * A one-directory-at-a-time browser rather than an expanding tree.
 *
 * A tree in a narrow column spends most of its width on indentation, and the
 * question this panel answers is "what is in here", not "show me the whole
 * repository at once".
 *
 * It is also the way files go *in*. Dropping a screenshot on the terminal has
 * always worked, but that route puts the file next to the session and types
 * the path at the prompt — it is a way of handing something to an agent, not a
 * way of putting a file somewhere. This one puts it in the directory you are
 * looking at, which is the other half of the question, and the two share
 * everything but that last step (see components/upload.ts).
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
  const [dropping, setDropping] = useState(false)
  const [note, setNote] = useState('')
  const [previewing, setPreviewing] = useState<FileEntry | null>(null)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const chooserRef = useRef<HTMLInputElement | null>(null)

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

  const take = useCallback(
    (files: File[]) => {
      if (files.length === 0) return
      void uploadFiles(projectId, path, files, setNote).then((paths) => {
        // Reread rather than splice the new names in. The upload is not the
        // only thing writing here — that is the whole premise of the panel —
        // so the honest picture is the one the server has.
        if (paths.length > 0) setReloads((n) => n + 1)
      })
    },
    [projectId, path],
  )

  /**
   * A screenshot on the clipboard, which is the case this exists for.
   *
   * On window rather than on the panel, because a div cannot receive a paste
   * unless it holds focus, and nobody clicks a file list before pressing
   * ctrl-V. The guard is what stops it stealing the terminal's: a paste is
   * aimed at whatever has focus, so this takes it only when that is something
   * inside this panel or nothing at all. With the terminal focused the event
   * still passes through here, and the terminal's own handler — which uploads
   * next to the session instead — is the one that must win.
   */
  useEffect(() => {
    const onPaste = (e: ClipboardEvent) => {
      const root = rootRef.current
      const target = e.target as Node | null
      if (!root || !target) return
      if (!root.contains(target) && target !== document.body) return
      const files = filesFrom(e.clipboardData)
      if (files.length === 0) return
      // Only now: preventing the default for a text paste would break the
      // ordinary case in order to serve the rare one.
      e.preventDefault()
      take(files)
    }
    window.addEventListener('paste', onPaste)
    return () => window.removeEventListener('paste', onPaste)
  }, [take])

  const here = listing ? listing.path || '/' : path || '/'

  const open = (e: FileEntry) => {
    if (e.isDir) setPath(e.path)
    else setPreviewing(e)
  }

  return (
    <div
      ref={rootRef}
      data-testid="file-tree"
      // Focusable so a keyboard can reach the panel and paste into it, and
      // min-h-full so the drop target is the whole column rather than only the
      // rows — dropping a file into the space under a short listing is the
      // gesture people actually make.
      tabIndex={0}
      aria-label={t('files.panel')}
      className="relative min-h-full py-1"
      onDragOver={(e) => {
        // Both handlers, and both preventDefault: without dragover the drop
        // never fires, and the browser navigates to the file instead.
        if (!e.dataTransfer) return
        e.preventDefault()
        e.dataTransfer.dropEffect = 'copy'
        setDropping(true)
      }}
      onDragLeave={(e) => {
        // Only when the pointer has left the panel itself. dragleave fires for
        // every child it crosses, and acting on those flickers the overlay all
        // the way down the list.
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDropping(false)
      }}
      onDrop={(e) => {
        if (!e.dataTransfer) return
        e.preventDefault()
        setDropping(false)
        take(filesFrom(e.dataTransfer))
      }}
    >
      <div className="flex items-center gap-1 px-2 py-1">
        {listing?.parent != null && (
          <button
            type="button"
            onClick={() => setPath(listing.parent ?? '')}
            title={t('files.up')}
            className="vp-press rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <ChevronLeft size={13} />
          </button>
        )}
        <span className="min-w-0 flex-1 truncate text-vp-sm text-ink-2" title={safeText(here)}>
          {safeText(here)}
        </span>
        {/* The third way in, after dropping and pasting. It is the only one
            that works on a phone, where there is nothing to drag from and no
            clipboard with a file on it. */}
        <button
          type="button"
          data-testid="file-upload"
          onClick={() => chooserRef.current?.click()}
          title={t('files.choose')}
          className="vp-press shrink-0 rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <Upload size={12} />
        </button>
        <input
          ref={chooserRef}
          type="file"
          multiple
          data-testid="file-chooser"
          className="hidden"
          onChange={(e) => {
            take([...(e.target.files ?? [])])
            // Cleared so that choosing the same file twice fires change twice.
            // Without it, an upload that failed on a name clash cannot be
            // retried after the clash is dealt with.
            e.target.value = ''
          }}
        />
        <button
          type="button"
          data-testid="file-refresh"
          onClick={() => setReloads((n) => n + 1)}
          title={t('files.reread')}
          className="vp-press shrink-0 rounded-md p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
        >
          <RefreshCw size={12} />
        </button>
      </div>

      {note && (
        <p data-testid="file-note" className="px-3 py-1 text-vp-sm text-ink-2">
          {/* Carries the server's message on a failure — "shot.png already
              exists" — which is a filename and so goes through safeText. */}
          {safeText(note)}
        </p>
      )}

      {error && (
        <p className="px-3 py-4 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
          {safeText(error)}
        </p>
      )}
      {!error && !listing && <p className="px-3 py-4 text-vp-base text-ink-2">{t('files.reading')}</p>}

      {listing && listing.entries.length === 0 && (
        <p className="px-3 py-3 text-vp-base text-ink-2">{t('files.empty')}</p>
      )}

      {/* A partial listing has to say so. The server caps a directory at two
          thousand entries, and without this line the panel shows the first two
          thousand of a hundred thousand files as though that were the
          directory — a file browser that quietly stops is worse than one that
          admits its limit. */}
      {listing?.truncated && (
        <p data-testid="file-truncated" className="px-3 py-2 text-vp-sm text-ink-2">
          {t('files.truncated', {
            shown: listing.entries.length.toLocaleString(),
            total: listing.total.toLocaleString(),
          })}
        </p>
      )}

      {listing?.entries.map((e) => {
        // A symlink out of the project is shown but does nothing: every
        // endpoint behind these rows resolves symlinks and refuses what leaves
        // the root, so descending or previewing one answers 403. The badge
        // beside it is the explanation.
        const reachable = !e.escapes && (e.isDir || e.readable)
        return (
          <div
            key={e.path}
            data-testid="file-entry"
            className="group flex items-center gap-2 rounded-md px-2 py-1.5 text-vp-base hover:bg-surface-2"
            title={safeText(e.path)}
          >
            {/* The name is the button, not the row. A row that is a button
                cannot contain the download link — nested interactive elements
                are invalid and a keyboard cannot reach the inner one — and
                before this the row was a div with a click handler, which no
                keyboard could reach at all. */}
            <button
              type="button"
              data-testid="file-open"
              disabled={!reachable}
              onClick={() => open(e)}
              title={
                e.isDir
                  ? t('preview.enter', { name: safeText(e.name) })
                  : t('preview.open', { name: safeText(e.name) })
              }
              className="flex min-w-0 flex-1 items-center gap-2 rounded-md text-left disabled:cursor-default"
            >
              {e.isDir ? (
                <Folder size={12} className="shrink-0 text-ink-2" />
              ) : (
                <File size={12} className="shrink-0 text-ink-2" />
              )}
              <span className={`min-w-0 flex-1 truncate ${e.isDir ? 'text-ink' : 'text-ink-2'}`}>
                {/* A filename is whatever an agent wrote to disk, and a
                    directional override in it renders the extension backwards
                    right next to the download link. See safeText. */}
                {safeText(e.name)}
                {e.symlink && <span className="text-ink-2"> ↗</span>}
              </span>
            </button>
            {/* Say why this one has nothing to click. Without it a file with no
                download among files that have one reads as the panel failing,
                rather than as the file being out of bounds. */}
            {e.escapes && (
              <span
                data-testid="file-escapes"
                title={t('files.escapeLink')}
                className="shrink-0 text-vp-xs text-ink-2"
              >
                {t('files.escapes')}
              </span>
            )}
            {!e.isDir && <span className="tabular shrink-0 text-vp-xs text-ink-2">{formatBytes(e.size)}</span>}
            {!e.isDir && e.readable && !e.escapes && (
              // A link, not a fetch-and-blob: the browser's own download machinery
              // handles the progress, the resume and the save dialog, and a blob
              // would hold the whole file in memory first.
              <a
                href={api.downloadURL(projectId, e.path)}
                download={e.name}
                data-testid="file-download"
                onClick={(ev) => ev.stopPropagation()}
                title={t('files.downloadOne', { name: safeText(e.name) })}
                className="vp-press vp-reveal shrink-0 rounded-md p-0.5 text-ink-2 hover:text-ink"
              >
                <Download size={12} />
              </a>
            )}
          </div>
        )
      })}

      {dropping && (
        // pointer-events-none, or the overlay becomes the drop target and the
        // dragleave that hides it never fires.
        <div
          data-testid="file-drop-overlay"
          className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-vp border-2 border-dashed border-accent bg-surface/85 px-3 text-center text-vp-sm text-ink"
        >
          {t('upload.dropHere', { dir: safeText(here) })}
        </div>
      )}

      {previewing && (
        <FilePreview
          key={previewing.path}
          projectId={projectId}
          entry={previewing}
          onClose={() => setPreviewing(null)}
        />
      )}
    </div>
  )
}
