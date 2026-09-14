import { useCallback, useEffect, useState } from 'react'
import { FolderOpen, GitFork, History, PanelsTopLeft, Plus, Trash2, Upload } from 'lucide-react'

import { api } from '../../protocol/api'
import type {
  Project,
  Session,
  ShareLink,
  SharePage,
  SharePageCatalogue,
  SharePageDetail,
  SharePageRow,
} from '../../protocol/wire'
import { t, useLang, type Key } from '../../i18n'
import { safeText } from '../text'
import { Field, PageLinks } from './PageLinks'

/**
 * Sharing, as one list: pages, and under each page the links that show it.
 *
 * A page is a directory an agent writes (under the data directory, as a
 * `page-…` project) and a history of published versions. A link is an address
 * for one screen that shows a page. There is nothing else to share: a link
 * cannot exist without a page, so a link is made on the page it shows.
 */

const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** How often pages and links are re-read while this is on screen: for the
 *  viewer counts, which are true for a few seconds each, and for a draft an
 *  agent is changing. */
const LIST_MS = 5000

const TEMPLATE_LABEL: Record<string, Key> = {
  blank: 'page.tpl.blank',
  wall: 'page.tpl.wall',
  spend: 'page.tpl.spend',
  built: 'page.tpl.built',
  glance: 'page.tpl.glance',
}

/** The directory name the server makes of a page name (httpapi dirName), for
 *  the placeholder: letters and digits of any script, the rest a dash. */
function slugOf(name: string): string {
  return [...name.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, '-').replace(/^-+|-+$/g, '')]
    .slice(0, 40)
    .join('')
    .replace(/-+$/, '')
}

export function Sharing({
  onOpenPage,
}: {
  /** Opens a page's project, with its Preview, starting an agent when
   *  `fresh` or when nothing is running in it. */
  onOpenPage?: (page: SharePage, fresh: boolean) => void
}) {
  useLang()
  const [pages, setPages] = useState<SharePageRow[]>([])
  const [links, setLinks] = useState<ShareLink[]>([])
  const [details, setDetails] = useState<Record<string, SharePageDetail>>({})
  const [catalogue, setCatalogue] = useState<SharePageCatalogue | null>(null)
  const [projects, setProjects] = useState<Project[]>([])
  const [sessions, setSessions] = useState<Session[]>([])
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

  const fail = useCallback((e: unknown) => setError(e instanceof Error ? e.message : String(e)), [])

  const refresh = useCallback(async () => {
    try {
      const [p, l] = await Promise.all([api.listPages(), api.listShares()])
      setPages(p)
      setLinks(l)
      const read = await Promise.all(p.map((row) => api.page(row.id).catch(() => null)))
      setDetails(Object.fromEntries(read.filter((d) => d !== null).map((d) => [d.page.id, d])))
    } catch (e) {
      fail(e)
    }
  }, [fail])

  useEffect(() => {
    let cancelled = false
    const tick = () => {
      if (!cancelled && !document.hidden) void refresh()
    }
    // Through a timer of zero rather than a call in the effect body, which the
    // hooks lint reads as a setState during the effect.
    const first = window.setTimeout(tick, 0)
    const timer = window.setInterval(tick, LIST_MS)
    api.pageCatalogue().then(
      (c) => {
        if (!cancelled) setCatalogue(c)
      },
      () => {},
    )
    // For the scope picker. A form that cannot fill it still works: the whole
    // panel needs no list.
    api.state().then(
      (s) => {
        if (cancelled) return
        setProjects(s.projects)
        setSessions(s.sessions)
      },
      () => {},
    )
    return () => {
      cancelled = true
      clearTimeout(first)
      clearInterval(timer)
    }
  }, [refresh])

  return (
    <div data-testid="sharing" className="@container">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <p className="min-w-0 flex-1 text-vp-base leading-relaxed text-ink-2">{t('page.why')}</p>
        {!creating && (
          <button
            type="button"
            data-testid="page-new"
            onClick={() => setCreating(true)}
            className="vp-press flex shrink-0 items-center gap-1 rounded-vp px-3 py-1.5 text-vp-base"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            <Plus size={13} />
            {t('page.create')}
          </button>
        )}
      </div>

      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-crashed)' }} data-testid="sharing-error">
          {safeText(error)}
        </p>
      )}

      {creating && (
        <NewPage
          catalogue={catalogue}
          onCancel={() => setCreating(false)}
          onMade={(page, startAgent) => {
            setCreating(false)
            setError('')
            void refresh()
            if (startAgent) onOpenPage?.(page, true)
          }}
          onError={fail}
        />
      )}

      {pages.length === 0 && !creating ? (
        <p className="text-vp-base text-ink-3">{t('page.none')}</p>
      ) : (
        pages.map((page) => (
          <PageCard
            key={page.id}
            page={page}
            detail={details[page.id] ?? null}
            links={links.filter((l) => l.pageId === page.id)}
            projects={projects}
            sessions={sessions}
            onOpen={() => onOpenPage?.(page, false)}
            onChanged={() => {
              setError('')
              void refresh()
            }}
            onError={fail}
          />
        ))
      )}
    </div>
  )
}

