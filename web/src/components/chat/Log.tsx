import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { AuditEntry } from '../../protocol/wire'
import { t } from '../../i18n'
import { safeText } from '../text'
import { Card, Section } from './Chat'

/**
 * What came in and what went into a pane, from the audit log.
 *
 * The same table the account's activity list reads, narrowed to the chat's
 * own events, because a second log would be a second place to forget to
 * write. What matters here is that every keystroke a phone caused is a row
 * with who sent it and which handle it went to.
 */
function when(unix: number): string {
  const d = new Date(unix * 1000)
  const today = new Date()
  const sameDay = d.toDateString() === today.toDateString()
  return sameDay ? d.toLocaleTimeString() : d.toLocaleString()
}

export function Log() {
  const [rows, setRows] = useState<AuditEntry[]>([])
  useEffect(() => {
    let cancelled = false
    const load = () => api.chatLog(100).then((r) => !cancelled && setRows(r), () => {})
    void load()
    const timer = window.setInterval(() => void load(), 5000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])
  return (
    <Section id="log" title={t('chat.log')} lead={t('chat.logLead')}>
      <Card testid="chat-log">
        {rows.length === 0 ? (
          <p className="text-vp-sm text-ink-3">{t('chat.logEmpty')}</p>
        ) : (
          <ul className="max-h-80 divide-y divide-hairline overflow-auto">
            {rows.map((e, i) => (
              <li key={i} className="py-1.5">
                {/* Time and event on one line, the detail on the next: three
                    columns left sixty pixels for the detail on a phone. The
                    date is the day's when it is today's, and the prefix
                    every event shares says nothing. */}
                <div className="font-mono text-vp-xs text-ink-3">
                  {when(e.at)} · {e.event.replace(/^chat\./, '')}
                </div>
                <div className="text-vp-sm break-all text-ink">{safeText(e.detail)}</div>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </Section>
  )
}
