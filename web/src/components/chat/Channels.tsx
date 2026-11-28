import { useEffect, useState } from 'react'
import { ArrowRight, Check, ChevronDown, Copy, Link2, LogIn, Send, Trash2, Users } from 'lucide-react'

import { copyTextInGesture } from '../../clipboard'
import { api } from '../../protocol/api'
import type { ChatChannel, ChatFactory, ChatLogin, ChatSettings } from '../../protocol/wire'
import { getLang, t } from '../../i18n'
import type { Key } from '../../i18n'
import { askConfirm } from '../ask'
import { showToast } from '../toasts'
import { safeText } from '../text'
import { Section } from './Chat'
import { ChannelIcon } from './ChannelIcon'
import { ensureChatConsent } from './consent'
import { chatError } from './errors'
import { INPUT, INPUT_SHORT, Primary, Secondary, errText } from './form'
import { QR } from './QR'

function ago(unix: number): string {
  if (!unix) return t('chat.never')
  const s = Math.max(0, Math.floor(Date.now() / 1000) - unix)
  if (s < 60) return t('chat.justNow')
  if (s < 3600) return t('chat.minutesAgo', { n: String(Math.floor(s / 60)) })
  if (s < 86400) return t('chat.hoursAgo', { n: String(Math.floor(s / 3600)) })
  return t('chat.daysAgo', { n: String(Math.floor(s / 86400)) })
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
export function Channels({ data, selected, onSelect, onChange }: {
  data: ChatSettings
  selected?: string
  onSelect: (kind: string) => void
  onChange: () => void
}) {
  return (
    <Section id="channels" title={t('chat.channels')}>
      {!selected && (
        <div className="chat-channel-grid">
          {data.factories.map((f) => {
            const channel = data.channels.find((c) => c.kind === f.kind) ?? null
            return (
              <button key={f.kind} type="button" className="chat-channel-tile"
                data-testid={`chat-overview-${f.kind}`} onClick={() => onSelect(f.kind)}>
                <span className="chat-channel-art" data-kind={f.kind}><ChannelIcon kind={f.kind} size={58} /></span>
                <span className="chat-tile-body">
                  <span className="chat-tile-title">{f.label}</span>
                  <span className="chat-tile-description">{['feishu', 'telegram', 'weixin'].includes(f.kind) ? t(`chat.description.${f.kind}` as Key) : t('chat.channelsLead')}</span>
                  <span className="chat-tile-footer">
                    <span className="chat-tile-status"><HealthLine ch={channel} testid={false} /></span>
                    <span className="chat-tile-arrow"><ArrowRight size={18} aria-hidden="true" /></span>
                  </span>
                </span>
              </button>
            )
          })}
        </div>
      )}
      {/* Keep forms mounted while moving between adapters so navigation does
          not erase an unsaved token or abandon an in-progress QR login. */}
      {data.factories.map((f) => (
        <div key={f.kind} hidden={selected !== f.kind}>
          <ChannelCard factory={f} channel={data.channels.find((c) => c.kind === f.kind) ?? null}
            paired={data.peers.filter((p) => p.channel === f.kind && p.status === 'paired').length}
            consentAt={data.consentAt} onChange={onChange} />
        </div>
      ))}
    </Section>
  )
}

function SetupStep({ id, number, title, done, open, locked, onSelect, children }: {
  id: string
  number: number
  title: string
  done?: boolean
  open: boolean
  locked: boolean
  onSelect: () => void
  children: React.ReactNode
}) {
  return (
    <div className="chat-setup-step" data-active={open}>
      <button type="button" className="chat-step-heading" disabled={locked}
        aria-expanded={open} aria-controls={`${id}-body`} onClick={onSelect}>
        <span className="chat-step-number" data-done={done}>{done ? <Check size={15} aria-hidden="true" /> : number}</span>
        <span>{title}</span><ChevronDown size={17} className="chat-step-chevron" aria-hidden="true" />
      </button>
      <div id={`${id}-body`} className="chat-step-body" hidden={!open}>{children}</div>
    </div>
  )
}

function HealthLine({ ch, testid = true }: { ch: ChatChannel | null; testid?: boolean }) {
  if (!ch) return <span className="text-vp-sm text-ink-3">{t('chat.notSetUp')}</span>
  const h = ch.health
  // The word and the colour say the same thing (red line 4): an error that
  // is current is "error", never "running" painted red.
  const erroring = Boolean(h.lastError) && h.lastErrorAt >= h.lastOk
  const tone = !ch.enabled
    ? 'var(--vp-ink-3)'
    : erroring
      ? 'var(--vp-danger-ink)'
      : h.running
        ? 'var(--ui-done-ink)'
        : 'var(--ui-warn-ink)'
  const word = !ch.enabled
    ? t('chat.off')
    : erroring
      ? t('chat.erroring')
      : h.running
        ? t('chat.running')
        : t('chat.stopped')
  return (
    <span className="block text-vp-sm text-ink-2" data-testid={testid ? `chat-health-${ch.kind}` : undefined}>
      <span className="font-medium" style={{ color: tone }}>
        {word}
      </span>
      <span className="text-ink-3"> · </span>
      <span>{t('chat.lastInbound', { when: ago(h.lastInbound) })}</span>
      {h.needsHello > 0 && (
        <>
          <span className="text-ink-3"> · </span>
          <span style={{ color: 'var(--vp-state-waiting)' }}>
            {t('chat.needsHello', { who: (h.waitingOn ?? []).map(safeText).join('、'), n: String(h.needsHello) })}
          </span>
        </>
      )}
      {erroring && (
        <span className="mt-1 block break-words" style={{ color: 'var(--vp-state-crashed)' }}>
          {safeText(h.lastError)}
        </span>
      )}
    </span>
  )
}

function ChannelCard({
  factory,
  channel,
  paired,
  consentAt,
  onChange,
}: {
  factory: ChatFactory
  channel: ChatChannel | null
  paired: number
  consentAt: number
  onChange: () => void
}) {
  const [values, setValues] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [login, setLogin] = useState<ChatLogin | null>(null)
  const [code, setCode] = useState('')
  const [copied, setCopied] = useState(false)
  const webhookKey = `vibepanel.chat.webhook-confirmed.${factory.kind}`
  const [webhookConfirmed, setWebhookConfirmed] = useState<string | null>(() => {
    try { return localStorage.getItem(webhookKey) } catch { return null }
  })
  const configured = Boolean(channel?.configured)
  const pairStep = factory.webhook ? 3 : 2
  const receiveStep = pairStep + 1
  // A callback URL being available only means credentials were saved. The
  // person must finish configuring it in Feishu before pairing is offered.
  // Existing paired people prove that this part already works.
  const webhookDone = !factory.webhook || paired > 0 || Boolean(channel?.health.lastInbound)
    || Boolean(channel?.webhookUrl && webhookConfirmed === channel.webhookUrl)
  const progress = !configured ? 1 : !webhookDone ? 2 : paired === 0 ? pairStep : receiveStep
  const [selection, setSelection] = useState({ progress, step: progress })
  if (selection.progress !== progress) setSelection({ progress, step: progress })
  const activeStep = selection.progress === progress ? Math.min(selection.step, progress) : progress
  const stepProps = (number: number) => ({
    id: `chat-${factory.kind}-step-${number}`,
    number,
    open: activeStep === number,
    locked: number > progress,
    onSelect: () => setSelection({ progress, step: number }),
  })
  const clearWebhookConfirmation = () => {
    setWebhookConfirmed(null)
    try { localStorage.removeItem(webhookKey) } catch { /* The current visit still works without storage. */ }
  }
  const confirmWebhook = () => {
    if (!channel?.webhookUrl) return
    setWebhookConfirmed(channel.webhookUrl)
    try { localStorage.setItem(webhookKey, channel.webhookUrl) } catch { /* Keep the confirmation for this visit. */ }
    setSelection({ progress, step: pairStep })
  }

  // The stored non-secret values arrive with every poll; the form keeps what
  // the person typed and shows the server's copy for fields they have not
  // touched, so a poll does not erase a half-typed token.
  const shown = (name: string) => values[name] ?? channel?.values[name] ?? ''
  const lang = getLang()
  const hint = (f: ChatFactory['fields'][number]) => (lang === 'zh' ? (f.hintZh ?? f.hint ?? '') : (f.hint ?? ''))

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

  // The switch acts at once, like the mode switch two sections down, and
  // only exists once there is a channel to switch. A new channel is
  // created on by Save.
  const toggle = async (on: boolean) => {
    if (on && !(await ensureChatConsent(consentAt))) return
    try {
      await api.saveChatChannel(factory.kind, on, {})
      showToast({ kind: 'success', key: on ? 'chat.channelOn' : 'chat.channelOff', params: { name: factory.label } })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: chatError(e) })
    }
  }

  const save = async () => {
    const enabled = channel?.enabled ?? true
    if (enabled && !(await ensureChatConsent(consentAt))) return
    setBusy(true)
    try {
      await api.saveChatChannel(factory.kind, enabled, values)
      clearWebhookConfirmation()
      setSelection({ progress, step: 2 })
      setValues((v) => {
        const next = { ...v }
        for (const f of factory.fields) if (f.secret) delete next[f.name]
        return next
      })
      showToast({ kind: 'success', key: 'chat.saved' })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: chatError(e) })
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
          ? { kind: 'error', key: 'chat.testFailed', detail: chatError(new Error(res.error)) }
          : { kind: 'success', key: 'chat.testOkN', params: { n: res.sent } },
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
      clearWebhookConfirmation()
      setValues({})
      showToast({ kind: 'success', key: 'chat.channelRemoved', params: { name: factory.label } })
      onChange()
    } catch (e) {
      showToast({ kind: 'error', key: 'chat.saveFailed', detail: errText(e) })
    }
  }

  const startLogin = async () => {
    if (!(await ensureChatConsent(consentAt))) return
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
    <div data-testid={`chat-channel-${factory.kind}`} className="chat-channel-detail">
      <div className="chat-detail-status">
        <HealthLine ch={channel} />
        {channel && (
          <label className="flex shrink-0 items-center gap-2 text-vp-sm text-ink-2">
            <input type="checkbox" className="vp-switch" checked={channel.enabled}
              onChange={(e) => void toggle(e.target.checked)} data-testid={`chat-enabled-${factory.kind}`} />
            {t('chat.enabled')}
          </label>
        )}
      </div>
      <div className="chat-setup-card">
        <SetupStep {...stepProps(1)} title={t(factory.login ? 'chat.setupAccount' : factory.webhook ? 'chat.setupCredentials' : 'chat.setupBot')}
          done={configured}>
          {factory.login ? (
            <div>
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
                        className={`${INPUT_SHORT} w-32`}
                        inputMode="numeric"
                        value={code}
                        onChange={(e) => setCode(e.target.value)}
                        placeholder="123456"
                        aria-label={t('chat.needCode')}
                      />
                      <Secondary onClick={() => void sendCode()}>{t('chat.send')}</Secondary>
                    </div>
                  )}
                  <div className="flex gap-2">
                    {(login.status === 'expired' || login.status === 'failed') && (
                      <Secondary onClick={() => void startLogin()}>{t('chat.tryAgain')}</Secondary>
                    )}
                    <Secondary onClick={() => setLogin(null)} data-testid={`chat-login-cancel-${factory.kind}`}>
                      {t('chat.cancel')}
                    </Secondary>
                  </div>
                </div>
              ) : (
                <Secondary disabled={busy} onClick={() => void startLogin()} data-testid={`chat-login-start-${factory.kind}`}>
                  <LogIn size={14} />
                  {signedIn ? t('chat.signInAgain') : t('chat.signIn')}
                </Secondary>
              )}
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-4.5">
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
                    placeholder={f.secret && channel?.secretSet[f.name] ? t('chat.secretKept') : ''}
                    onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
                  />
                  {/* Under the field, not in it: a placeholder that says where to
                      find the value read as a value already filled in. */}
                  {hint(f) && <p className="mt-0.5 text-vp-xs text-ink-3">{hint(f)}</p>}
                </div>
              ))}
            </div>
          )}

          {!factory.login && (
            <Primary disabled={busy} className="mt-5" onClick={() => void save()} data-testid={`chat-save-${factory.kind}`}>
              <Check size={14} />{t('chat.save')}
            </Primary>
          )}
        </SetupStep>
        {factory.webhook && (
          <SetupStep {...stepProps(2)} title={t('chat.setupWebhook')} done={configured && webhookDone}>
            {channel?.webhookUrl && (
              <div className="min-w-0">
                <span className="mb-1 block text-vp-sm text-ink-3">{t('chat.webhookUrl')}</span>
                <div className="flex items-center gap-1">
                  <code className="min-w-0 flex-1 truncate rounded-vp bg-surface-2 px-2 py-1 font-mono text-vp-sm text-ink">
                    {channel.webhookUrl}
                  </code>
                  <Secondary onClick={copy} title={t('chat.copy')} aria-label={t('chat.copy')}>
                    {copied ? <Check size={14} /> : <Copy size={14} />}
                  </Secondary>
                </div>
                <p className="mt-1 text-vp-xs text-ink-3">{t('chat.webhookHint')}</p>
              </div>
            )}
            {!channel?.webhookUrl && <p className="flex items-center gap-2 text-vp-sm text-ink-2"><Link2 size={15} />{t('chat.webhookAfterSave')}</p>}
            <Primary className="mt-5" disabled={!channel?.webhookUrl} onClick={confirmWebhook}
              data-testid={`chat-webhook-next-${factory.kind}`}>
              {t('chat.webhookConfiguredNext')}<ArrowRight size={14} />
            </Primary>
          </SetupStep>
        )}
        <SetupStep {...stepProps(pairStep)} title={t('chat.setupPair')} done={paired > 0}>
          <p className="mb-4 text-vp-base leading-relaxed text-ink-2">{t('chat.setupPairHint')}</p>
          <a href="#peers" className="vp-outline w-fit text-vp-sm"><Users size={15} />{t('chat.openPeople')}</a>
        </SetupStep>
        <SetupStep {...stepProps(receiveStep)} title={t('chat.setupReceive')} done={Boolean(channel?.health.sent)}>
          <p className="mb-4 text-vp-base leading-relaxed text-ink-2">{t('chat.setupReceiveHint')}</p>
          <Secondary disabled={busy || !channel?.enabled || paired === 0} onClick={() => void test()}>
            <Send size={14} />{t('chat.test')}
          </Secondary>
        </SetupStep>
      </div>
      <div className="chat-people-heading">
        <h3>{t('chat.connectedPeople')}</h3>
        {paired > 0 && <span className="text-vp-sm text-ink-2">{t('chat.pairedCount', { n: String(paired) })}</span>}
      </div>
      <a href="#peers" className="chat-people-card">
        <Users size={24} className="shrink-0 text-ink-3" aria-hidden="true" />
        <span className="min-w-0 flex-1"><span className="block font-medium">{paired > 0 ? t('chat.pairedCount', { n: String(paired) }) : t('chat.noPeople')}</span>
          <span className="mt-1 block text-vp-sm text-ink-2">{paired > 0 ? t('chat.openPeople') : t('chat.noPeopleHint')}</span></span>
        <ArrowRight size={18} className="shrink-0" aria-hidden="true" />
      </a>
      {channel && <div className="mt-6 flex justify-end">
        <Secondary onClick={() => void remove()}><Trash2 size={14} />{t('chat.remove')}</Secondary>
      </div>}
    </div>
  )
}
