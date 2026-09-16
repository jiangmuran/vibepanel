import { useState } from 'react'
import { Ban, Check, Pencil, Trash2, UserCheck } from 'lucide-react'

import { api } from '../../protocol/api'
import type { ChatPeer, ChatPeerMode, ChatSettings } from '../../protocol/wire'
import { t } from '../../i18n'
import { askConfirm } from '../ask'
import { showToast } from '../toasts'
import { safeText } from '../text'
import { Card, Section } from './Chat'
import { codeDigits } from './code'
import { chatError } from './errors'
import { INPUT_SHORT, Primary, SELECT, Secondary } from './form'

/**
 * Who may talk to the panel from a chat app.
 *
 * Nobody, until the owner says so: a stranger who messages the bot gets a
 * six-digit code and a row here, and typing the code the person reads out is
 * what turns the row into a person the panel pushes to and listens to.
 *
 * The code is the only way in, and it is not shown on the row. A pending row
 * says who wrote and when; a "pair" button beside it paired whoever had
 * written most recently, which on a bot anyone can find is a stranger as
 * often as it is the owner's own phone. Unblocking is removing the row for
 * the same reason: "unblock" that set paired let a stranger blocked while
 * pending in with two clicks. The server refuses both; the page does not
 * offer them.
 *
 * The mode switch is where the advanced mode is turned on, per person, so
 * one paired phone can talk in sentences while another only gets commands.
 */

function since(unix: number): string {
  const s = Math.max(0, Math.floor(Date.now() / 1000) - unix)
  if (s < 60) return t('chat.justNow')
  if (s < 3600) return t('chat.minutesAgo', { n: String(Math.floor(s / 60)) })
  return t('chat.hoursAgo', { n: String(Math.floor(s / 3600)) })
}

function nameOf(p: ChatPeer): string {
  return p.display || p.peerId
}

