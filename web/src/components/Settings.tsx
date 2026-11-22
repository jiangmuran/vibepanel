import { useEffect, useRef, useState } from 'react'
import { ArrowUpRight, Bell, Gauge, MemoryStick, MessageSquare, Puzzle, Share2, Terminal, UserRound, X } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { api } from '../protocol/api'
import type { Session, SettingsInfo } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { CHAT_PATH, PLUGINS_PATH, SHARING_PATH } from '../routes'
import { useDialogFocus } from './useDialogFocus'
import { useSlidingMarker } from './useSlidingMarker'
import { LanguageSwitch } from './LanguageSwitch'
import { PluginSections } from './plugins/slots'
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
import type { ThemeChoice } from './theme'

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
  theme,
  onThemeChange,
}: {
  openAt: SettingsSection
  onClose: () => void
  /** The snapshot's sessions, for names and states on the resources page. */
  sessions: Session[]
  /** A session the resources page opens on, when the memory question sent somebody here. */
  focusSession?: string
  theme?: ThemeChoice
  onThemeChange?: (next: ThemeChoice) => void
}) {
  // Subscribed to the language, so the switch in the header redraws every
  // string in this dialog and not only its own two.
  const lang = useLang()
  const [group, setGroup] = useState<SettingsGroup>(() => groupOf(openAt))
  const [slide, setSlide] = useState<{ group: SettingsGroup; dir: 'none' | 'down' | 'up' }>(() => ({
    group: groupOf(openAt),
    dir: 'none',
  }))
  if (slide.group !== group) {
    const from = SETTINGS_GROUPS.indexOf(slide.group)
    const to = SETTINGS_GROUPS.indexOf(group)
    setSlide({ group, dir: to >= from ? 'down' : 'up' })
  }
  const [info, setInfo] = useState<SettingsInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const railRef = useRef<HTMLDivElement>(null)
  const tablistRef = useRef<HTMLDivElement>(null)
  const bodyRef = useRef<HTMLDivElement>(null)
  const dialogRef = useRef<HTMLDivElement>(null)
  const marker = useSlidingMarker(tablistRef, `${group}:${lang}`)

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

  // Escape, Tab containment and the way back to the gear are useDialogFocus's.
  // Escape used to be ignored whenever the keyboard was inside the body, so a
  // person who had tabbed to a setting could not leave with the key that
  // leaves everything else. A control that needs Escape for itself calls
  // preventDefault, which the hook honours.
  useDialogFocus(dialogRef, onClose)

  // The keyboard starts on the rail, which is where the dialog's own
  // navigation is: arrows move between groups from the first keystroke, and
  // Tab from there walks into the group that is showing. Not on a field —
  // opening settings must never put a cursor in "current password".
  useEffect(() => {
    railRef.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.focus()
  }, [])

  return (
    <div className="vp-backdrop absolute inset-0 z-30 flex items-end justify-center overflow-y-auto bg-black/45 p-0 @xl:items-start @xl:px-4 @xl:py-8" style={{ alignItems: 'center' }}>
      <div
        ref={dialogRef}
        data-testid="settings"
        data-vp-modal="settings"
        role="dialog"
        aria-modal="true"
        aria-labelledby="vp-settings-title"
        className="vp-panel-in @container flex max-h-[90dvh] w-full max-w-3xl flex-col overflow-hidden rounded-vp-lg border border-hairline-strong bg-surface shadow-2xl mx-3"
      >
        <div className="flex flex-wrap items-center gap-2 border-b border-hairline px-5 py-3">
          <h2 id="vp-settings-title" className="mr-auto text-vp-md font-semibold tracking-tight text-ink">
            {t('settings.title')}
          </h2>
          <LanguageSwitch testid="settings" />
          <button
            type="button"
            onClick={onClose}
            title={t('settings.close')}
            data-testid="settings-close"
            className="vp-control vp-tap"
          >
            <X size={15} />
          </button>
        </div>

        {error && (
          <p className="px-5 pt-3 text-vp-base text-warn-ink">
            {error}
          </p>
        )}

        <div className="flex min-h-0 flex-1 flex-col gap-3 p-4 @xl:flex-row @xl:gap-5 @xl:p-5">
          <div
            ref={railRef}
            data-testid="settings-rail"
            // A row that scrolls sideways on a phone, a column on anything
            // wider. Not a select, and never a fold: the point of the rail is
            // that all the names are readable at once, which is what makes a
            // wrong guess cost one press instead of a hunt.
            className="vp-scroll-x flex shrink-0 items-center gap-1 @xl:w-40 @xl:flex-col @xl:items-stretch @xl:overflow-visible"
          >
            <div
              ref={tablistRef}
              role="tablist"
              aria-label={t('settings.groups')}
              data-marker={marker ? 'on' : 'off'}
              className="relative flex shrink-0 gap-1 @xl:flex-col"
            >
              {marker && (
                <span
                  aria-hidden="true"
                  className="vp-marker-2d vp-marker-rail"
                  style={{
                    width: marker.width,
                    height: marker.height,
                    transform: `translate3d(${marker.left}px, ${marker.top}px, 0)`,
                  }}
                />
              )}
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
                    className="vp-tab shrink-0 justify-start @xl:w-full"
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
            <div className="flex shrink-0 gap-1 @xl:mt-1 @xl:flex-col @xl:border-t @xl:border-hairline @xl:pt-1">
              <a
                href={SHARING_PATH}
                data-testid="settings-sharing-link"
                className="vp-tab shrink-0 justify-start @xl:w-full"
              >
                <Share2 size={13} className="vp-tab-icon shrink-0" />
                <span className="text-vp-base">{t('grp.sharing')}</span>
                <ArrowUpRight size={12} className="ml-auto shrink-0 text-ink-3" aria-hidden="true" />
              </a>
              <a
                href={CHAT_PATH}
                data-testid="settings-chat-link"
                className="vp-tab shrink-0 justify-start @xl:w-full"
              >
                <MessageSquare size={13} className="vp-tab-icon shrink-0" />
                <span className="text-vp-base">{t('grp.chat')}</span>
                <ArrowUpRight size={12} className="ml-auto shrink-0 text-ink-3" aria-hidden="true" />
              </a>
              <a
                href={PLUGINS_PATH}
                data-testid="settings-plugins-link"
                className="vp-tab shrink-0 justify-start @xl:w-full"
              >
                <Puzzle size={13} className="vp-tab-icon shrink-0" />
                <span className="text-vp-base">{t('grp.plugins')}</span>
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
            data-slide={slide.dir}
            className="vp-pane-slide min-h-0 min-w-0 flex-1 overflow-y-auto pr-1"
          >
            {group === 'sessions' && <SessionsGroup />}
            {group === 'notify' && <NotificationsGroup />}
            {group === 'account' && <AccountGroup info={info} />}
            {group === 'resources' && <ResourcesGroup sessions={sessions} focus={focusSession} />}
            {group === 'panel' && <PanelGroup info={info} theme={theme} onThemeChange={onThemeChange} />}
            {/* What plugins add under this rail item: a frame per
                settings.section entry whose group is this one, after the
                panel's own sections. docs/plugins.md §5. */}
            <PluginSections group={group} />
          </div>
        </div>
      </div>
    </div>
  )
}
