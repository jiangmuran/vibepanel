import { useEffect, useState } from 'react'
import { Fingerprint, Plus, X } from 'lucide-react'

import { api } from '../../protocol/api'
import {
  decodeCreationOptions,
  encodeAttestation,
  passkeysSupported,
} from '../../protocol/webauthn'
import type { Passkey } from '../../protocol/wire'
import { t } from '../../i18n'
import { askConfirm, askText } from '../ask'
import { passkeyLabel } from '../label'
import { showToast } from '../toasts'
import { blockerKey } from './passkeyReason'
import { Section } from './parts'

export function PasskeysSection({ blocker, rpID }: { blocker: string; rpID: string }) {
  const [keys, setKeys] = useState<Passkey[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = () =>
    api
      .passkeys()
      .then(setKeys)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))

  useEffect(() => {
    let ignore = false
    api
      .passkeys()
      .then((k) => {
        if (!ignore) setKeys(k)
      })
      .catch((e: unknown) => {
        if (!ignore) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      ignore = true
    }
  }, [])

  const add = async () => {
    // A question with a field, not window.prompt. The prompt could not say why
    // the name matters -- it is the only thing telling two credentials apart in
    // the list below -- and on a phone it arrived as a system sheet with the
    // hostname above it, which is the shape a phishing prompt has.
    const name = await askText({
      title: t('ask.passkeyNameTitle'),
      body: t('ask.passkeyNameBody'),
      field: { label: t('ask.passkeyNameField'), value: t('ask.passkeyNameDefault') },
      confirm: t('ask.add'),
      cancel: t('ask.cancel'),
    })
    if (name === null) return
    setBusy(true)
    setError(null)
    try {
      const options = decodeCreationOptions(
        (await api.passkeyRegisterBegin()) as Parameters<typeof decodeCreationOptions>[0],
      )
      const credential = (await navigator.credentials.create({
        publicKey: options,
      })) as PublicKeyCredential | null
      if (!credential) throw new Error('no passkey was created')
      await api.passkeyRegisterFinish(name.trim() || 'Passkey', encodeAttestation(credential))
      await load()
    } catch (e) {
      // Cancelling the browser prompt is a choice, not an error.
      if (e instanceof DOMException && e.name === 'NotAllowedError') setError(null)
      else setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  // The browser refuses a ceremony whose Relying Party ID is not a suffix of
  // the page's own host, and it refuses it with a DOMException that says
  // "registrable domain suffix" -- which is true, and is not what somebody
  // wants to read. The panel knows both halves, so it can say which two names
  // disagree before anybody presses anything.
  const host = typeof window === 'undefined' ? '' : window.location.hostname
  const mismatch =
    blocker === '' && rpID !== '' && host !== '' && host !== rpID && !host.endsWith('.' + rpID)

  return (
    <Section id="passkeys" title={t('set.passkeys')}>
      <p className="mb-3 text-vp-base leading-relaxed text-ink-2">
        {t('set.passkeysWhy')}
      </p>
      {blocker === '' && !passkeysSupported() && (
        <div
          data-testid="passkey-insecure"
          className="mb-3 rounded-vp border border-hairline bg-surface-2 px-3 py-2 text-vp-base leading-relaxed text-ink-2"
        >
          {/* The server no longer decides this. It cannot: with a proxy
              terminating TLS in front, its own TLS mode says nothing about
              what the browser is on. Outside a secure context the browser does
              not expose PublicKeyCredential at all, which is both the reason
              and the way to detect it. */}
          <p className="text-ink">{t('pk.insecure')}</p>
        </div>
      )}
      {blocker !== '' && (
        <div
          data-testid="passkey-blocked"
          className="mb-3 rounded-vp border border-hairline bg-surface-2 px-3 py-2 text-vp-base leading-relaxed text-ink-2"
        >
          <p className="text-ink">{t(blockerKey(blocker))}</p>
          <p className="mt-1">{t('pk.where')}</p>
        </div>
      )}
      {blocker === '' && rpID !== '' && (
        <p className="mb-3 text-vp-sm text-ink-3">
          {t('pk.rpid')} <span className="font-mono text-ink-2">{rpID}</span>
        </p>
      )}
      {mismatch && (
        <p
          data-testid="passkey-mismatch"
          className="mb-3 text-vp-base leading-relaxed"
          style={{ color: 'var(--vp-state-waiting)' }}
        >
          {t('pk.hostMismatch', { host, rpid: rpID })}
        </p>
      )}
      {error && (
        <p className="mb-2 text-vp-base" style={{ color: 'var(--vp-state-waiting)' }}>
          {error}
        </p>
      )}
      {keys.length === 0 && <p className="mb-2 text-vp-base text-ink-2">{t('set.noPasskeys')}</p>}
      {keys.map((k) => (
        <div
          key={k.id}
          data-testid="passkey-row"
          className="vp-press group flex items-center gap-2 rounded-vp px-2 py-1.5 hover:bg-surface-2"
        >
          <Fingerprint size={12} className="shrink-0 text-ink-2" />
          <span className="min-w-0 flex-1 truncate text-vp-base text-ink">{passkeyLabel(k)}</span>
          <span className="shrink-0 text-vp-xs text-ink-2">
            {k.lastUsedAt
              ? t('pk.used', { when: new Date(k.lastUsedAt * 1000).toLocaleDateString() })
              : t('pk.neverUsed')}
          </span>
          <button
            type="button"
            onClick={() => {
              void (async () => {
                const yes = await askConfirm({
                  title: t('ask.removePasskeyTitle', { name: passkeyLabel(k) }),
                  body: t('ask.removePasskeyBody'),
                  confirm: t('ask.remove'),
                  cancel: t('ask.cancel'),
                  destructive: true,
                })
                if (!yes) return
                // A toast rather than the section's error line: the row this
                // failure is about is three lines further down the dialog, and
                // the line at the top of a section is somewhere nobody looks
                // after pressing something at the bottom of it.
                await api
                  .deletePasskey(k.id)
                  .then(load)
                  .catch(() => showToast({ kind: 'error', key: 'toast.passkeyGone' }))
              })()
            }}
            title={t('set.remove')}
            className="vp-control vp-reveal"
          >
            <X size={12} />
          </button>
        </div>
      ))}
      <button
        type="button"
        disabled={busy || blocker !== '' || !passkeysSupported()}
        onClick={() => void add()}
        data-testid="passkey-add"
        className="mt-3 flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
        style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
      >
        <Plus size={13} />
        {busy ? t('pk.waiting') : t('pk.add')}
      </button>
    </Section>
  )
}
