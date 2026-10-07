import { useCallback, useEffect, useState } from 'react'
import { ArrowLeft, LogOut } from 'lucide-react'

import { t, useLang } from '../i18n'
import type { AuthState } from '../protocol/wire'
import { PANEL_PATH } from '../routes'
import { Mark } from './AuthGate'
import { ConfirmDialog } from './ConfirmDialog'
import { LanguageSwitch } from './LanguageSwitch'
import { ThemeToggle } from './ThemeToggle'
import { Plugins } from './plugins/Plugins'
import { safeText } from './text'
import { applyTheme, loadTheme } from './theme'
import type { ThemeChoice } from './theme'

/**
 * The page plugins are installed, granted and switched from (docs/plugins.md).
 *
 * A page of its own for the reason the sharing page is one: a list of cards
 * with an install screen and a settings form unfolding under each wants the
 * whole window, and the settings dialog's body is a third of it. The rail in
 * the dialog still says "Plugins", as a link that leaves it.
 *
 * Behind the same AuthGate as the panel and on the same cookie. What it draws
 * of its own is the frame — the way back, the language, the theme and the way
 * out — and the theme control is the one a plugin theme first shows up in.
 */
export function PluginsPage({ auth, onSignOut }: { auth: AuthState; onSignOut: () => void }) {
  useLang()
  const [theme, setThemeState] = useState<ThemeChoice>(loadTheme)
  const setTheme = useCallback((next: ThemeChoice) => {
    applyTheme(next)
    setThemeState(next)
  }, [])

  useEffect(() => {
    document.title = `${t('grp.plugins')} · vibepanel`
  })

  return (
    <div data-testid="plugins-page" className="h-full w-full overflow-y-auto bg-bg text-ink">
      <header className="vp-blur vp-safe-pad-top sticky top-0 z-10 border-b border-hairline">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-2 px-4 py-2 sm:px-6">
          <a
            href={PANEL_PATH}
            data-testid="plugins-back"
            title={t('sharing.back')}
            className="vp-control vp-press gap-1.5 pr-2"
          >
            <ArrowLeft size={15} />
            <Mark small />
          </a>
          <span className="flex min-w-0 items-center gap-1.5 text-vp-md">
            <span className="font-semibold tracking-tight text-ink">vibepanel</span>
            <span className="text-ink-3" aria-hidden="true">
              /
            </span>
            <span className="truncate text-ink-2">{t('grp.plugins')}</span>
          </span>
          <div className="ml-auto flex items-center gap-1.5">
            <LanguageSwitch testid="plugins" />
            <ThemeToggle theme={theme} onChange={setTheme} />
            <button
              type="button"
              data-testid="sign-out"
              onClick={onSignOut}
              title={t('app.signedInAs', { user: safeText(auth.username ?? '?') })}
              className="vp-control"
            >
              <LogOut size={15} />
            </button>
          </div>
        </div>
      </header>

      <main className="vp-safe-bottom mx-auto max-w-6xl px-4 pt-6 [--vp-safe-pad:2.5rem] sm:px-6 sm:pt-8">
        <Plugins />
      </main>
      <ConfirmDialog />
    </div>
  )
}