function NewPage({
  catalogue,
  onCancel,
  onMade,
  onError,
}: {
  catalogue: SharePageCatalogue | null
  onCancel: () => void
  onMade: (page: SharePage, startAgent: boolean) => void
  onError: (e: unknown) => void
}) {
  const [name, setName] = useState('')
  const [template, setTemplate] = useState('blank')
  const [dir, setDir] = useState('')
  const [startAgent, setStartAgent] = useState(true)
  const [busy, setBusy] = useState(false)
  const where = catalogue?.pagesRoot ? `${catalogue.pagesRoot}/page-${slugOf(name) || 'page'}` : ''

  const create = async () => {
    const trimmed = name.trim()
    if (!trimmed) return
    setBusy(true)
    try {
      const page = await api.createPage({ name: trimmed, template, sourceDir: dir.trim() })
      onMade(page, startAgent)
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div data-testid="page-form" className="mb-3 rounded-vp border border-hairline bg-surface-2 p-3">
      <div className="mb-2 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2">
        <Field label={t('page.nameLabel')} htmlFor="page-name">
          <input
            id="page-name"
            data-testid="page-new-name"
            value={name}
            maxLength={64}
            autoFocus
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void create()
            }}
            placeholder={t('page.namePlaceholder')}
            className={INPUT}
          />
        </Field>
        <Field label={t('page.template')} htmlFor="page-template">
          <select
            id="page-template"
            data-testid="page-new-template"
            value={template}
            onChange={(e) => setTemplate(e.target.value)}
            className={INPUT}
          >
            {(catalogue?.templates ?? [{ id: 'blank', sections: [] }]).map((tpl) => (
              <option key={tpl.id} value={tpl.id}>
                {TEMPLATE_LABEL[tpl.id] ? t(TEMPLATE_LABEL[tpl.id]) : tpl.id}
              </option>
            ))}
            <option value="">{t('page.adopt')}</option>
          </select>
        </Field>
        <div className="@md:col-span-2">
          <Field label={t('page.dir')} htmlFor="page-dir">
            <input
              id="page-dir"
              data-testid="page-new-dir"
              value={dir}
              onChange={(e) => setDir(e.target.value)}
              placeholder={template === '' ? t('page.dirExisting') : where}
              className={`${INPUT} font-mono`}
            />
          </Field>
        </div>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <label className="flex items-center gap-2 text-vp-sm text-ink-2">
          <input
            type="checkbox"
            data-testid="page-new-agent"
            checked={startAgent}
            onChange={(e) => setStartAgent(e.target.checked)}
          />
          {t('page.startAgent')}
        </label>
        <span className="flex items-center gap-2">
          <button type="button" onClick={onCancel} className="vp-press rounded-vp px-2 py-1 text-vp-base text-ink-2">
            {t('share.cancel')}
          </button>
          <button
            type="button"
            data-testid="page-create"
            disabled={busy || name.trim() === '' || (template === '' && dir.trim() === '')}
            onClick={() => void create()}
            className="vp-press rounded-vp px-3 py-1.5 text-vp-base disabled:opacity-40"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            {t('page.create')}
          </button>
        </span>
      </div>
    </div>
  )
}

