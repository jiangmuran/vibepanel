import { Fragment, useCallback, useEffect, useRef, useState } from 'react'
import { ChevronRight, Folder, FolderPlus, Pencil } from 'lucide-react'

import { api } from '../protocol/api'
import type { DirListing, FileEntry } from '../protocol/wire'
import { safeText } from './text'
import { t, useLang } from '../i18n'
import {
  absOf,
  classifyInput,
  crumbs,
  filterEntries,
  matchSpan,
  resolveKey,
  type Act,
} from './dirpicker'

/**
 * Where a project should live.
 *
 * Three shapes so far, and each one was replaced for the same reason: the
 * control was about itself rather than about the directories.
 *
 * It began as `window.prompt('Project directory', '~/projects/')`, which asks
 * you to know the answer already. It became a list with a filter box bolted
 * over it, a create-folder row and two buttons stacked underneath -- five
 * pieces of furniture, each added on its own day, none of them aligned with
 * the others, and the content fourth in line for the eye. That is what "选择
 * 目录那个控件我认为仍然得完全重构" is about, and repainting it would not have
 * touched any of it.
 *
 * What is here now is one list with one field over it.
 *
 * The field is the whole top of the dialog and it is one control doing what
 * used to be three. The crumbs live *inside* it, to the left of the caret:
 * click a segment to go there, or type and it filters what is on screen, or
 * type something that starts with `/` or `~` and the crumbs step aside because
 * you are now addressing a place rather than searching one. A breadcrumb you
 * can click and a path you can edit are the same fact, and every file dialog
 * worth using treats them as one control.
 *
 * There is no title bar. A dialog whose subject is a list should open with the
 * list nearest to the eye, and a heading reading "Choose a directory" over a
 * control that is visibly a directory chooser costs a row of the only thing
 * anybody came here to read. It is on the element as an aria-label, where it
 * is worth something to a screen reader and nothing to the layout.
 *
 * Rooted at the home directory, and the reason is noise rather than security:
 * this endpoint sits behind the same session as a writable terminal, so it
 * defends nothing that is not already open. What it does is make the first
 * screen a list of your projects instead of /boot and /proc. A project under
 * /srv or /opt is ordinary, so the way out is not an escape hatch at the
 * bottom -- it is the same field, which says in words which of the two things
 * Enter is about to do with what you typed.
 *
 * The decisions -- what the text means, which rows survive it, what each key
 * does -- are in dirpicker.ts, where they are pinned by tests. What is left
 * here is the arrangement.
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
  const [busy, setBusy] = useState(true)
  const [text, setText] = useState('')
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')

  /**
   * A refusal, and which control was refused.
   *
   * It used to be one string in a strip above the buttons, wherever it came
   * from: a path that does not exist, a folder name already taken and a
   * directory the server would not list all appeared in the same place, none
   * of them next to the thing you would have to change. `at` is what puts each
   * one under the control that produced it.
   */
  const [error, setError] = useState<{ at: 'locator' | 'confirm' | 'create'; message: string } | null>(
    null,
  )

  /** Which row the keyboard is on. -1 is "no row", not "the first one". */
  const [active, setActive] = useState(-1)

  /**
   * The direction the last navigation went, and a counter to re-key the list.
   *
   * The counter is what makes the animation run again for a move that lands on
   * a path we have been to before -- into `b`, back up, into `b` -- which a key
   * of the path alone would silently skip.
   */
  const [nav, setNav] = useState<{ dir: 'in' | 'out' | 'jump'; n: number }>({ dir: 'jump', n: 0 })

  const listRef = useRef<HTMLDivElement | null>(null)
  const crumbRef = useRef<HTMLDivElement | null>(null)
  const boxRef = useRef<HTMLInputElement | null>(null)

  const reason = (e: unknown) => (e instanceof Error ? e.message : String(e))

  const load = useCallback(async (path: string, dir: 'in' | 'out' | 'jump') => {
    setBusy(true)
    try {
      const next = await api.browse(path)
      setListing(next)
      setNav((prev) => ({ dir, n: prev.n + 1 }))
      // Arriving somewhere clears the box: what was in it described the list
      // you just left, and a filter that survives the move hides most of the
      // directory you asked to see.
      setText('')
      setActive(next.entries.length > 0 ? 0 : -1)
      setError(null)
    } catch (e) {
      // Next to the field, because the field is what said to come here --
      // whether that was a typed path or a crumb.
      setError({ at: 'locator', message: reason(e) })
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
        setActive(l.entries.length > 0 ? 0 : -1)
        setBusy(false)
      },
      (e: unknown) => {
        if (cancelled) return
        setError({ at: 'locator', message: reason(e) })
        setBusy(false)
      },
    )
    return () => {
      cancelled = true
    }
  }, [])

  // Escape closes, for the focus that is not in the field -- a button, a row.
  // The field handles its own Escape and stops it here, because there it means
  // "clear what I typed" first and "close" second.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // The highlight follows the keyboard into view. Without the scroll, a held
  // arrow key walks the selection off the bottom of a long directory and
  // nothing on screen says where it went.
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>('[data-active="true"]')
    el?.scrollIntoView({ block: 'nearest' })
  }, [active])

  // The end of the trail is the part that changed, so that is the part kept in
  // view. A crumb strip scrolled to its start shows `~ › projects › ...` and
  // hides the directory you are actually in.
  useEffect(() => {
    const el = crumbRef.current
    if (el) el.scrollLeft = el.scrollWidth
  }, [listing])

  const pick = async (path: string, at: 'locator' | 'confirm') => {
    setBusy(true)
    try {
      await onPick(path)
      // No setBusy(false): a successful pick unmounts this.
    } catch (e) {
      setError({ at, message: reason(e) })
      setBusy(false)
    }
  }

  const create = async () => {
    const name = newName.trim()
    if (!name || !listing) return
    setBusy(true)
    try {
      const made = await api.mkdir(listing.path, name)
      setNewName('')
      setCreating(false)
      // Straight into it, and from the right like any other descent. Making a
      // directory and being left outside it is the kind of half-step that
      // makes people click twice to check.
      await load(made.path, 'in')
    } catch (e) {
      setError({ at: 'create', message: reason(e) })
      setBusy(false)
    }
  }

  const root = listing?.root ?? ''
  const typed = classifyInput(text, root)
  const all: readonly FileEntry[] = listing?.entries ?? []
  // In path mode the list is left alone: it is the place you are still
  // standing in, and the crumbs above it are the context for what you type.
  const rows = typed.kind === 'filter' ? filterEntries(all, typed.query) : [...all]
  const here = listing ? absOf(listing.root, listing.path) : ''
  const trail = listing ? crumbs(listing.path) : []
  const parent = listing?.parent ?? null
  const query = typed.kind === 'filter' ? typed.query : ''
  // Nothing is navigable before the first listing lands, because until then
  // there is no root to have measured the typed path against.
  const navigable = listing !== null && typed.kind === 'path' && typed.inside !== null

  const run = (act: Act, at: 'locator' | 'confirm') => {
    switch (act.do) {
      case 'move':
        setActive(act.to)
        break
      case 'open': {
        const row = rows[active]
        if (row) void load(row.path, 'in')
        break
      }
      case 'up':
        if (parent !== null) void load(parent, 'out')
        break
      case 'go':
        if (typed.kind === 'path' && typed.inside !== null) void load(typed.inside, 'jump')
        break
      case 'use':
        void pick(typed.kind === 'path' ? typed.abs : here, at)
        break
      case 'createNamed':
        // What was typed becomes the name, because that is what somebody who
        // typed a name nothing matched is doing.
        setNewName(query)
        setText('')
        setCreating(true)
        break
      case 'clear':
        setText('')
        break
      case 'close':
        onClose()
        break
      case 'text':
        break
    }
  }

  const onBoxKey = (ev: React.KeyboardEvent<HTMLInputElement>) => {
    // Mid-composition Enter belongs to the input method: it is choosing a
    // candidate, not opening a directory.
    if (ev.nativeEvent.isComposing || ev.nativeEvent.keyCode === 229) return
    const act = resolveKey({
      key: ev.key,
      kind: typed.kind,
      navigable,
      hasText: text.trim() !== '',
      count: rows.length,
      active,
      hasParent: parent !== null,
    })
    if (act.do === 'text') return
    ev.preventDefault()
    // Handled here means handled here: without this, Escape reaches the window
    // listener above as well and clearing the box also closes the dialog.
    ev.stopPropagation()
    run(act, 'locator')
  }

  /**
   * What the button on the right does, which is what Enter does to a path.
   *
   * In filter mode it is the dialog's default action -- take the directory
   * being listed -- because there Enter belongs to the list. In path mode it
   * follows the field, and its label says which of the two: a picker whose
   * confirm button silently takes a typed path instead of the visible one is
   * the invisible mode this rebuild exists to remove.
   */
  const primary: Act = navigable ? { do: 'go' } : { do: 'use' }
  const primaryLabel =
    typed.kind !== 'path' ? t('dir.use') : navigable ? t('dir.goHere') : t('dir.usePath')

  const startCreate = () => {
    setError(null)
    setCreating(true)
    // The new row is the first row. Somebody who pressed this while forty rows
    // down a long directory would otherwise be typing into something off the
    // top of the screen.
    listRef.current?.scrollTo({ top: 0 })
  }

  const editPath = () => {
    setText(here)
    const el = boxRef.current
    if (el) {
      el.focus()
      el.select()
    }
  }

  return (
    <div
      className="vp-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
      data-testid="dir-picker-backdrop"
    >
      <div
        className="vp-panel-in flex h-[min(30rem,82vh)] w-full max-w-xl flex-col overflow-hidden rounded-vp-lg border border-hairline bg-surface shadow-xl"
        onClick={(e) => e.stopPropagation()}
        // Read by focusTerminal: while this is up the keyboard is the picker's,
        // and a terminal must not take it back mid-choice.
        data-vp-modal="directory"
        data-testid="dir-picker"
        role="dialog"
        aria-modal="true"
        aria-label={t('dir.title')}
      >
        {/* The locator: crumbs, caret and the two things you can do to where
            you are, in one box on one baseline. */}
        <div className="shrink-0 border-b border-hairline p-2.5">
          <div className="flex items-center gap-1.5 rounded-vp border border-hairline bg-surface-2 px-2 py-1 focus-within:border-accent">
            <div
              ref={crumbRef}
              data-testid="dir-crumbs"
              className={`vp-crumbs flex items-center gap-0.5 ${
                typed.kind === 'path' ? 'vp-crumbs-away' : ''
              }`}
              aria-label={t('dir.here')}
              // Stepped aside is still on the page: without this, Tab in path
              // mode walks through crumbs nobody can see, and a screen reader
              // reads out a location the field is no longer about.
              aria-hidden={typed.kind === 'path'}
            >
              {trail.map((crumb, i) => {
                const last = i === trail.length - 1
                return (
                  <Fragment key={crumb.path}>
                    {i > 0 && <ChevronRight size={11} className="shrink-0 text-ink-3" />}
                    <button
                      type="button"
                      disabled={last}
                      tabIndex={typed.kind === 'path' ? -1 : undefined}
                      onClick={() => void load(crumb.path, 'out')}
                      // The crumb one step back *is* "up one level"; a separate
                      // control for it would be a second answer to a question
                      // this bar already answers, sitting next to the first.
                      data-testid={i === trail.length - 2 ? 'dir-up' : 'dir-crumb'}
                      title={last ? undefined : t('dir.up')}
                      className={`vp-press shrink-0 whitespace-nowrap rounded-md px-1.5 py-0.5 text-vp-md ${
                        last
                          ? 'font-medium text-ink'
                          : 'text-ink-2 transition-colors duration-150 ease-vp hover:bg-surface hover:text-ink'
                      }`}
                    >
                      {safeText(crumb.label)}
                    </button>
                  </Fragment>
                )
              })}
            </div>

            <input
              ref={boxRef}
              value={text}
              onChange={(ev) => {
                setText(ev.target.value)
                // Back to the top of whatever the new text leaves on screen.
                // Keeping the index would leave the highlight on row 12 of a
                // list that now has three rows in it.
                setActive(0)
                if (error?.at === 'locator') setError(null)
              }}
              onKeyDown={onBoxKey}
              placeholder={t('dir.search')}
              data-testid="dir-search"
              autoFocus
              spellCheck={false}
              autoComplete="off"
              autoCorrect="off"
              autoCapitalize="off"
              aria-label={t('dir.search')}
              aria-controls="dir-listbox"
              aria-activedescendant={active >= 0 ? `dir-row-${active}` : undefined}
              className="min-w-0 flex-1 bg-transparent px-1 py-1 font-mono text-vp-md text-ink outline-none"
            />

            <button
              type="button"
              onClick={editPath}
              data-testid="dir-edit-path"
              title={t('dir.editPath')}
              aria-label={t('dir.editPath')}
              className="vp-press vp-tap shrink-0 rounded-md p-1.5 text-ink-3 transition-colors duration-150 ease-vp hover:bg-surface hover:text-ink"
            >
              <Pencil size={13} />
            </button>
            <button
              type="button"
              onClick={startCreate}
              data-testid="dir-new"
              title={t('dir.newFolder')}
              aria-label={t('dir.newFolder')}
              className="vp-press vp-tap shrink-0 rounded-md p-1.5 text-ink-3 transition-colors duration-150 ease-vp hover:bg-surface hover:text-ink"
            >
              <FolderPlus size={13} />
            </button>
          </div>

          {/* One slot under the field, for whatever the field currently has to
              say. A refusal outranks a hint: they are about the same text, and
              the one that stops you happening is the one to read. */}
          {error?.at === 'locator' ? (
            <p
              data-testid="dir-error"
              className="mt-1.5 px-1 text-vp-sm"
              style={{ color: 'var(--vp-state-crashed)' }}
            >
              {safeText(error.message)}
            </p>
          ) : (
            text.trim() !== '' && (
              <p data-testid="dir-search-hint" className="mt-1.5 px-1 text-vp-sm text-ink-2">
                {typed.kind === 'path'
                  ? navigable
                    ? t('dir.willGo')
                    : t('dir.willUse')
                  : t('dir.matches', { n: rows.length, total: all.length })}
              </p>
            )
          )}
        </div>

        {/* The list, and nothing over it. */}
        <div
          ref={listRef}
          id="dir-listbox"
          data-testid="dir-list"
          role="listbox"
          aria-label={t('dir.title')}
          aria-busy={busy}
          className="min-h-0 flex-1 overflow-y-auto py-1"
        >
          <div
            key={nav.n}
            // Deeper arrives from the right, up arrives from the left; a typed
            // path is not a step in the hierarchy, so it gets neither.
            className={nav.dir === 'in' ? 'vp-enter-right' : nav.dir === 'out' ? 'vp-enter-left' : ''}
          >
            {creating && (
              <div className="flex items-center gap-2.5 px-3 py-1" data-testid="dir-new-row">
                <span
                  className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md"
                  style={{ background: 'var(--vp-selection)' }}
                >
                  <FolderPlus size={13} style={{ color: 'var(--vp-accent)' }} />
                </span>
                <input
                  autoFocus
                  value={newName}
                  onChange={(ev) => setNewName(ev.target.value)}
                  onKeyDown={(ev) => {
                    if (ev.nativeEvent.isComposing || ev.nativeEvent.keyCode === 229) return
                    if (ev.key === 'Enter') {
                      ev.preventDefault()
                      void create()
                    }
                    if (ev.key === 'Escape') {
                      ev.preventDefault()
                      ev.stopPropagation()
                      setCreating(false)
                      setNewName('')
                      boxRef.current?.focus()
                    }
                  }}
                  placeholder={t('dir.newName')}
                  aria-label={t('dir.newFolder')}
                  data-testid="dir-new-name"
                  className="min-w-0 flex-1 rounded-md border border-hairline bg-surface-2 px-2 py-1 text-vp-md text-ink outline-none focus:border-accent"
                />
                <button
                  type="button"
                  onClick={() => void create()}
                  disabled={!newName.trim()}
                  data-testid="dir-new-confirm"
                  className="vp-press shrink-0 rounded-md px-2.5 py-1 text-vp-sm disabled:opacity-40"
                  style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
                >
                  {t('dir.create')}
                </button>
              </div>
            )}
            {error?.at === 'create' && (
              <p
                data-testid="dir-error"
                className="px-3 pb-1 pl-12 text-vp-sm"
                style={{ color: 'var(--vp-state-crashed)' }}
              >
                {safeText(error.message)}
              </p>
            )}

            {busy && listing === null ? (
              <Skeleton />
            ) : (
              rows.map((row, i) => (
                <button
                  key={row.path}
                  type="button"
                  role="option"
                  id={`dir-row-${i}`}
                  aria-selected={active === i}
                  tabIndex={-1}
                  onClick={() => void load(row.path, 'in')}
                  onMouseEnter={() => setActive(i)}
                  data-testid="dir-entry"
                  data-active={active === i}
                  className={`vp-press group relative flex w-full items-center gap-2.5 px-3 py-1 text-left text-vp-md text-ink ${
                    active === i ? 'bg-surface-2' : ''
                  }`}
                >
                  {/* Three signals, not one: the bar, the fill and the tinted
                      icon. Red line 4 -- the hue is the part that does not
                      arrive at 2am, or to a colour-blind reader. */}
                  {active === i && (
                    <span
                      className="absolute inset-y-1 left-0 w-0.5 rounded-full"
                      style={{ background: 'var(--vp-accent)' }}
                    />
                  )}
                  <span
                    className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md transition-colors duration-150 ease-vp"
                    style={{
                      background: active === i ? 'var(--vp-selection)' : 'var(--vp-surface-2)',
                    }}
                  >
                    <Folder
                      size={13}
                      style={{ color: active === i ? 'var(--vp-accent)' : 'var(--vp-ink-3)' }}
                    />
                  </span>
                  <span className="min-w-0 flex-1 truncate">
                    <RowName name={row.name} query={query} />
                  </span>
                  <ChevronRight
                    size={13}
                    className={`shrink-0 transition-transform duration-150 ease-vp ${
                      active === i ? 'translate-x-0.5 text-ink-2' : 'text-ink-3'
                    }`}
                  />
                </button>
              ))
            )}

            {listing !== null && !busy && rows.length === 0 && (
              <div className="px-6 py-8 text-center" data-testid="dir-blank">
                <Folder size={20} className="mx-auto mb-2 text-ink-3" />
                <p className="text-vp-base text-ink-2">
                  {query ? t('dir.noMatch', { q: safeText(query) }) : t('dir.empty')}
                </p>
                <button
                  type="button"
                  onClick={() => {
                    setNewName(query)
                    setText('')
                    startCreate()
                  }}
                  data-testid="dir-new-blank"
                  className="vp-press mt-3 rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-150 ease-vp hover:text-ink"
                >
                  {query ? t('dir.createNamed', { name: safeText(query) }) : t('dir.newFolder')}
                </button>
              </div>
            )}

            {listing?.truncated && (
              <p className="px-3 py-2 text-vp-sm text-ink-2" data-testid="dir-truncated">
                {t('dir.truncated', { shown: listing.entries.length, total: listing.total })}
              </p>
            )}
          </div>
        </div>

        <div className="shrink-0 border-t border-hairline p-2.5">
          {error?.at === 'confirm' && (
            <p
              data-testid="dir-error"
              className="mb-2 px-1 text-vp-sm"
              style={{ color: 'var(--vp-state-crashed)' }}
            >
              {safeText(error.message)}
            </p>
          )}
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
              onClick={() => run(primary, 'confirm')}
              disabled={busy || (listing === null && typed.kind !== 'path')}
              data-testid="dir-confirm"
              title={typed.kind === 'path' ? typed.abs : here}
              className="vp-press flex-[2] rounded-vp px-3 py-2 text-vp-md disabled:opacity-40"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {primaryLabel}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

/**
 * The name, with the part that matched marked.
 *
 * The span is measured on the raw name and the slices are taken from the
 * sanitised one, which is only correct because safeText replaces one character
 * with one character. Slicing the raw name instead would put an unfiltered
 * directional override on screen inside a highlight, which is the one place a
 * reader is looking closely.
 */
function RowName({ name, query }: { name: string; query: string }) {
  const safe = safeText(name)
  const span = matchSpan(name, query)
  if (!span) return <>{safe}</>
  return (
    <>
      {safe.slice(0, span[0])}
      <mark
        className="rounded-md px-0.5 font-medium text-ink"
        style={{ background: 'var(--vp-selection)' }}
      >
        {safe.slice(span[0], span[1])}
      </mark>
      {safe.slice(span[1])}
    </>
  )
}

/**
 * A slow read, designed rather than fallen into.
 *
 * Four bars the shape of the rows that are coming, so the list does not jump
 * when they are replaced, plus the word for what is happening -- the shimmer
 * is gone under prefers-reduced-motion and a row of grey bars on their own
 * could be an empty directory drawn badly.
 */
function Skeleton() {
  return (
    <div className="px-3 py-1" data-testid="dir-loading">
      {[0, 1, 2, 3].map((i) => (
        <div key={i} className="flex items-center gap-2.5 py-1.5" style={{ opacity: 1 - i * 0.2 }}>
          <span className="vp-skeleton h-6 w-6 shrink-0 rounded-md" />
          <span className="vp-skeleton h-3 rounded-md" style={{ width: `${55 - i * 9}%` }} />
        </div>
      ))}
      <p className="mt-2 text-vp-sm text-ink-3">{t('dir.loading')}</p>
    </div>
  )
}
