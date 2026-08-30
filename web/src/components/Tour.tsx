import { useEffect, useState } from 'react'
import { Check, ChevronLeft, ChevronRight, Circle, Triangle, X } from 'lucide-react'

import { api } from '../protocol/api'
import { requestNotifyPermission } from '../notify'
import { showToast } from './toasts'
import type { HookStatus, TuneStatus } from '../protocol/wire'
import type { SettingsSection } from './settings/groups'
import { t, getLang, useLang } from '../i18n'

/**
 * The first-run tour.
 *
 * It exists for one question, asked by everybody who installs this: why is a
 * session that has finished still blue. The answer is that without state
 * reporting the panel sees a running process and nothing else, and the fix is
 * two clicks that nobody finds on their own -- they are in a settings dialog
 * behind a gear, under a heading about hooks, which is a word for a thing you
 * already know exists.
 *
 * So the tour is not a welcome mat. Two of its five steps *do* something: they
 * install the reporters, and they offer the Claude Code settings. The rest is
 * the smallest amount of orientation that makes those two make sense.
 *
 * Dismissed for good rather than snoozed. It is remembered on the server, not
 * in localStorage: the panel is opened from a laptop and a phone and a wall,
 * and a tour that has been read is read.
 */
export function Tour({
  onDone,
  onOpenSettings,
}: {
  onDone: () => void
  /** Where a step hands off to. Every action the tour offers has a home in the
   *  settings dialog, and the tour is the short version of it rather than a
   *  second place to configure the same thing -- somebody who skipped the tour
   *  must not have to find it again. TourStepsHaveASettingsHome is the guard. */
  onOpenSettings: (section: SettingsSection) => void
}) {
  useLang()
  const [step, setStep] = useState(0)
  const steps = [Intro, Reporting, TuneStep, Notifications, Encryption, FirstProject, WhereTheRestIs]
  const Body = steps[step]
  const last = step === steps.length - 1

  const finish = () => {
    // The close does not wait for the write: a modal that refuses to close
    // because a settings row could not be written is worse than seeing it once
    // more.
    //
    // But the failure is said out loud, which it was not. The catch was empty,
    // so when every write was being refused with a 403 the tour closed, failed
    // to record itself, and came back on the next refresh -- forever, with
    // nothing on screen connecting the two. 「每次刷新都会弹出新手教程」.
    void api.tourDone().catch(() => showToast({ kind: 'error', key: 'tour.notSaved' }))
    onDone()
  }

  // Escape closes it, like every other dialog here. It is dismissed for good
  // either way: a tour you have to escape twice is a tour you resent.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') finish()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={t('tour.title')}
      data-testid="tour"
    >
      <div className="vp-panel-in flex max-h-full w-full max-w-2xl flex-col overflow-hidden rounded-vp-lg border border-hairline bg-surface shadow-xl">
        <header className="flex items-center gap-3 border-b border-hairline px-5 py-3">
          <h2 className="flex-1 text-vp-lg font-semibold text-ink">{t('tour.title')}</h2>
          <button
            type="button"
            onClick={finish}
            data-testid="tour-skip"
            aria-label={t('tour.skip')}
            title={t('tour.skip')}
            className="vp-control vp-tap"
          >
            <X size={16} />
          </button>
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          <Body onOpenSettings={onOpenSettings} />
        </div>

        <footer className="flex items-center gap-3 border-t border-hairline px-5 py-3">
          {/* Position as a count, not only as dots: five unlabelled circles
              tell you where you are and not how much is left. */}
          <span className="text-vp-sm tabular text-ink-3" data-testid="tour-step">
            {t('tour.step', { n: step + 1, of: steps.length })}
          </span>
          <span className="flex-1" />
          <button
            type="button"
            className="vp-press rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-40"
            disabled={step === 0}
            onClick={() => setStep((n) => n - 1)}
            data-testid="tour-back"
          >
            <ChevronLeft size={14} className="mr-1 inline" />
            {t('tour.back')}
          </button>
          <button
            type="button"
            className="vp-press rounded-vp border border-accent bg-accent/10 px-3 py-1.5 text-vp-base text-accent transition-colors duration-200 ease-vp hover:bg-accent/20"
            onClick={() => (last ? finish() : setStep((n) => n + 1))}
            data-testid="tour-next"
          >
            {last ? t('tour.done') : t('tour.next')}
            {!last && <ChevronRight size={14} className="ml-1 inline" />}
          </button>
        </footer>
      </div>
    </div>
  )
}

/** A paragraph, at the one size the tour uses. */
function P({ children }: { children: React.ReactNode }) {
  return <p className="mb-3 text-vp-base leading-relaxed text-ink-2 last:mb-0">{children}</p>
}

