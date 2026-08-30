import { useCallback, useEffect, useRef, useState } from 'react'
import { ExternalLink, Lock, LockOpen, Monitor, MonitorSmartphone, Pencil, Trash2 } from 'lucide-react'

import { api } from '../protocol/api'
import type {
  Project,
  Session,
  ShareBoard,
  ShareCatalogue,
  ShareDetail,
  ShareLink,
  SharePreset,
} from '../protocol/wire'
import type { Key } from '../i18n'
import { t, useLang } from '../i18n'
import { BoardEditor } from './BoardEditor'
import { useBoardPreview } from './board/preview'
import { presetLabel } from './board/labels'
import { safeText } from './text'
import { copyTextInGesture } from '../clipboard'

/**
 * Read-only share links, and the place a screen on a wall is edited from.
 *
 * The second half is the point of the page now. The case this exists for is a
 * television: nobody is standing at it, and walking to it to sign in and move a
 * widget is the thing that must not be necessary. So the board is changed from
 * here, on a laptop, and the wall picks it up on its next poll — two seconds —
 * because every poll re-reads the link's row. Nothing was added to the share
 * token's own surface to make that work; it is still exactly one GET.
 *
 * Three things make editing a screen you cannot see workable, and all three are
 * on this page:
 *
 *   - a preview drawn from the server's own builder, at the shape of the screen
 *     that is actually showing the link;
 *   - the count of screens that have it open, which is what tells you whether
 *     the wall you are about to rearrange is on at all;
 *   - a lock, so the one showing to a customer is not the one you edit by
 *     accident with several links open in a list.
 *
 * Editing is **live**, debounced. The wall is the preview, and a save button on
 * a thing you are watching change is a second source of truth: the failure it
 * creates is worse than a flicker, because you edit, walk away, and the screen
 * keeps the old board because nobody pressed it. Debounced so the wall lands on
 * finished states rather than on every keystroke, and so one edit is one row
 * write rather than one per character.
 *
 * The token is still readable exactly once, in the response that made it,
 * because the database keeps a SHA-256 and a leaked backup must not hand over
 * live links.
 */

/** Expiry choices, in seconds. 0 is a link that does not expire. */
const EXPIRIES: { seconds: number; label: Key }[] = [
  { seconds: 0, label: 'share.expiryNever' },
  { seconds: 86400, label: 'share.expiryDay' },
  { seconds: 604800, label: 'share.expiryWeek' },
  { seconds: 2592000, label: 'share.expiryMonth' },
]

/**
 * How long an edit sits still before it reaches the wall.
 *
 * Long enough that typing a caption arrives as a word rather than as letters,
 * short enough that dragging a widget and looking up is the same gesture.
 */
const SAVE_AFTER_MS = 700

/** How often the list is refetched while it is on screen.
 *
 *  Only for the viewer counts, which are true for about fifteen seconds each.
 *  A row that says "2 watching" about a television somebody switched off ten
 *  minutes ago is the one number on this page nobody would think to distrust. */
const LIST_MS = 5000

/** Every field in this page's two forms, so they are one width and one shape. */
const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** A control with its name above it, filling one cell of the grid. */
function Field({
  label,
  htmlFor,
  children,
}: {
  label: string
  htmlFor: string
  children: React.ReactNode
}) {
  return (
    <div className="min-w-0">
      <label htmlFor={htmlFor} className="mb-1 block text-vp-sm text-ink-3">
        {label}
      </label>
      {children}
    </div>
  )
}

function shareURL(token: string): string {
  return `${location.origin}/share/${token}`
}

function detailLabel(detail: string): string {
  return detail === 'names' ? t('share.detailNames') : t('share.detailCounts')
}

function scopeLabel(link: ShareLink): string {
  if (link.scope === '') return t('share.scopeWhole')
  if (link.scopeName) return safeText(link.scopeName)
  // A scope whose row is gone. Said plainly rather than left blank: a link
  // pointing at a deleted project shows an empty dashboard forever, and the
  // settings page is the only place that can explain why.
  return t('share.scopeGone')
}

