import { useCallback, useEffect, useState } from 'react'
import { GitFork, History, PanelsTopLeft, Trash2 } from 'lucide-react'

import { api } from '../../protocol/api'
import type { SharePage, SharePageCatalogue, SharePageDetail, SharePageRow } from '../../protocol/wire'
import { t, useLang, type Key } from '../../i18n'
import { safeText } from '../text'

/**
 * Share pages: HTML the owner (usually an agent) writes, drawn on a share link.
 *
 * This list is where a page is made, where its history is, and where it is
 * deleted. Editing happens elsewhere on purpose: a page is a directory an
 * agent works in, and its Preview sits next to that agent's terminal, not in
 * a dialog over it. Making one here ends by handing over to that -- a project,
 * a session, and a first line typed at its prompt.
 */

const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** How often the list is re-read while it is on screen. A page's draft lives on
 *  disk and an agent is changing it; the counts here should not be a photograph. */
const LIST_MS = 10_000

const TEMPLATE_LABEL: Record<string, Key> = {
  blank: 'page.tpl.blank',
  wall: 'page.tpl.wall',
  spend: 'page.tpl.spend',
  built: 'page.tpl.built',
  glance: 'page.tpl.glance',
}

export function SharePages({
  onStartPage,
  onChanged,
}: {
  /** Hands a new page over to a project and an agent session. */
  onStartPage?: (page: SharePage) => void
  /** Something about the list changed; the links below may want to re-read it. */
  onChanged?: () => void
}) {
  useLang()
  const [pages, setPages] = useState<SharePageRow[]>([])
  const [catalogue, setCatalogue] = useState<SharePageCatalogue | null>(null)
  const [name, setName] = useState('')
  const [template, setTemplate] = useState('blank')
  const [dir, setDir] = useState('')
  const [startAgent, setStartAgent] = useState(true)
  const [open, setOpen] = useState<string | null>(null)
  const [detail, setDetail] = useState<SharePageDetail | null>(null)
  const [confirming, setConfirming] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const fail = (e: unknown) => setError(e instanceof Error ? e.message : String(e))

  const refresh = useCallback(async () => {
    try {
      setPages(await api.listPages())
    } catch (e) {
      fail(e)
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    api.listPages().then(
      (list) => {
        if (!cancelled) setPages(list)
      },
      (e: unknown) => {
        if (!cancelled) fail(e)
      },
    )
    api.pageCatalogue().then(
      (c) => {
        if (!cancelled) setCatalogue(c)
      },
      () => {},
    )
    const timer = window.setInterval(() => void refresh(), LIST_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [refresh])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    api.page(open).then(
      (d) => {
        if (!cancelled) setDetail(d)
      },
      (e: unknown) => {
        if (!cancelled) fail(e)
      },
    )
    return () => {
      cancelled = true
    }
  }, [open])

  const create = async () => {
    const trimmed = name.trim()
    if (!trimmed) return
    setBusy(true)
    try {
      const page = await api.createPage({ name: trimmed, template, sourceDir: dir.trim() })
      setName('')
      setDir('')
      setError('')
      await refresh()
      onChanged?.()
      if (startAgent && onStartPage) onStartPage(page)
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const fork = async (page: SharePageRow) => {
    setBusy(true)
    try {
      const made = await api.forkPage(page.id, '')
      setError('')
      await refresh()
      if (onStartPage) onStartPage(made)
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const remove = async (page: SharePageRow) => {
    try {
      await api.deletePage(page.id)
      setConfirming(null)
      setError('')
      await refresh()
      onChanged?.()
    } catch (e) {
      setConfirming(null)
      fail(e)
    }
  }

  const rollback = async (page: SharePageRow, version: number) => {
    try {
      await api.rollbackPage(page.id, version)
      setDetail(await api.page(page.id))
      setError('')
      await refresh()
    } catch (e) {
      fail(e)
    }
  }

  const slug = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  const where = catalogue?.pagesRoot ? `${catalogue.pagesRoot}/${slug || 'page'}` : ''

  return (
    <div data-testid="share-pages" className="@container">
      <p className="mb-2 text-vp-base leading-relaxed text-ink-2">{t('page.why')}</p>

      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-crashed)' }}>
          {safeText(error)}
        </p>
      )}

      <div className="mb-3 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2 @3xl:grid-cols-4">
        <div className="min-w-0">
          <label htmlFor="page-name" className="mb-1 block text-vp-sm text-ink-3">
            {t('page.nameLabel')}
          </label>
          <input
            id="page-name"
            data-testid="page-new-name"
            value={name}
            maxLength={64}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void create()
            }}
            placeholder={t('page.namePlaceholder')}
            className={INPUT}
          />
        </div>
        <div className="min-w-0">
          <label htmlFor="page-template" className="mb-1 block text-vp-sm text-ink-3">
            {t('page.template')}
          </label>
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
        </div>
        <div className="min-w-0 @3xl:col-span-2">
          <label htmlFor="page-dir" className="mb-1 block text-vp-sm text-ink-3">
            {t('page.dir')}
          </label>
          <input
            id="page-dir"
            data-testid="page-new-dir"
            value={dir}
            onChange={(e) => setDir(e.target.value)}
            placeholder={template === '' ? t('page.dirExisting') : where}
            className={`${INPUT} font-mono`}
          />
        </div>
        {/* The choice and the action on one line across the whole form, so the
            button is read as applying to all of it rather than to the field
            it happens to sit under. */}
        <div className="flex flex-wrap items-center justify-between gap-2 @md:col-span-2 @3xl:col-span-4">
          <label className="flex items-center gap-2 text-vp-sm text-ink-2">
            <input
              type="checkbox"
              data-testid="page-new-agent"
              checked={startAgent}
              onChange={(e) => setStartAgent(e.target.checked)}
            />
            {t('page.startAgent')}
          </label>
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
        </div>
      </div>

      {pages.length === 0 ? (
        <p className="text-vp-base text-ink-3">{t('page.none')}</p>
      ) : (
        pages.map((page) => (
          <div key={page.id} data-testid="page-row" className="border-t border-hairline py-2 text-vp-base first:border-t-0">
            <div className="flex items-center gap-2">
              <PanelsTopLeft size={13} className="shrink-0 text-ink-2" />
              <span className="min-w-0 flex-1 truncate text-ink">{safeText(page.name)}</span>
              <span className="shrink-0 text-vp-sm text-ink-2">
                {page.publishedVersion > 0
                  ? t('page.published', { v: page.publishedVersion })
                  : t('page.unpublished')}
              </span>
              <button
                type="button"
                onClick={() => setOpen(open === page.id ? null : page.id)}
                aria-pressed={open === page.id}
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
                onClick={() => void fork(page)}
                title={t('page.fork')}
                aria-label={t('page.fork')}
                data-testid="page-fork"
                className="vp-control disabled:opacity-40"
              >
                <GitFork size={13} />
              </button>
              {confirming === page.id ? (
                <span className="flex shrink-0 items-center gap-1">
                  <button
                    type="button"
                    onClick={() => void remove(page)}
                    data-testid="page-delete-confirm"
                    className="vp-press shrink-0 rounded-vp px-2 py-1 text-vp-sm"
                    style={{ background: 'var(--vp-state-crashed)', color: 'var(--vp-accent-ink)' }}
                  >
                    {t('page.deleteSure')}
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
                  onClick={() => setConfirming(page.id)}
                  title={t('page.delete')}
                  aria-label={t('page.delete')}
                  data-testid="page-delete"
                  className="vp-control"
                >
                  <Trash2 size={13} />
                </button>
              )}
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 pl-5 text-vp-sm text-ink-2">
              <code className="min-w-0 truncate font-mono text-ink-3">{safeText(page.sourceDir)}</code>
              {!page.sourceExists && <span>{t('page.dirGone')}</span>}
              <span>{t('page.links', { n: page.links })}</span>
            </div>

            {open === page.id && detail?.page.id === page.id && (
              <ul data-testid="page-versions" className="mt-2 space-y-1 pl-5 text-vp-sm">
                {detail.versions.filter((v) => !v.candidate).length === 0 && (
                  <li className="text-ink-3">{t('page.noVersions')}</li>
                )}
                {detail.versions
                  .filter((v) => !v.candidate)
                  .map((v) => (
                    <li key={v.version} className="flex items-center gap-2">
                      <span className="tabular w-8 shrink-0 text-ink">v{v.version}</span>
                      <span className="shrink-0 text-ink-3">
                        {new Date(v.createdAt * 1000).toLocaleString()}
                      </span>
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
                          onClick={() => void rollback(page, v.version)}
                          className="vp-press shrink-0 rounded-vp px-2 py-0.5 text-ink-2 hover:text-ink"
                        >
                          {t('page.rollback')}
                        </button>
                      )}
                    </li>
                  ))}
              </ul>
            )}
          </div>
        ))
      )}
    </div>
  )
}
