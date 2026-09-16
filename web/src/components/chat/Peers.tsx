import { useState } from 'react'
import { Ban, Check, Trash2, UserCheck } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ChatPeer, ChatPeerMode, ChatPeerStatus, ChatSettings } from '../../protocol/wire'
import { t } from '../../i18n'
import { askConfirm } from '../ask'
import { showToast } from '../toasts'
import { safeText } from '../text'
import { Card, Section } from './Chat'
import { INPUT_SHORT, Primary, SELECT, Secondary, errText } from './form'

/**
 * Who may talk to the panel from a chat app.
 *
 * Nobody, until the owner says so: a stranger who messages the bot gets a
 * six-digit code and a row here, and typing the code (or pressing pair)
 * is what turns the row into a person the panel pushes to and listens to.
 * The mode switch is where the advanced mode is turned on, per person, so
 * one paired phone can talk in sentences while another only gets commands.
 */
export function Peers({ data, onChange }: { data: ChatSettings; onChange: () => void }) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const labels = new Map(data.factories.map((f) => [f.kind, f.label]))

  const pair = async () => {
    setBusy(true)
    try {
      await api.pairChat(code.trim())
      setCode('')
      showToast({ kind: 'success', key: 'chat.pairedOk' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.pairFailed', detail: errText(e) })
    } finally {
      setBusy(false)
    }
  }

  const patch = async (p: ChatPeer, body: { mode?: ChatPeerMode; status?: ChatPeerStatus }) => {
    try {
      await api.patchChatPeer(p.channel, p.peerId, body)
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    }
  }

  const remove = async (p: ChatPeer) => {
    const ok = await askConfirm({
      title: t('chat.removePeerTitle', { name: safeText(p.display || p.peerId) }),
      confirm: t('chat.remove'),
      cancel: t('chat.cancel'),
      destructive: true,
    })
    if (!ok) return
    try {
      await api.removeChatPeer(p.channel, p.peerId)
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    }
  }

  const pending = data.peers.filter((p) => p.status === 'pending')
  const others = data.peers.filter((p) => p.status !== 'pending')

  // The bot's language governs every reply to every person, so it lives
  // with the people rather than with the advanced mode.
  const setLang = async (lang: 'zh' | 'en') => {
    try {
      await api.saveChatLang(lang)
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    }
  }

  return (
    <Section id="peers" title={t('chat.peers')} lead={t('chat.peersLead')}>
      <Card testid="chat-peers">
        <form
          className="mb-3 flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void pair()
          }}
        >
          <label htmlFor="chat-pair-code" className="text-vp-sm text-ink-2">
            {t('chat.pairingCode')}
          </label>
          <input
            id="chat-pair-code"
            className={`${INPUT_SHORT} w-36 font-mono`}
            inputMode="numeric"
            placeholder="123456"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            data-testid="chat-pair-code"
          />
          <Primary type="submit" disabled={busy || code.trim().length !== 6}>
            <UserCheck size={14} />
            {t('chat.pair')}
          </Primary>
          <label className="ml-auto flex items-center gap-1 text-vp-sm text-ink-2">
            {t('chat.botLang')}
            <select className={SELECT} value={data.lang} onChange={(e) => void setLang(e.target.value as 'zh' | 'en')} data-testid="chat-lang">
              <option value="zh">{t('chat.langZh')}</option>
              <option value="en">{t('chat.langEn')}</option>
            </select>
          </label>
        </form>

        {data.peers.length === 0 ? (
          <p className="text-vp-sm text-ink-3">{t('chat.noPeers')}</p>
        ) : (
          <ul className="divide-y divide-hairline">
            {[...pending, ...others].map((p) => (
              <li
                key={`${p.channel}:${p.peerId}`}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2"
                data-testid={`chat-peer-${p.channel}-${p.peerId}`}
              >
                <span className="min-w-0 flex-1 basis-40">
                  <span className="block truncate text-vp-md text-ink">{safeText(p.display || p.peerId)}</span>
                  <span className="block truncate text-vp-xs text-ink-3">
                    {labels.get(p.channel) ?? p.channel} · {safeText(p.peerId)}
                  </span>
                </span>
                {p.status === 'pending' ? (
                  <>
                    <span className="font-mono text-vp-md text-ink" title={t('chat.pairingCode')}>
                      {p.pairingCode}
                    </span>
                    <Primary onClick={() => void patch(p, { status: 'paired' })}>
                      <Check size={14} />
                      {t('chat.pair')}
                    </Primary>
                    <Secondary onClick={() => void patch(p, { status: 'blocked' })}>
                      <Ban size={14} />
                      {t('chat.block')}
                    </Secondary>
                  </>
                ) : (
                  <>
                    <span
                      className="rounded-full px-2 py-0.5 text-vp-xs"
                      style={{
                        color: p.status === 'paired' ? 'var(--vp-state-done)' : 'var(--vp-state-crashed)',
                        background: 'var(--vp-surface-2)',
                      }}
                    >
                      {p.status === 'paired' ? t('chat.statusPaired') : t('chat.statusBlocked')}
                    </span>
                    {p.status === 'paired' ? (
                      <label className="flex items-center gap-1 text-vp-sm text-ink-2">
                        <select
                          className="rounded-vp border border-hairline bg-surface-2 px-1.5 py-1 text-vp-sm text-ink"
                          value={p.mode}
                          onChange={(e) => void patch(p, { mode: e.target.value as ChatPeerMode })}
                          data-testid={`chat-peer-mode-${p.peerId}`}
                        >
                          <option value="normal">{t('chat.modeNormal')}</option>
                          <option value="advanced">{t('chat.modeAdvanced')}</option>
                        </select>
                      </label>
                    ) : null}
                    {p.status === 'paired' ? (
                      <Secondary onClick={() => void patch(p, { status: 'blocked' })}>
                        <Ban size={14} />
                        {t('chat.block')}
                      </Secondary>
                    ) : (
                      <Secondary onClick={() => void patch(p, { status: 'paired' })}>
                        <Check size={14} />
                        {t('chat.unblock')}
                      </Secondary>
                    )}
                  </>
                )}
                <Secondary className="text-ink-2" title={t('chat.remove')} aria-label={t('chat.remove')} onClick={() => void remove(p)}>
                  <Trash2 size={14} />
                </Secondary>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </Section>
  )
}
