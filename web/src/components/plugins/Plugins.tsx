import { useCallback, useEffect, useRef, useState } from 'react'
import { BookOpen, Cpu, DoorOpen, Download, FileUp, FolderInput, HardDriveDownload, Hammer, KeyRound, Plus, Puzzle, ScrollText, ShieldCheck, SlidersHorizontal, Trash2 } from 'lucide-react'

import { t, tKey, useLang } from '../../i18n'
import type { Lang } from '../../i18n'
import { api } from '../../protocol/api'
import type { NewPluginResult, PluginAccessToken, PluginDetail, PluginDownloadRow, PluginProcessStatus, PluginRow, PluginSourceRow, PluginTemplate, SharePageServerLogLine } from '../../protocol/wire'
import { copyTextInGesture } from '../../clipboard'
import { confirmThen } from '../ask'
import { Chip, Empty, Field, INPUT, IconTile } from '../pages/bits'
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

export function Plugins({ onOpenProject }: { onOpenProject?: (projectId: string) => void } = {}) {
  const lang = useLang()
  const [newOpen, setNewOpen] = useState(false)
  const [plugins, setPlugins] = useState<PluginRow[]>([])
  const [details, setDetails] = useState<Record<string, PluginDetail>>({})
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [dirOpen, setDirOpen] = useState(false)
  const [dir, setDir] = useState('')
  const [busy, setBusy] = useState(false)
  const [screenFor, setScreenFor] = useState<string | null>(null)
  const [unsandboxed, setUnsandboxed] = useState<boolean | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  const fail = useCallback((e: unknown) => setError(e instanceof Error ? e.message : String(e)), [])

  const refresh = useCallback(async () => {
    try {
      // One request for the list and every card's detail: this runs every
      // five seconds while the page is open, and it used to be one request
      // per card on top of the list.
      const read = await api.listPluginDetails()
      setPlugins(read)
      refreshPlugins()
      setDetails(Object.fromEntries(read.map((d) => [d.id, d])))
    } catch (e) {
      fail(e)
    }
  }, [fail])

  useEffect(() => {
    api.unsandboxed().then((v) => setUnsandboxed(v.enabled), () => {})
  }, [])

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
            data-testid="plugin-new"
            disabled={busy}
            onClick={() => {
              setNewOpen((v) => !v)
              setDirOpen(false)
            }}
            className="vp-outline text-vp-base"
          >
            <Plus size={13} />
            {t('plg.new')}
          </button>
          <button
            type="button"
            data-testid="plugin-add-dir"
            disabled={busy}
            onClick={() => {
              setDirOpen((v) => !v)
              setNewOpen(false)
            }}
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

      {newOpen && (
        <NewPlugin
          onCancel={() => setNewOpen(false)}
          onMade={(made) => {
            setNewOpen(false)
            added(made.plugin)
            onOpenProject?.(made.projectId)
          }}
          onError={(e) => {
            setNotice('')
            fail(e)
          }}
        />
      )}

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

      {/* The panel-wide switch for rung-4 modules (docs/plugins.md §7): one
          strip, the red sentence beside it, and the way back in words. Only
          drawn when some plugin has a module, or the switch is already on:
          a control for a thing nobody has is a question nobody asked. */}
      {unsandboxed !== null && (unsandboxed || plugins.some((p) => p.rungs.unsandboxed)) && (
        <div className="mb-4 flex flex-col gap-2 rounded-vp border px-3 py-2 @3xl:flex-row @3xl:items-center @3xl:gap-4" style={{ borderColor: 'color-mix(in srgb, var(--vp-state-crashed) 50%, transparent)' }} data-testid="plugins-unsandboxed">
          <label className="flex items-center gap-2 text-vp-base text-ink">
            <input
              type="checkbox"
              data-testid="plugins-unsandboxed-switch"
              checked={unsandboxed}
              onChange={(e) => {
                const next = e.target.checked
                setUnsandboxed(next)
                api.setUnsandboxed(next).then(
                  (v) => {
                    setUnsandboxed(v.enabled)
                    void refresh()
                  },
                  (err: unknown) => {
                    setUnsandboxed(!next)
                    fail(err)
                  },
                )
              }}
              className="accent-[var(--vp-state-crashed)]"
            />
            <span className="font-medium">{t('plg.unsandboxed.title')}</span>
          </label>
          <span className="text-vp-sm" style={{ color: 'var(--vp-state-crashed)' }}>{t('plg.unsandboxed.why')}</span>
          <code className="font-mono text-vp-xs text-ink-3">{t('plg.unsandboxed.back')}</code>
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
  const [logOpen, setLogOpen] = useState(false)
  const [procOpen, setProcOpen] = useState(false)
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
          {row.rungs.process && row.installedVersion > 0 && (
            <button type="button" data-testid="plugin-proc" aria-pressed={procOpen} onClick={() => setProcOpen((v) => !v)} className="vp-outline text-vp-sm">
              <Cpu size={12} />
              {t('plg.proc')}
            </button>
          )}
          {row.rungs.service && (
            <button type="button" data-testid="plugin-log" aria-pressed={logOpen} onClick={() => setLogOpen((v) => !v)} className="vp-outline text-vp-sm">
              <ScrollText size={12} />
              {t('plg.service')}
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
      {logOpen && <ServiceBlock id={row.id} onError={onError} />}
      {procOpen && <ProcessBlock id={row.id} onError={onError} />}
    </article>
  )
}

/**
 * What a service did: its log, how many events it dropped, and each source's
 * last fetch. Polled while open, because a log is read to watch something
 * happen.
 */
function ServiceBlock({ id, onError }: { id: string; onError: (e: unknown) => void }) {
  const [log, setLog] = useState<{ lines: SharePageServerLogLine[]; dropped: number } | null>(null)
  const [sources, setSources] = useState<PluginSourceRow[]>([])
  // The clock is read when the poll answers, not during render: "fetched
  // 12s ago" is true of the moment the reading arrived.
  const [now, setNow] = useState(0)
  useEffect(() => {
    let cancelled = false
    const read = () => {
      if (document.hidden) return
      Promise.all([api.pluginServerLog(id), api.pluginSources(id)]).then(
        ([l, srcs]) => {
          if (cancelled) return
          setLog(l)
          setSources(srcs)
          setNow(Math.floor(Date.now() / 1000))
        },
        (e: unknown) => {
          if (!cancelled) onError(e)
        },
      )
    }
    read()
    const timer = window.setInterval(read, 3000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [id, onError])
  const ago = (unix: number) => {
    const d = Math.max(0, now - unix)
    return d < 60 ? `${d}s` : d < 3600 ? `${Math.floor(d / 60)}m` : `${Math.floor(d / 3600)}h`
  }
  return (
    <div className="grid gap-3 border-t border-hairline p-3 @md:p-4" data-testid="plugin-service">
      {sources.length > 0 && (
        <div className="grid gap-1">
          <h3 className="text-vp-xs font-semibold tracking-wide text-ink-3 uppercase">{t('plg.service.sources')}</h3>
          {sources.map((src) => (
            <div key={src.key} className="flex flex-wrap items-baseline gap-x-3 text-vp-sm" data-testid={`plugin-source-${src.key}`}>
              <code className="font-mono text-ink">{safeText(src.key)}</code>
              <span className="min-w-0 truncate text-ink-3">{safeText(src.host)}</span>
              <span className="text-vp-xs" style={{ color: !src.granted ? 'var(--vp-state-waiting)' : src.ok ? 'var(--vp-state-done)' : src.fetchedAt ? 'var(--vp-state-crashed)' : 'var(--vp-ink-3)' }}>
                {!src.granted
                  ? t('plg.service.notGranted')
                  : !src.fetchedAt
                    ? t('plg.service.notFetched')
                    : src.ok
                      ? t('plg.service.fetched', { ago: ago(src.fetchedAt) })
                      : safeText(src.error)}
              </span>
            </div>
          ))}
        </div>
      )}
      <div className="grid gap-1">
        <h3 className="text-vp-xs font-semibold tracking-wide text-ink-3 uppercase">{t('plg.service.log')}</h3>
        {log && log.dropped > 0 && (
          <p className="text-vp-xs" style={{ color: 'var(--vp-state-waiting)' }} data-testid="plugin-dropped">
            {t('plg.service.dropped', { n: log.dropped })}
          </p>
        )}
        {log && log.lines.length === 0 ? (
          <p className="text-vp-sm text-ink-3">{t('plg.service.empty')}</p>
        ) : (
          <pre className="max-h-64 overflow-auto rounded-vp bg-surface-2 p-2 font-mono text-vp-xs leading-relaxed text-ink" data-testid="plugin-log-lines">
            {(log?.lines ?? []).map((l) => `${new Date(l.at * 1000).toLocaleTimeString()} ${l.level} ${safeText(l.text)}`).join('\n')}
          </pre>
        )}
      </div>
    </div>
  )
}

/** The supervised process: its state in a sentence, its output, a restart. */
function ProcessBlock({ id, onError }: { id: string; onError: (e: unknown) => void }) {
  const [st, setSt] = useState<PluginProcessStatus | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    let cancelled = false
    const read = () => {
      if (document.hidden) return
      api.pluginProcess(id).then(
        (p) => {
          if (!cancelled) setSt(p)
        },
        (e: unknown) => {
          if (!cancelled) onError(e)
        },
      )
    }
    read()
    const timer = window.setInterval(read, 2000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [id, onError])
  const restart = async () => {
    setBusy(true)
    try {
      setSt(await api.restartPluginProcess(id))
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }
  if (!st) return null
  const line = st.running
    ? t('plg.proc.running', { pid: st.pid, n: st.restarts })
    : st.stopped
      ? t('plg.proc.stopped', { why: safeText(st.stopWhy) })
      : st.waitingFor.length > 0
        ? t('plg.proc.waitingFor', { names: safeText(st.waitingFor.join(', ')) })
        : t('plg.proc.waiting', { exit: st.lastExit ? t('plg.proc.lastExit', { exit: safeText(st.lastExit) }) : '' })
  const bin = st.command.split(' ')[0] ?? ''
  return (
    <div className="grid gap-2 border-t border-hairline p-3 @md:p-4" data-testid="plugin-process">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-vp-sm">
        <code className="font-mono text-ink">{safeText(st.command)}</code>
        <span data-testid="plugin-process-state" style={{ color: st.running ? 'var(--vp-state-done)' : st.stopped ? 'var(--vp-state-crashed)' : 'var(--vp-state-waiting)' }}>
          {line}
        </span>
        {!st.onPath && (
          <span style={{ color: 'var(--vp-state-crashed)' }}>{t('plg.proc.notOnPath', { bin: safeText(bin) })}</span>
        )}
        <button type="button" disabled={busy} onClick={() => void restart()} data-testid="plugin-process-restart" className="vp-outline ml-auto text-vp-sm disabled:opacity-40">
          {t('plg.proc.restart')}
        </button>
      </div>
      {st.mount && <DoorBlock id={id} st={st} onError={onError} />}
      <DownloadsBlock id={id} onError={onError} />
      <h3 className="text-vp-xs font-semibold tracking-wide text-ink-3 uppercase">{t('plg.proc.output')}</h3>
      {st.output === '' ? (
        <p className="text-vp-sm text-ink-3">{t('plg.proc.noOutput')}</p>
      ) : (
        <pre className="max-h-64 overflow-auto rounded-vp bg-surface-2 p-2 font-mono text-vp-xs leading-relaxed text-ink" data-testid="plugin-process-output">
          {safeText(st.output.slice(-8000))}
        </pre>
      )}
    </div>
  )
}

/**
 * New plugin (docs/plugins.md §9): a name and a template. The server
 * scaffolds the directory, stores it in dev mode and finds or makes the
 * project at it; the page then hands that project to the panel, which opens
 * the launch picker with the first line typed. Templates come from the
 * server, lowest rung first, so the list here is never a second copy.
 */
function NewPlugin({
  onCancel,
  onMade,
  onError,
}: {
  onCancel: () => void
  onMade: (made: NewPluginResult) => void
  onError: (e: unknown) => void
}) {
  const [name, setName] = useState('')
  const [template, setTemplate] = useState('pane')
  const [dir, setDir] = useState('')
  const [templates, setTemplates] = useState<PluginTemplate[]>([])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api.pluginTemplates().then(setTemplates, () => {})
  }, [])

  const create = async () => {
    const trimmed = name.trim()
    if (!trimmed) return
    setBusy(true)
    try {
      onMade(await api.newPlugin({ name: trimmed, template, path: dir.trim() }))
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div data-testid="plugin-new-form" className="vp-panel-in mb-4 rounded-vp-lg border border-accent/40 bg-surface p-4 shadow-sm">
      <div className="mb-1 flex items-center gap-2">
        <Plus size={14} className="shrink-0 text-ink-3" />
        <h3 className="text-vp-md font-medium text-ink">{t('plg.new')}</h3>
      </div>
      <p className="mb-3 text-vp-sm text-ink-3">{t('plg.newWhy')}</p>
      <div className="mb-3 grid grid-cols-1 gap-x-3 gap-y-2 @md:grid-cols-2">
        <Field label={t('plg.newName')} htmlFor="plugin-new-name">
          <input
            id="plugin-new-name"
            data-testid="plugin-new-name"
            value={name}
            maxLength={64}
            autoFocus
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void create()
            }}
            className={`${INPUT} w-full`}
          />
        </Field>
        <Field label={t('plg.newDir')} htmlFor="plugin-new-dir">
          <input
            id="plugin-new-dir"
            data-testid="plugin-new-dir"
            value={dir}
            placeholder={t('plg.newDirHint')}
            onChange={(e) => setDir(e.target.value)}
            className={`${INPUT} w-full`}
          />
        </Field>
      </div>
      <fieldset className="mb-3">
        <legend className="mb-1 block text-vp-sm text-ink-3">{t('plg.newTemplate')}</legend>
        <div className="grid grid-cols-1 gap-1.5 @md:grid-cols-2 @3xl:grid-cols-3">
          {templates.map((tp) => (
            <label
              key={tp.id}
              data-testid={`plugin-new-tpl-${tp.id}`}
              className={`flex cursor-pointer items-start gap-2 rounded-vp border px-2.5 py-2 text-vp-base ${
                template === tp.id ? 'border-accent bg-accent/5' : 'border-hairline hover:bg-surface-2'
              }`}
            >
              <input
                type="radio"
                name="plugin-new-template"
                value={tp.id}
                checked={template === tp.id}
                onChange={() => setTemplate(tp.id)}
                className="mt-1 accent-[var(--vp-accent)]"
              />
              <span className="min-w-0">
                <span className="block font-medium text-ink">{tp.id}</span>
                <span className="block text-vp-sm text-ink-3">{tKey(`plg.tpl.${tp.id}`) ?? tp.id}</span>
                <span className="mt-1 flex flex-wrap gap-1">
                  {rungsOf(tp).map((r) => (
                    <Chip key={r}>{t(`plg.rung.${r}`)}</Chip>
                  ))}
                </span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      <div className="flex flex-wrap items-center justify-end gap-2">
        <button type="button" onClick={onCancel} className="vp-outline text-vp-base">
          {t('dir.cancel')}
        </button>
        <button
          type="button"
          data-testid="plugin-new-create"
          disabled={busy || !name.trim()}
          onClick={() => void create()}
          className="vp-press flex items-center gap-1 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-40"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          <Plus size={13} />
          {t('plg.create')}
        </button>
      </div>
    </div>
  )
}

/**
 * The door on the panel's port (docs/plugins.md §5, "a process on the
 * panel's port"): where it answers and who may call; for a token door, the
 * owner's tokens, minted here and shown once; and the owner's list of
 * origins that may call across, which is the owner's and never the plugin's.
 */
function DoorBlock({ id, st, onError }: { id: string; st: PluginProcessStatus; onError: (e: unknown) => void }) {
  const [tokens, setTokens] = useState<PluginAccessToken[]>([])
  const [name, setName] = useState('')
  const [minted, setMinted] = useState<{ token: string; name: string } | null>(null)
  const [copied, setCopied] = useState(false)
  const [origins, setOrigins] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const isToken = st.auth === 'token'

  useEffect(() => {
    let cancelled = false
    // The token list is re-read while the block is on screen: a token's
    // last use is written by the door, not by this page.
    const readTokens = () => {
      if (!isToken || document.hidden) return
      api.pluginAccessTokens(id).then(
        (list) => {
          if (!cancelled) setTokens(list)
        },
        () => {},
      )
    }
    readTokens()
    const timer = window.setInterval(readTokens, 5000)
    api.pluginOrigins(id).then(
      (o) => {
        if (!cancelled) setOrigins(o.origins.join('\n'))
      },
      () => {},
    )
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [id, isToken])

  const mint = async () => {
    const trimmed = name.trim()
    if (!trimmed) return
    setBusy(true)
    try {
      const made = await api.createPluginAccessToken(id, trimmed)
      setMinted({ token: made.token, name: trimmed })
      setCopied(false)
      setName('')
      setTokens(await api.pluginAccessTokens(id))
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }
  const revoke = (tok: PluginAccessToken) => {
    confirmThen(
      {
        title: t('plg.door.revoke'),
        body: t('plg.door.revokeConfirm', { name: safeText(tok.name) }),
        confirm: t('plg.door.revoke'),
        cancel: t('dir.cancel'),
        destructive: true,
      },
      async () => {
        try {
          await api.revokePluginAccessToken(id, tok.id)
          setTokens(await api.pluginAccessTokens(id))
        } catch (e) {
          onError(e)
        }
      },
    )
  }
  const saveOrigins = async () => {
    if (origins === null) return
    setBusy(true)
    try {
      const o = await api.setPluginOrigins(id, origins.split('\n'))
      setOrigins(o.origins.join('\n'))
      setSaved(true)
    } catch (e) {
      onError(e)
    } finally {
      setBusy(false)
    }
  }
  const authWord = t(`plg.door.auth.${st.auth as 'owner' | 'token' | 'hmac'}`)
  return (
    <div className="grid gap-2 rounded-vp border border-hairline bg-surface-2/40 p-3" data-testid="plugin-door">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-vp-sm">
        <DoorOpen size={13} className="shrink-0 text-ink-3" />
        <span className="font-medium text-ink">{t('plg.door')}</span>
        <code className="font-mono text-ink" data-testid="plugin-door-mount">{st.mount}</code>
        <span className="text-ink-2">{authWord}</span>
        <span data-testid="plugin-door-socket" style={{ color: st.socketUp ? 'var(--vp-state-done)' : 'var(--vp-state-waiting)' }}>
          {st.socketUp ? '●' : '○'} {t(st.socketUp ? 'plg.door.socketUp' : 'plg.door.socketDown')}
        </span>
      </div>
      {isToken && (
        <div className="grid gap-1.5">
          <div className="flex items-center gap-1.5 text-vp-sm">
            <KeyRound size={12} className="shrink-0 text-ink-3" />
            <span className="font-medium text-ink">{t('plg.door.tokens')}</span>
            <span className="text-ink-3">{t('plg.door.tokensWhy')}</span>
          </div>
          {tokens.length === 0 && <p className="text-vp-sm text-ink-3">{t('plg.door.noTokens')}</p>}
          <ul className="grid gap-1" data-testid="plugin-door-tokens">
            {tokens.map((tok) => (
              <li key={tok.id} className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-vp-sm" data-testid="plugin-door-token">
                <span className="text-ink">{safeText(tok.name)}</span>
                <span className="text-ink-3">
                  {tok.revokedAt > 0
                    ? t('plg.door.revoked')
                    : tok.lastUsedAt > 0
                      ? t('plg.door.lastUsed', { when: new Date(tok.lastUsedAt * 1000).toLocaleString() })
                      : t('plg.door.neverUsed')}
                </span>
                {tok.revokedAt === 0 && (
                  <button type="button" onClick={() => revoke(tok)} data-testid="plugin-door-revoke" className="vp-outline ml-auto text-vp-xs">
                    {t('plg.door.revoke')}
                  </button>
                )}
              </li>
            ))}
          </ul>
          <div className="flex flex-wrap items-center gap-2">
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void mint()
              }}
              placeholder={t('plg.door.tokenName')}
              maxLength={64}
              data-testid="plugin-door-token-name"
              className={`${INPUT} flex-1`}
            />
            <button type="button" disabled={busy || !name.trim()} onClick={() => void mint()} data-testid="plugin-door-mint" className="vp-outline text-vp-sm disabled:opacity-40">
              {t('plg.door.mint')}
            </button>
          </div>
          {minted && (
            <div className="flex flex-wrap items-center gap-2 rounded-vp border border-accent/40 bg-surface p-2 text-vp-sm" data-testid="plugin-door-minted">
              <span className="text-ink-2">{t('plg.door.minted')}</span>
              <code className="break-all font-mono text-ink" data-testid="plugin-door-token-value">{minted.token}</code>
              <button
                type="button"
                onClick={() => copyTextInGesture(minted.token, (ok) => setCopied(ok))}
                className="vp-outline text-vp-xs"
              >
                {copied ? t('plg.door.copied') : t('plg.door.copy')}
              </button>
            </div>
          )}
        </div>
      )}
      {origins !== null && (
        <div className="grid gap-1.5">
          <div className="text-vp-sm">
            <span className="font-medium text-ink">{t('plg.door.origins')}</span> <span className="text-ink-3">{t('plg.door.originsWhy')}</span>
          </div>
          <textarea
            value={origins}
            onChange={(e) => {
              setOrigins(e.target.value)
              setSaved(false)
            }}
            rows={2}
            placeholder={"https://glasses.example\nhttp://127.0.0.1:0"}
            data-testid="plugin-door-origins"
            className={`${INPUT} w-full font-mono`}
          />
          <div className="flex items-center gap-2">
            <button type="button" disabled={busy} onClick={() => void saveOrigins()} data-testid="plugin-door-origins-save" className="vp-outline text-vp-sm disabled:opacity-40">
              {t('plg.door.originsSave')}
            </button>
            {saved && <span className="text-vp-sm text-ink-3">{t('plg.door.originsSaved')}</span>}
          </div>
        </div>
      )}
    </div>
  )
}

/** Bytes for a card: 239 MB. */
function humanBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`
  if (n >= 1 << 20) return `${Math.round(n / (1 << 20))} MB`
  if (n >= 1 << 10) return `${Math.round(n / (1 << 10))} KB`
  return `${n} B`
}

/**
 * Declared downloads (docs/plugins.md §5): one row per declaration, with
 * the job's progress while one runs. A required one is fetched at enable;
 * an optional one waits for the button here. Polled while on screen,
 * faster while something is running.
 */
function DownloadsBlock({ id, onError }: { id: string; onError: (e: unknown) => void }) {
  const lang = useLang()
  const [rows, setRows] = useState<PluginDownloadRow[] | null>(null)
  const [busy, setBusy] = useState('')
  const running = rows?.some((r) => r.status !== '' && r.status !== 'failed') ?? false
  useEffect(() => {
    let cancelled = false
    const read = () => {
      if (document.hidden) return
      api.pluginDownloads(id).then(
        (list) => {
          if (!cancelled) setRows(list)
        },
        () => {},
      )
    }
    read()
    const timer = window.setInterval(read, running ? 1000 : 5000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [id, running])
  if (!rows || rows.length === 0) return null
  const act = async (name: string, fn: () => Promise<PluginDownloadRow[]>) => {
    setBusy(name)
    try {
      setRows(await fn())
    } catch (e) {
      onError(e)
    } finally {
      setBusy('')
    }
  }
  const remove = (r: PluginDownloadRow) => {
    confirmThen(
      {
        title: t('plg.dl.remove'),
        body: t('plg.dl.removeConfirm', { name: safeText(textIn(r.label, lang)) }),
        confirm: t('plg.dl.remove'),
        cancel: t('dir.cancel'),
        destructive: true,
      },
      () => act(r.name, () => api.removePluginDownload(id, r.name)),
    )
  }
  const state = (r: PluginDownloadRow): { words: string; color: string } => {
    switch (r.status) {
      case 'downloading':
        return { words: t('plg.dl.downloading', { pct: r.size > 0 ? Math.min(100, Math.round((r.received / r.size) * 100)) : 0 }), color: 'var(--vp-state-working)' }
      case 'verifying':
        return { words: t('plg.dl.verifying'), color: 'var(--vp-state-working)' }
      case 'unpacking':
        return { words: t('plg.dl.unpacking'), color: 'var(--vp-state-working)' }
      case 'failed':
        return { words: t('plg.dl.failed', { why: safeText(r.error) }), color: 'var(--vp-state-crashed)' }
    }
    return r.ready ? { words: t('plg.dl.ready'), color: 'var(--vp-state-done)' } : { words: t('plg.dl.missing'), color: 'var(--vp-ink-3)' }
  }
  return (
    <div className="grid gap-1.5 rounded-vp border border-hairline bg-surface-2/40 p-3" data-testid="plugin-downloads">
      <div className="flex flex-wrap items-center gap-1.5 text-vp-sm">
        <HardDriveDownload size={13} className="shrink-0 text-ink-3" />
        <span className="font-medium text-ink">{t('plg.dl')}</span>
        <span className="text-ink-3">{t('plg.dl.why')}</span>
      </div>
      <ul className="grid gap-1.5">
        {rows.map((r) => {
          const st = state(r)
          const live = r.status !== '' && r.status !== 'failed'
          return (
            <li key={r.name} className="grid gap-1 text-vp-sm" data-testid={`plugin-download-${r.name}`}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                <span className="text-ink">{safeText(textIn(r.label, lang))}</span>
                <Chip>{t(r.optional ? 'plg.dl.optional' : 'plg.dl.required')}</Chip>
                <span className="text-ink-3">{t('plg.dl.from', { size: humanBytes(r.size), host: safeText(r.host) })}</span>
                <span data-testid={`plugin-download-${r.name}-state`} style={{ color: st.color }}>
                  {st.words}
                </span>
                <span className="ml-auto flex items-center gap-1.5">
                  {!r.ready && !live && (
                    <button type="button" disabled={busy !== ''} onClick={() => void act(r.name, () => api.startPluginDownload(id, r.name))} data-testid={`plugin-download-${r.name}-start`} className="vp-outline text-vp-xs disabled:opacity-40">
                      <Download size={11} />
                      {t(r.status === 'failed' ? 'plg.dl.retry' : 'plg.dl.install')}
                    </button>
                  )}
                  {(r.ready || live) && (
                    <button type="button" disabled={busy !== ''} onClick={() => remove(r)} data-testid={`plugin-download-${r.name}-remove`} className="vp-outline text-vp-xs disabled:opacity-40">
                      <Trash2 size={11} />
                      {t('plg.dl.remove')}
                    </button>
                  )}
                </span>
              </div>
              {r.status === 'downloading' && r.size > 0 && (
                <div className="h-1 w-full overflow-hidden rounded-full bg-surface-2">
                  <div className="h-full" style={{ width: `${Math.min(100, (r.received / r.size) * 100)}%`, background: 'var(--vp-accent)' }} />
                </div>
              )}
              <code className="truncate font-mono text-vp-xs text-ink-3" title={r.sha256}>
                sha256 {r.sha256.slice(0, 16)}…
              </code>
            </li>
          )
        })}
      </ul>
    </div>
  )
}