/** What is being edited, and whether it has reached the server yet. */
interface Editing {
  id: string
  name: string
  remark: string
  board: ShareBoard
}

export function ShareLinks() {
  useLang()
  const [links, setLinks] = useState<ShareLink[]>([])
  // Fetched here rather than threaded down from the settings page, which is
  // opened from three places and would have to carry them through all three.
  // One extra request, once, while a modal is open.
  const [projects, setProjects] = useState<Project[]>([])
  const [sessions, setSessions] = useState<Session[]>([])
  const [catalogue, setCatalogue] = useState<ShareCatalogue | null>(null)
  const [name, setName] = useState('')
  const [remark, setRemark] = useState('')
  const [detail, setDetail] = useState<ShareDetail>('counts')
  const [expiresIn, setExpiresIn] = useState(0)
  // "", "project:<id>" or "session:<id>". One control rather than two, because
  // the two halves are never chosen independently.
  const [target, setTarget] = useState('')
  const [board, setBoard] = useState<ShareBoard | null>(null)
  const [fresh, setFresh] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  // Which row is one more click from being revoked. An inline second step
  // rather than a browser dialog: this panel is used on a phone, where a
  // native confirm covers the screen and reads as a page change.
  const [confirming, setConfirming] = useState<string | null>(null)
  const [editing, setEditing] = useState<Editing | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    try {
      setLinks(await api.listShares())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    // The first listing is fetched here rather than through refresh(), which is
    // an async function the hooks lint reads as a setState in an effect body.
    // Same request, and the cancellation flag is what an unmounted modal needs.
    api.listShares().then(
      (list) => {
        if (!cancelled) setLinks(list)
      },
      (e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      },
    )
    // Kept fresh while the page is open, for the viewer counts. Nothing else on
    // a row changes without this tab having changed it.
    const timer = window.setInterval(() => void refresh(), LIST_MS)
    api.state().then(
      (state) => {
        if (cancelled) return
        setProjects(state.projects)
        setSessions(state.sessions)
      },
      // A scope picker that cannot be filled still leaves a working form: the
      // whole-panel option is the one that needs no list.
      () => {},
    )
    api.shareCatalogue().then(
      (cat) => {
        if (cancelled) return
        setCatalogue(cat)
        // Start from the first preset rather than from nothing: an empty board
        // is refused by the server, and offering a state that cannot be saved
        // is offering a dead end.
        const first = cat.presets[0]
        if (first) {
          setBoard({
            grid: cat.maxSpan,
            preset: first.id,
            rotate: first.rotate,
            fill: first.fill,
            density: first.density,
            widgets: [...first.widgets],
          })
        }
      },
      (e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      },
    )
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [refresh])

  // ── the live edit ──────────────────────────────────────────────────────
  //
  // A ref for the timer rather than state, because rescheduling it must not
  // re-render: the thing being edited is a board with twenty selects in it, and
  // a render per keystroke is a select that loses focus while somebody is using
  // it.
  const saveTimer = useRef(0)
  useEffect(() => {
    if (!editing) return
    clearTimeout(saveTimer.current)
    saveTimer.current = window.setTimeout(() => {
      void (async () => {
        setSaving(true)
        try {
          await api.updateShare(editing.id, {
            name: editing.name,
            remark: editing.remark,
            board: editing.board,
            locked: false,
          })
          setError('')
          await refresh()
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e))
        } finally {
          setSaving(false)
        }
      })()
    }, SAVE_AFTER_MS)
    return () => clearTimeout(saveTimer.current)
  }, [editing, refresh])

  const create = async () => {
    if (!board) return
    const [scope, scopeId] = target === '' ? ['', ''] : target.split(':', 2)
    try {
      const made = await api.createShare({
        name: name.trim(),
        detail,
        expiresIn,
        board,
        scope,
        scopeId: scopeId ?? '',
        remark: remark.trim(),
        locked: false,
      })
      setFresh(shareURL(made.token))
      setCopied(false)
      setName('')
      setRemark('')
      await refresh()
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const setLock = async (link: ShareLink, locked: boolean) => {
    try {
      if (locked) {
        await api.updateShare(link.id, {
          name: link.name,
          remark: link.remark,
          board: link.board,
          locked: true,
        })
        // The editor closes on lock. Leaving it open on a board that can no
        // longer be saved is a form that silently stops working.
        if (editing?.id === link.id) setEditing(null)
      } else {
        await api.unlockShare(link.id)
      }
      await refresh()
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const revoke = async (link: ShareLink) => {
    try {
      await api.deleteShare(link.id)
      await refresh()
      setConfirming(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    // A container, and everything inside it measures against this rather than
    // against the window. The board editor is the reason -- see BoardEditor --
    // but the form below had the same bug in miniature: `sm:` is true at 640px
    // of *browser*, which is the width at which this panel has about 340px.
    <div data-testid="share-links" className="@container">
      <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('share.why')}</p>

      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-crashed)' }}>
          {safeText(error)}
        </p>
      )}

      {fresh ? (
        <div className="mb-3 rounded-vp border border-hairline bg-surface-2 p-3">
          <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
            {t('share.once')}
          </p>
          <div className="flex items-center gap-2">
            <code
              data-testid="share-url"
              className="min-w-0 flex-1 truncate rounded-vp bg-surface px-2 py-1.5 font-mono text-vp-base text-ink"
            >
              {fresh}
            </code>
            <button
              type="button"
              data-testid="share-copy"
              onClick={() => {
                copyTextInGesture(fresh, setCopied)
              }}
              className="vp-press shrink-0 rounded-vp border border-hairline px-2 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
            >
              {copied ? t('tok.copied') : t('tok.copy')}
            </button>
            <a
              href={fresh}
              target="_blank"
              rel="noreferrer"
              title={t('share.open')}
              data-testid="share-open"
              className="vp-press shrink-0 rounded-vp border border-hairline p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
            >
              <ExternalLink size={13} />
            </a>
            <button
              type="button"
              data-testid="share-dismiss"
              onClick={() => setFresh(null)}
              className="shrink-0 rounded-vp px-2.5 py-1.5 text-vp-base"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('tok.done')}
            </button>
          </div>
        </div>
      ) : (
        <div className="mb-3">
          {/* A grid of labelled fields, not a row that wraps.

              It was five controls and a button in one `flex-wrap`, and the
              arithmetic went the wrong way at every width: the two `flex-1`
              text inputs gave up their space to three `shrink-0` selects, so
              at the width this panel actually has, "What is it for" was a
              90px box showing four truncated characters while the scope
              select was 570px wide. Then the row wrapped between the expiry
              and the button, which put the primary action on a line of its
              own with nothing to say what it applied to.

              Labels above rather than placeholders inside, for the reason
              written on the density control in BoardEditor: a placeholder is
              gone as soon as there is a value, `title` needs a pointer that a
              phone does not have, and a truncated placeholder is a control
              nobody can name. */}
          <div className="mb-2 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2 @3xl:grid-cols-3">
            <Field label={t('share.nameLabel')} htmlFor="share-name">
              <input
                id="share-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') void create()
                }}
                placeholder={t('share.name')}
                data-testid="share-name"
                className={INPUT}
              />
            </Field>
            <Field label={t('share.remarkLabel')} htmlFor="share-remark">
              <input
                id="share-remark"
                value={remark}
                maxLength={catalogue?.maxRemark ?? 80}
                onChange={(e) => setRemark(e.target.value)}
                placeholder={t('share.remark')}
                data-testid="share-remark"
                className={INPUT}
              />
            </Field>
            <Field label={t('share.scopeLabel')} htmlFor="share-scope">
              <select
                id="share-scope"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                data-testid="share-scope"
                className={INPUT}
              >
                <option value="">{t('share.scopeWhole')}</option>
                {projects.map((p) => (
                  <option key={p.id} value={`project:${p.id}`}>
                    {t('share.scopeProject', { name: safeText(p.name) })}
                  </option>
                ))}
                {sessions
                  .filter((s) => s.parentSessionId === null)
                  .map((s) => (
                    <option key={s.id} value={`session:${s.id}`}>
                      {t('share.scopeSession', {
                        name: safeText(s.title || t('share.untitled')),
                      })}
                    </option>
                  ))}
              </select>
            </Field>
            <Field label={t('share.shows')} htmlFor="share-detail">
              <select
                id="share-detail"
                value={detail}
                onChange={(e) => setDetail(e.target.value as ShareDetail)}
                data-testid="share-detail"
                className={INPUT}
              >
                <option value="counts">{t('share.detailCounts')}</option>
                <option value="names">{t('share.detailNames')}</option>
              </select>
            </Field>
            <Field label={t('share.expiry')} htmlFor="share-expiry">
              <select
                id="share-expiry"
                value={expiresIn}
                onChange={(e) => setExpiresIn(Number(e.target.value))}
                data-testid="share-expiry"
                className={INPUT}
              >
                {EXPIRIES.map((choice) => (
                  <option key={choice.seconds} value={choice.seconds}>
                    {t(choice.label)}
                  </option>
                ))}
              </select>
            </Field>
            {/* Bottom-aligned in its cell, so it sits on the baseline of the
                last row of fields rather than level with their labels. */}
            <div className="flex items-end">
              <button
                type="button"
                onClick={() => void create()}
                disabled={!board || board.widgets.length === 0}
                data-testid="share-create"
                className="vp-press w-full rounded-vp px-3 py-1.5 text-vp-base @md:w-auto"
                style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
              >
                {t('share.create')}
              </button>
            </div>
          </div>

          {catalogue && board && (
            <BoardEditorFor
              linkID=""
              board={board}
              catalogue={catalogue}
              viewportWidth={0}
              viewportHeight={0}
              onChange={setBoard}
              // A preset can carry a disclosure decision. One of them is only
              // correct scoped to a single project with names off, because the
              // failure is a customer reading another customer's project name
              // off the screen they were sat in front of. Applied here rather
              // than left to somebody to remember; the server checks both
              // again, from the request, exactly as it always did.
              onPickPreset={(preset) => {
                if (preset.detail === 'counts' || preset.detail === 'names') {
                  setDetail(preset.detail)
                }
                if (preset.needsScope && target === '' && projects[0]) {
                  setTarget(`project:${projects[0].id}`)
                }
              }}
            />
          )}

          <p className="mt-2 text-vp-sm leading-relaxed text-ink-3">{t('share.detailWhy')}</p>
          <p className="text-vp-sm leading-relaxed text-ink-3">{t('share.remarkWhy')}</p>
        </div>
      )}

      {links.length === 0 ? (
        <p className="text-vp-base text-ink-3">{t('share.none')}</p>
      ) : (
        links.map((link) => (
          <div key={link.id} data-testid="share-row-wrap">
            {/* Two lines, and the second one wraps.

                This was eleven things in one `flex` line: a name, a remark,
                the prefix, the viewer count, the scope, the board, the detail
                mode, the expiry and three buttons. Nine of them were
                `shrink-0`, which adds up to more than this panel is wide at
                any size it has ever had -- so the row overflowed the dialog
                sideways, `overflow-y: auto` on the body computed `overflow-x`
                to `auto` behind it, and pressing the pencil scrolled the
                whole settings page right to bring the button into view. The
                fields also started at a different x on every row, because
                each one's width came from its own text.

                So: what identifies the link and what you press on the first
                line, with the actions ending at the right margin on every
                row; everything that describes it on a second line that is
                allowed to wrap. Nothing here is `shrink-0` except the
                buttons. */}
            <div
              data-testid="share-row"
              className="border-t border-hairline py-2 text-vp-base first:border-t-0"
            >
              <div className="flex items-center gap-2">
                <MonitorSmartphone size={13} className="shrink-0 text-ink-2" />
                <span className="min-w-0 flex-1 truncate text-ink">{safeText(link.name)}</span>
                {/* How many screens have this open. An icon and a count, never a
                    colour: this is the number that decides whether the wall you
                    are about to rearrange is on at all.

                    On the first line rather than with the rest of the facts,
                    because it is the one that decides whether to touch the
                    board at all -- and it is the only one that changes while
                    you are looking at the page. */}
                <span
                  className="flex shrink-0 items-center gap-1 text-vp-sm text-ink-2"
                  data-testid="share-row-viewers"
                  data-viewers={link.viewers}
                >
                  {link.viewers > 0 ? (
                    <>
                      <Monitor size={12} />
                      {t('share.viewers', { n: link.viewers })}
                    </>
                  ) : (
                    <span className="text-ink-3">{t('share.noViewers')}</span>
                  )}
                </span>
              {/* Red line 4: an open padlock and a closed one, plus the word in
                  the title. A locked row is not distinguished by a tint. */}
              <button
                type="button"
                onClick={() => void setLock(link, !link.locked)}
                aria-pressed={link.locked}
                title={link.locked ? t('share.unlock') : t('share.lock')}
                data-testid="share-lock"
                data-locked={link.locked}
                className="vp-control vp-press"
              >
                {link.locked ? <Lock size={13} /> : <LockOpen size={13} />}
              </button>
              <button
                type="button"
                disabled={link.locked}
                onClick={() =>
                  setEditing(
                    editing?.id === link.id
                      ? null
                      : {
                          id: link.id,
                          name: link.name,
                          remark: link.remark,
                          board: link.board,
                        },
                  )
                }
                title={link.locked ? t('board.locked') : t('board.edit')}
                data-testid="share-edit"
                className="vp-control disabled:opacity-40"
              >
                <Pencil size={13} />
              </button>
              {confirming === link.id ? (
                <span className="flex shrink-0 items-center gap-1">
                  <button
                    type="button"
                    onClick={() => void revoke(link)}
                    data-testid="share-revoke-confirm"
                    className="vp-press shrink-0 rounded-vp px-2 py-1 text-vp-sm"
                    style={{ background: 'var(--vp-state-crashed)', color: 'var(--vp-accent-ink)' }}
                  >
                    {t('share.revokeSure')}
                  </button>
                  <button
                    type="button"
                    onClick={() => setConfirming(null)}
                    className="vp-press shrink-0 rounded-vp px-2 py-1 text-vp-sm text-ink-2"
                  >
                    {t('share.keep')}
                  </button>
                </span>
              ) : (
                <button
                  type="button"
                  onClick={() => setConfirming(link.id)}
                  title={t('share.revoke')}
                  data-testid="share-revoke"
                  className="vp-control"
                >
                  <Trash2 size={13} />
                </button>
              )}
              </div>

              {/* Everything the link *is*, indented under its name. Separated
                  by space rather than by a middot chain, so a fact that wraps
                  does not leave a dot at the end of a line. */}
              <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 pl-5 text-vp-sm text-ink-2">
                <code className="font-mono text-ink-3">{link.prefix}…</code>
                <span data-testid="share-row-scope">{scopeLabel(link)}</span>
                {link.board.preset !== '' && (
                  <span data-testid="share-row-board">{presetLabel(link.board.preset)}</span>
                )}
                <span>{detailLabel(link.detail)}</span>
                <span>
                  {link.expiresAt === 0
                    ? t('share.noExpiry')
                    : t('share.expiresOn', {
                        date: new Date(link.expiresAt * 1000).toLocaleDateString(),
                      })}
                </span>
                {link.remark !== '' && (
                  <span className="min-w-0 truncate text-ink-3" data-testid="share-row-remark">
                    {safeText(link.remark)}
                  </span>
                )}
              </div>
            </div>

            {editing?.id === link.id && catalogue && (
              <div
                className="mb-2 rounded-vp border border-hairline bg-surface-2 p-3"
                data-testid="share-edit-panel"
              >
                {/* The same two fields as the form above, labelled the same
                    way and in the same grid, because they are the same two
                    fields. */}
                <div className="mb-3 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
                  <Field label={t('share.nameLabel')} htmlFor="share-edit-name">
                    <input
                      id="share-edit-name"
                      value={editing.name}
                      onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                      placeholder={t('share.name')}
                      data-testid="share-edit-name"
                      className={INPUT}
                    />
                  </Field>
                  <Field label={t('share.remarkLabel')} htmlFor="share-edit-remark">
                    <input
                      id="share-edit-remark"
                      value={editing.remark}
                      maxLength={catalogue.maxRemark}
                      onChange={(e) => setEditing({ ...editing, remark: e.target.value })}
                      placeholder={t('share.remark')}
                      data-testid="share-edit-remark"
                      className={INPUT}
                    />
                  </Field>
                  {/* Two words, and no save button. The wall is the preview;
                      a button on a thing you are watching change is a second
                      source of truth, and the one that gets forgotten. */}
                  {/* The pair sits at the bottom of its cell so it lines up
                      with the two inputs, and its own two things are centred
                      on each other -- bottom-aligning a small word against a
                      padded button staggers them by the difference in their
                      line heights. */}
                  <div className="flex items-end">
                    <div className="flex w-full items-center justify-end gap-2 py-1.5">
                      <span className="text-vp-sm text-ink-3" data-testid="share-edit-status">
                        {saving ? t('board.saving') : t('board.live')}
                      </span>
                      <button
                        type="button"
                        onClick={() => setEditing(null)}
                        data-testid="share-edit-cancel"
                        className="vp-press rounded-vp px-2 py-1 text-vp-base text-ink-2"
                      >
                        {t('board.cancel')}
                      </button>
                    </div>
                  </div>
                </div>
                {/* One surface, not two. The picture of the wall used to sit
                    beside a list of dropdowns; it is the thing you arrange
                    now. */}
                <BoardEditorFor
                  linkID={link.id}
                  board={editing.board}
                  catalogue={catalogue}
                  viewportWidth={link.viewportWidth}
                  viewportHeight={link.viewportHeight}
                  onChange={(next) => setEditing({ ...editing, board: next })}
                />
              </div>
            )}
          </div>
        ))
      )}
    </div>
  )
}

/**
 * The editor with its own live payload attached.
 *
 * A wrapper rather than a fetch inside BoardEditor, so that file has no network
 * in it and so a board being *created* -- which has no link and therefore no
 * preview -- goes through the same component as one being edited. The canvas
 * still draws the arrangement in that case; the rectangles are the board.
 */
function BoardEditorFor(props: {
  linkID: string
  board: ShareBoard
  catalogue: ShareCatalogue
  viewportWidth: number
  viewportHeight: number
  onChange: (next: ShareBoard) => void
  onPickPreset?: (preset: SharePreset) => void
}) {
  const preview = useBoardPreview(props.linkID)
  return (
    <div>
      <BoardEditor
        board={props.board}
        catalogue={props.catalogue}
        preview={preview.data}
        linkID={props.linkID}
        viewportWidth={props.viewportWidth}
        viewportHeight={props.viewportHeight}
        onChange={props.onChange}
        onPickPreset={props.onPickPreset}
      />
      {preview.failed && (
        <p className="mt-1 text-vp-sm text-ink-3" data-testid="preview-failed">
          {t('share.previewGone')}
        </p>
      )}
    </div>
  )
}
