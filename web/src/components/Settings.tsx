import { useEffect, useRef, useState } from 'react'
import { ArrowUpRight, Bell, Gauge, MemoryStick, MessageSquare, Share2, Terminal, UserRound, X } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { api } from '../protocol/api'
import type { Session, SettingsInfo } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { CHAT_PATH, SHARING_PATH } from '../routes'
import { LanguageSwitch } from './LanguageSwitch'
import { AccountGroup } from './settings/AccountGroup'
import { NotificationsGroup } from './settings/NotificationsGroup'
import { PanelGroup } from './settings/PanelGroup'
import { ResourcesGroup } from './settings/ResourcesGroup'
import { SessionsGroup } from './settings/SessionsGroup'
import {
  GROUP_TITLE,
  SETTINGS_GROUPS,
  groupFromKey,
  groupOf,
} from './settings/groups'
import type { SettingsGroup, SettingsSection } from './settings/groups'

/** One glyph per rail item, so the rail is scannable before it is read. */
const GROUP_ICON: Record<SettingsGroup, LucideIcon> = {
  sessions: Terminal,
  notify: Bell,
  account: UserRound,
  resources: MemoryStick,
  panel: Gauge,
}

/**
 * What the panel is doing and how it is configured — a rail, and one group.
 *
 * This was twelve sections in one scroll, and finding anything meant reading
 * past everything: 「太长太恶心了」. What is on screen now is the group you asked
 * for. groups.ts holds the five and the argument for that particular five.
 *
 * Callers name a **section**, never a group — `openAt` — so the sidebar's
 * "states are being guessed" notice asks for state reporting and lands on
 * whichever rail item holds it today.
 *
 * The rail is the same five buttons and two links at every width; below `sm`
 * it is a row
 * above the body that scrolls sideways instead of a column beside it. That is
 * a CSS branch and not a JavaScript one, on purpose: `components/chrome.ts`
 * exists because a control that is present at one size and absent at another
 * rearranges the layout under somebody's finger, and the cheapest way to keep
 * that promise here is for there to be nothing to get wrong.
 */
