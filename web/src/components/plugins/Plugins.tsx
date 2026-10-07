import { useCallback, useEffect, useRef, useState } from 'react'
import { BookOpen, Download, FileUp, FolderInput, Hammer, Puzzle, ShieldCheck, SlidersHorizontal, Trash2 } from 'lucide-react'

import { t, useLang } from '../../i18n'
import type { Lang } from '../../i18n'
import { api } from '../../protocol/api'
import type { PluginDetail, PluginRow } from '../../protocol/wire'
import { confirmThen } from '../ask'
import { Chip, Empty, INPUT, IconTile } from '../pages/bits'
import { safeText } from '../text'
import { InstallScreen } from './InstallScreen'
import { PluginSettingsForm } from './PluginSettingsForm'
import { rungsOf, stateOf, textIn } from './screen'
import { refreshPlugins } from './slots'

/**
 * Plugins, as one list: a card per plugin, and under each the install screen
 * or the settings the panel draws for it (docs/plugins.md).
 *
 * A plugin arrives as a zip or a directory and is stored; nothing runs until
 * the install screen has been read and the button pressed with the boxes as
 * ticked. The card says which of those has happened, in a chip beside the
 * name, because that is the first thing somebody needs to know.
 */

/** How often the list is re-read while on screen: for a draft an agent is changing. */
const LIST_MS = 5000

const DOCS_URL = 'https://github.com/jiangmuran/vibepanel/blob/main/docs/plugins.md'

function docsURL(lang: Lang): string {
  return lang === 'zh' ? `${DOCS_URL}#1-the-shape` : DOCS_URL
}

