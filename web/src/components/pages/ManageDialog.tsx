import { useCallback, useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { X } from 'lucide-react'

import { api } from '../../protocol/api'
import type {
  SharePageCapabilities,
  SharePageRow,
  SharePageSecret,
  SharePageServerLogLine,
  SharePageSource,
} from '../../protocol/wire'
import { t, useLang, type Key } from '../../i18n'
import { safeText } from '../text'
import { DataForm } from './DataForm'

/**
 * Managing one page: its admin page, its data, its sources and secrets, and
 * what its server.js said.
 *
 * One dialog with tabs rather than four buttons on the card, because they are
 * one subject -- what the page holds and where it comes from -- and the card is
 * already a row of buttons. docs/page-backend.md §2–§5.
 *
 * The admin page is an iframe of the panel's own address for it, which is
 * behind the login and redirects to a grant; the sandbox is on the response
 * that answers there, not on this element, because the element cannot widen
 * it and the response is what has to be right when the page is opened in a tab.
 */

type Tab = 'admin' | 'data' | 'sources' | 'log'

const TAB_LABEL: Record<Tab, Key> = {
  admin: 'manage.tabAdmin',
  data: 'manage.tabData',
  sources: 'manage.tabSources',
  log: 'manage.tabLog',
}

const LOG_MS = 3000

const LEVEL_LABEL: Record<string, Key> = {
  info: 'serverlog.info',
  warn: 'serverlog.warn',
  error: 'serverlog.error',
}

const INPUT =
  'min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1 text-vp-sm text-ink outline-none focus:border-accent'

export function ManageDialog({
  page,
  capabilities,
  onClose,
}: {
  page: SharePageRow
  capabilities: SharePageCapabilities
  onClose: () => void
}) {
  useLang()
  const tabs = (['admin', 'data', 'sources', 'log'] as const).filter((tab) =>
    tab === 'admin'
      ? capabilities.admin
      : tab === 'data'
        ? capabilities.data
        : tab === 'sources'
          ? capabilities.sources
          : capabilities.server,
  )
  const [tab, setTab] = useState<Tab>(tabs[0] ?? 'data')

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      // A confirmation asked from inside this dialog answers Escape itself.
      if (document.querySelector('[data-vp-modal="confirm"]')) return
      e.stopPropagation()
      onClose()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [onClose])

  return createPortal(
    <div
      className="vp-backdrop fixed inset-0 z-40 flex items-stretch justify-center bg-black/40 p-4"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        data-vp-modal="page-manage"
        data-testid="page-manage"
        role="dialog"
        aria-modal="true"
        aria-label={t('manage.title', { name: safeText(page.name) })}
        className="vp-panel-in flex w-full max-w-5xl flex-col overflow-hidden rounded-vp border border-hairline bg-surface shadow-xl"
      >
        <header className="flex flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
          <span className="min-w-0 truncate text-vp-md font-medium text-ink">
            {t('manage.title', { name: safeText(page.name) })}
          </span>
          <div className="vp-segmented" role="tablist">
            {tabs.map((id) => (
              <button
                key={id}
                type="button"
                role="tab"
                data-testid={`page-manage-tab-${id}`}
                aria-selected={tab === id}
                data-active={tab === id}
                onClick={() => setTab(id)}
                className="vp-tab px-3 text-vp-sm whitespace-nowrap"
              >
                {t(TAB_LABEL[id])}
              </button>
            ))}
          </div>
          <span className="flex-1" />
          <button
            type="button"
            data-testid="page-manage-close"
            onClick={onClose}
            title={t('manage.close')}
            aria-label={t('manage.close')}
            className="vp-control"
          >
            <X size={14} />
          </button>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto p-3">
          {tab === 'admin' && <AdminFrame pageId={page.id} draft={false} />}
          {tab === 'data' && <DataForm pageId={page.id} ns="live" />}
          {tab === 'sources' && <SourcesPanel pageId={page.id} />}
          {tab === 'log' && <ServerLog pageId={page.id} />}
        </div>
      </div>
    </div>,
    document.body,
  )
}

