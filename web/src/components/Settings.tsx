import { useEffect, useState } from 'react'
import { Check, Copy, Fingerprint, Plus, X } from 'lucide-react'

import { api } from '../protocol/api'
import {
  decodeCreationOptions,
  encodeAttestation,
  passkeysSupported,
} from '../protocol/webauthn'
import type { AuditEntry, HookStatus, Passkey, SettingsInfo } from '../protocol/wire'

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
  const [info, setInfo] = useState<SettingsInfo | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let ignore = false
    api
      .settings()
      .then((i) => {
        if (!ignore) setInfo(i)
      })
      .catch((e: unknown) => {
        if (!ignore) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      ignore = true
    }
  }, [])

  return (
    <div className="absolute inset-0 z-30 flex items-start justify-center overflow-y-auto bg-black/40 px-4 py-8">
      <div
        data-testid="settings"
        className="w-full max-w-2xl rounded-vp-lg border border-hairline bg-surface p-6"
      >
        <div className="mb-5 flex items-center gap-2">
          <h2 className="flex-1 text-[15px] font-semibold tracking-tight text-ink">Settings</h2>
          <button
            type="button"
            onClick={onClose}
            title="Close"
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

        {info && <StatusSection info={info} />}
        <HooksSection />
        {info?.passkeysUsable && <PasskeysSection />}
        <AuditSection />
      </div>
    </div>
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

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline gap-3 border-b border-hairline py-1.5 last:border-0">
      <span className="w-32 shrink-0 text-[11.5px] text-ink-2">{label}</span>
      <span className="tabular min-w-0 flex-1 truncate text-[12.5px] text-ink" title={value}>
        {value}
      </span>
    </div>
  )
}

function StatusSection({ info }: { info: SettingsInfo }) {
  return (
    <Section title="Status">
      <div data-testid="settings-status">
        <Row label="Version" value={`${info.version} (${info.commit})`} />
        <Row label="Uptime" value={duration(info.uptime)} />
        <Row
          label="Sessions"
          value={`${info.sessions} on tmux ${info.tmuxVersion} · ${info.attached} attached`}
        />
        <Row label="Viewers" value={String(info.viewers)} />
        <Row label="tmux socket" value={info.tmuxSocket} />
        <Row label="Data" value={`${info.dataDir} · ${bytes(info.dbBytes)}`} />
        <Row label="Listening" value={`${info.addr} → ${info.url}`} />
        <Row
          label="TLS"
          value={info.tlsMode === 'off' ? 'off' : `${info.tlsMode} · ${info.domain}`}
        />
        <Row
          label="Access"
          value={info.allowAll ? 'any address' : 'restricted by --allow-from'}
        />
        <Row label="Signed in as" value={info.username} />
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

  const act = async (fn: () => Promise<HookStatus>) => {
    setBusy(true)
    setError(null)
    try {
      setStatus(await fn())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section title="State reporting">
      <p className="mb-3 text-[12px] leading-relaxed text-ink-2">
        Without this the panel infers what a session is doing from its output, which can tell
        working from quiet and sees the terminal bell, but cannot tell <em>finished</em> from{' '}
        <em>waiting for you</em>. With it, the agent says which.
      </p>

      {error && (
        <p className="mb-2 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
          {error}
        </p>
      )}

      {status && (
        <div data-testid="hooks-status">
          <Row
            label="Claude Code"
            value={
              status.installed
                ? `reporting ${(status.events ?? []).length} events`
                : 'not installed'
            }
          />
          <Row label="Settings file" value={status.settingsPath} />

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
                {busy ? 'Working…' : 'Install for Claude Code'}
              </button>
            )}
            <button
              type="button"
              onClick={() => setShowSnippet((v) => !v)}
              data-testid="hooks-preview"
              className="rounded-vp border border-hairline px-3 py-1.5 text-[12px] text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
            >
              {showSnippet ? 'Hide' : 'Show what it writes'}
            </button>
          </div>

          {/* Shown before agreeing, not after. It edits a file that is theirs
              and usually has other things in it — the existing contents are
              merged, every entry is tagged so removing them cannot take
              anyone else's with it, and a backup is written first. */}
          {showSnippet && (
            <div className="mt-3">
              <Snippet label="Claude Code" text={status.snippet} />
              <Snippet label="Codex (paste yourself)" text={status.codexSnippet} />
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
    <Section title="Passkeys">
      <p className="mb-3 text-[12px] leading-relaxed text-ink-2">
        Sign in with this device instead of a password. The password keeps working — a passkey is
        an addition, never the only way in.
      </p>
      {error && (
        <p className="mb-2 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
          {error}
        </p>
      )}
      {keys.length === 0 && <p className="mb-2 text-[12px] text-ink-2">None registered.</p>}
      {keys.map((k) => (
        <div
          key={k.id}
          data-testid="passkey-row"
          className="group flex items-center gap-2 rounded-vp px-2 py-1.5 hover:bg-surface-2"
        >
          <Fingerprint size={12} className="shrink-0 text-ink-2" />
          <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{k.name}</span>
          <span className="shrink-0 text-[10.5px] text-ink-2">
            {k.lastUsedAt ? `used ${new Date(k.lastUsedAt * 1000).toLocaleDateString()}` : 'never used'}
          </span>
          <button
            type="button"
            onClick={() => {
              if (!window.confirm(`Remove ${k.name}?`)) return
              void api.deletePasskey(k.id).then(load).catch(() => setError('could not remove it'))
            }}
            title="Remove"
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
    <Section title="Recent activity">
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
