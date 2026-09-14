import { useEffect, useRef, useState } from 'react'
import { Eye, ExternalLink, Lock, LockOpen, Monitor, Pencil, Plus, Trash2 } from 'lucide-react'

import { api } from '../../protocol/api'
import type {
  Project,
  Session,
  ShareDetail,
  ShareLink,
  SharePageDetail,
  SharePageRow,
  ShareParamValue,
} from '../../protocol/wire'
import { t, useLang, type Key } from '../../i18n'
import { copyTextInGesture } from '../../clipboard'
import { safeText } from '../text'
import { ParamsForm } from './ParamsForm'
import { manifestFor, useNow } from './usePages'

/**
 * The links that show one page: making one, the address that is readable
 * once, and what can be changed on one afterwards.
 *
 * Under the page rather than in a list of their own, because a link is only
 * ever a way of showing a page -- and "which screens show this" is the
 * question somebody asks before they publish a change to it.
 */

const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** Expiry choices, in seconds. 0 is a link that does not expire. */
const EXPIRIES: { seconds: number; label: Key }[] = [
  { seconds: 0, label: 'share.expiryNever' },
  { seconds: 86400, label: 'share.expiryDay' },
  { seconds: 604800, label: 'share.expiryWeek' },
  { seconds: 2592000, label: 'share.expiryMonth' },
]

/** How long a name, remark or parameter edit sits still before it is saved:
 *  the screen is the preview, so it lands on finished words, not letters. */
const SAVE_AFTER_MS = 700

/** The server's bound on a remark (store.MaxRemark). */
const MAX_REMARK = 80

