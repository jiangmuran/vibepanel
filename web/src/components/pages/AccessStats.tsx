import { useCallback, useEffect, useState } from 'react'
import { Activity, Check, Copy, RefreshCw, ShieldAlert, TriangleAlert, X } from 'lucide-react'

import { copyTextInGesture } from '../../clipboard'
import { t, useLang, type Key } from '../../i18n'
import { api } from '../../protocol/api'
import type { ShareAccessStats } from '../../protocol/wire'
import { safeText } from '../text'
import { BlockTitle, Chip, Empty } from './bits'

function eventLabel(kind: string): Key {
  switch (kind) {
    case 'visit':
      return 'share.event.visit'
    case 'unlock_ok':
      return 'share.event.unlockOk'
    case 'unlock_fail':
      return 'share.event.unlockFail'
    case 'action':
      return 'share.event.action'
    default:
      return 'share.event.visit'
  }
}

function formatWhen(unix: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toLocaleString()
}

export function AccessStatsPanel({
  linkId,
  onError,
  onClose,
}: {
  linkId: string
  onError: (msg: string) => void
  onClose: () => void
}) {
  useLang()
  const [stats, setStats] = useState<ShareAccessStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [tab, setTab] = useState<'ips' | 'events'>('ips')
  const [copiedIp, setCopiedIp] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const next = await api.shareAccessStats(linkId)
      setStats(next)
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [linkId, onError])

  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 0)
    return () => clearTimeout(timer)
  }, [load])

  const copyIp = (ip: string) => {
    copyTextInGesture(ip, (ok) => {
      if (ok) {
        setCopiedIp(ip)
        window.setTimeout(() => setCopiedIp((cur) => (cur === ip ? '' : cur)), 1500)
      }
    })
  }

  return (
    <div
      data-testid="share-access-panel"
      className="vp-panel-in mt-2.5 flex flex-col gap-3 rounded-vp border border-hairline border-l-2 border-l-accent bg-surface-2 p-3 @md:ml-8"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Activity size={13} className="shrink-0 text-ink-3" />
          <BlockTitle>{t('share.statsTitle')}</BlockTitle>
        </div>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            disabled={loading}
            onClick={() => void load()}
            data-testid="share-stats-refresh"
            className="vp-outline text-vp-xs"
          >
            <RefreshCw size={11} className={loading ? 'animate-spin' : ''} />
            {t('share.statsRefresh')}
          </button>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('share.editDone')}
            data-testid="share-stats-close"
            className="vp-control"
          >
            <X size={13} />
          </button>
        </div>
      </div>

      {stats && (
        <>
          <div className="grid grid-cols-2 gap-2 @xl:grid-cols-4">
            <div className="rounded-vp border border-hairline bg-surface px-2.5 py-2">
              <div className="text-vp-xs text-ink-3">{t('share.statsUniqueIps')}</div>
              <div className="tabular mt-0.5 text-vp-lg font-semibold text-ink" data-testid="share-stats-unique-ips">
                {stats.uniqueIps}
              </div>
            </div>
            <div className="rounded-vp border border-hairline bg-surface px-2.5 py-2">
              <div className="text-vp-xs text-ink-3">{t('share.statsVisits')}</div>
              <div className="tabular mt-0.5 text-vp-lg font-semibold text-ink" data-testid="share-stats-visits">
                {stats.totalVisits}
              </div>
            </div>
            <div className="rounded-vp border border-hairline bg-surface px-2.5 py-2">
              <div className="text-vp-xs text-ink-3">{t('share.statsUnlockOk')}</div>
              <div className="tabular mt-0.5 text-vp-lg font-semibold text-ink">{stats.totalUnlockOk}</div>
            </div>
            <div className="rounded-vp border border-hairline bg-surface px-2.5 py-2">
              <div className="text-vp-xs text-ink-3">{t('share.statsUnlockFail')}</div>
              <div
                className="tabular mt-0.5 text-vp-lg font-semibold"
                style={{ color: stats.totalUnlockFail > 0 ? 'var(--vp-state-waiting)' : 'var(--vp-ink)' }}
                data-testid="share-stats-unlock-fail"
              >
                {stats.totalUnlockFail}
              </div>
            </div>
          </div>

          {stats.loopbackOnly && (
            <p
              data-testid="share-stats-loopback-warn"
              className="flex items-start gap-1.5 rounded-vp border border-hairline bg-surface px-2.5 py-2 text-vp-xs"
              style={{ color: 'var(--vp-state-waiting)' }}
            >
              <TriangleAlert size={13} className="mt-0.5 shrink-0" />
              <span>{t('sharing.loopbackWarning')}</span>
            </p>
          )}

          <div className="flex items-center gap-1 border-b border-hairline pb-2">
            <button
              type="button"
              data-testid="share-stats-tab-ips"
              onClick={() => setTab('ips')}
              className={tab === 'ips' ? 'vp-primary text-vp-xs' : 'vp-outline text-vp-xs'}
            >
              {t('share.statsTabIps', { n: stats.ips.length })}
            </button>
            <button
              type="button"
              data-testid="share-stats-tab-events"
              onClick={() => setTab('events')}
              className={tab === 'events' ? 'vp-primary text-vp-xs' : 'vp-outline text-vp-xs'}
            >
              {t('share.statsTabEvents', { n: stats.events.length })}
            </button>
          </div>

          {tab === 'ips' ? (
            stats.ips.length === 0 ? (
              <Empty icon={Activity} testid="share-stats-empty">
                {t('share.statsEmpty')}
              </Empty>
            ) : (
              <div className="overflow-x-auto rounded-vp border border-hairline bg-surface">
                <table className="w-full border-collapse text-left text-vp-xs">
                  <thead>
                    <tr className="border-b border-hairline text-ink-3">
                      <th className="px-2.5 py-1.5 font-medium">{t('share.statsColIp')}</th>
                      <th className="px-2.5 py-1.5 font-medium">{t('share.statsColVisits')}</th>
                      <th className="px-2.5 py-1.5 font-medium">{t('share.statsColUnlocks')}</th>
                      <th className="px-2.5 py-1.5 font-medium">{t('share.statsColClient')}</th>
                      <th className="px-2.5 py-1.5 font-medium">{t('share.statsColLastSeen')}</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-hairline text-ink-2">
                    {stats.ips.map((row) => (
                      <tr key={row.ip} data-testid="share-stats-ip-row">
                        <td className="px-2.5 py-1.5 font-mono text-ink whitespace-nowrap">
                          <span className="inline-flex items-center gap-1">
                            {safeText(row.ip)}
                            <button
                              type="button"
                              onClick={() => copyIp(row.ip)}
                              title={t('tok.copy')}
                              aria-label={t('tok.copy')}
                              className="vp-control"
                            >
                              {copiedIp === row.ip ? <Check size={11} /> : <Copy size={11} />}
                            </button>
                          </span>
                        </td>
                        <td className="tabular px-2.5 py-1.5">{row.visitCount}</td>
                        <td className="tabular px-2.5 py-1.5 whitespace-nowrap">
                          <span>{row.unlockOkCount}</span>
                          {row.unlockFailCount > 0 && (
                            <span
                              className="ml-1.5 inline-flex items-center gap-0.5"
                              style={{ color: 'var(--vp-state-waiting)' }}
                            >
                              <ShieldAlert size={11} />
                              {row.unlockFailCount}
                            </span>
                          )}
                        </td>
                        <td className="px-2.5 py-1.5 text-ink-3">{row.lastUa ? safeText(row.lastUa) : '—'}</td>
                        <td className="tabular px-2.5 py-1.5 text-ink-3 whitespace-nowrap">
                          {formatWhen(row.lastSeenAt)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          ) : stats.events.length === 0 ? (
            <Empty icon={Activity} testid="share-stats-events-empty">
              {t('share.statsEmpty')}
            </Empty>
          ) : (
            <ul className="divide-y divide-hairline rounded-vp border border-hairline bg-surface text-vp-xs">
              {stats.events.map((ev) => (
                <li
                  key={ev.id}
                  data-testid="share-stats-event-row"
                  className="flex flex-wrap items-center gap-x-2 gap-y-1 px-2.5 py-1.5"
                >
                  <span className="tabular shrink-0 text-ink-3">{formatWhen(ev.at)}</span>
                  {ev.event === 'unlock_fail' ? (
                    <Chip icon={TriangleAlert} tone="warn">
                      {t(eventLabel(ev.event))}
                    </Chip>
                  ) : ev.event === 'unlock_ok' ? (
                    <Chip tone="accent">{t(eventLabel(ev.event))}</Chip>
                  ) : (
                    <Chip>{t(eventLabel(ev.event))}</Chip>
                  )}
                  <code className="font-mono text-ink">{safeText(ev.ip)}</code>
                  {ev.ua && <span className="text-ink-3">{safeText(ev.ua)}</span>}
                  {ev.detail && <span className="text-ink-3">{safeText(ev.detail)}</span>}
                </li>
              ))}
            </ul>
          )}
        </>
      )}

      {!stats && loading && <p className="text-vp-xs text-ink-3">{t('share.statsLoading')}</p>}
    </div>
  )
}
