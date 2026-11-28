import { useCallback, useEffect, useState } from 'react'
import {
  AlertTriangle,
  ShieldAlert,
  Eye,
  EyeOff,
  Fingerprint,
  Info,
  KeyRound,
  Loader2,
  Lock,
  LogIn,
  Terminal,
  User,
} from 'lucide-react'

import { t, useLang } from '../i18n'
import { api, errorText, OriginNotTrustedError } from '../protocol/api'
import {
  decodeRequestOptions,
  encodeAssertion,
  passkeysSupported,
} from '../protocol/webauthn'
import type { AuthState } from '../protocol/wire'
import { LanguageSwitch } from './LanguageSwitch'
import { blockerKey } from './settings/passkeyReason'
import mountain from '../assets/login-mountain.png'
import './auth.css'

/**
 * Stands between a stranger and the machine.
 *
 * Renders the setup form when no account exists, the sign-in form when nobody
 * is signed in, and the panel otherwise. The setup form asks for the one-time
 * token the server printed to its console: whoever can read that output is the
 * person entitled to claim the panel, and merely reaching it over the network
 * is not enough.
 *
 * This is the only screen most people see before they trust the thing with a
 * machine they care about, and it was a bordered box of stacked inputs that
 * looked the same whether it was asking you to sign in or asking you to claim
 * the panel for the first time. Those are different moments and they now look
 * different: signing in is the mark and one card, first run is a numbered walk
 * through the token and then the account.
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

  if (error && !state) return <Unreachable message={error} />
  if (!state) return <Loading />
  if (state.authenticated) {
    return <>{children(state, signOut)}</>
  }
  return <AuthForm state={state} onDone={refresh} />
}

/* ── the ground everything below stands on ───────────────────────────────── */

/**
 * The panel's icon, redrawn from tokens rather than loaded from /icon.svg.
 *
 * `<img src="/icon.svg">` is the shorter line and it is wrong twice here. The
 * file's colours are literals, so the mark would keep the light theme's blue
 * in the dark one; and it is a second request, so the one piece of identity on
 * this screen is an empty square until it lands — on the screen that is served
 * before anything else has been fetched. Inline, it paints with the first
 * frame, in whichever palette is on.
 */
export function Mark({ small }: { small?: boolean }) {
  const box = small ? 'h-6 w-6 rounded-md' : 'h-11 w-11 rounded-vp border border-hairline-strong shadow-sm'
  return (
    <span
      className={`flex shrink-0 items-center justify-center ${box}`}
      style={{ background: 'var(--ui-accent-fill)' }}
    >
      <svg viewBox="0 0 64 64" width={small ? 15 : 26} height={small ? 15 : 26} aria-hidden="true">
        <g fill="var(--ui-accent-fill-ink)">
          <rect x="13" y="17" width="26" height="6" rx="3" />
          <rect x="13" y="29" width="34" height="6" rx="3" />
          <rect x="13" y="41" width="20" height="6" rx="3" />
        </g>
        <path d="M50 15 L56 25.5 L44 25.5 Z" fill="var(--vp-state-waiting)" />
      </svg>
    </span>
  )
}

/**
 * Every state of this screen sits on the same ground.
 *
 * Scrolling is on the outer element rather than the page: at 320px with the
 * software keyboard up, the setup card is taller than what is left of the
 * viewport, and a card that cannot be scrolled to is a panel that cannot be
 * set up. The safe-area classes are the other half of that — index.html asks
 * for `viewport-fit=cover`, so without them the language switch sits under the
 * clock on a notched phone.
 */
