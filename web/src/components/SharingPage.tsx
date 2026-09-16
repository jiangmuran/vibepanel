import { useCallback, useEffect, useState } from 'react'
import { ArrowLeft, LogOut } from 'lucide-react'

import { t, useLang } from '../i18n'
import type { AuthState } from '../protocol/wire'
import { PANEL_PATH, panelOpeningPage } from '../routes'
import { Mark } from './AuthGate'
import { ConfirmDialog } from './ConfirmDialog'
import { LanguageSwitch } from './LanguageSwitch'
import { ThemeToggle } from './ThemeToggle'
import { Sharing } from './pages/Sharing'
import { safeText } from './text'
import { applyTheme, loadTheme } from './theme'
import type { ThemeChoice } from './theme'

/**
 * The page share pages and their links are made and edited from.
 *
 * It was a group in the settings dialog, and the dialog was the wrong room for
 * it: a list of pages with links and forms unfolding under each, in a body a
 * third of the window wide, and the dialog had grown a second width to hold
 * it. Now it has an address (`routes.ts`), which is also what makes it a
 * bookmark and a second tab.
 *
 * Behind the same AuthGate as the panel and on the same cookie, so arriving
 * here signed in asks nothing. What this page draws of its own is the frame:
 * a header with the way back, the language, the theme and the way out — the
 * same four things the panel's header answers, because a page that keeps the
 * cookie but loses the theme goes white at 2am, which is when the panel is
 * read. Everything below the header is `Sharing`, unchanged in what it
 * reaches: the settings routes, and nothing a share token can see.
 *
 * The one thing it cannot do here is *open* a page — that is a project, a
 * terminal and the Preview, which are the panel's — so it hands the page over
 * by address (`panelOpeningPage`) and the panel takes it from there.
 *
 * `ConfirmDialog` is mounted here as it is in the panel: the questions the
 * list asks before revoking a link go through `askConfirm`, and a host with no
 * dialog is a question that never resolves.
 */
export function SharingPage({ auth, onSignOut }: { auth: AuthState; onSignOut: () => void }) {
  useLang()
  const [theme, setThemeState] = useState<ThemeChoice>(loadTheme)
  const setTheme = useCallback((next: ThemeChoice) => {
    applyTheme(next)
    setThemeState(next)
  }, [])

  useEffect(() => {
    document.title = `${t('grp.sharing')} · vibepanel`
  })

  return (
    <div data-testid="sharing-page" className="h-full w-full overflow-y-auto bg-bg text-ink">
      {/* Sticky, and translucent over what scrolls under it, so the way back
          and the way out are on screen however far down a long list a link's
          editor has been unfolded. */}
      <header className="vp-blur vp-safe-pad-top sticky top-0 z-10 border-b border-hairline">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-2 px-4 py-2 sm:px-6">
          <a
            href={PANEL_PATH}
            data-testid="sharing-back"
            title={t('sharing.back')}
            className="vp-control vp-press gap-1.5 pr-2"
          >
            <ArrowLeft size={15} />
            <Mark small />
          </a>
          {/* Where you are, as a path: the panel's name, then this page's. */}
          <span className="flex min-w-0 items-center gap-1.5 text-vp-md">
            <span className="font-semibold tracking-tight text-ink">vibepanel</span>
            <span className="text-ink-3" aria-hidden="true">
              /
            </span>
            <span className="truncate text-ink-2">{t('grp.sharing')}</span>
          </span>
          <div className="ml-auto flex items-center gap-1.5">
            <LanguageSwitch testid="sharing" />
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
        <h1 className="mb-3 text-vp-xl font-semibold tracking-tight text-ink">{t('page.title')}</h1>
        <Sharing onOpenPage={(page, fresh) => location.assign(panelOpeningPage(page.id, fresh))} />
      </main>
      <ConfirmDialog />
    </div>
  )
}