/** The page's own admin page, behind the panel login. */
export function AdminFrame({ pageId, draft }: { pageId: string; draft: boolean }) {
  useLang()
  return (
    <iframe
      data-testid="page-admin-frame"
      title={t('manage.adminFrame')}
      src={api.pageAdminURL(pageId, draft)}
      referrerPolicy="no-referrer"
      className="h-full min-h-[28rem] w-full rounded-vp border border-hairline bg-surface-2"
    />
  )
}

function SourcesPanel({ pageId }: { pageId: string }) {
  const [sources, setSources] = useState<SharePageSource[] | null>(null)
  const [hosts, setHosts] = useState<string[]>([])
  const [secrets, setSecrets] = useState<SharePageSecret[]>([])
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      const [s, h, sec] = await Promise.all([api.pageSources(pageId), api.pageHosts(pageId), api.pageSecrets(pageId)])
      setSources(s)
      setHosts(h.hosts)
      setSecrets(sec)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [pageId])

  useEffect(() => {
    const first = window.setTimeout(() => void load(), 0)
    return () => clearTimeout(first)
  }, [load])

  const guard = async (fn: () => Promise<unknown>) => {
    try {
      await fn()
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
    await load()
  }

  const toggleHost = (host: string, on: boolean) => {
    const next = on ? [...new Set([...hosts, host])] : hosts.filter((h) => h !== host)
    void guard(() => api.setPageHosts(pageId, next))
  }

  // Every secret a source names, and every secret stored, whether or not a
  // source still names it: a stored value nothing uses is one to delete.
  const names = [
    ...new Set([...(sources ?? []).flatMap((s) => s.secrets.map((x) => x.name)), ...secrets.map((s) => s.name)]),
  ].sort()

  return (
    <div data-testid="page-sources" className="@container flex flex-col gap-4 text-vp-sm">
      {error && (
        <p style={{ color: 'var(--vp-state-crashed)' }} data-testid="page-sources-error">
          {safeText(error)}
        </p>
      )}
      <section>
        <h4 className="mb-2 font-semibold text-ink-2">{t('sources.title')}</h4>
        {sources === null ? (
          <p className="text-ink-3">{t('data.loading')}</p>
        ) : sources.length === 0 ? (
          <p className="text-ink-3">{t('sources.none')}</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {sources.map((s) => {
              const approved = hosts.includes(s.host) || s.approved
              return (
                <li
                  key={s.key}
                  data-testid="page-source"
                  data-key={s.key}
                  className="rounded-vp border border-hairline bg-surface-2 p-2"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="font-mono text-ink">{safeText(s.key)}</code>
                    <span className="text-ink-3">{t('sources.every', { every: safeText(s.every) })}</span>
                    <span className="flex-1" />
                    <label className="flex items-center gap-1.5 text-ink-2">
                      <input
                        type="checkbox"
                        data-testid="page-source-approve"
                        checked={approved}
                        onChange={(e) => toggleHost(s.host, e.target.checked)}
                      />
                      {t('sources.approve', { host: safeText(s.host) })}
                    </label>
                  </div>
                  <code className="mt-1 block break-all font-mono text-vp-xs text-ink-3">{safeText(s.url)}</code>
                  <p className="mt-1 text-vp-xs" data-testid="page-source-status" data-ok={s.ok}>
                    {!approved ? (
                      <span style={{ color: 'var(--vp-state-waiting)' }}>{t('sources.notApproved')}</span>
                    ) : s.fetchedAt === 0 ? (
                      <span className="text-ink-3">{t('sources.never')}</span>
                    ) : s.ok ? (
                      <span className="text-ink-2">
                        {t('sources.ok', { status: s.status, at: new Date(s.fetchedAt * 1000).toLocaleString() })}
                      </span>
                    ) : (
                      <span style={{ color: 'var(--vp-state-crashed)' }}>
                        {t('sources.failed', { why: safeText(s.error) })}
                      </span>
                    )}
                  </p>
                </li>
              )
            })}
          </ul>
        )}
      </section>
      <section>
        <h4 className="mb-1 font-semibold text-ink-2">{t('secrets.title')}</h4>
        <p className="mb-2 text-vp-xs text-ink-3">{t('secrets.why')}</p>
        {names.length === 0 ? (
          <p className="text-ink-3">{t('secrets.none')}</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {names.map((name) => (
              <SecretRow
                key={name}
                name={name}
                stored={secrets.find((s) => s.name === name) ?? null}
                onSave={(value) => guard(() => api.setPageSecret(pageId, name, value))}
                onDelete={() => guard(() => api.deletePageSecret(pageId, name))}
              />
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

function SecretRow({
  name,
  stored,
  onSave,
  onDelete,
}: {
  name: string
  stored: SharePageSecret | null
  onSave: (value: string) => Promise<void>
  onDelete: () => Promise<void>
}) {
  const [value, setValue] = useState('')
  return (
    <li data-testid="page-secret" data-name={name} className="flex flex-wrap items-center gap-2">
      <code className="min-w-0 font-mono text-ink">{safeText(name)}</code>
      <span className="text-vp-xs text-ink-3" data-testid="page-secret-state" data-set={stored !== null}>
        {stored ? t('secrets.setAt', { at: new Date(stored.setAt * 1000).toLocaleString() }) : t('secrets.unset')}
      </span>
      <span className="flex-1" />
      <input
        type="password"
        autoComplete="off"
        data-testid="page-secret-input"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={stored ? t('secrets.replace') : t('secrets.value')}
        aria-label={t('secrets.valueFor', { name: safeText(name) })}
        className={`${INPUT} w-56`}
      />
      <button
        type="button"
        data-testid="page-secret-save"
        disabled={value === ''}
        onClick={() => {
          const v = value
          setValue('')
          void onSave(v)
        }}
        className="vp-press rounded-vp px-2 py-1 text-vp-sm disabled:opacity-40"
        style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
      >
        {t('secrets.save')}
      </button>
      {stored && (
        <button
          type="button"
          data-testid="page-secret-delete"
          onClick={() => void onDelete()}
          className="vp-press rounded-vp border border-hairline px-2 py-1 text-vp-sm text-ink-2 hover:text-ink"
        >
          {t('secrets.delete')}
        </button>
      )}
    </li>
  )
}

function ServerLog({ pageId }: { pageId: string }) {
  const [lines, setLines] = useState<SharePageServerLogLine[] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let cancelled = false
    const read = () => {
      api.pageServerLog(pageId).then(
        (r) => {
          if (cancelled) return
          setLines(r.lines)
          setError('')
        },
        (e: unknown) => {
          if (!cancelled) setError(e instanceof Error ? e.message : String(e))
        },
      )
    }
    const first = window.setTimeout(read, 0)
    const timer = window.setInterval(read, LOG_MS)
    return () => {
      cancelled = true
      clearTimeout(first)
      clearInterval(timer)
    }
  }, [pageId])

  return (
    <div data-testid="page-server-log" className="text-vp-sm">
      {error && <p style={{ color: 'var(--vp-state-crashed)' }}>{safeText(error)}</p>}
      {lines === null ? (
        <p className="text-ink-3">{t('data.loading')}</p>
      ) : lines.length === 0 ? (
        <p className="text-ink-3">{t('serverlog.empty')}</p>
      ) : (
        <ul className="space-y-0.5 font-mono text-vp-xs">
          {lines
            .slice()
            .reverse()
            .map((l, i) => (
              <li key={i} className="flex gap-2" data-level={l.level}>
                <span className="tabular shrink-0 text-ink-3">{new Date(l.at * 1000).toLocaleTimeString()}</span>
                <span
                  className="w-10 shrink-0"
                  style={{
                    color:
                      l.level === 'error'
                        ? 'var(--vp-state-crashed)'
                        : l.level === 'warn'
                          ? 'var(--vp-state-waiting)'
                          : undefined,
                  }}
                >
                  {t(LEVEL_LABEL[l.level] ?? 'serverlog.info')}
                </span>
                <span className="min-w-0 break-words text-ink">{safeText(l.text)}</span>
              </li>
            ))}
        </ul>
      )}
    </div>
  )
}