function PageCard({
  page,
  detail,
  links,
  projects,
  sessions,
  onOpen,
  onChanged,
  onError,
}: {
  page: SharePageRow
  detail: SharePageDetail | null
  links: ShareLink[]
  projects: Project[]
  sessions: Session[]
  onOpen: () => void
  onChanged: () => void
  onError: (e: unknown) => void
}) {
  const [versions, setVersions] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await fn()
      onChanged()
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div data-testid="page-row" data-page={page.id} className="border-t border-hairline py-2.5 text-vp-base first:border-t-0">
      <div className="flex flex-wrap items-center gap-2">
        <PanelsTopLeft size={14} className="shrink-0 text-ink-2" />
        <span className="min-w-0 flex-1 truncate font-medium text-ink">{safeText(page.name)}</span>
        <span className="shrink-0 text-vp-sm text-ink-2" data-testid="page-status">
          {page.publishedVersion > 0 ? t('page.published', { v: page.publishedVersion }) : t('page.unpublished')}
        </span>
        {/* Its own line in a narrow container, so the page's name is not the
            thing that gives way to five buttons. */}
        <span className="flex w-full shrink-0 items-center justify-end gap-1 @xl:w-auto">
          <button
            type="button"
            data-testid="page-open"
            disabled={busy}
            onClick={onOpen}
            title={page.sourceExists ? t('page.openWhy') : t('page.restoreWhy')}
            className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink hover:border-accent disabled:opacity-40"
          >
            <FolderOpen size={12} />
            {page.sourceExists ? t('page.open') : t('page.restore')}
          </button>
          <button
            type="button"
            data-testid="page-publish"
            disabled={busy || !page.sourceExists}
            onClick={() => void act(() => api.publishPage(page.id, ''))}
            title={t('page.publishWhy')}
            className="vp-press flex items-center gap-1 rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink hover:border-accent disabled:opacity-40"
          >
            <Upload size={12} />
            {t('page.publish')}
          </button>
          <button
            type="button"
            onClick={() => setVersions(!versions)}
            aria-pressed={versions}
            title={t('page.history')}
            aria-label={t('page.history')}
            data-testid="page-history"
            className="vp-control"
          >
            <History size={13} />
          </button>
          <button
            type="button"
            disabled={busy || !page.sourceExists}
            onClick={() => void act(() => api.forkPage(page.id, ''))}
            title={t('page.fork')}
            aria-label={t('page.fork')}
            data-testid="page-fork"
            className="vp-control disabled:opacity-40"
          >
            <GitFork size={13} />
          </button>
          {confirming ? (
            <>
              <button
                type="button"
                onClick={() => {
                  setConfirming(false)
                  void act(() => api.deletePage(page.id))
                }}
                data-testid="page-delete-confirm"
                className="vp-press rounded-vp px-2 py-1 text-vp-sm"
                style={{ background: 'var(--vp-state-crashed)', color: 'var(--vp-accent-ink)' }}
              >
                {t('page.deleteSure')}
              </button>
              <button
                type="button"
                onClick={() => setConfirming(false)}
                className="vp-press rounded-vp px-2 py-1 text-vp-sm text-ink-2"
              >
                {t('share.keep')}
              </button>
            </>
          ) : (
            <button
              type="button"
              onClick={() => setConfirming(true)}
              title={t('page.delete')}
              aria-label={t('page.delete')}
              data-testid="page-delete"
              className="vp-control"
            >
              <Trash2 size={13} />
            </button>
          )}
        </span>
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 pl-6 text-vp-sm text-ink-2">
        <code className="min-w-0 truncate font-mono text-ink-3" data-testid="page-dir">
          {safeText(page.sourceDir)}
        </code>
        {!page.sourceExists && (
          <span data-testid="page-dir-gone">
            {page.publishedVersion > 0
              ? t('page.dirGoneRestore', { v: page.publishedVersion })
              : t('page.dirGone')}
          </span>
        )}
      </div>

      {versions && detail && (
        <ul data-testid="page-versions" className="mt-2 space-y-1 pl-6 text-vp-sm">
          {detail.versions.filter((v) => !v.candidate).length === 0 && (
            <li className="text-ink-3">{t('page.noVersions')}</li>
          )}
          {detail.versions
            .filter((v) => !v.candidate)
            .map((v) => (
              <li key={v.version} className="flex items-center gap-2">
                <span className="tabular w-8 shrink-0 text-ink">v{v.version}</span>
                <span className="shrink-0 text-ink-3">{new Date(v.createdAt * 1000).toLocaleString()}</span>
                {v.commitSha && (
                  <code className="shrink-0 font-mono text-ink-3">
                    {v.commitSha.slice(0, 8)}
                    {v.dirty ? '*' : ''}
                  </code>
                )}
                <span className="min-w-0 flex-1 truncate text-ink-2">{safeText(v.note)}</span>
                {v.version === detail.page.publishedVersion ? (
                  <span className="shrink-0 text-ink-2">{t('page.current')}</span>
                ) : (
                  <button
                    type="button"
                    data-testid="page-rollback"
                    onClick={() => void act(() => api.rollbackPage(page.id, v.version))}
                    className="vp-press shrink-0 rounded-vp px-2 py-0.5 text-ink-2 hover:text-ink"
                  >
                    {t('page.rollback')}
                  </button>
                )}
              </li>
            ))}
        </ul>
      )}

      <PageLinks
        page={page}
        detail={detail}
        links={links}
        projects={projects}
        sessions={sessions}
        onChanged={onChanged}
        onError={(m) => onError(new Error(m))}
      />
    </div>
  )
}
