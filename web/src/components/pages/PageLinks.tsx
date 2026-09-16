import { useEffect, useRef, useState } from 'react'
import {
  CalendarClock,
  Copy,
  Eye,
  ExternalLink,
  Hand,
  Lock,
  LockOpen,
  Monitor,
  Pencil,
  Pin,
  Plus,
  RefreshCw,
  ScanEye,
  Target,
  Trash2,
} from 'lucide-react'

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
import { askConfirm } from '../ask'
import { safeText } from '../text'
import { Menu } from '../Menu'
import { ParamsForm } from './ParamsForm'
import { BlockTitle, Chip, Mark } from './bits'
import { manifestFor, useNow } from './usePages'

/**
 * The links that show one page: making one, copying its address again, and
 * what can be changed on one afterwards.
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

/**
 * One question the form asks, under its own heading.
 *
 * The form was eight controls and a paragraph in one grid, and the grid put
 * what the link is called, who can see what, and the page's own colours in one
 * run of boxes with a hole in the middle of it. Three headings are the three
 * decisions, and the disclosure one is the one worth reading.
 */
function Group({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0 border-t border-hairline pt-3 first:border-t-0 first:pt-0">
      <h4 className="mb-2 text-vp-xs font-semibold tracking-wide text-ink-3 uppercase">{title}</h4>
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

/** Why an address is on screen: just made, copied from a row, or just replaced. */
type Shown = { url: string; kind: 'new' | 'copied' | 'rotated' }

export function PageLinks({
  page,
  detail,
  links,
  projects,
  sessions,
  visitorWrites,
  onChanged,
  onError,
}: {
  page: SharePageRow
  detail: SharePageDetail | null
  links: ShareLink[]
  projects: Project[]
  sessions: Session[]
  /** The panel-wide switch: off refuses every visitor action on every link. */
  visitorWrites: boolean
  onChanged: () => void
  onError: (message: string) => void
}) {
  useLang()
  const [adding, setAdding] = useState(false)
  const [fresh, setFresh] = useState<Shown | null>(null)
  const [copied, setCopied] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
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

  // The address, again. A link made before tokens were kept encrypted has none
  // to show, and the only address it can have is a new one.
  const copyAddress = async (link: ShareLink) => {
    if (!link.copyable) {
      await rotate(link)
      return
    }
    try {
      const got = await api.shareURL(link.id)
      setCopied(false)
      setFresh({ url: got.url, kind: 'copied' })
      copyTextInGesture(got.url, setCopied)
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    }
  }

  const rotate = async (link: ShareLink) => {
    const yes = await askConfirm({
      title: t('share.rotateTitle', { name: safeText(link.name) }),
      body: link.copyable ? t('share.rotateBody') : t('share.rotateLegacyBody'),
      confirm: t('share.rotate'),
      cancel: t('ask.cancel'),
      destructive: true,
    })
    if (!yes) return
    try {
      const got = await api.rotateShare(link.id)
      setCopied(false)
      setFresh({ url: got.url, kind: 'rotated' })
      onChanged()
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    }
  }

  // Asked in the panel's own dialog rather than by turning the row into two
  // buttons. The inline pair was there because a browser `confirm` covers a
  // phone; `askConfirm` is the panel's answer to that and it is what the rest
  // of this file already uses, so revoking reads like every other question the
  // panel asks -- and the row keeps its shape while it is being asked.
  const revoke = async (link: ShareLink) => {
    const yes = await askConfirm({
      title: t('share.revokeTitle', { name: safeText(link.name) }),
      body: t('share.revokeBody'),
      confirm: t('share.revoke'),
      cancel: t('ask.cancel'),
      destructive: true,
    })
    if (!yes) return
    try {
      await api.deleteShare(link.id)
      onChanged()
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    // The links a page is shown through, as a block of the page's card rather
    // than as rows indented under its name. The indent was the only thing
    // saying these belonged to the page above them, and at a phone's width
    // there is no room to spend on an indent.
    <div data-testid="page-links" className="border-t border-hairline px-3 py-3 @md:px-4">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <BlockTitle count={links.length}>{t('page.linksTitle')}</BlockTitle>
        <span className="flex-1" />
        {!adding && (
          <button
            type="button"
            data-testid="share-new"
            disabled={unpublished}
            onClick={() => setAdding(true)}
            title={unpublished ? t('share.needsPublish') : undefined}
            className="vp-press flex shrink-0 items-center gap-1 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:border-accent hover:text-ink disabled:opacity-40 disabled:hover:border-hairline"
          >
            <Plus size={12} />
            {t('share.create')}
          </button>
        )}
      </div>

      {fresh && (
        <div
          data-testid="share-fresh"
          className="vp-panel-in mb-2 rounded-vp border border-accent/40 bg-surface p-3"
        >
          <p className="mb-2 text-vp-sm text-ink-2" data-testid="share-fresh-kind" data-kind={fresh.kind}>
            {fresh.kind === 'new'
              ? t('share.once')
              : fresh.kind === 'rotated'
                ? t('share.rotated')
                : copied
                  ? t('share.addressCopied')
                  : t('share.address')}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <code
              data-testid="share-url"
              className="min-w-0 flex-1 basis-64 truncate rounded-vp bg-surface-2 px-2 py-1.5 font-mono text-vp-base text-ink"
            >
              {fresh.url}
            </code>
            <span className="flex shrink-0 items-center gap-2">
              <button
                type="button"
                data-testid="share-copy"
                onClick={() => copyTextInGesture(fresh.url, setCopied)}
                className="vp-press shrink-0 rounded-vp border border-hairline px-2 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
              >
                {copied ? t('tok.copied') : t('tok.copy')}
              </button>
              <a
                href={fresh.url}
                target="_blank"
                rel="noreferrer noopener"
                title={t('share.open')}
                aria-label={t('share.open')}
                data-testid="share-open"
                className="vp-press shrink-0 rounded-vp border border-hairline p-1.5 text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
              >
                <ExternalLink size={13} />
              </a>
              <button
                type="button"
                data-testid="share-dismiss"
                onClick={() => setFresh(null)}
                className="vp-press shrink-0 rounded-vp px-2.5 py-1.5 text-vp-base"
                style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
              >
                {t('tok.done')}
              </button>
            </span>
          </div>
        </div>
      )}

      {links.length === 0 && !adding ? (
        <p className="text-vp-sm text-ink-3">
          {unpublished ? t('share.needsPublish') : t('share.linksNone')}
        </p>
      ) : (
        <ul className="grid gap-2">
          {links.map((link) => (
            <li
              key={link.id}
              data-testid="share-row"
              className="rounded-vp border border-hairline bg-surface p-2.5"
            >
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
                <Mark icon={Monitor} size="sm" />
                <span className="min-w-0 flex-1 truncate text-vp-md text-ink">{safeText(link.name)}</span>
                {/* How many screens have this open: the one number that decides
                    whether the screen you are about to change is even on. An
                    icon and a count, never a colour alone. */}
                <Chip
                  icon={link.viewers > 0 ? ScanEye : undefined}
                  tone={link.viewers > 0 ? 'accent' : 'plain'}
                  testid="share-row-viewers"
                  data-viewers={link.viewers}
                >
                  {link.viewers > 0 ? t('share.viewers', { n: link.viewers }) : t('share.noViewers')}
                </Chip>
                {/* Three every day, the rest behind one button. This was five
                    identical 13px glyphs in a row, where copying the address --
                    the thing this page exists for -- looked exactly like
                    revoking the link. */}
                {/* Their own line in a narrow card, for the reason the page's
                    card does the same: with four controls on the name's line
                    a phone truncated "走廊电视" to one character. */}
                <span className="flex w-full shrink-0 items-center justify-end gap-1 @md:w-auto">
                  <button
                    type="button"
                    onClick={() => void copyAddress(link)}
                    title={link.copyable ? t('share.copyAddress') : t('share.newAddress')}
                    aria-label={link.copyable ? t('share.copyAddress') : t('share.newAddress')}
                    data-testid="share-copy-address"
                    data-copyable={link.copyable}
                    className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:border-accent hover:text-ink"
                  >
                    {link.copyable ? <Copy size={12} /> : <RefreshCw size={12} />}
                    {/* The label stays at every width. Hiding it on a phone
                        bought nothing -- the actions already wrap onto a line
                        of their own there -- and left a bordered box with a
                        glyph in it beside three borderless ones. */}
                    {link.copyable ? t('share.copyAddress') : t('share.newAddress')}
                  </button>
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
                  <Menu
                    testid="share-more"
                    items={[
                      {
                        // Red line 4: the padlock and the word, never a tint.
                        label: link.locked ? t('share.unlock') : t('share.lock'),
                        icon: link.locked ? LockOpen : Lock,
                        testid: 'share-lock',
                        onSelect: () => void setLock(link, !link.locked),
                      },
                      {
                        label: t('share.revoke'),
                        icon: Trash2,
                        testid: 'share-revoke',
                        destructive: true,
                        onSelect: () => void revoke(link),
                      },
                    ]}
                  />
                </span>
              </div>

              <LinkFacts link={link} />

              {editing === link.id && (
                <LinkEditor
                  link={link}
                  detail={detail}
                  visitorWrites={visitorWrites}
                  onRotate={() => void rotate(link)}
                  onSaved={onChanged}
                  onError={onError}
                  onClose={() => setEditing(null)}
                />
              )}
            </li>
          ))}
        </ul>
      )}

      {adding && (
        <NewLink
          page={page}
          detail={detail}
          projects={projects}
          sessions={sessions}
          onCancel={() => setAdding(false)}
          visitorWrites={visitorWrites}
          onMade={(token) => {
            setAdding(false)
            setFresh({ url: shareURL(token), kind: 'new' })
            setCopied(false)
            onChanged()
          }}
          onError={onError}
        />
      )}
    </div>
  )
}

function LinkFacts({ link }: { link: ShareLink }) {
  const now = useNow()
  const trial = link.pinUntil > now
  const expiring = link.expiresAt !== 0 && link.expiresAt - now < 86400
  return (
    // Under the name and aligned with it, not with the mark: the facts are
    // about the link, and a reader who has found the name reads straight down.
    <div className="mt-1.5 flex flex-wrap items-center gap-1 pl-8">
      <Chip icon={Target} testid="share-row-scope">
        {scopeLabel(link)}
      </Chip>
      <Chip icon={Eye}>{link.detail === 'names' ? t('share.detailNames') : t('share.detailCounts')}</Chip>
      <Chip icon={CalendarClock} tone={expiring ? 'warn' : 'plain'}>
        {link.expiresAt === 0
          ? t('share.noExpiry')
          : t('share.expiresOn', { date: new Date(link.expiresAt * 1000).toLocaleDateString() })}
      </Chip>
      {trial ? (
        <Chip icon={Pin} tone="warn">
          {t('page.trialShort', { v: link.pinVersion })}
        </Chip>
      ) : (
        link.pinVersion > 0 && <Chip icon={Pin}>{t('page.pinned', { v: link.pinVersion })}</Chip>
      )}
      {link.interactive && (
        <Chip icon={Hand} tone="accent" testid="share-row-interactive">
          {t('share.interactiveShort')}
        </Chip>
      )}
      {link.actionsToday > 0 && (
        <Chip testid="share-row-actions">{t('share.actionsToday', { n: link.actionsToday })}</Chip>
      )}
      {link.remark !== '' && (
        <span className="min-w-0 truncate text-vp-sm text-ink-3" data-testid="share-row-remark">
          {safeText(link.remark)}
        </span>
      )}
      <span className="flex-1" />
      {/* The prefix last and quietest: it identifies a link in the audit log,
          which is the only place anybody needs it. */}
      <code className="shrink-0 font-mono text-vp-xs text-ink-3">{link.prefix}…</code>
    </div>
  )
}

function NewLink({
  page,
  detail,
  projects,
  sessions,
  visitorWrites,
  onCancel,
  onMade,
  onError,
}: {
  page: SharePageRow
  detail: SharePageDetail | null
  projects: Project[]
  sessions: Session[]
  visitorWrites: boolean
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
  const [interactive, setInteractive] = useState(false)
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
        interactive: interactive && canInteract(detail),
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
    <div
      data-testid="share-form"
      className="vp-panel-in mt-2 flex flex-col gap-3 rounded-vp border border-accent/40 bg-surface p-3"
    >
      <div className="flex items-center gap-2">
        <Plus size={13} className="shrink-0 text-ink-3" />
        <h4 className="text-vp-md font-medium text-ink">{t('share.create')}</h4>
      </div>
      <Group title={t('share.groupScreen')}>
        <div className="grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2">
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
        </div>
      </Group>
      <Group title={t('share.groupAccess')}>
        <div className="grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-3">
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
        <p className="mt-2 text-vp-sm leading-relaxed text-ink-3">{t('share.detailWhy')}</p>
      </Group>
      <Group title={t('share.groupDo')}>
        <InteractiveChoice
          id={`share-interactive-${id}`}
          detail={detail}
          visitorWrites={visitorWrites}
          checked={interactive}
          onChange={setInteractive}
        />
      </Group>
      {manifest && (manifest.params ?? []).length > 0 && (
        <Group title={t('share.groupParams')}>
          <ParamsForm specs={manifest.params ?? []} values={params} idPrefix={`share-param-${id}`} onChange={setParams} />
        </Group>
      )}
      <div className="flex flex-wrap items-center justify-end gap-2 border-t border-hairline pt-3">
        <button
          type="button"
          onClick={onCancel}
          className="vp-press rounded-vp px-2 py-1 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
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
  visitorWrites,
  onRotate,
  onSaved,
  onError,
  onClose,
}: {
  link: ShareLink
  detail: SharePageDetail | null
  visitorWrites: boolean
  onRotate: () => void
  onSaved: () => void
  onError: (message: string) => void
  onClose: () => void
}) {
  const [name, setName] = useState(link.name)
  const [remark, setRemark] = useState(link.remark)
  const [params, setParams] = useState<Record<string, ShareParamValue>>(link.params)
  const [interactive, setInteractive] = useState(link.interactive)
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

  const saveInteractive = (next: boolean) => {
    setInteractive(next)
    api
      .updateShare(link.id, { name, remark, locked: false, interactive: next })
      .then(onSaved, (e: unknown) => {
        setInteractive(!next)
        onError(e instanceof Error ? e.message : String(e))
      })
  }

  const saveDrawing = (pinVersion: number, next: Record<string, ShareParamValue>) =>
    api.setSharePage(link.id, { pageId: link.pageId, pinVersion, params: next }).then(onSaved, (e: unknown) =>
      onError(e instanceof Error ? e.message : String(e)),
    )

  return (
    // Inset under the row it belongs to, with the accent edge on its left, so
    // a panel this tall is never read as a sibling of the row below it.
    <div
      data-testid="share-edit-panel"
      className="vp-panel-in mt-2.5 ml-8 flex flex-col gap-3 rounded-vp border border-hairline border-l-2 border-l-accent bg-surface-2 p-3"
    >
      <Group title={t('share.groupScreen')}>
        <div className="grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-3">
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
      </Group>
      <Group title={t('share.groupDo')}>
        <InteractiveChoice
          id={`share-edit-interactive-${link.id}`}
          detail={detail}
          visitorWrites={visitorWrites}
          checked={interactive}
          onChange={saveInteractive}
        />
      </Group>
      {manifest && (manifest.params ?? []).length > 0 && (
        <Group title={t('share.groupParams')}>
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
        </Group>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-hairline pt-3">
        <button
          type="button"
          onClick={onRotate}
          data-testid="share-rotate"
          title={t('share.rotateBody')}
          className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 hover:text-ink"
        >
          <RefreshCw size={12} />
          {t('share.newAddress')}
        </button>
        <button
          type="button"
          onClick={onClose}
          data-testid="share-edit-close"
          className="vp-press rounded-vp border border-hairline px-2.5 py-1 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:border-accent hover:text-ink"
        >
          {t('share.editDone')}
        </button>
      </div>
    </div>
  )
}

/** Whether the published version declares an action a visitor may run. */
function canInteract(detail: SharePageDetail | null): boolean {
  return detail?.capabilities?.visitorActions === true
}

/**
 * Whether visitors may run the page's actions through this link.
 *
 * Disabled, with the reason beside it, when the page declares no visitor
 * action: a switch that does nothing is one people turn on and trust.
 */
function InteractiveChoice({
  id,
  detail,
  visitorWrites,
  checked,
  onChange,
}: {
  id: string
  detail: SharePageDetail | null
  visitorWrites: boolean
  checked: boolean
  onChange: (next: boolean) => void
}) {
  const possible = canInteract(detail)
  return (
    <div className="text-vp-sm">
      <label htmlFor={id} className="flex items-center gap-2 text-ink-2">
        <input
          id={id}
          type="checkbox"
          data-testid="share-interactive"
          disabled={!possible}
          checked={checked && possible}
          onChange={(e) => onChange(e.target.checked)}
        />
        {t('share.interactive')}
      </label>
      <p className="mt-1 text-vp-xs text-ink-3" data-testid="share-interactive-note">
        {!possible ? t('share.interactiveNone') : t('share.interactiveWhy')}
      </p>
      {possible && !visitorWrites && (
        <p className="mt-0.5 text-vp-xs" style={{ color: 'var(--vp-state-waiting)' }} data-testid="share-interactive-off">
          {t('share.visitorWritesOffNote')}
        </p>
      )}
    </div>
  )
}
