import { useCallback, useEffect, useRef, useState } from 'react'
import { Download, ExternalLink, RefreshCw } from 'lucide-react'

import { api, UpdateRefusedError } from '../protocol/api'
import type { UpdateStatus } from '../protocol/wire'
import { t, useLang } from '../i18n'
import { askConfirm } from './ask'
import { showToast } from './toasts'
import { safeText } from './text'
import { formatAgo } from './panels/ago'
import { parseReleaseNotes, type Run } from './releaseNotes'
import { afterRefusal, initialElevate, type ElevateState } from './updateElevate'
import { canApply, jobActive, jobLine, releaseDate, statusLine, type Line } from './updateView'
import { waitForItToComeBack } from './settings/comeback'

/**
 * Updating the panel from the panel.
 *
 * What is on screen is the server's account of things, polled: what it last
 * heard from GitHub and when, and the one apply in progress. The page holds no
 * state of its own about the update, because the first version did and a page
 * reloaded mid-download came back knowing nothing while the download went on
 * without it. Now a reload shows the same bar.
 *
 * The confirmation says the part people actually worry about before it is
 * pressed rather than after — the sessions do not restart with the panel,
 * because they belong to tmux and both units set KillMode=process. That is the
 * whole premise of the project, and it is exactly the thing somebody hesitating
 * over a button labelled "restart" needs told.
 */
