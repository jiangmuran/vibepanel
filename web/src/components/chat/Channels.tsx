import { useEffect, useState } from 'react'
import { Check, Copy, Link2, LogIn, Send, Trash2 } from 'lucide-react'

import { copyTextInGesture } from '../../clipboard'
import { api } from '../../protocol/api'
import type { ChatChannel, ChatFactory, ChatLogin, ChatSettings } from '../../protocol/wire'
import { t } from '../../i18n'
import { askConfirm } from '../ask'
import { showToast } from '../toasts'
import { safeText } from '../text'
import { Card, INPUT, Section } from './Chat'
import { QR } from './QR'

function ago(unix: number): string {
  if (!unix) return t('chat.never')
  const s = Math.max(0, Math.floor(Date.now() / 1000) - unix)
  if (s < 60) return t('chat.justNow')
  if (s < 3600) return t('chat.minutesAgo', { n: String(Math.floor(s / 60)) })
  if (s < 86400) return t('chat.hoursAgo', { n: String(Math.floor(s / 3600)) })
  return t('chat.daysAgo', { n: String(Math.floor(s / 86400)) })
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/**
 * One card per adapter this build has.
 *
 * The health line is the point of the card. A token pasted wrong, a bot that
 * was never messaged, a webhook whose URL was never entered in the IM's
 * console: every one of them looks like a configured channel, and the only
 * thing that tells them apart from a working one is when something last
 * arrived. So the card says that, in words, always.
 */
export function Channels({ data, onChange }: { data: ChatSettings; onChange: () => void }) {
  return (
    <Section id="channels" title={t('chat.channels')} lead={t('chat.channelsLead')}>
      <div className="grid grid-cols-1 gap-3 @2xl:grid-cols-2 @5xl:grid-cols-3">
        {data.factories.map((f) => (
          <ChannelCard
            key={f.kind}
            factory={f}
            channel={data.channels.find((c) => c.kind === f.kind) ?? null}
            paired={data.peers.filter((p) => p.channel === f.kind && p.status === 'paired').length}
            onChange={onChange}
          />
        ))}
      </div>
    </Section>
  )
}

function HealthLine({ ch }: { ch: ChatChannel | null }) {
  if (!ch) return <p className="text-vp-sm text-ink-3">{t('chat.notSetUp')}</p>
  const h = ch.health
  const tone = !ch.enabled
    ? 'var(--vp-ink-3)'
    : h.lastError && h.lastErrorAt >= h.lastOk
      ? 'var(--vp-state-crashed)'
      : h.running
        ? 'var(--vp-state-done)'
        : 'var(--vp-state-waiting)'
  const word = !ch.enabled
    ? t('chat.off')
    : h.running
      ? t('chat.running')
      : t('chat.stopped')
  return (
    <div className="text-vp-sm text-ink-2" data-testid={`chat-health-${ch.kind}`}>
      <span className="font-medium" style={{ color: tone }}>
        {word}
      </span>
      <span className="text-ink-3"> · </span>
      <span>{t('chat.lastInbound', { when: ago(h.lastInbound) })}</span>
      {h.needsHello > 0 && (
        <>
          <span className="text-ink-3"> · </span>
          <span style={{ color: 'var(--vp-state-waiting)' }}>{t('chat.needsHello', { n: String(h.needsHello) })}</span>
        </>
      )}
      {h.lastError && h.lastErrorAt >= h.lastOk && (
        <p className="mt-1 break-words" style={{ color: 'var(--vp-state-crashed)' }}>
          {safeText(h.lastError)}
        </p>
      )}
    </div>
  )
}

function ChannelCard({
  factory,
  channel,
  paired,
  onChange,
}: {
  factory: ChatFactory
  channel: ChatChannel | null
  paired: number
  onChange: () => void
}) {
  const [values, setValues] = useState<Record<string, string>>({})
  const [enabled, setEnabled] = useState(channel?.enabled ?? true)
  const [busy, setBusy] = useState(false)
  const [login, setLogin] = useState<ChatLogin | null>(null)
  const [code, setCode] = useState('')
  const [copied, setCopied] = useState(false)

  // The stored non-secret values arrive with every poll; the form keeps what
  // the person typed and shows the server's copy for fields they have not
  // touched, so a poll does not erase a half-typed token. The enabled switch
  // follows the server until it is touched, during render (React's
  // "adjusting state when a prop changes").
  const serverEnabled = channel?.enabled ?? true
  const [seenEnabled, setSeenEnabled] = useState(serverEnabled)
  if (seenEnabled !== serverEnabled) {
    setSeenEnabled(serverEnabled)
    setEnabled(serverEnabled)
  }
  const shown = (name: string) => values[name] ?? channel?.values[name] ?? ''

  // The QR login: poll until it ends. Each status poll is one long request
  // to the IM held by the server, so the interval here is only what keeps
  // the picture fresh once the previous poll answered.
  useEffect(() => {
    if (!login || login.status === 'done' || login.status === 'expired' || login.status === 'failed') return
    let cancelled = false
    const tick = async () => {
      try {
        const next = await api.chatLoginStatus(factory.kind, login.id)
        if (cancelled) return
        setLogin(next)
        if (next.status === 'done') {
          showToast({ kind: 'success', key: 'chat.signedIn' })
          onChange()
        }
      } catch (e) {
        if (!cancelled) setLogin({ ...login, status: 'failed', error: errText(e) })
      }
    }
    const timer = window.setTimeout(() => void tick(), 1000)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [login, factory.kind, onChange])

  const save = async () => {
    setBusy(true)
    try {
      await api.saveChatChannel(factory.kind, enabled, values)
      setValues((v) => {
        const next = { ...v }
        for (const f of factory.fields) if (f.secret) delete next[f.name]
        return next
      })
      showToast({ kind: 'success', key: 'chat.saved' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    } finally {
      setBusy(false)
    }
  }

  const test = async () => {
    setBusy(true)
    try {
      const res = await api.testChatChannel(factory.kind)
      showToast(
        res.error
          ? { kind: 'error', key: 'chat.testFailed', detail: res.error }
          : { kind: 'success', key: 'chat.testOk', detail: String(res.sent) },
      )
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.testFailed', detail: errText(e) })
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    const ok = await askConfirm({
      title: t('chat.removeChannelTitle', { name: factory.label }),
      body: t('chat.removeChannelBody'),
      confirm: t('chat.remove'),
      cancel: t('chat.cancel'),
      destructive: true,
    })
    if (!ok) return
    try {
      await api.removeChatChannel(factory.kind)
      setValues({})
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    }
  }

  const startLogin = async () => {
    setBusy(true)
    try {
      setLogin(await api.startChatLogin(factory.kind))
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.loginFailed', detail: errText(e) })
    } finally {
      setBusy(false)
    }
  }

  const sendCode = async () => {
    if (!login) return
    try {
      await api.chatLoginCode(factory.kind, login.id, code)
      setCode('')
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.loginFailed', detail: errText(e) })
    }
  }

  const copy = () => {
    if (!channel?.webhookUrl) return
    // Through clipboard.ts, the one module that touches the clipboard: it
    // knows the http-origin case, where the URL stays on screen to select.
    copyTextInGesture(channel.webhookUrl, (ok) => {
      if (!ok) return
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    })
  }

  const signedIn = factory.login && channel?.configured
  return (
    <Card testid={`chat-channel-${factory.kind}`}>
      <div className="mb-2 flex items-center gap-2">
        <h3 className="text-vp-md font-semibold text-ink">{factory.label}</h3>
        {paired > 0 && (
          <span className="rounded-full bg-surface-2 px-2 py-0.5 text-vp-xs text-ink-2">
            {t('chat.pairedCount', { n: String(paired) })}
          </span>
        )}
        <label className="ml-auto flex items-center gap-1 text-vp-sm text-ink-2">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          {t('chat.enabled')}
        </label>
      </div>
      <HealthLine ch={channel} />

      {factory.login ? (
        <div className="mt-3">
          {signedIn && (
            <p className="mb-2 text-vp-sm text-ink-2">
              {t('chat.signedInAs', { id: safeText(channel?.values.user_id ?? '') })}
            </p>
          )}
          {login && login.status !== 'done' ? (
            <div className="flex flex-col items-start gap-2">
              {login.qrUrl && (login.status === 'waiting' || login.status === 'scanned' || login.status === 'needCode') && (
                <QR value={login.qrUrl} />
              )}
              <p className="text-vp-sm text-ink-2" data-testid={`chat-login-${factory.kind}`}>
                {login.status === 'waiting' && t('chat.scanToSignIn')}
                {login.status === 'scanned' && t('chat.scannedConfirm')}
                {login.status === 'needCode' && t('chat.needCode')}
                {login.status === 'expired' && t('chat.qrExpired')}
                {login.status === 'failed' && safeText(login.error || t('chat.loginFailed'))}
              </p>
              {login.status === 'needCode' && (
                <div className="flex gap-2">
                  <input
                    className={`${INPUT} w-32`}
                    inputMode="numeric"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder="123456"
                  />
                  <button type="button" className="vp-control" onClick={() => void sendCode()}>
                    {t('chat.send')}
                  </button>
                </div>
              )}
              {(login.status === 'expired' || login.status === 'failed') && (
                <button type="button" className="vp-control" onClick={() => void startLogin()}>
                  {t('chat.tryAgain')}
                </button>
              )}
            </div>
          ) : (
            <button
              type="button"
              className="vp-control vp-press gap-1.5"
              disabled={busy}
              onClick={() => void startLogin()}
              data-testid={`chat-login-start-${factory.kind}`}
            >
              <LogIn size={14} />
              {signedIn ? t('chat.signInAgain') : t('chat.signIn')}
            </button>
          )}
        </div>
      ) : (
        <div className="mt-3 grid grid-cols-1 gap-2">
          {factory.fields.map((f) => (
            <div key={f.name} className="min-w-0">
              <label htmlFor={`chat-${factory.kind}-${f.name}`} className="mb-1 block text-vp-sm text-ink-3">
                {f.label}
              </label>
              <input
                id={`chat-${factory.kind}-${f.name}`}
                className={`${INPUT} font-mono`}
                type={f.secret ? 'password' : 'text'}
                autoComplete="off"
                value={shown(f.name)}
                placeholder={f.secret && channel?.secretSet[f.name] ? t('chat.secretKept') : f.hint ?? ''}
                onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
              />
            </div>
          ))}
          {factory.webhook && channel?.webhookUrl && (
            <div className="min-w-0">
              <span className="mb-1 block text-vp-sm text-ink-3">{t('chat.webhookUrl')}</span>
              <div className="flex items-center gap-1">
                <code className="min-w-0 flex-1 truncate rounded-vp bg-surface-2 px-2 py-1 font-mono text-vp-sm text-ink">
                  {channel.webhookUrl}
                </code>
                <button type="button" className="vp-control" onClick={copy} title={t('chat.copy')}>
                  {copied ? <Check size={14} /> : <Copy size={14} />}
                </button>
              </div>
              <p className="mt-1 text-vp-xs text-ink-3">{t('chat.webhookHint')}</p>
            </div>
          )}
        </div>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        {(!factory.login || signedIn) && (
          <button
            type="button"
            className="vp-control vp-press gap-1.5"
            disabled={busy}
            onClick={() => void save()}
            data-testid={`chat-save-${factory.kind}`}
          >
            <Check size={14} />
            {t('chat.save')}
          </button>
        )}
        {channel && (
          <button type="button" className="vp-control gap-1.5" disabled={busy || !channel.enabled} onClick={() => void test()}>
            <Send size={14} />
            {t('chat.test')}
          </button>
        )}
        {factory.webhook && !channel && (
          <span className="flex items-center gap-1 text-vp-xs text-ink-3">
            <Link2 size={12} />
            {t('chat.webhookAfterSave')}
          </span>
        )}
        {channel && (
          <button type="button" className="vp-control ml-auto gap-1.5 text-ink-2" onClick={() => void remove()}>
            <Trash2 size={14} />
            {t('chat.remove')}
          </button>
        )}
      </div>
    </Card>
  )
}