function H({ children }: { children: React.ReactNode }) {
  return <h3 className="mb-2 text-vp-md font-semibold text-ink">{children}</h3>
}

function Intro() {
  return (
    <>
      <H>{t('tour.introH')}</H>
      <P>{t('tour.intro1')}</P>
      <P>{t('tour.intro2')}</P>
      {/* The three states, drawn the way the panel draws them. Shape and not
          only colour, which is the rule the panel itself follows. */}
      <div className="mt-4 flex flex-wrap gap-4 rounded-vp border border-hairline bg-surface-2 px-4 py-3">
        <span className="flex items-center gap-2 text-vp-sm text-ink-2">
          <Circle size={12} className="text-state-working" fill="currentColor" />
          {t('session.working')}
        </span>
        <span className="flex items-center gap-2 text-vp-sm text-ink-2">
          <Triangle size={12} className="text-state-waiting" fill="currentColor" />
          {t('session.waiting')}
        </span>
        <span className="flex items-center gap-2 text-vp-sm text-ink-2">
          <Check size={13} className="text-state-done" />
          {t('session.done')}
        </span>
      </div>
    </>
  )
}

/**
 * The step this whole thing exists for.
 *
 * Three agents, each configured by a different mechanism in a different file,
 * so three buttons and three answers rather than one "install everything" that
 * half-succeeds and reports nothing.
 */
function Reporting({ onOpenSettings }: StepProps) {
  const [st, setSt] = useState<HookStatus | null>(null)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  const load = () => {
    api
      .hookStatus()
      .then(setSt)
      .catch((e: unknown) => setErr(String(e)))
  }
  useEffect(load, [])

  const install = (agent: 'claude' | 'codex' | 'opencode') => {
    setBusy(agent)
    api
      .installHooks(agent)
      .then(() => load())
      .catch((e: unknown) => setErr(String(e)))
      .finally(() => setBusy(''))
  }

  const agents: { id: 'claude' | 'codex' | 'opencode'; name: string; on: boolean; note?: string }[] =
    st
      ? [
          { id: 'claude', name: t('set.claudeCode'), on: st.installed },
          { id: 'codex', name: t('set.codex'), on: st.codexInstalled, note: t('set.codexOneEvent') },
          { id: 'opencode', name: t('set.opencode'), on: st.opencodeInstalled },
        ]
      : []

  return (
    <>
      <H>{t('tour.hooksH')}</H>
      <P>{t('tour.hooks1')}</P>
      <P>{t('tour.hooks2')}</P>
      {!st && <p className="text-vp-sm text-ink-3">{err || t('tune.loading')}</p>}
      <div className="mt-3 flex flex-col gap-2">
        {agents.map((a) => (
          <div
            key={a.id}
            data-tour-agent={a.id}
            className="flex flex-wrap items-center gap-3 rounded-vp border border-hairline px-3 py-2"
          >
            <span className="min-w-0 flex-1">
              <span className="text-vp-base text-ink">{a.name}</span>
              {a.note && <span className="ml-2 text-vp-sm text-ink-3">{a.note}</span>}
            </span>
            {a.on ? (
              <span className="flex items-center gap-1 text-vp-sm text-state-done">
                <Check size={14} />
                {t('tour.on')}
              </span>
            ) : (
              <button
                type="button"
                disabled={busy === a.id}
                onClick={() => install(a.id)}
                data-testid={`tour-install-${a.id}`}
                className="vp-press rounded-vp border border-hairline px-3 py-1 text-vp-sm text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-50"
              >
                {t('tour.turnOn')}
              </button>
            )}
          </div>
        ))}
      </div>
      {st && (
        <p className="mt-3 text-vp-sm text-ink-3">{t('tour.hooksExisting')}</p>
      )}
      <More to="reporting" onOpenSettings={onOpenSettings} />
      {err && <p className="mt-2 text-vp-sm text-state-crashed">{err}</p>}
    </>
  )
}

