import { useCallback, useEffect, useState } from 'react'
import { ArrowLeft, LogOut } from 'lucide-react'

import { t, useLang } from '../i18n'
import type { AuthState } from '../protocol/wire'
import { PANEL_PATH } from '../routes'
import { Mark } from './AuthGate'
import { ConfirmDialog } from './ConfirmDialog'
import { LanguageSwitch } from './LanguageSwitch'
import { ThemeToggle } from './ThemeToggle'
import { Toasts } from './Toasts'
import { Chat } from './chat/Chat'
import { safeText } from './text'
import { applyTheme, loadTheme } from './theme'
import type { ThemeChoice } from './theme'

/**
 * The page the chat bridge is set up from: which apps, who is paired, what
 * is pushed where, and the advanced mode.
 *
 * A page rather than a group in the settings dialog, for the reason sharing
 * became one: a list of channels with a form unfolding under each, a rule
 * table, and a QR code to scan do not fit a body a third of the window wide.
 * The frame is the sharing page's frame, so the two rooms feel like rooms of
 * one house.
 */
export function ChatPage({ auth, onSignOut }: { auth: AuthState; onSignOut: () => void }) {
  useLang()
  const [theme, setThemeState] = useState<ThemeChoice>(loadTheme)
  const setTheme = useCallback((next: ThemeChoice) => {
    applyTheme(next)
    setThemeState(next)
  }, [])

  useEffect(() => {
    document.title = `${t('grp.chat')} · vibepanel`
  })

  return (
    <div data-testid="chat-page" className="h-full w-full overflow-y-auto bg-bg text-ink">
      <header className="vp-blur vp-safe-pad-top sticky top-0 z-10 border-b border-hairline">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center gap-2 px-4 py-2 sm:px-6">
          <a
            href={PANEL_PATH}
            data-testid="chat-back"
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
            <span className="truncate text-ink-2">{t('grp.chat')}</span>
          </span>
          <div className="ml-auto flex items-center gap-1.5">
            <LanguageSwitch testid="chat" />
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
        <h1 className="mb-1 text-vp-xl font-semibold tracking-tight text-ink">{t('chat.title')}</h1>
        <p className="mb-5 max-w-3xl text-vp-base leading-relaxed text-ink-2">{t('chat.intro')}</p>
        <Chat />
      </main>
      <ConfirmDialog />
      {/* Every save on this page answers with a toast; without the stack
          mounted here they were raised and never drawn, and a save looked
          like a button that did nothing. */}
      <Toasts narrow={false} />
    </div>
  )
}
