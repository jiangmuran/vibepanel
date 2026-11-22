import { useCallback, useEffect, useRef, useState } from 'react'
import { Bell, Bot, Cable, Gauge, ScrollText, Users, Waypoints } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ChatSettings } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import type { Key } from '../../i18n'
import { safeText } from '../text'
import { useSlidingMarker } from '../useSlidingMarker'
import { errText } from './form'
import { Alerts } from './Alerts'
import { Assistant } from './Assistant'
import { Channels } from './Channels'
import { Keys } from './Keys'
import { Log } from './Log'
import { Peers } from './Peers'
import { Routes } from './Routes'

/** Sections that are a tab on their own; see Section. */
const SOLE_SECTIONS = new Set(['channels', 'peers', 'routes', 'alerts', 'log'])

/** How often the page re-reads: for the health lines and a stranger's code. */
const POLL_MS = 4000

/**
 * One block of a tab, with its heading and a line saying what it is for.
 *
 * The heading is the settings dialog's section heading, size and weight, so
 * the two surfaces read as one product; the page used to set its own larger
 * headings with their own margins, and next to the dialog nothing lined up.
 */
export function Section({
  id,
  title,
  lead,
  children,
}: {
  id: string
  title: string
  lead?: string
  children: React.ReactNode
}) {
  // A section that is the whole of its tab does not repeat the tab's name
  // in large type right under it; the heading stays for a screen reader.
  const alone = SOLE_SECTIONS.has(id)
  return (
    <section data-section={id} className="mb-6 @container last:mb-0">
      <h2 className={alone ? 'sr-only' : 'text-vp-md font-semibold tracking-tight text-ink'}>{title}</h2>
      {lead && <p className={`${alone ? '' : 'mt-0.5 '}mb-3 max-w-3xl text-vp-sm leading-relaxed text-ink-2`}>{lead}</p>}
      {!lead && <div className="mb-3" />}
      {children}
    </section>
  )
}

export function Card({ children, testid, className = '' }: { children: React.ReactNode; testid?: string; className?: string }) {
  return (
    <div data-testid={testid} className={`rounded-vp border border-hairline bg-surface p-3 sm:p-4 ${className}`}>
      {children}
    </div>
  )
}

/**
 * The tabs, in the order a person sets the thing up: an app to talk through,
 * who may talk, what they are told, the machine, the advanced mode with its
 * key table, and what happened.
 *
 * Tabs rather than one page of seven blocks: the page had become a scroll of
 * forms where the thing being looked for was always four screens down, and a
 * 400px phone made it twice that. The hash keeps the tab, so a link or a
 * reload lands where it was.
 */
const TABS: { id: string; label: Key; icon: LucideIcon }[] = [
  { id: 'channels', label: 'chat.channels', icon: Cable },
  { id: 'peers', label: 'chat.peers', icon: Users },
  { id: 'routes', label: 'chat.routes', icon: Waypoints },
  { id: 'alerts', label: 'chat.alertsTab', icon: Gauge },
  { id: 'assistant', label: 'chat.assistant', icon: Bot },
  { id: 'log', label: 'chat.log', icon: ScrollText },
]

function tabFromHash(): string {
  const id = location.hash.replace(/^#/, '')
  return TABS.some((x) => x.id === id) ? id : 'channels'
}

/**
 * Everything on the page comes from one GET, polled while the page is open,
 * and every section writes through its own PUT and then asks for a fresh
 * read. No section keeps a copy of another's data, which is what makes a
 * peer paired in one tab show up as a destination in the next.
 */
export function Chat() {
  const lang = useLang()
  const [data, setData] = useState<ChatSettings | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState(tabFromHash)
  const [slide, setSlide] = useState<{ tab: string; dir: 'none' | 'right' | 'left' }>(() => ({
    tab: tabFromHash(),
    dir: 'none',
  }))
  if (slide.tab !== tab) {
    const from = TABS.findIndex((x) => x.id === slide.tab)
    const to = TABS.findIndex((x) => x.id === tab)
    setSlide({ tab, dir: to >= from ? 'right' : 'left' })
  }
  const tabsRef = useRef<HTMLDivElement>(null)
  const marker = useSlidingMarker(tabsRef, `${tab}:${lang}:${Boolean(data?.available)}`)

  // One fetch, used by the poll and by every section after a write. The
  // poll's copy is cancelled on unmount; a section's reload after the page
  // has gone is harmless and does not need to be.
  const reload = useCallback(() => {
    api.chat().then(
      (d) => {
        setData(d)
        setError(null)
      },
      (e: unknown) => setError(errText(e)),
    )
  }, [])

  useEffect(() => {
    reload()
    const timer = window.setInterval(reload, POLL_MS)
    const onHash = () => setTab(tabFromHash())
    window.addEventListener('hashchange', onHash)
    return () => {
      clearInterval(timer)
      window.removeEventListener('hashchange', onHash)
    }
  }, [reload])

  const choose = (id: string) => {
    setTab(id)
    history.replaceState(null, '', `#${id}`)
  }

  if (error && !data) {
    return (
      <p className="text-vp-base" style={{ color: 'var(--vp-state-crashed)' }}>
        {safeText(error)}
      </p>
    )
  }
  if (!data) {
    return <p className="text-vp-base text-ink-3">{t('chat.loading')}</p>
  }
  if (!data.available) {
    return (
      <p data-testid="chat-unavailable" className="text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
        {t('chat.unavailable')}
      </p>
    )
  }

  // A count beside a tab only where something is waiting on the owner: a
  // stranger's code to enter.
  const pending = data.peers.filter((p) => p.status === 'pending').length

  return (
    <div data-testid="chat" className="@container">
      <div className="mb-5">
        <div
          ref={tabsRef}
          role="tablist"
          aria-label={t('grp.chat')}
          data-marker={marker ? 'on' : 'off'}
          className="vp-segmented w-fit max-w-full"
        >
          {marker && (
            <span
              aria-hidden="true"
              className="vp-marker-2d"
              style={{
                width: marker.width,
                height: marker.height,
                transform: `translate3d(${marker.left}px, ${marker.top}px, 0)`,
              }}
            />
          )}
          {TABS.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={tab === id}
              data-active={tab === id}
              data-testid={`chat-tab-${id}`}
              onClick={() => choose(id)}
              // Padding inline, not px-: .vp-tab is unlayered and would win
              // over a utility, and the default leaves labels touching.
              style={{ paddingInline: '0.625rem' }}
              className="vp-tab shrink-0 gap-1.5 text-vp-base whitespace-nowrap"
            >
              <Icon size={13} className="shrink-0" aria-hidden="true" />
              {t(label)}
              {id === 'peers' && pending > 0 && (
                <span className="flex items-center gap-0.5 rounded-full bg-surface-2 px-1.5 text-vp-xs text-ink-2 tabular">
                  <Bell size={10} aria-hidden="true" />
                  {pending}
                </span>
              )}
            </button>
          ))}
        </div>
      </div>
      <div key={tab} role="tabpanel" data-slide={slide.dir} className="vp-pane-slide">
        {tab === 'channels' && <Channels data={data} onChange={reload} />}
        {tab === 'peers' && <Peers data={data} onChange={reload} />}
        {tab === 'routes' && <Routes data={data} onChange={reload} />}
        {tab === 'alerts' && <Alerts data={data} onChange={reload} />}
        {tab === 'assistant' && (
          <>
            <Assistant data={data} onChange={reload} />
            <Keys data={data} onChange={reload} />
          </>
        )}
        {tab === 'log' && <Log />}
      </div>
    </div>
  )
}