export function Field({
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
  return `${location.origin}/share/${token}/`
}

function scopeLabel(link: ShareLink): string {
  if (link.scope === '') return t('share.scopeWhole')
  if (link.scopeName) return safeText(link.scopeName)
  return t('share.scopeGone')
}

/**
 * Opens a fifteen-minute copy of a link in a new tab.
 *
 * The tab is opened inside the click and pointed at the address once it
 * exists: a window.open after an await is a popup the browser blocks. The
 * opener is cut before it navigates, so the page in that tab has no handle on
 * the panel's.
 */
async function peek(link: ShareLink, onError: (m: string) => void) {
  const tab = window.open('about:blank', '_blank')
  try {
    const made = await api.viewShare(link.id)
    if (tab) {
      tab.opener = null
      tab.location.href = shareURL(made.token)
    }
  } catch (e) {
    tab?.close()
    onError(e instanceof Error ? e.message : String(e))
  }
}

export function PageLinks({
  page,
  detail,
  links,
  projects,
  sessions,
  onChanged,
  onError,
}: {
  page: SharePageRow
  detail: SharePageDetail | null
  links: ShareLink[]
  projects: Project[]
  sessions: Session[]
  onChanged: () => void
  onError: (message: string) => void
}) {
  useLang()
  const [adding, setAdding] = useState(false)
  const [fresh, setFresh] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<string | null>(null)
  const unpublished = page.publishedVersion === 0

  const setLock = async (link: ShareLink, locked: boolean) => {
    try {
      if (locked) {
        await api.updateShare(link.id, {
          name: link.name,
          remark: link.remark,
          locked: true,
        })
        if (editing === link.id) setEditing(null)
      } else {
        await api.unlockShare(link.id)
      }
      onChanged()
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    }
  }

  const revoke = async (link: ShareLink) => {
    try {
      await api.deleteShare(link.id)
      setConfirming(null)
      onChanged()
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div data-testid="page-links" className="mt-2 pl-5">
      {fresh && (
        <div data-testid="share-fresh" className="mb-2 rounded-vp border border-hairline bg-surface-2 p-3">
          <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
            {t('share.once')}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <code
              data-testid="share-url"
              className="min-w-0 flex-1 truncate rounded-vp bg-surface px-2 py-1.5 font-mono text-vp-base text-ink"
            >
              {fresh}
            </code>
            <button
              type="button"
              data-testid="share-copy"
              onClick={() => copyTextInGesture(fresh, setCopied)}
              className="vp-press shrink-0 rounded-vp border border-hairline px-2 py-1.5 text-vp-base text-ink-2 hover:text-ink"
            >
              {copied ? t('tok.copied') : t('tok.copy')}
            </button>
            <a
              href={fresh}
              target="_blank"
              rel="noreferrer noopener"
              title={t('share.open')}
              aria-label={t('share.open')}
              data-testid="share-open"
              className="vp-press shrink-0 rounded-vp border border-hairline p-1.5 text-ink-2 hover:text-ink"
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
      )}

      {links.map((link) => (
        <div key={link.id} data-testid="share-row" className="border-t border-hairline py-1.5 text-vp-base first:border-t-0">
          <div className="flex items-center gap-2">
            <Monitor size={12} className="shrink-0 text-ink-3" />
            <span className="min-w-0 flex-1 truncate text-ink">{safeText(link.name)}</span>
            <span
              className="shrink-0 text-vp-sm text-ink-2"
              data-testid="share-row-viewers"
              data-viewers={link.viewers}
            >
              {link.viewers > 0 ? t('share.viewers', { n: link.viewers }) : t('share.noViewers')}
            </span>
            <button
              type="button"
              onClick={() => void peek(link, onError)}
              title={t('share.view')}
              aria-label={t('share.view')}
              data-testid="share-view"
              className="vp-control"
            >
              <Eye size={13} />
            </button>
            {/* Red line 4: an open padlock and a closed one, and the word in
                the title. */}
            <button
              type="button"
              onClick={() => void setLock(link, !link.locked)}
              aria-pressed={link.locked}
              title={link.locked ? t('share.unlock') : t('share.lock')}
              aria-label={link.locked ? t('share.unlock') : t('share.lock')}
              data-testid="share-lock"
              data-locked={link.locked}
              className="vp-control vp-press"
            >
              {link.locked ? <Lock size={13} /> : <LockOpen size={13} />}
            </button>
            <button
              type="button"
              disabled={link.locked}
              onClick={() => setEditing(editing === link.id ? null : link.id)}
              aria-pressed={editing === link.id}
              title={link.locked ? t('share.lockedRow') : t('share.edit')}
              aria-label={link.locked ? t('share.lockedRow') : t('share.edit')}
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
                aria-label={t('share.revoke')}
                data-testid="share-revoke"
                className="vp-control"
              >
                <Trash2 size={13} />
              </button>
            )}
          </div>
          <LinkFacts link={link} />
          {editing === link.id && (
            <LinkEditor
              link={link}
              detail={detail}
              onSaved={onChanged}
              onError={onError}
              onClose={() => setEditing(null)}
            />
          )}
        </div>
      ))}

      {adding ? (
        <NewLink
          page={page}
          detail={detail}
          projects={projects}
          sessions={sessions}
          onCancel={() => setAdding(false)}
          onMade={(token) => {
            setAdding(false)
            setFresh(shareURL(token))
            setCopied(false)
            onChanged()
          }}
          onError={onError}
        />
      ) : (
        <button
          type="button"
          data-testid="share-new"
          disabled={unpublished}
          onClick={() => setAdding(true)}
          title={unpublished ? t('share.needsPublish') : undefined}
          className="vp-press mt-1 flex items-center gap-1 rounded-vp px-2 py-1 text-vp-sm text-ink-2 hover:text-ink disabled:opacity-40"
        >
          <Plus size={12} />
          {unpublished ? t('share.needsPublish') : t('share.create')}
        </button>
      )}
    </div>
  )
}

function LinkFacts({ link }: { link: ShareLink }) {
  const now = useNow()
  return (
    <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 pl-5 text-vp-sm text-ink-2">
      <code className="font-mono text-ink-3">{link.prefix}…</code>
      <span data-testid="share-row-scope">{scopeLabel(link)}</span>
      <span>{link.detail === 'names' ? t('share.detailNames') : t('share.detailCounts')}</span>
      {link.pinUntil > now ? (
        <span>{t('page.trialShort', { v: link.pinVersion })}</span>
      ) : (
        link.pinVersion > 0 && <span>{t('page.pinned', { v: link.pinVersion })}</span>
      )}
      <span>
        {link.expiresAt === 0
          ? t('share.noExpiry')
          : t('share.expiresOn', { date: new Date(link.expiresAt * 1000).toLocaleDateString() })}
      </span>
      {link.remark !== '' && (
        <span className="min-w-0 truncate text-ink-3" data-testid="share-row-remark">
          {safeText(link.remark)}
        </span>
      )}
    </div>
  )
}

