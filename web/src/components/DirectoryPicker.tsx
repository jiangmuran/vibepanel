import { useCallback, useEffect, useState } from 'react'
import { ChevronRight, FolderPlus, Folder, Loader2 } from 'lucide-react'

import { api } from '../protocol/api'
import type { DirListing } from '../protocol/wire'
import { safeText } from './text'

/**
 * Where a project should live.
 *
 * This was `window.prompt('Project directory', '~/projects/')`. A prompt asks
 * you to know the answer already, spells nothing, and cannot tell you that the
 * directory you typed is empty, missing, or a file — the first feedback was a
 * server error after the fact.
 *
 * Rooted at the home directory, and the reason is noise rather than security:
 * this endpoint sits behind the same session as a writable terminal, so it
 * defends nothing that is not already open. What it does is make the first
 * screen a list of your projects instead of /boot and /proc. Anything outside
 * home is reached with the field at the bottom, which is the honest way to
 * offer a shortcut without pretending it is a boundary.
 */
export function DirectoryPicker({
  onPick,
  onClose,
}: {
  onPick: (absolutePath: string) => void
  onClose: () => void
}) {
  const [listing, setListing] = useState<DirListing | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(true)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [manual, setManual] = useState('')

  const load = useCallback(async (path: string) => {
    setBusy(true)
    try {
      setListing(await api.browse(path))
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }, [])

  // The first listing is fetched here rather than through load(), which sets
  // state synchronously -- and a setState in an effect body is what React's
  // rules call a cascading render. `busy` starts true instead, which is also
  // what is actually true.
  useEffect(() => {
    let cancelled = false
    api.browse('').then(
      (l) => {
        if (cancelled) return
        setListing(l)
        setBusy(false)
      },
      (e: unknown) => {
        if (cancelled) return
        setError(e instanceof Error ? e.message : String(e))
        setBusy(false)
      },
    )
    return () => {
      cancelled = true
    }
  }, [])

  // Escape closes. A modal that can only be dismissed by finding its button is
  // one people close by reloading the page.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const here = listing ? (listing.path ? `~/${listing.path}` : '~') : '…'
  const absHere = listing ? (listing.path ? `${listing.root}/${listing.path}` : listing.root) : ''

  const create = async () => {
    const name = newName.trim()
    if (!name || !listing) return
    setBusy(true)
    try {
      const made = await api.mkdir(listing.path, name)
      setNewName('')
      setCreating(false)
      // Straight into it. Making a directory and being left outside it is the
      // kind of half-step that makes people click twice to check.
      await load(made.path)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
      data-testid="dir-picker-backdrop"
    >
      <div
        className="flex max-h-[80vh] w-full max-w-lg flex-col overflow-hidden rounded-vp border border-hairline bg-surface shadow-xl"
        onClick={(e) => e.stopPropagation()}
        data-testid="dir-picker"
      >
        <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-3 py-2.5">
          <Folder size={14} className="shrink-0 text-ink-2" />
          <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-ink" title={absHere}>
            {safeText(here)}
          </span>
          {busy && <Loader2 size={13} className="shrink-0 animate-spin text-ink-2" />}
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {listing?.parent !== null && listing !== null && (
            <button
              type="button"
              onClick={() => void load(listing.parent ?? '')}
              data-testid="dir-up"
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-[13px] text-ink-2 transition-colors duration-150 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              <ChevronRight size={13} className="shrink-0 rotate-180" />
              上一层
            </button>
          )}
          {listing?.entries.map((e) => (
            <button
              key={e.path}
              type="button"
              onClick={() => void load(e.path)}
              data-testid="dir-entry"
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-[13px] text-ink transition-colors duration-150 ease-vp hover:bg-surface-2"
            >
              <Folder size={13} className="shrink-0 text-ink-2" />
              <span className="min-w-0 flex-1 truncate">{safeText(e.name)}</span>
              <ChevronRight size={13} className="shrink-0 text-ink-2" />
            </button>
          ))}
          {listing && listing.entries.length === 0 && (
            <div className="px-3 py-6 text-center text-[12px] text-ink-2">
              这里没有子目录 — 可以直接选它，或者新建一个
            </div>
          )}
          {listing?.truncated && (
            <div className="px-3 py-2 text-[11px] text-ink-2">
              目录太多，只显示了 {listing.entries.length} / {listing.total} 个
            </div>
          )}
        </div>

        {error && (
          <div
            data-testid="dir-error"
            className="shrink-0 border-t border-hairline px-3 py-2 text-[12px]"
            style={{ color: 'var(--vp-state-crashed)' }}
          >
            {safeText(error)}
          </div>
        )}

        <div className="shrink-0 border-t border-hairline p-3">
          {creating ? (
            <div className="mb-2 flex gap-2">
              <input
                autoFocus
                value={newName}
                onChange={(ev) => setNewName(ev.target.value)}
                onKeyDown={(ev) => {
                  if (ev.key === 'Enter') void create()
                  if (ev.key === 'Escape') setCreating(false)
                }}
                placeholder="新目录的名字"
                data-testid="dir-new-name"
                className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
              />
              <button
                type="button"
                onClick={() => void create()}
                disabled={!newName.trim()}
                data-testid="dir-new-confirm"
                className="shrink-0 rounded-vp px-3 py-1.5 text-[13px] disabled:opacity-40"
                style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
              >
                建
              </button>
            </div>
          ) : (
            <button
              type="button"
              onClick={() => setCreating(true)}
              data-testid="dir-new"
              className="mb-2 flex items-center gap-1.5 text-[12.5px] text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
            >
              <FolderPlus size={13} />
              在这里新建目录
            </button>
          )}

          {/* The way out of the root. Not a fallback nobody finds: a project
              under /srv or /opt is ordinary, and a picker that cannot reach one
              would send people back to typing paths blind. */}
          <input
            value={manual}
            onChange={(ev) => setManual(ev.target.value)}
            onKeyDown={(ev) => {
              if (ev.key === 'Enter' && manual.trim()) onPick(manual.trim())
            }}
            placeholder="或直接输入路径，支持 ~"
            data-testid="dir-manual"
            className="mb-3 w-full rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 font-mono text-[12.5px] text-ink outline-none focus:border-accent"
          />

          <div className="flex gap-2">
            <button
              type="button"
              onClick={onClose}
              data-testid="dir-cancel"
              className="flex-1 rounded-vp border border-hairline px-3 py-2 text-[13px] text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
            >
              取消
            </button>
            <button
              type="button"
              onClick={() => onPick(manual.trim() || absHere)}
              disabled={!listing && !manual.trim()}
              data-testid="dir-confirm"
              className="flex-[2] rounded-vp px-3 py-2 text-[13px] disabled:opacity-40"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              使用这个目录
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
