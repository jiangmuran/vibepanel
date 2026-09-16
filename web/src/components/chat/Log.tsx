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
          <div className="max-h-80 overflow-auto">
            <table className="w-full text-vp-sm">
              <tbody>
                {rows.map((e, i) => (
                  <tr key={i} className="border-t border-hairline first:border-t-0">
                    <td className="whitespace-nowrap py-1 pr-3 align-top font-mono text-vp-xs text-ink-3">
                      {new Date(e.at * 1000).toLocaleString()}
                    </td>
                    <td className="whitespace-nowrap py-1 pr-3 align-top font-mono text-vp-xs text-ink-2">{e.event}</td>
                    <td className="py-1 align-top break-words text-ink">{safeText(e.detail)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </Section>
  )
}