function NewLink({
  page,
  detail,
  projects,
  sessions,
  onCancel,
  onMade,
  onError,
}: {
  page: SharePageRow
  detail: SharePageDetail | null
  projects: Project[]
  sessions: Session[]
  onCancel: () => void
  onMade: (token: string) => void
  onError: (message: string) => void
}) {
  const [name, setName] = useState('')
  const [remark, setRemark] = useState('')
  const [shows, setShows] = useState<ShareDetail>('counts')
  const [expiresIn, setExpiresIn] = useState(0)
  // "", "project:<id>" or "session:<id>": the two halves are never chosen apart.
  const [target, setTarget] = useState('')
  const [params, setParams] = useState<Record<string, ShareParamValue>>({})
  const [busy, setBusy] = useState(false)
  const manifest = manifestFor(detail, 0)
  const id = page.id

  const create = async () => {
    const [scope, scopeId] = target === '' ? ['', ''] : target.split(':', 2)
    setBusy(true)
    try {
      const made = await api.createShare({
        name: name.trim() || page.name,
        detail: shows,
        expiresIn,
        scope,
        scopeId: scopeId ?? '',
        remark: remark.trim(),
        locked: false,
        pageId: page.id,
        params,
      })
      onMade(made.token)
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div data-testid="share-form" className="mt-2 rounded-vp border border-hairline bg-surface-2 p-3">
      <div className="mb-2 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2 @3xl:grid-cols-3">
        <Field label={t('share.nameLabel')} htmlFor={`share-name-${id}`}>
          <input
            id={`share-name-${id}`}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={safeText(page.name)}
            data-testid="share-name"
            className={INPUT}
          />
        </Field>
        <Field label={t('share.remarkLabel')} htmlFor={`share-remark-${id}`}>
          <input
            id={`share-remark-${id}`}
            value={remark}
            maxLength={MAX_REMARK}
            onChange={(e) => setRemark(e.target.value)}
            placeholder={t('share.remark')}
            data-testid="share-remark"
            className={INPUT}
          />
        </Field>
        <Field label={t('share.scopeLabel')} htmlFor={`share-scope-${id}`}>
          <select
            id={`share-scope-${id}`}
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
              .filter((s) => !s.scratch)
              .map((s) => (
                <option key={s.id} value={`session:${s.id}`}>
                  {t('share.scopeSession', { name: safeText(s.title || t('share.untitled')) })}
                </option>
              ))}
          </select>
        </Field>
        <Field label={t('share.shows')} htmlFor={`share-detail-${id}`}>
          <select
            id={`share-detail-${id}`}
            value={shows}
            onChange={(e) => setShows(e.target.value as ShareDetail)}
            data-testid="share-detail"
            className={INPUT}
          >
            <option value="counts">{t('share.detailCounts')}</option>
            <option value="names">{t('share.detailNames')}</option>
          </select>
        </Field>
        <Field label={t('share.expiry')} htmlFor={`share-expiry-${id}`}>
          <select
            id={`share-expiry-${id}`}
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
      </div>
      {manifest && (manifest.params ?? []).length > 0 && (
        <div className="mb-2">
          <ParamsForm specs={manifest.params ?? []} values={params} idPrefix={`share-param-${id}`} onChange={setParams} />
        </div>
      )}
      <p className="mb-2 text-vp-sm leading-relaxed text-ink-3">{t('share.detailWhy')}</p>
      <div className="flex flex-wrap items-center justify-end gap-2">
        <button
          type="button"
          onClick={onCancel}
          className="vp-press rounded-vp px-2 py-1 text-vp-base text-ink-2"
        >
          {t('share.cancel')}
        </button>
        <button
          type="button"
          onClick={() => void create()}
          disabled={busy}
          data-testid="share-create"
          className="vp-press rounded-vp px-3 py-1.5 text-vp-base disabled:opacity-40"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          {t('share.create')}
        </button>
      </div>
    </div>
  )
}