export function Settings({
  openAt,
  onClose,
  sessions,
  focusSession,
}: {
  openAt: SettingsSection
  onClose: () => void
  /** The snapshot's sessions, for names and states on the resources page. */
  sessions: Session[]
  /** A session the resources page opens on, when the memory question sent somebody here. */
  focusSession?: string
}) {
  // Subscribed to the language, so the switch in the header redraws every
  // string in this dialog and not only its own two.
  useLang()
  const [group, setGroup] = useState<SettingsGroup>(() => groupOf(openAt))
  const [info, setInfo] = useState<SettingsInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const railRef = useRef<HTMLDivElement>(null)
  const bodyRef = useRef<HTMLDivElement>(null)

  // Polled while open, not fetched once.
  //
  // Half of what this dialog shows is live — uptime, how many sessions exist,
  // how many browsers are watching, whether the hook script is installed — and
  // it was a photograph taken at the moment the dialog opened. "A settings page
  // for observing the backend" that stops observing the instant you look at it
  // answers a question about the past.
  //
  // Four seconds: this is somewhere you glance to see whether things are
  // healthy, not a monitor. The system monitor tab is where live graphs live,
  // and it has its own cadence.
  //
  // Polled for every group rather than only for the one that draws it: two
  // groups read it, the request is one row of counters, and a poll that starts
  // when you arrive at a tab shows the tab's first paint with nothing in it.
  useEffect(() => {
    let ignore = false
    const load = () =>
      api
        .settings()
        .then((i) => {
          if (!ignore) setInfo(i)
        })
        .catch((e: unknown) => {
          if (!ignore) setError(e instanceof Error ? e.message : String(e))
        })
    void load()
    const timer = window.setInterval(() => void load(), 4000)
    return () => {
      ignore = true
      clearInterval(timer)
    }
  }, [])

  // Escape closes — but only when nothing inside is already using it.
  //
  // Both exceptions were found by breaking them. A dialog that swallows every
  // Escape closes itself *and* the confirmation asking for a passkey name,
  // which is a cancel that throws away the page behind the question. And a
  // control inside may use Escape for itself: the render check once watched a
  // drag in progress survive its own cancel because this listener had closed
  // the dialog out from under the gesture.
  //
  // So: not while another modal is over this one, and not while the keyboard
  // is inside a group, where Escape belongs to whatever is holding it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (document.querySelector('[data-vp-modal]:not([data-vp-modal="settings"])')) return
      if (bodyRef.current?.contains(document.activeElement)) return
      onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // The keyboard starts on the rail, which is where the dialog's own
  // navigation is: arrows move between groups from the first keystroke, and
  // Tab from there walks into the group that is showing. Not on a field —
  // opening settings must never put a cursor in "current password".
  useEffect(() => {
    railRef.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.focus()
  }, [])

  return (
    <div className="vp-backdrop absolute inset-0 z-30 flex items-start justify-center overflow-y-auto bg-black/40 px-4 py-8">
      <div
        data-testid="settings"
        data-vp-modal="settings"
        role="dialog"
        aria-modal="true"
        aria-label={t('settings.title')}
        // A reading measure. Every group is label/value lines and a handful
        // of controls, which read badly past about seventy characters. The
        // one surface that wanted more — sharing — is a page now.
        className="vp-panel-in flex max-h-full w-full max-w-3xl flex-col rounded-vp-lg border border-hairline bg-surface shadow-xl"
      >
        {/* The language switch is in the header rather than in a group, and it
            is the one control here that is about the reading rather than about
            the panel. Somebody who needs it cannot read the rail — which is
            the whole reason they are looking for it — so it may not be behind
            a word they would have to translate first.

            A segmented pair rather than a dropdown: there are two, both fit,
            and a select for two options is a click that buys nothing. Each
            option is written in its own language. */}
        <div className="flex flex-wrap items-center gap-2 border-b border-hairline px-5 py-3">
          <h2 className="mr-auto text-vp-lg font-semibold tracking-tight text-ink">
            {t('settings.title')}
          </h2>
          <LanguageSwitch testid="settings" />
          <button
            type="button"
            onClick={onClose}
            title={t('settings.close')}
            data-testid="settings-close"
            className="vp-control"
          >
            <X size={15} />
          </button>
        </div>

        {error && (
          <p className="px-5 pt-3 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
            {error}
          </p>
        )}

        <div className="flex min-h-0 flex-1 flex-col gap-3 p-4 sm:flex-row sm:gap-5 sm:p-5">
          <div
            ref={railRef}
            data-testid="settings-rail"
            // A row that scrolls sideways on a phone, a column on anything
            // wider. Not a select, and never a fold: the point of the rail is
            // that all the names are readable at once, which is what makes a
            // wrong guess cost one press instead of a hunt.
            className="flex shrink-0 items-center gap-1 overflow-x-auto sm:w-40 sm:flex-col sm:items-stretch sm:overflow-visible"
          >
            <div
              role="tablist"
              aria-label={t('settings.groups')}
              className="flex shrink-0 gap-1 sm:flex-col"
            >
              {SETTINGS_GROUPS.map((id) => {
                const Icon = GROUP_ICON[id]
                return (
                  <button
                    key={id}
                    type="button"
                    role="tab"
                    id={`settings-tab-${id}`}
                    data-testid={`settings-group-${id}`}
                    aria-selected={group === id}
                    aria-controls="settings-panel"
                    // Roving: the rail is one stop in the dialog's tab order and
                    // the arrows move inside it, which is what a tablist is.
                    tabIndex={group === id ? 0 : -1}
                    onClick={() => setGroup(id)}
                    onKeyDown={(e) => {
                      const next = groupFromKey(e.key, group)
                      if (!next) return
                      e.preventDefault()
                      setGroup(next)
                      railRef.current
                        ?.querySelector<HTMLElement>(`[data-testid="settings-group-${next}"]`)
                        ?.focus()
                    }}
                    // No padding of its own: `.vp-tab` is unlayered and Tailwind's
                    // utilities are in a cascade layer, so a `px-` here would be
                    // inert and read as though it were doing something.
                    className="vp-tab shrink-0 justify-start sm:w-full"
                  >
                    <Icon size={13} className="vp-tab-icon shrink-0" />
                    <span className="text-vp-base">{t(GROUP_TITLE[id])}</span>
                  </button>
                )
              })}
            </div>
            {/* Sharing is a page, and this is the way to it from where it used
                to be. A link in the rail's own clothes, after a rule, with the
                arrow that says it leaves: somebody who last saw the list of
                pages under this word still finds it under this word. */}
            <div className="flex shrink-0 sm:mt-1 sm:flex-col sm:border-t sm:border-hairline sm:pt-2">
              <a
                href={SHARING_PATH}
                data-testid="settings-sharing-link"
                className="vp-tab shrink-0 justify-start sm:w-full"
              >
                <Share2 size={13} className="vp-tab-icon shrink-0" />
                <span className="text-vp-base">{t('grp.sharing')}</span>
                <ArrowUpRight size={12} className="ml-auto shrink-0 text-ink-3" aria-hidden="true" />
              </a>
              <a
                href={CHAT_PATH}
                data-testid="settings-chat-link"
                className="vp-tab shrink-0 justify-start sm:w-full"
              >
                <MessageSquare size={13} className="vp-tab-icon shrink-0" />
                <span className="text-vp-base">{t('grp.chat')}</span>
                <ArrowUpRight size={12} className="ml-auto shrink-0 text-ink-3" aria-hidden="true" />
              </a>
            </div>
          </div>

          {/* Keyed by the group, so switching remounts rather than reuses:
              every one of these blocks loads its own data on mount, and a
              reused body would show the previous group's scroll position with
              the next group's contents. */}
          <div
            key={group}
            ref={bodyRef}
            id="settings-panel"
            role="tabpanel"
            aria-labelledby={`settings-tab-${group}`}
            data-testid="settings-body"
            data-group={group}
            className="min-h-0 min-w-0 flex-1 overflow-y-auto"
          >
            {group === 'sessions' && <SessionsGroup />}
            {group === 'notify' && <NotificationsGroup />}
            {group === 'account' && <AccountGroup info={info} />}
            {group === 'resources' && <ResourcesGroup sessions={sessions} focus={focusSession} />}
            {group === 'panel' && <PanelGroup info={info} />}
          </div>
        </div>
      </div>
    </div>
  )
}