export function Plugins() {
  const lang = useLang()
  const [plugins, setPlugins] = useState<PluginRow[]>([])
  const [details, setDetails] = useState<Record<string, PluginDetail>>({})
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [dirOpen, setDirOpen] = useState(false)
  const [dir, setDir] = useState('')
  const [busy, setBusy] = useState(false)
  const [screenFor, setScreenFor] = useState<string | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  const fail = useCallback((e: unknown) => setError(e instanceof Error ? e.message : String(e)), [])

  const refresh = useCallback(async () => {
    try {
      const list = await api.listPlugins()
      setPlugins(list)
      refreshPlugins()
      const read = await Promise.all(list.map((p) => api.plugin(p.id).catch(() => null)))
      setDetails(Object.fromEntries(read.filter((d) => d !== null).map((d) => [d.id, d])))
    } catch (e) {
      fail(e)
    }
  }, [fail])

  useEffect(() => {
    let cancelled = false
    const tick = () => {
      if (!cancelled && !document.hidden) void refresh()
    }
    const first = window.setTimeout(tick, 0)
    const timer = window.setInterval(tick, LIST_MS)
    return () => {
      cancelled = true
      clearTimeout(first)
      clearInterval(timer)
    }
  }, [refresh])

  const added = (d: PluginDetail) => {
    setError('')
    setNotice(t('plg.added', { name: textIn(d.name, lang) }))
    setDetails((all) => ({ ...all, [d.id]: d }))
    setScreenFor(d.id)
    void refresh()
  }

  const addZip = async (file: File) => {
    setBusy(true)
    try {
      const made = await api.addPluginZip(file)
      added(made.plugin)
    } catch (e) {
      setNotice('')
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const addDir = async () => {
    if (!dir.trim()) return
    setBusy(true)
    try {
      const made = await api.addPluginDir(dir.trim())
      setDir('')
      setDirOpen(false)
      added(made.plugin)
    } catch (e) {
      setNotice('')
      fail(e)
    } finally {
      setBusy(false)
    }
  }

  const screenPlugin = screenFor ? details[screenFor] : undefined

  return (
    <div data-testid="plugins" className="@container">
      <header className="mb-4 flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
        <div className="min-w-0 max-w-2xl">
          <h1 className="text-vp-xl font-semibold tracking-tight text-ink">{t('plg.title')}</h1>
          <p className="mt-1 text-vp-base leading-relaxed text-ink-2">
            {t('plg.why')}{' '}
            <a
              href={docsURL(lang)}
              target="_blank"
              rel="noreferrer noopener"
              data-testid="plugins-docs"
              className="inline-flex items-center gap-1 whitespace-nowrap text-accent hover:underline"
            >
              <BookOpen size={12} />
              {t('plg.docs')}
            </a>
          </p>
        </div>
        <span className="flex shrink-0 flex-wrap items-center gap-2">
          <input
            ref={fileInput}
            type="file"
            accept=".zip,application/zip"
            hidden
            data-testid="plugin-zip-file"
            onChange={(e) => {
              const file = e.target.files?.[0]
              e.target.value = ''
              if (file) void addZip(file)
            }}
          />
          <button
            type="button"
            data-testid="plugin-add-dir"
            disabled={busy}
            onClick={() => setDirOpen((v) => !v)}
            className="vp-outline text-vp-base"
          >
            <FolderInput size={13} />
            {t('plg.fromDir')}
          </button>
          <button
            type="button"
            data-testid="plugin-add-zip"
            disabled={busy}
            onClick={() => fileInput.current?.click()}
            title={t('plg.installZipWhy')}
            className="vp-press flex items-center gap-1 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-40"
            style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
          >
            <FileUp size={13} />
            {t('plg.installZip')}
          </button>
        </span>
      </header>

      {dirOpen && (
        <div className="mb-4 flex flex-wrap items-center gap-2 rounded-vp border border-hairline bg-surface px-3 py-2">
          <input
            value={dir}
            onChange={(e) => setDir(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void addDir()
            }}
            placeholder={t('plg.dirPath')}
            data-testid="plugin-dir"
            className={`${INPUT} flex-1`}
          />
          <button type="button" disabled={busy || !dir.trim()} onClick={() => void addDir()} data-testid="plugin-dir-read" className="vp-outline text-vp-base disabled:opacity-40">
            {t('plg.read')}
          </button>
        </div>
      )}

      {notice && (
        <p className="mb-2 text-vp-base text-ink-2" data-testid="plugins-notice">
          {safeText(notice)}
        </p>
      )}
      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-crashed)' }} data-testid="plugins-error">
          {safeText(error)}
        </p>
      )}

      {plugins.length === 0 ? (
        <Empty icon={Puzzle} testid="plugins-empty">
          {t('plg.none')}
        </Empty>
      ) : (
        <div className="grid gap-3">
          {plugins.map((p) => (
            <PluginCard
              key={p.id}
              row={p}
              detail={details[p.id] ?? null}
              onScreen={() => setScreenFor(p.id)}
              onChanged={() => {
                setError('')
                void refresh()
              }}
              onError={fail}
            />
          ))}
        </div>
      )}

      {screenPlugin && (
        <InstallScreen
          plugin={screenPlugin}
          onClose={() => setScreenFor(null)}
          onConfirm={async (caps) => {
            const out = await api.installPlugin(screenPlugin.id, caps)
            setScreenFor(null)
            setError('')
            setNotice(
              out.needsSecrets.length > 0
                ? t('plg.needsSecrets', { names: out.needsSecrets.join(', ') })
                : t('plg.installedNotice', { name: textIn(out.plugin.name, lang) }),
            )
            await refresh()
          }}
        />
      )}
    </div>
  )
}