export function UpdateSection() {
  const lang = useLang()
  const [st, setSt] = useState<UpdateStatus | null>(null)
  const [busy, setBusy] = useState<'' | 'check' | 'apply' | 'setting'>('')
  // Held for as long as the form is open and no longer. Never put anywhere
  // that outlives the page: not localStorage, not a URL, not a log.
  const [secret, setSecret] = useState('')
  // Button or field, whose password, or neither. Set from the status and
  // moved by what sudo says; see updateElevate. Once sudo has spoken the
  // status no longer overrides it, or a poll would put the button back over
  // a field sudo just asked for.
  const [elev, setElev] = useState<ElevateState>(initialElevate(false, ''))
  const elevTouched = useRef(false)
  // After the job reaches "restarting": back, or not back within the budget.
  // The wait itself is started once, and the ref is what says so.
  const [comeback, setComeback] = useState<'' | 'back' | 'notBack'>('')
  const waiting = useRef(false)
  const wasBuild = useRef<string | null>(null)
  // The clock, read when the status is, so "last checked 3 minutes ago"
  // moves with the poll rather than with every render.
  const [now, setNow] = useState(0)

  const take = useCallback((next: UpdateStatus) => {
    setSt(next)
    setNow(Math.floor(Date.now() / 1000))
    if (!elevTouched.current) setElev(initialElevate(next.elevateNoPassword, next.elevateAs))
  }, [])

  const refresh = useCallback(async () => {
    try {
      take(await api.updateStatus())
    } catch {
      // The poll is the page's own; a failed one is the next one's problem.
      // The socket banner already says when the panel is unreachable.
    }
  }, [take])

  const active = jobActive(st?.job)

  // The build that is running, so the wait after a restart can tell a new
  // process from the old one answering. Asked once, up front: by the time
  // the job says "restarting" the old process may already be gone.
  useEffect(() => {
    void api
      .health()
      .then((h) => {
        wasBuild.current = `${h.version}@${h.commit}`
      })
      .catch(() => {})
  }, [])

  // Polled: once a second while something is happening, once a minute
  // otherwise. The minute is for a second tab, or the auto-check landing an
  // answer while this dialog is open; it costs nothing on the network,
  // because the status endpoint answers from memory.
  useEffect(() => {
    let ignore = false
    const load = () =>
      api
        .updateStatus()
        .then((s) => {
          if (!ignore) take(s)
        })
        .catch(() => {})
    void load()
    const timer = window.setInterval(() => void load(), active ? 1000 : 60_000)
    return () => {
      ignore = true
      clearInterval(timer)
    }
  }, [active, take])

  // Once the job says the panel is restarting, wait for it to come back and
  // reload into it. The websocket and every cached snapshot belong to the
  // process that just went away.
  const stage = st?.job?.stage
  useEffect(() => {
    if (stage !== 'restarting' || waiting.current) return
    waiting.current = true
    waitForItToComeBack({
      was: wasBuild.current,
      onBack: () => {
        setComeback('back')
        window.setTimeout(() => window.location.reload(), 400)
      },
      onGaveUp: () => setComeback('notBack'),
    })
  }, [stage])

  const check = async () => {
    setBusy('check')
    try {
      take(await api.checkUpdate())
    } catch (e) {
      showToast({ kind: 'error', key: 'upd.unreachable', detail: msg(e) })
    } finally {
      setBusy('')
    }
  }

  const setAutoCheck = async (on: boolean) => {
    setBusy('setting')
    // Optimistic, so the box moves under the finger; the poll corrects it if
    // the write did not land.
    setSt((prev) => (prev ? { ...prev, autoCheck: on } : prev))
    try {
      await api.setUpdateAutoCheck(on)
    } catch (e) {
      showToast({ kind: 'error', key: 'upd.saveFailed', detail: msg(e) })
      void refresh()
    } finally {
      setBusy('')
    }
  }

  const apply = async (secret?: string) => {
    if (!st?.version) return
    if (
      !(await askConfirm({
        title: t('upd.confirmTitle', { v: st.version }),
        body: t('upd.confirmBody', { v: st.version }),
        confirm: t('upd.apply'),
        cancel: t('ask.cancel'),
      }))
    )
      return
    setBusy('apply')
    try {
      const res = await api.applyUpdate(st.version, secret)
      // Cleared on the way out and not in `finally`: a wrong one has to stay
      // in the box, or correcting a typo means typing the whole thing again.
      setSecret('')
      if (res.job) {
        const job = res.job
        setSt((prev) => (prev ? { ...prev, job } : prev))
      }
      if (res.elevated) {
        // Authorised rather than installed. There is no version to report --
        // the installer is running behind this and ends by restarting the
        // unit -- so claiming one would be the page inventing a number.
        showToast({ kind: 'success', key: 'upd.elevated' })
      }
    } catch (e) {
      if (e instanceof UpdateRefusedError) {
        if (e.reason === 'busy') {
          showToast({ kind: 'info', key: 'upd.busy' })
          void refresh()
          return
        }
        if (e.reason === 'changed') {
          // What was confirmed is no longer what would be installed. Check
          // again, show the new one, and ask again.
          showToast({ kind: 'info', key: 'upd.changed' })
          void check()
          return
        }
        elevTouched.current = true
        const next = afterRefusal(elev, e.reason, e.askedFor, st.elevateAs)
        setElev(next.state)
        showToast({ kind: 'error', key: next.key, params: next.params, detail: e.message })
        return
      }
      showToast({ kind: 'error', key: 'upd.failed', detail: msg(e) })
    } finally {
      setBusy('')
    }
  }

  const line = st ? statusLine(st) : null
  const job = st?.job
  const jobSaid = job ? jobLine(job) : null
  const showRelease = Boolean(st?.newer && st.version)
  const offerElevate = Boolean(st?.byHand && st.newer && st.elevate && !elev.blocked && !active)
  const lastChecked =
    st?.checking && busy !== 'check'
      ? t('upd.checking')
      : st?.checkedAt
        ? t('upd.lastChecked', { when: formatAgo(st.checkedAt, now) })
        : t('upd.neverChecked')

  return (
    <div data-testid="update-section">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-vp-base text-ink" data-testid="update-current">
          <span className="text-ink-2">{t('upd.current')}</span>{' '}
          <span className="font-mono">{safeText(st?.current ?? '')}</span>
          <span className="ml-2 text-vp-sm text-ink-2" data-testid="update-checked">
            {lastChecked}
          </span>
        </div>
        <button
          type="button"
          onClick={() => void check()}
          disabled={busy !== '' || active}
          data-testid="update-check"
          className="vp-press flex items-center gap-1.5 rounded-vp border border-hairline px-3 py-1.5 text-vp-base text-ink hover:bg-surface-2 disabled:opacity-50"
        >
          <RefreshCw size={13} className={busy === 'check' ? 'animate-spin' : ''} />
          {busy === 'check' ? t('upd.checking') : t('upd.check')}
        </button>
      </div>

      <label className="mt-2 flex items-start gap-2 text-vp-base text-ink">
        <input
          type="checkbox"
          className="mt-1"
          checked={st?.autoCheck ?? true}
          disabled={!st || busy === 'setting'}
          onChange={(e) => void setAutoCheck(e.target.checked)}
          data-testid="update-auto-check"
        />
        <span>
          {t('upd.autoCheck')}
          <span className="block text-vp-sm leading-relaxed text-ink-2">{t('upd.autoCheckHint')}</span>
        </span>
      </label>

      {line && !showRelease && (
        <p className="mt-2 text-vp-sm leading-relaxed text-ink-2" data-testid="update-status">
          <Said line={line} />
        </p>
      )}

      {st && showRelease && (
        <div className="mt-3 rounded-vp border border-hairline p-3" data-testid="update-release">
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className="text-vp-md font-medium text-ink" data-testid="update-status">
              {line && <Said line={line} />}
            </span>
            {st.publishedAt && (
              <span className="text-vp-sm text-ink-2">
                {t('upd.published', { date: releaseDate(st.publishedAt, lang) })}
              </span>
            )}
            {st.url?.startsWith('https://github.com/') && (
              <a
                href={st.url}
                target="_blank"
                rel="noreferrer noopener"
                className="flex items-center gap-1 text-vp-sm text-ink-2 underline-offset-2 hover:text-ink hover:underline"
                data-testid="update-release-link"
              >
                <ExternalLink size={12} />
                {t('upd.openRelease')}
              </a>
            )}
          </div>

          {/* The job, while there is one: a sentence, and a bar during the
              download. The bar is decoration on the sentence, never the only
              carrier -- the text says the bytes. */}
          {job && jobSaid && (
            <div className="mt-3" data-testid="update-job" data-stage={job.stage}>
              <p
                className="text-vp-base leading-relaxed text-ink"
                style={job.stage === 'failed' ? { color: 'var(--vp-state-crashed)' } : undefined}
              >
                <Said line={jobSaid} />
              </p>
              {jobSaid.progress !== undefined && (
                <div
                  role="progressbar"
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.round(jobSaid.progress * 100)}
                  className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-surface-2"
                >
                  <div
                    className="h-full rounded-full transition-[width] duration-300 ease-vp"
                    style={{ width: `${Math.round(jobSaid.progress * 100)}%`, background: 'var(--vp-accent)' }}
                  />
                </div>
              )}
              {comeback === 'back' && (
                <p className="mt-1 text-vp-sm text-ink-2" data-testid="update-back">
                  {t('upd.back')}
                </p>
              )}
              {comeback === 'notBack' && (
                <p className="mt-1 text-vp-sm text-ink-2" data-testid="update-not-back">
                  {t('upd.notBack')}
                </p>
              )}
            </div>
          )}

          {canApply(st) && (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={() => void apply()}
                disabled={busy !== ''}
                data-testid="update-apply"
                className="vp-press flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
                style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
              >
                <Download size={13} />
                {job?.stage === 'failed' ? t('upd.retry') : t('upd.apply')}
              </button>
            </div>
          )}

          {/* The panel cannot replace a binary it does not own, and it does not
              hold the credential that could. What it can do is take one, typed
              here, at a moment somebody chose, and spend it on one fixed command.

              That is not the same as a console that can escalate, and the
              difference is worth stating: anybody who can reach this page holds a
              session on a panel whose purpose is running commands as this account,
              and can type the same thing into a terminal in the next tab. What
              this removes is a window switch. */}
          {offerElevate && !elev.field && (
            // sudo will run it without a password, so nothing is asked for: a
            // field here would collect something sudo never reads.
            <div data-testid="update-elevate" className="mt-3 flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={() => void apply()}
                disabled={busy !== ''}
                data-testid="update-elevate-apply"
                className="vp-press flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
                style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
              >
                <Download size={13} />
                {t('upd.apply')}
              </button>
              <span className="text-vp-sm text-ink-2" data-testid="update-no-password">
                {t('upd.noPassword')}
              </span>
            </div>
          )}
          {offerElevate && elev.field && (
            <form
              data-testid="update-elevate"
              onSubmit={(e) => {
                e.preventDefault()
                void apply(secret)
              }}
              className="mt-3 flex flex-wrap items-center gap-2"
            >
              <input
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                // Named, because "this account" was read as the panel account --
                // the one the person is signed into on this very page -- and sudo
                // wants the machine's. Whose, as sudo said after a refusal --
                // root under rootpw -- and until then the account the panel
                // runs as.
                placeholder={t('upd.secretHint', { user: elev.who })}
                autoComplete="current-password"
                data-testid="update-secret"
                className="min-w-0 flex-1 rounded-vp border border-hairline bg-surface-2 px-2 py-1.5 text-vp-md text-ink outline-none focus:border-accent"
              />
              <button
                type="submit"
                disabled={busy !== '' || secret === ''}
                data-testid="update-elevate-apply"
                className="vp-press flex items-center gap-1.5 rounded-vp px-3 py-1.5 text-vp-base font-medium disabled:opacity-50"
                style={{ background: 'var(--vp-accent)', color: 'var(--vp-accent-ink)' }}
              >
                <Download size={13} />
                {t('upd.apply')}
              </button>
            </form>
          )}
          {st.byHand && st.cannotElevate && (
            <p className="mt-3 text-vp-sm leading-relaxed text-ink-2" data-testid="update-cannot-elevate">
              {t('upd.cannotElevate')}
            </p>
          )}
          {st.byHand && !active && (
            <div className="mt-3 text-vp-sm leading-relaxed text-ink-2" data-testid="update-by-hand">
              {/* The server's sentence, which names the command. Kept even when
                  the field above is offered: a machine where the credential is
                  refused still needs the shell to be a way through, and it is the
                  only line here that survives that. Shown as it arrived -- it
                  contains a path this side does not know. */}
              <span className="mr-1">{t('upd.byHand')}</span>
              <code className="font-mono break-all text-ink">{safeText(byHandCommand(st.byHand))}</code>
            </div>
          )}

          {/* The notes come from a release page and are somebody else's text,
              so they are drawn as text and never as markup; see releaseNotes. */}
          {st.notes && (
            <details className="mt-3" open={!job}>
              <summary className="cursor-pointer text-vp-sm text-ink-2">{t('upd.notes')}</summary>
              <div
                className="mt-2 max-h-72 overflow-auto rounded-vp bg-surface-2 px-3 py-2 text-vp-sm leading-relaxed text-ink-2"
                data-testid="update-notes"
              >
                <Notes body={st.notes} />
              </div>
            </details>
          )}
        </div>
      )}
    </div>
  )
}

