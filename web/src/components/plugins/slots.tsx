import { useCallback, useEffect, useState } from 'react'
import { ArrowLeft, LogOut } from 'lucide-react'

import { t, useLang } from '../../i18n'
import { api } from '../../protocol/api'
import type { AuthState, PluginRow } from '../../protocol/wire'
import { PANEL_PATH } from '../../routes'
import { Mark } from './../AuthGate'
import { ConfirmDialog } from '../ConfirmDialog'
import { LanguageSwitch } from '../LanguageSwitch'
import { ThemeToggle } from '../ThemeToggle'
import { safeText } from '../text'
import { applyTheme, loadTheme } from '../theme'
import type { ThemeChoice } from '../theme'
import type { SettingsGroup } from '../settings/groups'
import { PluginFrame } from './PluginFrame'
import { entriesAt, pageEntry } from './host'
import { textIn } from './screen'

/**
 * The slots: where the panel draws a plugin's frames (docs/plugins.md §5).
 *
 * The list of installed plugins is read here, once per page and again every
 * half minute or when a frame asks, rather than pushed over the socket: it
 * changes when somebody installs something, which is rare, and the snapshot
 * every viewer receives on every change should not carry it.
 */

const PLUGINS_MS = 30_000

let cached: PluginRow[] | null = null
const listeners = new Set<() => void>()

function publish(list: PluginRow[]) {
  cached = list
  for (const fn of listeners) fn()
}

/** Ask every slot to re-read the list: after an install, an enable, dev mode. */
export function refreshPlugins() {
  api.listPlugins().then(publish, () => {})
}

/** The installed plugins, shared by every slot on the page. */
export function usePlugins(): PluginRow[] {
  const [list, setList] = useState<PluginRow[]>(cached ?? [])
  useEffect(() => {
    const fn = () => setList(cached ?? [])
    listeners.add(fn)
    if (cached === null) refreshPlugins()
    const timer = window.setInterval(() => {
      if (!document.hidden) refreshPlugins()
    }, PLUGINS_MS)
    return () => {
      listeners.delete(fn)
      clearInterval(timer)
    }
  }, [])
  return list
}

/**
 * A plugin's settings sections under one rail item, after the panel's own.
 * Drawn like a `Section` -- the same heading, the same spacing -- with the
 * frame sized to its content.
 */
export function PluginSections({ group }: { group: SettingsGroup }) {
  const lang = useLang()
  const plugins = usePlugins()
  const entries = entriesAt(plugins, 'settings.section').filter((e) => (e.spec.group || 'panel') === group)
  if (entries.length === 0) return null
  return (
    <>
      {entries.map((e) => (
        <section key={`${e.plugin.id}:${e.n}`} data-section={`ext:${e.plugin.id}:${e.n}`} data-testid="plugin-section" className="mb-6 last:mb-0">
          <h3 className="mb-2 text-vp-sm font-semibold tracking-wide text-ink-2 uppercase">
            {safeText(textIn(e.spec.title, lang))}
          </h3>
          <PluginFrame plugin={e.plugin} entry={e.spec.entry} slot="settings.section" testid={`plugin-frame-${e.plugin.id}-${e.n}`} />
        </section>
      ))}
    </>
  )
}

/** The header's plugin items: a small frame each, beside the panel's controls. */
export function PluginHeaderItems() {
  const lang = useLang()
  const plugins = usePlugins()
  const entries = entriesAt(plugins, 'header.item')
  if (entries.length === 0) return null
  return (
    <>
      {entries.map((e) => (
        <span
          key={`${e.plugin.id}:${e.n}`}
          data-testid="plugin-header-item"
          title={safeText(textIn(e.spec.title, lang))}
          className="hidden h-7 w-40 shrink-0 overflow-hidden rounded-vp border border-hairline sm:block"
        >
          <PluginFrame plugin={e.plugin} entry={e.spec.entry} slot="header.item" fill testid={`plugin-frame-${e.plugin.id}-${e.n}`} />
        </span>
      ))}
    </>
  )
}

/**
 * A plugin's page at `/x/<path>`: the panel's frame around a full-height
 * frame of the plugin's. The same header as the sharing page, because a page
 * that keeps the cookie but loses the theme goes white at 2am.
 */
export function PluginPageRoot({ path, auth, onSignOut }: { path: string; auth: AuthState; onSignOut: () => void }) {
  const lang = useLang()
  const plugins = usePlugins()
  const entry = pageEntry(plugins, path)
  const [theme, setThemeState] = useState<ThemeChoice>(loadTheme)
  const setTheme = useCallback((next: ThemeChoice) => {
    applyTheme(next)
    setThemeState(next)
  }, [])
  const title = entry ? textIn(entry.spec.title, lang) : path

  useEffect(() => {
    document.title = `${title} · vibepanel`
  }, [title])

  return (
    <div data-testid="plugin-page" className="flex h-full w-full flex-col bg-bg text-ink">
      <header className="vp-blur vp-safe-pad-top sticky top-0 z-10 shrink-0 border-b border-hairline">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-2 px-4 py-2 sm:px-6">
          <a href={PANEL_PATH} data-testid="plugin-page-back" title={t('sharing.back')} className="vp-control vp-press gap-1.5 pr-2">
            <ArrowLeft size={15} />
            <Mark small />
          </a>
          <span className="flex min-w-0 items-center gap-1.5 text-vp-md">
            <span className="font-semibold tracking-tight text-ink">vibepanel</span>
            <span className="text-ink-3" aria-hidden="true">
              /
            </span>
            <span className="truncate text-ink-2">{safeText(title)}</span>
          </span>
          <div className="ml-auto flex items-center gap-1.5">
            <LanguageSwitch testid="plugin-page" />
            <ThemeToggle theme={theme} onChange={setTheme} />
            <button type="button" data-testid="sign-out" onClick={onSignOut} title={t('app.signedInAs', { user: safeText(auth.username ?? '?') })} className="vp-control">
              <LogOut size={15} />
            </button>
          </div>
        </div>
      </header>
      <main className="min-h-0 flex-1">
        {entry ? (
          <PluginFrame plugin={entry.plugin} entry={entry.spec.entry} slot="page" fill testid={`plugin-frame-${entry.plugin.id}-${entry.n}`} />
        ) : cached !== null ? (
          <p className="px-6 py-8 text-vp-base text-ink-2" data-testid="plugin-page-missing">
            {t('plg.page.missing')}
          </p>
        ) : null}
      </main>
      <ConfirmDialog />
    </div>
  )
}
