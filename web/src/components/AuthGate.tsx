import { useCallback, useEffect, useState } from 'react'
import {
  AlertTriangle,
  ChevronRight,
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

import { setLang, t, useLang } from '../i18n'
import { api } from '../protocol/api'
import {
  decodeRequestOptions,
  encodeAssertion,
  passkeysSupported,
} from '../protocol/webauthn'
import type { AuthState } from '../protocol/wire'
import { blockerKey } from './settings/passkeyReason'

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
function Mark() {
  return (
    <span className="flex h-12 w-12 shrink-0 items-center justify-center rounded-vp bg-accent shadow-lg">
      <svg viewBox="0 0 64 64" width="30" height="30" aria-hidden="true">
        <g fill="var(--vp-accent-ink)">
          <rect x="13" y="17" width="26" height="6" rx="3" />
          <rect x="13" y="29" width="34" height="6" rx="3" />
          <rect x="13" y="41" width="20" height="6" rx="3" />
        </g>
        {/* The waiting marker is a triangle here for the same reason it is one
            in the sidebar: shape carries the meaning, never the hue alone. */}
        <path d="M50 15 L56 25.5 L44 25.5 Z" fill="var(--vp-state-waiting)" />
      </svg>
    </span>
  )
}

/**
 * The language switch, before there is anywhere else to put it.
 *
 * The same argument as the one in the settings header: somebody who needs this
 * cannot read the rest of the screen, which is why they are looking for it. On
 * every other screen it lives in settings — and settings is behind this one,
 * so a reader who lands here in the wrong language has no way through. Each
 * option is written in its own language for the same reason.
 */
function LanguageSwitch() {
  const lang = useLang()
  return (
    <div data-testid="auth-language" className="vp-segmented">
      {(['zh', 'en'] as const).map((code) => (
        <button
          key={code}
          type="button"
          data-testid={`auth-lang-${code}`}
          onClick={() => setLang(code)}
          aria-pressed={lang === code}
          data-active={lang === code}
          className="vp-tab px-3 text-vp-base whitespace-nowrap"
        >
          {code === 'zh' ? t('settings.languageZh') : t('settings.languageEn')}
        </button>
      ))}
    </div>
  )
}

/* Two washes behind the card, in tokens so they follow the theme.
 *
 * A flat page behind a flat card is what made the old screen read as an
 * unstyled form: nothing on it had any depth, so the browser's own default
 * rendering and this were the same picture. Written as an array because the
 * untranslated-string check reads `name: 'text with spaces'` as prose. */
const WASH = [
  'radial-gradient(52rem 26rem at 50% -8rem, color-mix(in srgb, var(--vp-accent) 18%, transparent), transparent 68%)',
  'radial-gradient(38rem 22rem at 108% 108%, color-mix(in srgb, var(--vp-state-waiting) 10%, transparent), transparent 70%)',
].join(', ')

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
  return (
    <div className="relative h-full w-full overflow-y-auto bg-bg text-ink">
      <div aria-hidden="true" className="pointer-events-none fixed inset-0" style={{ backgroundImage: WASH }} />
      <div className="relative flex min-h-full flex-col">
        <div className="vp-safe-pad-top flex shrink-0 justify-end px-4 pb-1">
          <LanguageSwitch />
        </div>
        <div className="vp-safe-bottom flex flex-1 items-center justify-center px-4">
          {/* The breathing room is on this element and not on its parent.
              `.vp-safe-bottom` used to *replace* padding-bottom -- it is
              emitted after Tailwind's utilities -- so a `pb-10` up there was
              silently dead and a tall card ended flush against the bottom of a
              short window. The class composes now (see styles.css), but the
              padding is still better here, where the card is. */}
          <div className={`w-full pt-2 pb-10 ${wide ? 'max-w-md' : 'max-w-sm'}`}>{children}</div>
        </div>
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
      <span className="mb-1.5 block text-vp-sm font-medium text-ink-2">{label}</span>
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

// py-2.5 rather than a height: under a coarse pointer styles.css forces every
// field to 16px so that iOS does not zoom the page on focus, and a fixed height
// would then clip the text it was sized for.
const INPUT =
  'w-full rounded-vp border border-hairline bg-bg py-2.5 pr-3 pl-9 text-vp-md text-ink ' +
  'outline-none transition-colors duration-200 ease-vp focus:border-accent focus:ring-2 focus:ring-accent/25'

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

  return (
    <Shell wide={setup}>
      <div className="mb-5 flex flex-col items-center gap-3 text-center">
        <Mark />
        {setup && (
          <span className="rounded-full border border-hairline bg-surface-2 px-2.5 py-1 text-vp-xs font-medium text-ink-2">
            {t('auth.firstRun')}
          </span>
        )}
        <div>
          <h1 className="text-vp-lg font-semibold tracking-tight text-ink">
            {setup ? t('auth.setupTitle') : 'vibepanel'}
          </h1>
          <p className="mt-1 text-vp-base leading-relaxed text-ink-2">
            {setup ? t('auth.setupHint') : t('auth.signInHint')}
          </p>
        </div>
      </div>

      <form
        onSubmit={submit}
        data-testid={setup ? 'setup-form' : 'login-form'}
        className="rounded-vp-lg border border-hairline bg-surface p-5 shadow-xl"
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
            {passkeyOffered && (
              <>
                {/* The passkey is the better answer and is offered as one: a
                    row you press, above the fields, rather than a second
                    button the same size as the first one underneath it. */}
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => void signInWithPasskey()}
                  data-testid="passkey-signin"
                  className="vp-press flex w-full items-center gap-3 rounded-vp border border-hairline bg-surface-2 px-3 py-3 text-left hover:border-hairline-strong disabled:opacity-50"
                >
                  <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-accent/15 text-accent">
                    <Fingerprint size={16} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block text-vp-md font-medium text-ink">{t('auth.usePasskey')}</span>
                    <span className="block text-vp-sm text-ink-2">{t('auth.passkeyHint')}</span>
                  </span>
                  <ChevronRight size={14} className="shrink-0 text-ink-3" />
                </button>
                <div className="my-4 flex items-center gap-3">
                  <span className="h-px flex-1 bg-hairline" />
                  <span className="text-vp-xs tracking-wider text-ink-2">{t('auth.or')}</span>
                  <span className="h-px flex-1 bg-hairline" />
                </div>
              </>
            )}
            <div className="space-y-3">
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
          className="vp-press mt-4 flex w-full items-center justify-center gap-2 rounded-vp bg-accent px-4 py-2.5 text-vp-md font-medium text-accent-ink shadow-sm disabled:opacity-50"
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
