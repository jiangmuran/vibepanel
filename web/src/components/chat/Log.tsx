import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { AuditEntry } from '../../protocol/wire'
import { getLang, t } from '../../i18n'
import type { Key } from '../../i18n'
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

/** The event, in words. An event this page does not know is shown as named. */
const EVENTS: Record<string, Key> = {
  'chat.in': 'chat.evIn',
  'chat.send': 'chat.evSend',
  'chat.approved': 'chat.evApproved',
  'chat.denied': 'chat.evDenied',
  'chat.interrupt': 'chat.evInterrupt',
  'chat.image': 'chat.evImage',
  'chat.undelivered': 'chat.evUndelivered',
  'chat.refused': 'chat.evRefused',
  'chat.paired': 'chat.evPaired',
  'chat.peer': 'chat.evPeer',
  'chat.stranger': 'chat.evStranger',
  'chat.channel': 'chat.evChannel',
  'chat.intent': 'chat.evIntent',
  'chat.ask': 'chat.evAsk',
}

/**
 * The server writes the audit log in English, once, for every surface that
 * reads it. The words a person meets in it here are swapped for the page's.
 */
const ZH_DETAIL: [RegExp, string][] = [
  [/ via only-waiting:/g, ' · 唯一在等的会话：'],
  [/ via focus:/g, ' · 默认会话：'],
  [/ via quote:/g, ' · 引用：'],
  [/ via button:/g, ' · 按钮：'],
  [/ via assistant:/g, ' · 助手：'],
  [/: removed$/, '：删掉'],
  [/ via handle:/g, ' · 回了编号：'],
  [/^(telegram|feishu|weixin) removed$/, '$1 已删除'],
  [/^(telegram|feishu|weixin) signed in$/, '$1 已登录'],
  [/^(telegram|feishu|weixin) enabled=true$/, '$1 启用'],
  [/^(telegram|feishu|weixin) enabled=false$/, '$1 关闭'],
  [/\(telegram\)/g, '（Telegram）'],
  [/\(feishu\)/g, '（飞书）'],
  [/\(weixin\)/g, '（微信）'],
  [/^telegram /, 'Telegram '],
  [/^feishu /, '飞书 '],
  [/^weixin /, '微信 '],
  [/）: /g, '）：'],
  [/: until they write$/, '：等对方先发消息'],
]

/** Status words, only in the events that are about a status: a message a
 *  person sent may say "paired" and is shown as they said it. */
const ZH_STATUS: [RegExp, string][] = [
  [/ -> /g, ' → '],
  [/\bpending\b/g, '待配对'],
  [/\bpaired\b/g, '已配对'],
  [/\bblocked\b/g, '已拉黑'],
]

function detailFor(event: string, detail: string): string {
  if (getLang() !== 'zh') return detail
  // Only the tail the server wrote: in "who (channel): words" the words are
  // the person's, and are left alone.
  const rules = event === 'chat.peer' ? [...ZH_DETAIL, ...ZH_STATUS] : event === 'chat.in' ? [] : ZH_DETAIL
  return rules.reduce((s, [re, to]) => s.replace(re, to), detail)
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
                  {when(e.at)} · {EVENTS[e.event] ? t(EVENTS[e.event]) : e.event.replace(/^chat\./, '')}
                </div>
                <div className="text-vp-sm break-all text-ink">{safeText(detailFor(e.event, e.detail))}</div>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </Section>
  )
}