function Shell({ wide, children }: { wide?: boolean; children: React.ReactNode }) {
  useLang()
  return (
    <div className="auth-shell bg-bg text-ink">
      <div className="auth-layout">
        <main className="auth-main">
          <header className="auth-topbar">
            <div className="auth-brand"><Mark small /><span>vibepanel</span></div>
            <LanguageSwitch testid="auth" />
          </header>
          <div className="auth-content">
            <div className={`auth-form-wrap ${wide ? 'auth-form-wide' : ''}`}>{children}</div>
          </div>
          <footer className="auth-footer"><Lock size={13} aria-hidden="true" />{t('auth.privateWorkspace')}</footer>
        </main>
        <aside className="auth-visual" data-testid="auth-visual">
          <img className="auth-mountain" src={mountain} alt="" />
          <div className="auth-visual-shade" />
          <div className="auth-visual-copy">
            <span className="auth-visual-eyebrow">{t('auth.visualEyebrow')}</span>
            <h2>{t('auth.visualTitle')}</h2>
            <p>{t('auth.visualBody')}</p>
          </div>
          <div className="auth-visual-footer"><span>vibepanel</span><span>{t('auth.visualFooter')}</span></div>
        </aside>
      </div>
    </div>
  )
}

/**
 * Anything that went wrong, said in one shape.
 *
 * The triangle is not decoration: red text on a surface is the only thing
 * distinguishing this from a hint, and red is exactly what a colour-blind
 * reader in a dark room does not get.
 */
function Alert({ testid, children }: { testid?: string; children: React.ReactNode }) {
  return (
    <div
      role="alert"
      data-testid={testid}
      className="flex items-start gap-2 rounded-vp border border-hairline bg-surface-2 px-3 py-2.5 text-vp-base text-danger-ink"
    >
      <AlertTriangle size={14} className="mt-px shrink-0" />
      <span className="min-w-0 break-words">{children}</span>
    </div>
  )
}

function Loading() {
  useLang()
  return (
    <Shell>
      <div className="flex flex-col items-center gap-4">
        <Mark />
        <p className="flex items-center gap-2 text-vp-base text-ink-2">
          <Loader2 size={13} className="animate-spin" />
          {t('auth.loading')}
        </p>
      </div>
    </Shell>
  )
}

function Unreachable({ message }: { message: string }) {
  useLang()
  return (
    <Shell>
      <div className="flex flex-col items-center gap-4">
        <Mark />
        <div className="w-full">
          <Alert>{message}</Alert>
        </div>
      </div>
    </Shell>
  )
}

/* ── the two moments ─────────────────────────────────────────────────────── */

/** One input, one label, one optional line under it. */
function Field({
  label,
  hint,
  icon,
  children,
}: {
  label: string
  hint?: string
  icon: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <label className="block">
      <span className="auth-label">{label}</span>
      <div className="relative">
        <span className="pointer-events-none absolute inset-y-0 left-3 flex items-center text-ink-3">
          {icon}
        </span>
        {children}
      </div>
      {hint && <span className="mt-1.5 block text-vp-xs text-ink-2">{hint}</span>}
    </label>
  )
}

const INPUT =
  'auth-input w-full border border-hairline-strong bg-surface pr-3 pl-10 text-ink ' +
  'outline-none transition-colors duration-100 ease-vp focus:border-accent'

/**
 * A step in the first run, numbered.
 *
 * Both steps are on screen and both are live. The numbers and the rail say
 * which order they are meant to be read in; they do not gate anything, because
 * the token is pasted once and a wizard that hides the field behind a "next"
 * is a wizard that can lose it.
 */
function Step({
  n,
  title,
  last,
  children,
}: {
  n: number
  title: string
  last?: boolean
  children: React.ReactNode
}) {
  return (
    <li className="relative flex gap-3">
      {!last && <span aria-hidden="true" className="absolute top-7 bottom-0 left-3 w-px bg-hairline" />}
      <span
        aria-hidden="true"
        className="tabular relative z-10 flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-hairline bg-surface-2 text-vp-xs font-semibold text-ink-2"
      >
        {n}
      </span>
      <div className={`min-w-0 flex-1 ${last ? 'pb-1' : 'pb-5'}`}>
        <div className="mb-2 text-vp-md font-medium text-ink">{title}</div>
        {children}
      </div>
    </li>
  )
}

