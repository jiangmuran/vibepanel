import { useCallback, useEffect, useRef, useState } from 'react'
import { ChevronRight, FolderPlus, Folder, Loader2 } from 'lucide-react'

import { api } from '../protocol/api'
import type { DirListing } from '../protocol/wire'
import { safeText } from './text'
import { t, useLang } from '../i18n'

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
  /**
   * Take this directory. Rejecting keeps the picker open with the reason in it.
   *
   * It used to return void and the picker closed the moment you chose. A path
   * that did not exist then took the modal away and left an error in a banner
   * at the top of the app -- so the way to retry was to reopen the picker and
   * type the whole thing again, and the field that was wrong was gone before
   * you could see what was wrong with it.
   */
  onPick: (absolutePath: string) => Promise<void>
  onClose: () => void
}) {
  useLang()
  const [listing, setListing] = useState<DirListing | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(true)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [manual, setManual] = useState('')

  // Which row the keyboard is on. -1 is the "up one level" row, which is only
  // there when there is a parent.
  const [active, setActive] = useState(-1)
  const listRef = useRef<HTMLDivElement | null>(null)


  const load = useCallback(async (path: string) => {
    setBusy(true)
    try {
      const next = await api.browse(path)
      setListing(next)
      // Top of the new directory: the "up" row when there is one, otherwise the
      // first entry.
      setActive(next.parent !== null ? -1 : 0)
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
        setActive(l.parent !== null ? -1 : 0)
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

  const pick = async (path: string) => {
    setBusy(true)
    try {
      await onPick(path)
      // No setBusy(false): a successful pick unmounts this.
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  const here = listing ? (listing.path ? `~/${listing.path}` : '~') : '…'
  const absHere = listing ? (listing.path ? `${listing.root}/${listing.path}` : listing.root) : ''

  // The highlight follows the keyboard into view. Without the scroll, a held
  // arrow key walks the selection off the bottom of a long directory and
  // nothing on screen says where it went.
  //
  // Resetting it belongs where the listing is set -- in load() and in the first
  // fetch -- not in an effect on `listing`. A setState in an effect body is a
  // cascading render, which is the rule that already shaped the fetch above.
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>('[data-active="true"]')
    el?.scrollIntoView({ block: 'nearest' })
  }, [active])

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

  const entries = listing?.entries ?? []
  const hasUp = listing !== null && listing.parent !== null
  const lowest = hasUp ? -1 : 0

  // Arrow keys move, Enter descends, Backspace or ArrowLeft goes up.
  //
  // A picker you can only click is a picker you use with your hand off the
  // keyboard you just typed a project name into. These are the four keys every
  // file dialog has had for thirty years, and their absence is most of what
  // "not smooth to operate" means.
  const onListKeys = (e: React.KeyboardEvent) => {
    if (!listing) return
    const last = entries.length - 1
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((i) => Math.min(last, i + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((i) => Math.max(lowest, i - 1))
    } else if (e.key === 'Home') {
      e.preventDefault()
      setActive(lowest)
    } else if (e.key === 'End') {
      e.preventDefault()
      setActive(last)
    } else if (e.key === 'Enter' || e.key === 'ArrowRight') {
      e.preventDefault()
      if (active === -1 && hasUp) void load(listing.parent ?? '')
      else if (entries[active]) void load(entries[active].path)
    } else if (e.key === 'Backspace' || e.key === 'ArrowLeft') {
      if (!hasUp) return
      e.preventDefault()
      void load(listing.parent ?? '')
    }
  }

  return (
    <div
      className="vp-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
      data-testid="dir-picker-backdrop"
    >
      <div
        className="vp-panel-in flex max-h-[80vh] w-full max-w-lg flex-col overflow-hidden rounded-vp border border-hairline bg-surface shadow-xl"
        onClick={(e) => e.stopPropagation()}
        data-testid="dir-picker"
      >
        <div className="flex shrink-0 items-center gap-2 border-b border-hairline px-3 py-2.5">
          <Folder size={14} className="shrink-0 text-ink-2" />
          <span className="min-w-0 flex-1 truncate font-mono text-vp-base text-ink" title={absHere}>
            {safeText(here)}
          </span>
          {busy && <Loader2 size={13} className="shrink-0 animate-spin text-ink-2" />}
        </div>

        {/* autoFocus so the keys work without a click first, and tabIndex so it
            can hold focus at all. The outline is suppressed because the active
            row draws the focus itself -- a ring around the whole scroller says
            nothing about which directory Enter would open. */}
        <div
          ref={listRef}
          tabIndex={0}
          autoFocus
          onKeyDown={onListKeys}
          data-testid="dir-list"
          className="min-h-0 flex-1 overflow-y-auto outline-none"
        >
          {listing?.parent !== null && listing !== null && (
            <button
              type="button"
              onClick={() => void load(listing.parent ?? '')}
              data-testid="dir-up"
              className="vp-press flex w-full items-center gap-2 px-3 py-2 text-left text-vp-md text-ink-2 transition-colors duration-150 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              <ChevronRight size={13} className="shrink-0 rotate-180" />
              {t('dir.up')}
            </button>
          )}
          {listing?.entries.map((e, i) => (
            <button
              key={e.path}
              type="button"
              onClick={() => void load(e.path)}
              onMouseEnter={() => setActive(i)}
              data-testid="dir-entry"
              data-active={active === i}
              className={`vp-press flex w-full items-center gap-2 px-3 py-2 text-left text-vp-md text-ink ${
                active === i ? 'bg-surface-2' : ''
              }`}
            >
              <Folder size={13} className="shrink-0 text-ink-2" />
              <span className="min-w-0 flex-1 truncate">{safeText(e.name)}</span>
              <ChevronRight size={13} className="shrink-0 text-ink-2" />
            </button>
          ))}
          {listing && listing.entries.length === 0 && (
            <div className="px-3 py-6 text-center text-vp-base text-ink-2">
              {t('dir.empty')}
            </div>
          )}
          {listing?.truncated && (
            <div className="px-3 py-2 text-vp-sm text-ink-2">
              {t('dir.truncated', { shown: listing.entries.length, total: listing.total })}
            </div>
          )}
        </div>

        {error && (
          <div
            data-testid="dir-error"
            className="shrink-0 border-t border-hairline px-3 py-2 text-vp-base"
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
                placeholder={t('dir.newName')}
                data-testid="dir-new-name"
                className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
              />
              <button
                type="button"
                onClick={() => void create()}
                disabled={!newName.trim()}
                data-testid="dir-new-confirm"
                className="shrink-0 rounded-vp px-3 py-1.5 text-vp-md disabled:opacity-40"
                style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
              >
                {t('dir.create')}
              </button>
            </div>
          ) : (
            <button
              type="button"
              onClick={() => setCreating(true)}
              data-testid="dir-new"
              className="vp-press mb-2 flex items-center gap-1.5 text-vp-base text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
            >
              <FolderPlus size={13} />
              {t('dir.newFolder')}
            </button>
          )}

          {/* The way out of the root. Not a fallback nobody finds: a project
              under /srv or /opt is ordinary, and a picker that cannot reach one
              would send people back to typing paths blind. */}
          <input
            value={manual}
            onChange={(ev) => setManual(ev.target.value)}
            onKeyDown={(ev) => {
              if (ev.key === 'Enter' && manual.trim()) void pick(manual.trim())
            }}
            placeholder={t('dir.manual')}
            data-testid="dir-manual"
            className="mb-3 w-full rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 font-mono text-vp-base text-ink outline-none focus:border-accent"
          />

          <div className="flex gap-2">
            <button
              type="button"
              onClick={onClose}
              data-testid="dir-cancel"
              className="vp-press flex-1 rounded-vp border border-hairline px-3 py-2 text-vp-md text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
            >
              {t('dir.cancel')}
            </button>
            <button
              type="button"
              onClick={() => void pick(manual.trim() || absHere)}
              disabled={!listing && !manual.trim()}
              data-testid="dir-confirm"
              className="flex-[2] rounded-vp px-3 py-2 text-vp-md disabled:opacity-40"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('dir.use')}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
