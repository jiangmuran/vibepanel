import { useCallback, useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { ChatSettings } from '../../protocol/wire'
import { t, useLang } from '../../i18n'
import { safeText } from '../text'
import { Assistant } from './Assistant'
import { Channels } from './Channels'
import { Keys } from './Keys'
import { Log } from './Log'
import { Peers } from './Peers'
import { Routes } from './Routes'

/** How often the page re-reads: for the health lines and a stranger's code. */
const POLL_MS = 4000

export const INPUT =
  'w-full min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** INPUT without the full width, for a code or a number beside a button. */
export const INPUT_SHORT =
  'min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

export const SELECT =
  'min-w-0 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent'

/** One block of the page, with its heading and a line saying what it is for. */
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
  return (
    <section data-section={id} className="mb-8 @container">
      <h2 className="mb-1 text-vp-lg font-semibold tracking-tight text-ink">{title}</h2>
      {lead && <p className="mb-3 max-w-3xl text-vp-sm leading-relaxed text-ink-2">{lead}</p>}
      {children}
    </section>
  )
}

export function Card({ children, testid }: { children: React.ReactNode; testid?: string }) {
  return (
    <div data-testid={testid} className="rounded-vp border border-hairline bg-surface p-3 sm:p-4">
      {children}
    </div>
  )
}

/**
 * Everything on the page comes from one GET, polled while the page is open,
 * and every section writes through its own PUT and then asks for a fresh
 * read. No section keeps a copy of another's data, which is what makes a
 * peer paired in one block show up as a destination in the next.
 */
export function Chat() {
  useLang()
  const [data, setData] = useState<ChatSettings | null>(null)
  const [error, setError] = useState<string | null>(null)

  const reload = useCallback(() => {
    api.chat().then(
      (d) => {
        setData(d)
        setError(null)
      },
      (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
    )
  }, [])

  useEffect(() => {
    let cancelled = false
    const load = () =>
      api.chat().then(
        (d) => {
          if (cancelled) return
          setData(d)
          setError(null)
        },
        (e: unknown) => {
          if (!cancelled) setError(e instanceof Error ? e.message : String(e))
        },
      )
    void load()
    const timer = window.setInterval(() => void load(), POLL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

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

  return (
    <div data-testid="chat" className="@container">
      <Channels data={data} onChange={reload} />
      <Peers data={data} onChange={reload} />
      <Routes data={data} onChange={reload} />
      <Assistant data={data} onChange={reload} />
      <Keys data={data} onChange={reload} />
      <Log />
    </div>
  )
}
