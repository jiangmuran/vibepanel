import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { ChatAssistantConfig, ChatSettings, LaunchProfile } from '../../protocol/wire'
import { t } from '../../i18n'
import { showToast } from '../toasts'
import { safeText } from '../text'
import { Card, Section } from './Chat'
import { INPUT, Primary, SELECT, errText, useServerCopy } from './form'

/**
 * The advanced mode: a headless agent that reads a sentence and decides
 * which session it was for, or answers a question with read-only tools.
 *
 * The launch profile is the same object sessions start with, so an API key
 * or a gateway URL is typed once, in the place that already hides it. What
 * is shown here is the spend, because a budget nobody can see is a budget
 * nobody trusts.
 */
export function Assistant({ data, onChange }: { data: ChatSettings; onChange: () => void }) {
  const [dirty, setDirty] = useState(false)
  const [cfg, setCfg] = useServerCopy<ChatAssistantConfig>(data.assistant, dirty)
  const [busy, setBusy] = useState(false)
  const [profiles, setProfiles] = useState<LaunchProfile[]>([])

  useEffect(() => {
    api.launchProfiles().then(setProfiles, () => {})
  }, [])

  const set = (patch: Partial<ChatAssistantConfig>) => {
    setCfg((c) => ({ ...c, ...patch }))
    setDirty(true)
  }

  const save = async () => {
    setBusy(true)
    try {
      setCfg(await api.saveChatAssistant(cfg))
      setDirty(false)
      showToast({ kind: 'success', key: 'chat.saved' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section id="assistant" title={t('chat.assistant')} lead={t('chat.assistantLead')}>
      <Card testid="chat-assistant">
        {!data.assistantAvailable && (
          <p className="mb-3 text-vp-sm" style={{ color: 'var(--vp-state-waiting)' }}>
            {t('chat.assistantUnavailable')}
          </p>
        )}
        <div className="grid grid-cols-1 gap-3 @2xl:grid-cols-2">
          <label className="flex items-center gap-2 text-vp-md text-ink @2xl:col-span-2">
            <input
              type="checkbox"
              checked={cfg.enabled}
              disabled={!data.assistantAvailable}
              onChange={(e) => set({ enabled: e.target.checked })}
              data-testid="chat-assistant-enabled"
            />
            {t('chat.assistantEnabled')}
          </label>
          <label className="min-w-0">
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.harness')}</span>
            <select className={`${SELECT} w-full`} value={cfg.harness} onChange={(e) => set({ harness: e.target.value as 'claude' | 'codex' })}>
              <option value="claude">Claude Code</option>
              <option value="codex">Codex</option>
            </select>
          </label>
          <label className="min-w-0">
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.model')}</span>
            <input className={`${INPUT} font-mono`} value={cfg.model} placeholder={t('chat.modelHint')} onChange={(e) => set({ model: e.target.value })} />
          </label>
          <label className="min-w-0">
            <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.profile')}</span>
            <select className={`${SELECT} w-full`} value={cfg.profileId} onChange={(e) => set({ profileId: e.target.value })}>
              <option value="">{t('chat.profileNone')}</option>
              {profiles.map((p) => (
                <option key={p.id} value={p.id}>
                  {safeText(p.name)}
                </option>
              ))}
            </select>
          </label>
          <div className="grid grid-cols-3 gap-2">
            <label className="min-w-0">
              <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.maxTurns')}</span>
              <input className={INPUT} type="number" min={1} max={50} value={cfg.maxTurns} onChange={(e) => set({ maxTurns: Number(e.target.value) || 0 })} />
            </label>
            <label className="min-w-0">
              <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.budget')}</span>
              <input className={INPUT} type="number" min={0} step={0.5} value={cfg.budgetUsd} onChange={(e) => set({ budgetUsd: Number(e.target.value) || 0 })} />
            </label>
            <label className="min-w-0">
              <span className="mb-1 block text-vp-xs text-ink-3">{t('chat.timeout')}</span>
              <input className={INPUT} type="number" min={10} max={900} value={cfg.timeoutSeconds} onChange={(e) => set({ timeoutSeconds: Number(e.target.value) || 0 })} />
            </label>
          </div>
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <Primary disabled={busy || !dirty} onClick={() => void save()} data-testid="chat-save-assistant">
            {t('chat.save')}
          </Primary>
          <span className="text-vp-sm text-ink-2">
            {t('chat.spendToday', { usd: data.spendToday.toFixed(2), n: String(data.callsToday) })}
          </span>
        </div>
      </Card>
    </Section>
  )
}