/** The rest of Claude Code's settings file, with every key named first. */
function TuneStep({ onOpenSettings }: StepProps) {
  const [st, setSt] = useState<TuneStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const zh = getLang() === 'zh'

  const load = () => {
    api
      .tuneStatus()
      .then(setSt)
      .catch((e: unknown) => setErr(String(e)))
  }
  useEffect(load, [])

  return (
    <>
      <H>{t('tour.tuneH')}</H>
      <P>{t('tour.tune1')}</P>
      {!st ? (
        <p className="text-vp-sm text-ink-3">{err || t('tune.loading')}</p>
      ) : (
        <>
          <ul className="mb-3 flex flex-col gap-1">
            {st.rows.map((r) => (
              <li key={r.key} className="flex items-start gap-2 text-vp-sm">
                <span aria-hidden className={r.same ? 'text-state-done' : 'text-accent'}>
                  {r.same ? '✓' : '•'}
                </span>
                <span className="text-ink-2">{zh ? r.whatZh : r.what}</span>
              </li>
            ))}
          </ul>
          <button
            type="button"
            disabled={busy || st.changes === 0}
            data-testid="tour-tune-apply"
            onClick={() => {
              setBusy(true)
              api
                .tuneApply()
                .then(() => load())
                .catch((e: unknown) => setErr(String(e)))
                .finally(() => setBusy(false))
            }}
            className="vp-press rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink disabled:opacity-50"
          >
            {st.changes === 0 ? t('tune.nothing') : t('tune.apply', { n: st.changes })}
          </button>
          <p className="mt-2 text-vp-sm text-ink-3">{t('tune.backup', { p: st.path })}</p>
        </>
      )}
      {err && <p className="mt-2 text-vp-sm text-state-crashed">{err}</p>}
      <More to="tune" onOpenSettings={onOpenSettings} />
    </>
  )
}

/** What every step is handed. Steps that need nothing ignore it; the array
 *  they live in has one type either way. */
type StepProps = { onOpenSettings: (s: SettingsSection) => void }

/** Where a step sends somebody who wants the full version. */
function More({ to, onOpenSettings }: { to: SettingsSection; onOpenSettings: (s: SettingsSection) => void }) {
  return (
    <button
      type="button"
      data-testid={`tour-to-${to}`}
      data-tour-settings={to}
      onClick={() => onOpenSettings(to)}
      className="vp-press mt-3 rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink-2 transition-colors duration-200 ease-vp hover:bg-surface-2 hover:text-ink"
    >
      {t('tour.inSettings')}
    </button>
  )
}

/**
 * Being told when an agent wants you.
 *
 * The permission has to be asked for from a gesture, so it is asked for here
 * rather than announced -- a step that says "you can turn on notifications"
 * and leaves you to find the switch is a step that does nothing.
 *
 * The second line is the part that is easy to leave out and then gets reported
 * as a bug. A browser notification is raised by this page, so the page has to
 * be running to notice; a phone that has frozen the tab hears nothing, and
 * there is nothing the panel can do about that from inside the tab. The
 * webhook is the mechanism that reaches a phone which is not looking.
 */
function Notifications({ onOpenSettings }: StepProps) {
  const [perm, setPerm] = useState(
    typeof Notification === 'undefined' ? 'denied' : Notification.permission,
  )
  return (
    <>
      <H>{t('tour.notifyH')}</H>
      <P>{t('tour.notify1')}</P>
      <P>{t('tour.notify2')}</P>
      {perm === 'granted' ? (
        <p className="mt-3 flex items-center gap-2 text-vp-base text-state-done">
          <Check size={14} /> {t('tour.on')}
        </p>
      ) : (
        <button
          type="button"
          data-testid="tour-notify-on"
          disabled={typeof Notification === 'undefined'}
          onClick={() => void requestNotifyPermission().then(setPerm)}
          className="vp-press mt-3 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
          style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
        >
          {t('tour.turnOn')}
        </button>
      )}
      <More to="webhooks" onOpenSettings={onOpenSettings} />
    </>
  )
}

/**
 * Whether the connection is encrypted, answered by the connection.
 *
 * `location.protocol` and not the panel's own TLS mode. Behind a proxy that
 * terminates TLS the panel is serving plaintext and the browser is on https,
 * and a step that read the server's setting would tell somebody with a
 * perfectly good deployment to go and fix it. This is the same rule the
 * passkey check had to learn.
 */
function Encryption({ onOpenSettings }: StepProps) {
  const secure = typeof window === 'undefined' || window.location.protocol === 'https:'
  return (
    <>
      <H>{t('tour.tlsH')}</H>
      {secure ? (
        <>
          <p className="flex items-center gap-2 text-vp-base text-state-done">
            <Check size={14} /> {t('tour.tlsOn')}
          </p>
          <P>{t('tour.tlsOnWhy')}</P>
        </>
      ) : (
        <>
          <P>{t('tour.tlsOff')}</P>
          <P>{t('tour.tlsHow')}</P>
          <More to="env" onOpenSettings={onOpenSettings} />
        </>
      )}
    </>
  )
}

function FirstProject() {
  return (
    <>
      <H>{t('tour.projectH')}</H>
      <P>{t('tour.project1')}</P>
      <P>{t('tour.project2')}</P>
    </>
  )
}

function WhereTheRestIs() {
  return (
    <>
      <H>{t('tour.restH')}</H>
      <P>{t('tour.rest1')}</P>
      <P>{t('tour.rest2')}</P>
    </>
  )
}
