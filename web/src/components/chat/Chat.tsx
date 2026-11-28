import { useCallback, useEffect, useState } from 'react'
import { Bell, Bot, LayoutGrid, Gauge, ScrollText, Users, Waypoints } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ChatSettings } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import type { Key } from '../../i18n'
import { safeText } from '../text'
import { ChannelIcon } from './ChannelIcon'
import './chat.css'
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
    <div data-testid={testid} className={`rounded-vp border border-hairline bg-surface p-4 @xl:p-5 ${className}`}>
      {children}
    </div>
  )
}

/**
 * Keep management destinations separate from the adapter list. With all
 * credentials inline, configuring one adapter meant scanning unrelated forms;
 * each adapter now has its own address and the overview only shows health.
 */
const TABS: { id: string; label: Key; icon: LucideIcon }[] = [
  { id: 'channels', label: 'chat.overview', icon: LayoutGrid },
  { id: 'peers', label: 'chat.peers', icon: Users },
  { id: 'routes', label: 'chat.routes', icon: Waypoints },
  { id: 'alerts', label: 'chat.alertsTab', icon: Gauge },
  { id: 'assistant', label: 'chat.assistant', icon: Bot },
  { id: 'log', label: 'chat.log', icon: ScrollText },
]

function tabFromHash(): string {
  const id = location.hash.replace(/^#/, '')
  return TABS.some((x) => x.id === id.split('/')[0]) ? id : 'channels'
}

/**
 * Everything on the page comes from one GET, polled while the page is open,
 * and every section writes through its own PUT and then asks for a fresh
 * read. No section keeps a copy of another's data, which is what makes a
 * peer paired in one tab show up as a destination in the next.
 */
export function Chat() {
  useLang()
  const [data, setData] = useState<ChatSettings | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState(tabFromHash)
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

  const section = tab.split('/')[0]
  const channelKind = section === 'channels' ? tab.split('/')[1] : undefined
  const factory = data.factories.find((f) => f.kind === channelKind)
  const currentTitle = factory?.label ?? t(TABS.find((x) => x.id === section)?.label ?? 'chat.overview')

  return (
    <div data-testid="chat" className="chat-layout">
      <nav className="chat-nav" aria-label={t('grp.chat')}>
        <button type="button" className="chat-nav-item" data-active={section === 'channels' && !factory}
          data-testid="chat-tab-channels" onClick={() => choose('channels')}>
          <LayoutGrid size={18} aria-hidden="true" />{t('chat.overview')}
        </button>
        <p className="chat-nav-label">{t('chat.channels')}</p>
        {data.factories.map((f) => (
          <button key={f.kind} type="button" className="chat-nav-item"
            data-active={channelKind === f.kind} aria-current={channelKind === f.kind ? 'page' : undefined}
            data-testid={`chat-nav-${f.kind}`} onClick={() => choose(`channels/${f.kind}`)}>
            <ChannelIcon kind={f.kind} size={19} />{f.label}
          </button>
        ))}
        <p className="chat-nav-label">{t('chat.manage')}</p>
        {TABS.filter((x) => x.id !== 'channels').map(({ id, label, icon: Icon }) => (
          <button key={id} type="button" className="chat-nav-item" data-active={section === id}
            aria-current={section === id ? 'page' : undefined}
            data-testid={`chat-tab-${id}`} onClick={() => choose(id)}>
            <Icon size={18} aria-hidden="true" />{t(label)}
            {id === 'peers' && pending > 0 && (
              <span className="chat-nav-count"><Bell size={11} aria-hidden="true" />{pending}</span>
            )}
          </button>
        ))}
      </nav>
      <div className="chat-content @container">
        <div className={`chat-heading ${factory ? 'chat-heading-detail' : ''}`}>
          <h1>{currentTitle}</h1>
          {section === 'channels' && <p>{factory ? (['feishu', 'telegram', 'weixin'].includes(factory.kind) ? t(`chat.description.${factory.kind}` as Key) : t('chat.channelsLead')) : t('chat.intro')}</p>}
        </div>
        {error && <p role="status" className="mb-4 text-vp-sm text-danger-ink">{safeText(error)}</p>}
        {section === 'channels' && <Channels data={data} selected={factory?.kind} onSelect={(kind) => choose(`channels/${kind}`)} onChange={reload} />}
        {section === 'peers' && <Peers data={data} onChange={reload} />}
        {section === 'routes' && <Routes data={data} onChange={reload} />}
        {section === 'alerts' && <Alerts data={data} onChange={reload} />}
        {section === 'assistant' && <><Assistant data={data} onChange={reload} /><Keys data={data} onChange={reload} /></>}
        {section === 'log' && <Log />}
      </div>
    </div>
  )
}
