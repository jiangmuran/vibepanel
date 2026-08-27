import { useCallback, useEffect, useState } from 'react'
import { Fingerprint, KeyRound, LogIn } from 'lucide-react'

import { t, useLang } from '../i18n'
import { api } from '../protocol/api'
import {
  decodeRequestOptions,
  encodeAssertion,
  passkeysSupported,
} from '../protocol/webauthn'
import type { AuthState } from '../protocol/wire'

/**
 * Stands between a stranger and the machine.
 *
 * Renders the setup form when no account exists, the sign-in form when nobody
 * is signed in, and the panel otherwise. The setup form asks for the one-time
 * token the server printed to its console: whoever can read that output is the
 * person entitled to claim the panel, and merely reaching it over the network
 * is not enough.
 */
export function AuthGate({ children }: { children: (state: AuthState, signOut: () => void) => React.ReactNode }) {
  const [state, setState] = useState<AuthState | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      setState(await api.authState())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    let ignore = false
    api
      .authState()
      .then((s) => {
        if (!ignore) setState(s)
      })
      .catch((e: unknown) => {
        if (!ignore) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      ignore = true
    }
  }, [])

  const signOut = useCallback(() => {
    void api
      .logout()
      .catch(() => {
        /* the session is going away either way */
      })
      .then(() => refresh())
  }, [refresh])

  if (error && !state) {
    return <Centered><p style={{ color: 'var(--vp-state-waiting)' }}>{error}</p></Centered>
  }
  if (!state) {
    return <Centered><p className="text-ink-2">Loading…</p></Centered>
  }
  if (state.authenticated) {
    return <>{children(state, signOut)}</>
  }
  return <AuthForm state={state} onDone={refresh} />
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full w-full items-center justify-center bg-bg text-[13px] text-ink">
      {children}
    </div>
  )
}

function AuthForm({ state, onDone }: { state: AuthState; onDone: () => void }) {
  useLang()
  const setup = !state.configured
  const [token, setToken] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const signInWithPasskey = async () => {
    setBusy(true)
    setError(null)
    try {
      const options = decodeRequestOptions(
        (await api.passkeyLoginBegin()) as Parameters<typeof decodeRequestOptions>[0],
      )
      const credential = (await navigator.credentials.get({
        publicKey: options,
        // Discoverable: the browser offers whichever key it holds for this
        // site, so nothing has to be typed.
        mediation: 'optional',
      })) as PublicKeyCredential | null
      if (!credential) throw new Error('no passkey was chosen')
      await api.passkeyLoginFinish(encodeAssertion(credential))
      onDone()
    } catch (err) {
      // A cancelled prompt is a choice, not a failure worth shouting about.
      if (err instanceof DOMException && err.name === 'NotAllowedError') {
        setError(null)
      } else {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      setBusy(false)
    }
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if (setup) await api.setup(token.trim(), username.trim(), password)
      else await api.login(username.trim(), password)
      setPassword('')
      onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex h-full w-full items-center justify-center bg-bg px-6">
      <form
        onSubmit={submit}
        data-testid={setup ? 'setup-form' : 'login-form'}
        className="w-full max-w-80 rounded-vp-lg border border-hairline bg-surface p-6"
      >
        <h1 className="mb-1 text-[15px] font-semibold tracking-tight text-ink">
          {setup ? t('auth.setupTitle') : 'vibepanel'}
        </h1>
        <p className="mb-5 text-[12px] leading-relaxed text-ink-2">
          {setup ? t('auth.setupHint') : t('auth.signInHint')}
        </p>

        {setup && (
          <Field label={t('auth.setupToken')}>
            <input
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              data-testid="setup-token"
              className="w-full rounded-vp border border-hairline bg-bg px-2 py-1.5 font-mono text-[12px] text-ink outline-none focus:border-accent"
            />
          </Field>
        )}

        <Field label={t('auth.username')}>
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            data-testid="auth-username"
            className="w-full rounded-vp border border-hairline bg-bg px-2 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
          />
        </Field>

        <Field
          label={t('auth.password')}
          hint={setup ? t('auth.passwordHint') : undefined}
        >
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={setup ? 'new-password' : 'current-password'}
            data-testid="auth-password"
            className="w-full rounded-vp border border-hairline bg-bg px-2 py-1.5 text-[13px] text-ink outline-none focus:border-accent"
          />
        </Field>

        {error && (
          <p data-testid="auth-error" className="mb-3 text-[12px]" style={{ color: 'var(--vp-state-waiting)' }}>
            {error}
          </p>
        )}

        <button
          type="submit"
          disabled={busy}
          data-testid="auth-submit"
          className="flex w-full items-center justify-center gap-1.5 rounded-vp px-3 py-2 text-[13px] font-medium transition-opacity duration-200 ease-vp disabled:opacity-50"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          {setup ? <KeyRound size={14} /> : <LogIn size={14} />}
          {busy ? t('auth.working') : setup ? t('auth.create') : t('auth.signIn')}
        </button>

        {!setup && state.passkeysUsable && passkeysSupported() && (
          <>
            <div className="my-4 flex items-center gap-3">
              <span className="h-px flex-1" style={{ background: 'var(--vp-hairline)' }} />
              <span className="text-[10.5px] text-ink-2">{t('auth.or')}</span>
              <span className="h-px flex-1" style={{ background: 'var(--vp-hairline)' }} />
            </div>
            <button
              type="button"
              disabled={busy}
              onClick={() => void signInWithPasskey()}
              data-testid="passkey-signin"
              className="flex w-full items-center justify-center gap-1.5 rounded-vp border border-hairline px-3 py-2 text-[13px] text-ink transition-colors duration-200 ease-vp hover:bg-surface-2 disabled:opacity-50"
            >
              <Fingerprint size={14} />
              {t('auth.usePasskey')}
            </button>
          </>
        )}

        {!setup && !state.passkeysUsable && (
          <p className="mt-4 text-[11px] leading-relaxed text-ink-2" data-testid="passkey-note">
            {t('auth.noPasskeys', { why: state.passkeyReason ?? t('auth.notSupported') })}
          </p>
        )}
      </form>
    </div>
  )
}

function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <label className="mb-3 block">
      <span className="mb-1 block text-[11px] text-ink-2">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[10.5px] text-ink-2">{hint}</span>}
    </label>
  )
}