export function Peers({ data, onChange }: { data: ChatSettings; onChange: () => void }) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [renaming, setRenaming] = useState<{ key: string; name: string } | null>(null)
  const labels = new Map(data.factories.map((f) => [f.kind, f.label]))
  const handles = new Map(data.sessions.map((s) => [s.id, s.handle]))

  const pair = async () => {
    setBusy(true)
    try {
      const p = await api.pairChat(codeDigits(code))
      setCode('')
      showToast({ kind: 'success', key: 'chat.pairedName', params: { name: safeText(nameOf(p)) } })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.pairFailed', detail: chatError(e) })
    } finally {
      setBusy(false)
    }
  }

  const patch = async (p: ChatPeer, body: { mode?: ChatPeerMode; status?: 'blocked'; display?: string }, done?: Parameters<typeof showToast>[0]) => {
    try {
      await api.patchChatPeer(p.channel, p.peerId, body)
      if (done) showToast(done)
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: chatError(e) })
    }
  }

  const block = async (p: ChatPeer) => {
    const ok = await askConfirm({
      title: t('chat.blockTitle', { name: safeText(nameOf(p)) }),
      body: t('chat.blockBody'),
      confirm: t('chat.block'),
      cancel: t('chat.cancel'),
      destructive: true,
    })
    if (ok) await patch(p, { status: 'blocked' }, { kind: 'success', key: 'chat.blockedName', params: { name: safeText(nameOf(p)) } })
  }

  const remove = async (p: ChatPeer, unblock: boolean) => {
    const ok = await askConfirm({
      title: t(unblock ? 'chat.unblockTitle' : 'chat.removePeerTitle', { name: safeText(nameOf(p)) }),
      body: t(unblock ? 'chat.unblockBody' : 'chat.removePeerBody'),
      confirm: unblock ? t('chat.unblock') : t('chat.remove'),
      cancel: t('chat.cancel'),
      destructive: !unblock,
    })
    if (!ok) return
    try {
      await api.removeChatPeer(p.channel, p.peerId)
      showToast({ kind: 'success', key: unblock ? 'chat.unblockedName' : 'chat.removedName', params: { name: safeText(nameOf(p)) } })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: chatError(e) })
    }
  }

  const rename = async (p: ChatPeer, name: string) => {
    setRenaming(null)
    if (name.trim() === p.display) return
    await patch(p, { display: name.trim() })
  }

  // Paired people first: they are who the page is mostly about. A stranger
  // waiting for their code is shown after them, and the code form is above
  // both.
  const rank = { paired: 0, pending: 1, blocked: 2 } as const
  const rows = [...data.peers].sort((a, b) => rank[a.status] - rank[b.status])

  // The bot's language governs every reply to every person, so it lives
  // with the people rather than with the advanced mode.
  const setLang = async (lang: 'zh' | 'en') => {
    try {
      await api.saveChatLang(lang)
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: chatError(e) })
    }
  }

  return (
    <Section id="peers" title={t('chat.peers')} lead={t('chat.peersLead')}>
      <Card testid="chat-peers">
        <form
          className="mb-1 flex flex-wrap items-center gap-2"
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
          <Primary type="submit" disabled={busy || codeDigits(code).length !== 6}>
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
        <p className="mb-3 text-vp-xs text-ink-3">{t('chat.pairScope')}</p>

        {rows.length === 0 ? (
          <p className="text-vp-sm text-ink-3">{t('chat.noPeers')}</p>
        ) : (
          <ul className="divide-y divide-hairline">
            {rows.map((p) => {
              const key = `${p.channel}:${p.peerId}`
              const focus = p.focusSession ? handles.get(p.focusSession) : undefined
              return (
                <li key={key} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2" data-testid={`chat-peer-${p.channel}-${p.peerId}`}>
                  <span className="min-w-0 flex-1 basis-40">
                    {renaming?.key === key ? (
                      <input
                        autoFocus
                        className={`${INPUT_SHORT} w-full`}
                        value={renaming.name}
                        maxLength={40}
                        aria-label={t('chat.rename')}
                        onChange={(e) => setRenaming({ key, name: e.target.value })}
                        onBlur={() => void rename(p, renaming.name)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter') void rename(p, renaming.name)
                          if (e.key === 'Escape') setRenaming(null)
                        }}
                        data-testid={`chat-peer-name-${p.peerId}`}
                      />
                    ) : (
                      <span className="flex min-w-0 items-center gap-1">
                        <span className="truncate text-vp-md text-ink">{safeText(nameOf(p))}</span>
                        <button
                          type="button"
                          className="vp-control shrink-0"
                          title={t('chat.rename')}
                          aria-label={t('chat.rename')}
                          onClick={() => setRenaming({ key, name: p.display })}
                        >
                          <Pencil size={12} />
                        </button>
                      </span>
                    )}
                    <span className="block truncate text-vp-xs text-ink-3">
                      {labels.get(p.channel) ?? p.channel}
                      {p.display ? ` · ${safeText(p.peerId)}` : ''}
                      {focus !== undefined ? ` · ${t('chat.focusOn', { n: String(focus) })}` : ''}
                    </span>
                  </span>
                  {p.status === 'pending' && (
                    <>
                      <span className="text-vp-sm text-ink-2" data-testid={`chat-pending-${p.peerId}`}>
                        {t('chat.pendingSince', { when: since(p.lastSeenAt) })}
                      </span>
                      <Secondary onClick={() => void block(p)}>
                        <Ban size={14} />
                        {t('chat.block')}
                      </Secondary>
                    </>
                  )}
                  {p.status === 'paired' && (
                    <>
                      <span className="flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-vp-xs text-ink-2">
                        <Check size={11} aria-hidden="true" style={{ color: 'var(--vp-state-done)' }} />
                        {t('chat.statusPaired')}
                      </span>
                      <select
                        className="rounded-vp border border-hairline bg-surface-2 px-1.5 py-1 text-vp-sm text-ink"
                        value={p.mode}
                        aria-label={t('chat.modeNormal') + ' / ' + t('chat.modeAdvanced')}
                        onChange={(e) => void patch(p, { mode: e.target.value as ChatPeerMode })}
                        data-testid={`chat-peer-mode-${p.peerId}`}
                      >
                        <option value="normal">{t('chat.modeNormal')}</option>
                        <option value="advanced">{t('chat.modeAdvanced')}</option>
                      </select>
                      <Secondary onClick={() => void block(p)}>
                        <Ban size={14} />
                        {t('chat.block')}
                      </Secondary>
                    </>
                  )}
                  {p.status === 'blocked' && (
                    <>
                      <span className="flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-vp-xs text-ink-2">
                        <Ban size={11} aria-hidden="true" style={{ color: 'var(--vp-state-crashed)' }} />
                        {t('chat.statusBlocked')}
                      </span>
                      <Secondary onClick={() => void remove(p, true)} data-testid={`chat-unblock-${p.peerId}`}>
                        {t('chat.unblock')}
                      </Secondary>
                    </>
                  )}
                  {p.status !== 'blocked' && (
                    <Secondary className="text-ink-2" title={t('chat.remove')} aria-label={t('chat.remove')} onClick={() => void remove(p, false)}>
                      <Trash2 size={14} />
                    </Secondary>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </Card>
    </Section>
  )
}
