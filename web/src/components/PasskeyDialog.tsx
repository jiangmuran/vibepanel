import { useEffect, useState } from 'react'
import { Fingerprint, Plus, X } from 'lucide-react'

import { api } from '../protocol/api'
import {
  decodeCreationOptions,
  encodeAttestation,
  passkeysSupported,
} from '../protocol/webauthn'
import type { Passkey } from '../protocol/wire'

function when(ts: number | null): string {
  if (!ts) return 'never used'
  const d = new Date(ts * 1000)
  return `last used ${d.toLocaleDateString()}`
}

/**
 * Register and remove passkeys.
 *
 * A dialog rather than a settings page because this is the one piece of
 * account management that exists so far, and burying it behind navigation that
 * does not exist yet would make it unreachable.
 */
export function PasskeyDialog({ onClose }: { onClose: () => void }) {
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

  const remove = async (k: Passkey) => {
    if (!window.confirm(`Remove ${k.name}? That device will need the password again.`)) return
    try {
      await api.deletePasskey(k.id)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div className="absolute inset-0 z-30 flex items-center justify-center bg-black/40 px-6">
      <div
        data-testid="passkey-dialog"
        className="w-full max-w-96 rounded-vp-lg border border-hairline bg-surface p-5"
      >
        <div className="mb-1 flex items-center gap-2">
          <Fingerprint size={15} className="text-ink-2" />
          <h2 className="flex-1 text-[14px] font-semibold text-ink">Passkeys</h2>
          <button
            type="button"
            onClick={onClose}
            title="Close"
            className="rounded p-1 text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
          >
            <X size={14} />
          </button>
        </div>
        <p className="mb-4 text-[12px] leading-relaxed text-ink-2">
          Sign in with your device instead of a password. The password keeps working — a passkey is
          an addition, never the only way in.
        </p>

        {error && (
          <p className="mb-3 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
            {error}
          </p>
        )}

        {keys.length === 0 && (
          <p className="mb-3 text-[12px] text-ink-2">No passkeys registered yet.</p>
        )}
        {keys.map((k) => (
          <div
            key={k.id}
            data-testid="passkey-row"
            className="group mb-1 flex items-center gap-2 rounded-vp px-2 py-1.5 hover:bg-surface-2"
          >
            <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{k.name}</span>
            <span className="shrink-0 text-[10.5px] text-ink-2">{when(k.lastUsedAt)}</span>
            <button
              type="button"
              onClick={() => void remove(k)}
              title="Remove"
              className="shrink-0 rounded p-0.5 text-ink-2 opacity-0 transition-opacity duration-200 ease-vp group-hover:opacity-100 hover:text-ink focus-visible:opacity-100"
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
          className="mt-3 flex w-full items-center justify-center gap-1.5 rounded-vp px-3 py-2 text-[13px] font-medium transition-opacity duration-200 ease-vp disabled:opacity-50"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          <Plus size={14} />
          {busy ? 'Waiting for your device…' : 'Add a passkey'}
        </button>
      </div>
    </div>
  )
}