/**
 * What can change on a link that has been handed out: what it is called, the
 * label on the screen, which version it holds, and the page's settings on it.
 * Saved as it changes -- the screen is the preview, and a save button on a
 * thing you are watching change is the one that gets forgotten.
 */
function LinkEditor({
  link,
  detail,
  onSaved,
  onError,
  onClose,
}: {
  link: ShareLink
  detail: SharePageDetail | null
  onSaved: () => void
  onError: (message: string) => void
  onClose: () => void
}) {
  const [name, setName] = useState(link.name)
  const [remark, setRemark] = useState(link.remark)
  const [params, setParams] = useState<Record<string, ShareParamValue>>(link.params)
  const labelTimer = useRef(0)
  const paramTimer = useRef(0)
  const now = useNow()
  const trial = link.pinUntil > now
  const manifest = manifestFor(detail, link.pinVersion)

  useEffect(
    () => () => {
      clearTimeout(labelTimer.current)
      clearTimeout(paramTimer.current)
    },
    [],
  )

  const saveLabels = (nextName: string, nextRemark: string) => {
    clearTimeout(labelTimer.current)
    labelTimer.current = window.setTimeout(() => {
      api.updateShare(link.id, { name: nextName, remark: nextRemark, locked: false }).then(onSaved, (e: unknown) =>
        onError(e instanceof Error ? e.message : String(e)),
      )
    }, SAVE_AFTER_MS)
  }

  const saveDrawing = (pinVersion: number, next: Record<string, ShareParamValue>) =>
    api.setSharePage(link.id, { pageId: link.pageId, pinVersion, params: next }).then(onSaved, (e: unknown) =>
      onError(e instanceof Error ? e.message : String(e)),
    )

  return (
    <div data-testid="share-edit-panel" className="mt-2 mb-1 rounded-vp border border-hairline bg-surface-2 p-3">
      <div className="mb-2 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-3">
        <Field label={t('share.nameLabel')} htmlFor={`share-edit-name-${link.id}`}>
          <input
            id={`share-edit-name-${link.id}`}
            value={name}
            onChange={(e) => {
              setName(e.target.value)
              saveLabels(e.target.value, remark)
            }}
            data-testid="share-edit-name"
            className={INPUT}
          />
        </Field>
        <Field label={t('share.remarkLabel')} htmlFor={`share-edit-remark-${link.id}`}>
          <input
            id={`share-edit-remark-${link.id}`}
            value={remark}
            maxLength={MAX_REMARK}
            onChange={(e) => {
              setRemark(e.target.value)
              saveLabels(name, e.target.value)
            }}
            placeholder={t('share.remark')}
            data-testid="share-edit-remark"
            className={INPUT}
          />
        </Field>
        {detail && (
          <Field label={t('page.version')} htmlFor={`link-pin-${link.id}`}>
            <select
              id={`link-pin-${link.id}`}
              data-testid="link-pin"
              value={trial ? -1 : link.pinVersion}
              disabled={trial}
              onChange={(e) => void saveDrawing(Number(e.target.value), params)}
              className={INPUT}
            >
              {trial && <option value={-1}>{t('page.trialShort', { v: link.pinVersion })}</option>}
              <option value={0}>{t('page.followPublished', { v: detail.page.publishedVersion })}</option>
              {detail.versions
                .filter((v) => !v.candidate)
                .map((v) => (
                  <option key={v.version} value={v.version}>
                    {t('page.pinned', { v: v.version })}
                  </option>
                ))}
            </select>
          </Field>
        )}
      </div>
      {manifest && (
        <ParamsForm
          specs={manifest.params ?? []}
          values={params}
          idPrefix={`param-${link.id}`}
          onChange={(next) => {
            setParams(next)
            clearTimeout(paramTimer.current)
            paramTimer.current = window.setTimeout(() => void saveDrawing(link.pinVersion, next), SAVE_AFTER_MS)
          }}
        />
      )}
      <div className="mt-2 flex justify-end">
        <button
          type="button"
          onClick={onClose}
          data-testid="share-edit-close"
          className="vp-press rounded-vp px-2 py-1 text-vp-base text-ink-2"
        >
          {t('share.editDone')}
        </button>
      </div>
    </div>
  )
}
