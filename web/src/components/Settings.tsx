import { useEffect, useState } from 'react'
import { Check, Copy, Fingerprint, Plus, X } from 'lucide-react'

import { api } from '../protocol/api'
import {
  decodeCreationOptions,
  encodeAttestation,
  passkeysSupported,
} from '../protocol/webauthn'
import type { AuditEntry, HookStatus, Passkey, SettingsInfo } from '../protocol/wire'
import { passkeyLabel } from './label'
import { setLang, t, useLang } from '../i18n'
import { notifyEnabled, notifySupported, requestNotifyPermission, setNotifyEnabled } from '../notify'

function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

function duration(seconds: number): string {
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m ${seconds % 60}s`
}

/**
 * What the panel is doing and how it is configured.
 *
 * Read-only for anything that lives in a flag: the panel is started by a
 * systemd unit or a compose file, and a setting that could be changed in two
 * places is a setting that disagrees with itself after the next restart. What
 * it does offer are the things that genuinely belong to the running instance —
 * passkeys, and whether agents report their own state.
 */
export function Settings({ onClose }: { onClose: () => void }) {
  const lang = useLang()
  const [notifyState, setNotifyState] = useState<NotificationPermission>(
    typeof Notification === 'undefined' ? 'denied' : Notification.permission,
  )
  const [notifyOn, setNotifyOn] = useState(notifyEnabled())
  const [info, setInfo] = useState<SettingsInfo | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Polled while open, not fetched once.
  //
  // Half of what this dialog shows is live — uptime, how many sessions exist,
  // how many browsers are watching, whether the hook script is installed — and
  // it was a photograph taken at the moment the dialog opened. "A settings page
  // for observing the backend" that stops observing the instant you look at it
  // answers a question about the past.
  //
  // Four seconds: this is somewhere you glance to see whether things are
  // healthy, not a monitor. The system monitor tab is where live graphs live,
  // and it has its own cadence.
  useEffect(() => {
    let ignore = false
    const load = () =>
      api
        .settings()
        .then((i) => {
          if (!ignore) setInfo(i)
        })
        .catch((e: unknown) => {
          if (!ignore) setError(e instanceof Error ? e.message : String(e))
        })
    void load()
    const timer = window.setInterval(() => void load(), 4000)
    return () => {
      ignore = true
      clearInterval(timer)
    }
  }, [])

  return (
    <div className="absolute inset-0 z-30 flex items-start justify-center overflow-y-auto bg-black/40 px-4 py-8">
      <div
        data-testid="settings"
        className="w-full max-w-2xl rounded-vp-lg border border-hairline bg-surface p-6"
      >
        <div className="mb-5 flex items-center gap-2">
          <h2 className="flex-1 text-[15px] font-semibold tracking-tight text-ink">{t('settings.title')}</h2>
          <button
            type="button"
            onClick={onClose}
            title={t('settings.close')}
            data-testid="settings-close"
            className="rounded p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <X size={15} />
          </button>
        </div>

        {error && (
          <p className="mb-4 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
            {error}
          </p>
        )}

        {/* First, because it decides whether anything below it can be read.
            A segmented pair rather than a dropdown: there are two, both fit,
            and a select for two options is a click that buys nothing. Each
            option is written in its own language, so you can find yours
            without being able to read the other. */}
        {/* Asked for from a click, never on load: a permission prompt fired on
            arrival is why browsers stopped showing them, and Safari refuses one
            outside a gesture at all. */}
        <Section title={t('notify.title')}>
          <p className="mb-2 text-[12px] leading-relaxed text-ink-2">{t('notify.explain')}</p>
          {!notifySupported() ? (
            <p className="text-[12px] text-ink-3">{t('notify.insecure')}</p>
          ) : notifyState === 'denied' ? (
            <p className="text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
              {t('notify.denied')}
            </p>
          ) : notifyState === 'granted' && notifyOn ? (
            <div className="flex items-center gap-2">
              <span className="text-[12.5px] text-ink">{t('notify.on')}</span>
              <button
                type="button"
                data-testid="notify-off"
                onClick={() => {
                  setNotifyEnabled(false)
                  setNotifyOn(false)
                }}
                className="rounded-vp border border-hairline px-2 py-1 text-[12px] text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
              >
                {t('dir.cancel')}
              </button>
            </div>
          ) : (
            <button
              type="button"
              data-testid="notify-enable"
              onClick={() => {
                void requestNotifyPermission().then((p) => {
                  setNotifyState(p)
                  setNotifyOn(p === 'granted')
                })
              }}
              className="rounded-vp px-3 py-1.5 text-[12.5px]"
              style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
            >
              {t('notify.enable')}
            </button>
          )}
        </Section>

        <Section title={t('settings.language')}>
          <div
            data-testid="settings-language"
            className="inline-flex items-center gap-0.5 rounded-lg bg-surface-2 p-0.5"
          >
            {(['zh', 'en'] as const).map((code) => (
              <button
                key={code}
                type="button"
                data-testid={`lang-${code}`}
                onClick={() => setLang(code)}
                aria-pressed={lang === code}
                className={`rounded-[7px] px-3 py-1 text-[12.5px] transition-colors duration-200 ease-vp ${
                  lang === code
                    ? 'bg-surface text-ink shadow-[0_1px_2px_rgb(0_0_0/0.12)]'
                    : 'text-ink-2 hover:text-ink'
                }`}
              >
                {code === 'zh' ? t('settings.languageZh') : t('settings.languageEn')}
              </button>
            ))}
          </div>
        </Section>

        {info && <StatusSection info={info} />}
        <HooksSection />
        <PasswordSection />
        {info?.passkeysUsable && <PasskeysSection />}
        <AuditSection />
      </div>
    </div>
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
    <Section title={t('set.password')}>
      <p className="mb-2 text-[12px] text-ink-2">
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
          className="min-w-48 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1.5 text-[12.5px] text-ink outline-none focus:border-accent"
        />
        <input
          type="password"
          data-testid="password-next"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          placeholder={t('set.newPassword')}
          autoComplete="new-password"
          className="min-w-48 flex-1 rounded-vp border border-hairline bg-surface px-2 py-1.5 text-[12.5px] text-ink outline-none focus:border-accent"
        />
        <button
          type="button"
          data-testid="password-submit"
          disabled={busy || !current || !next}
          onClick={() => void submit()}
          className="rounded-vp bg-accent px-3 py-1.5 text-[12.5px] font-medium text-white transition-opacity duration-200 ease-vp disabled:opacity-40"
        >
          {busy ? t('set.working') : t('set.change')}
        </button>
      </div>
      {error && (
        <p data-testid="password-error" className="mt-2 text-[12px] text-state-crashed">
          {error}
        </p>
      )}
      {done && (
        <p data-testid="password-done" className="mt-2 text-[12px] text-state-done">
          Changed. Every other browser has been signed out.
        </p>
      )}
    </Section>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mb-6">
      <h3 className="mb-2 text-[11px] font-semibold tracking-wide text-ink-2 uppercase">{title}</h3>
      {children}
    </section>
  )
}

/** Two weeks, matching the window the server logs a warning in. */
const CERT_WARN_MS = 14 * 24 * 60 * 60 * 1000

function certLabel(unixSeconds: number): string {
  const at = new Date(unixSeconds * 1000)
  const left = at.getTime() - Date.now()
  if (left <= 0) return `expired ${at.toLocaleDateString()}`
  const days = Math.floor(left / 86_400_000)
  return `${at.toLocaleDateString()} · ${days} day${days === 1 ? '' : 's'} left`
}

function certTone(unixSeconds: number): 'normal' | 'warn' | 'bad' {
  const left = unixSeconds * 1000 - Date.now()
  if (left <= 0) return 'bad'
  return left < CERT_WARN_MS ? 'warn' : 'normal'
}

function Row({
  label,
  value,
  tone = 'normal',
}: {
  label: string
  value: string
  tone?: 'normal' | 'warn' | 'bad'
}) {
  return (
    <div className="flex items-baseline gap-3 border-b border-hairline py-1.5 last:border-0">
      <span className="w-32 shrink-0 text-[11.5px] text-ink-2">{label}</span>
      <span
        className={`tabular min-w-0 flex-1 truncate text-[12.5px] ${
          tone === 'bad' ? 'text-state-crashed' : tone === 'warn' ? 'text-state-waiting' : 'text-ink'
        }`}
        title={value}
      >
        {value}
      </span>
    </div>
  )
}

function StatusSection({ info }: { info: SettingsInfo }) {
  return (
    <Section title={t('set.status')}>
      <div data-testid="settings-status">
        <Row label={t('set.version')} value={`${info.version} (${info.commit})`} />
        <Row label={t('set.uptime')} value={duration(info.uptime)} />
        <Row
          label={t('set.sessions')}
          value={`${info.sessions} on tmux ${info.tmuxVersion} · ${info.attached} attached`}
        />
        <Row label={t('set.viewers')} value={String(info.viewers)} />
        <Row label={t('set.socket')} value={info.tmuxSocket} />
        <Row label={t('set.data')} value={`${info.dataDir} · ${bytes(info.dbBytes)}`} />
        <Row label={t('set.listening')} value={`${info.addr} → ${info.url}`} />
        <Row
          label={t('set.tls')}
          value={info.tlsMode === 'off' ? 'off' : `${info.tlsMode} · ${info.domain}`}
        />
        {/* A certificate nobody renewed does not announce itself; it simply
            stops working one morning. The panel warns in its log as the date
            approaches, but a log on a machine nobody reads is not where this
            should first be noticed. */}
        {info.certExpiry !== undefined && (
          <Row
            label={t('set.cert')}
            value={certLabel(info.certExpiry)}
            tone={certTone(info.certExpiry)}
          />
        )}
        <Row
          label={t('set.access')}
          value={info.allowAll ? 'any address' : 'restricted by --allow-from'}
        />
        <Row label={t('set.signedIn')} value={info.username} />
      </div>
    </Section>
  )
}

function HooksSection() {
  const [status, setStatus] = useState<HookStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showSnippet, setShowSnippet] = useState(false)

  useEffect(() => {
    let ignore = false
    api
      .hookStatus()
      .then((h) => {
        if (!ignore) setStatus(h)
      })
      .catch((e: unknown) => {
        if (!ignore) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      ignore = true
    }
  }, [])

  // Sessions that were already running when this changed are the reason for
  // the notice below. See it for why.
  const [justChanged, setJustChanged] = useState(false)

  const act = async (fn: () => Promise<HookStatus>) => {
    setBusy(true)
    setError(null)
    try {
      setStatus(await fn())
      setJustChanged(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section title={t('set.reporting')}>
      <p className="mb-3 text-[12px] leading-relaxed text-ink-2">
        {t('set.reportingWhy')}
      </p>

      {error && (
        <p className="mb-2 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
          {error}
        </p>
      )}

      {status && (
        <div data-testid="hooks-status">
          <Row
            label={t('set.claudeCode')}
            // "installed", not "reporting". The panel has read a file; it has
            // not heard from anything. Saying "reporting 4 events" the instant
            // the file is written is a claim about behaviour that nothing has
            // checked, and it is wrong for every session that was already
            // running — see the notice below.
            value={
              status.installed
                ? `installed for ${status.events.length} events`
                : t('set.notInstalled')
            }
          />
          <Row label={t('set.settingsFile')} value={status.settingsPath} />

          <div className="mt-3 flex flex-wrap items-center gap-2">
            {status.installed ? (
              <button
                type="button"
                disabled={busy}
                data-testid="hooks-remove"
                onClick={() => void act(() => api.removeHooks())}
                className="rounded-vp border border-hairline px-3 py-1.5 text-[12px] text-ink transition-colors duration-200 ease-vp hover:bg-surface-2 disabled:opacity-50"
              >
                Remove
              </button>
            ) : (
              <button
                type="button"
                disabled={busy}
                data-testid="hooks-install"
                onClick={() => void act(() => api.installHooks())}
                className="rounded-vp px-3 py-1.5 text-[12px] font-medium disabled:opacity-50"
                style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
              >
                {busy ? t('set.working') : t('set.install')}
              </button>
            )}
            <button
              type="button"
              onClick={() => setShowSnippet((v) => !v)}
              data-testid="hooks-preview"
              className="rounded-vp border border-hairline px-3 py-1.5 text-[12px] text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              {showSnippet ? t('set.hide') : t('set.showWrites')}
            </button>
          </div>

          {/* An agent reads its hooks when it starts, so changing them does
              nothing to the sessions already open — which, in a panel built
              for a dozen long-lived agents, is all of them. Without this the
              status says "installed", every state stays guessed, and there is
              nothing on screen connecting the two.

              Claude Code's own instruction to itself, in the binary: "Tell the
              user to open `/hooks` once (reloads config) or restart — you
              can't do this yourself; `/hooks` is a user UI menu and opening it
              ends this turn." So the agent will not even be able to explain
              it. */}
          {justChanged && (
            <p data-testid="hooks-restart-note" className="mt-3 text-[12px] leading-relaxed text-ink-2">
              Sessions that are already running will not pick this up. In each one, open{' '}
              <code className="font-mono">/hooks</code> once to reload, or restart the agent.
            </p>
          )}

          {/* Shown before agreeing, not after. It edits a file that is theirs
              and usually has other things in it — the existing contents are
              merged, every entry is tagged so removing them cannot take
              anyone else's with it, and a backup is written first. */}
          {showSnippet && (
            <div className="mt-3">
              <Snippet label={t('set.claudeCode')} text={status.snippet} />
              <Snippet label={t('set.codexPaste')} text={status.codexSnippet} />
            </div>
          )}
        </div>
      )}
    </Section>
  )
}

function Snippet({ label, text }: { label: string; text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="mb-3">
      <div className="mb-1 flex items-center gap-2">
        <span className="text-[11px] text-ink-2">{label}</span>
        <button
          type="button"
          onClick={() => {
            void navigator.clipboard
              ?.writeText(text)
              .then(() => setCopied(true))
              .catch(() => setCopied(false))
          }}
          className="flex items-center gap-1 rounded p-1 text-ink-2 transition-colors duration-200 ease-vp hover:text-ink"
        >
          {copied ? <Check size={11} /> : <Copy size={11} />}
          <span className="text-[10.5px]">{copied ? 'Copied' : 'Copy'}</span>
        </button>
      </div>
      <pre className="max-h-56 overflow-auto rounded-vp border border-hairline bg-bg p-2 font-mono text-[11px] leading-relaxed text-ink">
        {text}
      </pre>
    </div>
  )
}

function PasskeysSection() {
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
    const name = window.prompt('Name this passkey', 'This device')
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

  return (
    <Section title={t('set.passkeys')}>
      <p className="mb-3 text-[12px] leading-relaxed text-ink-2">
        {t('set.passkeysWhy')}
      </p>
      {error && (
        <p className="mb-2 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
          {error}
        </p>
      )}
      {keys.length === 0 && <p className="mb-2 text-[12px] text-ink-2">{t('set.noPasskeys')}</p>}
      {keys.map((k) => (
        <div
          key={k.id}
          data-testid="passkey-row"
          className="group flex items-center gap-2 rounded-vp px-2 py-1.5 hover:bg-surface-2"
        >
          <Fingerprint size={12} className="shrink-0 text-ink-2" />
          <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{passkeyLabel(k)}</span>
          <span className="shrink-0 text-[10.5px] text-ink-2">
            {k.lastUsedAt ? `used ${new Date(k.lastUsedAt * 1000).toLocaleDateString()}` : 'never used'}
          </span>
          <button
            type="button"
            onClick={() => {
              if (!window.confirm(`Remove ${passkeyLabel(k)}?`)) return
              void api.deletePasskey(k.id).then(load).catch(() => setError('could not remove it'))
            }}
            title={t('set.remove')}
            className="shrink-0 rounded p-0.5 text-ink-2 vp-reveal hover:text-ink"
          >
            <X size={12} />
          </button>
        </div>
      ))}
      <button
        type="button"
        disabled={busy || !passkeysSupported()}
        onClick={() => void add()}
        data-testid="passkey-add"
        className="mt-3 flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-[12px] font-medium disabled:opacity-50"
        style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
      >
        <Plus size={13} />
        {busy ? 'Waiting for your device…' : 'Add a passkey'}
      </button>
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
    <Section title={t('set.activity')}>
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
          <div key={i} className="flex items-baseline gap-2 py-0.5 text-[11.5px]">
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