/** A sentence of the panel's, with the server's text after it. */
function Said({ line }: { line: Line }) {
  return (
    <>
      {t(line.key, line.params)}
      {line.detail && (
        <span className="ml-1 font-mono text-vp-xs break-all text-ink-2" data-testid="update-detail">
          {safeText(line.detail)}
        </span>
      )}
    </>
  )
}

/**
 * The command out of the server's byHand sentence, which is
 * `this panel cannot replace its own binary: …. Update it from a shell
 * instead: <command>`. The page says the sentence in its own language and
 * shows the command as it arrived.
 */
function byHandCommand(byHand: string): string {
  const i = byHand.lastIndexOf('instead: ')
  return i >= 0 ? byHand.slice(i + 'instead: '.length) : byHand
}

function Notes({ body }: { body: string }) {
  const blocks = parseReleaseNotes(body)
  return (
    <>
      {blocks.map((b, i) => {
        switch (b.kind) {
          case 'heading': {
            const cls =
              b.level === 1
                ? 'mt-2 text-vp-md font-semibold text-ink first:mt-0'
                : b.level === 2
                  ? 'mt-2 text-vp-base font-semibold text-ink first:mt-0'
                  : 'mt-2 text-vp-sm font-semibold text-ink first:mt-0'
            return (
              <p key={i} className={cls} role="heading" aria-level={b.level + 3}>
                <Runs runs={b.runs} />
              </p>
            )
          }
          case 'list':
            return (
              <ul key={i} className="my-1 list-disc pl-5">
                {b.items.map((runs, j) => (
                  <li key={j}>
                    <Runs runs={runs} />
                  </li>
                ))}
              </ul>
            )
          case 'code':
            return (
              <pre key={i} className="my-1 overflow-auto rounded-vp border border-hairline bg-surface p-2 font-mono text-vp-xs text-ink">
                {safeText(b.text)}
              </pre>
            )
          default:
            return (
              <p key={i} className="my-1">
                <Runs runs={b.runs} />
              </p>
            )
        }
      })}
    </>
  )
}

function Runs({ runs }: { runs: Run[] }) {
  return (
    <>
      {runs.map((r, i) =>
        r.kind === 'bold' ? (
          <strong key={i} className="font-semibold text-ink">
            {safeText(r.text)}
          </strong>
        ) : r.kind === 'code' ? (
          <code key={i} className="font-mono text-vp-xs text-ink">
            {safeText(r.text)}
          </code>
        ) : (
          <span key={i}>{safeText(r.text)}</span>
        ),
      )}
    </>
  )
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
