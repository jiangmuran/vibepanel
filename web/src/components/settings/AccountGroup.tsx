import { useEffect, useState } from 'react'

import { api } from '../../protocol/api'
import type { AuditEntry, SettingsInfo } from '../../protocol/wire'
import { t } from '../../i18n'
import { ApiTokens } from '../ApiTokens'
import { PasskeysSection } from './Passkeys'
import { Section } from './parts'

/**
 * The ways in, and who has come in.
 *
 * Password, passkeys and API tokens are one kind of thing — a credential you
 * make deliberately and revoke one at a time — and reading them together is
 * how somebody notices that one of them opens a terminal and the others open a
 * page. The activity log is under the same name because the question it
 * answers is "has anybody else been in here", which is the question you are
 * already asking when you came to change a password.
 */
export function AccountGroup({ info }: { info: SettingsInfo | null }) {
  return (
    <>
      <PasswordSection />
      {/* Always rendered, usable or not.
        *
        * It used to be `{passkeysUsable && ...}`, so a panel with no domain
        * configured had no passkey section at all and nothing saying why —
        * which is a feature somebody goes looking for, does not find, and
        * reports as missing. The reason is one line and it names the setting. */}
      <PasskeysSection blocker={info?.passkeyReason ?? ''} rpID={info?.domain ?? ''} />
      <Section id="tokens" title={t('tok.title')}>
        <ApiTokens />
      </Section>
      <AuditSection />
    </>
  )
}

/**
 * Changing the password, which had no way to happen from anywhere.
 *
 * The wizard set one once and nothing could replace it, so the answer to "this
 * leaked" was to stop the panel and edit SQLite by hand.
 *
 * The current password is required, and the server enforces that — a stolen
 * session cookie must not be enough to lock the owner out of their own panel.
 * Every other browser is signed out, because the reason to change a password is
 * that somebody else might have the old one.
 */
function PasswordSection() {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)

  const submit = async () => {
    setBusy(true)
    setError(null)
    setDone(false)
    try {
      await api.changePassword(current, next)
      setCurrent('')
      setNext('')
      setDone(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section id="password" title={t('set.password')}>
      <p className="mb-2 text-vp-base text-ink-2">
        {t('set.passwordWhy')}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <input
          type="password"
          data-testid="password-current"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          placeholder={t('set.currentPassword')}
          autoComplete="current-password"
          className="min-w-48 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1.5 text-vp-base text-ink outline-none focus:border-accent"
        />
        <input
          type="password"
          data-testid="password-next"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          placeholder={t('set.newPassword')}
          autoComplete="new-password"
          className="min-w-48 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1.5 text-vp-base text-ink outline-none focus:border-accent"
        />
        <button
          type="button"
          data-testid="password-submit"
          disabled={busy || !current || !next}
          onClick={() => void submit()}
          className="rounded-vp bg-accent px-3 py-1.5 text-vp-base font-medium text-white transition-opacity duration-200 ease-vp disabled:opacity-40"
        >
          {busy ? t('set.working') : t('set.change')}
        </button>
      </div>
      {error && (
        <p data-testid="password-error" className="mt-2 text-vp-base text-state-crashed">
          {error}
        </p>
      )}
      {done && (
        <p data-testid="password-done" className="mt-2 text-vp-base text-state-done">
          {t('settings.passwordChanged')}
        </p>
      )}
    </Section>
  )
}

function AuditSection() {
  const [entries, setEntries] = useState<AuditEntry[]>([])

  useEffect(() => {
    let ignore = false
    api
      .audit()
      .then((a) => {
        if (!ignore) setEntries(a)
      })
      .catch(() => {
        /* the section simply stays empty */
      })
    return () => {
      ignore = true
    }
  }, [])

  if (entries.length === 0) return null

  return (
    <Section id="activity" title={t('set.activity')}>
      {/* overflow-y-auto is enough, and that is not obvious.

          These rows are 408px of fixed columns in a dialog about 256 wide on a
          320px phone, so they do overflow. They are still reachable: CSS
          computes `overflow-x: visible` to `auto` when the other axis is not
          visible, so asking for vertical scrolling here quietly granted
          horizontal scrolling too. Measured, on a phone: overflowX computes to
          `auto` and the box does scroll sideways.

          Written down because a scan for boxes whose content does not fit
          flagged this, and the obvious "fix" — spelling out `overflow-auto` —
          would have changed nothing while claiming to have repaired something.
          */}
      <div data-testid="settings-audit" className="max-h-56 overflow-y-auto">
        {entries.map((e, i) => (
          <div key={i} className="flex items-baseline gap-2 py-0.5 text-vp-sm">
            <span className="tabular w-32 shrink-0 text-ink-2">
              {new Date(e.at * 1000).toLocaleString()}
            </span>
            <span className="w-40 shrink-0 truncate text-ink">{e.event}</span>
            <span className="w-24 shrink-0 truncate text-ink-2">{e.username || '—'}</span>
            <span className="min-w-0 flex-1 truncate text-ink-2" title={e.detail}>
              {e.ip} {e.detail}
            </span>
          </div>
        ))}
      </div>
    </Section>
  )
}