function AuthForm({ state, onDone }: { state: AuthState; onDone: () => void }) {
  useLang()
  const setup = !state.configured
  const [token, setToken] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [reveal, setReveal] = useState(false)
  const [busy, setBusy] = useState(false)
  // The origin the server refused to finish setup under, or null. Held rather
  // than acted on: the panel cannot tell this browser's name from an
  // attacker's, so the only thing that can answer is the person holding the
  // one-time token.
  const [askOrigin, setAskOrigin] = useState<OriginNotTrustedError | null>(null)
  const [error, setError] = useState<string | null>(null)
  const passkeyOffered = !setup && state.passkeysUsable && passkeysSupported()
  // Why not, when it is not offered. The browser's reason comes first: the
  // server can report a perfectly good configuration while this particular
  // page is on plain http, and it used to say nothing at all in that case --
  // the button simply was not there.
  const reasonKey = !passkeysSupported() ? 'pk.insecure' : blockerKey(state.passkeyReason)

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
        setError(errorText(err))
      }
    } finally {
      setBusy(false)
    }
  }

  const runSetup = async (trustOrigin: boolean) => {
    setBusy(true)
    setError(null)
    try {
      await api.setup(token.trim(), username.trim(), password, trustOrigin)
      setPassword('')
      setAskOrigin(null)
      onDone()
    } catch (err) {
      // Not an error on the way to the screen: a question, and the form is
      // kept exactly as it is so answering it costs nothing typed twice.
      if (err instanceof OriginNotTrustedError) setAskOrigin(err)
      else setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (setup) {
      await runSetup(false)
      return
    }
    setBusy(true)
    setError(null)
    try {
      await api.login(username.trim(), password)
      setPassword('')
      onDone()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  const passwordField = (
    <Field
      label={t('auth.password')}
      hint={setup ? t('auth.passwordHint') : undefined}
      icon={<Lock size={14} />}
    >
      <input
        type={reveal ? 'text' : 'password'}
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        autoComplete={setup ? 'new-password' : 'current-password'}
        data-testid="auth-password"
        className={`${INPUT} pr-10`}
      />
      <button
        type="button"
        onClick={() => setReveal((v) => !v)}
        title={reveal ? t('auth.hidePassword') : t('auth.showPassword')}
        aria-label={reveal ? t('auth.hidePassword') : t('auth.showPassword')}
        aria-pressed={reveal}
        data-testid="auth-reveal"
        className="vp-control absolute top-1/2 right-1.5 -translate-y-1/2"
      >
        {reveal ? <EyeOff size={14} /> : <Eye size={14} />}
      </button>
    </Field>
  )

  const usernameField = (
    <Field label={t('auth.username')} icon={<User size={14} />}>
      <input
        value={username}
        onChange={(e) => setUsername(e.target.value)}
        autoComplete="username"
        data-testid="auth-username"
        className={INPUT}
      />
    </Field>
  )

  if (askOrigin) {
    return (
      <Shell wide={setup}>
        <div
          data-testid="origin-trust"
          className="rounded-vp-lg border border-hairline bg-surface p-5 shadow-xl"
        >
          <div className="flex items-start gap-3">
            <span className="mt-0.5 text-ink-2">
              <ShieldAlert size={18} />
            </span>
            <div className="min-w-0">
              <h2 className="text-vp-base font-semibold text-ink">{t('auth.originTitle')}</h2>
              <p className="mt-1 text-vp-sm leading-relaxed text-ink-2">{t('auth.originBody')}</p>
            </div>
          </div>
          {/* The two names, as data rather than inside a sentence: the whole
              decision is comparing them, and a person cannot compare two
              things they have to pick out of prose. */}
          <dl className="mt-4 space-y-2 text-vp-sm">
            <div className="flex flex-wrap items-baseline gap-x-2">
              <dt className="shrink-0 text-ink-3">{t('auth.originYouAreOn')}</dt>
              <dd className="min-w-0 break-all font-mono text-ink" data-testid="origin-browser">
                {askOrigin.origin}
              </dd>
            </div>
            <div className="flex flex-wrap items-baseline gap-x-2">
              <dt className="shrink-0 text-ink-3">{t('auth.originPanelThinks')}</dt>
              <dd className="min-w-0 break-all font-mono text-ink-2">
                {askOrigin.answersTo.join(', ')}
              </dd>
            </div>
          </dl>
          <div className="mt-5 flex gap-2">
            <button
              type="button"
              disabled={busy}
              onClick={() => void runSetup(true)}
              data-testid="origin-trust-add"
              className="vp-primary h-9 px-4 text-vp-base"
            >
              {busy ? t('auth.working') : t('auth.originAdd')}
            </button>
            <button
              type="button"
              disabled={busy}
              onClick={() => setAskOrigin(null)}
              className="vp-outline h-9 px-4 text-vp-base"
            >
              {t('auth.originCancel')}
            </button>
          </div>
        </div>
      </Shell>
    )
  }

  return (
    <Shell wide={setup}>
      <div className="auth-heading">
        {setup && (
          <span className="rounded-full border border-hairline bg-surface-2 px-2.5 py-1 text-vp-xs font-medium text-ink-2">
            {t('auth.firstRun')}
          </span>
        )}
        <div>
          <h1>
            {setup ? t('auth.setupTitle') : t('auth.loginTitle')}
          </h1>
          <p className="text-ink-2">
            {setup ? t('auth.setupHint') : t('auth.signInHint')}
          </p>
        </div>
      </div>

      <form
        onSubmit={submit}
        data-testid={setup ? 'setup-form' : 'login-form'}
        className="auth-form"
      >
        {setup ? (
          <ol>
            <Step n={1} title={t('auth.setupToken')}>
              <div className="relative">
                <span className="pointer-events-none absolute inset-y-0 left-3 flex items-center text-ink-3">
                  <Terminal size={14} />
                </span>
                <input
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  data-testid="setup-token"
                  className={`${INPUT} font-mono`}
                />
              </div>
              <span className="mt-1.5 block text-vp-xs text-ink-2">{t('auth.tokenWhere')}</span>
            </Step>
            <Step n={2} title={t('auth.stepAccount')} last>
              <div className="space-y-3">
                {usernameField}
                {passwordField}
              </div>
            </Step>
          </ol>
        ) : (
          <>
            <div className="space-y-5">
              {usernameField}
              {passwordField}
            </div>
          </>
        )}

        {error && (
          <div className="mt-4">
            <Alert testid="auth-error">{error}</Alert>
          </div>
        )}

        <button
          type="submit"
          disabled={busy}
          data-testid="auth-submit"
          className="auth-submit mt-6 flex w-full items-center justify-center gap-2 disabled:opacity-50"
        >
          {busy ? (
            <Loader2 size={14} className="animate-spin" />
          ) : setup ? (
            <KeyRound size={14} />
          ) : (
            <LogIn size={14} />
          )}
          {busy ? t('auth.working') : setup ? t('auth.create') : t('auth.signIn')}
        </button>

        {passkeyOffered && (
          <>
            <div className="auth-divider"><span />{t('auth.or')}<span /></div>
            <button
              type="button"
              disabled={busy}
              onClick={() => void signInWithPasskey()}
              data-testid="passkey-signin"
              className="auth-passkey flex w-full items-center justify-center gap-2 border border-hairline-strong bg-surface text-ink disabled:opacity-50"
            >
              <Fingerprint size={19} />
              {t('auth.usePasskey')}
            </button>
          </>
        )}

        {!setup && !passkeyOffered && (
          <p
            className="mt-4 flex items-start gap-2 border-t border-hairline pt-4 text-vp-sm leading-relaxed text-ink-2"
            data-testid="passkey-note"
          >
            <Info size={13} className="mt-px shrink-0" />
            <span className="min-w-0">
              {t('auth.noPasskeys', { why: t(reasonKey) })}
            </span>
          </p>
        )}
      </form>
    </Shell>
  )
}