function PluginCard({
  row,
  detail,
  onScreen,
  onChanged,
  onError,
}: {
  row: PluginRow
  detail: PluginDetail | null
  onScreen: () => void
  onChanged: () => void
  onError: (e: unknown) => void
}) {
  const lang = useLang()
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)
  const state = stateOf(row)
  const name = safeText(textIn(row.name, lang))
  const hasSettings = (detail?.settings.fields.length ?? 0) > 0 || row.secrets.length > 0

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

  const remove = () =>
    confirmThen(
      {
        title: t('plg.removeTitle', { name }),
        body: t('plg.removeBody'),
        confirm: t('plg.remove'),
        cancel: t('ask.cancel'),
        destructive: true,
      },
      () => act(() => api.deletePlugin(row.id)),
    )

  const stateKey = `plg.state.${state.key}` as const

  return (
    <article
      data-testid="plugin-row"
      data-plugin={row.id}
      data-state={state.key}
      className="@container rounded-vp-lg border border-hairline bg-surface text-vp-base shadow-sm"
    >
      <header className="flex flex-wrap items-start gap-x-3 gap-y-2 p-3 @md:p-4">
        <IconTile icon={Puzzle} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="min-w-0 truncate text-vp-md font-medium text-ink">{name}</h2>
            <code className="font-mono text-vp-xs text-ink-3">{safeText(row.version)}</code>
            <Chip tone={state.tone} testid="plugin-state">
              {t(stateKey, { v: state.v })}
            </Chip>
            {row.dev && (
              <Chip tone="warn" testid="plugin-dev">
                {t('plg.devOn')}
              </Chip>
            )}
            {rungsOf(row).map((r) => (
              <Chip key={r} tone={r === 'unsandboxed' ? 'warn' : 'plain'} testid={`plugin-rung-${r}`}>
                {t(`plg.rung.${r}`)}
              </Chip>
            ))}
          </div>
          {row.description && (
            <p className="mt-0.5 text-vp-sm text-ink-2">{safeText(textIn(row.description, lang))}</p>
          )}
          <p className="mt-0.5 flex flex-wrap items-center gap-x-3 text-vp-xs text-ink-3">
            <code className="font-mono">{safeText(row.id)}</code>
            {row.wanted.length > 0 && (
              <span data-testid="plugin-granted">{t('plg.granted', { n: row.granted.length, m: row.wanted.length })}</span>
            )}
            {row.sourceDir && (
              <span className="min-w-0 truncate" title={t('plg.sourceDir')}>
                {safeText(row.sourceDir)}
              </span>
            )}
          </p>
          {row.problems.map((pr, i) => (
            <p key={i} className="mt-0.5 text-vp-xs" style={{ color: 'var(--vp-state-waiting)' }} data-testid="plugin-problem">
              {safeText(textIn(pr, lang))}
            </p>
          ))}
        </div>
        <div className="flex w-full shrink-0 flex-wrap items-center justify-end gap-1.5 @xl:w-auto">
          {state.key === 'new' || state.key === 'upgrade' ? (
            <button
              type="button"
              data-testid="plugin-install"
              disabled={busy || !detail}
              onClick={onScreen}
              className="vp-outline text-vp-sm"
              style={{ borderColor: 'color-mix(in srgb, var(--vp-accent) 50%, transparent)', color: 'var(--vp-accent)' }}
            >
              <ShieldCheck size={12} />
              {state.key === 'new' ? t('plg.install') : t('plg.upgrade', { v: state.v })}
            </button>
          ) : (
            <button type="button" data-testid="plugin-review" disabled={busy || !detail} onClick={onScreen} className="vp-outline text-vp-sm">
              <ShieldCheck size={12} />
              {t('plg.review')}
            </button>
          )}
          {row.installedVersion > 0 && (
            <button
              type="button"
              data-testid={row.enabled ? 'plugin-disable' : 'plugin-enable'}
              disabled={busy}
              onClick={() => void act(() => (row.enabled ? api.disablePlugin(row.id) : api.enablePlugin(row.id)))}
              className="vp-outline text-vp-sm"
            >
              {row.enabled ? t('plg.disable') : t('plg.enable')}
            </button>
          )}
          {row.sourceDir && (
            <button
              type="button"
              data-testid={row.dev ? 'plugin-dev-off' : 'plugin-dev-on'}
              disabled={busy}
              aria-pressed={row.dev}
              onClick={() => void act(() => api.setPluginDev(row.id, !row.dev))}
              title={t('plg.devWhy')}
              className="vp-outline text-vp-sm"
              style={row.dev ? { borderColor: 'color-mix(in srgb, var(--vp-state-waiting) 60%, transparent)', color: 'var(--vp-state-waiting)' } : undefined}
            >
              <Hammer size={12} />
              {t('plg.dev')}
            </button>
          )}
          {hasSettings && (
            <button type="button" data-testid="plugin-settings" aria-pressed={open} onClick={() => setOpen((v) => !v)} className="vp-outline text-vp-sm">
              <SlidersHorizontal size={12} />
              {t('plg.settings')}
            </button>
          )}
          <a href={api.exportPluginURL(row.id)} download data-testid="plugin-export" className="vp-outline text-vp-sm">
            <Download size={12} />
            {t('plg.export')}
          </a>
          <button type="button" data-testid="plugin-remove" disabled={busy} onClick={() => void remove()} title={t('plg.remove')} className="vp-control">
            <Trash2 size={13} />
          </button>
        </div>
      </header>
      {open && detail && (
        <div className="border-t border-hairline p-3 @md:p-4">
          <PluginSettingsForm plugin={detail} onChanged={onChanged} onError={onError} />
        </div>
      )}
    </article>
  )
}
