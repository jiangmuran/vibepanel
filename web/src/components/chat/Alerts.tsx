import { useEffect, useState } from 'react'
import { Check } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ChatAlerts, ChatSettings, SystemSample } from '../../protocol/wire'
import { t } from '../../i18n'
import { showToast } from '../toasts'
import { safeText } from '../text'
import { Card, Section } from './Chat'
import { chatError } from './errors'
import { INPUT, Primary, useServerCopy } from './form'

function percent(used: number, total: number): string {
  return total > 0 ? `${Math.round((used / total) * 100)}%` : '—'
}

/**
 * When the machine is worth a message on the phone.
 *
 * The thresholds and who is told, and the machine's numbers right now beside
 * them, so a threshold is set against what the machine actually reads rather
 * than a guess. "系统" in a chat answers the same numbers on demand.
 */
export function Alerts({ data, onChange }: { data: ChatSettings; onChange: () => void }) {
  const [dirty, setDirty] = useState(false)
  const [cfg, setCfg] = useServerCopy<ChatAlerts>(data.alerts, dirty)
  const [busy, setBusy] = useState(false)
  const [now, setNow] = useState<SystemSample | null>(null)

  useEffect(() => {
    let cancelled = false
    const load = () => api.system().then((s) => !cancelled && setNow(s), () => {})
    void load()
    const timer = window.setInterval(() => void load(), 5000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

  const set = (patch: Partial<ChatAlerts>) => {
    setCfg((c) => ({ ...c, ...patch }))
    setDirty(true)
  }

  const save = async () => {
    setBusy(true)
    try {
      setCfg(await api.saveChatAlerts(cfg))
      setDirty(false)
      showToast({ kind: 'success', key: 'chat.saved' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: chatError(e) })
    } finally {
      setBusy(false)
    }
  }

  const labels = new Map(data.factories.map((f) => [f.kind, f.label]))
  const peers = data.peers
    .filter((p) => p.status === 'paired')
    .map((p) => ({ key: `${p.channel}:${p.peerId}`, label: `${labels.get(p.channel) ?? p.channel} · ${p.display || p.peerId}` }))
  const to = cfg.to ?? []
  const everyone = to.includes('*')
  const toggle = (key: string, on: boolean) => set({ to: on ? [...to, key] : to.filter((k) => k !== key) })

  const number = (key: 'cpuPercent' | 'cpuMinutes' | 'memPercent' | 'diskPercent', label: string, min: number, max: number) => (
    <label className="min-w-0">
      <span className="mb-1 block text-vp-xs text-ink-3">{label}</span>
      <input
        className={INPUT}
        type="number"
        min={min}
        max={max}
        value={cfg[key]}
        onChange={(e) => set({ [key]: Number(e.target.value) || 0 } as Partial<ChatAlerts>)}
        data-testid={`chat-alert-${key}`}
      />
    </label>
  )

  return (
    <Section id="alerts" title={t('chat.alerts')} lead={t('chat.alertsLead')}>
      <Card testid="chat-alerts">
        {!data.monitorAvailable && (
          <p className="mb-3 text-vp-sm" style={{ color: 'var(--vp-state-waiting)' }}>
            {t('chat.monitorUnavailable')}
          </p>
        )}
        {now && (
          <p className="mb-3 flex flex-wrap gap-x-4 gap-y-1 text-vp-sm text-ink-2 tabular" data-testid="chat-alert-now">
            <span>{t('chat.nowLabel')}</span>
            {now.cpuReadable && <span>CPU {now.cpuPercent === null ? '—' : `${Math.round(now.cpuPercent)}%`}</span>}
            {now.memTotal > 0 && <span>{t('chat.nowMem', { p: percent(now.memTotal - now.memAvailable, now.memTotal) })}</span>}
            {now.diskTotal > 0 && <span>{t('chat.nowDisk', { p: percent(now.diskTotal - now.diskFree, now.diskTotal) })}</span>}
          </p>
        )}
        <label className="mb-3 flex items-center gap-2 text-vp-md text-ink">
          <input type="checkbox" checked={cfg.enabled} onChange={(e) => set({ enabled: e.target.checked })} data-testid="chat-alerts-enabled" />
          {t('chat.alertsEnabled')}
        </label>
        <div className="grid grid-cols-2 gap-3 @2xl:grid-cols-4">
          {number('cpuPercent', t('chat.alertCpu'), 50, 100)}
          {number('cpuMinutes', t('chat.alertCpuMinutes'), 1, 120)}
          {number('memPercent', t('chat.alertMem'), 50, 100)}
          {number('diskPercent', t('chat.alertDisk'), 50, 100)}
        </div>
        <div className="mt-3">
          <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.to')}</span>
          <div className="flex flex-wrap gap-1">
            {[{ key: '*', label: t('chat.toEveryone') }, ...(everyone ? [] : peers)].map((p) => {
              const on = to.includes(p.key)
              return (
                <label
                  key={p.key}
                  className={`vp-press flex cursor-pointer items-center gap-1 rounded-vp border px-2 py-1 text-vp-sm has-focus-visible:ring-2 has-focus-visible:ring-accent ${on ? 'border-accent' : 'border-hairline text-ink-2'}`}
                  style={on ? { background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' } : undefined}
                >
                  <input type="checkbox" className="sr-only" checked={on} onChange={(e) => toggle(p.key, e.target.checked)} />
                  {on && <Check size={12} aria-hidden="true" />}
                  {safeText(p.label)}
                </label>
              )
            })}
          </div>
        </div>
        <p className="mt-3 text-vp-xs text-ink-3">{t('chat.alertsHint')}</p>
        <Primary className="mt-3" disabled={busy || !dirty} onClick={() => void save()} data-testid="chat-save-alerts">
          {t('chat.save')}
        </Primary>
      </Card>
    </Section>
  )
}
